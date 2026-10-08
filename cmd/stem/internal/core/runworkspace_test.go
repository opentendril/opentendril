package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func safeRunWorkspaceEvidence() RunWorkspaceEvidence {
	created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	return RunWorkspaceEvidence{
		Allocation: RunWorkspaceAllocation{
			AllocationRunID: "allocation-9", SproutRunID: "history-4", StepID: "step-4",
			Repository: "/repo/sample", Path: "/home/botanist/.tendril/run-workspaces/9",
			Branch: "sprout/task-step-4", BaseCommit: "base-oid", Substrate: "sample",
			PhytomerID: "phytomer-2", Pollen: "codex", CreatedAt: created, State: "finalized",
		},
		History: RunWorkspaceHistoryEvidence{
			State: RunWorkspaceHistoryPresent, RunID: "history-4", StepID: "step-4",
			SessionID: "phytomer-2", Pollen: "codex", Substrate: "sample", Status: "matured",
		},
		OwnershipState: RunWorkspaceOwnershipMatched,
		PathState:      RunWorkspacePathPresent, PathContained: true,
		WorktreeState: RunWorkspaceWorktreeMatched, CurrentBranch: "sprout/task-step-4",
		Head: "base-oid", HeadKnown: true, BranchKnown: true, BranchExists: true,
		BranchHead: "base-oid", UniqueCommitsKnown: true, UniqueCommits: 0,
		BaseAncestorKnown: true, BaseIsAncestor: true, CleanKnown: true, Clean: true,
	}
}

func TestClassifyRunWorkspaceRequiresPositiveTerminalEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunWorkspaceEvidence)
		want   string
	}{
		{name: "history missing", mutate: func(e *RunWorkspaceEvidence) { e.History.State = RunWorkspaceHistoryMissing }, want: "history row is missing"},
		{name: "history unavailable", mutate: func(e *RunWorkspaceEvidence) { e.History.State = RunWorkspaceHistoryUnavailable }, want: "HistoryDB lifecycle evidence is unavailable"},
		{name: "history nonterminal", mutate: func(e *RunWorkspaceEvidence) { e.History.Status = "running" }, want: "non-terminal"},
		{name: "history relation mismatch", mutate: func(e *RunWorkspaceEvidence) { e.History.RunID = "step-4" }, want: "contradicts"},
		{name: "history session mismatch", mutate: func(e *RunWorkspaceEvidence) { e.History.SessionID = "other-session" }, want: "contradicts"},
		{name: "missing ownership", mutate: func(e *RunWorkspaceEvidence) { e.OwnershipState = RunWorkspaceOwnershipMissing }, want: "registry is missing"},
		{name: "malformed ownership", mutate: func(e *RunWorkspaceEvidence) { e.OwnershipState = RunWorkspaceOwnershipMalformed }, want: "registry is malformed"},
		{name: "dirty", mutate: func(e *RunWorkspaceEvidence) { e.Clean = false }, want: "contains uncommitted"},
		{name: "missing path without teardown record", mutate: func(e *RunWorkspaceEvidence) { e.PathState = RunWorkspacePathMissing }, want: "without a durable teardown record"},
		{name: "inconsistent worktree", mutate: func(e *RunWorkspaceEvidence) { e.WorktreeState = RunWorkspaceWorktreeInconsistent }, want: "identity is absent or inconsistent"},
		{name: "unknown commits", mutate: func(e *RunWorkspaceEvidence) { e.UniqueCommitsKnown = false }, want: "unique-commit state cannot be proven"},
		{name: "missing relation", mutate: func(e *RunWorkspaceEvidence) { e.Allocation.SproutRunID = "" }, want: "no explicit Sprout history RunID"},
		{name: "historical", mutate: func(e *RunWorkspaceEvidence) { e.Allocation.Historical = true; e.Allocation.AllocationRunID = "" }, want: "no durable allocation relation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := safeRunWorkspaceEvidence()
			test.mutate(&evidence)
			report := ClassifyRunWorkspace(evidence)
			if report.AutoReconcileable {
				t.Fatal("unsafe or incomplete RunWorkspace was marked auto-reconcileable")
			}
			if !strings.Contains(report.Reason, test.want) {
				t.Fatalf("reason = %q, want it to contain %q", report.Reason, test.want)
			}
		})
	}
}

