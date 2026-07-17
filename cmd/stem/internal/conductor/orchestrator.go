package conductor

import (
	"context"
)

// Orchestrator defines the interface for running Sprout containers.
// This allows swapping out Docker for Kubernetes, Nomad, or other engines in the future.
type Orchestrator interface {
	// RunSprout executes a Sprout with the given task prompt and reports what
	// the run actually did: its output, the outcome verdict, and the files it
	// changed. The error carries the failure when the outcome is failed or
	// timed-out.
	RunSprout(ctx context.Context, taskPrompt string) (SproutRunReport, error)
}
