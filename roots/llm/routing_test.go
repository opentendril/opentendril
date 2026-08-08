package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicContentBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicRequestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model       string                    `json:"model"`
	MaxTokens   int                       `json:"max_tokens"`
	Temperature float64                   `json:"temperature"`
	System      []anthropicContentBlock   `json:"system"`
	Messages    []anthropicRequestMessage `json:"messages"`
}

type anthropicCapture struct {
	Header  http.Header
	Request anthropicRequest
}

func clearTierModelEnv(t *testing.T, provider string) {
	t.Helper()

	upper := strings.ToUpper(strings.TrimSpace(provider))
	t.Setenv(upper+"_PREMIUM_MODEL", "")
	t.Setenv(upper+"_STANDARD_MODEL", "")
	t.Setenv(upper+"_CHEAPEST_MODEL", "")
	t.Setenv(upper+"_MODEL_NAME", "")
	t.Setenv("DEFAULT_MODEL_NAME", "")
}

func assertProviderSpec(t *testing.T, got ProviderSpec, wantProvider, wantModel string, wantMode Mode, wantEndpoint string) {
	t.Helper()

	// A spec that resolved is a spec that can be called. Without this, a
	// resolution failure carrying the right provider name and an empty model
	// would satisfy every field assertion below it that happens to match.
	if got.ResolutionErr != nil {
		t.Fatalf("spec.ResolutionErr = %v, want nil", got.ResolutionErr)
	}
	if got.Provider != wantProvider {
		t.Fatalf("spec.Provider = %q, want %q", got.Provider, wantProvider)
	}
	if got.Model != wantModel {
		t.Fatalf("spec.Model = %q, want %q", got.Model, wantModel)
	}
	if got.Mode != wantMode {
		t.Fatalf("spec.Mode = %q, want %q", got.Mode, wantMode)
	}
	if got.Endpoint != wantEndpoint {
		t.Fatalf("spec.Endpoint = %q, want %q", got.Endpoint, wantEndpoint)
	}
}

