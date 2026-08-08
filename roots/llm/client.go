package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Provider string
	BaseURL  string
	BaseURLs []string
	APIKey   string
	Model    string
	Endpoint string
	Mode     Mode
	// Temperature is the sampling temperature to send on every request. Nil means
	// the field is omitted from the wire and the provider's own default applies.
	// Set explicitly via temperature in .tendril/config.yaml or SetTemperature.
	Temperature *float64
	// IsRouter, when true, signals that this provider delegates model selection
	// to a third-party router (e.g. OpenRouter, NVIDIA NIM router). Set
	// explicitly via the `is-router` field in .tendril/config.yaml; it
	// overrides the string-matching heuristic in IsThirdPartyRouterModel.
	IsRouter bool
	// AcceptsToolDefinitions, when explicitly false, signals that this
	// endpoint cannot accept the native tool-calling protocol and should be
	// driven with the prose protocol instead. Unset (nil) means attempt native.
	AcceptsToolDefinitions *bool
	// OutputLimit is the maximum number of output tokens to request. Zero means
	// the provider's own default applies (the right answer for OpenAI-shaped
	// families; see BuildChatRequest in clientadapter.go). For Anthropic, where
	// max_tokens is required, the adapter substitutes the package fallback when
	// this is zero.
	OutputLimit int
	// ResolutionErr records why resolution could not name a model to use. It is
	// carried on the spec rather than returned because resolution happens
	// inside constructors that have no error return and many callers; a spec
	// that carries it names no model, and every request made through it fails
	// with this error instead of reaching a provider nobody chose.
	ResolutionErr error
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ErrRejectedWithTools reports that an endpoint returned a client error (400 or
// 422) on a request that carried tool definitions. It establishes only that:
// the request was turned away and it carried definitions. It is not proof the
// definitions were the cause — the caller must establish that through a probe.
// It is returned rather than being handled here: this client speaks the wire
// protocol, and which protocol should carry the turn instead is a decision only
// the caller holding the conversation can make.
//
// Only 400 and 422 raise it. A 429 or a 5xx says the endpoint is busy or
// broken, not that it cannot take tools, and treating those as a refusal would
// demote a run to the prose protocol over a transient failure — a quality loss
// announced as a capability finding.
var ErrRejectedWithTools = errors.New("endpoint returned a client error on a request carrying tool definitions")

// ToolDefinition declares one tool to a provider. The shape is the OpenAI
// family's, because Message is marshalled straight onto that family's wire and
// the two have to agree; the Anthropic adapter builds its own payload and
// translates. Parameters carries a JSON Schema — the one description of a
// tool's inputs both families accept.
type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the named half of a ToolDefinition. It is a named type rather
// than an inline struct so a caller can construct one without restating the
// field set; the conductor builds these for every tool on every turn.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
}

// ToolCall is one call a mind made. Arguments is a JSON string rather than a
// decoded object because that is what the OpenAI family puts on the wire and
// what its streaming path delivers in fragments; an adapter whose provider
// carries a decoded object translates at its own boundary.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the named half of a ToolCall, named for the same reason
// as ToolFunction.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Result is one parsed non-streaming response. ToolCalls is always empty until
// an adapter learns to parse them; a caller reading it today gets nothing, not
// a mistake.
type Result struct {
	Text      string
	ToolCalls []ToolCall
}

// StreamDelta is one parsed fragment of a streamed response. Exactly one of its
// fields is meaningful per fragment. ToolCall is always nil until an adapter
// learns to accumulate tool-call fragments, which is where a tool call streamed
// in pieces gets reassembled — no other layer sees the pieces.
type StreamDelta struct {
	Text             string
	ToolCallFragment string
	ToolCalls        []ToolCall
}

