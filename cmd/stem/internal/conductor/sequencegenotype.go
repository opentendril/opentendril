package conductor

import (
	"context"
	"strings"

	"github.com/opentendril/opentendril/roots/llm"
)

func stepGenotype(stepID string) string {
	trimmed := strings.TrimSpace(stepID)
	normalized := strings.ToLower(trimmed)

	switch {
	case isMeristemStep(trimmed):
		return "meristem"
	case strings.Contains(normalized, "debugger"):
		return "debugger"
	case strings.Contains(normalized, "macrophage"):
		return "macrophage"
	case strings.Contains(normalized, "verifier"):
		return "verifier"
	case strings.Contains(normalized, "thinker"):
		return "thinker"
	default:
		return trimmed
	}
}

func fallbackStepModelTier(stepID string) llm.ModelTier {
	normalized := strings.ToLower(strings.TrimSpace(stepID))
	switch {
	case isMeristemStep(stepID):
		return llm.TierPremium
	case strings.Contains(normalized, "verifier"):
		return llm.TierStandard
	case strings.Contains(normalized, "macrophage"):
		return llm.TierStandard
	case strings.Contains(normalized, "debugger"):
		return llm.TierStandard
	case strings.Contains(normalized, "compiler"):
		return llm.TierStandard
	case strings.Contains(normalized, "compile"):
		return llm.TierStandard
	default:
		return llm.TierPremium
	}
}

type stepLLMSelection struct {
	Tier     llm.ModelTier
	Provider string
	Model    string
	BaseURL  string
}

func resolveStepLLMSelection(ctx context.Context, step *SequenceStep) stepLLMSelection {
	if step == nil {
		return stepLLMSelection{Tier: llm.TierPremium}
	}

	if provider := strings.TrimSpace(step.ModelProvider); provider != "" {
		model := strings.TrimSpace(step.ModelName)
		baseURL := strings.TrimSpace(step.ModelBaseURL)
		if model != "" || baseURL != "" {
			return stepLLMSelection{Provider: provider, Model: model, BaseURL: baseURL, Tier: llm.TierPremium}
		}
	}

	if isMeristemStep(step.ID) {
		return stepLLMSelection{Tier: llm.TierPremium}
	}

	fallbackTier := fallbackStepModelTier(step.ID)
	if fallbackTier != llm.TierPremium {
		return stepLLMSelection{Tier: fallbackTier}
	}

	caps := llm.Capabilities{}
	if step.RequiresReasoning {
		caps.RequiresReasoning = true
	}
	if step.RequiresVision {
		caps.RequiresVision = true
	}

	registry := llm.GetModelRegistry(ctx)
	if selection, err := RouteTask(ctx, step.Transcript, caps, registry); err == nil {
		if strings.TrimSpace(selection.Provider) != "" && strings.TrimSpace(selection.Model) != "" {
			return stepLLMSelection{
				Provider: selection.Provider,
				Model:    selection.Model,
				Tier:     inferSelectionTier(registry, selection),
			}
		}
	}

	assessedTier, err := AssessTaskComplexity(ctx, step.Transcript)
	if err != nil {
		return stepLLMSelection{Tier: llm.TierPremium}
	}
	switch assessedTier {
	case llm.TierPremium, llm.TierStandard, llm.TierCheapest:
		return stepLLMSelection{Tier: assessedTier}
	default:
		return stepLLMSelection{Tier: llm.TierPremium}
	}
}

func inferSelectionTier(registry []llm.ModelDefinition, selection llm.RouteSelection) llm.ModelTier {
	for _, model := range registry {
		if strings.EqualFold(model.Provider, selection.Provider) && model.Name == selection.Model {
			switch model.CostTier {
			case llm.TierCheapest:
				return llm.TierCheapest
			case llm.TierStandard:
				return llm.TierStandard
			default:
				return llm.TierPremium
			}
		}
	}
	return llm.TierPremium
}

func resolveStepModelTier(ctx context.Context, step *SequenceStep) llm.ModelTier {
	return resolveStepLLMSelection(ctx, step).Tier
}

func applyStepLLMSelection(orch *DockerOrchestrator, selection stepLLMSelection) {
	if orch == nil {
		return
	}
	if selection.Provider != "" {
		orch.Provider = selection.Provider
		orch.Model = selection.Model
		orch.BaseURL = selection.BaseURL
	} else if selection.Tier != "" {
		orch.Tier = selection.Tier
	}
}

var newRunChroniclerFn = newEpigeneticChroniclerForTier

func newEpigeneticChroniclerForTier(workspace string, tier llm.ModelTier) *EpigeneticChronicler {
	chronicler := NewEpigeneticChronicler(workspace)
	if chronicler == nil {
		return nil
	}
	chronicler.client = llm.NewClientForTier(tier)
	return chronicler
}