func TestModelTierResolution(t *testing.T) {
	t.Run("provider premium override wins", func(t *testing.T) {
		t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
		clearTierModelEnv(t, "openai")
		t.Setenv("OPENAI_PREMIUM_MODEL", "gpt-5.5-custom")
		t.Setenv("OPENAI_MODEL_NAME", "gpt-5.4-mini")

		spec := ResolveTierProviderSpec(TierPremium)
		assertProviderSpec(t, spec, "openai", "gpt-5.5-custom", ModeOpenAIish, "/chat/completions")
	})

	t.Run("provider proxy uses premium tier", func(t *testing.T) {
		t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
		clearTierModelEnv(t, "openai")
		t.Setenv("OPENAI_PREMIUM_MODEL", "gpt-5.5-proxy")
		t.Setenv("OPENAI_MODEL_NAME", "gpt-5.4-mini")

		spec := ResolveProviderSpec()
		assertProviderSpec(t, spec, "openai", "gpt-5.5-proxy", ModeOpenAIish, "/chat/completions")
	})

	t.Run("default model override wins", func(t *testing.T) {
		t.Setenv("DEFAULT_LLM_PROVIDER", "google")
		clearTierModelEnv(t, "google")
		t.Setenv("DEFAULT_MODEL_NAME", "shared-override")

		spec := ResolveTierProviderSpec(TierCheapest)
		assertProviderSpec(t, spec, "google", "shared-override", ModeOpenAIish, "/chat/completions")
	})

	// The configured provider is a filter on selection, and the tier is a
	// ceiling under which the BEST model wins. Every case here names the
	// configured provider in wantProv: a resolution that answers with some
	// other provider's model is the defect this table exists to catch.
	t.Run("registry fallback stays on the configured provider and takes its best model under the tier", func(t *testing.T) {
		cases := []struct {
			name      string
			provider  string
			key       string
			local     bool
			tier      ModelTier
			wantProv  string
			wantModel string
			wantMode  Mode
			wantEndpt string
		}{
			{name: "anthropic premium cap", provider: "anthropic", key: "ANTHROPIC_API_KEY", tier: TierPremium, wantProv: "anthropic", wantModel: "claude-opus-4-8", wantMode: ModeAnthropic, wantEndpt: "/v1/messages"},
			{name: "anthropic standard cap", provider: "anthropic", key: "ANTHROPIC_API_KEY", tier: TierStandard, wantProv: "anthropic", wantModel: "claude-sonnet-5", wantMode: ModeAnthropic, wantEndpt: "/v1/messages"},
			{name: "anthropic cheapest cap", provider: "anthropic", key: "ANTHROPIC_API_KEY", tier: TierCheapest, wantProv: "anthropic", wantModel: "claude-haiku-4-5", wantMode: ModeAnthropic, wantEndpt: "/v1/messages"},
			{name: "openai premium cap", provider: "openai", key: "OPENAI_API_KEY", tier: TierPremium, wantProv: "openai", wantModel: "gpt-5.6-terra", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openai standard cap", provider: "openai", key: "OPENAI_API_KEY", tier: TierStandard, wantProv: "openai", wantModel: "gpt-5.6-luna", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openai cheapest cap", provider: "openai", key: "OPENAI_API_KEY", tier: TierCheapest, wantProv: "openai", wantModel: "gpt-5.6-luna", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "google premium cap", provider: "google", key: "GOOGLE_API_KEY", tier: TierPremium, wantProv: "google", wantModel: "gemini-3.1-pro", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "google standard cap", provider: "google", key: "GOOGLE_API_KEY", tier: TierStandard, wantProv: "google", wantModel: "gemini-3.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "google cheapest cap", provider: "google", key: "GOOGLE_API_KEY", tier: TierCheapest, wantProv: "google", wantModel: "gemini-3.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			// grok serves one premium model. A premium ceiling reaches it; the
			// cheaper ceilings reach nothing AT GROK, which is the failing case
			// covered by TestConfiguredProviderWithNoUsableModelFailsLoudly —
			// it used to answer with a local model instead.
			{name: "grok premium cap", provider: "grok", key: "GROK_API_KEY", tier: TierPremium, wantProv: "grok", wantModel: "grok-4.5", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openrouter premium cap", provider: "openrouter", key: "OPENROUTER_API_KEY", tier: TierPremium, wantProv: "openrouter", wantModel: "google/gemini-2.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openrouter standard cap", provider: "openrouter", key: "OPENROUTER_API_KEY", tier: TierStandard, wantProv: "openrouter", wantModel: "google/gemini-2.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openrouter cheapest cap", provider: "openrouter", key: "OPENROUTER_API_KEY", tier: TierCheapest, wantProv: "openrouter", wantModel: "google/gemini-2.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "local premium cap", provider: "local", local: true, tier: TierPremium, wantProv: "local", wantModel: "qwen2.5-coder:14b", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "local standard cap", provider: "local", local: true, tier: TierStandard, wantProv: "local", wantModel: "qwen3.5:9b", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "local cheapest cap", provider: "local", local: true, tier: TierCheapest, wantProv: "local", wantModel: "llama3.2", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				chdirWithoutTendrilConfig(t)
				clearProviderKeys(t)
				t.Setenv("DEFAULT_LLM_PROVIDER", tt.provider)
				clearTierModelEnv(t, tt.provider)
				if tt.key != "" {
					t.Setenv(tt.key, "test-key")
				}
				if tt.local {
					withLocalInference(t)
				}

				spec := ResolveTierProviderSpec(tt.tier)
				assertProviderSpec(t, spec, tt.wantProv, tt.wantModel, tt.wantMode, tt.wantEndpt)
			})
		}
	})

	t.Run("coordinator uses premium tier", func(t *testing.T) {
		t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
		clearTierModelEnv(t, "openai")
		t.Setenv("OPENAI_PREMIUM_MODEL", "gpt-5.5-coordinator")
		t.Setenv("OPENAI_MODEL_NAME", "gpt-5.4-mini")
		t.Setenv("COORDINATOR_LLM_PROVIDER", "")
		t.Setenv("COORDINATOR_MODEL_NAME", "")
		t.Setenv("COORDINATOR_LOCAL_INFERENCE_URL", "")

		spec := ResolveCoordinatorProviderSpec()
		assertProviderSpec(t, spec, "openai", "gpt-5.5-coordinator", ModeOpenAIish, "/chat/completions")
	})
}

