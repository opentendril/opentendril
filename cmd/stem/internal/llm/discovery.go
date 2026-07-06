package llm

import (
	"context"
	"strings"
	"sync"
	"time"
)

const ModelRegistryCacheTTL = 24 * time.Hour

var (
	modelRegistryMu      sync.RWMutex
	modelRegistryCache   []ModelDefinition
	modelRegistryLoaded  time.Time
	modelDiscoveryOnce   sync.Once
	modelDiscoveryClient = NewClient
)

func StartModelDiscovery() {
	modelDiscoveryOnce.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, _ = DiscoverAvailableModels(ctx)
		}()
	})
}

func ResetModelRegistryCache() {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	modelRegistryCache = nil
	modelRegistryLoaded = time.Time{}
}

func GetModelRegistry(ctx context.Context) []ModelDefinition {
	if ctx == nil {
		ctx = context.Background()
	}

	modelRegistryMu.RLock()
	if len(modelRegistryCache) > 0 && time.Since(modelRegistryLoaded) < ModelRegistryCacheTTL {
		cache := append([]ModelDefinition(nil), modelRegistryCache...)
		modelRegistryMu.RUnlock()
		return cache
	}
	modelRegistryMu.RUnlock()

	models, err := DiscoverAvailableModels(ctx)
	if err != nil || len(models) == 0 {
		return append([]ModelDefinition(nil), FallbackModels...)
	}
	return models
}

func DiscoverAvailableModels(ctx context.Context) ([]ModelDefinition, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	modelRegistryMu.RLock()
	if len(modelRegistryCache) > 0 && time.Since(modelRegistryLoaded) < ModelRegistryCacheTTL {
		cache := append([]ModelDefinition(nil), modelRegistryCache...)
		modelRegistryMu.RUnlock()
		return cache, nil
	}
	modelRegistryMu.RUnlock()

	discovered := make([]ModelDefinition, 0)
	seen := make(map[string]struct{})

	for _, provider := range AvailableProviders() {
		provider = strings.ToLower(strings.TrimSpace(provider))
		spec := discoveryProviderSpec(provider)
		if spec.BaseURL == "" {
			continue
		}

		client := modelDiscoveryClient(spec)
		names, err := client.ListModels(ctx)
		if err != nil || len(names) == 0 {
			for _, fallback := range fallbackModelsForProvider(provider) {
				key := modelRegistryKey(fallback.Provider, fallback.Name)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				discovered = append(discovered, fallback)
			}
			continue
		}

		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			key := modelRegistryKey(provider, name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			discovered = append(discovered, enrichModelDefinition(provider, name))
		}
	}

	if len(discovered) == 0 {
		discovered = append([]ModelDefinition(nil), FallbackModels...)
	}

	modelRegistryMu.Lock()
	modelRegistryCache = append([]ModelDefinition(nil), discovered...)
	modelRegistryLoaded = time.Now()
	modelRegistryMu.Unlock()

	return append([]ModelDefinition(nil), discovered...), nil
}

func discoveryProviderSpec(provider string) ProviderSpec {
	provider = strings.ToLower(strings.TrimSpace(provider))
	spec := providerSpecForModel(provider, TierPremium, "", "")
	if provider != "local" && strings.TrimSpace(spec.APIKey) == "" {
		return ProviderSpec{}
	}
	return spec
}

func fallbackModelsForProvider(provider string) []ModelDefinition {
	provider = strings.ToLower(strings.TrimSpace(provider))
	matches := make([]ModelDefinition, 0)
	for _, model := range FallbackModels {
		if strings.EqualFold(model.Provider, provider) {
			matches = append(matches, model)
		}
	}
	return matches
}

func modelRegistryKey(provider, name string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "::" + strings.ToLower(strings.TrimSpace(name))
}

func enrichModelDefinition(provider, name string) ModelDefinition {
	provider = strings.ToLower(strings.TrimSpace(provider))
	name = strings.TrimSpace(name)

	for _, fallback := range FallbackModels {
		if strings.EqualFold(fallback.Provider, provider) && strings.EqualFold(fallback.Name, name) {
			return fallback
		}
	}

	definition := ModelDefinition{
		Provider: provider,
		Name:     name,
	}
	inferCapabilitiesFromName(&definition)
	return definition
}

func inferCapabilitiesFromName(definition *ModelDefinition) {
	if definition == nil {
		return
	}

	normalized := strings.ToLower(definition.Name)
	switch {
	case strings.Contains(normalized, "claude"):
		definition.Family = ModelFamilyClaude
	case strings.Contains(normalized, "gemini"):
		definition.Family = ModelFamilyGemini
	case strings.Contains(normalized, "llama"):
		definition.Family = ModelFamilyLlama
	case strings.Contains(normalized, "qwen"):
		definition.Family = ModelFamilyQwen
	default:
		definition.Family = ModelFamilyGPT
	}

	if definition.ContextSize == 0 {
		switch definition.Family {
		case ModelFamilyGemini:
			definition.ContextSize = 1000000
		case ModelFamilyClaude:
			definition.ContextSize = 200000
		default:
			definition.ContextSize = 128000
		}
	}

	if !definition.HasVision {
		definition.HasVision = strings.Contains(normalized, "vision") ||
			strings.Contains(normalized, "4o") ||
			strings.Contains(normalized, "gemini") ||
			strings.Contains(normalized, "claude-3") ||
			strings.Contains(normalized, "llava") ||
			strings.Contains(normalized, "pixtral")
	}

	if !definition.HasReasoning {
		definition.HasReasoning = strings.Contains(normalized, "o1") ||
			strings.Contains(normalized, "o3") ||
			strings.Contains(normalized, "reason") ||
			strings.Contains(normalized, "think") ||
			strings.Contains(normalized, "r1") ||
			strings.Contains(normalized, "opus") ||
			strings.Contains(normalized, "grok-3")
	}

	if definition.CostTier == "" {
		switch {
		case strings.Contains(normalized, "mini") ||
			strings.Contains(normalized, "flash") ||
			strings.Contains(normalized, "haiku") ||
			strings.Contains(normalized, "nano"):
			definition.CostTier = TierCheapest
		case strings.Contains(normalized, "opus") ||
			strings.Contains(normalized, "o1") ||
			strings.Contains(normalized, "o3") ||
			strings.Contains(normalized, "70b") ||
			strings.Contains(normalized, "pro"):
			definition.CostTier = TierPremium
		default:
			definition.CostTier = TierStandard
		}
	}
}