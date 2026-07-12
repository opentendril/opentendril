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
	mu      sync.Mutex
	fires   []string
	err     error
	started chan struct{} // closed/sent when a fire begins, if non-nil
	release chan struct{} // fire blocks until this closes, if non-nil
}

func (f *fakeFirer) Fire(_ context.Context, name string, _ Entry) error {
	f.mu.Lock()
	f.fires = append(f.fires, name)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	return f.err
}

func (f *fakeFirer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fires)
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