func TestRunWorkspacePendingAllocationIsVisibleButNeedsConfirmedAbandonment(t *testing.T) {
	evidence := safeRunWorkspaceEvidence()
	evidence.Allocation.State = "pending"
	evidence.OwnershipPending = true
	report := ClassifyRunWorkspace(evidence)
	if report.AutoReconcileable || !strings.Contains(report.Reason, "pending") {
		t.Fatalf("pending allocation classification = %+v", report)
	}
	var abandoned bool
	service := NewService(nil).WithRunWorkspace(RunWorkspaceOperations{
		ListAllocations: func(context.Context) ([]RunWorkspaceAllocation, error) {
			return []RunWorkspaceAllocation{evidence.Allocation}, nil
		},
		Inspect: func(context.Context, RunWorkspaceAllocation) (RunWorkspaceEvidence, error) {
			return evidence, nil
		},
		Abandon: func(_ context.Context, got RunWorkspaceEvidence, confirm bool) (RunWorkspaceMutation, error) {
			abandoned = confirm && got.Allocation.State == "pending" && got.OwnershipPending
			return RunWorkspaceMutation{WorkspaceRemoved: true}, nil
		},
	})
	if _, err := service.AbandonRunWorkspace(context.Background(), RunWorkspaceAbandonInput{AllocationRunID: evidence.Allocation.AllocationRunID}); err == nil {
		t.Fatal("pending allocation was abandoned without explicit confirmation")
	}
	if _, err := service.AbandonRunWorkspace(context.Background(), RunWorkspaceAbandonInput{
		AllocationRunID: evidence.Allocation.AllocationRunID, Confirm: true,
	}); err != nil {
		t.Fatalf("confirmed exact pending allocation could not be recovered: %v", err)
	}
	if !abandoned {
		t.Fatal("Core did not pass the exact pending allocation to confirmed abandonment")
	}
}

func TestClassifyRunWorkspaceAllowsFruitPreservingWorktreeRemoval(t *testing.T) {
	evidence := safeRunWorkspaceEvidence()
	evidence.UniqueCommits = 2
	evidence.History.FruitBranch = evidence.Allocation.Branch
	evidence.History.FruitCommit = "fruit-oid"
	report := ClassifyRunWorkspace(evidence)
	if !report.AutoReconcileable || !strings.Contains(report.Reason, "branch is preserved") {
		t.Fatalf("Fruit-bearing workspace classification = %+v", report)
	}
}

func TestRunWorkspaceCoreReconcileAndAbandonAreBotanistOnly(t *testing.T) {
	evidence := safeRunWorkspaceEvidence()
	mutations := 0
	service := NewService(nil).WithRunWorkspace(RunWorkspaceOperations{
		ListAllocations: func(context.Context) ([]RunWorkspaceAllocation, error) {
			return []RunWorkspaceAllocation{evidence.Allocation}, nil
		},
		Inspect: func(_ context.Context, allocation RunWorkspaceAllocation) (RunWorkspaceEvidence, error) {
			if allocation.AllocationRunID != "allocation-9" {
				t.Fatalf("inspected allocation = %q", allocation.AllocationRunID)
			}
			return evidence, nil
		},
		Reconcile: func(_ context.Context, got RunWorkspaceEvidence) (RunWorkspaceMutation, error) {
			mutations++
			if got.Allocation.AllocationRunID != "allocation-9" || got.Allocation.SproutRunID != "history-4" {
				t.Fatalf("reconcile identity = %+v", got.Allocation)
			}
			return RunWorkspaceMutation{WorkspaceRemoved: true, BranchDeleted: true}, nil
		},
		Abandon: func(_ context.Context, _ RunWorkspaceEvidence, confirm bool) (RunWorkspaceMutation, error) {
			mutations++
			if !confirm {
				t.Fatal("Core called abandon without confirmation")
			}
			return RunWorkspaceMutation{WorkspaceRemoved: true, BranchPreserved: true, BranchReason: "committed Fruit"}, nil
		},
	})

	result, err := service.ReconcileRunWorkspace(context.Background(), RunWorkspaceInput{AllocationRunID: "allocation-9"})
	if err != nil || !result.WorkspaceRemoved || !result.BranchDeleted || mutations != 1 {
		t.Fatalf("ReconcileRunWorkspace = %+v, err=%v, mutations=%d", result, err, mutations)
	}
	if _, err := service.AbandonRunWorkspace(context.Background(), RunWorkspaceAbandonInput{AllocationRunID: "allocation-9"}); err == nil {
		t.Fatal("abandonment without --confirm succeeded")
	}
	if _, err := service.ReconcileRunWorkspace(WithPollen(context.Background(), "codex"), RunWorkspaceInput{AllocationRunID: "allocation-9"}); err == nil {
		t.Fatal("Pollinator reconciled a RunWorkspace")
	}
	result, err = service.AbandonRunWorkspace(context.Background(), RunWorkspaceAbandonInput{AllocationRunID: "allocation-9", Confirm: true})
	if err != nil || !result.WorkspaceRemoved || !result.BranchPreserved || mutations != 2 {
		t.Fatalf("AbandonRunWorkspace = %+v, err=%v, mutations=%d", result, err, mutations)
	}
}

func TestRunWorkspaceCoreDoesNotExposeGovernedCapability(t *testing.T) {
	for _, name := range CapabilityNames() {
		if strings.Contains(name, "workspace") {
			t.Fatalf("RunWorkspace lifecycle unexpectedly entered governed capabilities: %s", name)
		}
	}
}
