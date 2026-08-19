package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

type managedRunCapture struct {
	mu       sync.Mutex
	mounts   map[string]string
	sources  map[string]string
	branches map[string]string
	files    map[string]string
	caches   map[string]bool
}

func newManagedRunCapture() *managedRunCapture {
	return &managedRunCapture{
		mounts:   make(map[string]string),
		sources:  make(map[string]string),
		branches: make(map[string]string),
		files:    make(map[string]string),
		caches:   make(map[string]bool),
	}
}

func (capture *managedRunCapture) remember(stepID, mountPath, sourcePath, file string) error {
	branch, err := runGitCommand(context.Background(), mountPath, "branch", "--show-current")
	if err != nil {
		return err
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.mounts[stepID] = mountPath
	capture.sources[stepID] = sourcePath
	capture.branches[stepID] = strings.TrimSpace(branch)
	capture.files[mountPath] = file
	_, cacheErr := os.Stat(filepath.Join(mountPath, "vendor", "cache.txt"))
	capture.caches[stepID] = cacheErr == nil
	return nil
}

func (capture *managedRunCapture) get(stepID string) (mount, source, branch string) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.mounts[stepID], capture.sources[stepID], capture.branches[stepID]
}

func (capture *managedRunCapture) cacheVisible(stepID string) bool {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.caches[stepID]
}

type managedWritingRunner struct {
	workspace string
	file      string
	started   chan struct{}
	release   chan struct{}
	releaseOn sync.Once
}

func newManagedWritingRunner(file string) *managedWritingRunner {
	return &managedWritingRunner{
		file:    file,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (runner *managedWritingRunner) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	if runner.file != "" {
		if err := os.WriteFile(filepath.Join(runner.workspace, runner.file), []byte(runner.file+"\n"), 0o644); err != nil {
			return sproutResult{}, err
		}
	}
	close(runner.started)
	select {
	case <-runner.release:
		return sproutResult{Response: "managed run complete", WroteWorkspace: runner.file != ""}, nil
	case <-ctx.Done():
		return sproutResult{}, ctx.Err()
	}
}

func (runner *managedWritingRunner) releaseRun() {
	runner.releaseOn.Do(func() { close(runner.release) })
}

func prepareManagedRunRepository(t *testing.T) string {
	t.Helper()

	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	repository := filepath.Join(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT"), "managed")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatalf("create managed checkout: %v", err)
	}
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "managed@example.invalid"},
		{"config", "user.name", "Managed Run Test"},
	} {
		if _, err := runGitCommand(ctx, repository, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "seed.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := runGitCommand(ctx, repository, "add", "seed.txt"); err != nil {
		t.Fatalf("stage seed: %v", err)
	}
	if _, err := runGitCommand(ctx, repository, "commit", "-q", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return repository
}

func installManagedRunSeams(t *testing.T, capture *managedRunCapture, runners map[string]sproutRunner) {
	t.Helper()

	originalPreflight := runSproutPreflightChecksFn
	originalProbe := probeProviderAuthFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsure := ensureSproutImageFn
	originalStart := startTerrariumSessionFn
	originalNew := newSproutFn
	originalCollect := collectStageableFilesFn
	originalDiff := collectGitDiffFn
	originalCommit := commitTerrariumExecutionFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		probeProviderAuthFn = originalProbe
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsure
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNew
		collectStageableFilesFn = originalCollect
		collectGitDiffFn = originalDiff
		commitTerrariumExecutionFn = originalCommit
		mergeTerrariumCommitFn = originalMerge
	})

	runSproutPreflightChecksFn = func(context.Context) error { return nil }
	probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "# managed repo map\n", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(context.Context, string, string, string, bool, []string, []string, time.Duration, ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, sourcePath, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		runner, ok := runners[stepID]
		if !ok {
			return nil, errors.New("missing managed test runner for " + stepID)
		}
		file := ""
		if writing, ok := runner.(*managedWritingRunner); ok {
			writing.workspace = workspace
			file = writing.file
		}
		if err := capture.remember(stepID, workspace, sourcePath, file); err != nil {
			return nil, err
		}
		return runner, nil
	}
	collectStageableFilesFn = func(_ context.Context, mountPath string, _ ...string) ([]string, error) {
		capture.mu.Lock()
		file := capture.files[mountPath]
		capture.mu.Unlock()
		if file == "" {
			return []string{}, nil
		}
		if _, err := os.Stat(filepath.Join(mountPath, file)); err != nil {
			return []string{}, nil
		}
		return []string{file}, nil
	}
	collectGitDiffFn = func(context.Context, string) (string, error) { return "", nil }
	commitTerrariumExecutionFn = originalCommit
	mergeTerrariumCommitFn = func(context.Context, string, string) error { return nil }
}

