package conductor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunWorkspaceLifecyclePersistsDistinctRunIDsAndRetiresOnCleanup(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspaceWithMetadata(ctx, repo, "durable", base, RunWorkspaceMetadata{
		SproutRunID: "sprout-history-17",
		Substrate:   "substrate-a",
		PhytomerID:  "phytomer-b",
		Pollen:      "botanist-caller",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if workspace.RunID == workspace.SproutRunID || workspace.SproutRunID != "sprout-history-17" {
		t.Fatalf("workspace RunID relation = allocation %q, Sprout %q", workspace.RunID, workspace.SproutRunID)
	}
	allocations, err := LoadRunWorkspaceAllocations()
	if err != nil {
		t.Fatalf("load allocation ledger: %v", err)
	}
	if len(allocations) != 1 {
		t.Fatalf("allocation count = %d, want 1", len(allocations))
	}
	allocation := allocations[0]
	if allocation.AllocationRunID != workspace.RunID || allocation.SproutRunID != "sprout-history-17" ||
		allocation.StepID != workspace.StepID || allocation.Repository != repo || allocation.Path != workspace.Path ||
		allocation.Branch != workspace.Branch || allocation.BaseCommit != base || allocation.State != RunWorkspaceAllocationFinalized ||
		allocation.Substrate != "substrate-a" || allocation.PhytomerID != "phytomer-b" || allocation.Pollen != "botanist-caller" {
		t.Fatalf("durable allocation does not preserve exact identity and metadata: %#v", allocation)
	}
	if err := workspace.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("immediate cleanup: %v", err)
	}
	allocations, err = LoadRunWorkspaceAllocations()
	if err != nil || len(allocations) != 0 {
		t.Fatalf("allocation ledger after successful immediate cleanup = %#v, err %v", allocations, err)
	}
}

func TestRunWorkspaceLifecycleStrictOwnershipEvidence(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "strict-evidence", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	matched := ReadOwnedRefEvidence(repo, workspace.Branch, base, workspace.RunID)
	if matched.State != OwnedRefEvidenceMatched {
		t.Fatalf("ownership evidence state = %q, want matched: %v", matched.State, matched.Err)
	}
	if err := workspace.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}

	if err := os.WriteFile(ownedRefsPath(), []byte("[]\n"), 0o600); err != nil {
		t.Fatalf("write valid empty registry: %v", err)
	}
	if evidence := ReadOwnedRefEvidence(repo, workspace.Branch, base, workspace.RunID); evidence.State != OwnedRefEvidenceAbsent {
		t.Fatalf("valid absent ownership state = %q, want absent", evidence.State)
	}
	if err := os.WriteFile(ownedRefsPath(), []byte("{bad\n"), 0o600); err != nil {
		t.Fatalf("write malformed registry: %v", err)
	}
	if evidence := ReadOwnedRefEvidence(repo, workspace.Branch, base, workspace.RunID); evidence.State != OwnedRefEvidenceMalformed || evidence.Err == nil {
		t.Fatalf("malformed ownership evidence = %#v", evidence)
	}
	contradictory, err := json.Marshal([]OwnedRef{{
		Repository: repo, Branch: workspace.Branch, Purpose: PurposeSproutIsolation,
		Base: base, RunID: "replacement-allocation",
	}})
	if err != nil {
		t.Fatalf("encode contradictory registry: %v", err)
	}
	if err := os.WriteFile(ownedRefsPath(), contradictory, 0o600); err != nil {
		t.Fatalf("write contradictory registry: %v", err)
	}
	if evidence := ReadOwnedRefEvidence(repo, workspace.Branch, base, workspace.RunID); evidence.State != OwnedRefEvidenceContradictory {
		t.Fatalf("contradictory ownership evidence = %q, want contradictory", evidence.State)
	}
	if err := os.Remove(ownedRefsPath()); err != nil {
		t.Fatalf("remove ownership registry: %v", err)
	}
	if evidence := ReadOwnedRefEvidence(repo, workspace.Branch, base, workspace.RunID); evidence.State != OwnedRefEvidenceMissing {
		t.Fatalf("missing ownership evidence = %q, want missing registry", evidence.State)
	}
	if err := os.Mkdir(ownedRefsPath(), 0o700); err != nil {
		t.Fatalf("replace ownership registry with unreadable directory: %v", err)
	}
	if evidence := ReadOwnedRefEvidence(repo, workspace.Branch, base, workspace.RunID); evidence.State != OwnedRefEvidenceUnreadable || evidence.Err == nil {
		t.Fatalf("unreadable ownership evidence = %#v", evidence)
	}
	if err := os.RemoveAll(ownedRefsPath()); err != nil {
		t.Fatalf("remove unreadable ownership registry: %v", err)
	}
}

