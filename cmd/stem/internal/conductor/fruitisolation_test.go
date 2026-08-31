package conductor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

// prepareBareRemoteRepo creates a local bare git repository that acts as the
// remote for managed run tests. Returns (bare) path — the substrate URL points
// here directly, so `git push` from the managed checkout lands on the bare remote.
func prepareBareRemoteRepo(t *testing.T, branch string) string {
	t.Helper()
	ctx := context.Background()

	remote := t.TempDir()
	if _, err := runGitCommand(ctx, remote, "init", "--bare", "-b", branch); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	// Bootstrap the bare repo with an initial commit via a temporary clone.
	tmpClone := t.TempDir()
	for _, args := range [][]string{
		{"clone", remote, "."},
		{"config", "user.email", "bootstrap@example.invalid"},
		{"config", "user.name", "Bootstrap"},
	} {
		if _, err := runGitCommand(ctx, tmpClone, args...); err != nil {
			t.Fatalf("bootstrap git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmpClone, "seed.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	for _, args := range [][]string{
		{"add", "seed.txt"},
		{"commit", "-q", "-m", "seed"},
		{"push", "origin", branch},
	} {
		if _, err := runGitCommand(ctx, tmpClone, args...); err != nil {
			t.Fatalf("bootstrap push git %v: %v", args, err)
		}
	}
	return remote
}

// installRemoteManagedRunSeams installs the test seams needed for remote managed
// run tests. It returns the per-stepID writer map for tracking written files.
func installRemoteManagedRunSeams(
	t *testing.T,
	capture *managedRunCapture,
	runners map[string]sproutRunner,
) {
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

	runSproutPreflightChecksFn = func(_ context.Context, _ *llm.Client) error { return nil }
	probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "# repo map\n", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(context.Context, string, string, string, bool, []string, []string, time.Duration, ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, sourcePath, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		runner, ok := runners[stepID]
		if !ok {
			return nil, errors.New("missing runner for " + stepID)
		}
		if setter, ok := runner.(interface{ setWorkspace(string) }); ok {
			setter.setWorkspace(workspace)
		}
		file := ""
		if writing, ok := runner.(*managedWritingRunner); ok {
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

// resolveRemoteRefs returns a map of ref-name → commit from a bare remote repo.
func resolveRemoteRefs(t *testing.T, remote string) map[string]string {
	t.Helper()
	ctx := context.Background()
	out, err := runGitCommand(ctx, remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	if err != nil {
		t.Fatalf("for-each-ref %s: %v", remote, err)
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 {
			refs[strings.TrimPrefix(parts[0], "refs/heads/")] = parts[1]
		}
	}
	return refs
}

// remoteRef returns the commit for a single remote ref, or empty string if absent.
func remoteRef(t *testing.T, remote, ref string) string {
	t.Helper()
	ctx := context.Background()
	out, err := runGitCommand(ctx, remote, "rev-parse", "--verify", "--quiet", "refs/heads/"+ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// remoteSourceCommit returns the commit currently at refs/heads/<branch> in the remote.
func remoteSourceCommit(t *testing.T, remote, branch string) string {
	t.Helper()
	commit := remoteRef(t, remote, branch)
	if commit == "" {
		t.Fatalf("remote has no ref for branch %q", branch)
	}
	return commit
}

// TestRemoteManagedPublicationTargetsRunBranchNotSourceBranch proves that a
// managed remote run pushes Fruit to sprout/task-<stepID>, not to the configured
// source branch (main).
func TestRemoteManagedPublicationTargetsRunBranchNotSourceBranch(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	remote := prepareBareRemoteRepo(t, "main")
	initialMainCommit := remoteSourceCommit(t, remote, "main")

	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  repo:\n    url: "+remote+"\n    branch: main\n    identity:\n      name: Fruit Test Bot\n      email: test@example.invalid\n    checkout:\n      mode: managed\n")

	stepID := "pub-main"
	runner := newManagedWritingRunner("feature.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installRemoteManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	report, err := (&DockerOrchestrator{
		Substrate: "repo",
		StepID:    stepID,
		EventBus:  bus,
	}).RunSprout(context.Background(), "add feature")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}

	// Fruit identity must be present on the report.
	wantBranch := "sprout/task-" + stepID
	if report.FruitBranch != wantBranch {
		t.Errorf("FruitBranch = %q, want %q", report.FruitBranch, wantBranch)
	}
	if report.FruitCommit == "" {
		t.Error("FruitCommit is empty, want a commit hash")
	}

	// Remote must have the Fruit branch, not an updated main.
	fruitCommit := remoteRef(t, remote, wantBranch)
	if fruitCommit == "" {
		t.Errorf("remote has no ref %q; refs: %v", wantBranch, resolveRemoteRefs(t, remote))
	}
	if fruitCommit != report.FruitCommit {
		t.Errorf("remote Fruit commit = %q, report FruitCommit = %q; want match", fruitCommit, report.FruitCommit)
	}

	// Source branch (main) must be unchanged.
	afterMainCommit := remoteSourceCommit(t, remote, "main")
	if afterMainCommit != initialMainCommit {
		t.Errorf("remote main advanced from %q to %q; must be unchanged", initialMainCommit, afterMainCommit)
	}

	// Terminal event must carry Fruit identity.
	matured := filterEvents(*events, eventbus.EventSproutMatured)
	if len(matured) != 1 {
		t.Fatalf("matured events = %d, want 1", len(matured))
	}
	if got := matured[0].Data["fruitBranch"]; got != wantBranch {
		t.Errorf("terminal event fruitBranch = %v, want %q", got, wantBranch)
	}
	if got := matured[0].Data["fruitCommit"]; got != report.FruitCommit {
		t.Errorf("terminal event fruitCommit = %v, want %q", got, report.FruitCommit)
	}
}

// TestRemoteManagedPublicationNonProtectedSourceBranchTargetsRunBranch verifies
// that even when the configured starting branch is a non-protected feature branch,
// managed publication still targets sprout/task-<stepID> — not the feature branch.
func TestRemoteManagedPublicationNonProtectedSourceBranchTargetsRunBranch(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	// Use a non-protected feature branch as the starting branch.
	remote := prepareBareRemoteRepo(t, "feature")
	initialFeatureCommit := remoteSourceCommit(t, remote, "feature")

	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  repo:\n    url: "+remote+"\n    branch: feature\n    identity:\n      name: Fruit Test Bot\n      email: test@example.invalid\n    checkout:\n      mode: managed\n")

	stepID := "pub-feature"
	runner := newManagedWritingRunner("work.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installRemoteManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	report, err := (&DockerOrchestrator{
		Substrate: "repo",
		StepID:    stepID,
	}).RunSprout(context.Background(), "do work")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}

	wantBranch := "sprout/task-" + stepID

	// Fruit must land on the run branch, not on feature.
	fruitCommit := remoteRef(t, remote, wantBranch)
	if fruitCommit == "" {
		t.Errorf("remote has no Fruit branch %q; refs: %v", wantBranch, resolveRemoteRefs(t, remote))
	}

	// Configured source branch (feature) must be unchanged.
	afterFeatureCommit := remoteSourceCommit(t, remote, "feature")
	if afterFeatureCommit != initialFeatureCommit {
		t.Errorf("remote feature branch advanced from %q to %q; must be unchanged", initialFeatureCommit, afterFeatureCommit)
	}
}

// TestRemoteManagedPublicationAllowDefaultBranchCommitDoesNotBypassIsolation
// proves that allowDefaultBranchCommit=true cannot authorize a managed autonomous
// run to collapse its Fruit onto the configured source branch or main.
func TestRemoteManagedPublicationAllowDefaultBranchCommitDoesNotBypassIsolation(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	remote := prepareBareRemoteRepo(t, "main")
	initialMainCommit := remoteSourceCommit(t, remote, "main")

	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  repo:\n    url: "+remote+"\n    branch: main\n    identity:\n      name: Fruit Test Bot\n      email: test@example.invalid\n    checkout:\n      mode: managed\n      allowDefaultBranchCommit: true\n")

	stepID := "pub-allow-default"
	runner := newManagedWritingRunner("output.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installRemoteManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	report, err := (&DockerOrchestrator{
		Substrate: "repo",
		StepID:    stepID,
	}).RunSprout(context.Background(), "work with allow-default")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}

	wantBranch := "sprout/task-" + stepID

	// Even with allowDefaultBranchCommit, Fruit lands on run branch.
	fruitCommit := remoteRef(t, remote, wantBranch)
	if fruitCommit == "" {
		t.Errorf("remote has no Fruit branch %q despite allowDefaultBranchCommit=true; refs: %v",
			wantBranch, resolveRemoteRefs(t, remote))
	}

	// Main must be unchanged.
	afterMain := remoteSourceCommit(t, remote, "main")
	if afterMain != initialMainCommit {
		t.Errorf("remote main advanced from %q to %q; allowDefaultBranchCommit must not bypass managed isolation",
			initialMainCommit, afterMain)
	}
}

// TestConcurrentRemoteManagedRunsProduceDistinctFruit verifies that two concurrent
// managed remote runs starting from the same revision produce separate, independent
// Fruit branches without interfering with each other or advancing the source branch.
func TestConcurrentRemoteManagedRunsProduceDistinctFruit(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	remote := prepareBareRemoteRepo(t, "main")
	initialMainCommit := remoteSourceCommit(t, remote, "main")

	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  repo:\n    url: "+remote+"\n    branch: main\n    identity:\n      name: Fruit Test Bot\n      email: test@example.invalid\n    checkout:\n      mode: managed\n")

	stepA := "concurrent-a"
	stepB := "concurrent-b"

	// Both runs modify the same source file with different content, proving
	// that overlapping edits to the same file do not corrupt each other.
	runnerA := newManagedWritingRunner("shared.txt")
	runnerB := newManagedWritingRunner("shared.txt")
	capture := newManagedRunCapture()

	installRemoteManagedRunSeams(t, capture,
		map[string]sproutRunner{stepA: runnerA, stepB: runnerB})

	type result struct {
		report SproutRunReport
		err    error
	}
	results := make(chan result, 2)

	for _, stepID := range []string{stepA, stepB} {
		go func(stepID string) {
			report, err := (&DockerOrchestrator{
				Substrate: "repo",
				StepID:    stepID,
			}).RunSprout(context.Background(), "edit shared file")
			results <- result{report: report, err: err}
		}(stepID)
	}

	// Wait for both runs to start before releasing either.
	<-runnerA.started
	<-runnerB.started

	// Verify isolation during overlap: each run has its own worktree and branch.
	mountA, _, branchA := capture.get(stepA)
	mountB, _, branchB := capture.get(stepB)
	if mountA == "" || mountB == "" {
		t.Fatalf("concurrent mounts not captured: A=%q B=%q", mountA, mountB)
	}
	if mountA == mountB {
		t.Fatalf("concurrent runs share the same worktree %q", mountA)
	}
	if branchA != "sprout/task-"+stepA {
		t.Fatalf("run A branch = %q, want sprout/task-%s", branchA, stepA)
	}
	if branchB != "sprout/task-"+stepB {
		t.Fatalf("run B branch = %q, want sprout/task-%s", branchB, stepB)
	}

	// Prove file isolation: write different content to the same file in each workspace.
	if err := os.WriteFile(filepath.Join(mountA, "shared.txt"), []byte("run-a-content\n"), 0o644); err != nil {
		t.Fatalf("write A shared file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountB, "shared.txt"), []byte("run-b-content\n"), 0o644); err != nil {
		t.Fatalf("write B shared file: %v", err)
	}
	// Verify neither sees the other's filesystem change during execution.
	if content, _ := os.ReadFile(filepath.Join(mountA, "shared.txt")); string(content) != "run-a-content\n" {
		t.Fatalf("run A workspace contains run B's content %q", content)
	}
	if content, _ := os.ReadFile(filepath.Join(mountB, "shared.txt")); string(content) != "run-b-content\n" {
		t.Fatalf("run B workspace contains run A's content %q", content)
	}

	runnerA.releaseRun()
	runnerB.releaseRun()

	var reportA, reportB SproutRunReport
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent run error: %v", r.err)
		}
		if r.report.Outcome != SproutOutcomeComplete {
			t.Fatalf("concurrent outcome = %q, want %q", r.report.Outcome, SproutOutcomeComplete)
		}
		if r.report.FruitBranch == "sprout/task-"+stepA {
			reportA = r.report
		} else {
			reportB = r.report
		}
	}

	// Both Fruit branches exist on the remote.
	fruitA := remoteRef(t, remote, "sprout/task-"+stepA)
	fruitB := remoteRef(t, remote, "sprout/task-"+stepB)
	if fruitA == "" {
		t.Errorf("remote missing Fruit branch sprout/task-%s; refs: %v", stepA, resolveRemoteRefs(t, remote))
	}
	if fruitB == "" {
		t.Errorf("remote missing Fruit branch sprout/task-%s; refs: %v", stepB, resolveRemoteRefs(t, remote))
	}

	// Fruit commits are distinct descendants of the common base.
	if fruitA != "" && fruitB != "" && fruitA == fruitB {
		t.Errorf("concurrent runs produced the same Fruit commit %q; want competing descendants", fruitA)
	}

	// Report Fruit identity matches remote.
	if reportA.FruitCommit != "" && fruitA != "" && reportA.FruitCommit != fruitA {
		t.Errorf("run A FruitCommit = %q, remote = %q; want match", reportA.FruitCommit, fruitA)
	}
	if reportB.FruitCommit != "" && fruitB != "" && reportB.FruitCommit != fruitB {
		t.Errorf("run B FruitCommit = %q, remote = %q; want match", reportB.FruitCommit, fruitB)
	}

	// Source branch (main) must be unchanged.
	afterMain := remoteSourceCommit(t, remote, "main")
	if afterMain != initialMainCommit {
		t.Errorf("remote main changed from %q to %q; must be unchanged", initialMainCommit, afterMain)
	}
}

// TestLocalManagedFruitSurvivesCleanupSourceUnchanged proves that local managed
// Fruit is retained on its run branch after cleanup, and that source/main is
// untouched. This is a regression guard for the Slice 2 behaviour.
func TestLocalManagedFruitSurvivesCleanupSourceUnchanged(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	baseCommit, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit: %v", err)
	}
	stepID := "fruit-survives"
	runner := newManagedWritingRunner("work.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	report, err := (&DockerOrchestrator{
		Substrate: repository,
		StepID:    stepID,
	}).RunSprout(context.Background(), "do work")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}

	// Fruit identity is explicit on the report.
	wantBranch := "sprout/task-" + stepID
	if report.FruitBranch != wantBranch {
		t.Errorf("FruitBranch = %q, want %q", report.FruitBranch, wantBranch)
	}
	if report.FruitCommit == "" {
		t.Error("FruitCommit is empty, want a commit hash")
	}

	// Source/main is unchanged.
	assertManagedBaseClean(t, repository)
	afterCommit, _ := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if strings.TrimSpace(afterCommit) != strings.TrimSpace(baseCommit) {
		t.Errorf("managed base HEAD moved from %q to %q; must be unchanged", strings.TrimSpace(baseCommit), strings.TrimSpace(afterCommit))
	}

	// Fruit branch exists in the backing repository with the committed work.
	if _, err := runGitCommand(context.Background(), repository, "show", wantBranch+":work.txt"); err != nil {
		t.Errorf("Fruit branch %q does not contain work.txt: %v", wantBranch, err)
	}

	// work.txt must NOT appear in the managed base (main/configured branch).
	if _, err := os.Stat(filepath.Join(repository, "work.txt")); !os.IsNotExist(err) {
		t.Errorf("Sprout file work.txt appeared in managed base; should stay on Fruit branch only")
	}
}

