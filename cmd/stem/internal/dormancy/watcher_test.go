package dormancy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// origin is the synthetic clock's zero. Every test drives time by handing an
// explicit instant to Observe and Tick, so a whole run's cadence replays with no
// wall-clock time passing and no sleep anywhere in this file. A test that waited
// for its own configured intervals would be slower than the behaviour it proves
// and would still prove less.
var origin = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var probeRun = RunKey{Step: "step-probe", Session: "session-probe"}

// at returns an instant offset from the synthetic origin.
func at(offset time.Duration) time.Time { return origin.Add(offset) }

func streamAt(run RunKey, when time.Time) eventbus.Event {
	return eventbus.Event{
		Type:      eventbus.EventStreamToken,
		Timestamp: when,
		Source:    run.Step,
		SessionID: run.Session,
		Data:      map[string]interface{}{"token": "x"},
	}
}

func toolAt(run RunKey, when time.Time, tool string, arguments map[string]any) eventbus.Event {
	return eventbus.Event{
		Type:      eventbus.EventToolInvoked,
		Timestamp: when,
		Source:    run.Step,
		SessionID: run.Session,
		Data: map[string]interface{}{
			"tool":      tool,
			"arguments": arguments,
			"status":    "ok",
		},
	}
}

// recorder captures everything published on a bus, so a negative assertion can
// be written as a count over what was published rather than as the absence of a
// failure.
type recorder struct {
	events []eventbus.Event
}

func recordAll(bus *eventbus.Bus) *recorder {
	rec := &recorder{}
	for _, eventType := range eventbus.AllEventTypes() {
		bus.Subscribe(eventType, func(event eventbus.Event) {
			rec.events = append(rec.events, event)
		})
	}
	return rec
}

func (r *recorder) count(eventType eventbus.EventType) int {
	total := 0
	for _, event := range r.events {
		if event.Type == eventType {
			total++
		}
	}
	return total
}

// steadyStream drives count stream tokens spaced one gap apart starting at the
// given offset, and returns the instant of the last one.
func steadyStream(w *Watcher, run RunKey, start time.Time, gap time.Duration, count int) time.Time {
	last := start
	for i := 0; i < count; i++ {
		last = start.Add(time.Duration(i) * gap)
		w.Observe(streamAt(run, last))
	}
	return last
}

// TestSteadyStreamKeepsSuspicionAtZero pins the first half of the doctrine: a
// run behaving the way it has behaved all along is not suspicious, and it does
// not become suspicious merely because time is passing.
func TestSteadyStreamKeepsSuspicionAtZero(t *testing.T) {
	watcher := New(Config{Now: func() time.Time { return at(0) }})

	// Twenty gaps of one second. Suspicion is checked at every arrival, so a
	// version that raised it unconditionally cannot reach the end of the loop.
	for i := 0; i < 20; i++ {
		when := at(time.Duration(i) * time.Second)
		watcher.Observe(streamAt(probeRun, when))
		if level := watcher.Suspicion(probeRun, when); level != 0 {
			t.Fatalf("suspicion after a steady token at %s = %v, want 0", when.Sub(origin), level)
		}
	}

	// And still zero a full gap after the last token: the next one is not late
	// yet, so nothing has happened worth noticing.
	if level := watcher.Suspicion(probeRun, at(20*time.Second)); level != 0 {
		t.Fatalf("suspicion one gap after the last token = %v, want 0", level)
	}
}

