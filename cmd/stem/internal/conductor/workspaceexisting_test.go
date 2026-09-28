package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDelegatedWorkspaceExistingOnlyDoesNotCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repository := t.TempDir()
	gitIn(t, repository, "init", "-q", "-b", "main")
	gitIn(t, repository, "commit", "-q", "--allow-empty", "-m", "base")
	beforeBranches := gitIn(t, repository, "branch", "--format=%(refname:short)")
	workspacePath := filepath.Join(delegatedWorkspaceRoot(), "demo", "pollen")

	_, err := ResolveDelegatedWorkspaceWithMode(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, ExistingDelegatedWorkspaceOnly)
	if !errors.Is(err, ErrDelegatedWorkspaceAbsent) {
		t.Fatalf("resolve missing workspace error = %v, want ErrDelegatedWorkspaceAbsent", err)
	}
	if _, err := os.Stat(workspacePath); !os.IsNotExist(err) {
		t.Fatalf("missing workspace was created: stat error = %v", err)
	}
	if got := gitIn(t, repository, "branch", "--format=%(refname:short)"); got != beforeBranches {
		t.Fatalf("branch refs changed from %q to %q", beforeBranches, got)
	}
	if err := os.MkdirAll(filepath.Dir(workspacePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repository, workspacePath); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveDelegatedWorkspaceWithMode(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, ExistingDelegatedWorkspaceOnly)
	if !errors.Is(err, ErrDelegatedWorkspaceAbsent) {
		t.Fatalf("symlinked workspace resolution error = %v, want absent", err)
	}
	if got := gitIn(t, repository, "branch", "--format=%(refname:short)"); got != beforeBranches {
		t.Fatalf("symlinked workspace resolution changed repository branches to %q", got)
	}
}

func TestResolveDelegatedWorkspaceExistingOnlyDoesNotRotateAndKeepsPollenIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repository := t.TempDir()
	gitIn(t, repository, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "base.txt")
	gitIn(t, repository, "commit", "-q", "-m", "base")
	workspaceHead := gitIn(t, repository, "rev-parse", "HEAD")
	branch := ownedWorkspaceBranchName("pollen-one")
	workspacePath := filepath.Join(delegatedWorkspaceRoot(), "demo", "pollen-one")
	if err := os.MkdirAll(filepath.Dir(workspacePath), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "worktree", "add", "-q", "-b", branch, workspacePath, workspaceHead)
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("advanced default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "base.txt")
	gitIn(t, repository, "commit", "-q", "-m", "advance default")
	if err := RegisterOwnedRef(OwnedRef{
		Repository: repository, Branch: branch, Purpose: PurposeDelegatedWorkspace,
		Pollen: "pollen-one", Base: workspaceHead,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveDelegatedWorkspaceWithMode(context.Background(), "demo", repository, "pollen-one", ResolvedCredential{}, ExistingDelegatedWorkspaceOnly)
	if err != nil {
		t.Fatalf("resolve existing workspace: %v", err)
	}
	if resolved.Path != workspacePath || resolved.Branch != branch || !resolved.Isolated {
		t.Fatalf("resolved workspace = %+v, want existing isolated branch %s", resolved, branch)
	}
	if got := gitIn(t, workspacePath, "rev-parse", "HEAD"); got != workspaceHead {
		t.Fatalf("existing-only resolution rotated HEAD to %s, want %s", got, workspaceHead)
	}
	if got := gitIn(t, workspacePath, "branch", "--show-current"); got != branch {
		t.Fatalf("existing-only resolution switched branch to %s, want %s", got, branch)
	}

	other, err := ResolveDelegatedWorkspaceWithMode(context.Background(), "demo", repository, "pollen-two", ResolvedCredential{}, ExistingDelegatedWorkspaceOnly)
	if !errors.Is(err, ErrDelegatedWorkspaceAbsent) {
		t.Fatalf("second Pollen resolution error = %v, want absent", err)
	}
	if other.Path == resolved.Path {
		t.Fatalf("two Pollens resolved to the same workspace path %q", other.Path)
	}
	if _, err := os.Stat(other.Path); !os.IsNotExist(err) {
		t.Fatalf("second Pollen workspace was created: %v", err)
	}
}