// managedErrorRunner is a sproutRunner that signals started, blocks until
// released, then returns a deliberate execution error without writing any
// files. It is used to prove that a genuinely failing overlapping run cannot
// damage a concurrently successful run's Fruit branch.
type managedErrorRunner struct {
	runErr  error
	started chan struct{}
	release chan struct{}
}

func newManagedErrorRunner(err error) *managedErrorRunner {
	return &managedErrorRunner{
		runErr:  err,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (r *managedErrorRunner) releaseRun() {
	select {
	case <-r.release:
	default:
		close(r.release)
	}
}

func (r *managedErrorRunner) Run(ctx context.Context, _ string) (sproutResult, error) {
	close(r.started)
	select {
	case <-r.release:
		return sproutResult{}, r.runErr
	case <-ctx.Done():
		return sproutResult{}, ctx.Err()
	}
}

// TestFailedManagedRunDoesNotDamageSuccessfulRunFruit proves that a genuinely
// failing overlapping run — one that returns a deliberate execution error —
// cannot delete, move, or overwrite a successful concurrent run's Fruit.
// Both runs are live in distinct RunWorkspaces simultaneously; only A succeeds.
func TestFailedManagedRunDoesNotDamageSuccessfulRunFruit(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	successID := "success-fruit"
	failID := "fail-run"

	deliberateErr := errors.New("deliberate execution failure for fail-run")

	successRunner := newManagedWritingRunner("success.txt")
	// failRunner returns a deliberate error and never writes any files.
	failRunner := newManagedErrorRunner(deliberateErr)
	capture := newManagedRunCapture()

	installManagedRunSeams(t, capture,
		map[string]sproutRunner{successID: successRunner, failID: failRunner})

	type result struct {
		stepID string
		report SproutRunReport
		err    error
	}
	results := make(chan result, 2)

	// Launch both runs concurrently.
	for _, stepID := range []string{successID, failID} {
		go func(stepID string) {
			report, err := (&DockerOrchestrator{
				Substrate: repository,
				StepID:    stepID,
			}).RunSprout(context.Background(), "work")
			results <- result{stepID: stepID, report: report, err: err}
		}(stepID)
	}

	// Wait for both to start — proving both RunWorkspaces are live simultaneously
	// before either is released.
	<-successRunner.started
	<-failRunner.started

	// Verify both runs have distinct live worktrees while they overlap.
	successMount, _, _ := capture.get(successID)
	failMount, _, _ := capture.get(failID)
	if successMount == "" || failMount == "" {
		t.Fatalf("mounts not captured: success=%q fail=%q", successMount, failMount)
	}
	if successMount == failMount {
		t.Fatalf("overlapping runs share the same worktree %q; must be isolated", successMount)
	}

	// Release success first, then fail — order does not affect correctness.
	successRunner.releaseRun()
	failRunner.releaseRun()

	// Collect both results. Capture the success report for post-loop assertions.
	wantFruitBranch := "sprout/task-" + successID
	var successReport SproutRunReport
	for i := 0; i < 2; i++ {
		r := <-results
		switch r.stepID {
		case successID:
			// Success run: must complete with reviewable Fruit identity.
			if r.err != nil {
				t.Errorf("success run error = %v; want nil", r.err)
			}
			if r.report.Outcome != SproutOutcomeComplete {
				t.Errorf("success run outcome = %q, want %q", r.report.Outcome, SproutOutcomeComplete)
			}
			if r.report.FruitBranch != wantFruitBranch {
				t.Errorf("success run FruitBranch = %q, want %q", r.report.FruitBranch, wantFruitBranch)
			}
			if r.report.FruitCommit == "" {
				t.Errorf("success run FruitCommit is empty; want a non-empty commit hash")
			}
			successReport = r.report
		case failID:
			// Fail run: must carry no fabricated Fruit identity (it never
			// committed any Fruit before the error) and must report failed.
			if r.report.Outcome != SproutOutcomeFailed {
				t.Errorf("fail run outcome = %q, want %q", r.report.Outcome, SproutOutcomeFailed)
			}
			if r.report.FruitBranch != "" {
				t.Errorf("fail run FruitBranch = %q, want empty (no committed Fruit before failure)", r.report.FruitBranch)
			}
			if r.report.FruitCommit != "" {
				t.Errorf("fail run FruitCommit = %q, want empty (no committed Fruit before failure)", r.report.FruitCommit)
			}
			// The error must wrap deliberateErr.
			if r.err == nil || !errors.Is(r.err, deliberateErr) {
				t.Errorf("fail run error = %v; want errors.Is(err, deliberateErr) to be true", r.err)
			}
		}
	}

	// Success Fruit branch must still exist in the repository.
	if !branchExists(t, repository, wantFruitBranch) {
		t.Errorf("success Fruit branch %q was lost; failed overlapping run cleanup must not delete another run's branch", wantFruitBranch)
	}

	// The local Fruit branch must resolve to exactly the commit the success run
	// reported — proving the branch pointer was not moved by the overlapping
	// failed run's teardown.
	if successReport.FruitCommit != "" {
		got, err := runGitCommand(context.Background(), repository, "rev-parse", wantFruitBranch)
		if err != nil {
			t.Errorf("resolve local Fruit branch %q: %v", wantFruitBranch, err)
		} else if got != successReport.FruitCommit {
			t.Errorf("local Fruit branch %q resolves to %q, want %q (successReport.FruitCommit)", wantFruitBranch, got, successReport.FruitCommit)
		}
	}

	assertManagedBaseClean(t, repository)
}

// TestConcurrentManagedRunsWithSameSourceEditsProduceCompetingFruit verifies the
// core isolation scenario: two managed runs each edit the SAME EXISTING TRACKED
// file (seed.txt) from the same base commit, writing different content. Both
// complete successfully with distinct competing commits, each a direct descendant
// of the common base, each containing only its own version of the file.
func TestConcurrentManagedRunsWithSameSourceEditsProduceCompetingFruit(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	baseCommit, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit: %v", err)
	}
	baseCommit = strings.TrimSpace(baseCommit)

	// Confirm seed.txt exists and is tracked at the baseline.
	if _, err := runGitCommand(context.Background(), repository, "show", "HEAD:seed.txt"); err != nil {
		t.Fatalf("baseline seed.txt missing from HEAD: %v", err)
	}

	stepA := "compete-a"
	stepB := "compete-b"

	// Both runs modify the same existing tracked file (seed.txt) with distinct content.
	runnerA := newManagedWritingRunner("seed.txt")
	runnerB := newManagedWritingRunner("seed.txt")
	capture := newManagedRunCapture()

	installManagedRunSeams(t, capture,
		map[string]sproutRunner{stepA: runnerA, stepB: runnerB})

	type result struct {
		report SproutRunReport
		err    error
	}
	results := make(chan result, 2)

	for _, stepID := range []string{stepA, stepB} {
		go func(stepID string) {
			report, err := (&DockerOrchestrator{
				Substrate: repository,
				StepID:    stepID,
			}).RunSprout(context.Background(), "edit existing tracked file")
			results <- result{report: report, err: err}
		}(stepID)
	}

	<-runnerA.started
	<-runnerB.started

	mountA, _, _ := capture.get(stepA)
	mountB, _, _ := capture.get(stepB)
	if mountA == mountB {
		t.Fatalf("runs share the same worktree: %q", mountA)
	}

	// Write competing content to the same tracked file in each isolated workspace.
	const contentA = "version-a\n"
	const contentB = "version-b\n"
	if err := os.WriteFile(filepath.Join(mountA, "seed.txt"), []byte(contentA), 0o644); err != nil {
		t.Fatalf("write A seed.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountB, "seed.txt"), []byte(contentB), 0o644); err != nil {
		t.Fatalf("write B seed.txt: %v", err)
	}

	runnerA.releaseRun()
	runnerB.releaseRun()

	var (
		reportA SproutRunReport
		reportB SproutRunReport
	)
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("competing run error: %v", r.err)
		}
		if r.report.Outcome != SproutOutcomeComplete {
			t.Fatalf("competing outcome = %q, want %q", r.report.Outcome, SproutOutcomeComplete)
		}
		switch r.report.FruitBranch {
		case "sprout/task-" + stepA:
			reportA = r.report
		case "sprout/task-" + stepB:
			reportB = r.report
		}
	}

	// Both Fruit branches must exist.
	ctx := context.Background()
	if !branchExists(t, repository, "sprout/task-"+stepA) {
		t.Errorf("Fruit branch for run A %q does not exist", "sprout/task-"+stepA)
	}
	if !branchExists(t, repository, "sprout/task-"+stepB) {
		t.Errorf("Fruit branch for run B %q does not exist", "sprout/task-"+stepB)
	}

	// Each Fruit commit must be a DIRECT descendant of the common base — its
	// single parent must be exactly baseCommit. merge-base --is-ancestor would
	// accept any ancestor; rev-parse HASH^ checks the literal parent pointer.
	if reportA.FruitCommit != "" {
		parentA, err := runGitCommand(ctx, repository, "rev-parse", reportA.FruitCommit+"^")
		if err != nil {
			t.Errorf("resolve parent of run A Fruit commit %q: %v", reportA.FruitCommit, err)
		} else if parentA != baseCommit {
			t.Errorf("run A Fruit commit %q parent = %q, want baseCommit %q (must be a direct descendant)",
				reportA.FruitCommit, parentA, baseCommit)
		}
	}
	if reportB.FruitCommit != "" {
		parentB, err := runGitCommand(ctx, repository, "rev-parse", reportB.FruitCommit+"^")
		if err != nil {
			t.Errorf("resolve parent of run B Fruit commit %q: %v", reportB.FruitCommit, err)
		} else if parentB != baseCommit {
			t.Errorf("run B Fruit commit %q parent = %q, want baseCommit %q (must be a direct descendant)",
				reportB.FruitCommit, parentB, baseCommit)
		}
	}

	// Each Fruit branch must contain only its own version of seed.txt.
	wantA := strings.TrimSpace(contentA)
	wantB := strings.TrimSpace(contentB)
	if reportA.FruitCommit != "" {
		got, err := runGitCommand(ctx, repository, "show", reportA.FruitCommit+":seed.txt")
		if err != nil {
			t.Errorf("show run A seed.txt: %v", err)
		} else if got != wantA {
			t.Errorf("run A Fruit seed.txt = %q, want %q", got, wantA)
		}
		// Must not contain run B's version.
		if strings.Contains(got, wantB) {
			t.Errorf("run A Fruit seed.txt contains run B's content %q", wantB)
		}
	}
	if reportB.FruitCommit != "" {
		got, err := runGitCommand(ctx, repository, "show", reportB.FruitCommit+":seed.txt")
		if err != nil {
			t.Errorf("show run B seed.txt: %v", err)
		} else if got != wantB {
			t.Errorf("run B Fruit seed.txt = %q, want %q", got, wantB)
		}
		// Must not contain run A's version.
		if strings.Contains(got, wantA) {
			t.Errorf("run B Fruit seed.txt contains run A's content %q", wantA)
		}
	}

	// Commits must be distinct (competing descendants, not the same object).
	if reportA.FruitCommit != "" && reportB.FruitCommit != "" && reportA.FruitCommit == reportB.FruitCommit {
		t.Errorf("both runs produced the same Fruit commit %q; want competing independent descendants", reportA.FruitCommit)
	}

	// Source branch (main) must remain at the original commit.
	afterMain, _ := runGitCommand(ctx, repository, "rev-parse", "HEAD")
	if strings.TrimSpace(afterMain) != baseCommit {
		t.Errorf("managed base HEAD moved from %q to %q; must be unchanged", baseCommit, strings.TrimSpace(afterMain))
	}
	assertManagedBaseClean(t, repository)
}

