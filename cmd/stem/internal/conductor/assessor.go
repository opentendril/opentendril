package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opentendril/opentendril/roots/llm"
)

const complexityAssessorSystemPrompt = `You are OpenTendril's automated task complexity assessor.
Classify the user's transcript into the cheapest sufficient model tier.
Use "premium" for broad architecture, ambiguous multi-file implementation, security-sensitive work, or complex debugging.
Use "standard" for routine implementation, focused debugging, or moderate code changes.
Use "cheapest" for simple edits, formatting, documentation, inspection, or mechanical tasks.
Output ONLY a JSON object in this exact shape: {"tier":"<premium|standard|cheapest>"}`

const taskRouterSystemPrompt = `You are OpenTendril's Dynamic LLM Router.
Given a task transcript and a constrained list of available provider/model pairs, choose the single best option.
Prefer vision-capable models for image or layout analysis tasks.
Prefer reasoning-capable models for complex debugging, architecture, security, or ambiguous multi-step work.
Prefer cheaper models for simple edits, formatting, documentation, or mechanical tasks.
Output ONLY a JSON object in this exact shape: {"provider":"<provider>","model":"<model>"}`

type assessorResponse struct {
	Tier llm.ModelTier `json:"tier"`
}

type routerResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// AssessTaskComplexity asks the cheapest configured model to classify the task's model tier.
func AssessTaskComplexity(ctx context.Context, transcript string) (llm.ModelTier, error) {
	client := llm.NewClientForTier(llm.TierCheapest)
	response, err := client.CallPrompt(ctx, complexityAssessorSystemPrompt, strings.TrimSpace(transcript))
	if err != nil {
		return "", fmt.Errorf("assess task complexity: %w", err)
	}

	tier, err := parseAssessorResponse(response)
	if err != nil {
		return "", fmt.Errorf("parse assessor response: %w", err)
	}
	return tier, nil
}

// RouteTask selects the best provider/model pair for a task using the dynamic router.
func RouteTask(ctx context.Context, transcript string, caps llm.Capabilities, registry []llm.ModelDefinition) (llm.RouteSelection, error) {
	if llm.ShouldBypassInternalRouter() {
		return llm.DefaultRouteSelection(), nil
	}

	filtered := filterRegistry(registry, caps)
	if !llm.ShouldUseDynamicRouter(filtered) {
		model, err := llm.SelectBestModelFromRegistry(caps, filtered)
		if err != nil {
			return llm.RouteSelection{}, err
		}
		return llm.RouteSelection{Provider: model.Provider, Model: model.Name}, nil
	}

	client := llm.NewClientForTier(llm.TierCheapest)
	prompt := buildRouterPrompt(strings.TrimSpace(transcript), filtered)
	response, err := client.CallPrompt(ctx, taskRouterSystemPrompt, prompt)
	if err != nil {
		model, fallbackErr := llm.SelectBestModelFromRegistry(caps, filtered)
		if fallbackErr != nil {
			return llm.RouteSelection{}, fmt.Errorf("route task: %w", err)
		}
		return llm.RouteSelection{Provider: model.Provider, Model: model.Name}, nil
	}

	selection, err := parseRouterResponse(response)
	if err != nil {
		model, fallbackErr := llm.SelectBestModelFromRegistry(caps, filtered)
		if fallbackErr != nil {
			return llm.RouteSelection{}, fmt.Errorf("parse router response: %w", err)
		}
		return llm.RouteSelection{Provider: model.Provider, Model: model.Name}, nil
	}

	if !registryContains(filtered, selection.Provider, selection.Model) {
		model, err := llm.SelectBestModelFromRegistry(caps, filtered)
		if err != nil {
			return llm.RouteSelection{}, fmt.Errorf("router selected unavailable model %q/%q", selection.Provider, selection.Model)
		}
		return llm.RouteSelection{Provider: model.Provider, Model: model.Name}, nil
	}

	return selection, nil
}

func filterRegistry(registry []llm.ModelDefinition, caps llm.Capabilities) []llm.ModelDefinition {
	providers := llm.AvailableProviders()
	available := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		available[strings.ToLower(strings.TrimSpace(provider))] = struct{}{}
	}

	filtered := make([]llm.ModelDefinition, 0, len(registry))
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
		if caps.MinContextSize > 0 && model.ContextSize < caps.MinContextSize {
			continue
		}
		if caps.MaxCostTier != "" && compareRegistryCostTier(model.CostTier, caps.MaxCostTier) > 0 {
			continue
		}
		if llm.IsThirdPartyRouterModel(model.Name) {
			continue
		}
		filtered = append(filtered, model)
	}
	return filtered
}

func compareRegistryCostTier(left llm.ModelTier, right llm.ModelTier) int {
	leftRank := registryCostTierRank(left)
	rightRank := registryCostTierRank(right)
	switch {
	case leftRank < rightRank:
		return -1
	case leftRank > rightRank:
		return 1
	default:
		return 0
	}
}

func registryCostTierRank(tier llm.ModelTier) int {
	switch tier {
	case llm.TierCheapest:
		return 1
	case llm.TierStandard:
		return 2
	default:
		return 3
	}
}

func buildRouterPrompt(transcript string, registry []llm.ModelDefinition) string {
	var builder strings.Builder
	builder.WriteString("Task transcript:\n")
	builder.WriteString(transcript)
	builder.WriteString("\n\nAvailable models:\n")
	for _, model := range registry {
		builder.WriteString("- provider=")
		builder.WriteString(model.Provider)
		builder.WriteString(" model=")
		builder.WriteString(model.Name)
		if model.HasVision {
			builder.WriteString(" vision=true")
		}
		if model.HasReasoning {
			builder.WriteString(" reasoning=true")
		}
		builder.WriteString(" tier=")
		builder.WriteString(string(model.CostTier))
		builder.WriteByte('\n')
	}
	return builder.String()
}

func registryContains(registry []llm.ModelDefinition, provider, model string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	for _, entry := range registry {
		if strings.EqualFold(entry.Provider, provider) && entry.Name == model {
			return true
		}
	}
	return false
}

func parseAssessorResponse(text string) (llm.ModelTier, error) {
	var response assessorResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &response); err != nil {
		return "", fmt.Errorf("decode assessor JSON: %w", err)
	}

	switch response.Tier {
	case llm.TierPremium, llm.TierStandard, llm.TierCheapest:
		return response.Tier, nil
	default:
		return "", fmt.Errorf("invalid assessor tier %q", response.Tier)
	}
}

func parseRouterResponse(text string) (llm.RouteSelection, error) {
	var response routerResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &response); err != nil {
		return llm.RouteSelection{}, fmt.Errorf("decode router JSON: %w", err)
	}

	provider := strings.ToLower(strings.TrimSpace(response.Provider))
	model := strings.TrimSpace(response.Model)
	if provider == "" || model == "" {
		return llm.RouteSelection{}, fmt.Errorf("router response missing provider or model")
	}
	return llm.RouteSelection{Provider: provider, Model: model}, nil
}
