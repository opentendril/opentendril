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
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Mode string

const (
	ModeAnthropic Mode = "anthropic"
	ModeOpenAIish Mode = "openaiish"
)

// DefaultOutputFallback is used when neither a tier environment variable, an
// operator config, nor the model registry declares an explicit output-token limit.
// 8192 is chosen because:
//   - A realistic file-write tool call produces 2-4 KB of JSON as output tokens;
//     the previous value (2048) was too small for the native-tool path.
//   - It leaves meaningful headroom above a single large write without
//     inventing a ceiling that no model declaration asked for.
//   - It is small enough that a misconfigured model name is rejected quickly
//     rather than expensively.
const DefaultOutputFallback = 8192

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
	// EndpointResolution is the canonical local-provider endpoint state. It is
	// retained on the resolved spec so request execution, model discovery and
	// preflight can consume the same endpoint contract.
	EndpointResolution LocalEndpointResolution
	APIKey             string
	Model              string
	Endpoint           string
	Mode               Mode
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
	// OutputLimit is the maximum number of output tokens to request. Zero is
	// only allowed for manually constructed specs in tests, while resolved specs
	// should carry a governed value (defaulting to DefaultOutputFallback).
	OutputLimit int
	// CeilingSource is a string describing where the OutputLimit came from (e.g., "provider-specific env var", "general tier env var", "yaml config", "registry", "compiled fallback").
	CeilingSource string
	// Tier is the resolved ModelTier used to fetch the model and limits.
	Tier ModelTier
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

// Usage carries provider-native token and cost metrics for one request.
// Fields are pointers to explicitly preserve honest absence; zero is a valid
// measured value but nil means the provider did not supply it.
// CostAmount uses a lossless decimal string representation.
type Usage struct {
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int

	CostAmount     *string
	CostUnit       *string
	CostProvenance *string
}

// Result is one parsed non-streaming response. ToolCalls is always empty until
// an adapter learns to parse them; a caller reading it today gets nothing, not
// a mistake.
type Result struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
}

// StreamDelta is one parsed fragment of a streamed response. Exactly one of its
// fields is meaningful per fragment. ToolCall is always nil until an adapter
// learns to accumulate tool-call fragments, which is where a tool call streamed
// in pieces gets reassembled — no other layer sees the pieces.
type StreamDelta struct {
	Text             string
	ToolCallFragment string
	ToolCalls        []ToolCall
	Usage            Usage
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
	if strings.EqualFold(strings.TrimSpace(spec.Provider), "local") {
		if strings.TrimSpace(spec.BaseURL) != "" &&
			strings.TrimSpace(spec.EndpointResolution.EffectiveEndpoint) != "" &&
			strings.TrimSpace(spec.BaseURL) != strings.TrimSpace(spec.EndpointResolution.EffectiveEndpoint) {
			ApplyExplicitBaseURLOverride(&spec, spec.BaseURL)
		}
		if spec.EndpointResolution.EffectiveEndpoint == "" {
			spec.EndpointResolution = resolutionForExplicitSpec(spec)
		}
		if spec.BaseURL == "" {
			spec.BaseURL = spec.EndpointResolution.EffectiveEndpoint
		}
		if len(spec.BaseURLs) == 0 {
			spec.BaseURLs = spec.EndpointResolution.candidateURLs()
		}
	}
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		spec:       spec,
		adapter:    adapterForMode(spec.Mode),
	}
}

// EndpointResolution returns a copy of the endpoint state used by this
// client's requests and model discovery.
func (c *Client) EndpointResolution() LocalEndpointResolution {
	if c == nil {
		return LocalEndpointResolution{}
	}
	resolution := c.spec.EndpointResolution
	resolution.Candidates = append([]string(nil), resolution.Candidates...)
	return resolution
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
	return ResolveModelProviderSpecForContext(provider, model, HostLocalEndpointContext())
}

// ResolveModelProviderSpecForContext resolves a provider using the supplied
// caller context for any local endpoint it may need.
func ResolveModelProviderSpecForContext(provider string, model string, context LocalEndpointContext) ProviderSpec {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" {
		return ResolveTierProviderSpecForContext(TierPremium, context)
	}
	if model != "" {
		return providerSpecForModelInContext(provider, TierPremium, model, "", context)
	}
	return carryResolutionFailure(resolveForProviderChoiceInContext(
		providerChoice{name: provider, explicit: true}, TierPremium, false, context,
	))
}

