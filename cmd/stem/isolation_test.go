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
// Before isolation, two subjects granted the same substrate silently corrupted
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

func subjectContext(subject string) context.Context {
	return core.WithDelegationSubject(context.Background(), subject)
}

// TestDelegatedSubjectsGetSeparateWorkspaces is the core property: two
// subjects on one substrate never share a working tree.
func TestDelegatedSubjectsGetSeparateWorkspaces(t *testing.T) {
	name, path := newIsolationSubstrate(t)

	first, err := conductor.ResolveDelegatedWorkspace(subjectContext("agent-a"), name, path, "agent-a")
	if err != nil {
		t.Fatalf("resolve for agent-a: %v", err)
	}
	second, err := conductor.ResolveDelegatedWorkspace(subjectContext("agent-b"), name, path, "agent-b")
	if err != nil {
		t.Fatalf("resolve for agent-b: %v", err)
	}

	if first.Path == second.Path {
		t.Fatal("two subjects resolved to the same workspace — this is exactly the shared tree that let one agent commit another's work")
	}
	if first.Path == path || second.Path == path {
		t.Fatal("a delegated workspace resolved to the substrate's own checkout")
	}
	if !first.Isolated || !second.Isolated {
		t.Fatalf("workspaces not reported as isolated: %+v %+v", first, second)
	}
	// Reuse is stable: the same subject always returns to its own tree, which
	// is what lets an agent's sequence of calls stay consistent without the
	// agent tracking anything.
	again, err := conductor.ResolveDelegatedWorkspace(subjectContext("agent-a"), name, path, "agent-a")
	if err != nil {
		t.Fatalf("re-resolve for agent-a: %v", err)
	}
	if again.Path != first.Path {
		t.Fatalf("agent-a got %s then %s — a subject's workspace must be stable", first.Path, again.Path)
	}
}