func TestAnthropicPromptCachingPayload(t *testing.T) {
	capturedCh := make(chan anthropicCapture, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %s, want /v1/messages", r.URL.Path)
		}

		var captured anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		capturedCh <- anthropicCapture{
			Header:  r.Header.Clone(),
			Request: captured,
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": "ok",
				},
			},
		})
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider:    "anthropic",
		BaseURL:     server.URL,
		Model:       "claude-sonnet-5",
		APIKey:      "test-key",
		Endpoint:    "/v1/messages",
		Mode:        ModeAnthropic,
		Temperature: ptr(0.25),
	})

	content := strings.Repeat("repomap.md cached context ", 60)
	result, err := client.Call(context.Background(), []Message{
		{Role: "system", Content: "System prompt text here."},
		{Role: "user", Content: "small note"},
		{Role: "assistant", Content: content},
	})
	if err != nil {
		t.Fatalf("client.Call failed: %v", err)
	}
	if result != "ok" {
		t.Fatalf("client.Call result = %q, want %q", result, "ok")
	}

	captured := <-capturedCh

	// The beta header was required while prompt caching was in beta; it is dead now.
	if got := captured.Header.Get("anthropic-beta"); got != "" {
		t.Fatalf("anthropic-beta header = %q, want absent (header is dead; prompt caching is GA)", got)
	}
	if got := captured.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version header = %q, want %q", got, "2023-06-01")
	}

	if captured.Request.Model != "claude-sonnet-5" {
		t.Fatalf("model = %q, want %q", captured.Request.Model, "claude-sonnet-5")
	}
	if captured.Request.MaxTokens != anthropicOutputFallback {
		t.Fatalf("max_tokens = %d, want %d (anthropicOutputFallback)", captured.Request.MaxTokens, anthropicOutputFallback)
	}
	if captured.Request.Temperature != 0.25 {
		t.Fatalf("temperature = %v, want 0.25", captured.Request.Temperature)
	}
	if len(captured.Request.System) != 1 {
		t.Fatalf("system block count = %d, want 1", len(captured.Request.System))
	}
	if captured.Request.System[0].Type != "text" {
		t.Fatalf("system block type = %q, want text", captured.Request.System[0].Type)
	}
	if captured.Request.System[0].Text != "System prompt text here." {
		t.Fatalf("system block text = %q, want %q", captured.Request.System[0].Text, "System prompt text here.")
	}
	if captured.Request.System[0].CacheControl == nil || captured.Request.System[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("system cache_control = %#v, want ephemeral", captured.Request.System[0].CacheControl)
	}
	if len(captured.Request.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(captured.Request.Messages))
	}
	if captured.Request.Messages[0].Role != "user" {
		t.Fatalf("first message role = %q, want user", captured.Request.Messages[0].Role)
	}
	if captured.Request.Messages[1].Role != "assistant" {
		t.Fatalf("second message role = %q, want assistant", captured.Request.Messages[1].Role)
	}

	// Message 0 ("small note") is always block form regardless of length.
	var smallBlocks []anthropicContentBlock
	if err := json.Unmarshal(captured.Request.Messages[0].Content, &smallBlocks); err != nil {
		t.Fatalf("decode small message as blocks: %v", err)
	}
	if len(smallBlocks) != 1 {
		t.Fatalf("small message block count = %d, want 1", len(smallBlocks))
	}
	if smallBlocks[0].Type != "text" {
		t.Fatalf("small message block type = %q, want text", smallBlocks[0].Type)
	}
	if smallBlocks[0].Text != "small note" {
		t.Fatalf("small message text = %q, want %q", smallBlocks[0].Text, "small note")
	}
	// With two single-block messages, the flat index selects position 1 only.
	// Message 0 must be unmarked — this pins "we don't mark everything".
	if smallBlocks[0].CacheControl != nil {
		t.Fatalf("small message has cache_control = %#v, want none (only the last position is selected)", smallBlocks[0].CacheControl)
	}

	var largeBlocks []anthropicContentBlock
	if err := json.Unmarshal(captured.Request.Messages[1].Content, &largeBlocks); err != nil {
		t.Fatalf("decode large message content blocks: %v", err)
	}
	if len(largeBlocks) != 1 {
		t.Fatalf("large message block count = %d, want 1", len(largeBlocks))
	}
	if largeBlocks[0].Type != "text" {
		t.Fatalf("large message block type = %q, want text", largeBlocks[0].Type)
	}
	// Text is preserved verbatim regardless of caching decisions.
	if !strings.Contains(largeBlocks[0].Text, "repomap.md") {
		t.Fatalf("large message text was not preserved verbatim: %q", largeBlocks[0].Text)
	}
	// Message 1 is the last message; the positional strategy always marks it.
	if largeBlocks[0].CacheControl == nil || largeBlocks[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("large message cache_control = %#v, want ephemeral", largeBlocks[0].CacheControl)
	}
}