// TestPublicationFailureDoesNotDamageOtherRunFruit proves that when one run's
// remote push fails with a deterministic injected error:
//   - the failing run returns an error satisfying errors.Is(err, pushFailErr)
//   - its terminal outcome is SproutOutcomeFailed
//   - its local FruitBranch and FruitCommit are retained (the commit was already
//     created; publication failure does not erase committed work)
//   - the failing run's Fruit branch was not pushed to the remote
//   - the previously successful run's remote Fruit branch and commit are unchanged
//   - remote main/configured source remains at the initial commit
//   - the withered terminal event for the failing run carries its local Fruit identity
func TestPublicationFailureDoesNotDamageOtherRunFruit(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	remote := prepareBareRemoteRepo(t, "main")
	initialMain := remoteSourceCommit(t, remote, "main")

	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  repo:\n    url: "+remote+"\n    branch: main\n    identity:\n      name: Fruit Test Bot\n      email: test@example.invalid\n    checkout:\n      mode: managed\n")

	stepGood := "pub-good"
	stepFail := "pub-fail"
	pushFailErr := errors.New("remote rejected push for pub-fail")

	runnerGood := newManagedWritingRunner("good.txt")
	runnerFail := newManagedWritingRunner("fail.txt")
	// Both runners are pre-released. Install seams once for both so that
	// collectStageableFilesFn finds each run's file from the same shared map.
	runnerGood.releaseRun()
	runnerFail.releaseRun()
	capture := newManagedRunCapture()
	installRemoteManagedRunSeams(t, capture,
		map[string]sproutRunner{stepGood: runnerGood, stepFail: runnerFail})

	// Track whether the push path was actually reached for the fail step.
	var pushCalledForFail bool

	// Intercept push: succeed for the good run, inject pushFailErr for the fail run.
	origPush := pushTerrariumCommitFn
	t.Cleanup(func() { pushTerrariumCommitFn = origPush })
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
		if stepID == stepFail {
			pushCalledForFail = true
			return pushFailErr
		}
		return origPush(ctx, mountPath, branch, cred, allowDefaultBranchCommit, stepID)
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	// Run the good run first (complete successfully with pushed Fruit).
	// SubstrateURL is set so that the plan-resolution step always treats this
	// as a remote-clone run, even when the managed checkout already exists on
	// disk from a previous run in the same test (which would otherwise set
	// remoteClone=false and bypass the push path).
	reportGood, err := (&DockerOrchestrator{
		Substrate:    "repo",
		SubstrateURL: remote,
		StepID:       stepGood,
		EventBus:     bus,
	}).RunSprout(context.Background(), "good work")
	if err != nil {
		t.Fatalf("good RunSprout: %v", err)
	}
	if reportGood.Outcome != SproutOutcomeComplete {
		t.Fatalf("good outcome = %q, want %q", reportGood.Outcome, SproutOutcomeComplete)
	}
	goodFruitCommit := remoteRef(t, remote, "sprout/task-"+stepGood)
	if goodFruitCommit == "" {
		t.Fatalf("good Fruit not pushed; refs: %v", resolveRemoteRefs(t, remote))
	}

	// Run the fail run: its push is intercepted with pushFailErr.
	// SubstrateURL forces remoteClone=true even on the persistent checkout.
	reportFail, failErr := (&DockerOrchestrator{
		Substrate:    "repo",
		SubstrateURL: remote,
		StepID:       stepFail,
		EventBus:     bus,
	}).RunSprout(context.Background(), "fail work")

	// The push path must have been reached for the fail step.
	if !pushCalledForFail {
		t.Fatal("push was not called for the fail step; the publication path was not exercised")
	}

	// The fail run must return an error wrapping pushFailErr.
	if failErr == nil {
		t.Fatalf("fail RunSprout returned nil error; want an error wrapping pushFailErr")
	}
	if !errors.Is(failErr, pushFailErr) {
		t.Errorf("fail RunSprout error = %v; want errors.Is(err, pushFailErr) to be true", failErr)
	}

	// The failing run's terminal outcome must be SproutOutcomeFailed.
	if reportFail.Outcome != SproutOutcomeFailed {
		t.Errorf("fail run outcome = %q, want %q", reportFail.Outcome, SproutOutcomeFailed)
	}

	// The failing run must retain its LOCAL Fruit identity: the commit was
	// already created before the push was attempted, so publication failure
	// must not erase the branch/commit fields.
	wantFailBranch := "sprout/task-" + stepFail
	if reportFail.FruitBranch != wantFailBranch {
		t.Errorf("fail run FruitBranch = %q, want %q (local committed Fruit must be retained)", reportFail.FruitBranch, wantFailBranch)
	}
	if reportFail.FruitCommit == "" {
		t.Errorf("fail run FruitCommit is empty; local committed Fruit identity must be retained despite push failure")
	}

	// Prove the Fruit commit is physically present in the persistent managed
	// checkout. The push failed so the remote has no copy, but the local branch
	// must still point to exactly reportFail.FruitCommit.
	{
		_, failSourcePath, _ := capture.get(stepFail)
		if failSourcePath == "" {
			t.Errorf("could not resolve sourcePath for stepFail from capture; cannot verify local Fruit ref")
		} else {
			localRef, refErr := runGitCommand(context.Background(), failSourcePath, "rev-parse", "refs/heads/"+wantFailBranch)
			if refErr != nil {
				t.Errorf("local branch %q not found in managed checkout %q after push failure: %v",
					wantFailBranch, failSourcePath, refErr)
			} else if localRef != reportFail.FruitCommit {
				t.Errorf("local branch %q resolves to %q, want %q (reportFail.FruitCommit)",
					wantFailBranch, localRef, reportFail.FruitCommit)
			}
		}
	}

	// The failing run's Fruit branch must NOT appear on the remote.
	if got := remoteRef(t, remote, wantFailBranch); got != "" {
		t.Errorf("fail run Fruit branch %q appears on remote at %q; push failed so it must be absent", wantFailBranch, got)
	}

	// The withered terminal event for the failing run must carry its local
	// Fruit identity so consumers can locate the locally-committed work.
	withered := filterEvents(*events, eventbus.EventSproutWithered)
	var failWithered *eventbus.Event
	for i := range withered {
		if withered[i].Data["stepId"] == stepFail {
			copy := withered[i]
			failWithered = &copy
			break
		}
	}
	if failWithered == nil {
		t.Fatalf("no withered event for stepId %q; fail run must publish a withered terminal event", stepFail)
	}
	if got := failWithered.Data["fruitBranch"]; got != wantFailBranch {
		t.Errorf("withered event fruitBranch = %v, want %q", got, wantFailBranch)
	}
	if got := failWithered.Data["fruitCommit"]; got != reportFail.FruitCommit {
		t.Errorf("withered event fruitCommit = %v, want %q", got, reportFail.FruitCommit)
	}

	// Good run's Fruit must still be intact on the remote.
	afterGoodFruit := remoteRef(t, remote, "sprout/task-"+stepGood)
	if afterGoodFruit != goodFruitCommit {
		t.Errorf("good Fruit changed from %q to %q after fail run; must be untouched", goodFruitCommit, afterGoodFruit)
	}

	// main must still be at initial commit.
	afterMain := remoteSourceCommit(t, remote, "main")
	if afterMain != initialMain {
		t.Errorf("remote main changed from %q to %q; must be unchanged", initialMain, afterMain)
	}
}

