package scheduler

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"
)

// fakeFirer records fires and can block or fail on demand.
type fakeFirer struct {
	mu        sync.Mutex
	fires     []string
	err       error         // every fire fails with this, if non-nil
	failFirst int           // this many initial fires wither, then fires succeed
	started   chan struct{} // closed/sent when a fire begins, if non-nil
	release   chan struct{} // fire blocks until this closes, if non-nil
}

func (f *fakeFirer) Fire(_ context.Context, name string, _ Entry) error {
	f.mu.Lock()
	f.fires = append(f.fires, name)
	n := len(f.fires)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	if f.err != nil {
		return f.err
	}
	if n <= f.failFirst {
		return errors.New("the run withered")
	}
	return nil
}

func (f *fakeFirer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fires)
}

func (f *fakeFirer) countFor(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, fired := range f.fires {
		if fired == name {
			n++
		}
	}
	return n
}

// testScheduler builds a Scheduler over the given entries with an injected
// clock, so tests never sleep on wall-clock cron.
func testScheduler(t *testing.T, firer Firer, at time.Time, entries map[string]Entry) (*Scheduler, *time.Time) {
	t.Helper()
	now := at
	cfg := Config{Enabled: true, Schedules: entries}
	s := New(cfg, firer, log.New(io.Discard, "", 0))
	s.now = func() time.Time { return now }
	return s, &now
}

func TestDueEntryFiresAndNotDueDoesNot(t *testing.T) {
	firer := &fakeFirer{}
	start := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"noon":     {Cron: "0 12 * * *", Sequence: "nightly"},
		"midnight": {Cron: "0 0 * * *", Sequence: "other"},
	})

	s.primeAll()

	// 11:30 — neither entry is due.
	*now = time.Date(2026, 7, 13, 11, 30, 0, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()
	if firer.count() != 0 {
		t.Fatalf("no entry is due at 11:30, but %d fired", firer.count())
	}

	// 12:00:10 — only "noon" is due.
	*now = time.Date(2026, 7, 13, 12, 0, 10, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()
	if firer.count() != 1 {
		t.Fatalf("want exactly 1 fire at 12:00, got %d", firer.count())
	}
	if firer.fires[0] != "noon" {
		t.Fatalf("want the noon entry to fire, got %q", firer.fires[0])
	}

	// Still 12:00 on the next tick — the entry already advanced to tomorrow,
	// so it must not re-fire within the same minute.
	*now = time.Date(2026, 7, 13, 12, 0, 40, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()
	if firer.count() != 1 {
		t.Fatalf("entry re-fired within its scheduled minute: %d fires", firer.count())
	}
}

func TestSkipIfRunningPreventsConcurrentFire(t *testing.T) {
	firer := &fakeFirer{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"hourly": {Cron: "0 * * * *", Sequence: "seq", Overlap: OverlapSkip},
	})

	s.primeAll()

	// First fire starts and blocks inside the firer.
	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	<-firer.started

	// The next cron boundary arrives while the first run is still growing:
	// skip-if-running must prevent a second concurrent fire.
	*now = time.Date(2026, 7, 13, 13, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	if got := firer.count(); got != 1 {
		t.Fatalf("skip-if-running failed: want 1 in-flight fire, got %d", got)
	}

	// After the first run finishes, the following boundary fires normally.
	close(firer.release)
	s.wg.Wait()
	firer.release = nil
	*now = time.Date(2026, 7, 13, 14, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	<-firer.started
	s.wg.Wait()
	if got := firer.count(); got != 2 {
		t.Fatalf("entry must fire again once the previous run finished: want 2 fires, got %d", got)
	}
}

func TestFirerErrorDoesNotKillTheLoop(t *testing.T) {
	firer := &fakeFirer{err: errors.New("the run withered")}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"hourly": {Cron: "0 * * * *", Sequence: "seq"},
	})

	s.primeAll()

	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()

	*now = time.Date(2026, 7, 13, 13, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()

	if got := firer.count(); got != 2 {
		t.Fatalf("a failing firer must not stop future fires: want 2 fires, got %d", got)
	}
}

func TestImpossibleSpecIsDisabledNotBusyLooped(t *testing.T) {
	firer := &fakeFirer{}
	start := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	// February 30th never exists, so Next always returns the zero time.
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"never": {Cron: "0 0 30 2 *", Sequence: "seq"},
	})

	s.primeAll()

	if len(s.entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(s.entries))
	}
	if !s.entries[0].disabled {
		t.Fatal("an entry whose Next is the zero time must be disabled at prime")
	}

	*now = start.AddDate(1, 0, 0)
	s.checkAndFire(context.Background())
	s.wg.Wait()
	if firer.count() != 0 {
		t.Fatalf("a disabled entry must never fire, got %d fires", firer.count())
	}
}