// TestIsRouterConfigOverride exercises the three scenarios described in the
// task verification plan for the explicit is-router config field.
//
// Each sub-test creates a temporary .tendril/config.yaml, chdirs into the
// temp tree so that loadTendrilConfig picks it up, and restores the original
// directory on cleanup — the same pattern used by
// TestResolveLocalProviderSpecUsesTendrilConfig.
func TestIsRouterConfigOverride(t *testing.T) {
	// chdir sets up a temp tree with the given YAML and returns to the original
	// directory when the test ends.
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

	// (1) is-router: true bypasses regardless of model name.
	//     The model name "my-custom-proxy" would never match any existing
	//     heuristic pattern — proving that the explicit field, not the
	//     heuristic, drove the bypass decision.
	t.Run("explicit true bypasses for non-heuristic model name", func(t *testing.T) {
		clearProviderKeys(t)
		chdir(t, `
llm:
  default-provider: openrouter
  providers:
    openrouter:
      base-url: https://openrouter.ai/api/v1
      api-key: test-key
      model: my-custom-proxy
      is-router: true
`)
		t.Setenv("DEFAULT_LLM_PROVIDER", "openrouter")
		t.Setenv("OPENROUTER_API_KEY", "test-key")
		clearTierModelEnv(t, "openrouter")
		t.Setenv("DEFAULT_MODEL_NAME", "")

		if got := ShouldBypassInternalRouter(); !got {
			t.Fatalf("ShouldBypassInternalRouter() = false, want true (is-router: true must bypass even for non-heuristic model name)")
		}
		// Double-check: the model name alone would NOT trigger the heuristic.
		if IsThirdPartyRouterModel("my-custom-proxy") {
			t.Fatalf("IsThirdPartyRouterModel(%q) = true, want false (heuristic must not match this name)", "my-custom-proxy")
		}
	})

	// (2) is-router: false prevents bypass even when the model name matches an
	//     existing heuristic pattern. An operator running a self-hosted proxy
	//     literally named "nvidia/my-router" (contains "router" and "nvidia")
	//     must be able to opt out, letting the internal dynamic router run.
	t.Run("explicit false prevents bypass for heuristic-matching model name", func(t *testing.T) {
		clearProviderKeys(t)
		chdir(t, `
llm:
  default-provider: nvidia
  providers:
    nvidia:
      base-url: https://localproxy.internal/v1
      api-key: test-key
      model: nvidia/my-router
      is-router: false
`)
		t.Setenv("DEFAULT_LLM_PROVIDER", "nvidia")
		t.Setenv("NVIDIA_API_KEY", "test-key")
		clearTierModelEnv(t, "nvidia")
		t.Setenv("DEFAULT_MODEL_NAME", "")

		// Sanity: the heuristic alone would say "bypass".
		if !IsThirdPartyRouterModel("nvidia/my-router") {
			t.Fatalf("IsThirdPartyRouterModel(%q) = false, want true (pre-condition: heuristic must match)", "nvidia/my-router")
		}
		if got := ShouldBypassInternalRouter(); got {
			t.Fatalf("ShouldBypassInternalRouter() = true, want false (is-router: false must prevent bypass even for heuristic-matching model name)")
		}
	})

	// (3) Regression: heuristic-only behavior (no is-router set) is unchanged
	//     for the two already-detected routers. This guards against accidentally
	//     breaking zero-config OpenRouter and NVIDIA setups.
	t.Run("zero-config heuristic still detects openrouter/auto", func(t *testing.T) {
		clearProviderKeys(t)
		// Use an empty root (no .tendril/config.yaml) so that loadTendrilConfig
		// returns a zero tendrilConfig — exactly what a zero-config operator sees.
		root := t.TempDir()
		orig, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		if err := os.Chdir(root); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(orig) })

		t.Setenv("DEFAULT_LLM_PROVIDER", "openrouter")
		t.Setenv("OPENROUTER_API_KEY", "test-key")
		clearTierModelEnv(t, "openrouter")
		t.Setenv("DEFAULT_MODEL_NAME", "")
		t.Setenv("OPENROUTER_MODEL_NAME", "openrouter/auto")

		if got := ShouldBypassInternalRouter(); !got {
			t.Fatalf("ShouldBypassInternalRouter() = false, want true (zero-config heuristic must still detect openrouter/auto)")
		}
	})

	t.Run("zero-config heuristic still detects nvidia router model", func(t *testing.T) {
		clearProviderKeys(t)
		root := t.TempDir()
		orig, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		if err := os.Chdir(root); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(orig) })

		t.Setenv("DEFAULT_LLM_PROVIDER", "nvidia")
		t.Setenv("NVIDIA_API_KEY", "test-key")
		clearTierModelEnv(t, "nvidia")
		t.Setenv("DEFAULT_MODEL_NAME", "")
		t.Setenv("NVIDIA_MODEL_NAME", "nvidia/llama-3.3-nemotron-super-49b-v1-router")

		if got := ShouldBypassInternalRouter(); !got {
			t.Fatalf("ShouldBypassInternalRouter() = false, want true (zero-config heuristic must still detect nvidia router model)")
		}
	})
}