func NewCoordinatorClientFromEnv() *Client {
	return NewClient(ResolveCoordinatorProviderSpec())
}

func ResolveLocalProviderSpec() ProviderSpec {
	return ResolveLocalProviderSpecForContext(HostLocalEndpointContext())
}

// ResolveLocalProviderSpecForCaller resolves a local provider for the named
// caller and establishes the default container alias only when it resolves.
func ResolveLocalProviderSpecForCaller(caller EndpointCaller) ProviderSpec {
	return ResolveLocalProviderSpecForContext(localEndpointContextForCaller(caller))
}

// ResolveLocalProviderSpecForContext resolves a local provider using the
// caller-side endpoint facts supplied by the caller.
func ResolveLocalProviderSpecForContext(context LocalEndpointContext) ProviderSpec {
	return resolveTierProviderSpecForProviderInContext("local", TierPremium, "", context)
}

func (c *Client) CallPromptWithResult(ctx context.Context, systemPrompt string, userPrompt string) (Result, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	return c.CallWithResult(ctx, messages)
}

func (c *Client) CallPrompt(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	res, err := c.CallPromptWithResult(ctx, systemPrompt, userPrompt)
	return res.Text, err
}

func (c *Client) CallStreamPromptWithResult(ctx context.Context, systemPrompt string, userPrompt string, tokenChan chan<- string) (Result, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	return c.CallStreamWithResult(ctx, messages, tokenChan)
}

func (c *Client) CallStreamPrompt(ctx context.Context, systemPrompt string, userPrompt string, tokenChan chan<- string) (string, error) {
	res, err := c.CallStreamPromptWithResult(ctx, systemPrompt, userPrompt, tokenChan)
	return res.Text, err
}

func (c *Client) CallWithResult(ctx context.Context, messages []Message) (Result, error) {
	return c.CallStreamWithResult(ctx, messages, nil)
}

// ProbeAuthentication issues the smallest authenticated chat request this
// client already knows how to send. Stem uses it to discover a provider
// authentication rejection without a second HTTP client or a Terrarium.
func (c *Client) ProbeAuthentication(ctx context.Context) error {
	_, err := c.CallWithResult(ctx, []Message{{Role: "user", Content: "ok"}})
	return err
}

func (c *Client) Call(ctx context.Context, messages []Message) (string, error) {
	res, err := c.CallWithResult(ctx, messages)
	return res.Text, err
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

	candidates := c.endpointCandidates()

	var primaryErr error
	for _, baseURL := range candidates {
		models, err := c.listModelsAtBaseURL(ctx, baseURL)
		if err == nil {
			return models, nil
		}
		primaryErr = preferAttemptError(primaryErr, err)
	}
	if primaryErr == nil {
		primaryErr = fmt.Errorf("list models failed for provider %q", c.spec.Provider)
	}
	return nil, primaryErr
}

func (c *Client) CallStreamWithResult(ctx context.Context, messages []Message, tokenChan chan<- string) (Result, error) {
	return c.callInternal(ctx, messages, nil, tokenChan)
}

func (c *Client) CallStream(ctx context.Context, messages []Message, tokenChan chan<- string) (string, error) {
	res, err := c.CallStreamWithResult(ctx, messages, tokenChan)
	return res.Text, err
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

	candidates := c.endpointCandidates()

	var primaryErr error
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
		primaryErr = preferAttemptError(primaryErr, err)
	}

	if primaryErr == nil {
		primaryErr = fmt.Errorf("llm request failed for provider %q", c.spec.Provider)
	}

	return Result{}, primaryErr
}

func ResolveProviderSpec() ProviderSpec {
	return ResolveTierProviderSpec(TierPremium)
}

func ResolveTierProviderSpec(tier ModelTier) ProviderSpec {
	return ResolveTierProviderSpecForContext(tier, HostLocalEndpointContext())
}

// ResolveTierProviderSpecForContext resolves a tier using caller-side local
// endpoint facts supplied by the caller.
func ResolveTierProviderSpecForContext(tier ModelTier, context LocalEndpointContext) ProviderSpec {
	return resolveTierProviderSpecWithCapsInContext(tier, false, context)
}

// ResolveAgentTierProviderSpec resolves the tier default for an autonomous
// Sprout run. It behaves like ResolveTierProviderSpec but, when selection falls
// through to the registry's best model, requires a tool-capable one — so a
// no-session sprout never silently lands on a model that cannot drive tools
// (e.g. a 3B local llama that returns an empty completion). Explicit env/config
// model choices are still honoured, since those are a deliberate override.
func ResolveAgentTierProviderSpec(tier ModelTier) ProviderSpec {
	return ResolveAgentTierProviderSpecForContext(tier, HostLocalEndpointContext())
}

