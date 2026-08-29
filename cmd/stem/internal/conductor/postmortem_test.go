package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// expiringPatienceBudget is short enough to expire inside a test and long
// enough that the stubbed setup ahead of the Sprout turn completes first. The
// run's own clock, deliberately nothing like the post-mortem's.
const expiringPatienceBudget = 300 * time.Millisecond

// budgetWaitingRunner returns whatever the run's context returns when it
// expires — the shape a real Sprout turn produces when a clock ends it. It
// never sleeps: the clock itself is the clock.
type budgetWaitingRunner struct{}

func (budgetWaitingRunner) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	<-ctx.Done()
	// A run a clock cut off mid-flight has written whatever it had written by
	// then, and the post-mortem is what commits it. Reporting otherwise would
	// make this test assert that a budget expiry discards the work.
	return sproutResult{WroteWorkspace: true}, ctx.Err()
}

// postMortemCapture records the state of the context the post-run tail was
// handed, sampled inside the seam rather than before it.
type postMortemCapture struct {
	called      bool
	hasDeadline bool
	remaining   time.Duration
	err         error
}

// capturePostMortemContext replaces the file-measurement seam — the first thing
// the post-run tail does — with one that records the context reaching it, and
// then behaves as the caller asked.
func capturePostMortemContext(t *testing.T, files []string, failWith error) *postMortemCapture {
	t.Helper()

	capture := &postMortemCapture{}
	original := collectStageableFilesFn
	t.Cleanup(func() { collectStageableFilesFn = original })

	collectStageableFilesFn = func(ctx context.Context, mountPath string, excludedPaths ...string) ([]string, error) {
		capture.called = true
		capture.err = ctx.Err()
		if deadline, ok := ctx.Deadline(); ok {
			capture.hasDeadline = true
			capture.remaining = time.Until(deadline)
		}
		if failWith != nil {
			return nil, failWith
		}
		copied := make([]string, len(files))
		copy(copied, files)
		return copied, nil
	}

	return capture
}

// assertPostMortemHasItsOwnClock is the load-bearing assertion of this slice.
//
// Asserting merely that the tail received a non-nil context passes against the
// defect: the spent context is non-nil, it simply has no time left. The
// property that separates fixed from broken is that the tail's context is
// still usable — unexpired, and bounded by the post-mortem budget rather than
// by the growth budget that just ran out.
func assertPostMortemHasItsOwnClock(t *testing.T, label string, capture *postMortemCapture) {
	t.Helper()

	if !capture.called {
		t.Fatalf("%s: the post-run tail was never reached; nothing was measured", label)
	}
	if capture.err != nil {
		t.Fatalf("%s: the tail was handed an already-spent context (Err = %v); the budget that ended the run also ended its post-mortem", label, capture.err)
	}
	if !capture.hasDeadline {
		t.Fatalf("%s: the tail's context carries no deadline; the post-mortem is unbounded, which is its own failure mode after a stalled run", label)
	}
	if capture.remaining <= 0 {
		t.Fatalf("%s: the tail's context has %s remaining; it is the expired growth budget, not a post-mortem budget", label, capture.remaining)
	}
	if capture.remaining > sproutPostMortemBudget {
		t.Fatalf("%s: the tail's context has %s remaining, more than the post-mortem budget %s; it is bounded by something else", label, capture.remaining, sproutPostMortemBudget)
	}
	// A deadline expired to get here, so anything close to it is that clock
	// rather than a fresh one.
	if capture.remaining <= expiringPatienceBudget {
		t.Fatalf("%s: the tail's context has %s remaining, within the expired run budget %s; it did not get its own clock", label, capture.remaining, expiringPatienceBudget)
	}
}

// TestBudgetExpiryIsTimedOutNotFailed pins the classification. A run cut off by
// a deadline surfaces as context.DeadlineExceeded, and calling that "failed" is
// the conflation the timed-out outcome exists to prevent.
func TestBudgetExpiryIsTimedOutNotFailed(t *testing.T) {
	// The wrapped case is the one observed live: the deadline surfaces from
	// whatever git call the tail made, not as a bare sentinel.
	wrapped := fmt.Errorf("git status --porcelain -uall -z failed: %w", context.DeadlineExceeded)

	testCases := []struct {
		name   string
		runErr error
		want   string
	}{
		{"bare deadline", context.DeadlineExceeded, SproutOutcomeTimedOut},
		{"wrapped deadline", wrapped, SproutOutcomeTimedOut},
		{"watchdog kill still times out", ErrSproutTimedOut, SproutOutcomeTimedOut},
		// Not a budget expiring. Naming an interrupt "timed-out" would be the
		// same conflation running the other way.
		{"cancellation is not a timeout", context.Canceled, SproutOutcomeFailed},
		{"an ordinary error still fails", errors.New("compile error"), SproutOutcomeFailed},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifySproutOutcome(testCase.runErr, changeEvidence{}, "", false)
			if got != testCase.want {
				t.Fatalf("classifySproutOutcome(%v) = %q, want %q", testCase.runErr, got, testCase.want)
			}
		})
	}
}

