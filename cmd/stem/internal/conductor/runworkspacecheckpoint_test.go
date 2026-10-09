package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionCheckpointPersistsExactIdentityAndRejectsMismatch(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspaceWithMetadata(ctx, repo, "checkpointed", base, RunWorkspaceMetadata{
		SproutRunID: "sprout-history-checkpoint",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := MarkRunWorkspaceExecutionComplete(workspace.RunID, "other-sprout"); err == nil {
		t.Fatal("mismatched Sprout history RunID was accepted")
	}
	allocations, err := LoadRunWorkspaceAllocations()
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if len(allocations) != 1 || allocations[0].ExecutionCheckpoint != nil {
		t.Fatalf("mismatched checkpoint mutated the ledger: %#v", allocations)
	}
	if err := MarkRunWorkspaceExecutionComplete(workspace.RunID, workspace.SproutRunID); err != nil {
		t.Fatalf("persist checkpoint: %v", err)
	}
	if err := MarkRunWorkspaceExecutionComplete(workspace.RunID, workspace.SproutRunID); err != nil {
		t.Fatalf("repeat checkpoint: %v", err)
	}
	allocations, err = LoadRunWorkspaceAllocations()
	if err != nil || len(allocations) != 1 || allocations[0].ExecutionCheckpoint == nil {
		t.Fatalf("checkpoint ledger = %#v, err %v", allocations, err)
	}
	got := allocations[0].ExecutionCheckpoint
	if got.State != RunWorkspaceExecutionComplete || got.AllocationRunID != workspace.RunID || got.SproutRunID != workspace.SproutRunID {
		t.Fatalf("checkpoint = %#v", got)
	}
}

func TestExecutionCheckpointLedgerRejectsIncompleteRecord(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspaceWithMetadata(ctx, repo, "incomplete-checkpoint", base, RunWorkspaceMetadata{
		SproutRunID: "sprout-history-incomplete",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := MarkRunWorkspaceExecutionComplete(workspace.RunID, workspace.SproutRunID); err != nil {
		t.Fatalf("persist checkpoint: %v", err)
	}
	path := runWorkspaceAllocationsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	broken := strings.Replace(string(data), workspace.SproutRunID, "other-sprout", 1)
	if broken == string(data) {
		t.Fatal("ledger fixture did not contain the Sprout history RunID")
	}
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatalf("write broken ledger: %v", err)
	}
	if _, err := LoadRunWorkspaceAllocations(); err == nil {
		t.Fatal("mismatched checkpoint ledger was accepted")
	}
}

func TestCheckpointPersistenceFailureSkipsCleanup(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspaceWithMetadata(ctx, repo, "checkpoint-failure", base, RunWorkspaceMetadata{
		SproutRunID: "sprout-history-failure",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	ledgerPath := runWorkspaceAllocationsPath()
	original, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove ledger file: %v", err)
	}
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatalf("block ledger path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ledgerPath, "blocker"), []byte("x"), 0o600); err != nil {
		t.Fatalf("occupy ledger path: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(ledgerPath)
	})
	err = cleanupManagedWorkspaceAfterCheckpoint(ctx, workspace, ResolvedCredential{})
	if err == nil {
		t.Fatal("cleanup proceeded after a failed checkpoint save")
	}
	info, statErr := os.Stat(workspace.Path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("workspace path after refused cleanup: %v", statErr)
	}
	if err := os.RemoveAll(ledgerPath); err != nil {
		t.Fatalf("remove ledger blocker: %v", err)
	}
	if err := os.WriteFile(ledgerPath, original, 0o600); err != nil {
		t.Fatalf("restore ledger: %v", err)
	}
	allocations, loadErr := LoadRunWorkspaceAllocations()
	if loadErr != nil || len(allocations) != 1 || allocations[0].ExecutionCheckpoint != nil {
		t.Fatalf("allocation after failed checkpoint = %#v, err %v", allocations, loadErr)
	}
}