// ResolveAgentTierProviderSpecForContext resolves an autonomous tier using
// caller-side local endpoint facts supplied by the caller.
func ResolveAgentTierProviderSpecForContext(tier ModelTier, context LocalEndpointContext) ProviderSpec {
	return resolveTierProviderSpecWithCapsInContext(tier, true, context)
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
	return resolveTierProviderSpecWithCapsInContext(tier, requireTools, HostLocalEndpointContext())
}

func resolveTierProviderSpecWithCapsInContext(tier ModelTier, requireTools bool, context LocalEndpointContext) ProviderSpec {
	return carryResolutionFailure(resolveForProviderChoiceInContext(configuredProviderChoice(), tier, requireTools, context))
}

func resolveForProviderChoice(choice providerChoice, tier ModelTier, requireTools bool) (ProviderSpec, error) {
	return resolveForProviderChoiceInContext(choice, tier, requireTools, HostLocalEndpointContext())
}

func resolveForProviderChoiceInContext(choice providerChoice, tier ModelTier, requireTools bool, context LocalEndpointContext) (ProviderSpec, error) {
	tier = canonicalModelTier(tier)

	if model, ok := explicitModelForTier(choice.name, tier); ok {
		return providerSpecForModelInContext(choice.name, tier, model, "", context), nil
	}
	if model := configuredModelForProvider(choice.name); model != "" {
		return providerSpecForModelInContext(choice.name, tier, model, "", context), nil
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

		// Say so. This escalation is correct — a run that cannot drive tools
		// does no work at all — but it changes what a run costs, and it fired
		// on every autonomous growth for as long as the cheapest tier held no
		// model declared tool-capable. Nothing reported it, so the only way to
		// notice was to compare a run record against the configuration and then
		// probe the resolver across model discovery. An escalation nobody can
		// see is indistinguishable from a tier that was never honoured.
		if err == nil {
			fmt.Fprintf(os.Stderr,
				"⚠️ no model at the %s tier declares tool capability; raising the cost ceiling and selecting %q instead. "+
					"Tool capability is kept ahead of cost deliberately — but this run costs more than the tier implies.\n",
				tier, model.Name)
		}

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
		return providerSpecForModelInContext(choice.name, tier, "", "", context), resolutionFailure(choice, tier, err)
	}

	return providerSpecForModelInContext(model.Provider, tier, model.Name, "", context), nil
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
				ApplyExplicitBaseURLOverride(&spec, baseURL)
			}
		}
		if val := strings.TrimSpace(os.Getenv("MYCORRHIZA_COORDINATOR_MAX_OUTPUT_TOKENS")); val != "" {
			if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
				spec.OutputLimit = limit
				spec.CeilingSource = "coordinator override env var"
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
	if val := strings.TrimSpace(os.Getenv("MYCORRHIZA_COORDINATOR_MAX_OUTPUT_TOKENS")); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			spec.OutputLimit = limit
			spec.CeilingSource = "coordinator override env var"
		}
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
	return resolveTierProviderSpecForProviderInContext(provider, tier, localInferenceOverride, HostLocalEndpointContext())
}

func resolveTierProviderSpecForProviderInContext(provider string, tier ModelTier, localInferenceOverride string, context LocalEndpointContext) ProviderSpec {
	provider = strings.ToLower(strings.TrimSpace(provider))
	tier = canonicalModelTier(tier)
	localInferenceOverride = strings.TrimSpace(localInferenceOverride)
	model, _ := explicitModelForTier(provider, tier)
	return providerSpecForModelInContext(provider, tier, model, localInferenceOverride, context)
}

func providerSpecForModel(provider string, tier ModelTier, model string, localInferenceOverride string) ProviderSpec {
	return providerSpecForModelInContext(provider, tier, model, localInferenceOverride, HostLocalEndpointContext())
}

