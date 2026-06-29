package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Mode string

const (
	ModeAnthropic Mode = "anthropic"
	ModeOpenAIish Mode = "openaiish"
)

type ModelTier string

const (
	TierPremium  ModelTier = "premium"
	TierStandard ModelTier = "standard"
	TierCheapest ModelTier = "cheapest"
)

type ProviderSpec struct {
	Provider    string
	BaseURL     string
	BaseURLs    []string
	APIKey      string
	Model       string
	Endpoint    string
	Mode        Mode
	Temperature float64
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	httpClient *http.Client
	spec       ProviderSpec
}

func (c *Client) SetTemperature(temp float64) {
	if c != nil {
		c.spec.Temperature = temp
	}
}

func NewClient(spec ProviderSpec) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		spec:       spec,
	}
}

func NewClientFromEnv() *Client {
	return NewClient(ResolveProviderSpec())
}

func NewClientForTier(tier ModelTier) *Client {
	return NewClient(ResolveTierProviderSpec(tier))
}

func NewCoordinatorClientFromEnv() *Client {
	return NewClient(ResolveCoordinatorProviderSpec())
}

func (c *Client) CallPrompt(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	return c.Call(ctx, messages)
}

func (c *Client) Call(ctx context.Context, messages []Message) (string, error) {
	if c == nil {
		return "", fmt.Errorf("llm client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if c.spec.BaseURL == "" {
		return "", fmt.Errorf("no LLM base URL configured for provider %q", c.spec.Provider)
	}
	if c.spec.Model == "" {
		return "", fmt.Errorf("no LLM model configured for provider %q", c.spec.Provider)
	}
	if c.spec.Provider != "local" && strings.TrimSpace(c.spec.APIKey) == "" {
		return "", fmt.Errorf("no API key configured for provider %q", c.spec.Provider)
	}

	candidates := c.spec.BaseURLs
	if len(candidates) == 0 {
		candidates = []string{c.spec.BaseURL}
	}

	var lastErr error
	for _, baseURL := range candidates {
		content, err := c.callAtBaseURL(ctx, baseURL, messages)
		if err == nil {
			return content, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("llm request failed for provider %q", c.spec.Provider)
	}

	return "", lastErr
}

func ResolveProviderSpec() ProviderSpec {
	return ResolveTierProviderSpec(TierPremium)
}

func ResolveTierProviderSpec(tier ModelTier) ProviderSpec {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("DEFAULT_LLM_PROVIDER")))
	if provider == "" {
		provider = detectProviderFallback()
	}

	return resolveTierProviderSpecForProvider(provider, tier, "")
}

func ResolveCoordinatorProviderSpec() ProviderSpec {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("COORDINATOR_LLM_PROVIDER")))
	if provider == "" {
		spec := ResolveTierProviderSpec(TierPremium)
		if model := strings.TrimSpace(os.Getenv("COORDINATOR_MODEL_NAME")); model != "" {
			spec.Model = model
		}
		if strings.EqualFold(spec.Provider, "local") {
			if baseURL := strings.TrimSpace(os.Getenv("COORDINATOR_LOCAL_INFERENCE_URL")); baseURL != "" {
				spec.BaseURL = baseURL
				spec.BaseURLs = localInferenceBaseURLs(baseURL)
			}
		}
		return spec
	}

	spec := resolveTierProviderSpecForProvider(
		provider,
		TierPremium,
		strings.TrimSpace(os.Getenv("COORDINATOR_LOCAL_INFERENCE_URL")),
	)
	if model := strings.TrimSpace(os.Getenv("COORDINATOR_MODEL_NAME")); model != "" {
		spec.Model = model
	}
	return spec
}

func detectProviderFallback() string {
	if os.Getenv("LOCAL_INFERENCE_URL") != "" || os.Getenv("LOCAL_MODEL_NAME") != "" {
		return "local"
	}
	candidates := []struct {
		provider string
		key      string
	}{
		{provider: "openai", key: "OPENAI_API_KEY"},
		{provider: "anthropic", key: "ANTHROPIC_API_KEY"},
		{provider: "grok", key: "GROK_API_KEY"},
		{provider: "google", key: "GOOGLE_API_KEY"},
		{provider: "openrouter", key: "OPENROUTER_API_KEY"},
		{provider: "opentendril", key: "OPENTENDRIL_API_KEY"},
		{provider: "nvidia", key: "NVIDIA_API_KEY"},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(os.Getenv(candidate.key)) != "" {
			return candidate.provider
		}
	}
	return "local"
}

