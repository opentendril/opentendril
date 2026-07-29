package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoverAvailableModelsCachesResults(t *testing.T) {
	ResetModelRegistryCache()
	clearProviderKeys(t)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-4o-mini"},
			},
		})
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", server.URL)
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")

	originalClientFactory := modelDiscoveryClient
	modelDiscoveryClient = func(spec ProviderSpec) *Client {
		if spec.Provider == "openai" {
			spec.BaseURL = server.URL
		}
		return originalClientFactory(spec)
	}
	t.Cleanup(func() {
		modelDiscoveryClient = originalClientFactory
		ResetModelRegistryCache()
	})

	ctx := context.Background()
	first, err := DiscoverAvailableModels(ctx)
	if err != nil {
		t.Fatalf("DiscoverAvailableModels first call failed: %v", err)
	}
	if len(first) == 0 {
		t.Fatalf("expected discovered models, got none")
	}

	second, err := DiscoverAvailableModels(ctx)
	if err != nil {
		t.Fatalf("DiscoverAvailableModels second call failed: %v", err)
	}
	if requests != 1 {
		t.Fatalf("models API requests = %d, want 1 due to cache", requests)
	}
	if len(second) != len(first) {
		t.Fatalf("cached registry length = %d, want %d", len(second), len(first))
	}
}

func TestDiscoverAvailableModelsHandlesAPIFailuresGracefully(t *testing.T) {
	ResetModelRegistryCache()
	clearProviderKeys(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", server.URL)
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")

	originalClientFactory := modelDiscoveryClient
	modelDiscoveryClient = func(spec ProviderSpec) *Client {
		if spec.Provider == "openai" {
			spec.BaseURL = server.URL
		}
		return originalClientFactory(spec)
	}
	t.Cleanup(func() {
		modelDiscoveryClient = originalClientFactory
		ResetModelRegistryCache()
	})

	models, err := DiscoverAvailableModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverAvailableModels returned error: %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("expected fallback models after API failure")
	}

	foundOpenAI := false
	for _, model := range models {
		if model.Provider == "openai" {
			foundOpenAI = true
			break
		}
	}
	if !foundOpenAI {
		t.Fatalf("expected openai fallback models, got %#v", models)
	}
}

func TestGetModelRegistryUsesCacheWithinTTL(t *testing.T) {
	ResetModelRegistryCache()
	modelRegistryMu.Lock()
	modelRegistryCache = []ModelDefinition{
		{Provider: "openai", Name: "cached-model", CostTier: TierCheapest},
	}
	modelRegistryLoaded = time.Now()
	modelRegistryMu.Unlock()
	t.Cleanup(ResetModelRegistryCache)

	registry := GetModelRegistry(context.Background())
	if len(registry) != 1 || registry[0].Name != "cached-model" {
		t.Fatalf("GetModelRegistry() = %#v, want cached-model", registry)
	}
}

func TestConfiguredModelsOverride(t *testing.T) {
	chdir := func(t *testing.T, yaml string) {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".tendril"), 0o755); err != nil {
			t.Fatalf("mkdir .tendril: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".tendril", "config.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
		orig, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		if err := os.Chdir(root); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(orig) })
	}

	t.Run("fallbackModelsForProvider returns configured models, not merged", func(t *testing.T) {
		chdir(t, `
llm:
  providers:
    openai:
      models:
        - name: my-custom-gpt
          family: gpt
          cost-tier: premium
`)
		models := fallbackModelsForProvider("openai")
		if len(models) != 1 {
			t.Fatalf("got %d models, want 1", len(models))
		}
		if models[0].Name != "my-custom-gpt" {
			t.Fatalf("got %q, want my-custom-gpt", models[0].Name)
		}
	})

	t.Run("fallbackModelsForProvider falls back to FallbackModels when no config", func(t *testing.T) {
		chdir(t, `
llm:
  providers:
    openai:
      models: []
`)
		models := fallbackModelsForProvider("openai")
		if len(models) < 2 {
			t.Fatalf("expected compiled-in models, got %d", len(models))
		}
		foundDefault := false
		for _, m := range models {
			if m.Name == "gpt-5.6-luna" {
				foundDefault = true
				break
			}
		}
		if !foundDefault {
			t.Fatalf("did not find gpt-5.6-luna in fallback models")
		}
	})

	t.Run("enrichModelDefinition returns configured metadata or falls through", func(t *testing.T) {
		chdir(t, `
llm:
  providers:
    anthropic:
      models:
        - name: claude-custom
          context-size: 500
`)
		m1 := enrichModelDefinition("anthropic", "claude-custom")
		if m1.ContextSize != 500 {
			t.Fatalf("got context size %d, want 500", m1.ContextSize)
		}

		m2 := enrichModelDefinition("anthropic", "claude-3-5-sonnet-latest")
		if m2.ContextSize != 200000 {
			t.Fatalf("expected compiled-in fallback context size, got %d", m2.ContextSize)
		}
		if m2.Family != ModelFamilyClaude {
			t.Fatalf("expected family claude, got %q", m2.Family)
		}

		m3 := enrichModelDefinition("anthropic", "unknown-claude-9")
		if m3.Family != ModelFamilyClaude {
			t.Fatalf("expected inferred family claude, got %q", m3.Family)
		}
	})

	t.Run("configured model with only name gets fields filled by inferCapabilitiesFromName", func(t *testing.T) {
		chdir(t, `
llm:
  providers:
    anthropic:
      models:
        - name: claude-3-pro
`)
		models := fallbackModelsForProvider("anthropic")
		if len(models) != 1 {
			t.Fatalf("got %d models, want 1", len(models))
		}
		m := models[0]
		if m.Family != ModelFamilyClaude {
			t.Fatalf("inferred family %q, want claude", m.Family)
		}
		if m.CostTier != TierPremium {
			t.Fatalf("inferred tier %q, want premium", m.CostTier)
		}
		if m.ContextSize != 200000 {
			t.Fatalf("inferred context size %d, want 200000", m.ContextSize)
		}
	})

	t.Run("configured model with explicit family prevents heuristic overwrite", func(t *testing.T) {
		chdir(t, `
llm:
  providers:
    google:
      models:
        - name: some-gemini-name
          family: llama
`)
		models := fallbackModelsForProvider("google")
		if len(models) != 1 {
			t.Fatalf("got %d models, want 1", len(models))
		}
		m := models[0]
		if m.Family != ModelFamilyLlama {
			t.Fatalf("family %q, want llama (heuristic overwrote it)", m.Family)
		}
	})
}