// TestSilenceRaisesSuspicionAndReportsDormancy drives the accrual end to end:
// nothing while the stream flows, a rise once it stops, and exactly one report
// per episode of silence.
func TestSilenceRaisesSuspicionAndReportsDormancy(t *testing.T) {
	bus := eventbus.New()
	rec := recordAll(bus)
	watcher := New(Config{Bus: bus, Now: func() time.Time { return at(0) }})

	last := steadyStream(watcher, probeRun, at(0), time.Second, 10)

	// While suppressors are arriving, ticking must not report anything. A
	// detector that raised suspicion unconditionally would trip here.
	watcher.Tick(context.Background(), last)
	if got := rec.count(eventbus.EventSproutDormant); got != 0 {
		t.Fatalf("dormancy reported %d time(s) while the stream was flowing, want 0", got)
	}

	// Inside the envelope: rising is not yet reportable.
	watcher.Tick(context.Background(), last.Add(3*time.Second))
	if level := watcher.Suspicion(probeRun, last.Add(3*time.Second)); level <= 0 {
		t.Fatalf("suspicion three gaps into the silence = %v, want above zero", level)
	}
	if got := rec.count(eventbus.EventSproutDormant); got != 0 {
		t.Fatalf("dormancy reported %d time(s) before the reporting level, want 0", got)
	}

	// Past the reporting level: exactly one report.
	watcher.Tick(context.Background(), last.Add(30*time.Second))
	if got := rec.count(eventbus.EventSproutDormant); got != 1 {
		t.Fatalf("dormancy reported %d time(s) after crossing, want exactly 1", got)
	}

	// Still silent, and still exactly one: a report is per episode, not per
	// tick. A test that only asserted "eventually reported" would pass against
	// an implementation that reported on every tick forever.
	watcher.Tick(context.Background(), last.Add(60*time.Second))
	watcher.Tick(context.Background(), last.Add(90*time.Second))
	if got := rec.count(eventbus.EventSproutDormant); got != 1 {
		t.Fatalf("dormancy reported %d time(s) across a single silence, want exactly 1", got)
	}

	report := rec.events[0]
	if report.Source != probeRun.Step || report.SessionID != probeRun.Session {
		t.Fatalf("report is not correlated to the run: source %q session %q", report.Source, report.SessionID)
	}
	if learned, ok := report.Data["cadenceLearned"].(bool); !ok || !learned {
		t.Fatalf("report claims cadenceLearned = %v; a run with ten gaps must be judged on its own cadence", report.Data["cadenceLearned"])
	}

	// A sign of life re-arms reporting, so a second episode reports again.
	wake := last.Add(120 * time.Second)
	watcher.Observe(streamAt(probeRun, wake))
	if level := watcher.Suspicion(probeRun, wake); level != 0 {
		t.Fatalf("suspicion immediately after a sign of life = %v, want 0", level)
	}
	watcher.Tick(context.Background(), wake.Add(2*time.Hour))
	if got := rec.count(eventbus.EventSproutDormant); got != 2 {
		t.Fatalf("dormancy reported %d time(s) across two episodes, want exactly 2", got)
	}
}

// suspicionAfterRepeat is the shared drive for the asymmetry test. Every watcher
// gets the identical prelude; what differs is the single event handed to it at
// the ten-second mark.
func suspicionAfterRepeat(t *testing.T, interject func(w *Watcher, run RunKey, when time.Time)) float64 {
	t.Helper()

	watcher := New(Config{Now: func() time.Time { return at(0) }})
	run := RunKey{Step: "step-asymmetry", Session: "session-asymmetry"}

	steadyStream(watcher, run, at(0), time.Second, 4)
	// One distinct call, so a later identical one is genuinely a repeat.
	watcher.Observe(toolAt(run, at(4*time.Second), "read_file", map[string]any{"path": "main.go"}))

	if interject != nil {
		interject(watcher, run, at(10*time.Second))
	}

	return watcher.Suspicion(run, at(20*time.Second))
}

// TestRepeatedToolCallNeitherSuppressesNorAccelerates is the asymmetry the whole
// doctrine rests on. An identical repeat is not a sign of life, so it must not
// lower suspicion — and "not evidence of life" is not "evidence of death", so it
// must not raise it either. The only correct behaviour is total inertness, which
// is checked as exact equality against a run where the event never arrived.
//
// The distinct-call arm is not decoration. Without it, equality would also hold
// for an implementation that ignored tool events entirely, and the test would be
// asserting nothing.
func TestRepeatedToolCallNeitherSuppressesNorAccelerates(t *testing.T) {
	silent := suspicionAfterRepeat(t, nil)
	if silent <= 0 {
		t.Fatalf("baseline suspicion = %v; the comparison is meaningless unless suspicion has accrued", silent)
	}

	repeated := suspicionAfterRepeat(t, func(w *Watcher, run RunKey, when time.Time) {
		w.Observe(toolAt(run, when, "read_file", map[string]any{"path": "main.go"}))
	})
	if repeated != silent {
		t.Fatalf("suspicion after an identical repeat = %v, want exactly the %v of a run where nothing arrived", repeated, silent)
	}

	distinct := suspicionAfterRepeat(t, func(w *Watcher, run RunKey, when time.Time) {
		w.Observe(toolAt(run, when, "write_file", map[string]any{"path": "main.go"}))
	})
	if distinct >= silent {
		t.Fatalf("suspicion after a DISTINCT call = %v, want below the %v of a run where nothing arrived", distinct, silent)
	}
}

