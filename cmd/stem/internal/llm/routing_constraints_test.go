package llm

import "testing"

func TestIsThirdPartyRouterModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "openrouter auto", model: "openrouter/auto", want: true},
		{name: "bare auto", model: "auto", want: true},
		{name: "nvidia router", model: "nvidia/llm-router", want: true},
		{name: "standard model", model: "gpt-4o-mini", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsThirdPartyRouterModel(tt.model); got != tt.want {
				t.Fatalf("IsThirdPartyRouterModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestShouldBypassInternalRouterWithPinnedModel(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
	clearTierModelEnv(t, "openai")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_PREMIUM_MODEL", "gpt-4o")

	if !ShouldBypassInternalRouter() {
		t.Fatalf("ShouldBypassInternalRouter() = false, want true for pinned premium model")
	}
}

func TestShouldBypassInternalRouterWithOpenRouterAuto(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "openrouter")
	clearTierModelEnv(t, "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("DEFAULT_MODEL_NAME", "openrouter/auto")

	if !ShouldBypassInternalRouter() {
		t.Fatalf("ShouldBypassInternalRouter() = false, want true for openrouter/auto")
	}
}

func TestShouldUseDynamicRouterRequiresMultipleOptions(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("GROK_API_KEY", "test-key")
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
	clearTierModelEnv(t, "openai")
	clearTierModelEnv(t, "grok")
	t.Setenv("DEFAULT_MODEL_NAME", "")

	registry := []ModelDefinition{
		{Provider: "openai", Name: "gpt-4o-mini", CostTier: TierCheapest},
		{Provider: "grok", Name: "grok-beta", CostTier: TierPremium},
	}
	if !ShouldUseDynamicRouter(registry) {
		t.Fatalf("ShouldUseDynamicRouter() = false, want true with multiple models")
	}

	single := []ModelDefinition{
		{Provider: "openai", Name: "gpt-4o-mini", CostTier: TierCheapest},
	}
	if ShouldUseDynamicRouter(single) {
		t.Fatalf("ShouldUseDynamicRouter() = true, want false with one model")
	}
}