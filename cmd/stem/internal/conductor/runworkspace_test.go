package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func prepareRunWorkspaceTest(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	repo := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Run Workspace Test"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	for _, args := range [][]string{{"add", "shared.txt"}, {"commit", "-m", "base"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	return repo, strings.TrimSpace(base)
}

func TestRunWorkspacesAreIndependent(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)

	first, err := CreateRunWorkspace(ctx, repo, "run-a", base)
	if err != nil {
		t.Fatalf("create run A workspace: %v", err)
	}
	second, err := CreateRunWorkspace(ctx, repo, "run-b", base)
	if err != nil {
		t.Fatalf("create run B workspace: %v", err)
	}

	if first.Path == second.Path {
		t.Fatal("run workspaces share a filesystem path")
	}
	if first.Branch != "sprout/task-run-a" || second.Branch != "sprout/task-run-b" {
		t.Fatalf("branches = %q and %q, want step-scoped sprout/task branches", first.Branch, second.Branch)
	}
	if first.BaseCommit != base || second.BaseCommit != base {
		t.Fatalf("base commits = %q and %q, want explicitly supplied %q", first.BaseCommit, second.BaseCommit, base)
	}

	if err := os.WriteFile(filepath.Join(first.Path, "shared.txt"), []byte("run A\n"), 0o644); err != nil {
		t.Fatalf("write run A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second.Path, "shared.txt"), []byte("run B\n"), 0o644); err != nil {
		t.Fatalf("write run B: %v", err)
	}
	assertFileContents(t, filepath.Join(first.Path, "shared.txt"), "run A\n")
	assertFileContents(t, filepath.Join(second.Path, "shared.txt"), "run B\n")
	assertFileContents(t, filepath.Join(repo, "shared.txt"), "base\n")
	assertGitClean(t, repo)

	if err := os.WriteFile(filepath.Join(first.Path, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("restore run A fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second.Path, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("restore run B fixture: %v", err)
	}
	if err := first.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup run A: %v", err)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("cleanup run A removed run B workspace: %v", err)
	}
	if !branchExists(t, repo, second.Branch) {
		t.Fatal("cleanup run A removed run B branch")
	}
	assertFileContents(t, filepath.Join(second.Path, "shared.txt"), "base\n")
	assertFileContents(t, filepath.Join(repo, "shared.txt"), "base\n")

	if err := second.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup run B: %v", err)
	}
	assertGitClean(t, repo)
}

func TestRunWorkspaceUseCanOverlapAfterAllocation(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	first, err := CreateRunWorkspace(ctx, repo, "parallel-a", base)
	if err != nil {
		t.Fatalf("create run A workspace: %v", err)
	}
	second, err := CreateRunWorkspace(ctx, repo, "parallel-b", base)
	if err != nil {
		t.Fatalf("create run B workspace: %v", err)
	}

	start := make(chan struct{})
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for _, workspace := range []RunWorkspace{first, second} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if err := os.WriteFile(filepath.Join(workspace.Path, "shared.txt"), []byte(workspace.StepID+"\n"), 0o644); err != nil {
				errors <- err
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}

	assertFileContents(t, filepath.Join(first.Path, "shared.txt"), "parallel-a\n")
	assertFileContents(t, filepath.Join(second.Path, "shared.txt"), "parallel-b\n")
	assertFileContents(t, filepath.Join(repo, "shared.txt"), "base\n")
	assertGitClean(t, repo)
}

