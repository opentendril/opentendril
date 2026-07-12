package conductor

import (
	"context"
)

// Orchestrator defines the interface for running Sprout containers.
// This allows swapping out Docker for Kubernetes, Nomad, or other engines in the future.
type Orchestrator interface {
	// RunSprout executes a Sprout with the given task prompt and returns its output.
	RunSprout(ctx context.Context, taskPrompt string) (string, error)
}
