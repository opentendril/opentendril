package llm

import (
	"os"
	"strings"
)

type RouteSelection struct {
	Provider string
	Model    string
}

func HasStrictModelConstraint(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, tier := range []ModelTier{TierPremium, TierStandard, TierCheapest} {
		if _, ok := explicitModelForTier(provider, tier); ok {
			return true
		}
	}
	if model := configuredModelForProvider(provider); model != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("DEFAULT_MODEL_NAME")) != "" {
		return true
	}
	return false
}

func ShouldBypassInternalRouter() bool {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("DEFAULT_LLM_PROVIDER")))
	if provider == "" {
		provider = configuredDefaultProvider()
	}
	if provider == "" {
		provider = detectProviderFallback()
	}

	if HasStrictModelConstraint(provider) {
		return true
	}

	model := configuredModelForProvider(provider)
	if model == "" {
		if pinned, ok := explicitModelForTier(provider, TierPremium); ok {
			model = pinned
		}
	}
	return IsThirdPartyRouterModel(model)
}

func IsThirdPartyRouterModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	if normalized == "openrouter/auto" || normalized == "auto" {
		return true
	}
	if strings.Contains(normalized, "router") && (strings.HasPrefix(normalized, "nvidia/") || strings.Contains(normalized, "nvidia")) {
		return true
	}
	return false
}

func ShouldUseDynamicRouter(registry []ModelDefinition) bool {
	if ShouldBypassInternalRouter() {
		return false
	}
	return countRoutableOptions(registry) > 1
}

func countRoutableOptions(registry []ModelDefinition) int {
	providers := AvailableProviders()
	available := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		available[strings.ToLower(strings.TrimSpace(provider))] = struct{}{}
	}

	seen := make(map[string]struct{})
	for _, model := range registry {
		provider := strings.ToLower(strings.TrimSpace(model.Provider))
		if _, ok := available[provider]; !ok {
			continue
		}
		name := strings.TrimSpace(model.Name)
		if name == "" || IsThirdPartyRouterModel(name) {
			continue
		}
		key := provider + "::" + name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
	}
	return len(seen)
}

func DefaultRouteSelection() RouteSelection {
	spec := ResolveTierProviderSpec(TierPremium)
	return RouteSelection{
		Provider: spec.Provider,
		Model:    spec.Model,
	}
}

func ResolveRouteSelection(selection RouteSelection) ProviderSpec {
	provider := strings.ToLower(strings.TrimSpace(selection.Provider))
	model := strings.TrimSpace(selection.Model)
	if provider == "" {
		return ResolveTierProviderSpec(TierPremium)
	}
	return providerSpecForModel(provider, TierPremium, model, "")
}