func providerSpecForModelInContext(provider string, tier ModelTier, model string, localInferenceOverride string, context LocalEndpointContext) ProviderSpec {
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
	outputLimit, ceilingSource := resolveOutputLimit(provider, tier, providerConfig, model)
	localProviderSpec := func() ProviderSpec {
		endpointResolution := resolveLocalEndpoint(context, localInferenceOverride, os.Getenv("LOCAL_INFERENCE_URL"), providerConfig.BaseURL)
		endpoint := configOrDefault(providerConfig.Endpoint, "/chat/completions")
		spec := ProviderSpec{
			Provider:               "local",
			BaseURL:                endpointResolution.EffectiveEndpoint,
			BaseURLs:               endpointResolution.candidateURLs(),
			EndpointResolution:     endpointResolution,
			Model:                  model,
			Endpoint:               endpoint,
			Mode:                   ModeOpenAIish,
			Temperature:            temperature,
			IsRouter:               isRouter,
			AcceptsToolDefinitions: acceptsToolDefs,
			OutputLimit:            outputLimit,
			CeilingSource:          ceilingSource,
			Tier:                   tier,
		}
		if endpointResolution.EffectiveEndpoint == "" {
			spec.ResolutionErr = newProviderReachabilityError(spec, "", ReachabilityFailureConnection, errors.New(endpointResolution.SynthesisReason))
		}
		return spec
	}

	switch provider {
	case "local":
		return localProviderSpec()
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
			CeilingSource:          ceilingSource,
			Tier:                   tier,
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
			CeilingSource:          ceilingSource,
			Tier:                   tier,
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
			CeilingSource:          ceilingSource,
			Tier:                   tier,
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
			CeilingSource:          ceilingSource,
			Tier:                   tier,
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
			CeilingSource:          ceilingSource,
			Tier:                   tier,
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
			CeilingSource:          ceilingSource,
			Tier:                   tier,
		}
	default:
		return localProviderSpec()
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

// resolveOutputLimit returns the output-token limit to carry on a ProviderSpec, and its source.
// It resolves the ceiling by following this precedence (first configured source wins):
//  1. Provider-specific tier env var (primary, then alias)
//  2. General tier env var (primary, then alias)
//  3. Configured output-limit from YAML config
//  4. Registry limit
//  5. Compiled fallback (DefaultOutputFallback)
//
// A configured value larger than the registry limit is used as-is; the operator's
// stated intent wins, but a warning is written to stderr so the mismatch is visible.
func resolveOutputLimit(provider string, tier ModelTier, cfg tendrilProviderConfig, modelName string) (int, string) {
	registryLimit := 0
	for _, m := range activeModelRegistry() {
		if m.Name == modelName {
			registryLimit = m.OutputLimit
			break
		}
	}

	envTierPrimary, envTierAlias := tierEnvNames(tier)
	providerEnvPrefix := strings.ToUpper(strings.TrimSpace(provider))

	// 1. Provider-specific tier env var (primary, then alias)
	if val := os.Getenv(fmt.Sprintf("MYCORRHIZA_%s_%s_MAX_OUTPUT_TOKENS", providerEnvPrefix, envTierPrimary)); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			return checkConfiguredLimit(limit, registryLimit, modelName), "provider-specific tier env var (primary)"
		}
	}
	if envTierAlias != "" {
		if val := os.Getenv(fmt.Sprintf("MYCORRHIZA_%s_%s_MAX_OUTPUT_TOKENS", providerEnvPrefix, envTierAlias)); val != "" {
			if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
				return checkConfiguredLimit(limit, registryLimit, modelName), "provider-specific tier env var (alias)"
			}
		}
	}

	// 2. General tier env var (primary, then alias)
	if val := os.Getenv(fmt.Sprintf("MYCORRHIZA_%s_MAX_OUTPUT_TOKENS", envTierPrimary)); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			return checkConfiguredLimit(limit, registryLimit, modelName), "general tier env var (primary)"
		}
	}
	if envTierAlias != "" {
		if val := os.Getenv(fmt.Sprintf("MYCORRHIZA_%s_MAX_OUTPUT_TOKENS", envTierAlias)); val != "" {
			if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
				return checkConfiguredLimit(limit, registryLimit, modelName), "general tier env var (alias)"
			}
		}
	}

	// 3. YAML config
	if cfg.OutputLimit > 0 {
		return checkConfiguredLimit(cfg.OutputLimit, registryLimit, modelName), "yaml config"
	}

	// 4. Registry limit
	if registryLimit > 0 {
		return registryLimit, "registry"
	}

	// 5. Compiled fallback
	return DefaultOutputFallback, "compiled fallback"
}