func assertManagedBaseClean(t *testing.T, repository string) {
	t.Helper()
	branch, err := runGitCommand(context.Background(), repository, "branch", "--show-current")
	if err != nil {
		t.Fatalf("read managed base branch: %v", err)
	}
	if strings.TrimSpace(branch) != "main" {
		t.Fatalf("managed base branch = %q, want main", strings.TrimSpace(branch))
	}
	assertGitClean(t, repository)
}

func TestRunSproutManagedRunUsesIndependentWorkspaceAndBackingSource(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepID := "managed-source"
	baseCommit, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve managed base commit: %v", err)
	}
	runner := newManagedWritingRunner("sprout.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	originalCreateWorkspace := createRunWorkspaceFn
	t.Cleanup(func() { createRunWorkspaceFn = originalCreateWorkspace })
	createRunWorkspaceFn = func(ctx context.Context, sourcePath, gotStepID, startRevision string) (RunWorkspace, error) {
		if sourcePath != repository || gotStepID != stepID || strings.TrimSpace(startRevision) != strings.TrimSpace(baseCommit) {
			t.Fatalf("CreateRunWorkspace args = (%q, %q, %q), want (%q, %q, %q)", sourcePath, gotStepID, startRevision, repository, stepID, strings.TrimSpace(baseCommit))
		}
		return CreateRunWorkspace(ctx, sourcePath, gotStepID, startRevision)
	}

	stashCalls := 0
	mergeCalls := 0
	originalStash := stashHostWorkspaceFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		stashHostWorkspaceFn = originalStash
		mergeTerrariumCommitFn = originalMerge
	})
	stashHostWorkspaceFn = func(context.Context, string, string) (bool, error) {
		stashCalls++
		return false, nil
	}
	mergeTerrariumCommitFn = func(context.Context, string, string) error {
		mergeCalls++
		return nil
	}

	report, err := (&DockerOrchestrator{
		Substrate:  repository,
		StepID:     stepID,
		StatusPath: filepath.Join(repository, "tendril-status.json"),
	}).RunSprout(context.Background(), "write one file")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}

	mount, source, branch := capture.get(stepID)
	if mount == "" || mount == repository {
		t.Fatalf("mount = %q, want a run workspace distinct from %q", mount, repository)
	}
	if source != repository {
		t.Fatalf("source = %q, want persistent managed checkout %q", source, repository)
	}
	if branch != "sprout/task-"+stepID {
		t.Fatalf("run branch = %q, want sprout/task-%s", branch, stepID)
	}
	if stashCalls != 0 {
		t.Fatalf("managed run stashed its base %d time(s)", stashCalls)
	}
	if mergeCalls != 0 {
		t.Fatalf("managed run published Fruit directly to its persistent base %d time(s)", mergeCalls)
	}
	assertManagedBaseClean(t, repository)
	if _, err := os.Stat(filepath.Join(repository, "sprout.txt")); !os.IsNotExist(err) {
		t.Fatalf("Sprout file reached managed base, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, ".tendril", "genome", repositoryMapFile)); !os.IsNotExist(err) {
		t.Fatalf("repo map reached managed base, stat err = %v", err)
	}
	statusPath := filepath.Join(repository, "tendril-status.json")
	if status, err := os.ReadFile(statusPath); err != nil {
		t.Fatalf("managed run status was not written at the caller path: %v", err)
	} else if !strings.Contains(string(status), stepID) {
		t.Fatalf("managed run status missing step ID %q: %s", stepID, status)
	}
	if _, err := runGitCommand(context.Background(), repository, "show", "sprout/task-"+stepID+":sprout.txt"); err != nil {
		t.Fatalf("run Fruit branch does not contain Sprout file: %v", err)
	}
}