type Client struct {
	httpClient *http.Client
	spec       ProviderSpec
	adapter    providerAdapter
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
	// IsRouter, when true, marks this provider as a third-party router so that
	// ShouldBypassInternalRouter returns true regardless of the model name. Set
	// to false to explicitly prevent bypass even when the model name would match
	// the string-matching heuristic (e.g. a self-hosted proxy whose model is
	// coincidentally named "my-router"). Unset (zero value) defers the decision
	// to the existing string-matching heuristic, preserving zero-config behavior.
	IsRouter *bool `yaml:"is-router"`
	// AcceptsToolDefinitions, when explicitly false, signals that this
	// endpoint cannot accept native tool definitions. Unset defers to an attempt.
	AcceptsToolDefinitions *bool `yaml:"accepts-tool-definitions"`
	// OutputLimit, when non-zero, overrides the model-registry declared limit
	// for this provider. A configured value larger than the registry limit is
	// used as-is; the operator's stated intent wins, but a warning is written
	// to stderr so the mismatch is visible before Anthropic rejects the request.
	OutputLimit int                  `yaml:"output-limit"`
	Models      []tendrilModelConfig `yaml:"models"`
}

// tendrilModelConfig declares one model's fallback/capability metadata for a
// provider in .tendril/config.yaml, overriding the compiled-in
// FallbackModels table for that provider. Only Name is required — any other
// field left at its zero value is filled in by inferCapabilitiesFromName
// (registry.go), the same heuristic used to enrich a live-discovered name
// with no compiled-in match.
type tendrilModelConfig struct {
	Name         string      `yaml:"name"`
	Family       ModelFamily `yaml:"family"`
	ContextSize  int         `yaml:"context-size"`
	HasVision    bool        `yaml:"has-vision"`
	HasReasoning bool        `yaml:"has-reasoning"`
	DrivesTools  bool        `yaml:"drives-tools"`
	CostTier     ModelTier   `yaml:"cost-tier"`
}

func (c *Client) SetTemperature(temp float64) {
	if c != nil {
		t := temp
		c.spec.Temperature = &t
	}
}

// ToolDefinitionsCapable returns true if the endpoint can accept native tool
// definitions. It is a pure query; if explicitly false, the conductor decides
// how to announce the fallback.
func (c *Client) ToolDefinitionsCapable() bool {
	if c == nil {
		return false
	}
	if c.spec.AcceptsToolDefinitions != nil && !*c.spec.AcceptsToolDefinitions {
		return false
	}
	return true
}

// Provider reports the provider this client will actually send to. It is the
// resolved value, which is not always the configured one and is never the
// requested one when nothing was requested.
func (c *Client) Provider() string {
	if c == nil {
		return ""
	}
	return c.spec.Provider
}

// Model reports the model name this client will actually put on the wire.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.spec.Model
}

// ResolutionError reports why this client has no model to call, or nil when it
// has one. A caller that can refuse to start expensive work checks it first;
// one that cannot gets the same error from the first request.
func (c *Client) ResolutionError() error {
	if c == nil {
		return fmt.Errorf("llm client is nil")
	}
	return c.spec.ResolutionErr
}

func NewClient(spec ProviderSpec) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		spec:       spec,
		adapter:    adapterForMode(spec.Mode),
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

// ResolveModelProviderSpec resolves a spec for a caller that has named a
// provider, and optionally a model. A named provider with no model is still a
// choice of provider: the model is selected from what that provider serves, and
// the resolution fails rather than answering with somebody else's model.
func ResolveModelProviderSpec(provider string, model string) ProviderSpec {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" {
		return ResolveTierProviderSpec(TierPremium)
	}
	if model != "" {
		return providerSpecForModel(provider, TierPremium, model, "")
	}
	return carryResolutionFailure(resolveForProviderChoice(
		providerChoice{name: provider, explicit: true}, TierPremium, false,
	))
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
	if c.spec.ResolutionErr != nil {
		return nil, c.spec.ResolutionErr
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
	result, err := c.callInternal(ctx, messages, nil, tokenChan)
	return result.Text, err
}

func (c *Client) CallWithTools(ctx context.Context, messages []Message, tools []ToolDefinition, tokenChan chan<- string) (Result, error) {
	return c.callInternal(ctx, messages, tools, tokenChan)
}