func TestRunWorkspaceLifecycleContradictoryOwnershipCannotMutate(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "contradictory-owner", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	contradictory, err := json.Marshal([]OwnedRef{{
		Repository: repo, Branch: workspace.Branch, Purpose: PurposeSproutIsolation,
		Base: base, RunID: "different-allocation",
	}})
	if err != nil {
		t.Fatalf("encode contradictory registry: %v", err)
	}
	if err := os.WriteFile(ownedRefsPath(), contradictory, 0o600); err != nil {
		t.Fatalf("write contradictory registry: %v", err)
	}
	snapshot, err := InspectRunWorkspace(ctx, workspace.RunID)
	if err != nil {
		t.Fatalf("inspect workspace: %v", err)
	}
	if snapshot.Ownership != OwnedRefEvidenceContradictory {
		t.Fatalf("ownership evidence = %q, want contradictory", snapshot.Ownership)
	}
	if _, err := ReconcileRunWorkspace(ctx, workspace.RunID, snapshot); err == nil {
		t.Fatal("reconciliation mutated a workspace with contradictory ownership")
	}
	if _, err := os.Stat(workspace.Path); err != nil {
		t.Fatalf("contradictory ownership removed workspace: %v", err)
	}
	if !branchExists(t, repo, workspace.Branch) {
		t.Fatal("contradictory ownership removed branch")
	}
	restored, err := json.Marshal([]OwnedRef{{
		Repository: repo, Branch: workspace.Branch, Purpose: PurposeSproutIsolation,
		Base: base, RunID: workspace.RunID,
	}})
	if err != nil {
		t.Fatalf("encode exact ownership registry: %v", err)
	}
	if err := os.WriteFile(ownedRefsPath(), restored, 0o600); err != nil {
		t.Fatalf("restore exact ownership registry: %v", err)
	}
	if err := workspace.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup exact workspace after restoring fixture ownership: %v", err)
	}
}

func TestRunWorkspaceAllocationReservationRemainsInspectableWhilePending(t *testing.T) {
	repo, base := prepareRunWorkspaceTest(t)
	allocation := RunWorkspaceAllocation{
		AllocationRunID: "pending-allocation", SproutRunID: "history-run",
		StepID: "pending-step", Repository: repo,
		Path:   filepath.Join(runWorkspaceRoot(), "pending-allocation"),
		Branch: "sprout/task-pending-step", BaseCommit: base,
		CreatedAt: time.Now().UTC(), State: RunWorkspaceAllocationPending,
	}
	if err := ReserveRunWorkspaceAllocation(allocation); err != nil {
		t.Fatalf("reserve allocation: %v", err)
	}
	allocations, err := LoadRunWorkspaceAllocations()
	if err != nil {
		t.Fatalf("load pending allocation: %v", err)
	}
	if len(allocations) != 1 || allocations[0].AllocationRunID != allocation.AllocationRunID || allocations[0].State != RunWorkspaceAllocationPending {
		t.Fatalf("pending allocation was not retained visibly: %#v", allocations)
	}
	if err := RetireRunWorkspaceAllocation(allocation); err != nil {
		t.Fatalf("retire test reservation: %v", err)
	}
}

func TestRunWorkspaceLifecycleReconcileRemovesOnlyEmptyWorkspace(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "empty-lifecycle", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	snapshot, err := InspectRunWorkspace(ctx, workspace.RunID)
	if err != nil {
		t.Fatalf("inspect workspace: %v", err)
	}
	if snapshot.Ownership != OwnedRefEvidenceMatched || snapshot.PathState != RunWorkspacePathPresent ||
		snapshot.WorktreeState != RunWorkspaceWorktreeMatched || !snapshot.CleanKnown || !snapshot.Clean ||
		!snapshot.UniqueCommitsKnown || snapshot.UniqueCommits != 0 {
		t.Fatalf("unexpected clean workspace snapshot: %#v", snapshot)
	}
	result, err := ReconcileRunWorkspace(ctx, workspace.RunID, snapshot)
	if err != nil {
		t.Fatalf("reconcile clean workspace: %v", err)
	}
	if !result.WorkspaceRemoved || !result.BranchDeleted || result.BranchPreserved {
		t.Fatalf("reconcile result = %#v, want workspace and empty branch removed", result)
	}
	if _, err := os.Stat(workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("workspace path remains after reconciliation, stat error %v", err)
	}
	if branchExists(t, repo, workspace.Branch) {
		t.Fatal("empty branch remains after reconciliation")
	}
	if _, err := runWorkspaceAllocationByID(workspace.RunID); err == nil {
		t.Fatal("reconciled allocation remains in the ledger")
	}
}