func TestManagedRunCopiesMycorrhizalCacheWithoutMutatingBase(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	cachePath := filepath.Join(repository, "vendor", "cache.txt")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("create cache directory: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("cached dependency\n"), 0o644); err != nil {
		t.Fatalf("write cache fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".git", "info", "exclude"), []byte("/vendor/\n"), 0o644); err != nil {
		t.Fatalf("ignore cache fixture: %v", err)
	}

	stepID := "managed-cache"
	runner := newManagedWritingRunner("cache-consumer.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	if _, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "use cache"); err != nil {
		t.Fatalf("managed cache RunSprout: %v", err)
	}
	if !capture.cacheVisible(stepID) {
		t.Fatal("managed RunWorkspace did not receive the copied Mycorrhizal cache")
	}
	if got, err := os.ReadFile(cachePath); err != nil {
		t.Fatalf("read persistent cache: %v", err)
	} else if string(got) != "cached dependency\n" {
		t.Fatalf("persistent cache changed to %q", got)
	}
	assertManagedBaseClean(t, repository)
}

func TestManagedRunWorkspacesOverlapAndCleanupIsIndependent(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	firstID := "overlap-a"
	secondID := "overlap-b"
	first := newManagedWritingRunner("a.txt")
	second := newManagedWritingRunner("b.txt")
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{firstID: first, secondID: second})

	type result struct {
		report SproutRunReport
		err    error
	}
	results := make(chan result, 2)
	for _, stepID := range []string{firstID, secondID} {
		go func(stepID string) {
			report, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "overlap")
			results <- result{report: report, err: err}
		}(stepID)
	}

	<-first.started
	<-second.started
	firstMount, firstSource, firstBranch := capture.get(firstID)
	secondMount, secondSource, secondBranch := capture.get(secondID)
	if firstMount == "" || secondMount == "" || firstMount == secondMount {
		t.Fatalf("live mounts = %q and %q, want distinct run workspaces", firstMount, secondMount)
	}
	if firstSource != repository || secondSource != repository {
		t.Fatalf("live sources = %q and %q, want managed base %q", firstSource, secondSource, repository)
	}
	if firstBranch != "sprout/task-"+firstID || secondBranch != "sprout/task-"+secondID {
		t.Fatalf("live branches = %q and %q, want step-scoped branches", firstBranch, secondBranch)
	}
	assertManagedBaseClean(t, repository)

	first.releaseRun()
	second.releaseRun()
	for range []int{0, 1} {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("overlapping RunSprout: %v", outcome.err)
		}
		if outcome.report.Outcome != SproutOutcomeComplete {
			t.Fatalf("overlapping outcome = %q, want %q", outcome.report.Outcome, SproutOutcomeComplete)
		}
	}
	if _, err := os.Stat(firstMount); !os.IsNotExist(err) {
		t.Fatalf("cleanup of run A left its workspace at %q, stat err = %v", firstMount, err)
	}
	if _, err := os.Stat(secondMount); !os.IsNotExist(err) {
		t.Fatalf("cleanup of run B left its workspace at %q, stat err = %v", secondMount, err)
	}
	if _, err := runGitCommand(context.Background(), repository, "show", "sprout/task-"+firstID+":a.txt"); err != nil {
		t.Fatalf("run A Fruit missing after run B cleanup: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repository, "show", "sprout/task-"+secondID+":b.txt"); err != nil {
		t.Fatalf("run B Fruit missing after run A cleanup: %v", err)
	}
	assertManagedBaseClean(t, repository)
}