func TestRunWorkspaceCollisionDoesNotDestroyExistingRun(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	first, err := CreateRunWorkspace(ctx, repo, "collision", base)
	if err != nil {
		t.Fatalf("create existing workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "shared.txt"), []byte("existing work\n"), 0o644); err != nil {
		t.Fatalf("write existing work: %v", err)
	}

	if _, err := CreateRunWorkspace(ctx, repo, "collision", base); err == nil {
		t.Fatal("colliding run workspace allocation succeeded")
	}
	assertFileContents(t, filepath.Join(first.Path, "shared.txt"), "existing work\n")
	if !branchExists(t, repo, first.Branch) {
		t.Fatal("colliding allocation removed the existing run branch")
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("colliding allocation removed the existing run workspace: %v", err)
	}

	if _, err := CreateRunWorkspace(ctx, repo, "invalid-start", "not-a-revision"); err == nil {
		t.Fatal("invalid start revision was accepted")
	}
	assertFileContents(t, filepath.Join(first.Path, "shared.txt"), "existing work\n")

	if err := os.WriteFile(filepath.Join(first.Path, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("restore existing workspace: %v", err)
	}
	if err := first.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup existing workspace: %v", err)
	}
}

func TestRunWorkspaceCleanupCannotTargetReplacementRun(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	first, err := CreateRunWorkspace(ctx, repo, "reused", base)
	if err != nil {
		t.Fatalf("create first workspace: %v", err)
	}
	if err := first.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup first workspace: %v", err)
	}

	second, err := CreateRunWorkspace(ctx, repo, "reused", base)
	if err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	if first.RunID == second.RunID {
		t.Fatal("replacement workspace reused the first run identity")
	}
	if err := first.Cleanup(ctx, ResolvedCredential{}); err == nil {
		t.Fatal("stale cleanup handle removed the replacement workspace")
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("stale cleanup removed replacement path: %v", err)
	}
	if !branchExists(t, repo, second.Branch) {
		t.Fatal("stale cleanup removed replacement branch")
	}

	if err := second.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup replacement workspace: %v", err)
	}
}

func TestRunWorkspaceCleanupRequiresExactBaseAndRunID(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "identity", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	missingBase := workspace
	missingBase.BaseCommit = ""
	if err := missingBase.Cleanup(ctx, ResolvedCredential{}); err == nil {
		t.Fatal("cleanup accepted an empty base commit")
	}
	wrongRunID := workspace
	wrongRunID.RunID = "other-run"
	if err := wrongRunID.Cleanup(ctx, ResolvedCredential{}); err == nil {
		t.Fatal("cleanup accepted a mismatched run ID")
	}
	if _, err := os.Stat(workspace.Path); err != nil {
		t.Fatalf("identity validation removed workspace: %v", err)
	}
	if !branchExists(t, repo, workspace.Branch) {
		t.Fatal("identity validation removed branch")
	}

	if err := workspace.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
}

func TestRunWorkspaceRejectsRootSymlinkIntoRepository(t *testing.T) {
	repo, base := prepareRunWorkspaceTest(t)
	linkedRoot := filepath.Join(t.TempDir(), "run-workspaces")
	if err := os.Symlink(repo, linkedRoot); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	t.Setenv(runWorkspaceRootEnv, linkedRoot)

	if _, err := CreateRunWorkspace(context.Background(), repo, "symlink", base); err == nil {
		t.Fatal("run workspace root symlink into repository was accepted")
	}
	if branchExists(t, repo, "sprout/task-symlink") {
		t.Fatal("rejected symlink root created a branch")
	}
}

func TestRunWorkspaceUsesGitTopLevelForContainment(t *testing.T) {
	repo, base := prepareRunWorkspaceTest(t)
	subdirectory := filepath.Join(repo, "subdirectory")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatalf("create repository subdirectory: %v", err)
	}
	t.Setenv(runWorkspaceRootEnv, filepath.Join(repo, "run-workspaces"))

	if _, err := CreateRunWorkspace(context.Background(), subdirectory, "subdirectory", base); err == nil {
		t.Fatal("workspace inside Git top-level was accepted from a repository subdirectory")
	}
	if branchExists(t, repo, "sprout/task-subdirectory") {
		t.Fatal("containment refusal created a branch")
	}
}

func TestRunWorkspaceFailedGitAllocationRollsBack(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing checkout hook: %v", err)
	}

	if _, err := CreateRunWorkspace(ctx, repo, "hook-failure", base); err == nil {
		t.Fatal("worktree allocation with a failing Git hook succeeded")
	}
	if branchExists(t, repo, "sprout/task-hook-failure") {
		t.Fatal("failed allocation left its branch behind")
	}
	if refs := OwnedRefsFor(repo); len(refs) != 0 {
		t.Fatalf("failed allocation left ownership behind: %+v", refs)
	}
}

