package conductor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

// detachWaitLimit bounds how long a test waits for a terminal event that must
// arrive from the goroutine holding a detached run. It is a deadline for a
// failure, never a dwell: the events below are published as soon as the run
// ends, so a test only ever reaches this limit when the terminal is not coming.
const detachWaitLimit = 10 * time.Second

// heldSproutRunner is a Sprout turn that ends when the test says so, not when a
// clock says so. That is what makes a detach observable: the growth budget can
// expire with the run demonstrably still going, which is the whole claim under
// test. It also returns whatever a real turn would once released, so the
// terminal event that follows is a real one.
type heldSproutRunner struct {
	released chan struct{}
	response string
	// observed is the context the turn was actually handed, so a test can
	// prove the work was NOT run on the clock the Stem was waiting on.
	observed     chan context.Context
	usage        llm.Usage
	requestsMade bool
}

func newHeldSproutRunner(response string) *heldSproutRunner {
	return &heldSproutRunner{
		released: make(chan struct{}),
		response: response,
		observed: make(chan context.Context, 1),
	}
}

func (h *heldSproutRunner) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	select {
	case h.observed <- ctx:
	default:
	}
	select {
	case <-h.released:
		return sproutResult{
			Response:       h.response,
			WroteWorkspace: true,
			Usage:          h.usage,
			RequestsMade:   h.requestsMade,
		}, nil
	case <-ctx.Done():
		return sproutResult{}, ctx.Err()
	}
}

func (h *heldSproutRunner) release() { close(h.released) }

// countingToolSession counts how many times the terrarium was closed. The count
// is the evidence for the one thing a detach must not do: a detach that also
// tears down is a kill with a nicer name, and only a count can tell the two
// apart from outside.
type countingToolSession struct {
	closes atomic.Int64
}

func (c *countingToolSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
	return nil, nil
}

func (c *countingToolSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	return ToolResponse{}, nil
}

func (c *countingToolSession) Close() error {
	c.closes.Add(1)
	return nil
}

func (c *countingToolSession) Logs() string { return "" }

// stubCountingSession points the session seam at one countable terrarium and
// returns it.
func stubCountingSession(t *testing.T) *countingToolSession {
	t.Helper()

	session := &countingToolSession{}
	original := startTerrariumSessionFn
	t.Cleanup(func() { startTerrariumSessionFn = original })

	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return session, nil
	}

	return session
}

// stubCountingCommit counts calls to the commit and merge-back seams. A
// detached run has produced no result to commit, so the assertion this supports
// is that those paths are not reached AT ALL — reaching them and writing an
// "incomplete" message would be a different, quieter defect.
func stubCountingCommit(t *testing.T) (*atomic.Int64, *atomic.Int64) {
	t.Helper()

	commits := &atomic.Int64{}
	merges := &atomic.Int64{}
	originalCommit := commitTerrariumExecutionFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		commitTerrariumExecutionFn = originalCommit
		mergeTerrariumCommitFn = originalMerge
	})

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential) (string, error) {
		commits.Add(1)
		return "deadbeefcafe", nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error {
		merges.Add(1)
		return nil
	}

	return commits, merges
}

// sproutEventRecorder records sprout lifecycle events from whichever goroutine
// publishes them — which, for a detached run, is not the one the test is on.
type sproutEventRecorder struct {
	mu       sync.Mutex
	events   []eventbus.Event
	arrivals chan eventbus.Event
}

// recordSproutEvents subscribes to the detachment event and both terminals. All
// three go into one record on purpose: counting terminals only means something
// when a detachment published in place of one would have been seen too.
func recordSproutEvents(bus *eventbus.Bus) *sproutEventRecorder {
	recorder := &sproutEventRecorder{arrivals: make(chan eventbus.Event, 64)}
	for _, eventType := range []eventbus.EventType{
		eventbus.EventSproutMatured,
		eventbus.EventSproutWithered,
		eventbus.EventSproutDetached,
	} {
		bus.Subscribe(eventType, func(event eventbus.Event) {
			recorder.mu.Lock()
			recorder.events = append(recorder.events, event)
			recorder.mu.Unlock()
			select {
			case recorder.arrivals <- event:
			default:
			}
		})
	}
	return recorder
}

func (r *sproutEventRecorder) recorded() []eventbus.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]eventbus.Event, len(r.events))
	copy(copied, r.events)
	return copied
}