func TestManagedRunDetachedWorkspaceLivesUntilTerminalEnding(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), `
substrates:
  managed:
    checkout:
      mode: managed
    patience:
      growth: 1s
`)
	runner := newManagedWritingRunner("")
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{"detached-managed": runner})
	terminal := make(chan struct{}, 1)

	report, err := (&DockerOrchestrator{
		Substrate:  "managed",
		StepID:     "detached-managed",
		OnTerminal: func(SproutRunReport, error) { terminal <- struct{}{} },
	}).RunSprout(context.Background(), "hold managed run")
	if err != nil {
		t.Fatalf("detached managed RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeDetached {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeDetached)
	}
	mount, source, branch := capture.get("detached-managed")
	if mount == "" || source != repository || branch != "sprout/task-detached-managed" {
		t.Fatalf("detached capture = mount %q source %q branch %q", mount, source, branch)
	}
	if _, err := os.Stat(mount); err != nil {
		t.Fatalf("detached RunWorkspace disappeared before terminal ending: %v", err)
	}
	assertManagedBaseClean(t, repository)

	runner.releaseRun()
	select {
	case <-terminal:
	case <-time.After(5 * time.Second):
		t.Fatal("detached managed run never reached terminal ending")
	}
	if _, err := os.Stat(mount); !os.IsNotExist(err) {
		t.Fatalf("detached RunWorkspace was not cleaned after terminal ending, stat err = %v", err)
	}
}

func TestManagedRunFailureCleansItsGeneratedState(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepID := "managed-preflight-failure"
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{})
	originalRepoMap := generateRepoMapFn
	t.Cleanup(func() { generateRepoMapFn = originalRepoMap })
	generateRepoMapFn = func(_ context.Context, mountPath string) (string, error) {
		path := filepath.Join(mountPath, tendrilStateDirectory, "genome", repositoryMapFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte("generated before failure\n"), 0o644); err != nil {
			return "", err
		}
		return "", errors.New("controlled repository-map failure")
	}

	if _, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "fail after allocation"); err == nil {
		t.Fatal("managed preflight failure returned nil")
	}
	if branchExists(t, repository, "sprout/task-"+stepID) {
		t.Fatal("managed preflight failure left its run branch behind")
	}
	assertManagedBaseClean(t, repository)
	if _, err := os.Stat(filepath.Join(repository, ".tendril")); !os.IsNotExist(err) {
		t.Fatalf("managed preflight failure left generated Tendril state in base, stat err = %v", err)
	}
}

func TestRunSproutRemoteManagedCheckoutUsesRunWorkspace(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	managedRoot := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	source := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "remote@example.invalid"},
		{"config", "user.name", "Remote Managed Test"},
	} {
		if _, err := runGitCommand(ctx, source, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatalf("write remote seed: %v", err)
	}
	if _, err := runGitCommand(ctx, source, "add", "remote.txt"); err != nil {
		t.Fatalf("stage remote seed: %v", err)
	}
	if _, err := runGitCommand(ctx, source, "commit", "-q", "-m", "remote seed"); err != nil {
		t.Fatalf("commit remote seed: %v", err)
	}
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), "substrates:\n  remote:\n    url: "+source+"\n    branch: main\n    checkout:\n      mode: managed\n")

	stepID := "remote-managed"
	runner := newManagedWritingRunner("")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	if _, err := (&DockerOrchestrator{Substrate: "remote", StepID: stepID, DisableMergeBack: true}).RunSprout(ctx, "read remote"); err != nil {
		t.Fatalf("remote managed RunSprout: %v", err)
	}

	mount, backing, branch := capture.get(stepID)
	wantBacking := filepath.Join(managedRoot, "remote")
	if backing != wantBacking {
		t.Fatalf("remote source = %q, want persistent managed checkout %q", backing, wantBacking)
	}
	if mount == backing {
		t.Fatalf("remote managed mount reused persistent checkout %q", mount)
	}
	if branch != "sprout/task-"+stepID {
		t.Fatalf("remote run branch = %q, want sprout/task-%s", branch, stepID)
	}
	assertManagedBaseClean(t, backing)
}

