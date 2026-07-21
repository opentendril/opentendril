package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// Concurrency safety for the delegated git ladder.
//
// Before isolation, two Pollinators granted the same substrate silently corrupted
// each other: the delegated commit stages the whole tree, the tree was shared,
// so one subject's uncommitted files were committed by the other, onto the
// other's branch, under the other's identity — destroying the attribution the
// delegated commit exists to provide. These tests reproduce that interleaving
// and assert it no longer happens.

// gitRun shells out directly rather than reaching for a conductor helper: the
// test asserts on real repository state, and adding production API surface for
// a test's benefit would be the wrong trade.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newIsolationSubstrate builds a substrate repository and points the delegated
// workspace root at a temporary directory, so the test never touches the
// operator's real ~/.tendril.
func newIsolationSubstrate(t *testing.T) (name, path string) {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"checkout", "-b", "trunk"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		gitRun(t, repo, args...)
	}
	t.Setenv("HOME", t.TempDir())
	return "shared", repo
}

func pollenContext(pollen string) context.Context {
	return core.WithPollen(context.Background(), pollen)
}

// TestDelegatedPollensGetSeparateWorkspaces is the core property: two
// pollen on one substrate never share a working tree.
func TestDelegatedPollensGetSeparateWorkspaces(t *testing.T) {
	name, path := newIsolationSubstrate(t)

	first, err := conductor.ResolveDelegatedWorkspace(pollenContext("claude"), name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("resolve for claude: %v", err)
	}
	second, err := conductor.ResolveDelegatedWorkspace(pollenContext("codex"), name, path, "codex", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("resolve for codex: %v", err)
	}

	if first.Path == second.Path {
		t.Fatal("two Pollinators resolved to the same workspace — this is exactly the shared tree that let one Pollinator commit another's work")
	}
	if first.Path == path || second.Path == path {
		t.Fatal("a delegated workspace resolved to the substrate's own checkout")
	}
	if !first.Isolated || !second.Isolated {
		t.Fatalf("workspaces not reported as isolated: %+v %+v", first, second)
	}
	// Reuse is stable: the same pollen always returns to its own tree, which
	// is what lets a Pollinator's sequence of calls stay consistent without the
	// Pollinator tracking anything.
	again, err := conductor.ResolveDelegatedWorkspace(pollenContext("claude"), name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("re-resolve for claude: %v", err)
	}
	if again.Path != first.Path {
		t.Fatalf("claude got %s then %s — a subject's workspace must be stable", first.Path, again.Path)
	}
}

