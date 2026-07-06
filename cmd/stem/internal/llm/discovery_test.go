package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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