func resolveTierProviderSpecForProvider(provider string, tier ModelTier, localInferenceOverride string) ProviderSpec {
	provider = strings.ToLower(strings.TrimSpace(provider))
	tier = canonicalModelTier(tier)
	localInferenceOverride = strings.TrimSpace(localInferenceOverride)
	model := resolveModelForTier(provider, tier)

	switch provider {
	case "local":
		baseURL := localInferenceOverride
		if baseURL == "" {
			baseURL = envOr("LOCAL_INFERENCE_URL", "http://host.docker.internal:11434/v1")
		}
		return ProviderSpec{
			Provider:    "local",
			BaseURL:     baseURL,
			BaseURLs:    localInferenceBaseURLs(baseURL),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	case "anthropic":
		return ProviderSpec{
			Provider:    "anthropic",
			BaseURL:     envOr("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
			APIKey:      os.Getenv("ANTHROPIC_API_KEY"),
			Model:       model,
			Endpoint:    "/v1/messages",
			Mode:        ModeAnthropic,
			Temperature: 0.1,
		}
	case "openai":
		return ProviderSpec{
			Provider:    "openai",
			BaseURL:     envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey:      os.Getenv("OPENAI_API_KEY"),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	case "grok":
		return ProviderSpec{
			Provider:    "grok",
			BaseURL:     envOr("GROK_BASE_URL", "https://api.x.ai/v1"),
			APIKey:      os.Getenv("GROK_API_KEY"),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	case "google":
		return ProviderSpec{
			Provider:    "google",
			BaseURL:     envOr("GOOGLE_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"),
			APIKey:      os.Getenv("GOOGLE_API_KEY"),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	case "openrouter":
		return ProviderSpec{
			Provider:    "openrouter",
			BaseURL:     envOr("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
			APIKey:      os.Getenv("OPENROUTER_API_KEY"),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	case "opentendril":
		return ProviderSpec{
			Provider:    "opentendril",
			BaseURL:     envOr("OPENTENDRIL_BASE_URL", "https://api.opentendril.com/v1"),
			APIKey:      os.Getenv("OPENTENDRIL_API_KEY"),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	case "nvidia":
		return ProviderSpec{
			Provider:    "nvidia",
			BaseURL:     envOr("NVIDIA_BASE_URL", "https://integrate.api.nvidia.com/v1"),
			APIKey:      os.Getenv("NVIDIA_API_KEY"),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	default:
		baseURL := localInferenceOverride
		if baseURL == "" {
			baseURL = envOr("LOCAL_INFERENCE_URL", "http://host.docker.internal:11434/v1")
		}
		return ProviderSpec{
			Provider:    "local",
			BaseURL:     baseURL,
			BaseURLs:    localInferenceBaseURLs(baseURL),
			Model:       model,
			Endpoint:    "/chat/completions",
			Mode:        ModeOpenAIish,
			Temperature: 0.1,
		}
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func canonicalModelTier(tier ModelTier) ModelTier {
	switch tier {
	case TierPremium, TierStandard, TierCheapest:
		return tier
	default:
		return TierPremium
	}
}

func tierSpecificModelEnvName(provider string, tier ModelTier) string {
	return fmt.Sprintf("%s_%s_MODEL", strings.ToUpper(strings.TrimSpace(provider)), strings.ToUpper(string(canonicalModelTier(tier))))
}

func providerModelEnvName(provider string) string {
	return fmt.Sprintf("%s_MODEL_NAME", strings.ToUpper(strings.TrimSpace(provider)))
}

func resolveModelForTier(provider string, tier ModelTier) string {
	if model := strings.TrimSpace(os.Getenv(tierSpecificModelEnvName(provider, tier))); model != "" {
		return model
	}
	if model := strings.TrimSpace(os.Getenv(providerModelEnvName(provider))); model != "" {
		return model
	}
	if model := strings.TrimSpace(os.Getenv("DEFAULT_MODEL_NAME")); model != "" {
		return model
	}
	return fallbackModelForTier(provider, tier)
}

func fallbackModelForTier(provider string, tier ModelTier) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return tierFallback(tier, "claude-sonnet-4-6", "claude-haiku-4-5", "claude-haiku-4-5")
	case "openai":
		return tierFallback(tier, "gpt-5.5", "gpt-5.4-mini", "gpt-5.4-nano")
	case "google":
		return tierFallback(tier, "gemini-3-pro", "gemini-3-flash", "gemini-3-flash")
	case "grok":
		return tierFallback(tier, "grok-4", "grok-4-fast-non-reasoning", "grok-4-fast-non-reasoning")
	case "openrouter":
		return tierFallback(tier, "anthropic/claude-sonnet-4-6", "openai/gpt-5.4-mini", "google/gemini-3-flash")
	case "local":
		return tierFallback(tier, "qwen2.5-coder:14b", "qwen2.5-coder:7b", "llama3.2")
	case "opentendril":
		return "anthropic/claude-3.5-sonnet"
	case "nvidia":
		return "meta/llama-3.1-70b-instruct"
	default:
		return fallbackModelForTier("local", tier)
	}
}

func tierFallback(tier ModelTier, premium string, standard string, cheapest string) string {
	switch canonicalModelTier(tier) {
	case TierStandard:
		return standard
	case TierCheapest:
		return cheapest
	default:
		return premium
	}
}

func localInferenceBaseURLs(baseURL string) []string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://host.docker.internal:11434/v1"
	}

	candidates := []string{baseURL}
	switch {
	case strings.Contains(baseURL, "host.docker.internal"):
		candidates = append(candidates,
			strings.ReplaceAll(baseURL, "host.docker.internal", "localhost"),
			strings.ReplaceAll(baseURL, "host.docker.internal", "127.0.0.1"),
		)
	case strings.Contains(baseURL, "localhost"):
		candidates = append(candidates,
			strings.ReplaceAll(baseURL, "localhost", "127.0.0.1"),
			strings.ReplaceAll(baseURL, "localhost", "host.docker.internal"),
		)
	case strings.Contains(baseURL, "127.0.0.1"):
		candidates = append(candidates,
			strings.ReplaceAll(baseURL, "127.0.0.1", "localhost"),
			strings.ReplaceAll(baseURL, "127.0.0.1", "host.docker.internal"),
		)
	default:
		candidates = append(candidates, strings.ReplaceAll(baseURL, "host.docker.internal", "localhost"))
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}

	return out
}

func (c *Client) callAtBaseURL(ctx context.Context, baseURL string, messages []Message) (string, error) {
	var (
		payload []byte
		url     = strings.TrimRight(baseURL, "/") + c.spec.Endpoint
		req     *http.Request
		err     error
	)

	switch c.spec.Mode {
	case ModeAnthropic:
		systemParts := make([]string, 0, 2)
		anthropicMessages := make([]map[string]any, 0, len(messages))
		for _, message := range messages {
			role := strings.ToLower(strings.TrimSpace(message.Role))
			content := message.Content
			trimmedContent := strings.TrimSpace(content)
			switch role {
			case "system":
				if trimmedContent != "" {
					systemParts = append(systemParts, trimmedContent)
				}
			case "assistant", "user":
				anthropicMessages = append(anthropicMessages, anthropicMessagePayload(role, content))
			default:
				anthropicMessages = append(anthropicMessages, anthropicMessagePayload("user", content))
			}
		}

		payloadBody := map[string]any{
			"model":       c.spec.Model,
			"max_tokens":  2048,
			"temperature": c.spec.Temperature,
			"messages":    anthropicMessages,
		}
		if len(systemParts) > 0 {
			payloadBody["system"] = []map[string]any{
				anthropicTextBlock(strings.Join(systemParts, "\n\n"), true),
			}
		}

		payload, err = json.Marshal(payloadBody)
		if err != nil {
			return "", fmt.Errorf("marshal anthropic request: %w", err)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return "", fmt.Errorf("create anthropic request: %w", err)
		}
		req.Header.Set("x-api-key", c.spec.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		payload, err = json.Marshal(map[string]any{
			"model":       c.spec.Model,
			"temperature": c.spec.Temperature,
			"stream":      false,
			"messages":    messages,
		})
		if err != nil {
			return "", fmt.Errorf("marshal chat request: %w", err)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return "", fmt.Errorf("create chat request: %w", err)
		}
		if c.spec.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.spec.APIKey)
		}
	}

	req.Header.Set("Content-Type", "application/json")
	if c.spec.Mode == ModeAnthropic {
		req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read llm response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	switch c.spec.Mode {
	case ModeAnthropic:
		var decoded struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return "", fmt.Errorf("decode anthropic response: %w", err)
		}
		for _, block := range decoded.Content {
			if strings.TrimSpace(block.Text) != "" {
				return strings.TrimSpace(block.Text), nil
			}
		}
		return "", fmt.Errorf("anthropic response contained no text")
	default:
		var decoded struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return "", fmt.Errorf("decode chat response: %w", err)
		}
		if len(decoded.Choices) == 0 {
			return "", fmt.Errorf("chat response contained no choices")
		}
		content := strings.TrimSpace(decoded.Choices[0].Message.Content)
		if content == "" {
			return "", fmt.Errorf("chat response contained no content")
		}
		return content, nil
	}
}

func anthropicTextBlock(text string, cache bool) map[string]any {
	block := map[string]any{
		"type": "text",
		"text": text,
	}
	if cache {
		block["cache_control"] = map[string]string{
			"type": "ephemeral",
		}
	}
	return block
}

func anthropicMessagePayload(role string, content string) map[string]any {
	if shouldCacheAnthropicContent(content) {
		return map[string]any{
			"role":    role,
			"content": []map[string]any{anthropicTextBlock(content, true)},
		}
	}
	return map[string]any{
		"role":    role,
		"content": content,
	}
}

func shouldCacheAnthropicContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.Contains(trimmed, "repomap.md") || len(trimmed) > 1000
}
