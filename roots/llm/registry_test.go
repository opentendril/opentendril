package llm

import "testing"

func clearProviderKeys(t *testing.T) {
	t.Helper()

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENTENDRIL_API_KEY", "")
	t.Setenv("NVIDIA_API_KEY", "")
}

func TestSelectBestModelUsesOnlyAvailableProviders(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "openai-key")

	model, err := SelectBestModel(Capabilities{
		RequiresVision: true,
		MaxCostTier:    TierCheapest,
	})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if model.Provider != "openai" {
		t.Fatalf("model.Provider = %q, want %q", model.Provider, "openai")
	}
	if model.Name != "gpt-4o-mini" {
		t.Fatalf("model.Name = %q, want %q", model.Name, "gpt-4o-mini")
	}
}

func TestSelectBestModelFiltersCapabilities(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "openai-key")

	model, err := SelectBestModel(Capabilities{RequiresReasoning: true})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if !model.HasReasoning {
		t.Fatalf("selected model %#v without reasoning", model)
	}
	if model.Name != "o1-mini" {
		t.Fatalf("model.Name = %q, want %q", model.Name, "o1-mini")
	}
}

func TestSelectBestModelFiltersContextAndCost(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("GOOGLE_API_KEY", "google-key")

	model, err := SelectBestModel(Capabilities{
		MinContextSize: 2000000,
		MaxCostTier:    TierPremium,
	})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if model.Provider != "google" || model.Name != "gemini-1.5-pro" {
		t.Fatalf("model = %#v, want google gemini-1.5-pro", model)
	}

	_, err = SelectBestModel(Capabilities{
		MinContextSize: 2000000,
		MaxCostTier:    TierCheapest,
	})
	if err == nil {
		t.Fatalf("SelectBestModel succeeded, want error")
	}
}

// With only the always-available local provider, the cheapest model is
// llama3.2 — which cannot drive tools. RequiresToolUse must skip it (and the
// coder models) and select the one local model that can. This is the fix for a
// no-session sprout silently landing on a model that returns empty completions.
func TestSelectBestModelRequiresToolUseSkipsNonDrivers(t *testing.T) {
	clearProviderKeys(t)

	generic, err := SelectBestModel(Capabilities{MaxCostTier: TierPremium})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if generic.Name != "llama3.2" {
		t.Fatalf("without RequiresToolUse, cheapest local = %q, want llama3.2 (documents the default that was broken)", generic.Name)
	}

	toolCapable, err := SelectBestModel(Capabilities{MaxCostTier: TierPremium, RequiresToolUse: true})
	if err != nil {
		t.Fatalf("SelectBestModel(RequiresToolUse) failed: %v", err)
	}
	if !toolCapable.DrivesTools {
		t.Fatalf("selected model %#v does not drive tools", toolCapable)
	}
	if toolCapable.Provider != "local" || toolCapable.Name != "qwen3.5:9b" {
		t.Fatalf("tool-capable local selection = %s/%s, want local/qwen3.5:9b", toolCapable.Provider, toolCapable.Name)
	}
}
