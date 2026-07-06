package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

func TestRunTendrilRestoresHostStashAfterCanceledContext(t *testing.T) {
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
	originalNewAgent := newAgentFn
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
		newAgentFn = originalNewAgent
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
	newAgentFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string) (tendrilRunner, error) {
		return nil, errors.New("agent should not start")
	}

	orch := &DockerOrchestrator{Substrate: root, DisableMergeBack: false}
	_, err := orch.RunTendril(ctx, "cleanup test")
	if err == nil {
		t.Fatal("RunTendril() error = nil, want failure before terrarium start")
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