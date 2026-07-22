package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

type tendrilConfig struct {
	LLM tendrilLLMConfig `yaml:"llm"`
}

type tendrilLLMConfig struct {
	DefaultProvider string                           `yaml:"default-provider"`
	Providers       map[string]tendrilProviderConfig `yaml:"providers"`
}

type tendrilProviderConfig struct {
	BaseURL     string  `yaml:"base-url"`
	APIKey      string  `yaml:"api-key"`
	Model       string  `yaml:"model"`
	Endpoint    string  `yaml:"endpoint"`
	Temperature float64 `yaml:"temperature"`
}

func (c *Client) SetTemperature(temp float64) {
	if c != nil {
		c.spec.Temperature = temp
	}
}

func NewClient(spec ProviderSpec) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		spec:       spec,
	}
}

func NewClientFromEnv() *Client {
	return NewClient(ResolveProviderSpec())
}

func NewClientForTier(tier ModelTier) *Client {
	return NewClient(ResolveTierProviderSpec(tier))
}

func NewClientForModel(provider string, model string) *Client {
	return NewClient(ResolveModelProviderSpec(provider, model))
}

func ResolveModelProviderSpec(provider string, model string) ProviderSpec {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" {
		return ResolveTierProviderSpec(TierPremium)
	}
	return providerSpecForModel(provider, TierPremium, model, "")
}

func NewCoordinatorClientFromEnv() *Client {
	return NewClient(ResolveCoordinatorProviderSpec())
}

func ResolveLocalProviderSpec() ProviderSpec {
	return resolveTierProviderSpecForProvider("local", TierPremium, "")
}

func (c *Client) CallPrompt(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	return c.Call(ctx, messages)
}

func (c *Client) CallStreamPrompt(ctx context.Context, systemPrompt string, userPrompt string, tokenChan chan<- string) (string, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	return c.CallStream(ctx, messages, tokenChan)
}

func (c *Client) Call(ctx context.Context, messages []Message) (string, error) {
	return c.CallStream(ctx, messages, nil)
}

func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.spec.BaseURL == "" {
		return nil, fmt.Errorf("no LLM base URL configured for provider %q", c.spec.Provider)
	}

	candidates := c.spec.BaseURLs
	if len(candidates) == 0 {
		candidates = []string{c.spec.BaseURL}
	}

	var lastErr error
	for _, baseURL := range candidates {
		models, err := c.listModelsAtBaseURL(ctx, baseURL)
		if err == nil {
			return models, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("list models failed for provider %q", c.spec.Provider)
	}
	return nil, lastErr
}

func (c *Client) CallStream(ctx context.Context, messages []Message, tokenChan chan<- string) (string, error) {
	// Closing the channel is this function's job, on every path. It used to
	// close only where it streamed or exhausted its candidates, so returning
	// early — a missing model, an absent key — left a caller ranging over the
	// channel blocked forever. A caller cannot close it itself without racing
	// the closes below, so the guarantee has to live here: return from
	// CallStream and the channel is closed.
	if tokenChan != nil {
		defer close(tokenChan)
	}

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
		content, err := c.doCall(ctx, baseURL, messages, tokenChan != nil, tokenChan)
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
	return resolveTierProviderSpecWithCaps(tier, false)
}

// ResolveAgentTierProviderSpec resolves the tier default for an autonomous
// Sprout run. It behaves like ResolveTierProviderSpec but, when selection falls
// through to the registry's best model, requires a tool-capable one — so a
// no-session sprout never silently lands on a model that cannot drive tools
// (e.g. a 3B local llama that returns an empty completion). Explicit env/config
// model choices are still honoured, since those are a deliberate override.
func ResolveAgentTierProviderSpec(tier ModelTier) ProviderSpec {
	return resolveTierProviderSpecWithCaps(tier, true)
}

func resolveTierProviderSpecWithCaps(tier ModelTier, requireTools bool) ProviderSpec {
	tier = canonicalModelTier(tier)
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("DEFAULT_LLM_PROVIDER")))
	if provider == "" {
		provider = configuredDefaultProvider()
	}
	if provider == "" {
		provider = detectProviderFallback()
	}
	if model, ok := explicitModelForTier(provider, tier); ok {
		return providerSpecForModel(provider, tier, model, "")
	}
	if model := configuredModelForProvider(provider); model != "" {
		return providerSpecForModel(provider, tier, model, "")
	}

	if model, err := SelectBestModel(Capabilities{MaxCostTier: tier, RequiresToolUse: requireTools}); err == nil {
		return providerSpecForModel(model.Provider, tier, model.Name, "")
	}

	// A tool-capable model was required but none matched (e.g. only small local
	// models are available). Rather than return an empty spec, fall back to the
	// unconstrained best model — the run then reports its outcome honestly
	// instead of silently maturing on nothing.
	if requireTools {
		if model, err := SelectBestModel(Capabilities{MaxCostTier: tier}); err == nil {
			return providerSpecForModel(model.Provider, tier, model.Name, "")
		}
	}

	return providerSpecForModel(provider, tier, "", "")
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
				spec.BaseURLs = LocalInferenceBaseURLs(baseURL)
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
	model, _ := explicitModelForTier(provider, tier)
	return providerSpecForModel(provider, tier, model, localInferenceOverride)
}

