package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearProviderKeys(t *testing.T) {
	t.Helper()

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("NVIDIA_API_KEY", "")
}

// chdirWithTendrilConfig writes the given YAML to a temporary .tendril tree and
// moves into it, so loadTendrilConfig finds it, restoring the original working
// directory afterwards. Same shape as TestIsRouterConfigOverride's local helper.
func chdirWithTendrilConfig(t *testing.T, yaml string) {
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
	if model.Name != "gpt-5.6-luna" {
		t.Fatalf("model.Name = %q, want %q", model.Name, "gpt-5.6-luna")
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
	if model.Name != "gpt-5.6-luna" {
		t.Fatalf("model.Name = %q, want %q", model.Name, "gpt-5.6-luna")
	}
}

func TestSelectBestModelFiltersContextAndCost(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	// A one-million-token context requirement excludes claude-haiku-4-5
	// (200K); the cheapest remaining match under a premium cap is the
	// standard-tier claude-sonnet-5.
	model, err := SelectBestModel(Capabilities{
		MinContextSize: 1000000,
		MaxCostTier:    TierPremium,
	})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if model.Provider != "anthropic" || model.Name != "claude-sonnet-5" {
		t.Fatalf("model = %#v, want anthropic claude-sonnet-5", model)
	}

	_, err = SelectBestModel(Capabilities{
		MinContextSize: 1000000,
		MaxCostTier:    TierCheapest,
	})
	if err == nil {
		t.Fatalf("SelectBestModel succeeded, want error")
	}
}

// The fallback registry must offer current-generation, provider-served model
// names: a retired name (for example claude-3-5-sonnet) means every
// auto-selected request fails at the provider with a model-not-found error.
func TestFallbackRegistryServesCurrentGenerationAnthropic(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	model, err := SelectBestModel(Capabilities{
		RequiresToolUse:   true,
		RequiresVision:    true,
		RequiresReasoning: true,
	})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if model.Provider != "anthropic" {
		t.Fatalf("model.Provider = %q, want anthropic", model.Provider)
	}
	if model.Name != "claude-sonnet-5" {
		t.Fatalf("model.Name = %q, want claude-sonnet-5", model.Name)
	}

	for _, entry := range FallbackModels {
		if entry.Provider != "anthropic" {
			continue
		}
		if !entry.DrivesTools {
			t.Fatalf("anthropic fallback %q must drive tools", entry.Name)
		}
		if strings.HasPrefix(entry.Name, "claude-3") {
			t.Fatalf("anthropic fallback %q is a retired generation", entry.Name)
		}
	}
}

// With only the always-available local provider, the cheapest model is
// llama3.2 — which cannot drive tools. RequiresToolUse must skip it (and the
// coder models) and select the one local model that can. This is the fix for a
// no-session sprout silently landing on a model that returns empty completions.
//
// It runs with no configuration on purpose. Selection reached through a
// provider's own config is a different path, and a guard that only holds when
// something is configured does not hold for the setup an operator gets by
// default — which is the setup where a 3B local model is what is available.
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

// Whether an endpoint will take a tools field says nothing about whether the
// model behind it drives them — a 3B llama on Ollama accepts the field and
// still answers in prose. Selection must reach the same model either way, or a
// config key silently switches off a measurement.
func TestAcceptsToolDefinitionsDoesNotChangeSelection(t *testing.T) {
	for _, accepts := range []string{"true", "false"} {
		t.Run("accepts-tool-definitions "+accepts, func(t *testing.T) {
			clearProviderKeys(t)
			chdirWithTendrilConfig(t, `
llm:
  providers:
    local:
      accepts-tool-definitions: `+accepts+`
`)

			selected, err := SelectBestModel(Capabilities{MaxCostTier: TierPremium, RequiresToolUse: true})
			if err != nil {
				t.Fatalf("SelectBestModel(RequiresToolUse) failed: %v", err)
			}
			if !selected.DrivesTools {
				t.Fatalf("selected %s/%s, which does not drive tools", selected.Provider, selected.Name)
			}
		})
	}
}