func (c *Client) callInternal(ctx context.Context, messages []Message, tools []ToolDefinition, tokenChan chan<- string) (Result, error) {
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
		return Result{}, fmt.Errorf("llm client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Checked before anything else about the spec: a resolution failure already
	// knows which provider was asked for and what it was missing, and the
	// checks below would replace that with a vaguer symptom of it.
	if c.spec.ResolutionErr != nil {
		return Result{}, c.spec.ResolutionErr
	}
	if c.spec.BaseURL == "" {
		return Result{}, fmt.Errorf("no LLM base URL configured for provider %q", c.spec.Provider)
	}
	if c.spec.Model == "" {
		return Result{}, fmt.Errorf("no LLM model configured for provider %q", c.spec.Provider)
	}
	if c.spec.Provider != "local" && strings.TrimSpace(c.spec.APIKey) == "" {
		return Result{}, fmt.Errorf("no API key configured for provider %q", c.spec.Provider)
	}

	candidates := c.spec.BaseURLs
	if len(candidates) == 0 {
		candidates = []string{c.spec.BaseURL}
	}

	var lastErr error
	for _, baseURL := range candidates {
		result, err := c.doCall(ctx, baseURL, messages, tools, tokenChan != nil, tokenChan)
		if err == nil {
			return result, nil
		}
		// A client error is the request's fault. The candidates are one
		// endpoint reached at several addresses, so offering the same request
		// to the next address only earns the same answer — and spends a
		// request doing it.
		if errors.Is(err, ErrRejectedWithTools) {
			return Result{}, err
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("llm request failed for provider %q", c.spec.Provider)
	}

	return Result{}, lastErr
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

// providerChoice is the provider a resolution will run against, and whether an
// operator actually said so.
type providerChoice struct {
	name string
	// explicit is true when the name came from DEFAULT_LLM_PROVIDER or from
	// llm.default-provider in .tendril/config.yaml. It is false when the name
	// was guessed from whichever credential happened to be present, which is a
	// starting point rather than an instruction and must not constrain
	// selection.
	explicit bool
}

func configuredProviderChoice() providerChoice {
	if provider := strings.ToLower(strings.TrimSpace(os.Getenv("DEFAULT_LLM_PROVIDER"))); provider != "" {
		return providerChoice{name: provider, explicit: true}
	}
	if provider := configuredDefaultProvider(); provider != "" {
		return providerChoice{name: provider, explicit: true}
	}
	return providerChoice{name: detectProviderFallback()}
}

// carryResolutionFailure announces a resolution failure and attaches it to the
// spec. The two answer different questions: the warning tells an operator
// watching the process that their configuration selected nothing, at the moment
// it did, while the carried error makes the next request fail with that reason
// instead of leaving somewhere downstream to report a missing model as if no
// provider had ever been named.
func carryResolutionFailure(spec ProviderSpec, err error) ProviderSpec {
	if err == nil {
		return spec
	}
	fmt.Fprintf(os.Stderr, "⚠️ %v\n", err)
	spec.ResolutionErr = err
	return spec
}

func resolveTierProviderSpecWithCaps(tier ModelTier, requireTools bool) ProviderSpec {
	return carryResolutionFailure(resolveForProviderChoice(configuredProviderChoice(), tier, requireTools))
}

func resolveForProviderChoice(choice providerChoice, tier ModelTier, requireTools bool) (ProviderSpec, error) {
	tier = canonicalModelTier(tier)

	if model, ok := explicitModelForTier(choice.name, tier); ok {
		return providerSpecForModel(choice.name, tier, model, ""), nil
	}
	if model := configuredModelForProvider(choice.name); model != "" {
		return providerSpecForModel(choice.name, tier, model, ""), nil
	}

	caps := Capabilities{MaxCostTier: tier, RequiresToolUse: requireTools}
	if choice.explicit {
		caps.Provider = choice.name
	}

	model, err := SelectBestModel(caps)
	if err != nil && requireTools {
		// Nothing tool-capable fits under the ceiling. The two constraints are
		// not equal here and the order of giving them up matters: an autonomous
		// run that cannot drive tools does not do cheap work, it does NO work —
		// it returns prose or an empty completion and matures having achieved
		// nothing. A run that costs more than intended still does the work.
		//
		// So cost gives way first. Raising the ceiling while keeping the tool
		// requirement is what a local-only installation needs: its one
		// tool-capable model is standard-tier, so any cheaper ceiling would
		// otherwise select a model that cannot drive tools at all.
		raised := caps
		raised.MaxCostTier = ""
		model, err = SelectBestModel(raised)

		if err != nil {
			// Nothing tool-capable exists at this provider at any price. Relax
			// the requirement last, so the run reports its outcome honestly
			// instead of refusing outright — but relax ONLY that. caps.Provider
			// stays set, because leaving the chosen provider is the very thing
			// this resolution exists to prevent, and a fallback allowed to do it
			// is the same defect wearing a different name.
			relaxed := caps
			relaxed.RequiresToolUse = false
			model, err = SelectBestModel(relaxed)
		}
	}
	if err != nil {
		return providerSpecForModel(choice.name, tier, "", ""), resolutionFailure(choice, tier, err)
	}

	return providerSpecForModel(model.Provider, tier, model.Name, ""), nil
}

// resolutionFailure explains a selection that produced no model, naming the
// provider, where the provider came from, and how to pin a model for it.
func resolutionFailure(choice providerChoice, tier ModelTier, cause error) error {
	if choice.explicit {
		return fmt.Errorf(
			"llm provider %q is configured (DEFAULT_LLM_PROVIDER or llm.default-provider) but no model could be resolved for the %s tier: %w; pin one with %s or llm.providers.%s.model",
			choice.name, canonicalModelTier(tier), cause, providerModelEnvName(choice.name), choice.name,
		)
	}
	return fmt.Errorf(
		"no llm provider is configured and no model could be resolved for the %s tier: %w; set DEFAULT_LLM_PROVIDER and the matching API key, or point LOCAL_INFERENCE_URL at a local inference endpoint",
		canonicalModelTier(tier), cause,
	)
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
	temperature := configuredTemperature(providerConfig)
	isRouter := resolveIsRouter(providerConfig)
	acceptsToolDefs := resolveAcceptsToolDefinitions(providerConfig)
	outputLimit := resolveOutputLimit(providerConfig, model)

	switch provider {
	case "local":
		baseURL := localInferenceOverride
		if baseURL == "" {
			baseURL = envOrConfig("LOCAL_INFERENCE_URL", providerConfig.BaseURL, "http://host.docker.internal:11434/v1")
		}
		endpoint := configOrDefault(providerConfig.Endpoint, "/chat/completions")
		return ProviderSpec{
			Provider:               "local",
			BaseURL:                baseURL,
			BaseURLs:               LocalInferenceBaseURLs(baseURL),
			Model:                  model,
			Endpoint:               endpoint,
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	case "anthropic":
		return ProviderSpec{
			Provider:               "anthropic",
			BaseURL:                envOrConfig("ANTHROPIC_BASE_URL", providerConfig.BaseURL, "https://api.anthropic.com"),
			APIKey:                 envOrConfig("ANTHROPIC_API_KEY", providerConfig.APIKey, ""),
			Model:                  model,
			Endpoint:               configOrDefault(providerConfig.Endpoint, "/v1/messages"),
			Mode:                   ModeAnthropic,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	case "openai":
		return ProviderSpec{
			Provider:               "openai",
			BaseURL:                envOrConfig("OPENAI_BASE_URL", providerConfig.BaseURL, "https://api.openai.com/v1"),
			APIKey:                 envOrConfig("OPENAI_API_KEY", providerConfig.APIKey, ""),
			Model:                  model,
			Endpoint:               configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	case "grok":
		return ProviderSpec{
			Provider:               "grok",
			BaseURL:                envOrConfig("GROK_BASE_URL", providerConfig.BaseURL, "https://api.x.ai/v1"),
			APIKey:                 envOrConfig("GROK_API_KEY", providerConfig.APIKey, ""),
			Model:                  model,
			Endpoint:               configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	case "google":
		return ProviderSpec{
			Provider:               "google",
			BaseURL:                envOrConfig("GOOGLE_BASE_URL", providerConfig.BaseURL, "https://generativelanguage.googleapis.com/v1beta/openai"),
			APIKey:                 envOrConfig("GOOGLE_API_KEY", providerConfig.APIKey, ""),
			Model:                  model,
			Endpoint:               configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	case "openrouter":
		return ProviderSpec{
			Provider:               "openrouter",
			BaseURL:                envOrConfig("OPENROUTER_BASE_URL", providerConfig.BaseURL, "https://openrouter.ai/api/v1"),
			APIKey:                 envOrConfig("OPENROUTER_API_KEY", providerConfig.APIKey, ""),
			Model:                  model,
			Endpoint:               configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	case "nvidia":
		return ProviderSpec{
			Provider:               "nvidia",
			BaseURL:                envOrConfig("NVIDIA_BASE_URL", providerConfig.BaseURL, "https://integrate.api.nvidia.com/v1"),
			APIKey:                 envOrConfig("NVIDIA_API_KEY", providerConfig.APIKey, ""),
			Model:                  model,
			Endpoint:               configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	default:
		baseURL := localInferenceOverride
		if baseURL == "" {
			baseURL = envOrConfig("LOCAL_INFERENCE_URL", providerConfig.BaseURL, "http://host.docker.internal:11434/v1")
		}
		return ProviderSpec{
			Provider:               "local",
			BaseURL:                baseURL,
			BaseURLs:               LocalInferenceBaseURLs(baseURL),
			Model:                  model,
			Endpoint:               configOrDefault(providerConfig.Endpoint, "/chat/completions"),
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
		}
	}
}

// resolveIsRouter extracts the optional is-router flag from a provider config.
// It returns false when the field is not set (nil pointer), preserving the
// zero-config behaviour where the string-matching heuristic in
// IsThirdPartyRouterModel is the sole decision maker.
func resolveIsRouter(cfg tendrilProviderConfig) bool {
	if cfg.IsRouter == nil {
		return false
	}
	return *cfg.IsRouter
}

func resolveAcceptsToolDefinitions(cfg tendrilProviderConfig) *bool {
	return cfg.AcceptsToolDefinitions
}

// resolveOutputLimit returns the output-token limit to carry on a ProviderSpec.
// Config wins over the registry; when both are set and the configured value
// exceeds the declared registry limit, a warning is written to stderr. The
// configured value is still used because the operator's stated intent beats a
// table that docs/DESIGN-ROOTS-LLM.md already flags as going stale. If
// Anthropic rejects the value, the resulting 400 is visible and classified by
// the error path added in the earlier HTTP-status fix.
func resolveOutputLimit(cfg tendrilProviderConfig, modelName string) int {
	registryLimit := 0
	for _, m := range activeModelRegistry() {
		if m.Name == modelName {
			registryLimit = m.OutputLimit
			break
		}
	}
	if cfg.OutputLimit > 0 {
		if registryLimit > 0 && cfg.OutputLimit > registryLimit {
			fmt.Fprintf(os.Stderr,
				"warning: configured output-limit %d for model %q exceeds registry limit %d;"+
					" the configured value will be sent; Anthropic will reject it if it is over the model's hard cap\n",
				cfg.OutputLimit, modelName, registryLimit)
		}
		return cfg.OutputLimit
	}
	return registryLimit
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

// configuredTemperature returns a pointer to the operator-configured temperature
// when one is set, or nil when the config field is zero (meaning no temperature
// was configured). Nil propagates to the adapter, which omits the field from the
// request body so the provider's own default applies.
//
// Note: YAML 0.0 and an absent key both parse to float64(0), so a deliberate
// zero cannot be expressed through config. This is a known limitation.
func configuredTemperature(config tendrilProviderConfig) *float64 {
	if config.Temperature != 0 {
		t := config.Temperature
		return &t
	}
	return nil
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

// hasConfiguredProvider reports whether .tendril/config.yaml declares a block
// for this provider at all, whatever it puts in it. Writing the block is the
// operator saying the provider exists for them, which is the statement an API
// key makes for the providers that have one.
func hasConfiguredProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	for name := range loadTendrilConfig().LLM.Providers {
		if strings.EqualFold(strings.TrimSpace(name), provider) {
			return true
		}
	}
	return false
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
	result, err := c.doCall(ctx, baseURL, messages, nil, false, nil)
	return result.Text, err
}

func (c *Client) listModelsAtBaseURL(ctx context.Context, baseURL string) ([]string, error) {
	// Anthropic's Models API lives at /v1/models and authenticates with the
	// x-api-key + anthropic-version headers — its base URL carries no version
	// segment and it rejects Bearer auth. The OpenAI-shaped providers bake the
	// version into their base URL and use a Bearer token. Without this split,
	// Anthropic discovery hit api.anthropic.com/models (a 404) and silently fell
	// back to the static registry on every call, making that registry the only
	// source of Anthropic model selection.
	url := strings.TrimRight(baseURL, "/") + c.adapter.ModelsPath()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	c.adapter.SetModelsAuthHeaders(req, c.spec.APIKey)

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

func (c *Client) doCall(ctx context.Context, baseURL string, messages []Message, tools []ToolDefinition, stream bool, tokenChan chan<- string) (Result, error) {
	var (
		url = strings.TrimRight(baseURL, "/") + c.spec.Endpoint
		req *http.Request
	)

	payload, err := c.adapter.BuildChatRequest(c.spec, messages, tools, stream)
	if err != nil {
		return Result{}, err
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("create chat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	c.adapter.SetChatHeaders(req, c.spec)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	// A streamed non-200 previously returned no error, so a rejected turn read as
	// an empty answer and failover never advanced. Both paths check it here.
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return Result{}, fmt.Errorf("read llm response: %w", err)
		}

		// A request carrying tool definitions turned away with a schema
		// rejection is the one status pair that means "I cannot take these",
		// as opposed to "not now" or "I am broken". Wrapped rather than
		// replaced so the provider's own message still reaches the operator.
		if len(tools) > 0 && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) {
			return Result{}, fmt.Errorf("%w: llm returned %d: %s", ErrRejectedWithTools, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		return Result{}, fmt.Errorf("llm returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if stream {
		scanner := bufio.NewScanner(resp.Body)
		var fullContent strings.Builder
		var toolCalls []ToolCall
		decoder := c.adapter.NewStreamDecoder()

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

			if delta, ok := decoder.ParseChunk(dataStr); ok {
				if delta.Text != "" {
					fullContent.WriteString(delta.Text)
					if tokenChan != nil {
						tokenChan <- delta.Text
					}
				}
				if delta.ToolCallFragment != "" && tokenChan != nil {
					tokenChan <- delta.ToolCallFragment
				}
				if len(delta.ToolCalls) > 0 {
					toolCalls = append(toolCalls, delta.ToolCalls...)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return Result{Text: fullContent.String(), ToolCalls: toolCalls}, fmt.Errorf("error reading stream: %w", err)
		}
		if err := decoder.Finalize(); err != nil {
			return Result{Text: fullContent.String(), ToolCalls: toolCalls}, err
		}
		return Result{Text: fullContent.String(), ToolCalls: toolCalls}, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read llm response: %w", err)
	}

	res, err := c.adapter.ParseResponse(body)
	if err != nil {
		return Result{}, err
	}
	return res, nil
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

func anthropicMessagePayload(msg Message) map[string]any {
	var contentBlocks []map[string]any

	if msg.ToolCallID != "" {
		return map[string]any{
			"role": msg.Role,
			"content": []map[string]any{
				{
					"type":        "tool_result",
					"tool_use_id": msg.ToolCallID,
					"content":     msg.Content,
				},
			},
		}
	}

	if msg.Content != "" {
		contentBlocks = append(contentBlocks, anthropicTextBlock(msg.Content, shouldCacheAnthropicContent(msg.Content)))
	}
	for _, call := range msg.ToolCalls {
		var inputObj map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &inputObj); err != nil {
			inputObj = make(map[string]any)
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Function.Name,
			"input": inputObj,
		})
	}

	if len(contentBlocks) == 1 && msg.Content != "" && !shouldCacheAnthropicContent(msg.Content) {
		return map[string]any{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}

	return map[string]any{
		"role":    msg.Role,
		"content": contentBlocks,
	}
}

func shouldCacheAnthropicContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	return strings.Contains(trimmed, "repomap.md") || len(trimmed) > 1000
}