func (r *sproutEventRecorder) count(eventType eventbus.EventType) int {
	total := 0
	for _, event := range r.recorded() {
		if event.Type == eventType {
			total++
		}
	}
	return total
}

// terminalCount is the load-bearing number of this slice. It is a count rather
// than a presence check because "the terminal arrives exactly once, later" is
// not provable by a test that can only observe zero or non-zero: a run that
// published two would pass that.
func (r *sproutEventRecorder) terminalCount() int {
	return r.count(eventbus.EventSproutMatured) + r.count(eventbus.EventSproutWithered)
}

// awaitTerminal blocks until a terminal event arrives, and fails if none does.
// Arrivals are buffered from the moment of subscription, so an event published
// before this call is not missed.
func (r *sproutEventRecorder) awaitTerminal(t *testing.T, label string) eventbus.Event {
	t.Helper()

	deadline := time.NewTimer(detachWaitLimit)
	defer deadline.Stop()
	for {
		select {
		case event := <-r.arrivals:
			if event.Type == eventbus.EventSproutMatured || event.Type == eventbus.EventSproutWithered {
				return event
			}
		case <-deadline.C:
			t.Fatalf("%s: no terminal event arrived within %s; the run detached and never reported its ending. Recorded: %#v", label, detachWaitLimit, r.recorded())
		}
	}
}

// assertDetachedEvent checks the detachment was announced once, and announced
// the budget that was actually configured. The budget value is what makes this
// more than a presence check: a call site that applied the wrong duration, or
// the wrong unit, still publishes an event.
func assertDetachedEvent(t *testing.T, label string, recorder *sproutEventRecorder, wantBudget string) {
	t.Helper()

	detachments := 0
	var detachment eventbus.Event
	for _, event := range recorder.recorded() {
		if event.Type == eventbus.EventSproutDetached {
			detachments++
			detachment = event
		}
	}
	if detachments != 1 {
		t.Fatalf("%s: got %d sprout-detached events, want exactly 1; recorded %#v", label, detachments, recorder.recorded())
	}
	if budget, _ := detachment.Data["budget"].(string); budget != wantBudget {
		t.Fatalf("%s: sprout-detached reports budget %q, want the configured %q", label, budget, wantBudget)
	}
	if outcome, _ := detachment.Data["outcome"].(string); outcome != SproutOutcomeDetached {
		t.Fatalf("%s: sprout-detached carries outcome %q, want %q", label, outcome, SproutOutcomeDetached)
	}
}

// TestRunSproutDetachesInsteadOfKilling is the centre of this slice. A growth
// budget running out must stop the Stem waiting and nothing else: no terminal
// event, no closed terrarium, no commit — and then, when the Sprout actually
// finishes, exactly one terminal event carrying the real outcome.
func TestRunSproutDetachesInsteadOfKilling(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	runner := newHeldSproutRunner("finished after the Stem stopped waiting")
	stubRunSproutCollaborators(t, root, runner, []string{"pkg/thing.go"})
	session := stubCountingSession(t)
	commits, merges := stubCountingCommit(t)

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)

	orch := &DockerOrchestrator{
		Substrate: "bounded",
		StepID:    "detach-runsprout",
		EventBus:  bus,
	}
	report, err := orch.RunSprout(context.Background(), "detach probe")

	if err != nil {
		t.Fatalf("RunSprout returned %v; detaching is not a failure of the Stem", err)
	}
	if report.Outcome != SproutOutcomeDetached {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeDetached)
	}

	// The negative assertion this slice turns on, written as a count over
	// recorded events so it can fail. A terminal published here would announce
	// an ending that has not happened, and spend the run's only one.
	if terminals := recorder.terminalCount(); terminals != 0 {
		t.Fatalf("got %d terminal events at detach time, want 0; recorded %#v", terminals, recorder.recorded())
	}
	assertDetachedEvent(t, "RunSprout", recorder, "300ms")

	// A detach that also tears down is a kill with a nicer name.
	if closes := session.closes.Load(); closes != 0 {
		t.Fatalf("the terrarium was closed %d times on detach, want 0; the Sprout is still growing in it", closes)
	}
	if committed := commits.Load(); committed != 0 {
		t.Fatalf("the commit path ran %d times on detach, want 0; a detached run has produced no result to commit", committed)
	}
	if merged := merges.Load(); merged != 0 {
		t.Fatalf("the merge-back path ran %d times on detach, want 0", merged)
	}

	// The work continues, and ends on its own terms.
	runner.release()
	terminal := recorder.awaitTerminal(t, "RunSprout")

	if terminal.Type != eventbus.EventSproutMatured {
		t.Fatalf("terminal event is %s, want %s; the run finished cleanly after detaching", terminal.Type, eventbus.EventSproutMatured)
	}
	if outcome, _ := terminal.Data["outcome"].(string); outcome != SproutOutcomeComplete {
		t.Fatalf("terminal event carries outcome %q, want %q", outcome, SproutOutcomeComplete)
	}
	if terminals := recorder.terminalCount(); terminals != 1 {
		t.Fatalf("got %d terminal events once the run ended, want exactly 1; recorded %#v", terminals, recorder.recorded())
	}
	if closes := session.closes.Load(); closes == 0 {
		t.Fatal("the terrarium was never closed after the run ended; detaching may not leak a container forever")
	}
	if committed := commits.Load(); committed != 1 {
		t.Fatalf("the commit path ran %d times after the run ended, want exactly 1", committed)
	}
}