// TestFruitIdentityNoReviewableFruitIsEmpty proves that when a managed run
// produces no reviewable Fruit (no changes), FruitBranch and FruitCommit are
// empty — no fabricated identity.
func TestFruitIdentityNoReviewableFruitIsEmpty(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepID := "no-fruit"

	// A runner that writes nothing (WroteWorkspace=false).
	runner := &stubSproutRunner{result: sproutResult{Response: "investigated", WroteWorkspace: false}}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	report, err := (&DockerOrchestrator{
		Substrate: repository,
		StepID:    stepID,
	}).RunSprout(context.Background(), "investigate only")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}

	// No Fruit identity for a no-changes run.
	if report.FruitBranch != "" {
		t.Errorf("FruitBranch = %q, want empty (no reviewable Fruit)", report.FruitBranch)
	}
	if report.FruitCommit != "" {
		t.Errorf("FruitCommit = %q, want empty (no reviewable Fruit)", report.FruitCommit)
	}
}

// TestDetachedManagedRunReportsCorrectFruitIdentityOnTerminal proves that a
// detached managed run (growth budget spent before completion) reports the
// correct Fruit identity in its terminal event when it eventually finishes.
func TestDetachedManagedRunReportsCorrectFruitIdentityOnTerminal(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), fmt.Sprintf(`
substrates:
  managed:
    path: %s
    checkout:
      mode: managed
    patience:
      growth: 50ms
`, repository))

	stepID := "detached-fruit"
	runner := newManagedWritingRunner("detached.txt")
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	terminal := make(chan SproutRunReport, 1)
	bus := eventbus.New()
	bus.Subscribe(eventbus.EventSproutMatured, func(e eventbus.Event) {
		if e.Data["stepId"] == stepID {
			// Extract Fruit identity from the event.
			terminal <- SproutRunReport{
				FruitBranch: func() string {
					if v, ok := e.Data["fruitBranch"].(string); ok {
						return v
					}
					return ""
				}(),
				FruitCommit: func() string {
					if v, ok := e.Data["fruitCommit"].(string); ok {
						return v
					}
					return ""
				}(),
			}
		}
	})

	report, err := (&DockerOrchestrator{
		Substrate: "managed",
		StepID:    stepID,
		EventBus:  bus,
		OnTerminal: func(r SproutRunReport, _ error) {
			// OnTerminal is called for detached runs; capture the event instead.
		},
	}).RunSprout(context.Background(), "detached work")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeDetached {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeDetached)
	}

	runner.releaseRun()

	// Wait for the detached terminal event.
	select {
	case terminalReport := <-terminal:
		wantBranch := "sprout/task-" + stepID
		if terminalReport.FruitBranch != wantBranch {
			t.Errorf("detached terminal fruitBranch = %q, want %q", terminalReport.FruitBranch, wantBranch)
		}
		if terminalReport.FruitCommit == "" {
			t.Error("detached terminal fruitCommit is empty, want a commit hash")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detached managed run never produced a terminal matured event")
	}
}