// TestDistinctToolCallSuppresses states the other half directly: a call nobody
// has seen before is progress and restarts the silence.
func TestDistinctToolCallSuppresses(t *testing.T) {
	watcher := New(Config{Now: func() time.Time { return at(0) }})
	run := RunKey{Step: "step-distinct", Session: "session-distinct"}

	steadyStream(watcher, run, at(0), time.Second, 4)

	if level := watcher.Suspicion(run, at(30*time.Second)); level <= 0 {
		t.Fatalf("suspicion before the call = %v, want above zero", level)
	}

	watcher.Observe(toolAt(run, at(30*time.Second), "run_command", map[string]any{"cmd": "go build"}))
	if level := watcher.Suspicion(run, at(30*time.Second)); level != 0 {
		t.Fatalf("suspicion at the instant of a distinct call = %v, want 0", level)
	}
}

// TestDiffGrowthSuppressesAndStaticDiffIsInert covers the injected probe. A
// workspace that gained a file it never had is progress; one that did not is not
// evidence of anything, and must move nothing in either direction.
func TestDiffGrowthSuppressesAndStaticDiffIsInert(t *testing.T) {
	run := RunKey{Step: "step-scratch", Session: "session-scratch"}

	// drive replays the same prelude and probe schedule against a probe whose
	// answers the caller chooses, returning the suspicion at the end.
	drive := func(answers [][]string) float64 {
		index := 0
		watcher := New(Config{
			ScratchInterval: 10 * time.Second,
			Now:             func() time.Time { return at(0) },
			Scratch: func(_ context.Context, _ RunKey) ([]string, error) {
				answer := answers[index]
				if index < len(answers)-1 {
					index++
				}
				return answer, nil
			},
		})

		steadyStream(watcher, run, at(0), time.Second, 4)
		// The first sample is a baseline, the second and third are the ones
		// that can carry growth.
		watcher.Tick(context.Background(), at(10*time.Second))
		watcher.Tick(context.Background(), at(20*time.Second))
		watcher.Tick(context.Background(), at(30*time.Second))
		return watcher.Suspicion(run, at(40*time.Second))
	}

	static := drive([][]string{{"a.go"}, {"a.go"}, {"a.go"}})
	if static <= 0 {
		t.Fatalf("suspicion under a static diff = %v; a diff that stood still must not suppress", static)
	}

	growing := drive([][]string{{"a.go"}, {"a.go", "b.go"}, {"a.go", "b.go", "c.go"}})
	if growing >= static {
		t.Fatalf("suspicion under a growing diff = %v, want below the %v of a static one", growing, static)
	}

	// A probe that could not answer is not an answer: it must leave suspicion
	// exactly where a static diff left it rather than counting as either.
	failingIndex := 0
	failing := New(Config{
		ScratchInterval: 10 * time.Second,
		Now:             func() time.Time { return at(0) },
		Scratch: func(_ context.Context, _ RunKey) ([]string, error) {
			failingIndex++
			return nil, errors.New("git status --porcelain failed: context deadline exceeded")
		},
	})
	steadyStream(failing, run, at(0), time.Second, 4)
	failing.Tick(context.Background(), at(10*time.Second))
	failing.Tick(context.Background(), at(20*time.Second))
	failing.Tick(context.Background(), at(30*time.Second))
	if failingIndex == 0 {
		t.Fatal("the probe was never called; nothing was measured")
	}
	if level := failing.Suspicion(run, at(40*time.Second)); level != static {
		t.Fatalf("suspicion under an unmeasurable diff = %v, want the %v of a static one", level, static)
	}
}

// TestSubscribeCorrelatesEventsByStepAndSession proves the bus wiring is real:
// events published normally reach the detector and land against the right run.
func TestSubscribeCorrelatesEventsByStepAndSession(t *testing.T) {
	bus := eventbus.New()
	watcher := New(Config{Bus: bus, Now: func() time.Time { return at(0) }})
	detach := watcher.Subscribe(bus)

	one := RunKey{Step: "step-one", Session: "session-one"}
	two := RunKey{Step: "step-two", Session: "session-two"}

	steadyStream(watcher, one, at(0), time.Second, 4)
	for i := 0; i < 4; i++ {
		bus.Publish(streamAt(two, at(time.Duration(i)*time.Minute)))
	}

	// The two runs learned different cadences, so the same silence means
	// different things to each. Equal levels would mean the correlation
	// collapsed them into one record.
	levelOne := watcher.Suspicion(one, at(10*time.Minute))
	levelTwo := watcher.Suspicion(two, at(10*time.Minute))
	if levelOne <= levelTwo {
		t.Fatalf("suspicion for the fast run (%v) is not above the slow run's (%v); the events were not correlated per run", levelOne, levelTwo)
	}

	detach()
	if got := bus.HandlerCount(eventbus.EventStreamToken); got != 0 {
		t.Fatalf("stream-token handlers after detach = %d, want 0", got)
	}
}