func TestRunWorkspaceRefusesUnownedBranchCollision(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	branch := "sprout/task-unowned"
	if _, err := runGitCommand(ctx, repo, "branch", branch, base); err != nil {
		t.Fatalf("create unowned branch: %v", err)
	}

	if _, err := CreateRunWorkspace(ctx, repo, "unowned", base); err == nil {
		t.Fatal("allocation reused an unowned branch")
	}
	if !branchExists(t, repo, branch) {
		t.Fatal("unowned collision branch was removed")
	}
	assertGitClean(t, repo)
}

func TestRunWorkspaceRefusesUnrelatedOwnedCollision(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	branch := "sprout/task-owned-by-delegated-workspace"
	if err := RegisterOwnedRef(OwnedRef{
		Repository: repo,
		Branch:     branch,
		Purpose:    PurposeDelegatedWorkspace,
		Pollen:     "pollen-that-is-not-run-identity",
		Base:       base,
	}); err != nil {
		t.Fatalf("register unrelated owned branch: %v", err)
	}

	if _, err := CreateRunWorkspace(ctx, repo, "owned-by-delegated-workspace", base); err == nil {
		t.Fatal("allocation reused unrelated owned state")
	}
	refs := OwnedRefsFor(repo)
	if len(refs) != 1 || refs[0].Purpose != PurposeDelegatedWorkspace || refs[0].Pollen != "pollen-that-is-not-run-identity" {
		t.Fatalf("unrelated owned state changed: %+v", refs)
	}
	assertGitClean(t, repo)
}

func TestRunWorkspaceReclaimsUnusedBranchAndKeepsFruit(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)

	empty, err := CreateRunWorkspace(ctx, repo, "empty", base)
	if err != nil {
		t.Fatalf("create empty workspace: %v", err)
	}
	if err := empty.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup empty workspace: %v", err)
	}
	if branchExists(t, repo, empty.Branch) {
		t.Fatal("cleanup retained an unused owned run branch")
	}
	if _, err := os.Stat(empty.Path); !os.IsNotExist(err) {
		t.Fatalf("empty workspace path still exists: %v", err)
	}

	fruit, err := CreateRunWorkspace(ctx, repo, "fruit", base)
	if err != nil {
		t.Fatalf("create fruit workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fruit.Path, "fruit.txt"), []byte("reviewable fruit\n"), 0o644); err != nil {
		t.Fatalf("write fruit: %v", err)
	}
	if _, err := runGitCommand(ctx, fruit.Path, "add", "fruit.txt"); err != nil {
		t.Fatalf("stage fruit: %v", err)
	}
	if _, err := runGitCommand(ctx, fruit.Path, "commit", "-m", "fruit"); err != nil {
		t.Fatalf("commit fruit: %v", err)
	}
	fruitCommit, err := runGitCommand(ctx, repo, "rev-parse", fruit.Branch)
	if err != nil {
		t.Fatalf("read fruit branch: %v", err)
	}
	if err := fruit.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup fruit workspace: %v", err)
	}
	if !branchExists(t, repo, fruit.Branch) {
		t.Fatal("cleanup destroyed a branch carrying committed Fruit")
	}
	retainedCommit, err := runGitCommand(ctx, repo, "rev-parse", fruit.Branch)
	if err != nil {
		t.Fatalf("read retained fruit branch: %v", err)
	}
	if strings.TrimSpace(retainedCommit) != strings.TrimSpace(fruitCommit) {
		t.Fatalf("retained branch moved from %q to %q", fruitCommit, retainedCommit)
	}
	assertFileContents(t, filepath.Join(repo, "shared.txt"), "base\n")
	assertGitClean(t, repo)
}

func TestRunWorkspaceCleanupRefusesUncommittedWork(t *testing.T) {
	ctx := context.Background()
	repo, base := prepareRunWorkspaceTest(t)
	workspace, err := CreateRunWorkspace(ctx, repo, "dirty", base)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "shared.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write uncommitted work: %v", err)
	}

	if err := workspace.Cleanup(ctx, ResolvedCredential{}); err == nil {
		t.Fatal("cleanup discarded uncommitted run work")
	}
	assertFileContents(t, filepath.Join(workspace.Path, "shared.txt"), "uncommitted\n")
	if !branchExists(t, repo, workspace.Branch) {
		t.Fatal("cleanup removed the branch holding uncommitted run state")
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertGitClean(t *testing.T, repo string) {
	t.Helper()
	status, err := runGitCommand(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		t.Fatalf("repository is not clean: %q", status)
	}
}
