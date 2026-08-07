package llm

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type ModelDefinition struct {
	Provider    string
	Name        string
	Family      ModelFamily
	ContextSize int
	// OutputLimit is the maximum number of output tokens this model accepts on
	// a single request. Zero means the model's own provider default applies.
	// For Anthropic, where max_tokens is required by the API, the adapter uses
	// anthropicOutputFallback when this is zero (see clientadapter.go).
	// Source: docs.anthropic.com/en/docs/about-claude/models/overview
	OutputLimit  int
	HasVision    bool
	HasReasoning bool
	// DrivesTools marks a model that reliably follows the tool-calling
	// protocol. Frontier hosted models and large instruct models do; small
	// local models (e.g. a 3B llama3.2) and code-completion-tuned models do
	// not — measured, they return prose or an empty completion and the sprout
	// matures having done nothing.
	//
	// This is a property of the model, not of the endpoint serving it. An
	// endpoint that accepts a tools field is not evidence that the model
	// behind it emits calls that parse: a 3B llama behind Ollama accepts the
	// field and still answers in prose. See ProviderSpec.AcceptsToolDefinitions
	// for the separate endpoint-level property.
	DrivesTools bool
	CostTier    ModelTier
}

// FallbackModels preserves capability metadata for providers that do not expose a models API.
// It is a curated snapshot of current-generation, tool-capable models per provider — the set
// the router selects from whenever live discovery is unavailable. Entries must be model names
// the provider actually serves today: a retired name here means every auto-selected request
// fails at the provider with a model-not-found error.
var FallbackModels = []ModelDefinition{
	// OutputLimit values sourced from docs.anthropic.com/en/docs/about-claude/models/overview
	{Provider: "anthropic", Name: "claude-opus-4-8", Family: ModelFamilyClaude, ContextSize: 1000000, OutputLimit: 128000, HasVision: true, HasReasoning: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "anthropic", Name: "claude-sonnet-5", Family: ModelFamilyClaude, ContextSize: 1000000, OutputLimit: 128000, HasVision: true, HasReasoning: true, DrivesTools: true, CostTier: TierStandard},
	{Provider: "anthropic", Name: "claude-haiku-4-5", Family: ModelFamilyClaude, ContextSize: 200000, OutputLimit: 64000, HasVision: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "openai", Name: "gpt-5.6-terra", Family: ModelFamilyGPT, ContextSize: 400000, HasVision: true, HasReasoning: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "openai", Name: "gpt-5.6-luna", Family: ModelFamilyGPT, ContextSize: 400000, HasVision: true, HasReasoning: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "google", Name: "gemini-2.5-pro", Family: ModelFamilyGemini, ContextSize: 1000000, HasVision: true, HasReasoning: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "google", Name: "gemini-3.5-flash", Family: ModelFamilyGemini, ContextSize: 1000000, HasVision: true, HasReasoning: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "grok", Name: "grok-4.5", Family: ModelFamilyGPT, ContextSize: 256000, HasVision: true, HasReasoning: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "openrouter", Name: "google/gemini-2.5-flash", Family: ModelFamilyGemini, ContextSize: 1000000, HasVision: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "nvidia", Name: "meta/llama-3.1-405b-instruct", Family: ModelFamilyLlama, ContextSize: 128000, DrivesTools: true, CostTier: TierPremium},
	{Provider: "nvidia", Name: "meta/llama-3.3-70b-instruct", Family: ModelFamilyLlama, ContextSize: 128000, DrivesTools: true, CostTier: TierStandard},
	// Local models: only qwen3.5:9b reliably drives tools (measured). A 3B
	// llama3.2 and the code-completion-tuned qwen2.5-coder models do not, so
	// they must never be auto-selected for an autonomous sprout.
	{Provider: "local", Name: "qwen3.5:9b", Family: ModelFamilyQwen, ContextSize: 128000, DrivesTools: true, CostTier: TierStandard},
	{Provider: "local", Name: "llama3.2", Family: ModelFamilyLlama, ContextSize: 128000, CostTier: TierCheapest},
	{Provider: "local", Name: "qwen2.5-coder:7b", Family: ModelFamilyQwen, ContextSize: 128000, CostTier: TierStandard},
	{Provider: "local", Name: "qwen2.5-coder:14b", Family: ModelFamilyQwen, ContextSize: 128000, CostTier: TierPremium},
}

// ErrNoModelAvailable reports that nothing in the registry satisfied the
// requested capabilities. Callers match on it with errors.Is to tell "the
// operator asked for something unreachable" from a transport failure.
var ErrNoModelAvailable = errors.New("no available model satisfies capabilities")

// LocalProviderAvailable reports whether a local inference endpoint has been
// pointed at.
//
// Every hosted provider announces itself with an API key: the key is both the
// credential and the operator's statement that the provider is set up. The
// local provider has no key, so the equivalent statement is the endpoint —
// LOCAL_INFERENCE_URL, LOCAL_MODEL_NAME, or a `local` block under
// llm.providers in .tendril/config.yaml. Without one of those, nothing has
// said a local inference server exists, and offering local models as
// candidates makes a model that can never answer the default choice.
//
// Availability is decided from configuration, never probed over the network. A
// probe would put an HTTP request inside model selection — slow, and worse,
// non-deterministic: whether a run picked a model would depend on whether a
// server happened to answer within a timeout. A declared endpoint that is down
// fails at the request, where the error names the endpoint, rather than
// silently changing which model was chosen.
func LocalProviderAvailable() bool {
	if strings.TrimSpace(os.Getenv("LOCAL_INFERENCE_URL")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv(providerModelEnvName("local"))) != "" {
		return true
	}
	return hasConfiguredProvider("local")
}