// TestWatcherPublishesOnlyDormancyReports is the containment assertion. The
// detector is driven directly, so every event that appears on the bus was put
// there by the watcher, and the count is taken over all of them rather than over
// the one type the test hopes to see.
func TestWatcherPublishesOnlyDormancyReports(t *testing.T) {
	bus := eventbus.New()
	rec := recordAll(bus)
	watcher := New(Config{Bus: bus, Now: func() time.Time { return at(0) }})

	last := steadyStream(watcher, probeRun, at(0), time.Second, 6)
	watcher.Observe(toolAt(probeRun, last.Add(time.Second), "read_file", map[string]any{"path": "a.go"}))
	watcher.Observe(eventbus.Event{Type: eventbus.EventThoughtBranch, Timestamp: last.Add(2 * time.Second), Source: probeRun.Step, SessionID: probeRun.Session})
	watcher.Tick(context.Background(), last.Add(time.Hour))
	watcher.Observe(eventbus.Event{Type: eventbus.EventSproutWithered, Timestamp: last.Add(2 * time.Hour), Source: probeRun.Step, SessionID: probeRun.Session})
	watcher.Tick(context.Background(), last.Add(3*time.Hour))

	if len(rec.events) == 0 {
		t.Fatal("nothing was published at all; the assertion below would pass vacuously")
	}
	for _, event := range rec.events {
		if event.Type != eventbus.EventSproutDormant {
			t.Fatalf("the watcher published a %q event; it may only ever report dormancy", event.Type)
		}
	}
}

// TestEndedRunStopsAccruing states that a run which has already reported its own
// end cannot then be reported dormant. Dormancy is about a run nobody can see
// progress from, not about one that finished.
func TestEndedRunStopsAccruing(t *testing.T) {
	bus := eventbus.New()
	rec := recordAll(bus)
	watcher := New(Config{Bus: bus, Now: func() time.Time { return at(0) }})

	last := steadyStream(watcher, probeRun, at(0), time.Second, 6)
	watcher.Observe(eventbus.Event{Type: eventbus.EventSproutMatured, Timestamp: last, Source: probeRun.Step, SessionID: probeRun.Session})
	watcher.Tick(context.Background(), last.Add(time.Hour))

	if got := rec.count(eventbus.EventSproutDormant); got != 0 {
		t.Fatalf("a matured run was reported dormant %d time(s), want 0", got)
	}
}

// detachedAt builds the event the Stem publishes when it stops waiting.
func detachedAt(run RunKey, when time.Time) eventbus.Event {
	return eventbus.Event{
		Type:      eventbus.EventSproutDetached,
		Timestamp: when,
		Source:    run.Step,
		SessionID: run.Session,
		Data:      map[string]interface{}{"growthBudget": "20m"},
	}
}

// TestDetachedRunIsStillWatched covers the interaction with detachment. The Stem
// stopping its wait is a fact about the Stem, not about the Sprout: the run is
// still growing, so it must still be watched — and it is the run nobody else is
// looking at, so going blind here would be the worst possible moment to.
func TestDetachedRunIsStillWatched(t *testing.T) {
	bus := eventbus.New()
	rec := recordAll(bus)
	watcher := New(Config{Bus: bus, Now: func() time.Time { return at(0) }})

	last := steadyStream(watcher, probeRun, at(0), time.Second, 6)
	watcher.Observe(detachedAt(probeRun, last.Add(time.Second)))
	watcher.Tick(context.Background(), last.Add(time.Hour))

	if got := rec.count(eventbus.EventSproutDormant); got != 1 {
		t.Fatalf("a detached run was reported dormant %d time(s), want 1; detachment is not an ending and must not stop the watch", got)
	}
	if detached, ok := rec.events[0].Data["detached"].(bool); !ok || !detached {
		t.Fatalf("the report does not record that nothing is waiting on the run: %#v", rec.events[0].Data["detached"])
	}
}