func TestRunSproutRemoteEphemeralCheckoutKeepsExistingLifecycle(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	source := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "ephemeral@example.invalid"},
		{"config", "user.name", "Ephemeral Test"},
		{"commit", "--allow-empty", "-q", "-m", "seed"},
	} {
		if _, err := runGitCommand(ctx, source, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), "substrates:\n  ephemeral:\n    url: "+source+"\n    branch: main\n    checkout:\n      mode: ephemeral\n")

	stepID := "remote-ephemeral"
	runner := newManagedWritingRunner("")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	originalCreateWorkspace := createRunWorkspaceFn
	t.Cleanup(func() { createRunWorkspaceFn = originalCreateWorkspace })
	createRunWorkspaceFn = func(context.Context, string, string, string) (RunWorkspace, error) {
		t.Fatal("ephemeral checkout was routed through RunWorkspace")
		return RunWorkspace{}, nil
	}

	if _, err := (&DockerOrchestrator{Substrate: "ephemeral", StepID: stepID, DisableMergeBack: true}).RunSprout(ctx, "read ephemeral"); err != nil {
		t.Fatalf("ephemeral RunSprout: %v", err)
	}
	mount, sourcePath, _ := capture.get(stepID)
	if mount == "" || !strings.HasPrefix(mount, os.TempDir()) {
		t.Fatalf("ephemeral mount = %q, want a temporary checkout", mount)
	}
	if sourcePath != mount {
		t.Fatalf("ephemeral source = %q, mount = %q; existing ephemeral path should remain direct", sourcePath, mount)
	}
	if isManagedCheckoutPath(mount) {
		t.Fatalf("ephemeral mount %q was classified as managed", mount)
	}
	if _, err := os.Stat(mount); !os.IsNotExist(err) {
		t.Fatalf("ephemeral checkout was not removed after the run, stat err = %v", err)
	}
}

func TestCheckoutPathStillUsesShadowWorktree(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	repository := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "path@example.invalid"},
		{"config", "user.name", "Path Checkout Test"},
		{"commit", "--allow-empty", "-q", "-m", "seed"},
	} {
		if _, err := runGitCommand(ctx, repository, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	managedRoot := os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT")
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), "substrates:\n  local:\n    path: "+repository+"\n    checkout:\n      mode: path\n      path: "+repository+"\n")

	originalPreflight := runSproutPreflightChecksFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsure := ensureSproutImageFn
	originalShadow := createShadowWorktreeFn
	originalRemove := removeShadowWorktreeFn
	originalStart := startTerrariumSessionFn
	originalNew := newSproutFn
	originalCollect := collectStageableFilesFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsure
		createShadowWorktreeFn = originalShadow
		removeShadowWorktreeFn = originalRemove
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNew
		collectStageableFilesFn = originalCollect
	})
	runSproutPreflightChecksFn = func(context.Context) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	shadowPath := filepath.Join(t.TempDir(), "shadow")
	if err := os.MkdirAll(shadowPath, 0o755); err != nil {
		t.Fatalf("mkdir shadow: %v", err)
	}
	shadowCalls := 0
	createShadowWorktreeFn = func(sourcePath, branch string) (string, error) {
		shadowCalls++
		return shadowPath, nil
	}
	removeShadowWorktreeFn = func(sourcePath, path string) {}
	var mounted string
	startTerrariumSessionFn = func(_ context.Context, _ string, _ string, mountPath string, _ bool, _ []string, _ []string, _ time.Duration, _ ...terrarium.ActivationObserver) (toolSession, error) {
		mounted = mountPath
		return &stubToolSession{}, nil
	}
	newSproutFn = func(context.Context, string, string, string, llmCaller, toolSession, *eventbus.Bus, string, string) (sproutRunner, error) {
		return &stubSproutRunner{result: sproutResult{Response: "path"}}, nil
	}
	collectStageableFilesFn = func(context.Context, string, ...string) ([]string, error) { return []string{}, nil }

	if _, err := (&DockerOrchestrator{Substrate: "local", StepID: "path-unchanged", DisableMergeBack: true}).RunSprout(ctx, "path"); err != nil {
		t.Fatalf("path RunSprout: %v", err)
	}
	if shadowCalls != 1 || mounted != shadowPath {
		t.Fatalf("path execution used shadow %q %d time(s), want %q exactly once; managed root was %q", mounted, shadowCalls, shadowPath, managedRoot)
	}
}