// TestRunSproutDetachRunsTheWorkOffTheWaitsClock proves the mechanism behind
// the behaviour above rather than only its symptom: the Sprout turn is handed a
// context that is not the one the Stem waits on. If the turn ran on the growth
// budget it could not survive the budget, whatever the events said.
func TestRunSproutDetachRunsTheWorkOffTheWaitsClock(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	runner := newHeldSproutRunner("done")
	stubRunSproutCollaborators(t, root, runner, nil)
	stubCountingSession(t)

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "detach-workclock",
		EventBus:         bus,
		DisableMergeBack: true,
	}
	report, err := orch.RunSprout(context.Background(), "detach probe")
	if err != nil || report.Outcome != SproutOutcomeDetached {
		t.Fatalf("RunSprout = (%q, %v), want a detached run", report.Outcome, err)
	}

	var workCtx context.Context
	select {
	case workCtx = <-runner.observed:
	default:
		t.Fatal("the Sprout turn was never started; nothing was detached from")
	}
	if workCtx.Err() != nil {
		t.Fatalf("the Sprout turn's context is already done (%v) while the run is still going; the work was run on the clock the Stem was waiting on", workCtx.Err())
	}
	if deadline, ok := workCtx.Deadline(); ok {
		t.Fatalf("the Sprout turn's context carries a deadline (%s away) though no reaper is configured; the growth budget reached the work", time.Until(deadline))
	}

	runner.release()
	recorder.awaitTerminal(t, "RunSprout")
}

// TestOrphanReaperStopsARunNothingWaitsOn covers the backstop. The Stem detaches
// when its patience runs out, and nothing is listening after that — so a second,
// much longer clock ends the terrarium and names why it ended.
func TestOrphanReaperStopsARunNothingWaitsOn(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 200ms\n      reap: 700ms\n")

	// This runner ends only when its own context does, so the reaper is the
	// only thing that can end it.
	stubRunSproutCollaborators(t, root, budgetWaitingRunner{}, nil)
	session := stubCountingSession(t)

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "reaper-runsprout",
		EventBus:         bus,
		DisableMergeBack: true,
	}
	report, err := orch.RunSprout(context.Background(), "reaper probe")
	if err != nil {
		t.Fatalf("RunSprout returned %v, want a clean detach", err)
	}
	if report.Outcome != SproutOutcomeDetached {
		t.Fatalf("report.Outcome = %q, want %q before the reaper fires", report.Outcome, SproutOutcomeDetached)
	}
	if closes := session.closes.Load(); closes != 0 {
		t.Fatalf("the terrarium was closed %d times at detach, want 0; the reaper stops it later or not at all", closes)
	}

	terminal := recorder.awaitTerminal(t, "orphan reaper")

	// Named as reaped, never as matured and never as a plain failure: the run
	// was stopped because nothing was waiting on it, which is not a verdict on
	// the work.
	if terminal.Type != eventbus.EventSproutWithered {
		t.Fatalf("terminal event is %s, want %s; a reaped run did not finish", terminal.Type, eventbus.EventSproutWithered)
	}
	if outcome, _ := terminal.Data["outcome"].(string); outcome != SproutOutcomeReaped {
		t.Fatalf("terminal event carries outcome %q, want %q; the backstop must name itself", outcome, SproutOutcomeReaped)
	}
	if reason, _ := terminal.Data["error"].(string); !strings.Contains(reason, "reaped") {
		t.Fatalf("terminal event reason %q does not say the run was reaped", reason)
	}
	if terminals := recorder.terminalCount(); terminals != 1 {
		t.Fatalf("got %d terminal events, want exactly 1; recorded %#v", terminals, recorder.recorded())
	}
	if closes := session.closes.Load(); closes == 0 {
		t.Fatal("the reaper never closed the terrarium; a container nothing waits on was left running")
	}
}

