package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if err := os.WriteFile(filepath.Join(workspacePath, "work.txt"), []byte("committed work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, workspacePath, "add", "work.txt")
	gitIn(t, workspacePath, "-c", "user.email=bot@example.com", "-c", "user.name=Bot", "commit", "-q", "-m", "unmerged work")
	workHead := gitIn(t, workspacePath, "rev-parse", "HEAD")
	continued, err := ResolveDelegatedWorkspaceWithMode(context.Background(), "demo", repository, "pollen-one", ResolvedCredential{}, ExistingDelegatedWorkspaceOnly)
	if err != nil {
		t.Fatalf("existing-only resolution blocked active unique branch work: %v", err)
	}
	if continued.Path != workspacePath || continued.Branch != branch {
		t.Fatalf("existing-only resolution = %+v, want the same active workspace %s on %s", continued, workspacePath, branch)
	}
	if got := gitIn(t, workspacePath, "rev-parse", "HEAD"); got != workHead {
		t.Fatalf("existing-only resolution changed HEAD to %s, want preserved active work %s", got, workHead)
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

func TestExistingWorkspaceResolverWaitsForWorkspaceLockBeforeRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repository := t.TempDir()
	gitIn(t, repository, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "base.txt")
	gitIn(t, repository, "commit", "-q", "-m", "base")
	gitIn(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	baseHead := gitIn(t, repository, "rev-parse", "HEAD")
	branch := ownedWorkspaceBranchName("pollen")
	workspacePath := filepath.Join(delegatedWorkspaceRoot(), "demo", "pollen")
	if err := os.MkdirAll(filepath.Dir(workspacePath), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "worktree", "add", "-q", "-b", branch, workspacePath, baseHead)
	if err := RegisterOwnedRef(OwnedRef{
		Repository: repository,
		Branch:     branch,
		Purpose:    PurposeDelegatedWorkspace,
		Pollen:     "pollen",
		Base:       baseHead,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("advanced default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "base.txt")
	gitIn(t, repository, "commit", "-q", "-m", "advance default")
	advancedHead := gitIn(t, repository, "rev-parse", "HEAD")

	originalRun := runGitCommitCommandFn
	branchInspection := make(chan struct{}, 1)
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		if filepath.Clean(dir) == filepath.Clean(workspacePath) && len(args) == 2 && args[0] == "branch" && args[1] == "--show-current" {
			select {
			case branchInspection <- struct{}{}:
			default:
			}
		}
		return originalRun(ctx, dir, args...)
	}
	t.Cleanup(func() { runGitCommitCommandFn = originalRun })

	unlockOperation := LockWorkspace(workspacePath)
	resolverStarted := make(chan struct{})
	resolved := make(chan error, 1)
	go func() {
		close(resolverStarted)
		_, err := ResolveDelegatedWorkspace(context.Background(), "demo", repository, "pollen", ResolvedCredential{})
		resolved <- err
	}()
	<-resolverStarted

	select {
	case <-branchInspection:
		unlockOperation()
		select {
		case <-resolved:
		case <-time.After(5 * time.Second):
			t.Fatal("resolver did not finish after the lock was released")
		}
		t.Fatal("resolver inspected or rotated the workspace branch while another operation held its lock")
	case err := <-resolved:
		unlockOperation()
		t.Fatalf("resolver returned while another operation held its lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := gitIn(t, workspacePath, "rev-parse", "HEAD"); got != baseHead {
		unlockOperation()
		t.Fatalf("workspace HEAD changed under operation lock to %s, want %s", got, baseHead)
	}
	if got := gitIn(t, workspacePath, "branch", "--show-current"); got != branch {
		unlockOperation()
		t.Fatalf("workspace branch changed under operation lock to %s, want %s", got, branch)
	}
	unlockOperation()

	select {
	case err := <-resolved:
		if err != nil {
			t.Fatalf("resolver after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolver remained blocked after operation released the workspace lock")
	}
	if got := gitIn(t, workspacePath, "rev-parse", "HEAD"); got != advancedHead {
		t.Fatalf("resolver HEAD after lock release = %s, want rotated base %s", got, advancedHead)
	}
	if got := gitIn(t, workspacePath, "branch", "--show-current"); got != branch {
		t.Fatalf("resolver branch after rotation = %s, want %s", got, branch)
	}
	if strings.TrimSpace(gitIn(t, repository, "branch", "--show-current")) != "main" {
		t.Fatal("resolver changed the substrate checkout branch")
	}
}
