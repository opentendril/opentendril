package llm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestAnthropicRequestCarriesModelDeclaredOutputLimit asserts that the
// marshalled request body contains the registry-declared limit for the model,
// not a compile-time constant.
//
// Mutation: hardcode 2048 in BuildChatRequest → this test goes red.
func TestAnthropicRequestCarriesModelDeclaredOutputLimit(t *testing.T) {
	// claude-sonnet-5 declares OutputLimit: 128000 in FallbackModels.
	spec := ProviderSpec{
		Model:       "claude-sonnet-5",
		OutputLimit: 128000,
	}
	payload, err := anthropicAdapter{}.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	got, ok := body["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens missing or wrong type in payload: %v", body["max_tokens"])
	}
	if int(got) != 128000 {
		t.Errorf("max_tokens = %d, want 128000 (claude-sonnet-5 registry limit)", int(got))
	}
}

// TestAnthropicDifferentModelsDifferentOutputLimits asserts that two models
// with different declared limits produce different max_tokens values in their
// requests. A mutation that reads the limit from the wrong place (a package
// default or the first registry entry) would leave both values identical,
// which test 1 alone cannot detect.
//
// Mutation: read outputLimit from a fixed constant or the first registry entry
// instead of the spec → both values become the same → this test goes red.
func TestAnthropicDifferentModelsDifferentOutputLimits(t *testing.T) {
	// claude-opus-4-8 declares 128000, claude-haiku-4-5 declares 64000.
	cases := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-8", 128000},
		{"claude-haiku-4-5", 64000},
	}

	for _, tc := range cases {
		spec := ProviderSpec{
			Model:       tc.model,
			OutputLimit: tc.want,
		}
		payload, err := anthropicAdapter{}.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
		if err != nil {
			t.Fatalf("model %s: BuildChatRequest failed: %v", tc.model, err)
		}

		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("model %s: unmarshal: %v", tc.model, err)
		}

		got, ok := body["max_tokens"].(float64)
		if !ok {
			t.Fatalf("model %s: max_tokens missing or wrong type: %v", tc.model, body["max_tokens"])
		}
		if int(got) != tc.want {
			t.Errorf("model %s: max_tokens = %d, want %d", tc.model, int(got), tc.want)
		}
	}
}

// TestOutputLimitConfigOverridesRegistry asserts that a per-provider
// output-limit in config wins over the model registry when both are set and
// the configured value is within the declared limit.
//
// Mutation: prefer the registry value over config → this test goes red when
// the config value is smaller than the registry limit.
func TestOutputLimitConfigOverridesRegistry(t *testing.T) {
	clearProviderKeys(t)

	root := t.TempDir()
	tendrilDir := root + "/.tendril"
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// claude-haiku-4-5 has registry limit 64000; configure 16000 (within limit).
	cfg := []byte(`
llm:
  default-provider: anthropic
  providers:
    anthropic:
      model: claude-haiku-4-5
      output-limit: 16000
`)
	if err := os.WriteFile(tendrilDir+"/config.yaml", cfg, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	spec := providerSpecForModel("anthropic", TierPremium, "claude-haiku-4-5", "")

	payload, err := anthropicAdapter{}.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := body["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens missing or wrong type: %v", body["max_tokens"])
	}
	if int(got) != 16000 {
		t.Errorf("max_tokens = %d, want 16000 (config wins over registry 64000)", int(got))
	}
}

// TestOpenAIishRequestCarriesMaxTokens asserts that the OpenAI-shaped
// adapter adds max_tokens to its request body.
//
// Mutation: remove max_tokens from openAIishAdapter.BuildChatRequest → this test
// goes red.
func TestOpenAIishRequestCarriesMaxTokens(t *testing.T) {
	spec := ProviderSpec{
		Model:       "gpt-test",
		Temperature: ptr(0.5),
		OutputLimit: 8192,
	}
	payload, err := openAIishAdapter{}.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, present := body["max_tokens"]
	if !present {
		t.Errorf("max_tokens missing in OpenAI-shaped request body; it must carry the governed output limit")
	} else if int(got.(float64)) != 8192 {
		t.Errorf("max_tokens = %v, want 8192", got)
	}
}