// TestRunSproutWithoutReaperLeavesTheWorkUnbounded pins the other half of the
// reaper's contract: unconfigured means no reaper, not a silent default. A
// default would end runs at a duration no operator ever wrote.
func TestRunSproutWithoutReaperLeavesTheWorkUnbounded(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 200ms\n")

	runner := newHeldSproutRunner("done")
	stubRunSproutCollaborators(t, root, runner, nil)
	stubCountingSession(t)

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "reaper-unset",
		EventBus:         bus,
		DisableMergeBack: true,
	}
	if _, err := orch.RunSprout(context.Background(), "reaper probe"); err != nil {
		t.Fatalf("RunSprout returned %v, want a clean detach", err)
	}

	var workCtx context.Context
	select {
	case workCtx = <-runner.observed:
	default:
		t.Fatal("the Sprout turn was never started")
	}
	if deadline, ok := workCtx.Deadline(); ok {
		t.Fatalf("the work carries a deadline %s away though patience.reap is unset; a reaper was invented", time.Until(deadline))
	}

	runner.release()
	recorder.awaitTerminal(t, "unset reaper")
}

// TestRunSequenceSproutAtPathDetachesInsteadOfKilling covers the other call
// site. This is the path the parallel budget actually flows through, so a
// detach implemented only in RunSprout would leave the behaviour absent exactly
// where it is load-bearing.
func TestRunSequenceSproutAtPathDetachesInsteadOfKilling(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	stubSequencePostMortemCollaborators(t, root)
	runner := newHeldSproutRunner("finished after the Stem stopped waiting")
	stubSequenceRunner(t, runner)
	session := stubCountingSession(t)
	commits := stubCountingSequenceCommit(t)

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "detach-seqpath",
		EventBus:         bus,
		DisableMergeBack: true,
	}
	result, err := runSequenceSproutAtPath(context.Background(), orch, "detach probe", root, root)

	if err != nil {
		t.Fatalf("runSequenceSproutAtPath returned %v; detaching is not a failure of the Stem", err)
	}
	if result.Outcome != SproutOutcomeDetached {
		t.Fatalf("result.Outcome = %q, want %q", result.Outcome, SproutOutcomeDetached)
	}
	if result.DetachedEnd == nil {
		t.Fatal("a detached run reported no way to learn when it ends; its caller cannot hold the worktree open")
	}

	if terminals := recorder.terminalCount(); terminals != 0 {
		t.Fatalf("got %d terminal events at detach time, want 0; recorded %#v", terminals, recorder.recorded())
	}
	assertDetachedEvent(t, "runSequenceSproutAtPath", recorder, "300ms")
	if closes := session.closes.Load(); closes != 0 {
		t.Fatalf("the terrarium was closed %d times on detach, want 0", closes)
	}
	if committed := commits.Load(); committed != 0 {
		t.Fatalf("the commit path ran %d times on detach, want 0", committed)
	}

	runner.release()
	terminal := recorder.awaitTerminal(t, "runSequenceSproutAtPath")

	if terminal.Type != eventbus.EventSproutMatured {
		t.Fatalf("terminal event is %s, want %s", terminal.Type, eventbus.EventSproutMatured)
	}
	if terminals := recorder.terminalCount(); terminals != 1 {
		t.Fatalf("got %d terminal events once the run ended, want exactly 1; recorded %#v", terminals, recorder.recorded())
	}

	// The run reports its real ending to whoever was holding on, so a caller
	// that could not leave it alone gets the result rather than an empty one.
	ending := <-result.DetachedEnd
	if ending.err != nil {
		t.Fatalf("the detached run ended with %v, want a clean finish", ending.err)
	}
	if ending.result.Response != "finished after the Stem stopped waiting" {
		t.Fatalf("the detached run reported response %q; the caller would have recorded an empty result", ending.result.Response)
	}
	if closes := session.closes.Load(); closes == 0 {
		t.Fatal("the terrarium was never closed after the sequence run ended")
	}
}

