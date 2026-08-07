package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearProviderKeys removes every signal that makes a provider available, so a
// test states its own availability and nothing else does. The local endpoint
// variables are cleared alongside the API keys because local availability is
// now decided the same way a keyed provider's is — by configuration — and a
// developer machine that happens to run an inference server would otherwise
// hand these tests a candidate the assertions never asked for.
func clearProviderKeys(t *testing.T) {
	t.Helper()

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GROK_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("NVIDIA_API_KEY", "")
	t.Setenv("LOCAL_INFERENCE_URL", "")
	t.Setenv("LOCAL_MODEL_NAME", "")

	// The discovered-model registry is process-global and outlives the test
	// that filled it, so a cache left behind by an earlier test decides which
	// models a later one can select from.
	ResetModelRegistryCache()
	t.Cleanup(ResetModelRegistryCache)
}

// withLocalInference declares a local inference endpoint for the duration of a
// test, which is what makes the local provider a selection candidate.
func withLocalInference(t *testing.T) {
	t.Helper()
	t.Setenv("LOCAL_INFERENCE_URL", "http://127.0.0.1:11434/v1")
}

// chdirWithoutTendrilConfig moves into an empty temporary directory so that
// loadTendrilConfig finds nothing. This repository ships its own
// .tendril/config.yaml pinning a local model, and a test run from the source
// tree silently reads it — which is how a resolution test can pass while
// asserting nothing about resolution.
func chdirWithoutTendrilConfig(t *testing.T) {
	t.Helper()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
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
	// No cost ceiling means no cost constraint, so the best reasoning model
	// OpenAI serves is the answer.
	if model.Name != "gpt-5.6-terra" {
		t.Fatalf("model.Name = %q, want %q", model.Name, "gpt-5.6-terra")
	}
}

func TestSelectBestModelFiltersContextAndCost(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	// A one-million-token context requirement excludes claude-haiku-4-5
	// (200K); under a premium ceiling the best remaining match is the
	// premium-tier claude-opus-4-8.
	model, err := SelectBestModel(Capabilities{
		MinContextSize: 1000000,
		MaxCostTier:    TierPremium,
	})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if model.Provider != "anthropic" || model.Name != "claude-opus-4-8" {
		t.Fatalf("model = %#v, want anthropic claude-opus-4-8", model)
	}

	// The ceiling still excludes: lowering it to standard drops opus and
	// leaves the standard-tier model that also holds the context.
	model, err = SelectBestModel(Capabilities{
		MinContextSize: 1000000,
		MaxCostTier:    TierStandard,
	})
	if err != nil {
		t.Fatalf("SelectBestModel(standard ceiling) failed: %v", err)
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
	if model.Name != "claude-opus-4-8" {
		t.Fatalf("model.Name = %q, want claude-opus-4-8", model.Name)
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

// With only a local inference endpoint declared, the models on offer include
// several that cannot drive tools. RequiresToolUse must skip them and select
// the one local model that can. This is the fix for a no-session sprout
// silently landing on a model that returns empty completions.
//
// Nothing but the endpoint is configured, on purpose. Selection reached through
// a provider's own model pin is a different path, and a guard that only holds
// when a model is pinned does not hold for the setup where a small local model
// is what is available.
func TestSelectBestModelRequiresToolUseSkipsNonDrivers(t *testing.T) {
	clearProviderKeys(t)
	withLocalInference(t)

	generic, err := SelectBestModel(Capabilities{MaxCostTier: TierCheapest})
	if err != nil {
		t.Fatalf("SelectBestModel failed: %v", err)
	}
	if generic.Name != "llama3.2" {
		t.Fatalf("under a cheapest ceiling, local selection = %q, want llama3.2 (the model RequiresToolUse must reject)", generic.Name)
	}
	if generic.DrivesTools {
		t.Fatalf("llama3.2 is registered as driving tools; this test no longer proves anything")
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
