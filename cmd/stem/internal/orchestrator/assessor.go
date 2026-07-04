package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

const complexityAssessorSystemPrompt = `You are OpenTendril's automated task complexity assessor.
Classify the user's transcript into the cheapest sufficient model tier.
Use "premium" for broad architecture, ambiguous multi-file implementation, security-sensitive work, or complex debugging.
Use "standard" for routine implementation, focused debugging, or moderate code changes.
Use "cheapest" for simple edits, formatting, documentation, inspection, or mechanical tasks.
Output ONLY a JSON object in this exact shape: {"tier":"<premium|standard|cheapest>"}`

type assessorResponse struct {
	Tier llm.ModelTier `json:"tier"`
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
