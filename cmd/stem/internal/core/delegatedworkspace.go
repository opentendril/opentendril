package core

import (
	"context"
	"fmt"
	"strings"
)

// DelegatedWorkspaceInput is a Botanist-local request for one exact workspace.
// It is intentionally absent from the governed capability registry.
type DelegatedWorkspaceInput struct {
	Pollen    string `json:"pollen"`
	Substrate string `json:"substrate"`
}

// DelegatedWorkspaceTargetSpec is resolved by the local adapter before the
// Stem's deterministic lifecycle port is called.
type DelegatedWorkspaceTargetSpec struct {
	Pollen           string
	Substrate        string
	Repository       string
	ConfiguredBranch string
}

// DelegatedWorkspaceReport is a safe local snapshot of workspace and Fruit
// lifecycle state.
type DelegatedWorkspaceReport struct {
	Pollen            string `json:"pollen"`
	Substrate         string `json:"substrate"`
	Repository        string `json:"repository"`
	Path              string `json:"path"`
	CurrentBranch     string `json:"currentBranch"`
	Head              string `json:"head"`
	Clean             bool   `json:"clean"`
	CleanKnown        bool   `json:"cleanKnown"`
	FruitState        string `json:"fruitState"`
	PullRequest       int    `json:"pullRequest,omitempty"`
	FruitEvidence     string `json:"fruitEvidence"`
	WorkspaceVerified bool   `json:"workspaceVerified"`
	BranchOwned       bool   `json:"branchOwned"`
	UniqueWork        bool   `json:"uniqueWork"`
	UniqueWorkKnown   bool   `json:"uniqueWorkKnown"`
	AutoReclaimable   bool   `json:"autoReclaimable"`
	Reason            string `json:"reason"`
}

// DelegatedWorkspaceAbandonInput requires the explicit local confirmation
// flag even when the workspace is clean.
type DelegatedWorkspaceAbandonInput struct {
	Pollen    string `json:"pollen"`
	Substrate string `json:"substrate"`
	Confirm   bool   `json:"confirm"`
}

// DelegatedWorkspaceAbandonResult records what was removed and whether its
// branch remains for review.
type DelegatedWorkspaceAbandonResult struct {
	Report          DelegatedWorkspaceReport `json:"report"`
	WorktreeRemoved bool                     `json:"worktreeRemoved"`
	BranchDeleted   bool                     `json:"branchDeleted"`
	BranchPreserved bool                     `json:"branchPreserved"`
	BranchReason    string                   `json:"branchReason"`
}

// DelegatedWorkspaceOperations is the local control-plane port. It is not
// projected into Capabilities and is never exposed to Pollinator transports.
type DelegatedWorkspaceOperations struct {
	Inspect func(ctx context.Context, target DelegatedWorkspaceTargetSpec) (DelegatedWorkspaceReport, error)
	Abandon func(ctx context.Context, target DelegatedWorkspaceTargetSpec, confirm bool) (DelegatedWorkspaceAbandonResult, error)
}

func (s *Service) WithDelegatedWorkspace(operations DelegatedWorkspaceOperations) *Service {
	s.delegatedWorkspace = operations
	return s
}

func (s *Service) InspectDelegatedWorkspace(ctx context.Context, in DelegatedWorkspaceInput) (DelegatedWorkspaceReport, error) {
	if PollenFromContext(ctx) != "" {
		return DelegatedWorkspaceReport{}, fmt.Errorf("delegated workspace inspection is Botanist-only")
	}
	if strings.TrimSpace(in.Pollen) == "" || strings.TrimSpace(in.Substrate) == "" || in.Pollen != strings.TrimSpace(in.Pollen) || in.Substrate != strings.TrimSpace(in.Substrate) {
		return DelegatedWorkspaceReport{}, fmt.Errorf("exact Pollen and Substrate identifiers are required")
	}
	if s.delegatedWorkspace.Inspect == nil {
		return DelegatedWorkspaceReport{}, fmt.Errorf("delegated workspace inspection is not wired")
	}
	return s.delegatedWorkspace.Inspect(ctx, DelegatedWorkspaceTargetSpec{Pollen: in.Pollen, Substrate: in.Substrate})
}

func (s *Service) AbandonDelegatedWorkspace(ctx context.Context, in DelegatedWorkspaceAbandonInput) (DelegatedWorkspaceAbandonResult, error) {
	if PollenFromContext(ctx) != "" {
		return DelegatedWorkspaceAbandonResult{}, fmt.Errorf("delegated workspace abandonment is Botanist-only")
	}
	if strings.TrimSpace(in.Pollen) == "" || strings.TrimSpace(in.Substrate) == "" || in.Pollen != strings.TrimSpace(in.Pollen) || in.Substrate != strings.TrimSpace(in.Substrate) {
		return DelegatedWorkspaceAbandonResult{}, fmt.Errorf("exact Pollen and Substrate identifiers are required")
	}
	if !in.Confirm {
		return DelegatedWorkspaceAbandonResult{}, fmt.Errorf("delegated workspace abandonment requires --confirm")
	}
	if s.delegatedWorkspace.Abandon == nil {
		return DelegatedWorkspaceAbandonResult{}, fmt.Errorf("delegated workspace abandonment is not wired")
	}
	return s.delegatedWorkspace.Abandon(ctx, DelegatedWorkspaceTargetSpec{Pollen: in.Pollen, Substrate: in.Substrate}, in.Confirm)
}
