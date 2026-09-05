package core

import (
	"context"
	"fmt"
	"strings"
)

// ResolveDelegationRequest composes the DelegationRequest for one governed
// delegated invocation. Pollen is taken only from trusted Core context.
// Impact is the Stem-owned CapabilityImpact for the operation class.
//
// Explicit-Substrate capabilities authorize against the decoded substrate
// field. phytomer.continue authorizes against the Substrate bound on the
// Stem-owned continuation target; a caller-supplied substrate is ignored.
func (s *Service) ResolveDelegationRequest(ctx context.Context, operationClass string, input map[string]any) (DelegationRequest, error) {
	operationClass = strings.TrimSpace(operationClass)
	if operationClass == "" {
		return DelegationRequest{}, fmt.Errorf("operation class is required")
	}
	pollen := PollenFromContext(ctx)
	impact := CapabilityImpact(operationClass)
	if operationClass == CapContinuePhytomer {
		return s.resolveContinuationDelegationRequest(ctx, input, impact)
	}
	return DelegationRequest{
		Pollen:         pollen,
		OperationClass: operationClass,
		Substrate:      substrateFromCapabilityInput(input),
		Impact:         impact,
	}, nil
}

func (s *Service) resolveContinuationDelegationRequest(ctx context.Context, input map[string]any, impact string) (DelegationRequest, error) {
	phytomerID := sessionIDFromCapabilityInput(input)
	target, err := s.ResolveContinuationTarget(ctx, phytomerID)
	if err != nil {
		return DelegationRequest{}, err
	}
	request := target.ToDelegationRequest(CapContinuePhytomer)
	request.Impact = impact
	return request, nil
}

func substrateFromCapabilityInput(input map[string]any) string {
	if input == nil {
		return ""
	}
	substrate, _ := input["substrate"].(string)
	return strings.TrimSpace(substrate)
}

func sessionIDFromCapabilityInput(input map[string]any) string {
	if input == nil {
		return ""
	}
	sessionID, _ := input["sessionId"].(string)
	return strings.TrimSpace(sessionID)
}
