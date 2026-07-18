package llm

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type ModelDefinition struct {
	Provider     string
	Name         string
	Family       ModelFamily
	ContextSize  int
	HasVision    bool
	HasReasoning bool
	// DrivesTools marks a model that reliably follows the tool-calling
	// protocol an autonomous sprout depends on. Frontier hosted models and
	// large instruct models do; small local models (e.g. a 3B llama3.2) and
	// code-completion-tuned models do not — measured, they return prose or an
	// empty completion and the sprout matures having done nothing.
	DrivesTools bool
	CostTier    ModelTier
}

// FallbackModels preserves capability metadata for providers that do not expose a models API.
var FallbackModels = []ModelDefinition{
	{Provider: "anthropic", Name: "claude-3-5-sonnet", Family: ModelFamilyClaude, ContextSize: 200000, HasVision: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "anthropic", Name: "claude-3-5-haiku", Family: ModelFamilyClaude, ContextSize: 200000, HasVision: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "openai", Name: "gpt-4o", Family: ModelFamilyGPT, ContextSize: 128000, HasVision: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "openai", Name: "o1-mini", Family: ModelFamilyGPT, ContextSize: 128000, HasReasoning: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "openai", Name: "gpt-4o-mini", Family: ModelFamilyGPT, ContextSize: 128000, HasVision: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "google", Name: "gemini-1.5-pro", Family: ModelFamilyGemini, ContextSize: 2000000, HasVision: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "google", Name: "gemini-1.5-flash", Family: ModelFamilyGemini, ContextSize: 1000000, HasVision: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "grok", Name: "grok-beta", Family: ModelFamilyGPT, ContextSize: 128000, DrivesTools: true, CostTier: TierPremium},
	{Provider: "openrouter", Name: "google/gemini-1.5-flash", Family: ModelFamilyGemini, ContextSize: 1000000, HasVision: true, DrivesTools: true, CostTier: TierCheapest},
	{Provider: "opentendril", Name: "anthropic/claude-3.5-sonnet", Family: ModelFamilyClaude, ContextSize: 200000, HasVision: true, DrivesTools: true, CostTier: TierPremium},
	{Provider: "nvidia", Name: "meta/llama-3.1-405b-instruct", Family: ModelFamilyLlama, ContextSize: 128000, DrivesTools: true, CostTier: TierPremium},
	{Provider: "nvidia", Name: "meta/llama-3.1-70b-instruct", Family: ModelFamilyLlama, ContextSize: 128000, DrivesTools: true, CostTier: TierStandard},
	// Local models: only qwen3.5:9b reliably drives tools (measured). A 3B
	// llama3.2 and the code-completion-tuned qwen2.5-coder models do not, so
	// they must never be auto-selected for an autonomous sprout.
	{Provider: "local", Name: "qwen3.5:9b", Family: ModelFamilyQwen, ContextSize: 128000, DrivesTools: true, CostTier: TierStandard},
	{Provider: "local", Name: "llama3.2", Family: ModelFamilyLlama, ContextSize: 128000, CostTier: TierCheapest},
	{Provider: "local", Name: "qwen2.5-coder:7b", Family: ModelFamilyQwen, ContextSize: 128000, CostTier: TierStandard},
	{Provider: "local", Name: "qwen2.5-coder:14b", Family: ModelFamilyQwen, ContextSize: 128000, CostTier: TierPremium},
}

func AvailableProviders() []string {
	providers := []string{"local"}
	candidates := []struct {
		provider string
		key      string
	}{
		{provider: "anthropic", key: "ANTHROPIC_API_KEY"},
		{provider: "openai", key: "OPENAI_API_KEY"},
		{provider: "google", key: "GOOGLE_API_KEY"},
		{provider: "grok", key: "GROK_API_KEY"},
		{provider: "openrouter", key: "OPENROUTER_API_KEY"},
		{provider: "opentendril", key: "OPENTENDRIL_API_KEY"},
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

	matches := make([]ModelDefinition, 0, len(registry))
	for _, model := range registry {
		if _, ok := available[strings.ToLower(strings.TrimSpace(model.Provider))]; !ok {
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
		return ModelDefinition{}, fmt.Errorf("no available model satisfies capabilities")
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return costTierRank(matches[i].CostTier) < costTierRank(matches[j].CostTier)
	})
	return matches[0], nil
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