func providerSpecForModel(provider string, tier ModelTier, model string, localInferenceOverride string) ProviderSpec {
	provider = strings.ToLower(strings.TrimSpace(provider))
	tier = canonicalModelTier(tier)
	localInferenceOverride = strings.TrimSpace(localInferenceOverride)
	providerConfig := configuredProvider(provider)
	if model == "" {
		model = strings.TrimSpace(providerConfig.Model)
	}
	temperature := configuredTemperature(providerConfig, 0.1)

	switch provider {
	case "local":
		baseURL := localInferenceOverride
		if baseURL == "" {
			baseURL = envOrConfig("LOCAL_INFERENCE_URL", providerConfig.BaseURL, "http://host.docker.internal:11434/v1")
		}
		endpoint := configOrDefault(providerConfig.Endpoint, "/chat/completions")
		return ProviderSpec{
			Provider:    "local",
			BaseURL:     baseURL,
			BaseURLs:    LocalInferenceBaseURLs(baseURL),
			Model:       model,
			Endpoint:    endpoint,
			Mode:        ModeOpenAIish,
			Temperature: temperature,
		}
	case "anthropic":
		return ProviderSpec{
			Provider:    "anthropic",
			BaseURL:     envOrConfig("ANTHROPIC_BASE_URL", providerConfig.BaseURL, "https://api.anthropic.com"),
			APIKey:      envOrConfig("ANTHROPIC_API_KEY", providerConfig.APIKey, ""),
			Model:       model,
			Endpoint:    configOrDefault(providerConfig.Endpoint, "/v1/messages"),
			Mode:        ModeAnthropic,
			Temperature: temperature,
		}
	case "openai":
		return ProviderSpec{
			Provider:    "openai",
			BaseURL:     envOrConfig("OPENAI_BASE_URL", providerConfig.BaseURL, "https://api.openai.com/v1"),
			APIKey:      envOrConfig("OPENAI_API_KEY", providerConfig.APIKey, ""),
			Model:       model,
			Endpoint:    configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:        ModeOpenAIish,
			Temperature: temperature,
		}
	case "grok":
		return ProviderSpec{
			Provider:    "grok",
			BaseURL:     envOrConfig("GROK_BASE_URL", providerConfig.BaseURL, "https://api.x.ai/v1"),
			APIKey:      envOrConfig("GROK_API_KEY", providerConfig.APIKey, ""),
			Model:       model,
			Endpoint:    configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:        ModeOpenAIish,
			Temperature: temperature,
		}
	case "google":
		return ProviderSpec{
			Provider:    "google",
			BaseURL:     envOrConfig("GOOGLE_BASE_URL", providerConfig.BaseURL, "https://generativelanguage.googleapis.com/v1beta/openai"),
			APIKey:      envOrConfig("GOOGLE_API_KEY", providerConfig.APIKey, ""),
			Model:       model,
			Endpoint:    configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:        ModeOpenAIish,
			Temperature: temperature,
		}
	case "openrouter":
		return ProviderSpec{
			Provider:    "openrouter",
			BaseURL:     envOrConfig("OPENROUTER_BASE_URL", providerConfig.BaseURL, "https://openrouter.ai/api/v1"),
			APIKey:      envOrConfig("OPENROUTER_API_KEY", providerConfig.APIKey, ""),
			Model:       model,
			Endpoint:    configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:        ModeOpenAIish,
			Temperature: temperature,
		}
	case "nvidia":
		return ProviderSpec{
			Provider:    "nvidia",
			BaseURL:     envOrConfig("NVIDIA_BASE_URL", providerConfig.BaseURL, "https://integrate.api.nvidia.com/v1"),
			APIKey:      envOrConfig("NVIDIA_API_KEY", providerConfig.APIKey, ""),
			Model:       model,
			Endpoint:    configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:        ModeOpenAIish,
			Temperature: temperature,
		}
	default:
		baseURL := localInferenceOverride
		if baseURL == "" {
			baseURL = envOrConfig("LOCAL_INFERENCE_URL", providerConfig.BaseURL, "http://host.docker.internal:11434/v1")
		}
		return ProviderSpec{
			Provider:    "local",
			BaseURL:     baseURL,
			BaseURLs:    LocalInferenceBaseURLs(baseURL),
			Model:       model,
			Endpoint:    configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:        ModeOpenAIish,
			Temperature: temperature,
		}
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envOrConfig(key, configured string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	return fallback
}

func configOrDefault(configured string, fallback string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	return fallback
}

func configuredTemperature(config tendrilProviderConfig, fallback float64) float64 {
	if config.Temperature != 0 {
		return config.Temperature
	}
	return fallback
}

func configuredDefaultProvider() string {
	return strings.ToLower(strings.TrimSpace(loadTendrilConfig().LLM.DefaultProvider))
}

func configuredProvider(provider string) tendrilProviderConfig {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return tendrilProviderConfig{}
	}
	providers := loadTendrilConfig().LLM.Providers
	if providers == nil {
		return tendrilProviderConfig{}
	}
	for name, config := range providers {
		if strings.EqualFold(strings.TrimSpace(name), provider) {
			return config
		}
	}
	return tendrilProviderConfig{}
}

func configuredModelForProvider(provider string) string {
	return strings.TrimSpace(configuredProvider(provider).Model)
}

func loadTendrilConfig() tendrilConfig {
	path := findTendrilConfigPath()
	if path == "" {
		return tendrilConfig{}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return tendrilConfig{}
	}

	var config tendrilConfig
	if err := yaml.Unmarshal(content, &config); err != nil {
		return tendrilConfig{}
	}
	return config
}

func findTendrilConfigPath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, ".tendril", "config.yaml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
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

func explicitModelForTier(provider string, tier ModelTier) (string, bool) {
	if model := strings.TrimSpace(os.Getenv(tierSpecificModelEnvName(provider, tier))); model != "" {
		return model, true
	}
	if model := strings.TrimSpace(os.Getenv(providerModelEnvName(provider))); model != "" {
		return model, true
	}
	if model := strings.TrimSpace(os.Getenv("DEFAULT_MODEL_NAME")); model != "" {
		return model, true
	}
	return "", false
}

func LocalInferenceBaseURLs(baseURL string) []string {
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
	return c.doCall(ctx, baseURL, messages, false, nil)
}

func (c *Client) listModelsAtBaseURL(ctx context.Context, baseURL string) ([]string, error) {
	// Anthropic's Models API lives at /v1/models and authenticates with the
	// x-api-key + anthropic-version headers — its base URL carries no version
	// segment and it rejects Bearer auth. The OpenAI-shaped providers bake the
	// version into their base URL and use a Bearer token. Without this split,
	// Anthropic discovery hit api.anthropic.com/models (a 404) and silently fell
	// back to the static registry on every call, making that registry the only
	// source of Anthropic model selection.
	modelsPath := "/models"
	if c.spec.Mode == ModeAnthropic {
		modelsPath = "/v1/models"
	}
	url := strings.TrimRight(baseURL, "/") + modelsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	if c.spec.APIKey != "" {
		if c.spec.Mode == ModeAnthropic {
			req.Header.Set("x-api-key", c.spec.APIKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+c.spec.APIKey)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}

	models := make([]string, 0, len(decoded.Data))
	for _, model := range decoded.Data {
		id := strings.TrimSpace(model.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("models response contained no model ids")
	}
	return models, nil
}

func (c *Client) doCall(ctx context.Context, baseURL string, messages []Message, stream bool, tokenChan chan<- string) (string, error) {
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
			"stream":      stream,
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
			"stream":      stream,
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

	if stream {
		scanner := bufio.NewScanner(resp.Body)
		var fullContent strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "[DONE]" {
				break
			}

			if c.spec.Mode == ModeAnthropic {
				var event struct {
					Type  string `json:"type"`
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(dataStr), &event); err == nil {
					if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
						text := event.Delta.Text
						fullContent.WriteString(text)
						if tokenChan != nil {
							tokenChan <- text
						}
					}
				}
			} else {
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
					if len(chunk.Choices) > 0 {
						text := chunk.Choices[0].Delta.Content
						if text != "" {
							fullContent.WriteString(text)
							if tokenChan != nil {
								tokenChan <- text
							}
						}
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return fullContent.String(), fmt.Errorf("error reading stream: %w", err)
		}
		return fullContent.String(), nil
	}

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
