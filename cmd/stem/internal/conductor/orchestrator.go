package conductor

import (
	"context"
)

// Orchestrator defines the interface for running Tendril containers.
// This allows swapping out Docker for Kubernetes, Nomad, or other engines in the future.
type Orchestrator interface {
	// RunTendril executes a Tendril with the given task prompt and returns its output.
	RunTendril(ctx context.Context, taskPrompt string) (string, error)
}
