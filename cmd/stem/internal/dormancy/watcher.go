// Package dormancy watches growing Sprouts for signs of life and reports the
// ones that have stopped showing any.
//
// It is the event bus's first deciding consumer, and the one thing it decides
// is how loudly to talk. The doctrine it is built on: a sign of life suppresses
// suspicion; the absence of one is never itself evidence of death. Every signal
// here is a suppressor only. A forged signal costs nothing but patience, and a
// missing one costs nothing but verbosity — neither can produce a wrong ending,
// because this package produces no endings at all. Nothing in it stops a run,
// closes a session, cancels a context or touches a Terrarium, and its import
// list is asserted in the tests for exactly that reason.
//
// Deciding whether a quiet growth is stopped or merely slow is undecidable in
// general, so nothing here decides it. What it maintains instead is an accrued
// suspicion level, measured against the cadence that same run has been showing
// all along rather than against any fixed idea of how long the work should take.
package dormancy

import (
	"context"
	"sync"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// maxRetainedRuns bounds how many finished runs the readout keeps. The record
// is retained past the end of a run because the readout is usually wanted after
// the fact, but a long-lived Stem must not accumulate them without limit.
const maxRetainedRuns = 64

// RunKey identifies one growth. Every Sprout event already carries both halves
// — the step id in Source and the session in SessionID — so runs are correlated
// without any new plumbing on the publishing side.
type RunKey struct {
	Step    string
	Session string
}

// ScratchProbe actively asks what a run has changed in its workspace so far,
// returning the paths that currently differ.
//
// It is a function port rather than an imported collaborator on purpose. The
// implementation lives behind an unexported symbol in the orchestrator package;
// importing that package to reach it would drag an orchestrator dependency into
// a supervisor, and exporting it would widen a surface for one caller. The
// caller supplies a closure instead, and this package stays a leaf.
type ScratchProbe func(ctx context.Context, run RunKey) ([]string, error)

// DormancyCapture is called once per dormancy report, for the run that crossed
// the reporting level. It captures whatever the caller knows about the run's
// live state at that instant — container stderr, last request and response,
// Terrarium state, a process listing — and retains it as an artifact.
//
// It is a function port for the same reason ScratchProbe is: the capture
// implementation reaches things that live in the orchestrator package (session
// logs, Terrarium exec), and importing that package from here would pull a
// dependency across the leaf boundary this package asserts in its tests.
//
// A non-nil error from the capture is not fatal to the report: the dormancy
// event is published regardless, and "capture could not be taken" is itself
// evidence worth recording.
type DormancyCapture func(ctx context.Context, run RunKey) error

// Config wires a Watcher. Every field is optional: with none of them set the
// Watcher still accrues suspicion from observed events, it simply has nothing
// to publish to and no workspace to probe.
type Config struct {
	// Bus receives EventSproutDormant reports. A nil Bus accrues suspicion
	// silently, which is what the tests that assert on levels rather than on
	// events use.
	Bus *eventbus.Bus
	// Scratch is the active probe. Nil disables the scratch test; the two
	// passive suppressors still work.
	Scratch ScratchProbe
	// ScratchInterval is how often the probe is taken, and the tick interval
	// the production loop runs at. Zero disables the probe and the loop.
	ScratchInterval time.Duration
	// Capture is called once per dormancy report to retain evidence about the
	// silent run as an artifact. Nil disables artifact capture; the dormancy
	// report is still published to the bus either way.
	Capture DormancyCapture
	// Now supplies the current time. Nil means time.Now. Tests pass a
	// synthetic clock so a run's whole cadence can be replayed without
	// waiting for any of it.
	Now func() time.Time
}

// signalKind names one retained sign of life. These are the suppressors, and
// the list is closed: anything not on it may make a run more interesting to
// read about but may not move its suspicion.
type signalKind string

const (
	signalStream  signalKind = "stream cadence"
	signalTool    signalKind = "distinct tool call"
	signalScratch signalKind = "diff growth"
)

// suppressorKinds fixes the readout's column order so two renderings of the
// same record cannot differ.
var suppressorKinds = []signalKind{signalStream, signalTool, signalScratch}

// runRecord is everything retained about one growth. The readout renders from
// this and nothing else, so a field that is not recorded here is a claim the
// readout is not entitled to make.
type runRecord struct {
	key RunKey

	first  time.Time
	last   time.Time // the last sign of life, and so the start of the current silence
	latest time.Time // the last observation of any kind, suppressing or not

	ended     bool
	endedWith eventbus.EventType

	// detached records that the Stem stopped waiting on this run. The run is
	// still live — accrual continues — but nothing is blocking on it, which is
	// the single most useful thing the readout can say about it.
	detached   bool
	detachedAt time.Time

	cadence cadence

	// toolPrints counts how often each distinct tool call has been seen. A
	// count above one is a repeat, and a repeat is inert.
	toolPrints map[string]int

	// scratchSeen is the union of every path the probe has ever reported for
	// this run, so growth means "a path nobody has seen before" rather than
	// "the set changed", and a file reverting cannot read as progress.
	scratchSeen map[string]struct{}
	baselined   bool
	lastScratch time.Time
	scratchNote string
	probeFails  int

	counts       map[eventbus.EventType]int
	suppressions map[signalKind]int
	inertTools   int
	inertScratch int

	reported int
	// armed gates the report to once per episode of silence. A sign of life
	// re-arms it, so a run that goes quiet, wakes, and goes quiet again is
	// reported twice, while one long silence is reported once.
	armed bool
	peak  float64
}

// Watcher subscribes to a bus, accrues per-run suspicion, and reports dormancy.
type Watcher struct {
	bus             *eventbus.Bus
	scratch         ScratchProbe
	capture         DormancyCapture
	scratchInterval time.Duration
	now             func() time.Time

	mu    sync.Mutex
	runs  map[RunKey]*runRecord
	order []RunKey
}

// New builds a Watcher. It subscribes to nothing until Subscribe is called and
// probes nothing until Tick or Start runs.
func New(cfg Config) *Watcher {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Watcher{
		bus:             cfg.Bus,
		scratch:         cfg.Scratch,
		capture:         cfg.Capture,
		scratchInterval: cfg.ScratchInterval,
		now:             now,
		runs:            make(map[RunKey]*runRecord),
	}
}

// observedTypes are the event types the Watcher listens to. Stream tokens and
// tool calls are the passive suppressors; emergence starts a run's clock and
// the two terminal events stop it. Nothing here is acted on beyond recording.
var observedTypes = []eventbus.EventType{
	eventbus.EventSproutEmerged,
	eventbus.EventStreamToken,
	eventbus.EventToolInvoked,
	eventbus.EventThoughtBranch,
	eventbus.EventSproutDetached,
	eventbus.EventSproutMatured,
	eventbus.EventSproutWithered,
}

// Subscribe attaches the Watcher to its bus and returns a function that detaches
// it again. The returned function is safe to call more than once.
func (w *Watcher) Subscribe(bus *eventbus.Bus) func() {
	if w == nil || bus == nil {
		return func() {}
	}

	cancels := make([]func(), 0, len(observedTypes))
	for _, eventType := range observedTypes {
		cancels = append(cancels, bus.Subscribe(eventType, w.Observe))
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			for _, cancel := range cancels {
				cancel()
			}
		})
	}
}

