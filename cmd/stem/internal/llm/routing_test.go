package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	t.Run("registry fallback selects lowest available cost", func(t *testing.T) {
		cases := []struct {
			name      string
			provider  string
			key       string
			tier      ModelTier
			wantProv  string
			wantModel string
			wantMode  Mode
			wantEndpt string
		}{
			{name: "anthropic premium cap", provider: "anthropic", key: "ANTHROPIC_API_KEY", tier: TierPremium, wantProv: "anthropic", wantModel: "claude-3-5-haiku", wantMode: ModeAnthropic, wantEndpt: "/v1/messages"},
			{name: "anthropic standard cap", provider: "anthropic", key: "ANTHROPIC_API_KEY", tier: TierStandard, wantProv: "anthropic", wantModel: "claude-3-5-haiku", wantMode: ModeAnthropic, wantEndpt: "/v1/messages"},
			{name: "anthropic cheapest cap", provider: "anthropic", key: "ANTHROPIC_API_KEY", tier: TierCheapest, wantProv: "anthropic", wantModel: "claude-3-5-haiku", wantMode: ModeAnthropic, wantEndpt: "/v1/messages"},
			{name: "openai premium cap", provider: "openai", key: "OPENAI_API_KEY", tier: TierPremium, wantProv: "openai", wantModel: "gpt-4o-mini", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openai standard cap", provider: "openai", key: "OPENAI_API_KEY", tier: TierStandard, wantProv: "openai", wantModel: "gpt-4o-mini", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openai cheapest cap", provider: "openai", key: "OPENAI_API_KEY", tier: TierCheapest, wantProv: "openai", wantModel: "gpt-4o-mini", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "google premium cap", provider: "google", key: "GOOGLE_API_KEY", tier: TierPremium, wantProv: "google", wantModel: "gemini-1.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "google standard cap", provider: "google", key: "GOOGLE_API_KEY", tier: TierStandard, wantProv: "google", wantModel: "gemini-1.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "google cheapest cap", provider: "google", key: "GOOGLE_API_KEY", tier: TierCheapest, wantProv: "google", wantModel: "gemini-1.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "grok premium cap uses local lower cost", provider: "grok", key: "GROK_API_KEY", tier: TierPremium, wantProv: "local", wantModel: "llama3.2", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "grok standard cap uses local lower cost", provider: "grok", key: "GROK_API_KEY", tier: TierStandard, wantProv: "local", wantModel: "llama3.2", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "grok cheapest cap uses local lower cost", provider: "grok", key: "GROK_API_KEY", tier: TierCheapest, wantProv: "local", wantModel: "llama3.2", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openrouter premium cap", provider: "openrouter", key: "OPENROUTER_API_KEY", tier: TierPremium, wantProv: "openrouter", wantModel: "google/gemini-1.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openrouter standard cap", provider: "openrouter", key: "OPENROUTER_API_KEY", tier: TierStandard, wantProv: "openrouter", wantModel: "google/gemini-1.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "openrouter cheapest cap", provider: "openrouter", key: "OPENROUTER_API_KEY", tier: TierCheapest, wantProv: "openrouter", wantModel: "google/gemini-1.5-flash", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "local premium cap", provider: "local", tier: TierPremium, wantProv: "local", wantModel: "llama3.2", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "local standard cap", provider: "local", tier: TierStandard, wantProv: "local", wantModel: "llama3.2", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
			{name: "local cheapest cap", provider: "local", tier: TierCheapest, wantProv: "local", wantModel: "llama3.2", wantMode: ModeOpenAIish, wantEndpt: "/chat/completions"},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				clearProviderKeys(t)
				t.Setenv("DEFAULT_LLM_PROVIDER", tt.provider)
				clearTierModelEnv(t, tt.provider)
				if tt.key != "" {
					t.Setenv(tt.key, "test-key")
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
		Model:       "claude-sonnet-4-6",
		APIKey:      "test-key",
		Endpoint:    "/v1/messages",
		Mode:        ModeAnthropic,
		Temperature: 0.25,
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

	if got := captured.Header.Get("anthropic-beta"); got != "prompt-caching-2024-07-31" {
		t.Fatalf("anthropic-beta header = %q, want %q", got, "prompt-caching-2024-07-31")
	}
	if got := captured.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version header = %q, want %q", got, "2023-06-01")
	}

	if captured.Request.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q, want %q", captured.Request.Model, "claude-sonnet-4-6")
	}
	if captured.Request.MaxTokens != 2048 {
		t.Fatalf("max_tokens = %d, want 2048", captured.Request.MaxTokens)
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

	var smallContent string
	if err := json.Unmarshal(captured.Request.Messages[0].Content, &smallContent); err != nil {
		t.Fatalf("decode small message content: %v", err)
	}
	if smallContent != "small note" {
		t.Fatalf("small message content = %q, want %q", smallContent, "small note")
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
	if !strings.Contains(largeBlocks[0].Text, "repomap.md") {
		t.Fatalf("large message text did not preserve cached context marker: %q", largeBlocks[0].Text)
	}
	if largeBlocks[0].CacheControl == nil || largeBlocks[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("large message cache_control = %#v, want ephemeral", largeBlocks[0].CacheControl)
	}
}