// TestOutputLimitOverLargeIsReportedNotClamped asserts that a configured
// output-limit larger than the registry limit is sent as-is and that a warning
// is written to stderr. The assertion is on the stderr output and the sent
// value — not merely that the outgoing value is bounded — because a silent
// clamp also produces a bounded value, which is exactly the invisibility this
// issue is about, moved one layer up.
//
// Mutation: silently clamp to the registry limit instead of warning →
// the sent value check would still pass (both produce a bounded value), but
// the stderr assertion goes red because nothing was written.
func TestOutputLimitOverLargeIsReportedNotClamped(t *testing.T) {
	clearProviderKeys(t)

	root := t.TempDir()
	tendrilDir := root + "/.tendril"
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// claude-haiku-4-5 registry limit is 64000; configure 200000 (over limit).
	cfg := []byte(`
llm:
  default-provider: anthropic
  providers:
    anthropic:
      model: claude-haiku-4-5
      output-limit: 200000
`)
	if err := os.WriteFile(tendrilDir+"/config.yaml", cfg, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	// Capture stderr by redirecting os.Stderr temporarily.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	spec := providerSpecForModel("anthropic", TierPremium, "claude-haiku-4-5", "")

	w.Close()
	os.Stderr = origStderr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	stderrOut := string(buf[:n])

	// The configured value must be sent on the wire — config wins, operator
	// is warned, not silently overridden.
	payload, err := anthropicAdapter{}.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := body["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens missing or wrong type: %v", body["max_tokens"])
	}
	if int(got) != 200000 {
		t.Errorf("max_tokens = %d, want 200000 (config wins; operator is warned, not silently clamped)", int(got))
	}

	// A warning must have been written to stderr containing the configured value.
	if !strings.Contains(stderrOut, "200000") {
		t.Errorf("expected a warning about output-limit 200000 on stderr, got: %q", stderrOut)
	}
}

// TestUnsetEnvFallsBackToCompiledDefault asserts that a bare
// resolution request (no env vars, no config, unknown model) still produces a
// valid request body with max_tokens set to DefaultOutputFallback.
func TestUnsetEnvFallsBackToCompiledDefault(t *testing.T) {
	clearProviderKeys(t)
	os.Unsetenv("MYCORRHIZA_POWER_MAX_OUTPUT_TOKENS")
	os.Unsetenv("MYCORRHIZA_ANTHROPIC_POWER_MAX_OUTPUT_TOKENS")

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	spec := providerSpecForModel("anthropic", TierPremium, "unknown-future-model", "")
	if spec.OutputLimit != DefaultOutputFallback {
		t.Errorf("spec.OutputLimit = %d, want DefaultOutputFallback (%d)", spec.OutputLimit, DefaultOutputFallback)
	}

	payload, err := anthropicAdapter{}.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := body["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens missing or wrong type: %v", body["max_tokens"])
	}
	if int(got) != DefaultOutputFallback {
		t.Errorf("max_tokens = %d, want DefaultOutputFallback (%d)", int(got), DefaultOutputFallback)
	}
}