// TestNonDelegatedCallUsesOperatorCheckout: a human at a terminal carries no
// subject and must keep seeing their own working copy.
func TestNonDelegatedCallUsesOperatorCheckout(t *testing.T) {
	name, path := newIsolationSubstrate(t)

	workspace, err := conductor.ResolveDelegatedWorkspace(context.Background(), name, path, "")
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

// TestConcurrentSubjectsDoNotCorruptEachOther replays the exact interleaving
// that was reproduced before this work: A branches and starts editing, B
// branches and commits. Before isolation, B's commit contained A's file, on
// B's branch, under B's identity, and A's branch was left empty.
func TestConcurrentSubjectsDoNotCorruptEachOther(t *testing.T) {
	name, path := newIsolationSubstrate(t)
	credential := conductor.ResolvedCredential{
		Identity: conductor.ResolvedIdentity{Name: "Bot", Email: "bot@example.com"},
	}

	agentA, err := conductor.ResolveDelegatedWorkspace(subjectContext("agent-a"), name, path, "agent-a")
	if err != nil {
		t.Fatalf("workspace for agent-a: %v", err)
	}
	agentB, err := conductor.ResolveDelegatedWorkspace(subjectContext("agent-b"), name, path, "agent-b")
	if err != nil {
		t.Fatalf("workspace for agent-b: %v", err)
	}

	// A creates its branch and starts work (uncommitted).
	if _, err := conductor.RunGitBranch(context.Background(), conductor.GitBranchExecution{
		Workspace: agentA.Path, Branch: "feat/agent-a", ConfiguredBranch: "trunk",
	}); err != nil {
		t.Fatalf("agent-a branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentA.Path, "agent-a-work.txt"), []byte("SECRET-A\n"), 0o644); err != nil {
		t.Fatalf("agent-a write: %v", err)
	}

	// B creates its branch and commits its own work, concurrently.
	if _, err := conductor.RunGitBranch(context.Background(), conductor.GitBranchExecution{
		Workspace: agentB.Path, Branch: "feat/agent-b", ConfiguredBranch: "trunk",
	}); err != nil {
		t.Fatalf("agent-b branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentB.Path, "agent-b-work.txt"), []byte("work-b\n"), 0o644); err != nil {
		t.Fatalf("agent-b write: %v", err)
	}
	result, err := conductor.RunGitCommit(context.Background(), conductor.GitCommitExecution{
		Workspace: agentB.Path, Message: "feat: agent B's change", Credential: credential, ConfiguredBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("agent-b commit: %v", err)
	}
	if result.Status != "committed" {
		t.Fatalf("agent-b commit status = %q, want committed", result.Status)
	}

	// B's commit must contain ONLY B's file.
	files := gitRun(t, agentB.Path, "show", "--name-only", "--pretty=format:", "HEAD")
	if strings.Contains(files, "agent-a-work.txt") {
		t.Fatalf("agent B's commit contains agent A's work:\n%s", files)
	}
	if !strings.Contains(files, "agent-b-work.txt") {
		t.Fatalf("agent B's commit is missing its own work:\n%s", files)
	}

	// B must have branched from the substrate's branch, not from A's branch.
	base := gitRun(t, agentB.Path, "log", "--oneline", "trunk..HEAD")
	if strings.Count(base, "\n") > 0 {
		t.Fatalf("agent B's branch carries more than its own commit:\n%s", base)
	}

	// A's uncommitted work must still be A's, and still uncommitted.
	if _, err := os.Stat(filepath.Join(agentA.Path, "agent-a-work.txt")); err != nil {
		t.Fatalf("agent A's work vanished from its own workspace: %v", err)
	}
	aStatus, err := conductor.RunGitStatus(context.Background(), conductor.GitStatusExecution{
		Workspace: agentA.Path, ConfiguredBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("agent-a status: %v", err)
	}
	if aStatus.Clean {
		t.Fatal("agent A's uncommitted work was absorbed by agent B's commit")
	}
	if aStatus.Branch != "feat/agent-a" {
		t.Fatalf("agent A is on %q, want to still be on its own branch", aStatus.Branch)
	}

	// A can now commit its own work, attributably.
	if _, err := conductor.RunGitCommit(context.Background(), conductor.GitCommitExecution{
		Workspace: agentA.Path, Message: "feat: agent A's change", Credential: credential, ConfiguredBranch: "trunk",
	}); err != nil {
		t.Fatalf("agent-a commit: %v", err)
	}
	aFiles := gitRun(t, agentA.Path, "show", "--name-only", "--pretty=format:", "HEAD")
	if strings.Contains(aFiles, "agent-b-work.txt") {
		t.Fatalf("agent A's commit contains agent B's work:\n%s", aFiles)
	}

	// Both branches are visible in the substrate: a worktree shares the object
	// store, which is what keeps push, pull requests and human review working.
	branches := gitRun(t, path, "branch", "--list")
	for _, want := range []string{"feat/agent-a", "feat/agent-b"} {
		if !strings.Contains(branches, want) {
			t.Fatalf("substrate does not see %s:\n%s", want, branches)
		}
	}
}

// TestIsolatedWorkspaceArrivesReadyToWork is the cause-removal this slice is
// for. A delegated workspace used to arrive detached, so the agent had to
// choose and create a branch — a decision that existed only so that a later
// guard could catch it being made badly.
//
// Now the workspace arrives ON an owned branch, cut from the resolved default
// branch. The agent never chooses, so it cannot choose wrongly: committing
// onto the default branch is not refused here, it is unreachable, because no
// delegated workspace is ever on it.
func TestIsolatedWorkspaceArrivesReadyToWork(t *testing.T) {
	name, path := newIsolationSubstrate(t)
	workspace, err := conductor.ResolveDelegatedWorkspace(subjectContext("agent-a"), name, path, "agent-a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if workspace.Branch == "" {
		t.Fatal("workspace arrived with no branch — the agent is being asked to choose one again")
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

	// The agent can work immediately, with no branch step of its own.
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
	workspace, err := conductor.ResolveDelegatedWorkspace(subjectContext("agent-a"), name, path, "agent-a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	owned := conductor.OwnedRefsFor(path)
	found := false
	for _, ref := range owned {
		if ref.Branch == workspace.Branch {
			found = true
			if ref.Subject != "agent-a" || ref.Purpose != conductor.PurposeDelegatedWorkspace {
				t.Errorf("owned reference = %+v, want it attributed to agent-a's workspace", ref)
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
