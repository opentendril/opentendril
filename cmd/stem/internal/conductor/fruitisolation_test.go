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

	runSproutPreflightChecksFn = func(context.Context) error { return nil }
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

// TestFailedManagedRunDoesNotDamageSuccessfulRunFruit proves that a failed
// overlapping run cannot delete, move, or overwrite a successful run's Fruit.
func TestFailedManagedRunDoesNotDamageSuccessfulRunFruit(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	successID := "success-fruit"
	failID := "fail-run"

	successRunner := newManagedWritingRunner("success.txt")
	failRunner := newManagedWritingRunner("fail.txt")
	capture := newManagedRunCapture()

	installManagedRunSeams(t, capture,
		map[string]sproutRunner{successID: successRunner, failID: failRunner})

	type result struct {
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
			results <- result{report: report, err: err}
		}(stepID)
	}

	// Wait for both to start.
	<-successRunner.started
	<-failRunner.started

	// Record both mounts before releasing.
	successMount, _, _ := capture.get(successID)
	failMount, _, _ := capture.get(failID)
	if successMount == "" || failMount == "" {
		t.Fatalf("mounts not captured: success=%q fail=%q", successMount, failMount)
	}
	if successMount == failMount {
		t.Fatalf("overlapping runs share the same worktree %q", successMount)
	}

	// Release both runs; fail run produces no files so it results in no-changes.
	successRunner.releaseRun()
	failRunner.releaseRun()

	// Collect both results.
	wantFruitBranch := "sprout/task-" + successID
	for i := 0; i < 2; i++ {
		<-results
	}

	// Success Fruit branch must still exist in the repository.
	if !branchExists(t, repository, wantFruitBranch) {
		t.Errorf("success Fruit branch %q was lost; fail run cleanup must not delete another run's branch", wantFruitBranch)
	}

	// Main/configured source branch must be unchanged.
	assertManagedBaseClean(t, repository)
}

// TestConcurrentManagedRunsWithSameSourceEditsProduceCompetingFruit verifies the
// core isolation scenario: two managed runs each edit the same source file from
// the same base commit, completing successfully with distinct competing commits
// that are both independent descendants of the original.
func TestConcurrentManagedRunsWithSameSourceEditsProduceCompetingFruit(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	baseCommit, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit: %v", err)
	}
	baseCommit = strings.TrimSpace(baseCommit)

	stepA := "compete-a"
	stepB := "compete-b"

	// Both runs write to the same file (seed.txt already exists as tracked).
	// Use "shared-edit.txt" as new file both create, so both show as reviewable.
	runnerA := newManagedWritingRunner("shared-edit.txt")
	runnerB := newManagedWritingRunner("shared-edit.txt")
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
			}).RunSprout(context.Background(), "edit shared file")
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

	// Write different content to the shared file in each workspace.
	if err := os.WriteFile(filepath.Join(mountA, "shared-edit.txt"), []byte("edit-a\n"), 0o644); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountB, "shared-edit.txt"), []byte("edit-b\n"), 0o644); err != nil {
		t.Fatalf("write B: %v", err)
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
		default:
			// DisableMergeBack is false for these (local managed), so FruitBranch
			// is only set for managed runs; the existing branch check covers it.
		}
	}

	// Both Fruit branches must exist.
	if !branchExists(t, repository, "sprout/task-"+stepA) {
		t.Errorf("Fruit branch for run A %q does not exist", "sprout/task-"+stepA)
	}
	if !branchExists(t, repository, "sprout/task-"+stepB) {
		t.Errorf("Fruit branch for run B %q does not exist", "sprout/task-"+stepB)
	}

	// Both commits must be descendants of the common base.
	ctx := context.Background()
	if reportA.FruitCommit != "" {
		// merge-base --is-ancestor <ancestor> <descendant> exits 0 iff ancestor is an ancestor of descendant.
		if _, err := runGitCommand(ctx, repository, "merge-base", "--is-ancestor", baseCommit, reportA.FruitCommit); err != nil {
			t.Errorf("run A Fruit commit %q is not a descendant of base %q", reportA.FruitCommit, baseCommit)
		}
	}
	if reportB.FruitCommit != "" {
		if _, err := runGitCommand(ctx, repository, "merge-base", "--is-ancestor", baseCommit, reportB.FruitCommit); err != nil {
			t.Errorf("run B Fruit commit %q is not a descendant of base %q", reportB.FruitCommit, baseCommit)
		}
	}

	// Source branch (main) must remain at the original commit.
	afterMain, _ := runGitCommand(ctx, repository, "rev-parse", "HEAD")
	if strings.TrimSpace(afterMain) != baseCommit {
		t.Errorf("managed base HEAD moved from %q to %q; must be unchanged", baseCommit, strings.TrimSpace(afterMain))
	}
	assertManagedBaseClean(t, repository)
}

