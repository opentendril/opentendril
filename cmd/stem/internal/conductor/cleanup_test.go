package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

func TestRunSproutRestoresHostStashAfterCanceledContext(t *testing.T) {
	root := t.TempDir()
	if _, err := runGitCommand(context.Background(), root, "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.email", "test@example.com"); err != nil {
		t.Fatalf("git config email failed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("git config name failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatalf("write baseline file: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "add", "README.md"); err != nil {
		t.Fatalf("git add baseline: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "commit", "-m", "baseline"); err != nil {
		t.Fatalf("git commit baseline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("host change\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	chdirToTempDir(t)
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir repo root: %v", err)
	}

	originalPreflight := runSproutPreflightChecksFn
	originalEnsureImage := ensureSproutImageFn
	originalStartSession := startTerrariumSessionFn
	originalNewSprout := newSproutFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalInjectCache := injectMycorrhizalCacheFn
	originalRestore := restoreHostStashFn

	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		ensureSproutImageFn = originalEnsureImage
		startTerrariumSessionFn = originalStartSession
		newSproutFn = originalNewSprout
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		injectMycorrhizalCacheFn = originalInjectCache
		restoreHostStashFn = originalRestore
	})

	runSproutPreflightChecksFn = func(ctx context.Context) error { return nil }
	generateRepoMapFn = func(ctx context.Context, workspace string) (string, error) {
		return "# Repo Map", nil
	}
	generateMemoryMapFn = func(ctx context.Context, workspace string) (string, error) {
		return "", nil
	}
	ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
	createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) {
		shadowPath := filepath.Join(root, "shadow-worktree")
		if err := os.MkdirAll(shadowPath, 0o755); err != nil {
			return "", err
		}
		return shadowPath, nil
	}
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {
		_ = os.RemoveAll(shadowPath)
	}
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}

	var restored bool
	restoreHostStashFn = func(ctx context.Context, root string) error {
		restored = true
		if ctx.Err() != nil {
			t.Fatalf("restoreHostStash called with canceled context: %v", ctx.Err())
		}
		return originalRestore(ctx, root)
	}

	ctx, cancel := context.WithCancel(context.Background())
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, command []string, extraEnv ...string) (toolSession, error) {
		cancel()
		return nil, errors.New("stop before terrarium starts")
	}
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string, sessionID string) (sproutRunner, error) {
		return nil, errors.New("Sprout should not start")
	}

	orch := &DockerOrchestrator{Substrate: root, DisableMergeBack: false}
	_, err := orch.RunSprout(ctx, "cleanup test")
	if err == nil {
		t.Fatal("RunSprout() error = nil, want failure before terrarium start")
	}
	if !restored {
		t.Fatal("expected host stash restore during canceled context cleanup")
	}

	status, err := runGitCommand(context.Background(), root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status after restore: %v", err)
	}
	if strings.TrimSpace(status) == "" {
		t.Fatal("expected restored dirty workspace after stash pop")
	}
}

func TestRunSproutAutoBranchesBeforeStash(t *testing.T) {
	root := t.TempDir()
	if _, err := runGitCommand(context.Background(), root, "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.email", "test@example.com"); err != nil {
		t.Fatalf("git config email failed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("git config name failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatalf("write baseline file: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "add", "README.md"); err != nil {
		t.Fatalf("git add baseline: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "commit", "-m", "baseline"); err != nil {
		t.Fatalf("git commit baseline: %v", err)
	}

	currentBranch, err := runGitCommand(context.Background(), root, "branch", "--show-current")
	if err != nil {
		t.Fatalf("read initial branch: %v", err)
	}
	if currentBranch != "main" && currentBranch != "master" {
		if _, err := runGitCommand(context.Background(), root, "branch", "-m", "main"); err != nil {
			t.Fatalf("rename branch to main: %v", err)
		}
		currentBranch = "main"
	}
	if currentBranch != "main" && currentBranch != "master" {
		t.Fatalf("initial branch = %q, want main or master", currentBranch)
	}

	chdirToTempDir(t)
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir repo root: %v", err)
	}

	originalPreflight := runSproutPreflightChecksFn
	originalStash := stashHostWorkspaceFn

	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		stashHostWorkspaceFn = originalStash
	})

	runSproutPreflightChecksFn = func(ctx context.Context) error { return nil }

	stashHostWorkspaceFn = func(ctx context.Context, repoRoot, runID string) (bool, error) {
		branch, err := runGitCommand(ctx, repoRoot, "branch", "--show-current")
		if err != nil {
			return false, err
		}
		if branch != "sprout/task-step-1" {
			t.Fatalf("branch at stash time = %q, want sprout/task-step-1", branch)
		}
		return false, errors.New("stop after branch isolation check")
	}

	_, err = (&DockerOrchestrator{
		Substrate: root,
		StepID:    "step-1",
	}).RunSprout(context.Background(), "verify auto-branching")
	if err == nil {
		t.Fatal("RunSprout() error = nil, want stop after branch isolation check")
	}

	// The run produced no commits, so its protective branch was pure residue.
	// It is reclaimed by the run that created it, and the workspace returns to
	// the branch it started on. Previously the branch was left behind forever
	// — and, never having been pushed, nothing that requires remote evidence
	// could ever clean it up.
	finalBranch, err := runGitCommand(context.Background(), root, "branch", "--show-current")
	if err != nil {
		t.Fatalf("read final branch: %v", err)
	}
	if finalBranch != currentBranch {
		t.Fatalf("final branch = %q, want the run to return to %q it started on", finalBranch, currentBranch)
	}

	branches, err := runGitCommand(context.Background(), root, "branch", "--list")
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	if strings.Contains(branches, "sprout/task-step-1") {
		t.Fatalf("the empty isolation branch was left behind:\n%s", branches)
	}
	if owned := OwnedRefsFor(root); len(owned) != 0 {
		t.Fatalf("owned references left registered after reclamation: %+v", owned)
	}
}
