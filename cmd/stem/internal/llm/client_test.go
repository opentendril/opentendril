package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCoordinatorProviderSpecUsesDefaultProviderFallback(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("OPENAI_MODEL_NAME", "gpt-worker")
	t.Setenv("OPENAI_API_KEY", "worker-key")
	t.Setenv("COORDINATOR_LLM_PROVIDER", "")
	t.Setenv("COORDINATOR_MODEL_NAME", "gpt-coordinator")
	t.Setenv("COORDINATOR_LOCAL_INFERENCE_URL", "")

	spec := ResolveCoordinatorProviderSpec()

	if spec.Provider != "openai" {
		t.Fatalf("spec.Provider = %q, want %q", spec.Provider, "openai")
	}
	if spec.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("spec.BaseURL = %q, want %q", spec.BaseURL, "https://api.openai.com/v1")
	}
	if spec.Model != "gpt-coordinator" {
		t.Fatalf("spec.Model = %q, want %q", spec.Model, "gpt-coordinator")
	}
	if spec.APIKey != "worker-key" {
		t.Fatalf("spec.APIKey = %q, want %q", spec.APIKey, "worker-key")
	}
	if spec.Endpoint != "/chat/completions" {
		t.Fatalf("spec.Endpoint = %q, want %q", spec.Endpoint, "/chat/completions")
	}
	if spec.Mode != ModeOpenAIish {
		t.Fatalf("spec.Mode = %q, want %q", spec.Mode, ModeOpenAIish)
	}
}

func TestResolveCoordinatorProviderSpecUsesExplicitCoordinatorLocalSettings(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "worker-key")
	t.Setenv("COORDINATOR_LLM_PROVIDER", "local")
	t.Setenv("COORDINATOR_MODEL_NAME", "qwen2.5:1.5b-instruct")
	t.Setenv("COORDINATOR_LOCAL_INFERENCE_URL", "http://coordinator:11434/v1")

	spec := ResolveCoordinatorProviderSpec()

	if spec.Provider != "local" {
		t.Fatalf("spec.Provider = %q, want %q", spec.Provider, "local")
	}
	if spec.BaseURL != "http://coordinator:11434/v1" {
		t.Fatalf("spec.BaseURL = %q, want %q", spec.BaseURL, "http://coordinator:11434/v1")
	}
	if len(spec.BaseURLs) == 0 || spec.BaseURLs[0] != "http://coordinator:11434/v1" {
		t.Fatalf("spec.BaseURLs = %#v, want to start with coordinator URL", spec.BaseURLs)
	}
	if spec.Model != "qwen2.5:1.5b-instruct" {
		t.Fatalf("spec.Model = %q, want %q", spec.Model, "qwen2.5:1.5b-instruct")
	}
	if spec.Endpoint != "/chat/completions" {
		t.Fatalf("spec.Endpoint = %q, want %q", spec.Endpoint, "/chat/completions")
	}
	if spec.Mode != ModeOpenAIish {
		t.Fatalf("spec.Mode = %q, want %q", spec.Mode, ModeOpenAIish)
	}
}

func TestListModelsUsesOpenAICompatibleEndpointWithoutAPIKey(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s, want /v1/models", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "llama3.2"},
				{"id": "qwen2.5-coder:7b"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider: "local",
		BaseURL:  server.URL + "/v1",
		Endpoint: "/chat/completions",
		Mode:     ModeOpenAIish,
	})

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if authHeader != "" {
		t.Fatalf("Authorization header = %q, want empty", authHeader)
	}
	if len(models) != 2 || models[0] != "llama3.2" || models[1] != "qwen2.5-coder:7b" {
		t.Fatalf("models = %#v, want llama3.2 and qwen2.5-coder:7b", models)
	}
}

func TestResolveLocalProviderSpecUsesTendrilConfig(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "")
	t.Setenv("LOCAL_INFERENCE_URL", "")
	t.Setenv("LOCAL_MODEL_NAME", "")
	t.Setenv("DEFAULT_MODEL_NAME", "")

	root := t.TempDir()
	tendrilDir := filepath.Join(root, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir .tendril: %v", err)
	}
	config := []byte(`
llm:
  default-provider: local
  providers:
    local:
      base-url: http://localhost:11434/v1
      model: qwen2.5-coder:7b
      temperature: 0.2
`)
	if err := os.WriteFile(filepath.Join(tendrilDir, "config.yaml"), config, 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousDir)
	})

	spec := ResolveProviderSpec()

	if spec.Provider != "local" {
		t.Fatalf("spec.Provider = %q, want local", spec.Provider)
	}
	if spec.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("spec.BaseURL = %q, want configured URL", spec.BaseURL)
	}
	if spec.Model != "qwen2.5-coder:7b" {
		t.Fatalf("spec.Model = %q, want configured model", spec.Model)
	}
	if spec.Temperature != 0.2 {
		t.Fatalf("spec.Temperature = %v, want 0.2", spec.Temperature)
	}
}