// TestNonDelegatedCallUsesOperatorCheckout: a human at a terminal carries no
// pollen and must keep seeing their own working copy.
func TestNonDelegatedCallUsesOperatorCheckout(t *testing.T) {
	name, path := newIsolationSubstrate(t)

	workspace, err := conductor.ResolveDelegatedWorkspace(context.Background(), name, path, "", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if workspace.Path != path {
		t.Fatalf("workspace = %s, want the substrate's own checkout %s", workspace.Path, path)
	}
	if workspace.Isolated {
		t.Fatal("a non-delegated call was given an isolated workspace")
	}
}

// TestConcurrentPollenDoNotCorruptEachOther replays the exact interleaving
// that was reproduced before this work: A branches and starts editing, B
// branches and commits. Before isolation, B's commit contained A's file, on
// B's branch, under B's identity, and A's branch was left empty.
func TestConcurrentPollenDoNotCorruptEachOther(t *testing.T) {
	name, path := newIsolationSubstrate(t)
	credential := conductor.ResolvedCredential{
		Identity: conductor.ResolvedIdentity{Name: "Bot", Email: "bot@example.com"},
	}

	PollinatorA, err := conductor.ResolveDelegatedWorkspace(pollenContext("claude"), name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("workspace for claude: %v", err)
	}
	PollinatorB, err := conductor.ResolveDelegatedWorkspace(pollenContext("codex"), name, path, "codex", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("workspace for codex: %v", err)
	}

	// A creates its branch and starts work (uncommitted).
	if _, err := conductor.RunGitBranch(context.Background(), conductor.GitBranchExecution{
		Workspace: PollinatorA.Path, Branch: "feat/claude", ConfiguredBranch: "trunk",
	}); err != nil {
		t.Fatalf("claude branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(PollinatorA.Path, "claude-work.txt"), []byte("SECRET-A\n"), 0o644); err != nil {
		t.Fatalf("claude write: %v", err)
	}

	// B creates its branch and commits its own work, concurrently.
	if _, err := conductor.RunGitBranch(context.Background(), conductor.GitBranchExecution{
		Workspace: PollinatorB.Path, Branch: "feat/codex", ConfiguredBranch: "trunk",
	}); err != nil {
		t.Fatalf("codex branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(PollinatorB.Path, "codex-work.txt"), []byte("work-b\n"), 0o644); err != nil {
		t.Fatalf("codex write: %v", err)
	}
	result, err := conductor.RunGitCommit(context.Background(), conductor.GitCommitExecution{
		Workspace: PollinatorB.Path, Message: "feat: Pollinator B's change", Credential: credential, ConfiguredBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("codex commit: %v", err)
	}
	if result.Status != "committed" {
		t.Fatalf("codex commit status = %q, want committed", result.Status)
	}

	// B's commit must contain ONLY B's file.
	files := gitRun(t, PollinatorB.Path, "show", "--name-only", "--pretty=format:", "HEAD")
	if strings.Contains(files, "claude-work.txt") {
		t.Fatalf("Pollinator B's commit contains Pollinator A's work:\n%s", files)
	}
	if !strings.Contains(files, "codex-work.txt") {
		t.Fatalf("Pollinator B's commit is missing its own work:\n%s", files)
	}

	// B must have branched from the substrate's branch, not from A's branch.
	base := gitRun(t, PollinatorB.Path, "log", "--oneline", "trunk..HEAD")
	if strings.Count(base, "\n") > 0 {
		t.Fatalf("Pollinator B's branch carries more than its own commit:\n%s", base)
	}

	// A's uncommitted work must still be A's, and still uncommitted.
	if _, err := os.Stat(filepath.Join(PollinatorA.Path, "claude-work.txt")); err != nil {
		t.Fatalf("Pollinator A's work vanished from its own workspace: %v", err)
	}
	aStatus, err := conductor.RunGitStatus(context.Background(), conductor.GitStatusExecution{
		Workspace: PollinatorA.Path, ConfiguredBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("claude status: %v", err)
	}
	if aStatus.Clean {
		t.Fatal("Pollinator A's uncommitted work was absorbed by Pollinator B's commit")
	}
	if aStatus.Branch != "feat/claude" {
		t.Fatalf("Pollinator A is on %q, want to still be on its own branch", aStatus.Branch)
	}

	// A can now commit its own work, attributably.
	if _, err := conductor.RunGitCommit(context.Background(), conductor.GitCommitExecution{
		Workspace: PollinatorA.Path, Message: "feat: Pollinator A's change", Credential: credential, ConfiguredBranch: "trunk",
	}); err != nil {
		t.Fatalf("claude commit: %v", err)
	}
	aFiles := gitRun(t, PollinatorA.Path, "show", "--name-only", "--pretty=format:", "HEAD")
	if strings.Contains(aFiles, "codex-work.txt") {
		t.Fatalf("Pollinator A's commit contains Pollinator B's work:\n%s", aFiles)
	}

	// Both branches are visible in the substrate: a worktree shares the object
	// store, which is what keeps push, pull requests and human review working.
	branches := gitRun(t, path, "branch", "--list")
	for _, want := range []string{"feat/claude", "feat/codex"} {
		if !strings.Contains(branches, want) {
			t.Fatalf("substrate does not see %s:\n%s", want, branches)
		}
	}
}

// TestIsolatedWorkspaceArrivesReadyToWork is the cause-removal this slice is
// for. A delegated workspace used to arrive detached, so the Pollinator had to
// choose and create a branch — a decision that existed only so that a later
// guard could catch it being made badly.
//
// Now the workspace arrives ON an owned branch, cut from the resolved default
// branch. The Pollinator never chooses, so it cannot choose wrongly: committing
// onto the default branch is not refused here, it is unreachable, because no
// delegated workspace is ever on it.
func TestIsolatedWorkspaceArrivesReadyToWork(t *testing.T) {
	name, path := newIsolationSubstrate(t)
	workspace, err := conductor.ResolveDelegatedWorkspace(pollenContext("claude"), name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if workspace.Branch == "" {
		t.Fatal("workspace arrived with no branch — the Pollinator is being asked to choose one again")
	}
	if workspace.Branch == "trunk" {
		t.Fatal("workspace arrived on the default branch, which is exactly what must be impossible")
	}

	status, err := conductor.RunGitStatus(context.Background(), conductor.GitStatusExecution{
		Workspace: workspace.Path, ConfiguredBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.DetachedHead {
		t.Fatal("workspace arrived detached — the detached-head guard should be unreachable through the normal path")
	}
	if status.OnDefaultBranch {
		t.Fatal("workspace arrived on the default branch")
	}
	if !status.CommitAllowed {
		t.Fatalf("a freshly created workspace cannot commit: %s", status.BlockedReason)
	}

	// The Pollinator can work immediately, with no branch step of its own.
	if err := os.WriteFile(filepath.Join(workspace.Path, "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := conductor.RunGitCommit(context.Background(), conductor.GitCommitExecution{
		Workspace:        workspace.Path,
		Message:          "feat: work with no branch step",
		Credential:       conductor.ResolvedCredential{Identity: conductor.ResolvedIdentity{Name: "Bot", Email: "bot@example.com"}},
		ConfiguredBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("commit in a ready workspace: %v", err)
	}
	if result.Status != "committed" {
		t.Fatalf("result = %+v, want a commit", result)
	}
}

// TestOwnedWorkspaceBranchIsRegistered: a branch nobody recorded is a branch
// nobody can ever decide is finished, which is how the system came to litter.
func TestOwnedWorkspaceBranchIsRegistered(t *testing.T) {
	name, path := newIsolationSubstrate(t)
	workspace, err := conductor.ResolveDelegatedWorkspace(pollenContext("claude"), name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	owned := conductor.OwnedRefsFor(path)
	found := false
	for _, ref := range owned {
		if ref.Branch == workspace.Branch {
			found = true
			if ref.Pollen != "claude" || ref.Purpose != conductor.PurposeDelegatedWorkspace {
				t.Errorf("owned reference = %+v, want it attributed to claude's workspace", ref)
			}
			if ref.Base == "" {
				t.Error("owned reference has no recorded base — 'has this produced anything' becomes unanswerable")
			}
		}
	}
	if !found {
		t.Fatalf("workspace branch %q was not registered as owned; owned = %+v", workspace.Branch, owned)
	}
}

// TestWorkspaceBranchRotatesWhenFinished: a Pollinator returning to its workspace
// after its work landed starts from the current default branch, rather than
// piling the next task onto a branch that is already merged. A workspace whose
// branch holds unmerged commits is left strictly alone — that is work in
// progress, and resetting it would destroy exactly what this design protects.
func TestWorkspaceBranchRotatesWhenFinished(t *testing.T) {
	name, path := newIsolationSubstrate(t)
	ctx := pollenContext("claude")

	workspace, err := conductor.ResolveDelegatedWorkspace(ctx, name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	startingHead := gitRun(t, workspace.Path, "rev-parse", "HEAD")

	// The substrate's default branch moves on while the subject's empty
	// workspace sits idle.
	gitRun(t, path, "commit", "--allow-empty", "-m", "someone else's work")
	movedHead := gitRun(t, path, "rev-parse", "trunk")
	if movedHead == startingHead {
		t.Fatal("fixture did not advance the default branch")
	}

	// Returning to an EMPTY workspace rotates it onto the new default state.
	again, err := conductor.ResolveDelegatedWorkspace(ctx, name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("re-resolve: %v", err)
	}
	if again.Branch != workspace.Branch {
		t.Fatalf("branch changed name to %q, want the stable %q", again.Branch, workspace.Branch)
	}
	if head := gitRun(t, again.Path, "rev-parse", "HEAD"); head != movedHead {
		t.Fatalf("workspace head = %s, want it rebuilt on the current default %s", head, movedHead)
	}

	// Now the Pollinator has unmerged work: the workspace must be left alone.
	if err := os.WriteFile(filepath.Join(again.Path, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, again.Path, "add", "-A")
	gitRun(t, again.Path, "-c", "user.email=bot@example.com", "-c", "user.name=Bot", "commit", "-m", "unmerged work")
	workHead := gitRun(t, again.Path, "rev-parse", "HEAD")

	gitRun(t, path, "commit", "--allow-empty", "-m", "default moves again")

	third, err := conductor.ResolveDelegatedWorkspace(ctx, name, path, "claude", conductor.ResolvedCredential{})
	if err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if head := gitRun(t, third.Path, "rev-parse", "HEAD"); head != workHead {
		t.Fatalf("workspace head = %s, want the subject's unmerged work %s left untouched", head, workHead)
	}
}