// TestDetachmentNeitherSuppressesNorAccelerates holds detachment to the same
// asymmetry as a repeated tool call, for the same reason: the Stem running out
// of patience says nothing about whether the work is progressing, so it may move
// suspicion in neither direction.
func TestDetachmentNeitherSuppressesNorAccelerates(t *testing.T) {
	drive := func(interject bool) float64 {
		watcher := New(Config{Now: func() time.Time { return at(0) }})
		run := RunKey{Step: "step-detach", Session: "session-detach"}
		steadyStream(watcher, run, at(0), time.Second, 4)
		if interject {
			watcher.Observe(detachedAt(run, at(10*time.Second)))
		}
		return watcher.Suspicion(run, at(20*time.Second))
	}

	attended := drive(false)
	if attended <= 0 {
		t.Fatalf("baseline suspicion = %v; the comparison is meaningless unless suspicion has accrued", attended)
	}
	if detached := drive(true); detached != attended {
		t.Fatalf("suspicion after a detach = %v, want exactly the %v of a run that was never detached", detached, attended)
	}
}

// TestReadoutNamesAnUnattendedRun proves the retained detachment reaches the one
// surface a Botanist actually reads.
func TestReadoutNamesAnUnattendedRun(t *testing.T) {
	watcher := New(Config{Now: func() time.Time { return at(0) }})

	last := steadyStream(watcher, probeRun, at(0), time.Second, 6)
	watcher.Observe(detachedAt(probeRun, last.Add(time.Second)))

	var page bytes.Buffer
	if err := watcher.Render(&page); err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if !strings.Contains(page.String(), "the Stem stopped waiting") {
		t.Fatalf("readout does not say the run is unattended:\n%s", page.String())
	}
	if strings.Contains(page.String(), "ended: sprout-detached") {
		t.Fatalf("readout reports detachment as an ending:\n%s", page.String())
	}
}

// TestWatcherNeverCancelsTheCallersContext is the behavioural companion to the
// import assertion: the one context the package is handed comes back untouched.
func TestWatcherNeverCancelsTheCallersContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probed := false
	watcher := New(Config{
		Bus:             eventbus.New(),
		ScratchInterval: time.Second,
		Now:             func() time.Time { return at(0) },
		Scratch: func(probeCtx context.Context, _ RunKey) ([]string, error) {
			probed = true
			if probeCtx.Err() != nil {
				t.Errorf("the probe was handed a cancelled context: %v", probeCtx.Err())
			}
			return []string{"a.go"}, nil
		},
	})

	last := steadyStream(watcher, probeRun, at(0), time.Second, 6)
	watcher.Tick(ctx, last.Add(time.Hour))
	watcher.Tick(ctx, last.Add(2*time.Hour))

	if !probed {
		t.Fatal("the probe never ran; nothing was measured")
	}
	if ctx.Err() != nil {
		t.Fatalf("the caller's context was cancelled by the watcher: %v", ctx.Err())
	}
}

// TestReadoutRendersRetainedRecord captures the readout and asserts on it. An
// io.Discard here would leave the whole rendering channel unasserted, which is a
// defect this repository has shipped before.
func TestReadoutRendersRetainedRecord(t *testing.T) {
	watcher := New(Config{Bus: eventbus.New(), Now: func() time.Time { return at(0) }})

	last := steadyStream(watcher, probeRun, at(0), time.Second, 6)
	watcher.Observe(toolAt(probeRun, last.Add(time.Second), "read_file", map[string]any{"path": "a.go"}))
	watcher.Observe(toolAt(probeRun, last.Add(2*time.Second), "read_file", map[string]any{"path": "a.go"}))
	watcher.Observe(toolAt(probeRun, last.Add(3*time.Second), "read_file", map[string]any{"path": "a.go"}))
	watcher.Tick(context.Background(), last.Add(time.Hour))

	var page bytes.Buffer
	if err := watcher.Render(&page); err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	rendered := page.String()

	for _, want := range []string{
		probeRun.Step,
		probeRun.Session,
		"stream cadence 6",
		"distinct tool call 1",
		"repeated tool calls 2",
		"learned from",
		"reported dormant 1 time(s)",
		"stream-token 6",
		"tool-invoked 3",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("readout does not contain %q:\n%s", want, rendered)
		}
	}
}

