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
		Temperature: 0.1,
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
			Temperature: 0.1,
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

// TestOpenAIishRequestCarriesNoMaxTokens asserts that the OpenAI-shaped
// adapter never adds max_tokens to its request body. The asymmetry with the
// Anthropic adapter is deliberate: OpenAI-shaped families treat max_tokens as
// optional and the provider's own default is the right answer.
//
// Mutation: add max_tokens to openAIishAdapter.BuildChatRequest → this test
// goes red. It also prevents the byte-identity OpenAI fixtures from moving.
func TestOpenAIishRequestCarriesNoMaxTokens(t *testing.T) {
	spec := ProviderSpec{
		Model:       "gpt-test",
		Temperature: 0.5,
		OutputLimit: 8192, // carried on the spec but must not reach the wire
	}
	payload, err := openAIishAdapter{}.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := body["max_tokens"]; present {
		t.Errorf("max_tokens present in OpenAI-shaped request body; it must not be sent — "+
			"OpenAI-shaped providers use their own default, and inventing a ceiling "+
			"for providers that do not require one is a regression. Got body: %s", payload)
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

// TestAnthropicRequestWithNoModelDeclarationIsValid asserts that a bare
// ProviderSpec (OutputLimit zero, model not in registry) still produces a
// valid request body with max_tokens set to anthropicOutputFallback.
//
// This is the most-travelled path in the existing suite: almost every existing
// test builds a bare ProviderSpec with an unknown model name (e.g.
// "claude-test"), and none of them set OutputLimit. The fallback must produce a
// value Anthropic accepts; a zero on the wire would not be.
func TestAnthropicRequestWithNoModelDeclarationIsValid(t *testing.T) {
	spec := ProviderSpec{
		Model:       "unknown-future-model",
		Temperature: 0.1,
		// OutputLimit intentionally zero: registry has no entry for this name.
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
	if int(got) != anthropicOutputFallback {
		t.Errorf("max_tokens = %d, want anthropicOutputFallback (%d)", int(got), anthropicOutputFallback)
	}
	if int(got) <= 0 {
		t.Errorf("max_tokens = %d, must be positive for Anthropic to accept the request", int(got))
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