// TestEphemeralPublicationSemanticsUnchanged verifies that ephemeral (non-managed)
// remote runs still use the configured source branch as the push target, not a
// sprout/task-* branch. The existing legacy push behaviour must be unchanged.
func TestEphemeralPublicationSemanticsUnchanged(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	// Track what branch was passed to the push function.
	var capturedPushBranch string
	origPush := pushTerrariumCommitFn
	t.Cleanup(func() { pushTerrariumCommitFn = origPush })
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
		capturedPushBranch = branch
		return nil
	}

	ephemeralRemote := prepareBareRemoteRepo(t, "feat")
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  ephemeral:\n    url: "+ephemeralRemote+"\n    branch: feat\n    identity:\n      name: Ephemeral Test Bot\n      email: test@example.invalid\n    checkout:\n      mode: ephemeral\n")

	stepID := "ephemeral-push"
	runner := newManagedWritingRunner("work.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()

	// For ephemeral we use the non-managed seams (no managed workspace allocation).
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
	runSproutPreflightChecksFn = func(_ context.Context, _ *llm.Client) error { return nil }
	probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(context.Context, string, string, string, bool, []string, []string, time.Duration, ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	var ephemeralMount string
	newSproutFn = func(ctx context.Context, workspace, sourcePath, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, sID, sessionID string) (sproutRunner, error) {
		ephemeralMount = workspace
		_ = capture.remember(stepID, workspace, sourcePath, runner.file)
		runner.setWorkspace(workspace)
		return runner, nil
	}
	collectStageableFilesFn = func(_ context.Context, mountPath string, _ ...string) ([]string, error) {
		if _, err := os.Stat(filepath.Join(mountPath, "work.txt")); err != nil {
			return []string{}, nil
		}
		return []string{"work.txt"}, nil
	}
	collectGitDiffFn = func(context.Context, string) (string, error) { return "", nil }
	commitTerrariumExecutionFn = originalCommit
	mergeTerrariumCommitFn = func(context.Context, string, string) error { return nil }

	report, err := (&DockerOrchestrator{
		Substrate: "ephemeral",
		StepID:    stepID,
	}).RunSprout(context.Background(), "ephemeral work")
	if err != nil {
		t.Fatalf("ephemeral RunSprout: %v", err)
	}
	// Ephemeral runs are not managed, so FruitBranch should be empty.
	if report.FruitBranch != "" {
		t.Errorf("ephemeral run FruitBranch = %q, want empty (not managed)", report.FruitBranch)
	}

	// The push must target exactly the configured source branch ("feat"),
	// never a sprout/task-* run branch. This is the explicit contract:
	// non-managed (ephemeral) publication semantics are unchanged.
	if capturedPushBranch != "feat" {
		t.Errorf("ephemeral push branch = %q, want exactly %q (configured source branch)", capturedPushBranch, "feat")
	}
	_ = ephemeralMount
}

// TestManagedStepsRetainStepScopedFruitIdentity proves that Fruit identity is
// step/run scoped: two sequential managed runs from the same substrate, each
// with a distinct StepID, produce distinct FruitBranch and FruitCommit values.
//
// This test does NOT prove same-Pollen vs. different-Pollen concurrency: the
// DockerOrchestrator Conductor seam carries no Pollen identity, so Pollen
// isolation is above this layer. That property is evidence for the final
// OBJECTIVE concurrency exercise and is not demonstrated by a unit/integration
// test at this seam.
func TestManagedStepsRetainStepScopedFruitIdentity(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepA := "step-identity-a"
	stepB := "step-identity-b"

	runnerA := newManagedWritingRunner("a.txt")
	runnerB := newManagedWritingRunner("b.txt")
	runnerA.releaseRun()
	runnerB.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepA: runnerA, stepB: runnerB})

	// Run sequentially: two steps, same substrate, different StepIDs.
	reportA, err := (&DockerOrchestrator{Substrate: repository, StepID: stepA}).RunSprout(context.Background(), "step 1")
	if err != nil {
		t.Fatalf("step A: %v", err)
	}
	reportB, err := (&DockerOrchestrator{Substrate: repository, StepID: stepB}).RunSprout(context.Background(), "step 2")
	if err != nil {
		t.Fatalf("step B: %v", err)
	}

	// Each step has its own step-scoped Fruit identity.
	if reportA.FruitBranch == reportB.FruitBranch {
		t.Errorf("steps share FruitBranch %q; want step-scoped identity", reportA.FruitBranch)
	}
	if reportA.FruitCommit != "" && reportB.FruitCommit != "" && reportA.FruitCommit == reportB.FruitCommit {
		t.Errorf("steps share FruitCommit %q; want distinct commits", reportA.FruitCommit)
	}

	assertManagedBaseClean(t, repository)
}
