package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func currentBranch(t *testing.T, repo string) string {
	t.Helper()
	out, err := runGitCommand(context.Background(), repo, "branch", "--show-current")
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	return strings.TrimSpace(out)
}

func TestRunGitBranchValidatesExecution(t *testing.T) {
	ctx := context.Background()
	if _, err := RunGitBranch(ctx, GitBranchExecution{Branch: "feat/x"}); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if _, err := RunGitBranch(ctx, GitBranchExecution{Workspace: t.TempDir()}); err == nil {
		t.Fatal("missing branch accepted")
	}
}

// TestRunGitBranchRejectsUnsafeNames: a delegated caller supplies this name,
// so anything that could read as a flag or a malformed reference is refused.
func TestRunGitBranchRejectsUnsafeNames(t *testing.T) {
	repo := newBranchRepo(t, "feat/base", "trunk")
	for _, name := range []string{
		"--force", "-x", "/leading", "trailing/", "has..dots", "spaced name",
		"tilde~1", "caret^", "colon:ref", "star*", "quote\"", "semi;colon",
		"pipe|", "amp&", "sub$(x)", "back`tick`", "ends.lock",
	} {
		if _, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: name}); err == nil {
			t.Errorf("unsafe branch name %q was accepted", name)
		}
	}
	if got := currentBranch(t, repo); got != "feat/base" {
		t.Fatalf("workspace moved to %q during refused operations, want feat/base", got)
	}
}

// TestRunGitBranchCreatesAndSwitches covers the normal path and the
// idempotent repeat: creating, then asking again, which switches rather than
// failing or resetting.
func TestRunGitBranchCreatesAndSwitches(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/base", "trunk")

	result, err := RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "refs/heads/feat/new-leaf"})
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if result.Status != "created" || result.Branch != "feat/new-leaf" || result.PreviousBranch != "feat/base" {
		t.Fatalf("result = %+v, want feat/new-leaf created from feat/base", result)
	}
	if got := currentBranch(t, repo); got != "feat/new-leaf" {
		t.Fatalf("workspace on %q, want feat/new-leaf", got)
	}

	// Asking for the branch the workspace is already on is a no-op success.
	result, err = RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/new-leaf"})
	if err != nil {
		t.Fatalf("re-request current branch: %v", err)
	}
	if result.Status != "switched" {
		t.Fatalf("result = %+v, want switched for the branch already checked out", result)
	}

	// Go back, then ask for the existing branch again: it switches, and the
	// branch is NOT reset — its commit must survive.
	if _, err := runGitCommand(ctx, repo, "checkout", "feat/base"); err != nil {
		t.Fatalf("checkout base: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "feat/new-leaf"); err != nil {
		t.Fatalf("checkout leaf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "leaf.txt"), []byte("grown\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "work on the leaf"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "feat/base"); err != nil {
		t.Fatalf("checkout base: %v", err)
	}

	result, err = RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/new-leaf"})
	if err != nil {
		t.Fatalf("switch to existing branch: %v", err)
	}
	if result.Status != "switched" {
		t.Fatalf("result = %+v, want switched", result)
	}
	after, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if strings.TrimSpace(after) != strings.TrimSpace(head) {
		t.Fatal("switching to an existing branch moved it — an existing branch must never be reset, that discards commits")
	}
}

// TestRunGitBranchRefusesDefaultBranch: creating a branch named as the
// repository's default branch is refused, same reasoning as the pull-request
// guard.
func TestRunGitBranchRefusesDefaultBranch(t *testing.T) {
	repo := newBranchRepo(t, "feat/base", "trunk")
	_, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: "trunk"})
	if err == nil {
		t.Fatal("a branch named as the default branch was created")
	}
	if !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("error = %v, want a refusal naming the default branch", err)
	}

	// And under the floor, when the default branch cannot be determined.
	bare := newBranchRepo(t, "feat/base", "")
	if _, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: bare, Branch: "main"}); err == nil {
		t.Fatal("the protection floor did not refuse a branch named main when the default branch was undetermined")
	}
}

// TestRunGitBranchCarriesChangesToNewBranchButRefusesDirtySwitch pins the
// asymmetry: uncommitted work follows you onto a NEW branch (the normal
// started-editing-first recovery), but is never carried onto an EXISTING one.
func TestRunGitBranchCarriesChangesToNewBranchButRefusesDirtySwitch(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/base", "trunk")
	if _, err := runGitCommand(ctx, repo, "branch", "feat/already-there"); err != nil {
		t.Fatalf("pre-create branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Existing branch + dirty workspace: refused.
	_, err := RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/already-there"})
	if err == nil {
		t.Fatal("switching to an existing branch with uncommitted changes was allowed")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want an uncommitted-changes refusal", err)
	}
	if got := currentBranch(t, repo); got != "feat/base" {
		t.Fatalf("workspace moved to %q despite the refusal", got)
	}

	// New branch + dirty workspace: allowed, and the work comes along.
	result, err := RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/brand-new"})
	if err != nil {
		t.Fatalf("create branch with uncommitted changes: %v", err)
	}
	if result.Status != "created" {
		t.Fatalf("result = %+v, want created", result)
	}
	if _, err := os.Stat(filepath.Join(repo, "wip.txt")); err != nil {
		t.Fatalf("uncommitted work did not follow onto the new branch: %v", err)
	}
}