// TestRunSequenceSproutDetachHoldsBackTerminalAndWorktree covers the sequence
// path one level up, where its terminal event is actually published and where
// the worktree the Sprout is still writing to is actually removed. The call
// below returns while the run continues, so both have to be held back — and
// both are published or removed by code the call site test cannot reach.
func TestRunSequenceSproutDetachHoldsBackTerminalAndWorktree(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	stubSequencePostMortemCollaborators(t, root)
	runner := newHeldSproutRunner("finished after the Stem stopped waiting")
	stubSequenceRunner(t, runner)
	stubCountingSession(t)
	stubCountingSequenceCommit(t)

	worktreeRemovals := &atomic.Int64{}
	removed := make(chan struct{}, 1)
	originalRemove := removeShadowWorktreeFn
	t.Cleanup(func() { removeShadowWorktreeFn = originalRemove })
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {
		worktreeRemovals.Add(1)
		select {
		case removed <- struct{}{}:
		default:
		}
	}

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "detach-seqwrapper",
		EventBus:         bus,
		DisableMergeBack: true,
	}
	if _, err := runSequenceSprout(context.Background(), orch, "detach probe"); err != nil {
		t.Fatalf("runSequenceSprout returned %v; detaching is not a failure of the Stem", err)
	}

	if terminals := recorder.terminalCount(); terminals != 0 {
		t.Fatalf("got %d terminal events at detach time, want 0; recorded %#v", terminals, recorder.recorded())
	}
	assertDetachedEvent(t, "runSequenceSprout", recorder, "300ms")
	if removals := worktreeRemovals.Load(); removals != 0 {
		t.Fatalf("the worktree was removed %d times on detach, want 0; the Sprout is still writing to it", removals)
	}

	runner.release()
	recorder.awaitTerminal(t, "runSequenceSprout")
	if terminals := recorder.terminalCount(); terminals != 1 {
		t.Fatalf("got %d terminal events once the run ended, want exactly 1; recorded %#v", terminals, recorder.recorded())
	}

	select {
	case <-removed:
	case <-time.After(detachWaitLimit):
		t.Fatalf("the worktree was never removed after the detached run ended; detaching may not leak a worktree forever")
	}
}

// TestAwaitDetachedRunReturnsTheRunsRealEnding pins what a caller that cannot
// leave a run alone gets back. Most sprout call sites remove the worktree the
// moment they return, so they wait — and waiting has to yield the run's own
// result, not the empty placeholder the detach itself returned.
func TestAwaitDetachedRunReturnsTheRunsRealEnding(t *testing.T) {
	t.Run("a detached run's real ending replaces the placeholder", func(t *testing.T) {
		ending := make(chan detachedRunEnding, 1)
		realFailure := errors.New("the model gave up eventually")
		ending <- detachedRunEnding{
			result: sproutExecutionResult{Response: "the real answer", Outcome: SproutOutcomeFailed},
			err:    realFailure,
		}
		close(ending)

		placeholder := sproutExecutionResult{Outcome: SproutOutcomeDetached, DetachedEnd: ending}

		result, err := awaitDetachedRun(placeholder, nil)

		if result.Response != "the real answer" {
			t.Fatalf("response = %q, want the detached run's own; the caller recorded the placeholder", result.Response)
		}
		if result.Outcome != SproutOutcomeFailed {
			t.Fatalf("outcome = %q, want %q; a run still reported as detached would be filed as matured by a caller that waited for it", result.Outcome, SproutOutcomeFailed)
		}
		if !errors.Is(err, realFailure) {
			t.Fatalf("err = %v, want the detached run's own error %v", err, realFailure)
		}
	})

	t.Run("a run that never detached passes straight through", func(t *testing.T) {
		attached := sproutExecutionResult{Response: "done", Outcome: SproutOutcomeComplete}
		attachedErr := errors.New("commit failed")

		result, err := awaitDetachedRun(attached, attachedErr)

		if result.Response != "done" || result.Outcome != SproutOutcomeComplete {
			t.Fatalf("result = %#v, want it unchanged", result)
		}
		if !errors.Is(err, attachedErr) {
			t.Fatalf("err = %v, want it unchanged", err)
		}
	})
}

// stubSequenceRunner replaces the Sprout the sequence path builds, after
// stubSequencePostMortemCollaborators has installed its own.
func stubSequenceRunner(t *testing.T, runner sproutRunner) {
	t.Helper()

	original := newSproutFn
	t.Cleanup(func() { newSproutFn = original })

	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		return runner, nil
	}
}