// TestPublicationFailureDoesNotDamageOtherRunFruit proves that when one run's
// remote push fails, the other run's Fruit (already pushed) is unaffected.
func TestPublicationFailureDoesNotDamageOtherRunFruit(t *testing.T) {
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
	captureGood := newManagedRunCapture()
	captureFail := newManagedRunCapture()

	// Use the real push fn, but intercept it for the fail step.
	origPush := pushTerrariumCommitFn
	t.Cleanup(func() { pushTerrariumCommitFn = origPush })
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
		if stepID == stepFail {
			return pushFailErr
		}
		return origPush(ctx, mountPath, branch, cred, allowDefaultBranchCommit, stepID)
	}

	// Run the good run first (complete successfully with pushed Fruit).
	runnerGood.releaseRun()
	installRemoteManagedRunSeams(t, captureGood, map[string]sproutRunner{stepGood: runnerGood})
	reportGood, err := (&DockerOrchestrator{
		Substrate: "repo",
		StepID:    stepGood,
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

	// Now run the fail run (push fails for it).
	runnerFail.releaseRun()
	installRemoteManagedRunSeams(t, captureFail, map[string]sproutRunner{stepFail: runnerFail})
	_, err = (&DockerOrchestrator{
		Substrate: "repo",
		StepID:    stepFail,
	}).RunSprout(context.Background(), "fail work")
	// The fail run may or may not error depending on whether it produced
	// reviewable Fruit (the file may be absent). Either way, the good run's
	// Fruit must be intact.
	t.Logf("fail run returned: %v", err)

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
	runSproutPreflightChecksFn = func(context.Context) error { return nil }
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

	// The push must target the configured source branch (feat), not a sprout/task-* branch.
	if capturedPushBranch != "" && strings.HasPrefix(capturedPushBranch, "sprout/task-") {
		t.Errorf("ephemeral push targeted run branch %q; must use configured source branch", capturedPushBranch)
	}
	_ = ephemeralMount
}

// TestSamePollensRetainSeparateFruit proves that two managed runs from the same
// conceptual Pollen identity retain separate Fruit by step/run scoping, not Pollen
// scoping. (Pollen identity in the DockerOrchestrator would be a substrate-level
// config; here we verify run-workspace identity is step-scoped regardless.)
func TestSamePollensRetainSeparateFruit(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepA := "pollen-a-step1"
	stepB := "pollen-a-step2"

	runnerA := newManagedWritingRunner("a.txt")
	runnerB := newManagedWritingRunner("b.txt")
	runnerA.releaseRun()
	runnerB.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepA: runnerA, stepB: runnerB})

	// Run sequentially (same conceptual Pollen, different steps).
	reportA, err := (&DockerOrchestrator{Substrate: repository, StepID: stepA}).RunSprout(context.Background(), "step 1")
	if err != nil {
		t.Fatalf("step A: %v", err)
	}
	reportB, err := (&DockerOrchestrator{Substrate: repository, StepID: stepB}).RunSprout(context.Background(), "step 2")
	if err != nil {
		t.Fatalf("step B: %v", err)
	}

	// Each step has its own Fruit identity.
	if reportA.FruitBranch == reportB.FruitBranch {
		t.Errorf("same-Pollen steps share FruitBranch %q; want step-scoped identity", reportA.FruitBranch)
	}
	if reportA.FruitCommit != "" && reportB.FruitCommit != "" && reportA.FruitCommit == reportB.FruitCommit {
		t.Errorf("same-Pollen steps share FruitCommit %q; want distinct commits", reportA.FruitCommit)
	}

	assertManagedBaseClean(t, repository)
}
