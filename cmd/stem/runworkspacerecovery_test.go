package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

func TestStartupRecoveryUsesRealCheckpointAndRunningHistory(t *testing.T) {
	t.Run("clean checkpoint is reclaimed", func(t *testing.T) {
		assertStartupRecovery(t, true, false, true)
	})
	t.Run("clean workspace without checkpoint is retained", func(t *testing.T) {
		assertStartupRecovery(t, false, false, false)
	})
	t.Run("dirty checkpoint is retained", func(t *testing.T) {
		assertStartupRecovery(t, true, true, false)
	})
}

func assertStartupRecovery(t *testing.T, checkpoint, dirty, wantRemoved bool) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "recovery@example.com")
	git("config", "user.name", "Recovery Test")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	git("add", "shared.txt")
	git("commit", "-m", "base")
	baseBytes, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	base := strings.TrimSpace(string(baseBytes))
	workspace, err := conductor.CreateRunWorkspaceWithMetadata(ctx, repo, "recovery", base, conductor.RunWorkspaceMetadata{
		SproutRunID: "sprout-recovery",
		Substrate:   "recovery-substrate",
		PhytomerID:  "phytomer-recovery",
		Pollen:      "botanist",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	store, err := historydb.Open(ctx, filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := persistDispatchSproutRun(ctx, store, historydb.SproutRun{
		RunID: workspace.SproutRunID, SessionID: "phytomer-recovery", StepID: workspace.StepID,
		Substrate: "recovery-substrate", Pollen: "botanist", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("persist running history: %v", err)
	}
	if checkpoint {
		if err := conductor.MarkRunWorkspaceExecutionComplete(workspace.RunID, workspace.SproutRunID); err != nil {
			t.Fatalf("persist checkpoint: %v", err)
		}
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(workspace.Path, "uncommitted.txt"), []byte("keep me\n"), 0o644); err != nil {
			t.Fatalf("dirty the workspace: %v", err)
		}
	}
	service := core.NewService(nil).WithRunWorkspace(runWorkspaceLifecycleOperations(store))
	if err := reconcileServeRunWorkspaces(ctx, service); err != nil {
		t.Fatalf("startup reconciliation: %v", err)
	}
	_, statErr := os.Stat(workspace.Path)
	removed := os.IsNotExist(statErr)
	if removed != wantRemoved {
		t.Fatalf("workspace removed = %v, want %v (stat %v)", removed, wantRemoved, statErr)
	}
	evidence, found, err := store.LoadSproutLifecycleByID(ctx, workspace.SproutRunID)
	if err != nil || !found || evidence.Status != "running" {
		t.Fatalf("history after startup = %#v found=%v err=%v", evidence, found, err)
	}
	allocations, err := conductor.LoadRunWorkspaceAllocations()
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if wantRemoved {
		if len(allocations) != 0 {
			t.Fatalf("reclaimed allocation remains: %#v", allocations)
		}
		cmd := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+workspace.Branch)
		if err := cmd.Run(); err == nil {
			t.Fatal("no-work branch remained after clean recovery")
		}
		return
	}
	if len(allocations) != 1 || allocations[0].AllocationRunID != workspace.RunID {
		t.Fatalf("retained allocation = %#v", allocations)
	}
	if checkpoint && (allocations[0].ExecutionCheckpoint == nil || allocations[0].ExecutionCheckpoint.SproutRunID != workspace.SproutRunID) {
		t.Fatalf("retained checkpoint = %#v", allocations[0].ExecutionCheckpoint)
	}
}
