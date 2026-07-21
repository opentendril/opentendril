package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGitStatusValidatesExecution(t *testing.T) {
	if _, err := RunGitStatus(context.Background(), GitStatusExecution{}); err == nil {
		t.Fatal("missing workspace accepted")
	}
}

// TestRunGitStatusReportsCleanFeatureBranch is the ordinary case: a clean
// workspace on a feature branch, with the default branch resolved and a commit
// predicted to be allowed.
func TestRunGitStatusReportsCleanFeatureBranch(t *testing.T) {
	repo := newBranchRepo(t, "feat/new-leaf", "trunk")

	status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: repo})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Branch != "feat/new-leaf" {
		t.Fatalf("branch = %q, want feat/new-leaf", status.Branch)
	}
	if status.DefaultBranch != "trunk" || status.DefaultBranchSource != string(DefaultBranchFromRemoteHead) {
		t.Fatalf("default branch = %q from %q, want trunk from the local remote head", status.DefaultBranch, status.DefaultBranchSource)
	}
	if !status.Clean || status.ChangeCount != 0 || len(status.Changes) != 0 {
		t.Fatalf("status = %+v, want a clean workspace", status)
	}
	if !status.HasCommits || status.Head == "" || status.DetachedHead {
		t.Fatalf("status = %+v, want a normal committed workspace", status)
	}
	if status.Repository != "opentendril/opentendril" {
		t.Fatalf("repository = %q, want owner/repo parsed from origin", status.Repository)
	}
	if status.OnDefaultBranch || !status.CommitAllowed || status.BlockedReason != "" {
		t.Fatalf("status = %+v, want a commit predicted to be allowed", status)
	}
	// Never pushed: reported as no upstream rather than as an error.
	if status.Upstream != "" || status.Ahead != 0 || status.Behind != 0 {
		t.Fatalf("status = %+v, want no upstream for an unpushed branch", status)
	}
}

// TestRunGitStatusClassifiesChanges covers every change kind the porcelain
// parser distinguishes, including the rename entry whose original path is a
// separate field and must not be counted twice.
func TestRunGitStatusClassifiesChanges(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/x", "trunk")
	for _, name := range []string{"modify.txt", "delete.txt", "rename-from.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("original\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, err := runGitCommand(ctx, repo, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo, "modify.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if err := os.Remove(filepath.Join(repo, "delete.txt")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "mv", "rename-from.txt", "rename-to.txt"); err != nil {
		t.Fatalf("mv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("untracked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "added.txt"), []byte("added\n"), 0o644); err != nil {
		t.Fatalf("added: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "added.txt"); err != nil {
		t.Fatalf("add added.txt: %v", err)
	}

	status, err := RunGitStatus(ctx, GitStatusExecution{Workspace: repo})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Clean {
		t.Fatal("a dirty workspace was reported clean")
	}
	byPath := map[string]string{}
	for _, change := range status.Changes {
		byPath[change.Path] = change.Kind
	}
	for path, want := range map[string]string{
		"modify.txt":    "modified",
		"delete.txt":    "deleted",
		"rename-to.txt": "renamed",
		"untracked.txt": "untracked",
		"added.txt":     "added",
	} {
		if got := byPath[path]; got != want {
			t.Errorf("%s classified as %q, want %q", path, got, want)
		}
	}
	// The rename's original path is a separate porcelain field and must not
	// appear as a change of its own.
	if _, found := byPath["rename-from.txt"]; found {
		t.Error("a rename's original path was counted as its own change")
	}
	if status.ChangeCount != len(status.Changes) || status.Truncated {
		t.Fatalf("status = %+v, want an untruncated list matching the count", status)
	}
	if status.Modified != 1 || status.Deleted != 1 || status.Renamed != 1 || status.Untracked != 1 || status.Added != 1 {
		t.Fatalf("counts = %+v, want one of each kind", status)
	}
}

// TestRunGitStatusBoundsChangeList proves a large change cannot flood an
// agent's context, and that the total is still reported honestly.
func TestRunGitStatusBoundsChangeList(t *testing.T) {
	repo := newBranchRepo(t, "feat/x", "trunk")
	total := gitStatusFileLimit + 17
	for i := 0; i < total; i++ {
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("file-%03d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: repo})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(status.Changes) != gitStatusFileLimit {
		t.Fatalf("returned %d change(s), want the list capped at %d", len(status.Changes), gitStatusFileLimit)
	}
	if !status.Truncated {
		t.Fatal("a capped list was not flagged as truncated")
	}
	if status.ChangeCount != total {
		t.Fatalf("change count = %d, want the true total %d even when the list is capped", status.ChangeCount, total)
	}
}

// TestRunGitStatusDescribesUnusualStates: a repository with no commits and a
// detached head are described precisely rather than refused — those are the
// states an agent most needs explained.
func TestRunGitStatusDescribesUnusualStates(t *testing.T) {
	ctx := context.Background()

	fresh := t.TempDir()
	if _, err := runGitCommand(ctx, fresh, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	status, err := RunGitStatus(ctx, GitStatusExecution{Workspace: fresh})
	if err != nil {
		t.Fatalf("status on a fresh repository: %v", err)
	}
	if status.HasCommits || status.Head != "" || status.DetachedHead {
		t.Fatalf("status = %+v, want a no-commits report", status)
	}

	repo := newBranchRepo(t, "feat/x", "trunk")
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", strings.TrimSpace(head)); err != nil {
		t.Fatalf("detach: %v", err)
	}
	status, err = RunGitStatus(ctx, GitStatusExecution{Workspace: repo})
	if err != nil {
		t.Fatalf("status on a detached head: %v", err)
	}
	if !status.DetachedHead || status.Branch != "" {
		t.Fatalf("status = %+v, want a detached-head report", status)
	}
	// A detached head is not the default branch, so a commit is not blocked
	// by this guard — the honest answer, not a defensive guess.
	if !status.CommitAllowed {
		t.Fatalf("status = %+v, want a commit allowed on a detached head", status)
	}
}

// TestRunGitStatusReportsUpstreamDivergence covers the ahead/behind counts an
// agent needs before deciding whether to push.
func TestRunGitStatusReportsUpstreamDivergence(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if _, err := runGitCommand(ctx, remote, "init", "--bare"); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	repo := newBranchRepo(t, "feat/x", "trunk")
	if _, err := runGitCommand(ctx, repo, "remote", "set-url", "origin", remote); err != nil {
		t.Fatalf("set-url: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "push", "-u", "origin", "feat/x"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ahead.txt"), []byte("ahead\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "ahead by one"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	status, err := RunGitStatus(ctx, GitStatusExecution{Workspace: repo})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Upstream == "" {
		t.Fatal("no upstream reported for a pushed branch")
	}
	if status.Ahead != 1 || status.Behind != 0 {
		t.Fatalf("ahead/behind = %d/%d, want 1/0", status.Ahead, status.Behind)
	}
}