// TestRunSproutPostMortemOutlivesExpiredBudget drives a real deadline expiry
// through RunSprout and asserts the run still produces an account of itself:
// its own clock for the tail, the honest outcome, and the status record.
//
// The expiring clock here is the CALLER'S own deadline, not the configured
// growth budget. That is a deliberate re-pointing: a growth budget expiring no
// longer ends a run at all, it detaches from one, so it can no longer stand for
// "a clock ended this run" — while an inherited deadline still does, and the
// post-mortem must still outlive it. The substrate below therefore configures
// no patience at all.
//
// It doubles as the proof that a context.DeadlineExceeded which is NOT the
// growth budget must not quietly become a detach: nothing else would carry the
// work on, so the run ends, is classified, and is recorded.
func TestRunSproutPostMortemOutlivesExpiredBudget(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "")

	stubRunSproutCollaborators(t, root, budgetWaitingRunner{}, nil)
	// Replaces the commit seam the helper above installed, so the assertions
	// below read the written artifact rather than a captured struct.
	statusPath := stubStatusFileWrite(t, root)
	capture := capturePostMortemContext(t, []string{"pkg/thing.go"}, nil)

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "postmortem-runsprout",
		StatusPath:       statusPath,
		EventBus:         bus,
		DisableMergeBack: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), expiringPatienceBudget)
	defer cancel()
	report, err := orch.RunSprout(ctx, "post-mortem probe")

	assertPostMortemHasItsOwnClock(t, "RunSprout", capture)

	// The run's own ending is still reported as an error to the caller; what
	// changed is that it no longer takes the evidence with it.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunSprout error = %v, want context.DeadlineExceeded", err)
	}
	if report.Outcome != SproutOutcomeTimedOut {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeTimedOut)
	}
	if len(report.FilesModified) != 1 || report.FilesModified[0] != "pkg/thing.go" {
		t.Fatalf("report.FilesModified = %#v, want the measured file; the evidence did not survive the budget", report.FilesModified)
	}

	// The artifact a Botanist reads is a file on disk, so assert the file, not
	// only the struct that was handed to the writer.
	onDisk := readStatusFile(t, statusPath)
	if onDisk.Status != SproutOutcomeTimedOut {
		t.Fatalf("tendril-status.json status = %q, want %q", onDisk.Status, SproutOutcomeTimedOut)
	}
	if onDisk.Error == "" {
		t.Fatal("tendril-status.json carries no error; a timed-out run must say what ended it")
	}

	assertTerminalOutcome(t, events, eventbus.EventSproutWithered, SproutOutcomeTimedOut)

	// A deadline nobody else will carry on from is an ending, not a detach.
	// Detaching here would report a run as still growing when the very clock
	// that governs the work has expired.
	if detachedEvents := filterEvents(*events, eventbus.EventSproutDetached); len(detachedEvents) != 0 {
		t.Fatalf("got %d sprout-detached events for an inherited deadline, want 0; an expired caller deadline was mistaken for a spent growth budget", len(detachedEvents))
	}
}

// TestRunSproutReportsUnmeasurableEvidence covers the other half: when the
// measurement itself cannot be taken, the run says so rather than going silent.
// An absent filesModified with no reason beside it is what made a cut-off run
// indistinguishable from a run that changed nothing.
func TestRunSproutReportsUnmeasurableEvidence(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "")

	// Two distinct errors, so the test can tell which one reached the caller.
	runFailure := errors.New("the model gave up")
	measureFailure := errors.New("git status --porcelain -uall -z failed: no such worktree")

	stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "partial", err: runFailure}, nil)
	statusPath := stubStatusFileWrite(t, root)
	capturePostMortemContext(t, nil, measureFailure)

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "postmortem-unmeasurable",
		StatusPath:       statusPath,
		EventBus:         bus,
		DisableMergeBack: true,
	}
	report, err := orch.RunSprout(context.Background(), "post-mortem probe")

	// A failed measurement must not stand in for the run's own ending. It did
	// before: the collector's error was returned and the run's was discarded.
	if !errors.Is(err, runFailure) {
		t.Fatalf("RunSprout error = %v, want the run's own error %v", err, runFailure)
	}
	if errors.Is(err, measureFailure) {
		t.Fatalf("RunSprout error = %v; the measurement's error masked the run's", err)
	}
	if report.FilesUnmeasured == "" {
		t.Fatal("report.FilesUnmeasured is empty; an unmeasurable run must say why, not go silent")
	}
	if !strings.Contains(report.FilesUnmeasured, "no such worktree") {
		t.Fatalf("report.FilesUnmeasured = %q, want the measurement failure's reason", report.FilesUnmeasured)
	}

	onDisk := readStatusFile(t, statusPath)
	if onDisk.FilesUnmeasured == "" {
		t.Fatal("tendril-status.json carries no filesUnmeasured; the reason did not reach the artifact")
	}

	withered := findTerminalEvent(t, events, eventbus.EventSproutWithered)
	if _, present := withered.Data["filesModified"]; present {
		t.Fatalf("terminal event carries filesModified %#v though nothing was measured", withered.Data["filesModified"])
	}
	reason, ok := withered.Data["filesUnmeasured"].(string)
	if !ok || reason == "" {
		t.Fatalf("terminal event carries no filesUnmeasured; Data = %#v", withered.Data)
	}
}