// TestOutputLimitReachesProviderSpecForAllProviders asserts that
// providerSpecForModel propagates a non-zero OutputLimit from the registry to
// the returned ProviderSpec for every provider branch. Dropping the assignment
// from any single branch leaves that branch with OutputLimit==0, which means
// an Anthropic request built through that branch would use the fallback even
// when the registry declared a limit — the value came from the right place but
// the wiring was broken.
//
// Mutation: remove the OutputLimit assignment from any one branch of the
// switch in providerSpecForModel → this test goes red for that provider.
func TestOutputLimitReachesProviderSpecForAllProviders(t *testing.T) {
	// Inject claude-haiku-4-5 (OutputLimit: 64000) into the registry for every
	// provider name under test, so the lookup finds a non-zero value regardless
	// of which provider string is used. The field being tested is the wiring,
	// not the model selection logic.
	sentinel := ModelDefinition{
		Provider:    "", // overwritten per-case
		Name:        "probe-model",
		Family:      ModelFamilyClaude,
		ContextSize: 200000,
		OutputLimit: 64000,
		DrivesTools: true,
		CostTier:    TierPremium,
	}

	providers := []string{"local", "anthropic", "openai", "grok", "google", "openrouter", "nvidia", "unknown-falls-to-default"}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			entry := sentinel
			entry.Provider = provider
			if provider == "unknown-falls-to-default" {
				entry.Provider = "local" // default branch maps to "local"
			}

			// Temporarily prepend the sentinel so the registry lookup finds it.
			origFallback := FallbackModels
			FallbackModels = append([]ModelDefinition{entry}, FallbackModels...)
			t.Cleanup(func() { FallbackModels = origFallback })

			// Also clear the live cache so activeModelRegistry() returns our
			// modified FallbackModels rather than a cached slice.
			modelRegistryMu.Lock()
			modelRegistryCache = nil
			modelRegistryMu.Unlock()
			t.Cleanup(func() {
				modelRegistryMu.Lock()
				modelRegistryCache = nil
				modelRegistryMu.Unlock()
			})

			spec := providerSpecForModel(provider, TierPremium, "probe-model", "")
			if spec.OutputLimit != 64000 {
				t.Errorf("provider %q: ProviderSpec.OutputLimit = %d, want 64000 (registry value must reach the spec via all branches)", provider, spec.OutputLimit)
			}
		})
	}
}

func TestGeneralTierEnvLimitIsApplied(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("MYCORRHIZA_PREMIUM_MAX_OUTPUT_TOKENS", "4000")

	spec := providerSpecForModel("anthropic", TierPremium, "claude-haiku-4-5", "")
	if spec.OutputLimit != 4000 {
		t.Errorf("spec.OutputLimit = %d, want 4000 (general tier env)", spec.OutputLimit)
	}
	if spec.CeilingSource != "general tier env var (primary)" {
		t.Errorf("spec.CeilingSource = %q, want 'general tier env var (primary)'", spec.CeilingSource)
	}
}

func TestAliasEnvLimitIsAppliedAndOverriddenByPrimary(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("MYCORRHIZA_FAST_MAX_OUTPUT_TOKENS", "3000") // alias

	spec := providerSpecForModel("anthropic", TierCheapest, "claude-haiku-4-5", "")
	if spec.OutputLimit != 3000 {
		t.Errorf("spec.OutputLimit = %d, want 3000 (general alias env)", spec.OutputLimit)
	}
	if spec.CeilingSource != "general tier env var (alias)" {
		t.Errorf("spec.CeilingSource = %q, want 'general tier env var (alias)'", spec.CeilingSource)
	}

	// Now set primary, it should override alias
	t.Setenv("MYCORRHIZA_CHEAPEST_MAX_OUTPUT_TOKENS", "4000")
	spec = providerSpecForModel("anthropic", TierCheapest, "claude-haiku-4-5", "")
	if spec.OutputLimit != 4000 {
		t.Errorf("spec.OutputLimit = %d, want 4000 (general primary env overrides alias)", spec.OutputLimit)
	}
	if spec.CeilingSource != "general tier env var (primary)" {
		t.Errorf("spec.CeilingSource = %q, want 'general tier env var (primary)'", spec.CeilingSource)
	}
}

func TestProviderSpecificTierEnvOverridesGeneral(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("MYCORRHIZA_PREMIUM_MAX_OUTPUT_TOKENS", "4000")
	t.Setenv("MYCORRHIZA_OPENROUTER_PREMIUM_MAX_OUTPUT_TOKENS", "5000")

	spec := providerSpecForModel("openrouter", TierPremium, "anthropic/claude-3-opus", "")
	if spec.OutputLimit != 5000 {
		t.Errorf("spec.OutputLimit = %d, want 5000 (provider-specific tier env wins)", spec.OutputLimit)
	}
	if spec.CeilingSource != "provider-specific tier env var (primary)" {
		t.Errorf("spec.CeilingSource = %q, want 'provider-specific tier env var (primary)'", spec.CeilingSource)
	}
}