func TestRunWorkspaceLifecycleRetriesAfterDurableWorkspaceRemoval(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "retry-teardown", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := markRunWorkspaceRemovalPendingForHandle(workspace); err != nil {
		t.Fatalf("persist teardown intent: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "worktree", "remove", workspace.Path); err != nil {
		t.Fatalf("simulate completed worktree removal: %v", err)
	}
	snapshot, err := InspectRunWorkspace(ctx, workspace.RunID)
	if err != nil {
		t.Fatalf("inspect partially completed teardown: %v", err)
	}
	if snapshot.PathState != RunWorkspacePathMissing || snapshot.WorktreeState != RunWorkspaceWorktreeAbsent ||
		!snapshot.Allocation.WorkspaceRemovalPending || snapshot.Allocation.WorkspaceRemoved {
		t.Fatalf("durable interrupted cleanup evidence = %#v", snapshot)
	}
	result, err := ReconcileRunWorkspace(ctx, workspace.RunID, snapshot)
	if err != nil {
		t.Fatalf("retry interrupted teardown: %v", err)
	}
	if !result.WorkspaceRemoved || !result.BranchDeleted {
		t.Fatalf("retry result = %#v, want empty branch reclaimed", result)
	}
}

func TestRunWorkspaceLifecycleAbandonPreservesCommittedFruit(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "fruit-lifecycle", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "fruit.txt"), []byte("review me\n"), 0o644); err != nil {
		t.Fatalf("write Fruit: %v", err)
	}
	for _, args := range [][]string{{"add", "fruit.txt"}, {"commit", "-m", "Fruit"}} {
		if _, err := runGitCommand(ctx, workspace.Path, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "uncommitted.txt"), []byte("discard only after confirmation\n"), 0o644); err != nil {
		t.Fatalf("write dirty workspace state: %v", err)
	}
	snapshot, err := InspectRunWorkspace(ctx, workspace.RunID)
	if err != nil {
		t.Fatalf("inspect committed workspace: %v", err)
	}
	if !snapshot.CleanKnown || snapshot.Clean {
		t.Fatalf("workspace should be positively classified dirty before confirmed abandonment: %#v", snapshot)
	}
	result, err := AbandonRunWorkspace(ctx, workspace.RunID, snapshot, true)
	if err != nil {
		t.Fatalf("abandon workspace: %v", err)
	}
	if !result.WorkspaceRemoved || result.BranchDeleted || !result.BranchPreserved || !strings.Contains(result.BranchReason, "Fruit") {
		t.Fatalf("abandon result = %#v, want workspace removed and Fruit branch retained", result)
	}
	if !branchExists(t, repo, workspace.Branch) {
		t.Fatal("abandon deleted branch carrying committed Fruit")
	}
	fruit, err := runGitCommand(ctx, repo, "show", "refs/heads/"+workspace.Branch+":fruit.txt")
	if err != nil || strings.TrimSpace(fruit) != "review me" {
		t.Fatalf("preserved Fruit = %q, err %v", fruit, err)
	}
}

func TestRunWorkspaceLifecycleRejectsStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "stale-snapshot", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	snapshot, err := InspectRunWorkspace(ctx, workspace.RunID)
	if err != nil {
		t.Fatalf("inspect workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "late.txt"), []byte("changed after inspection\n"), 0o644); err != nil {
		t.Fatalf("make stale snapshot: %v", err)
	}
	if _, err := ReconcileRunWorkspace(ctx, workspace.RunID, snapshot); err == nil {
		t.Fatal("reconcile accepted a physical snapshot that changed after inspection")
	}
	assertFileContents(t, filepath.Join(workspace.Path, "late.txt"), "changed after inspection\n")
	if err := os.Remove(filepath.Join(workspace.Path, "late.txt")); err != nil {
		t.Fatalf("restore clean workspace: %v", err)
	}
	if err := workspace.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
}

func TestRunWorkspaceLifecycleMissingPathWithoutRemovalEvidenceIsRetained(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "missing-path", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.RemoveAll(workspace.Path); err != nil {
		t.Fatalf("remove fixture path: %v", err)
	}
	snapshot, err := InspectRunWorkspace(ctx, workspace.RunID)
	if err != nil {
		t.Fatalf("inspect missing workspace: %v", err)
	}
	if snapshot.PathState != RunWorkspacePathMissing || snapshot.Allocation.WorkspaceRemoved {
		t.Fatalf("missing path evidence = %#v", snapshot)
	}
	if _, err := ReconcileRunWorkspace(ctx, workspace.RunID, snapshot); err == nil {
		t.Fatal("automatic reconciliation accepted a missing path without durable teardown evidence")
	}
	if err := workspace.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("immediate cleanup should recover its exact stale registration: %v", err)
	}
}

func TestRunWorkspaceLifecycleStaleAllocationCannotTargetReplacement(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	first, err := CreateRunWorkspace(ctx, repo, "replacement-lifecycle", base)
	if err != nil {
		t.Fatalf("create first workspace: %v", err)
	}
	snapshot, err := InspectRunWorkspace(ctx, first.RunID)
	if err != nil {
		t.Fatalf("inspect first workspace: %v", err)
	}
	if err := first.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup first workspace: %v", err)
	}
	second, err := CreateRunWorkspace(ctx, repo, "replacement-lifecycle", base)
	if err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	if _, err := ReconcileRunWorkspace(ctx, first.RunID, snapshot); err == nil {
		t.Fatal("stale allocation identity reconciled a replacement workspace")
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("stale reconciliation removed replacement path: %v", err)
	}
	if !branchExists(t, repo, second.Branch) {
		t.Fatal("stale reconciliation removed replacement branch")
	}
	if err := second.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup replacement workspace: %v", err)
	}
}