func TestOverlapQueueCoalescesFiresIntoOnePendingRun(t *testing.T) {
	firer := &fakeFirer{
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"hourly": {Cron: "0 * * * *", Sequence: "seq", Overlap: OverlapQueue},
	})

	s.primeAll()

	// First fire starts and blocks inside the firer.
	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	<-firer.started

	// Two more cron boundaries land while the first run is still growing:
	// they must coalesce into exactly ONE pending run, not a backlog.
	*now = time.Date(2026, 7, 13, 13, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	*now = time.Date(2026, 7, 13, 14, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	if got := firer.count(); got != 1 {
		t.Fatalf("queued fires must not start while the previous run is growing: want 1 fire, got %d", got)
	}

	// When the in-flight run finishes, exactly one queued run grows.
	close(firer.release)
	s.wg.Wait()
	if got := firer.count(); got != 2 {
		t.Fatalf("coalescing failed: 2 fires landed while growing, want exactly 1 queued run (2 fires total), got %d", got)
	}

	// The latch is fully released afterwards: the next boundary fires anew.
	firer.release = nil
	*now = time.Date(2026, 7, 13, 15, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()
	if got := firer.count(); got != 3 {
		t.Fatalf("entry must fire normally after the queue drained: want 3 fires, got %d", got)
	}
}

func TestOverlapQueueDivergesFromSkip(t *testing.T) {
	firer := &fakeFirer{
		started: make(chan struct{}, 6),
		release: make(chan struct{}),
	}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"queued":  {Cron: "0 * * * *", Sequence: "seq-a", Overlap: OverlapQueue},
		"skipped": {Cron: "0 * * * *", Sequence: "seq-b", Overlap: OverlapSkip},
	})

	s.primeAll()

	// Both entries fire and block.
	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	<-firer.started
	<-firer.started

	// The next boundary lands while both are growing: the skip entry drops
	// its fire, the queue entry keeps it pending.
	*now = time.Date(2026, 7, 13, 13, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())

	close(firer.release)
	s.wg.Wait()

	if got := firer.countFor("queued"); got != 2 {
		t.Fatalf("overlap %q entry must grow its queued run: want 2 fires, got %d", OverlapQueue, got)
	}
	if got := firer.countFor("skipped"); got != 1 {
		t.Fatalf("overlap %q entry must drop the overlapping fire: want 1 fire, got %d", OverlapSkip, got)
	}
}

func TestRetriesExhaustThenGiveUp(t *testing.T) {
	firer := &fakeFirer{err: errors.New("the run withered")}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"hourly": {Cron: "0 * * * *", Sequence: "seq", Retries: 2},
	})
	var backoffs []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) bool {
		backoffs = append(backoffs, d)
		return true
	}

	s.primeAll()

	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()

	if got := firer.count(); got != 3 {
		t.Fatalf("retries: 2 means 1 attempt + 2 re-attempts: want 3 fires, got %d", got)
	}
	if len(backoffs) != 2 {
		t.Fatalf("want a backoff before each of the 2 re-attempts, got %d", len(backoffs))
	}

	// Giving up is per-fire: the next cron boundary grows a fresh attempt.
	*now = time.Date(2026, 7, 13, 13, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()
	if got := firer.count(); got != 6 {
		t.Fatalf("after giving up, the next fire must retry afresh: want 6 fires, got %d", got)
	}
}

