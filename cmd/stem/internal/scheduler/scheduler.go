package scheduler

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"
)

// Firer is the seam through which the scheduler grows a due entry. The serve
// layer provides the concrete implementation (Hormonal Trigger evaluation plus
// the governed sequence.run / sprout.run capabilities); this package stays
// decoupled from the Core and the event bus.
type Firer interface {
	Fire(ctx context.Context, name string, e Entry) error
}

// FirerFunc adapts a plain function to the Firer interface.
type FirerFunc func(ctx context.Context, name string, e Entry) error

// Fire implements Firer.
func (f FirerFunc) Fire(ctx context.Context, name string, e Entry) error {
	return f(ctx, name, e)
}

// defaultTickInterval is how often the scheduler checks for due entries.
// Cron has minute resolution, so a ~30s tick fires every entry within its
// scheduled minute without busy-polling.
const defaultTickInterval = 30 * time.Second

// retryBackoff is the fixed pause between re-growing a withered run. The
// scheduler-level retry is a coarse whole-run safety net (the sequence engine
// has its own per-step onFailure/maxRetries), so a small constant backoff is
// deliberate — no exponential cleverness.
const retryBackoff = 5 * time.Second

// scheduledEntry is one schedule plus its parsed cron and runtime state. The
// runtime fields (next, disabled) are only touched by the ticker goroutine
// (or, in tests, the single goroutine driving checkAndFire), so they need no
// locking; only the in-flight map is shared with fire goroutines.
type scheduledEntry struct {
	name     string
	entry    Entry
	schedule Schedule
	next     time.Time
	disabled bool
}

// Scheduler runs the in-process ticker loop that grows due schedule entries
// through the injected Firer.
type Scheduler struct {
	firer  Firer
	logger *log.Logger
	tick   time.Duration

	// now is injectable so tests can drive the loop deterministically.
	now func() time.Time

	// sleep waits out the retry backoff (or returns false when ctx is done
	// first). Injectable so retry tests never wait on the wall clock.
	sleep func(ctx context.Context, d time.Duration) bool

	entries []*scheduledEntry

	// mu guards inFlight and pending. inFlight is the per-entry
	// one-run-at-a-time latch: concurrent runs of the same sequence corrupt
	// its YAML step-state, so a fire that lands while the previous run is
	// still growing is skipped (overlap: skip) or queued (overlap: queue).
	// pending holds at most ONE queued run per entry — fires that land while
	// a run is already pending coalesce into it, so a cron firing faster
	// than runs complete never builds a backlog.
	mu       sync.Mutex
	inFlight map[string]bool
	pending  map[string]bool

	// wg tracks fire goroutines so tests (and shutdown diagnostics) can wait
	// for outstanding runs.
	wg sync.WaitGroup
}

// New builds a Scheduler over the loaded config. Entries are walked in sorted
// name order so logs and fire order are deterministic. A disabled config (or
// one with no schedules) yields a Scheduler whose Start is a no-op.
func New(cfg Config, firer Firer, logger *log.Logger) *Scheduler {
	if logger == nil {
		logger = log.Default()
	}
	s := &Scheduler{
		firer:    firer,
		logger:   logger,
		tick:     defaultTickInterval,
		now:      time.Now,
		sleep:    sleepCtx,
		inFlight: make(map[string]bool),
		pending:  make(map[string]bool),
	}
	if !cfg.Enabled {
		return s
	}

	names := make([]string, 0, len(cfg.Schedules))
	for name := range cfg.Schedules {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := cfg.Schedules[name]
		schedule, err := Parse(entry.Cron)
		if err != nil {
			// LoadConfig validates every cron up front, so this only guards
			// hand-built configs; skip rather than crash the daemon.
			logger.Printf("⚠️ Schedule %q: invalid cron %q: %v (entry ignored)", name, entry.Cron, err)
			continue
		}
		s.entries = append(s.entries, &scheduledEntry{
			name:     name,
			entry:    entry,
			schedule: schedule,
		})
	}
	return s
}

// Start primes each entry's next fire time and launches the ticker goroutine.
// The loop stops when ctx is cancelled, so wiring it to the daemon's shutdown
// context stops scheduling with the daemon.
func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.firer == nil || len(s.entries) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.primeAll()

	go func() {
		ticker := time.NewTicker(s.tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.checkAndFire(ctx)
			}
		}
	}()
}

// primeAll computes every entry's first fire time, disabling entries whose
// cron never matches a real date (e.g. February 30th).
func (s *Scheduler) primeAll() {
	now := s.now()
	for _, entry := range s.entries {
		s.advance(entry, now)
	}
}