// TestRunSequenceSproutAtPathPostMortemOutlivesExpiredBudget covers the other
// call site. This is the path the parallel budget actually flows through, so
// fixing only RunSprout would leave the observed defect exactly where it was.
//
// Re-pointed at the caller's own deadline for the reason given on the RunSprout
// case above: a growth budget no longer ends a run, so it can no longer stand
// for a clock that does.
func TestRunSequenceSproutAtPathPostMortemOutlivesExpiredBudget(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "")

	stubSequencePostMortemCollaborators(t, root)
	captured := stubSequenceCommit(t)
	capture := capturePostMortemContext(t, []string{"pkg/thing.go"}, nil)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "postmortem-seqpath",
		DisableMergeBack: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), expiringPatienceBudget)
	defer cancel()
	result, err := runSequenceSproutAtPath(ctx, orch, "post-mortem probe", root, root)

	assertPostMortemHasItsOwnClock(t, "runSequenceSproutAtPath", capture)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runSequenceSproutAtPath error = %v, want context.DeadlineExceeded", err)
	}
	if result.Outcome != SproutOutcomeTimedOut {
		t.Fatalf("result.Outcome = %q, want %q", result.Outcome, SproutOutcomeTimedOut)
	}
	if captured.Status != "" {
		t.Fatalf("recorded status = %q, want empty (commitTerrariumExecutionFn must not run for non-reviewable outcomes)", captured.Status)
	}
	if len(result.FilesModified) != 1 {
		t.Fatalf("result.FilesModified = %#v, want the measured file", result.FilesModified)
	}
}

// stubStatusFileWrite points the commit seam at the real status writer, so the
// tests above assert the JSON artifact rather than only the struct handed to
// the seam. The staging and committing around it stay stubbed.
func stubStatusFileWrite(t *testing.T, root string) string {
	t.Helper()

	statusPath := filepath.Join(root, "tendril-status.json")
	original := commitTerrariumExecutionFn
	t.Cleanup(func() { commitTerrariumExecutionFn = original })

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, path string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		if err := writeSproutStatus(statusPath, executionStatus); err != nil {
			return "", err
		}
		return "deadbeefcafe", nil
	}

	return statusPath
}

// readStatusFile decodes the written artifact. A missing file is the failure
// this slice exists to prevent, so it is reported as one rather than skipped.
func readStatusFile(t *testing.T, path string) sproutExecutionStatus {
	t.Helper()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("tendril-status.json was not written: %v", err)
	}
	var status sproutExecutionStatus
	if err := json.Unmarshal(payload, &status); err != nil {
		t.Fatalf("decode tendril-status.json: %v", err)
	}
	return status
}

// findTerminalEvent returns the single terminal event of the wanted type, and
// fails if the run did not publish exactly one.
func findTerminalEvent(t *testing.T, events *[]eventbus.Event, want eventbus.EventType) eventbus.Event {
	t.Helper()

	var found []eventbus.Event
	for _, event := range *events {
		if event.Type == want {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d %s events, want exactly 1; events = %#v", len(found), want, *events)
	}
	return found[0]
}

func assertTerminalOutcome(t *testing.T, events *[]eventbus.Event, want eventbus.EventType, wantOutcome string) {
	t.Helper()

	event := findTerminalEvent(t, events, want)
	if outcome, _ := event.Data["outcome"].(string); outcome != wantOutcome {
		t.Fatalf("%s carries outcome %q, want %q", want, event.Data["outcome"], wantOutcome)
	}
}

// stubSequencePostMortemCollaborators fakes the collaborators around the
// sequence path's Sprout turn, leaving the post-run tail real.
func stubSequencePostMortemCollaborators(t *testing.T, root string) {
	t.Helper()

	originalEnsure := ensureSproutImageFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalInjectCache := injectMycorrhizalCacheFn
	originalNewSprout := newSproutFn
	originalStash := stashHostWorkspaceFn
	originalSession := startTerrariumSessionFn
	originalDiff := collectGitDiffFn
	t.Cleanup(func() {
		ensureSproutImageFn = originalEnsure
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		injectMycorrhizalCacheFn = originalInjectCache
		newSproutFn = originalNewSprout
		stashHostWorkspaceFn = originalStash
		startTerrariumSessionFn = originalSession
		collectGitDiffFn = originalDiff
	})

	ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
	createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) { return root, nil }
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {}
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
	stashHostWorkspaceFn = func(ctx context.Context, repoRoot, runID string) (bool, error) { return false, nil }
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		return budgetWaitingRunner{}, nil
	}
	collectGitDiffFn = func(ctx context.Context, mountPath string) (string, error) { return "", nil }
}

func stubSequenceCommit(t *testing.T) *sproutExecutionStatus {
	t.Helper()

	captured := &sproutExecutionStatus{}
	original := commitTerrariumExecutionFn
	t.Cleanup(func() { commitTerrariumExecutionFn = original })

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		*captured = executionStatus
		return "deadbeefcafe", nil
	}

	return captured
}
