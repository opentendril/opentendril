package main

import (
	"context"
	"fmt"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

// runWorkspaceLifecycleOperations adapts Conductor mechanics and exact
// HistoryDB evidence into Core's local lifecycle port. No operation is
// registered as a governed capability or transport route.
func runWorkspaceLifecycleOperations(history *historydb.Store) core.RunWorkspaceOperations {
	return core.RunWorkspaceOperations{
		ListAllocations: func(context.Context) ([]core.RunWorkspaceAllocation, error) {
			allocations, err := conductor.LoadRunWorkspaceAllocations()
			if err != nil {
				return nil, err
			}
			out := make([]core.RunWorkspaceAllocation, len(allocations))
			for index, allocation := range allocations {
				out[index] = toCoreRunWorkspaceAllocation(allocation)
			}
			return out, nil
		},
		Inspect: func(ctx context.Context, allocation core.RunWorkspaceAllocation) (core.RunWorkspaceEvidence, error) {
			conductorAllocation := toConductorRunWorkspaceAllocation(allocation)
			evidence := core.RunWorkspaceEvidence{Allocation: allocation}
			if allocation.Historical || allocation.AllocationRunID == "" {
				evidence.History.State = core.RunWorkspaceHistoryUnlinked
				evidence.OwnershipState = core.RunWorkspaceOwnershipAbsent
				evidence.PathState = core.RunWorkspacePathUnknown
				evidence.WorktreeState = core.RunWorkspaceWorktreeUnknown
				return evidence, nil
			}
			snapshot, err := conductor.InspectRunWorkspaceAllocation(ctx, conductorAllocation)
			if err != nil {
				return evidence, err
			}
			evidence = toCoreRunWorkspaceEvidence(snapshot)
			evidence.History = loadRunWorkspaceHistory(ctx, history, allocation)
			return evidence, nil
		},
		Reconcile: func(ctx context.Context, evidence core.RunWorkspaceEvidence) (core.RunWorkspaceMutation, error) {
			result, err := conductor.ReconcileRunWorkspace(ctx, evidence.Allocation.AllocationRunID, toConductorRunWorkspaceSnapshot(evidence))
			return core.RunWorkspaceMutation{
				WorkspaceRemoved: result.WorkspaceRemoved,
				BranchDeleted:    result.BranchDeleted,
				BranchPreserved:  result.BranchPreserved,
				BranchReason:     result.BranchReason,
			}, err
		},
		Abandon: func(ctx context.Context, evidence core.RunWorkspaceEvidence, confirm bool) (core.RunWorkspaceMutation, error) {
			result, err := conductor.AbandonRunWorkspace(ctx, evidence.Allocation.AllocationRunID, toConductorRunWorkspaceSnapshot(evidence), confirm)
			return core.RunWorkspaceMutation{
				WorkspaceRemoved: result.WorkspaceRemoved,
				BranchDeleted:    result.BranchDeleted,
				BranchPreserved:  result.BranchPreserved,
				BranchReason:     result.BranchReason,
			}, err
		},
	}
}

func loadRunWorkspaceHistory(ctx context.Context, history *historydb.Store, allocation core.RunWorkspaceAllocation) core.RunWorkspaceHistoryEvidence {
	if allocation.SproutRunID == "" {
		return core.RunWorkspaceHistoryEvidence{State: core.RunWorkspaceHistoryUnlinked}
	}
	if history == nil {
		return core.RunWorkspaceHistoryEvidence{State: core.RunWorkspaceHistoryUnavailable, Reason: "HistoryDB is unavailable"}
	}
	evidence, found, err := history.LoadSproutLifecycleByID(ctx, allocation.SproutRunID)
	if err != nil {
		return core.RunWorkspaceHistoryEvidence{State: core.RunWorkspaceHistoryUnavailable, Reason: fmt.Sprintf("HistoryDB lifecycle lookup failed: %v", err)}
	}
	if !found {
		return core.RunWorkspaceHistoryEvidence{State: core.RunWorkspaceHistoryMissing}
	}
	return core.RunWorkspaceHistoryEvidence{
		State: core.RunWorkspaceHistoryPresent,
		RunID: evidence.RunID, SessionID: evidence.SessionID, StepID: evidence.StepID,
		Pollen: evidence.Pollen, Substrate: evidence.Substrate, Status: evidence.Status,
		FruitRepository: evidence.FruitRepository, FruitBranch: evidence.FruitBranch,
		FruitCommit: evidence.FruitCommit, FruitPublicationState: evidence.FruitPublicationState,
		FruitCreatedAt: evidence.FruitCreatedAt,
	}
}

func toCoreRunWorkspaceAllocation(allocation conductor.RunWorkspaceAllocation) core.RunWorkspaceAllocation {
	return core.RunWorkspaceAllocation{
		AllocationRunID: allocation.AllocationRunID, SproutRunID: allocation.SproutRunID,
		StepID: allocation.StepID, Repository: allocation.Repository, Path: allocation.Path,
		Branch: allocation.Branch, BaseCommit: allocation.BaseCommit, Substrate: allocation.Substrate,
		PhytomerID: allocation.PhytomerID, Pollen: allocation.Pollen, CreatedAt: allocation.CreatedAt,
		State: string(allocation.State), WorkspaceRemovalPending: allocation.WorkspaceRemovalPending,
		WorkspaceRemoved: allocation.WorkspaceRemoved,
		Historical:       allocation.Historical, OwnedRefRunID: allocation.OwnedRefRunID,
	}
}

func toConductorRunWorkspaceAllocation(allocation core.RunWorkspaceAllocation) conductor.RunWorkspaceAllocation {
	return conductor.RunWorkspaceAllocation{
		AllocationRunID: allocation.AllocationRunID, SproutRunID: allocation.SproutRunID,
		StepID: allocation.StepID, Repository: allocation.Repository, Path: allocation.Path,
		Branch: allocation.Branch, BaseCommit: allocation.BaseCommit, Substrate: allocation.Substrate,
		PhytomerID: allocation.PhytomerID, Pollen: allocation.Pollen, CreatedAt: allocation.CreatedAt,
		State:                   conductor.RunWorkspaceAllocationState(allocation.State),
		WorkspaceRemovalPending: allocation.WorkspaceRemovalPending,
		WorkspaceRemoved:        allocation.WorkspaceRemoved, Historical: allocation.Historical,
		OwnedRefRunID: allocation.OwnedRefRunID,
	}
}

func toCoreRunWorkspaceEvidence(snapshot conductor.RunWorkspaceSnapshot) core.RunWorkspaceEvidence {
	return core.RunWorkspaceEvidence{
		Allocation:     toCoreRunWorkspaceAllocation(snapshot.Allocation),
		OwnershipState: string(snapshot.Ownership), OwnershipPending: snapshot.OwnershipPending,
		OwnershipError: snapshot.OwnershipError,
		PathState:      string(snapshot.PathState), PathContained: snapshot.PathContained,
		WorktreeState: string(snapshot.WorktreeState), CurrentBranch: snapshot.CurrentBranch,
		Head: snapshot.Head, HeadKnown: snapshot.HeadKnown, BranchKnown: snapshot.BranchKnown,
		BranchExists: snapshot.BranchExists, BranchHead: snapshot.BranchHead,
		UniqueCommitsKnown: snapshot.UniqueCommitsKnown, UniqueCommits: snapshot.UniqueCommits,
		BaseAncestorKnown: snapshot.BaseAncestorKnown, BaseIsAncestor: snapshot.BaseIsAncestor,
		CleanKnown: snapshot.CleanKnown, Clean: snapshot.Clean, InspectionError: snapshot.InspectionError,
	}
}

func toConductorRunWorkspaceSnapshot(evidence core.RunWorkspaceEvidence) conductor.RunWorkspaceSnapshot {
	return conductor.RunWorkspaceSnapshot{
		Allocation:       toConductorRunWorkspaceAllocation(evidence.Allocation),
		Ownership:        conductor.OwnedRefEvidenceState(evidence.OwnershipState),
		OwnershipPending: evidence.OwnershipPending,
		OwnershipError:   evidence.OwnershipError,
		PathState:        conductor.RunWorkspacePathState(evidence.PathState),
		PathContained:    evidence.PathContained,
		WorktreeState:    conductor.RunWorkspaceWorktreeState(evidence.WorktreeState),
		CurrentBranch:    evidence.CurrentBranch, Head: evidence.Head, HeadKnown: evidence.HeadKnown,
		BranchKnown: evidence.BranchKnown, BranchExists: evidence.BranchExists, BranchHead: evidence.BranchHead,
		UniqueCommitsKnown: evidence.UniqueCommitsKnown, UniqueCommits: evidence.UniqueCommits,
		BaseAncestorKnown: evidence.BaseAncestorKnown, BaseIsAncestor: evidence.BaseIsAncestor,
		CleanKnown: evidence.CleanKnown, Clean: evidence.Clean, InspectionError: evidence.InspectionError,
	}
}