func checkConfiguredLimit(configuredLimit, registryLimit int, modelName string) int {
	if registryLimit > 0 && configuredLimit > registryLimit {
		fmt.Fprintf(os.Stderr,
			"warning: configured output-limit %d for model %q exceeds registry limit %d;"+
				" the configured value will be sent; the provider may reject it if it is over the model's hard cap\n",
			configuredLimit, modelName, registryLimit)
	}
	return configuredLimit
}

func tierEnvNames(tier ModelTier) (primary, alias string) {
	switch canonicalModelTier(tier) {
	case TierCheapest:
		return "CHEAPEST", "FAST"
	case TierStandard:
		return "STANDARD", ""
	case TierPremium:
		return "PREMIUM", "POWER"
	default:
		return "PREMIUM", "POWER"
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
	// This helper remains for source compatibility. Endpoint resolution owns
	// caller-aware selection; this compatibility path never invents aliases.
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	return []string{baseURL}
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
		return nil, newProviderReachabilityError(c.spec, baseURL, classifyTransportFailure(err), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, newRequestErrorAt(resp.StatusCode, string(body), c.spec, nil, baseURL)
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
		return Result{}, newProviderReachabilityError(c.spec, baseURL, classifyTransportFailure(err), err)
	}
	defer resp.Body.Close()

	// A streamed non-200 previously returned no error, so a rejected turn read as
	// an empty answer and failover never advanced. Both paths check it here.
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return Result{}, fmt.Errorf("read llm response: %w", err)
		}

		// This status pair establishes two things and no more: the request was
		// turned away for its own contents rather than "not now" or "I am
		// broken", and it carried tool definitions. Which of the two the
		// endpoint objected to is not knowable here — a rejected parameter and
		// a rejected definition arrive as the same status — so the error names
		// the coincidence and leaves the cause to a caller that can test it.
		// Wrapped rather than replaced so the provider's own message, which is
		// usually the whole answer, still reaches the operator.
		if len(tools) > 0 && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) {
			return Result{}, newRequestErrorAt(resp.StatusCode, string(body), c.spec, ErrRejectedWithTools, baseURL)
		}

		return Result{}, newRequestErrorAt(resp.StatusCode, string(body), c.spec, nil, baseURL)
	}

	if stream {
		scanner := bufio.NewScanner(resp.Body)
		var fullContent strings.Builder
		var toolCalls []ToolCall
		var accumulatedUsage Usage
		decoder := c.adapter.NewStreamDecoder(c.spec)

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

				if delta.Usage.PromptTokens != nil {
					accumulatedUsage.PromptTokens = delta.Usage.PromptTokens
				}
				if delta.Usage.CompletionTokens != nil {
					accumulatedUsage.CompletionTokens = delta.Usage.CompletionTokens
				}
				if delta.Usage.TotalTokens != nil {
					accumulatedUsage.TotalTokens = delta.Usage.TotalTokens
				}
				if delta.Usage.CostAmount != nil {
					accumulatedUsage.CostAmount = delta.Usage.CostAmount
					accumulatedUsage.CostUnit = delta.Usage.CostUnit
					accumulatedUsage.CostProvenance = delta.Usage.CostProvenance
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return Result{Text: fullContent.String(), ToolCalls: toolCalls, Usage: accumulatedUsage}, fmt.Errorf("error reading stream: %w", err)
		}
		if err := decoder.Finalize(); err != nil {
			return Result{Text: fullContent.String(), ToolCalls: toolCalls, Usage: accumulatedUsage}, err
		}
		return Result{Text: fullContent.String(), ToolCalls: toolCalls, Usage: accumulatedUsage}, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("read llm response: %w", err)
	}

	res, err := c.adapter.ParseResponse(c.spec, body)
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func anthropicTextBlock(text string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": text,
	}
}

// annotateCacheControl adds an ephemeral cache_control entry to block in place
// and returns block, allowing it to be used inline as a slice element.
func annotateCacheControl(block map[string]any) map[string]any {
	block["cache_control"] = map[string]string{"type": "ephemeral"}
	return block
}

// anthropicMessagePayload converts a Message to the wire shape Anthropic
// expects. Tool-result messages return a bare tool_result block. All other
// messages always return block form — never a bare string — so that serialisation
// is byte-deterministic regardless of content length. cache_control markers are
// not set here; BuildChatRequest injects them positionally after the full message
// slice is assembled.
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
		contentBlocks = append(contentBlocks, anthropicTextBlock(msg.Content))
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

	return map[string]any{
		"role":    msg.Role,
		"content": contentBlocks,
	}
}