// Observe folds one event into the watched run's record. It is the bus handler,
// and is exported so a caller can replay a recorded stream through it directly.
func (w *Watcher) Observe(event eventbus.Event) {
	if w == nil {
		return
	}

	key := RunKey{Step: event.Source, Session: event.SessionID}
	if key.Step == "" && key.Session == "" {
		return
	}

	at := event.Timestamp
	if at.IsZero() {
		at = w.now()
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	record := w.recordLocked(key, at)
	record.counts[event.Type]++
	if at.After(record.latest) {
		record.latest = at
	}

	switch event.Type {
	case eventbus.EventSproutEmerged:
		// Emergence is not a sign of progress, but it is the moment from which
		// silence starts being measurable at all. Without it the first silence
		// would be measured from whenever the process happened to notice.
		if record.last.IsZero() {
			record.last = at
		}
	case eventbus.EventStreamToken:
		// The cheapest signal and the most forgeable: a loop emitting tokens
		// suppresses forever. That is tolerable precisely because suppression
		// cannot cause a wrong ending — a forged stream buys patience, and
		// patience is all it can buy.
		w.signOfLifeLocked(record, at, signalStream)
	case eventbus.EventToolInvoked:
		// The asymmetry the whole design rests on. A tool call nobody has seen
		// before is progress and suppresses. The same call repeated is a run
		// going in circles, so it does not suppress — and it does not raise
		// suspicion either, because "not evidence of life" is not "evidence of
		// death". It is recorded and otherwise inert: no clock reset, no gap
		// folded into the cadence, nothing.
		if w.distinctToolLocked(record, event) {
			w.signOfLifeLocked(record, at, signalTool)
		} else {
			record.inertTools++
		}
	case eventbus.EventThoughtBranch:
		// Retained for the readout, deliberately not a suppressor. A thought
		// is emitted from a completed model turn, which has already streamed
		// its tokens, so it suppresses nothing the stream did not already
		// suppress — and on its own it is as repeatable as a repeated tool
		// call, without a fingerprint established as comparable. Admitting it
		// would add a forgeable suppressor for no signal.
	case eventbus.EventSproutDetached:
		// The Stem stopped waiting; the Sprout did not stop growing. This is
		// emphatically NOT an ending, and treating it as one would blind the
		// watcher to the only run nobody else is looking at.
		//
		// It is equally not a signal about the Sprout. The Stem running out of
		// patience is a fact about the Stem: it neither suppresses (nothing
		// about the work became more alive) nor accelerates (nothing about the
		// work became more dead). So it is retained for the readout — which run
		// is unattended is worth knowing — and is inert against suspicion, which
		// keeps accruing from the same cadence it was accruing from before.
		record.detached = true
		record.detachedAt = at
	case eventbus.EventSproutMatured, eventbus.EventSproutWithered:
		// A run that has ended cannot be dormant. The record is kept for the
		// readout; the accrual stops.
		record.ended = true
		record.endedWith = event.Type
	}
}

// distinctToolLocked reports whether this tool call has been seen before on this
// run, fingerprinting on the tool name together with its arguments — both of
// which the event already carries, so distinctness needs no new plumbing.
func (w *Watcher) distinctToolLocked(record *runRecord, event eventbus.Event) bool {
	print := toolFingerprint(event)
	seen := record.toolPrints[print]
	record.toolPrints[print] = seen + 1
	return seen == 0
}

// signOfLifeLocked records one suppressor: it folds the gap since the previous
// sign into the run's own cadence, restarts the silence, and re-arms reporting.
func (w *Watcher) signOfLifeLocked(record *runRecord, at time.Time, kind signalKind) {
	record.suppressions[kind]++
	record.armed = true

	if record.last.IsZero() {
		record.last = at
		return
	}
	if !at.After(record.last) {
		// Out-of-order or same-instant arrivals say nothing about pacing and
		// must never wind the silence backwards.
		return
	}

	record.cadence.observe(at.Sub(record.last))
	record.last = at
}

// recordLocked returns the record for a run, creating it on first sight.
func (w *Watcher) recordLocked(key RunKey, at time.Time) *runRecord {
	if record := w.runs[key]; record != nil {
		return record
	}

	record := &runRecord{
		key:          key,
		first:        at,
		latest:       at,
		toolPrints:   make(map[string]int),
		scratchSeen:  make(map[string]struct{}),
		counts:       make(map[eventbus.EventType]int),
		suppressions: make(map[signalKind]int),
		armed:        true,
	}
	w.runs[key] = record
	w.order = append(w.order, key)
	w.evictLocked()
	return record
}

// evictLocked drops the oldest ENDED runs once the retention bound is passed.
// A live run is never evicted: forgetting one would silently stop watching it.
func (w *Watcher) evictLocked() {
	for len(w.order) > maxRetainedRuns {
		evicted := -1
		for i, key := range w.order {
			if record := w.runs[key]; record != nil && record.ended {
				evicted = i
				break
			}
		}
		if evicted < 0 {
			return
		}
		delete(w.runs, w.order[evicted])
		w.order = append(w.order[:evicted], w.order[evicted+1:]...)
	}
}

// Tick samples the scratch probe where it is due and then evaluates every live
// run's suspicion, publishing a report for any that has newly crossed the
// reporting level. It is the whole of the Watcher's periodic work, and it takes
// the time as an argument so a test can replay hours of a run without waiting
// for any of it.
func (w *Watcher) Tick(ctx context.Context, now time.Time) {
	if w == nil {
		return
	}
	if now.IsZero() {
		now = w.now()
	}

	w.sampleScratch(ctx, now)
	w.evaluate(now)
}

// sampleScratch takes the active probe for every run whose interval has elapsed.
// The first sample of a run is a baseline and never suppresses: whatever already
// differed in the workspace when watching started is not progress this run made.
func (w *Watcher) sampleScratch(ctx context.Context, now time.Time) {
	if w.scratch == nil || w.scratchInterval <= 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	w.mu.Lock()
	due := make([]RunKey, 0, len(w.order))
	for _, key := range w.order {
		record := w.runs[key]
		if record == nil || record.ended {
			continue
		}
		if !record.lastScratch.IsZero() && now.Sub(record.lastScratch) < w.scratchInterval {
			continue
		}
		due = append(due, key)
	}
	w.mu.Unlock()

	// The probe reaches outside this package, so it runs without the lock held.
	for _, key := range due {
		paths, err := w.scratch(ctx, key)

		w.mu.Lock()
		record := w.runs[key]
		if record == nil {
			w.mu.Unlock()
			continue
		}
		record.lastScratch = now

		switch {
		case err != nil:
			// A probe that could not answer is not an answer. It is recorded
			// as unmeasured so the readout can say "unknown" rather than
			// implying the diff stood still, and it is otherwise inert — a
			// failed measurement is not evidence about the run.
			record.scratchNote = err.Error()
			record.probeFails++
		case !record.baselined:
			record.baselined = true
			for _, path := range paths {
				record.scratchSeen[path] = struct{}{}
			}
			record.scratchNote = ""
		default:
			record.scratchNote = ""
			grew := false
			for _, path := range paths {
				if _, seen := record.scratchSeen[path]; !seen {
					record.scratchSeen[path] = struct{}{}
					grew = true
				}
			}
			if grew {
				w.signOfLifeLocked(record, now, signalScratch)
			} else {
				// A static diff proves nothing. The growth may be reading, or
				// thinking, or wedged, and this probe cannot tell them apart —
				// so it stays inert rather than accelerating anything.
				record.inertScratch++
			}
		}
		w.mu.Unlock()
	}
}

// evaluate recomputes every live run's suspicion and publishes a report for each
// one that has newly crossed the reporting level. Publishing happens outside the
// lock so a handler on the far side of the bus can never deadlock the Watcher.
func (w *Watcher) evaluate(now time.Time) {
	type dormantRun struct {
		key   RunKey
		event eventbus.Event
	}
	var dormant []dormantRun

	w.mu.Lock()
	for _, key := range w.order {
		record := w.runs[key]
		if record == nil || record.ended || record.last.IsZero() {
			continue
		}

		level := record.suspicionAt(now)
		if level > record.peak {
			record.peak = level
		}
		if level < reportingSuspicion || !record.armed {
			continue
		}

		record.armed = false
		record.reported++
		dormant = append(dormant, dormantRun{key: key, event: record.dormantEvent(now, level)})
	}
	w.mu.Unlock()

	for _, d := range dormant {
		// Capture runs BEFORE the report is published, so a subscriber handling
		// the event finds the evidence already on disk. Bus.Publish invokes
		// handlers synchronously on this goroutine, so capturing afterwards
		// would guarantee every subscriber saw the report before the artifact
		// existed — the opposite of what a report about a silent run is for.
		//
		// The cost is that the report waits on the capture, bounded by the
		// implementation's own timeout. A run that has already been silent for
		// several times its own widest gap is not harmed by that wait, and a
		// report nobody can find evidence for is worth less than a late one.
		//
		// Errors are deliberately not fatal to the report: "the capture could
		// not be taken" is itself evidence, recorded by the implementation, and
		// suppressing the report as well would lose both.
		if w.capture != nil {
			// context.Background is correct here: the run's work context may be
			// closing (that is the ordinary way a run ends), and a capture
			// issued on an already-expired context would always fail.
			w.capture(context.Background(), d.key) //nolint:errcheck
		}
		w.bus.Publish(d.event)
	}
}

// Start runs Tick on the configured interval until the context is done or the
// returned stop function is called. It is the production loop; tests drive Tick
// directly instead, so no test depends on wall-clock time passing.
func (w *Watcher) Start(ctx context.Context) func() {
	if w == nil || w.scratchInterval <= 0 {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(w.scratchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				w.Tick(ctx, w.now())
			}
		}
	}()

	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}