func AvailableProviders() []string {
	providers := make([]string, 0, 7)
	if LocalProviderAvailable() {
		providers = append(providers, "local")
	}
	candidates := []struct {
		provider string
		key      string
	}{
		{provider: "anthropic", key: "ANTHROPIC_API_KEY"},
		{provider: "openai", key: "OPENAI_API_KEY"},
		{provider: "google", key: "GOOGLE_API_KEY"},
		{provider: "grok", key: "GROK_API_KEY"},
		{provider: "openrouter", key: "OPENROUTER_API_KEY"},
		{provider: "nvidia", key: "NVIDIA_API_KEY"},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(os.Getenv(candidate.key)) != "" {
			providers = append(providers, candidate.provider)
		}
	}
	return providers
}

func activeModelRegistry() []ModelDefinition {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	if len(modelRegistryCache) > 0 && time.Since(modelRegistryLoaded) < ModelRegistryCacheTTL {
		return append([]ModelDefinition(nil), modelRegistryCache...)
	}
	return append([]ModelDefinition(nil), FallbackModels...)
}

func SelectBestModel(caps Capabilities) (ModelDefinition, error) {
	return SelectBestModelFromRegistry(caps, activeModelRegistry())
}

func SelectBestModelFromRegistry(caps Capabilities, registry []ModelDefinition) (ModelDefinition, error) {
	providers := AvailableProviders()
	available := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		available[strings.ToLower(strings.TrimSpace(provider))] = struct{}{}
	}

	// A named provider is a filter on the candidate set, not a starting point
	// the search may wander away from. Checked before the loop so that a
	// provider nobody can reach is reported as exactly that, rather than as the
	// capability mismatch it would otherwise look like once every one of its
	// models had been filtered out.
	wanted := strings.ToLower(strings.TrimSpace(caps.Provider))
	if wanted != "" {
		if _, ok := available[wanted]; !ok {
			return ModelDefinition{}, fmt.Errorf("%w: provider %q is not available (no API key, and no inference endpoint configured for it)", ErrNoModelAvailable, wanted)
		}
	}

	matches := make([]ModelDefinition, 0, len(registry))
	for _, model := range registry {
		if _, ok := available[strings.ToLower(strings.TrimSpace(model.Provider))]; !ok {
			continue
		}
		if wanted != "" && !strings.EqualFold(strings.TrimSpace(model.Provider), wanted) {
			continue
		}
		if caps.RequiresVision && !model.HasVision {
			continue
		}
		if caps.RequiresReasoning && !model.HasReasoning {
			continue
		}
		if caps.RequiresToolUse && !model.DrivesTools {
			continue
		}
		if caps.MinContextSize > 0 && model.ContextSize < caps.MinContextSize {
			continue
		}
		if caps.MaxCostTier != "" && compareCostTier(model.CostTier, caps.MaxCostTier) > 0 {
			continue
		}
		matches = append(matches, model)
	}

	if len(matches) == 0 {
		if wanted != "" {
			return ModelDefinition{}, fmt.Errorf("%w: provider %q serves no model that %s", ErrNoModelAvailable, wanted, describeCapabilities(caps))
		}
		return ModelDefinition{}, fmt.Errorf("%w: no available provider serves a model that %s", ErrNoModelAvailable, describeCapabilities(caps))
	}

	// MaxCostTier is a CEILING, so the best model at or below it is the one the
	// caller asked for. Sorting the other way made the ceiling select against
	// itself: asking for premium admitted every cheaper model and then actively
	// preferred them, so the tier a caller chose could never be the tier it
	// got, and a request assessed as complex was answered by the cheapest model
	// on the shelf. Ties inside a tier keep registry order, which keeps the
	// choice deterministic.
	sort.SliceStable(matches, func(i, j int) bool {
		return costTierRank(matches[i].CostTier) > costTierRank(matches[j].CostTier)
	})
	return matches[0], nil
}

// describeCapabilities renders the constraints a selection could not satisfy,
// so the failure names what was asked for rather than only that it failed.
func describeCapabilities(caps Capabilities) string {
	parts := make([]string, 0, 5)
	if caps.RequiresToolUse {
		parts = append(parts, "drives tools")
	}
	if caps.RequiresVision {
		parts = append(parts, "has vision")
	}
	if caps.RequiresReasoning {
		parts = append(parts, "has reasoning")
	}
	if caps.MinContextSize > 0 {
		parts = append(parts, fmt.Sprintf("holds a %d-token context", caps.MinContextSize))
	}
	if caps.MaxCostTier != "" {
		parts = append(parts, fmt.Sprintf("costs at most the %s tier", canonicalModelTier(caps.MaxCostTier)))
	}
	if len(parts) == 0 {
		return "is registered at all"
	}
	return strings.Join(parts, ", ")
}

func compareCostTier(left ModelTier, right ModelTier) int {
	leftRank := costTierRank(left)
	rightRank := costTierRank(right)
	switch {
	case leftRank < rightRank:
		return -1
	case leftRank > rightRank:
		return 1
	default:
		return 0
	}
}

func costTierRank(tier ModelTier) int {
	switch canonicalModelTier(tier) {
	case TierCheapest:
		return 1
	case TierStandard:
		return 2
	default:
		return 3
	}
}