// TestReadoutSaysWhatItDoesNotKnow pins the rule that the readout renders from
// retained events and never past them. A run too young to have a cadence must be
// described as having no cadence, not given a plausible-looking one, and an
// unmeasurable probe must be reported as unmeasured rather than as no growth.
func TestReadoutSaysWhatItDoesNotKnow(t *testing.T) {
	watcher := New(Config{
		ScratchInterval: 10 * time.Second,
		Now:             func() time.Time { return at(0) },
		Scratch: func(_ context.Context, _ RunKey) ([]string, error) {
			return nil, errors.New("git status --porcelain failed: context deadline exceeded")
		},
	})

	watcher.Observe(streamAt(probeRun, at(0)))
	watcher.Observe(streamAt(probeRun, at(time.Second)))
	watcher.Tick(context.Background(), at(10*time.Second))

	var page bytes.Buffer
	if err := watcher.Render(&page); err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	rendered := page.String()

	for _, want := range []string{
		"not yet learned",
		"cold-start",
		"diff growth unmeasured on 1 sample(s)",
		"context deadline exceeded",
		"still growing as far as the retained events show",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("readout does not contain %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "learned from") {
		t.Fatalf("readout claims a learned cadence for a run with two gaps:\n%s", rendered)
	}
}

// TestColdStartIsNotAPolicy pins the one duration constant to the only job it is
// allowed to have. It stands in before a run's own cadence exists and is out of
// the picture once it does — so two runs whose own cadences differ must diverge,
// and neither may be judged against the cold-start figure afterwards.
func TestColdStartIsNotAPolicy(t *testing.T) {
	fast := &cadence{}
	for i := 0; i < minLearnedIntervals; i++ {
		fast.observe(100 * time.Millisecond)
	}
	slow := &cadence{}
	for i := 0; i < minLearnedIntervals; i++ {
		slow.observe(5 * time.Minute)
	}

	fastEnvelope, fastLearned := fast.envelope()
	slowEnvelope, slowLearned := slow.envelope()

	if !fastLearned || !slowLearned {
		t.Fatalf("envelopes not learned after %d gaps (fast %v, slow %v)", minLearnedIntervals, fastLearned, slowLearned)
	}
	if fastEnvelope == coldStartCadence || slowEnvelope == coldStartCadence {
		t.Fatalf("a learned envelope equals the cold-start value (fast %s, slow %s); the cold start is being used as a policy", fastEnvelope, slowEnvelope)
	}
	if fastEnvelope >= slowEnvelope {
		t.Fatalf("fast envelope %s is not below slow envelope %s; the envelope is not derived from the run's own cadence", fastEnvelope, slowEnvelope)
	}

	// One gap short of the threshold, the cold start is still standing in, and
	// it is reported as standing in rather than as a measurement.
	young := &cadence{}
	for i := 0; i < minLearnedIntervals-1; i++ {
		young.observe(100 * time.Millisecond)
	}
	if envelope, learned := young.envelope(); learned || envelope != coldStartCadence {
		t.Fatalf("young cadence envelope = %s learned=%v, want the cold-start %s reported as unlearned", envelope, learned, coldStartCadence)
	}
}

// TestIdenticalArgumentsFingerprintIdentically guards the mechanism the
// asymmetry runs on. Go randomises map iteration order, so a fingerprint built
// by formatting a map directly would make an identical repeat look distinct on
// most attempts — turning the asymmetry off without failing anything.
func TestIdenticalArgumentsFingerprintIdentically(t *testing.T) {
	arguments := map[string]any{"path": "main.go", "start": 1, "end": 200, "mode": "read"}

	first := toolFingerprint(toolAt(probeRun, at(0), "read_file", arguments))
	for i := 0; i < 64; i++ {
		copied := make(map[string]any, len(arguments))
		for key, value := range arguments {
			copied[key] = value
		}
		if again := toolFingerprint(toolAt(probeRun, at(0), "read_file", copied)); again != first {
			t.Fatalf("identical arguments fingerprinted differently:\n%q\n%q", first, again)
		}
	}

	changed := toolFingerprint(toolAt(probeRun, at(0), "read_file", map[string]any{"path": "other.go", "start": 1, "end": 200, "mode": "read"}))
	if changed == first {
		t.Fatalf("different arguments share a fingerprint %q; every call would read as a repeat", first)
	}
}