// Suspicion reports one run's accrued suspicion as of the given instant, in
// envelopes of silence beyond what that run has shown itself capable of. Zero
// for an unknown run, and zero for a run behaving as it always has.
func (w *Watcher) Suspicion(run RunKey, at time.Time) float64 {
	if w == nil {
		return 0
	}
	if at.IsZero() {
		at = w.now()
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	record := w.runs[run]
	if record == nil || record.last.IsZero() {
		return 0
	}
	return record.suspicionAt(at)
}

// Reported returns how many dormancy reports the given run has produced.
func (w *Watcher) Reported(run RunKey) int {
	if w == nil {
		return 0
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if record := w.runs[run]; record != nil {
		return record.reported
	}
	return 0
}

// ReportedAny reports whether any watched run has gone dormant. It is what a
// caller consults to decide whether the readout is worth printing at all.
func (w *Watcher) ReportedAny() bool {
	if w == nil {
		return false
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, record := range w.runs {
		if record.reported > 0 {
			return true
		}
	}
	return false
}

// suspicionAt measures the current silence against this run's own envelope.
func (r *runRecord) suspicionAt(now time.Time) float64 {
	envelope, _ := r.cadence.envelope()
	return suspicionFor(now.Sub(r.last), envelope)
}

// dormantEvent builds the report. Every field in it is read back from the
// retained record, including whether the envelope was learned or borrowed from
// the cold start, because a level means something different in each case.
func (r *runRecord) dormantEvent(now time.Time, level float64) eventbus.Event {
	envelope, learned := r.cadence.envelope()

	return eventbus.Event{
		Type:      eventbus.EventSproutDormant,
		Timestamp: now,
		Source:    r.key.Step,
		SessionID: r.key.Session,
		Data: map[string]interface{}{
			"stepId":          r.key.Step,
			"suspicion":       level,
			"silence":         now.Sub(r.last).String(),
			"cadenceEnvelope": envelope.String(),
			"cadenceLearned":  learned,
			"cadenceSamples":  r.cadence.count,
			"streamSignals":   r.suppressions[signalStream],
			"toolSignals":     r.suppressions[signalTool],
			"diffSignals":     r.suppressions[signalScratch],
			"repeatedTools":   r.inertTools,
			"staticDiffs":     r.inertScratch,
			"probeUnmeasured": r.probeFails,
			// Whether anything is still waiting on this run. A dormancy report
			// about an unattended run is a different thing to read than one
			// about a run somebody is blocked on, and only the record knows.
			"detached": r.detached,
		},
	}
}