func TestRetriesSucceedOnAttemptN(t *testing.T) {
	firer := &fakeFirer{failFirst: 2}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"hourly": {Cron: "0 * * * *", Sequence: "seq", Retries: 5},
	})
	s.sleep = func(context.Context, time.Duration) bool { return true }

	s.primeAll()

	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	s.wg.Wait()

	if got := firer.count(); got != 3 {
		t.Fatalf("a run that recovers on attempt 3 must stop retrying: want 3 fires, got %d", got)
	}
}

func TestRetrySequenceCountsAsOneInFlightRun(t *testing.T) {
	firer := &fakeFirer{err: errors.New("the run withered")}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"hourly": {Cron: "0 * * * *", Sequence: "seq", Retries: 1, Overlap: OverlapSkip},
	})
	sleepEntered := make(chan struct{}, 2)
	sleepRelease := make(chan struct{})
	s.sleep = func(context.Context, time.Duration) bool {
		sleepEntered <- struct{}{}
		<-sleepRelease
		return true
	}

	s.primeAll()

	// Attempt 1 withers and the run parks in its retry backoff.
	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	<-sleepEntered

	// A boundary landing mid-backoff sees the whole retry sequence as one
	// in-flight run and skips.
	*now = time.Date(2026, 7, 13, 13, 0, 5, 0, time.UTC)
	s.checkAndFire(context.Background())
	if got := firer.count(); got != 1 {
		t.Fatalf("a fire mid-backoff must be skipped: want 1 fire so far, got %d", got)
	}

	close(sleepRelease)
	s.wg.Wait()
	if got := firer.count(); got != 2 {
		t.Fatalf("want exactly the 2 attempts of one run (the 13:00 fire skipped), got %d", got)
	}
}

func TestRetryBackoffAbandonedOnShutdown(t *testing.T) {
	firer := &fakeFirer{
		err:     errors.New("the run withered"),
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	start := time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC)
	s, now := testScheduler(t, firer, start, map[string]Entry{
		"hourly": {Cron: "0 * * * *", Sequence: "seq", Retries: 5},
	})
	// Keep the real sleepCtx: with a cancelled ctx it returns false
	// immediately, so the test never waits on the wall clock.

	s.primeAll()

	ctx, cancel := context.WithCancel(context.Background())
	*now = time.Date(2026, 7, 13, 12, 0, 5, 0, time.UTC)
	s.checkAndFire(ctx)

	// Cancel the daemon context while attempt 1 is still growing, then let
	// it wither: the backoff must abandon the remaining retries at once.
	<-firer.started
	cancel()
	close(firer.release)
	s.wg.Wait()

	if got := firer.count(); got != 1 {
		t.Fatalf("shutdown during backoff must abandon retries: want 1 fire, got %d", got)
	}
}

func TestDisabledConfigAndNilSchedulerAreNoOps(t *testing.T) {
	firer := &fakeFirer{}
	cfg := Config{Enabled: false, Schedules: map[string]Entry{
		"noon": {Cron: "0 12 * * *", Sequence: "seq"},
	}}
	s := New(cfg, firer, log.New(io.Discard, "", 0))
	if len(s.entries) != 0 {
		t.Fatalf("a disabled config must produce no entries, got %d", len(s.entries))
	}
	// Start on an empty scheduler (and on nil) must not panic or spin.
	s.Start(context.Background())
	var nilSched *Scheduler
	nilSched.Start(context.Background())
	if firer.count() != 0 {
		t.Fatalf("disabled config must never fire, got %d", firer.count())
	}
}
