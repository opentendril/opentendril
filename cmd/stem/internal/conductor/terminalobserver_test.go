package conductor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// TestRunSproutTerminalObserverFiresBeforeSynchronousReturn is the sync half of
// the persistence seam: the observer must run at the existing terminal
// lifecycle point, before RunSprout hands its report back.
func TestRunSproutTerminalObserverFiresBeforeSynchronousReturn(t *testing.T) {
	var called bool
	var seen SproutRunReport
	orch := &DockerOrchestrator{
		Substrate:        t.TempDir(),
		StepID:           "sync-observer",
		DisableMergeBack: true,
		OnTerminal: func(report SproutRunReport, err error) {
			called = true
			seen = report
		},
	}
	stubUsageReportRun(t, usageReportRunner{
		result: sproutResult{Response: "done", Usage: completeUsage(4, 2, 6, "0.10", "USD", "api"), RequestsMade: true},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := orch.RunSprout(ctx, "task")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if !called {
		t.Fatal("OnTerminal was not invoked before RunSprout returned")
	}
	if !seen.RequestsMade || seen.Usage.PromptTokens == nil || *seen.Usage.PromptTokens != 4 {
		t.Fatalf("synchronous observer report = %+v", seen)
	}
	if report.Outcome != seen.Outcome {
		t.Fatalf("observer outcome %q != returned outcome %q", seen.Outcome, report.Outcome)
	}
}

// TestRunSproutDetachedTerminalObserverWritesOnlyWhenComplete proves the
// detached persistence seam: the immediate detached return is not a terminal
// write, and the later completeRun invokes the observer once with the real
// populated report.
func TestRunSproutDetachedTerminalObserverWritesOnlyWhenComplete(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	runner := newHeldSproutRunner("finished after the Stem stopped waiting")
	runner.usage = completeUsage(30, 15, 45, "4.00", "credits", "openrouter")
	runner.requestsMade = true
	stubRunSproutCollaborators(t, root, runner, []string{"pkg/thing.go"})
	stubCountingSession(t)
	stubCountingCommit(t)

	var mu sync.Mutex
	writes := make([]SproutRunReport, 0, 1)
	arrived := make(chan struct{})
	bus := eventbus.New()
	orch := &DockerOrchestrator{
		Substrate: "bounded",
		StepID:    "detach-observer",
		EventBus:  bus,
		OnTerminal: func(report SproutRunReport, err error) {
			mu.Lock()
			writes = append(writes, report)
			mu.Unlock()
			select {
			case <-arrived:
			default:
				close(arrived)
			}
		},
	}

	report, err := orch.RunSprout(context.Background(), "detach probe")
	if err != nil {
		t.Fatalf("RunSprout returned %v; detaching is not a failure", err)
	}
	if report.Outcome != SproutOutcomeDetached {
		t.Fatalf("Outcome = %q, want detached", report.Outcome)
	}

	mu.Lock()
	atDetach := len(writes)
	mu.Unlock()
	if atDetach != 0 {
		t.Fatalf("got %d terminal observer writes at detach, want 0", atDetach)
	}

	runner.release()
	select {
	case <-arrived:
	case <-time.After(detachWaitLimit):
		t.Fatalf("terminal observer was not invoked within %s after the run ended", detachWaitLimit)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("got %d terminal observer writes after completion, want 1", len(writes))
	}
	got := writes[0]
	if got.Outcome == SproutOutcomeDetached {
		t.Fatal("later observer write still carried the detached outcome")
	}
	if !got.RequestsMade {
		t.Fatal("later observer write had RequestsMade=false")
	}
	if got.Usage.PromptTokens == nil || *got.Usage.PromptTokens != 30 {
		t.Fatalf("later observer write usage = %+v, want populated execution tokens", got.Usage)
	}
	if got.Usage.CostAmount == nil || *got.Usage.CostAmount != "4.00" {
		t.Fatalf("later observer write cost = %v, want 4.00", got.Usage.CostAmount)
	}
}

// TestRunSproutAwaitsGrowthExpiryTimesOutInsteadOfDetaching is the one-shot
// contract: a caller that will close its store cannot detach. Growth expiry
// ends the work as timed-out, fires OnTerminal once, and keeps any usage
// captured before the cancel.
func TestRunSproutAwaitsGrowthExpiryTimesOutInsteadOfDetaching(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	runner := newHeldSproutRunner("should not be seen as a finished turn")
	runner.usage = completeUsage(21, 7, 28, "0.25", "credits", "openrouter")
	runner.requestsMade = true
	stubRunSproutCollaborators(t, root, runner, nil)
	stubCountingSession(t)

	var mu sync.Mutex
	var writes []struct {
		report SproutRunReport
		err    error
	}
	bus := eventbus.New()
	recorder := recordSproutEvents(bus)
	orch := &DockerOrchestrator{
		Substrate:        "bounded",
		StepID:           "await-observer",
		EventBus:         bus,
		DisableMergeBack: true,
		AwaitsRunEnding:  true,
		OnTerminal: func(report SproutRunReport, err error) {
			mu.Lock()
			writes = append(writes, struct {
				report SproutRunReport
				err    error
			}{report, err})
			mu.Unlock()
		},
	}

	report, err := orch.RunSprout(context.Background(), "await probe")
	if err == nil {
		t.Fatal("RunSprout returned nil error; an awaiting growth expiry is a terminal timed-out")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if report.Outcome == SproutOutcomeDetached {
		t.Fatal("awaiting RunSprout returned detached")
	}
	if report.Outcome != SproutOutcomeTimedOut {
		t.Fatalf("Outcome = %q, want timed-out", report.Outcome)
	}
	if recorder.count(eventbus.EventSproutDetached) != 0 {
		t.Fatalf("awaiting path published sprout-detached: %#v", recorder.recorded())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("OnTerminal fired %d times, want exactly 1", len(writes))
	}
	got := writes[0]
	if got.report.Outcome != SproutOutcomeTimedOut {
		t.Fatalf("observer outcome = %q, want timed-out", got.report.Outcome)
	}
	if !got.report.RequestsMade {
		t.Fatal("observer dropped RequestsMade on the timed-out path")
	}
	if got.report.Usage.PromptTokens == nil || *got.report.Usage.PromptTokens != 21 {
		t.Fatalf("observer usage = %+v, want usage captured before timeout", got.report.Usage)
	}
	if got.report.Usage.CostAmount == nil || *got.report.Usage.CostAmount != "0.25" {
		t.Fatalf("observer cost = %v, want 0.25 credits", got.report.Usage.CostAmount)
	}
}