// advance recomputes an entry's next fire time strictly after now. A zero
// Next means the spec can never fire again: log once and disable the entry
// rather than re-scanning five years of calendar on every tick.
func (s *Scheduler) advance(entry *scheduledEntry, now time.Time) {
	entry.next = entry.schedule.Next(now)
	if entry.next.IsZero() {
		entry.disabled = true
		s.logger.Printf("⚠️ Schedule %q: cron %q has no future fire time; disabling this entry", entry.name, entry.entry.Cron)
	}
}

// checkAndFire is one tick of the loop: every enabled entry whose fire time
// has arrived is grown (or skipped if its previous run is still going).
func (s *Scheduler) checkAndFire(ctx context.Context) {
	now := s.now()
	for _, entry := range s.entries {
		if entry.disabled || entry.next.IsZero() || now.Before(entry.next) {
			continue
		}
		// Advance before firing so a long run doesn't re-fire on the next
		// tick — the entry only becomes due again at its next cron boundary.
		s.advance(entry, now)
		s.fire(ctx, entry)
	}
}

// fire grows one due entry on its own goroutine, honoring the entry's overlap
// policy. With overlap: skip, a fire that lands while the previous run is
// still growing is dropped; with overlap: queue, it is remembered (capped at
// one pending run — extra fires coalesce) and grown as soon as the in-flight
// run finishes.
func (s *Scheduler) fire(ctx context.Context, entry *scheduledEntry) {
	s.mu.Lock()
	if s.inFlight[entry.name] {
		if entry.entry.Overlap != OverlapQueue {
			s.mu.Unlock()
			s.logger.Printf("⏭️ Schedule %q: previous run still growing; skipping this fire (overlap: %s)", entry.name, OverlapSkip)
			return
		}
		if s.pending[entry.name] {
			s.mu.Unlock()
			s.logger.Printf("⏳ Schedule %q: previous run still growing and one run is already queued; coalescing this fire into it (overlap: %s)", entry.name, OverlapQueue)
			return
		}
		s.pending[entry.name] = true
		s.mu.Unlock()
		s.logger.Printf("⏳ Schedule %q: previous run still growing; queueing one run to grow when it finishes (overlap: %s)", entry.name, OverlapQueue)
		return
	}
	s.inFlight[entry.name] = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			s.growOnce(ctx, entry)

			// The run (including its whole retry sequence) has finished.
			// Drain at most one queued fire before releasing the latch.
			s.mu.Lock()
			if s.pending[entry.name] && ctx.Err() == nil {
				delete(s.pending, entry.name)
				s.mu.Unlock()
				s.logger.Printf("⏰ Schedule %q: previous run finished; growing the queued run now", entry.name)
				continue
			}
			delete(s.pending, entry.name)
			delete(s.inFlight, entry.name)
			s.mu.Unlock()
			return
		}
	}()
}

// growOnce grows one scheduled run, re-attempting a withered run up to
// entry.Retries additional times with a fixed backoff between attempts. The
// whole retry sequence counts as ONE in-flight run for overlap purposes — the
// inFlight latch stays held across every attempt. A firer error is the run
// withering, not the loop dying: log it and keep ticking.
func (s *Scheduler) growOnce(ctx context.Context, entry *scheduledEntry) {
	attempts := 1 + entry.entry.Retries
	for attempt := 1; attempt <= attempts; attempt++ {
		err := s.firer.Fire(ctx, entry.name, entry.entry)
		if err == nil {
			if attempt > 1 {
				s.logger.Printf("✅ Schedule %q: scheduled run recovered on retry (attempt %d of %d)", entry.name, attempt, attempts)
			}
			return
		}
		if attempt == attempts {
			if attempts > 1 {
				s.logger.Printf("❌ Schedule %q: scheduled run withered on all %d attempts; giving up until the next cron fire: %v", entry.name, attempts, err)
			} else {
				s.logger.Printf("❌ Schedule %q: scheduled run withered: %v", entry.name, err)
			}
			return
		}
		s.logger.Printf("🔁 Schedule %q: scheduled run withered (attempt %d of %d); re-growing in %s: %v", entry.name, attempt, attempts, retryBackoff, err)
		if !s.sleep(ctx, retryBackoff) {
			s.logger.Printf("⏹️ Schedule %q: shutdown during retry backoff; abandoning the remaining attempts", entry.name)
			return
		}
	}
}

// sleepCtx waits d, returning false early if ctx is done first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