// stubCountingSequenceCommit counts the sequence path's commit seam.
func stubCountingSequenceCommit(t *testing.T) *atomic.Int64 {
	t.Helper()

	commits := &atomic.Int64{}
	original := commitTerrariumExecutionFn
	t.Cleanup(func() { commitTerrariumExecutionFn = original })

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential) (string, error) {
		commits.Add(1)
		return "deadbeefcafe", nil
	}

	return commits
}

// TestPatienceReapLoading pins the schema half of the reaper, on the same terms
// the growth budget is held to: a written value parses to exactly that
// duration, an absent one is zero without error, and a value that cannot be
// honoured fails the load by name rather than defaulting quietly.
func TestPatienceReapLoading(t *testing.T) {
	t.Run("configured value parses to the written duration", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      reap: 6h\n")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}

		spec, ok := config.Substrates["core"]
		if !ok {
			t.Fatalf("substrate core missing from %#v", config.Substrates)
		}
		budget, err := spec.Patience.ReapBudget()
		if err != nil {
			t.Fatalf("ReapBudget failed: %v", err)
		}
		// Six hours as a literal the test owns, never a value read back from
		// the parser it is checking.
		if budget != 6*time.Hour {
			t.Fatalf("ReapBudget = %s, want %s", budget, 6*time.Hour)
		}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "core"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan failed: %v", err)
		}
		if plan.reapBudget != 6*time.Hour {
			t.Fatalf("plan.reapBudget = %s, want %s; the plan is the carrier and dropped it", plan.reapBudget, 6*time.Hour)
		}
	})

	t.Run("absent reaper is zero without error", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      growth: 20m\n")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}
		budget, err := config.Substrates["core"].Patience.ReapBudget()
		if err != nil {
			t.Fatalf("ReapBudget failed: %v", err)
		}
		if budget != 0 {
			t.Fatalf("ReapBudget = %s, want 0 when unconfigured", budget)
		}
	})

	t.Run("malformed duration fails the load naming the field", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      reap: eventually\n")

		config, err := LoadSubstratesConfig("")
		if err == nil {
			t.Fatalf("LoadSubstratesConfig returned no error; config = %#v", config)
		}
		if config != nil {
			t.Fatalf("expected nil config alongside the error, got %#v", config)
		}
		for _, want := range []string{"patience.reap", "eventually", `"core"`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("load error %q does not mention %q", err.Error(), want)
			}
		}
	})

	t.Run("zero duration fails the load rather than reaping instantly", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      reap: 0s\n")

		config, err := LoadSubstratesConfig("")
		if err == nil {
			t.Fatalf("LoadSubstratesConfig returned no error for a zero reaper; config = %#v", config)
		}
		if !strings.Contains(err.Error(), "patience.reap") {
			t.Fatalf("load error %q does not mention patience.reap", err.Error())
		}
	})
}

// TestAwaitingCallerEndsInsteadOfDetaching proves that an awaiting caller whose
// budget expires gets a finished result classified as timed-out, rather than
// detaching into an immediate await.
func TestAwaitingCallerEndsInsteadOfDetaching(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	stubSequencePostMortemCollaborators(t, root)
	runner := newHeldSproutRunner("this response should not be seen")
	stubSequenceRunner(t, runner)
	stubCountingSession(t)
	stubCountingSequenceCommit(t)

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)

	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "await-seqpath",
		EventBus:         bus,
		DisableMergeBack: true,
		AwaitsRunEnding:  true,
	}
	_, err := runSequenceSprout(context.Background(), orch, "await probe")

	if err == nil {
		t.Fatalf("runSequenceSprout returned nil error; expected it to fail with timed-out")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}

	// 2. no sprout-detached event — as an explicit count over recorded bus events
	if detachments := recorder.count(eventbus.EventSproutDetached); detachments != 0 {
		t.Fatalf("got %d sprout-detached events, want 0; an awaiting caller does not detach", detachments)
	}

	// 3. exactly one terminal event, counted, at the ending
	if terminals := recorder.terminalCount(); terminals != 1 {
		t.Fatalf("got %d terminal events, want exactly 1; recorded %#v", terminals, recorder.recorded())
	}

	terminal := recorder.awaitTerminal(t, "Awaiting Caller")
	if terminal.Type != eventbus.EventSproutWithered {
		t.Fatalf("terminal event is %s, want %s", terminal.Type, eventbus.EventSproutWithered)
	}
	if outcome, _ := terminal.Data["outcome"].(string); outcome != SproutOutcomeTimedOut {
		t.Fatalf("terminal event carries outcome %q, want %q", outcome, SproutOutcomeTimedOut)
	}
}
