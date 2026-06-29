package llm

import "testing"

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
