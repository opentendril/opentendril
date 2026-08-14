package conductor

import (
	"context"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/dormancy"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/roots/llm"
)

// toolOnlyNativeLLM is a native caller whose first streamed turn carries only
// tool-call argument fragments — no text at all. That is the liveness scenario
// decision 5 of RFC #700 is concerned with: a turn that streams argument
// fragments and no text must still show signs of life on the event bus, or
// native carriage would make well-behaved growths look dormant to the detector.
//
// It is deliberately separate from nativeFakeLLM. That type sends response.Text
// to tokenChan, which would let text arrive — making the fixture a
// text-streaming turn in disguise rather than a tool-only one.
type toolOnlyNativeLLM struct {
	toolCallID string
	toolName   string
	// fragment is the argument string streamed as a ToolCallFragment.
	fragment string
	// calls records how many times CallWithTools has been invoked.
	calls int
	// firstResultText records the Text field of the Result returned on the
	// first call. A tool-only turn must return empty Text — the liveness
	// signal is the ToolCallFragment pushed to tokenChan, not any text in
	// the response.
	firstResultText string
}

func (f *toolOnlyNativeLLM) ToolDefinitionsCapable() bool { return true }

// CallWithTools pushes one tool-call argument fragment on tokenChan (the
// liveness signal — sprout.go:292 publishes it as EventStreamToken) on the
// first invocation, then on the second returns a final answer to end the run.
// Both invocations produce zero text in their Result.
func (f *toolOnlyNativeLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	f.calls++

	if f.calls == 1 {
		// First turn: push the argument fragment and return a tool call.
		// The Text field is deliberately empty — the liveness signal on this
		// turn must come from the ToolCallFragment reaching the event bus via
		// sprout.go:292, not from any prose-text path.
		result := llm.Result{
			Text: "",
			ToolCalls: []llm.ToolCall{{
				ID:   f.toolCallID,
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      f.toolName,
					Arguments: f.fragment,
				},
			}},
		}
		f.firstResultText = result.Text
		if tokenChan != nil {
			tokenChan <- f.fragment
			close(tokenChan)
		}
		return result, nil
	}

	// Subsequent turns: final answer, no token, end the run.
	if tokenChan != nil {
		close(tokenChan)
	}
	return llm.Result{Text: "done"}, nil
}

func (f *toolOnlyNativeLLM) Call(ctx context.Context, messages []llm.Message) (string, error) {
	return "done", nil
}

func (f *toolOnlyNativeLLM) CallStream(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (string, error) {
	if tokenChan != nil {
		close(tokenChan)
	}
	return "done", nil
}

func (f *toolOnlyNativeLLM) CallPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return "done", nil
}

func (f *toolOnlyNativeLLM) CallWithResult(ctx context.Context, messages []llm.Message) (llm.Result, error) {
	resp, err := f.Call(ctx, messages)
	return llm.Result{Text: resp}, err
}

func (f *toolOnlyNativeLLM) CallStreamWithResult(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (llm.Result, error) {
	resp, err := f.CallStream(ctx, messages, tokenChan)
	return llm.Result{Text: resp}, err
}

// silentNativeLLM is the mutation fixture for assertion 3. It executes a tool
// call but writes NOTHING to tokenChan, simulating the world where
// ToolCallFragment tokens are not forwarded to the event bus. EventStreamToken
// never arrives, so the stream-cadence suppressor in the dormancy watcher
// (watcher.go:255) never fires for this turn.
type silentNativeLLM struct {
	toolCallID string
	toolName   string
	arguments  string
	calls      int
}

func (f *silentNativeLLM) ToolDefinitionsCapable() bool { return true }

func (f *silentNativeLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	f.calls++
	// Always close without writing anything — no fragment token ever.
	if tokenChan != nil {
		close(tokenChan)
	}
	if f.calls == 1 {
		return llm.Result{
			Text: "",
			ToolCalls: []llm.ToolCall{{
				ID:   f.toolCallID,
				Type: "function",
				Function: llm.ToolCallFunction{
					Name:      f.toolName,
					Arguments: f.arguments,
				},
			}},
		}, nil
	}
	return llm.Result{Text: "done"}, nil
}

func (f *silentNativeLLM) Call(ctx context.Context, messages []llm.Message) (string, error) {
	return "done", nil
}

func (f *silentNativeLLM) CallStream(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (string, error) {
	if tokenChan != nil {
		close(tokenChan)
	}
	return "done", nil
}

func (f *silentNativeLLM) CallPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return "done", nil
}

func (f *silentNativeLLM) CallWithResult(ctx context.Context, messages []llm.Message) (llm.Result, error) {
	resp, err := f.Call(ctx, messages)
	return llm.Result{Text: resp}, err
}

func (f *silentNativeLLM) CallStreamWithResult(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (llm.Result, error) {
	resp, err := f.CallStream(ctx, messages, tokenChan)
	return llm.Result{Text: resp}, err
}

// collectEventsByType returns every event of the given type from the bus history.
func collectEventsByType(bus *eventbus.Bus, eventType eventbus.EventType) []eventbus.Event {
	var found []eventbus.Event
	for _, ev := range bus.History(500) {
		if ev.Type == eventType {
			found = append(found, ev)
		}
	}
	return found
}

// prefixBeforeFirstToolInvoked returns the prefix of history that precedes the
// first EventToolInvoked. This is the mid-turn slice — the events that exist
// while a tool call is streaming its arguments but before the tool has executed.
//
// The property decision 5 is about is mid-turn: argument fragments are the only
// signal keeping the run from looking dormant while the model streams a large
// tool call. That moment cannot be captured from a finished run (where
// EventToolInvoked always dominates record.last), only from the prefix.
func prefixBeforeFirstToolInvoked(history []eventbus.Event) []eventbus.Event {
	for i, ev := range history {
		if ev.Type == eventbus.EventToolInvoked {
			return history[:i]
		}
	}
	return history
}

// replayPrefixIntoWatcher subscribes a new Watcher to a fresh replay bus, then
// publishes the mid-turn prefix of a run's event history through that bus, and
// ticks it to a point that distinguishes a run with EventStreamToken from one
// without.
//
// Why publish-through-bus rather than call Observe directly:
// Calling w.Observe(ev) directly bypasses the subscription filter — the Watcher
// still handles every event type regardless of observedTypes in watcher.go:184.
// To make the required mutation (removing EventStreamToken from observedTypes)
// detectable, events must travel through the bus subscription so the filter
// applies. bus.Publish dispatches handlers synchronously (eventbus.go:283–285),
// so there is no race between publish and Tick.
//
// Why prefix-only:
// In a completed run EventToolInvoked always arrives after the argument
// fragments, so it always dominates record.last — making the token a bystander
// that cannot change the verdict. The prefix isolates the moment when fragments
// are the only signal. In the healthy run the prefix is
// [EventSproutEmerged, EventStreamToken]; in the silent run it is
// [EventSproutEmerged]. The token is then the last sign of life, and its
// absence is what accrues the silence the watcher measures.
//
// Why EventSproutEmerged is injected by the test:
// The Sprout does not publish EventSproutEmerged — that is the orchestrator's
// job. Injecting it into the run bus before the Sprout starts simulates the
// real deployment sequence. Without it the silent run's prefix is empty, no
// record is ever created, and Tick finds nothing to evaluate — the report is
// impossible and the test vacuous.
//
// Timing arithmetic (coldStartCadence=30s, reportingSuspicion=4.0):
//
//	Healthy prefix [EventSproutEmerged@1min, EventStreamToken@2min]:
//	  record.last = 2min → threshold = 2min + 5×30s = 4.5min
//	  At tick 4min: silence = 2min, suspicion = 3.0 < 4.0 → no report.
//
//	Silent prefix [EventSproutEmerged@1min]:
//	  record.last = 1min → threshold = 1min + 5×30s = 3.5min
//	  At tick 4min: silence = 3min, suspicion = 5.0 ≥ 4.0 → reports.
//
// Required mutation: removing eventbus.EventStreamToken from observedTypes in
// cmd/stem/internal/dormancy/watcher.go:186 must redden assertion 2. With that
// mutation the watcher's Subscribe never registers a handler for EventStreamToken,
// so published tokens are not delivered and both prefixes reduce to
// [EventSproutEmerged@1min], giving record.last = 1min for both — and the
// 4-minute tick produces a report for both.
func replayPrefixIntoWatcher(history []eventbus.Event, bus *eventbus.Bus) *dormancy.Watcher {
	origin := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 4 minutes sits between the healthy threshold (4.5min) and the silent
	// threshold (3.5min), making the verdict deterministic without real
	// elapsed time or sleeps.
	farFuture := origin.Add(4 * time.Minute)

	w := dormancy.New(dormancy.Config{
		Bus: bus,
		Now: func() time.Time { return origin },
	})

	// Subscribe the watcher to the replay bus. This is what makes the required
	// mutation effective: Subscribe registers handlers only for the types in
	// observedTypes, so removing EventStreamToken from that list means the
	// watcher never receives those events, no matter how many are published.
	detach := w.Subscribe(bus)
	defer detach()

	prefix := prefixBeforeFirstToolInvoked(history)

	// Always replace timestamps so farFuture is always ahead of every event.
	// One-minute spacing creates the 1-minute window between thresholds
	// described above; second-scale spacing would narrow that window to 1
	// second, which a single event insertion or removal could erase.
	//
	// Publish through the bus (not w.Observe directly) so the subscription
	// filter in observedTypes applies — see the function comment above.
	for i, ev := range prefix {
		ev.Timestamp = origin.Add(time.Duration(i+1) * time.Minute)
		bus.Publish(ev)
	}

	w.Tick(context.Background(), farFuture)
	return w
}

// TestNativeToolOnlyTurnWithoutTokenDoesTriggerDormancyReport is assertion 3.
// Written first as required: confirmed red before assertions 1 and 2 existed.
//
// The mutation: a native caller that closes tokenChan without writing anything.
// EventStreamToken never reaches the bus; only EventSproutEmerged (injected by
// the test to arm the record) is in the prefix. With record.last = 1min and a
// tick at 4min the silence is 3min; with coldStartCadence=30s the suspicion is
// 5.0 ≥ reportingSuspicion 4.0. The watcher must report.
//
// This is the assertion that proves assertion 2 is not vacuous: if the watcher
// does not distinguish a prefix with EventStreamToken from one without, the
// non-report in assertion 2 would be satisfied by a broken implementation.
func TestNativeToolOnlyTurnWithoutTokenDoesTriggerDormancyReport(t *testing.T) {
	workspace := t.TempDir()
	runBus := eventbus.New()
	defer runBus.Shutdown()

	// Inject EventSproutEmerged to simulate the orchestrator arming the record.
	// Without it the silent prefix is empty and the watcher has nothing to
	// evaluate — making the expected report impossible and the test vacuous.
	runBus.Publish(eventbus.Event{
		Type:      eventbus.EventSproutEmerged,
		Source:    "step-silent",
		SessionID: "session-silent",
	})

	client := &silentNativeLLM{
		toolCallID: "call-silent-gamma",
		toolName:   "readFile",
		arguments:  `{"path":"go.mod"}`,
	}
	session := &fakeSession{
		tools: []ToolDefinition{{Name: "readFile", Description: "Read a file",
			Arguments: []ToolArgument{{Name: "path", Type: "string", Required: true}}}},
	}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout",
		client, session, runBus, "step-silent", "session-silent")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	if _, err := sprout.Run(context.Background(), "check go.mod"); err != nil {
		t.Fatalf("sprout.Run: %v", err)
	}

	history := runBus.History(500)
	prefix := prefixBeforeFirstToolInvoked(history)

	// Guard: no EventStreamToken must appear in the prefix. If any does, the
	// stream-cadence suppressor fires and the expected report below may not
	// appear — meaning the test is not measuring the token-publication seam.
	for _, ev := range prefix {
		if ev.Type == eventbus.EventStreamToken {
			t.Fatalf("EventStreamToken in prefix %v; the fixture is wrong — this must be absent for the mutation to be meaningful", prefix)
		}
	}

	// Guard: the prefix must be non-empty (EventSproutEmerged was injected
	// above). If the prefix is empty, the injection was not captured or the
	// truncation is broken.
	if len(prefix) == 0 {
		t.Fatalf("prefix is empty; EventSproutEmerged was not captured in bus history before the truncation point")
	}

	watchBus := eventbus.New()
	defer watchBus.Shutdown()
	w := replayPrefixIntoWatcher(history, watchBus)

	if !w.ReportedAny() {
		t.Errorf("the dormancy watcher did not report on a silent prefix; " +
			"assertion 2 (TestNativeToolOnlyTurnDoesNotTriggerDormancyReport) would be vacuous — " +
			"a real token-publication defect would not be caught")
	}
}

// TestNativeToolOnlyTurnPublishesBothLivenessSignals is assertion 1.
//
// A Sprout whose first native turn streams tool-call argument fragments and no
// text must publish at least one EventStreamToken and at least one
// EventToolInvoked to the bus.
//
// The seam under test is sprout.go:292: the goroutine that ranges over tokenChan
// and publishes each item as EventStreamToken. The fake writes a fragment
// directly to tokenChan, so the test exercises the Sprout's publication path.
// The roots/llm/client.go stream-reassembly tests (introduced with slices 2 and
// 3) pin the other link in the chain: that ToolCallFragment deltas from the LLM
// reach tokenChan in the first place.
//
// The fixture asserts Result.Text was empty on the first turn, guarding against
// this test passing because the prose-text path happened to flow.
func TestNativeToolOnlyTurnPublishesBothLivenessSignals(t *testing.T) {
	workspace := t.TempDir()
	bus := eventbus.New()
	defer bus.Shutdown()

	client := &toolOnlyNativeLLM{
		toolCallID: "call-liveness-alpha",
		toolName:   "readFile",
		fragment:   `{"path":"README.md"}`,
	}
	session := &fakeSession{
		tools: []ToolDefinition{{
			Name:        "readFile",
			Description: "Read a file",
			Arguments:   []ToolArgument{{Name: "path", Type: "string", Required: true}},
		}},
	}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout",
		client, session, bus, "step-liveness", "session-liveness")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	if _, err := sprout.Run(context.Background(), "read the README"); err != nil {
		t.Fatalf("sprout.Run: %v", err)
	}

	// Guard: the first turn must have returned empty Text — the liveness signal
	// must come from the ToolCallFragment pushed to tokenChan (sprout.go:292),
	// not from any prose-text path. A non-empty firstResultText means the
	// fixture is exercising the text-streaming path that other tests already pin.
	if client.firstResultText != "" {
		t.Errorf("the first turn returned non-empty Text %q; the fixture is testing the text-streaming path, not the tool-only one",
			client.firstResultText)
	}

	// Assertion 1a: at least one EventStreamToken reached the bus. This is
	// what the dormancy detector observes as a stream-cadence sign of life
	// (watcher.go:255). The goroutine at sprout.go:292 converts every token on
	// tokenChan — including ToolCallFragment tokens — into this event.
	tokens := collectEventsByType(bus, eventbus.EventStreamToken)
	if len(tokens) == 0 {
		t.Errorf("no EventStreamToken published for the tool-only turn; " +
			"the dormancy detector would see no liveness signal and the run would look " +
			"dormant during native tool calls (sprout.go:292 is the publication seam)")
	}

	// Assertion 1b: at least one EventToolInvoked reached the bus.
	invoked := collectEventsByType(bus, eventbus.EventToolInvoked)
	if len(invoked) == 0 {
		t.Errorf("no EventToolInvoked published; native tool execution is unobservable on the bus")
	}
}

// TestNativeToolOnlyTurnDoesNotTriggerDormancyReport is assertion 2.
//
// The mid-turn prefix of events published by a native tool-only run — replayed
// through a fresh bus into a dormancy.Watcher and ticked at 4 minutes from a
// synthetic origin — must not produce a dormancy report.
//
// This test is not vacuous: assertion 3 confirms that the identical prefix
// without EventStreamToken (the silent run) DOES make the watcher report,
// proving EventStreamToken is the suppressor in this arrangement, not a
// bystander.
//
// The required mutation (stated in the replayPrefixIntoWatcher comment):
// removing eventbus.EventStreamToken from observedTypes in
// cmd/stem/internal/dormancy/watcher.go:186 must redden this test. With that
// mutation Subscribe never registers a handler for EventStreamToken, so the
// token published in the healthy prefix is not delivered, both prefixes reduce
// to [EventSproutEmerged@1min], and the watcher reports for both.
func TestNativeToolOnlyTurnDoesNotTriggerDormancyReport(t *testing.T) {
	workspace := t.TempDir()
	runBus := eventbus.New()
	defer runBus.Shutdown()

	// Inject EventSproutEmerged so the record is armed in the same way as in
	// assertion 3, making the two prefixes differ only in EventStreamToken.
	runBus.Publish(eventbus.Event{
		Type:      eventbus.EventSproutEmerged,
		Source:    "step-nodormancy",
		SessionID: "session-nodormancy",
	})

	client := &toolOnlyNativeLLM{
		toolCallID: "call-nodormancy-beta",
		toolName:   "readFile",
		fragment:   `{"path":"CHANGELOG.md"}`,
	}
	session := &fakeSession{
		tools: []ToolDefinition{{Name: "readFile", Description: "Read a file",
			Arguments: []ToolArgument{{Name: "path", Type: "string", Required: true}}}},
	}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout",
		client, session, runBus, "step-nodormancy", "session-nodormancy")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	if _, err := sprout.Run(context.Background(), "check the changelog"); err != nil {
		t.Fatalf("sprout.Run: %v", err)
	}

	history := runBus.History(500)
	prefix := prefixBeforeFirstToolInvoked(history)

	// Guard: the prefix must contain at least one EventStreamToken. Without it
	// the healthy prefix is identical to the silent one and the non-report in
	// this test would be vacuous — it would pass even if the token were never
	// the suppressor.
	tokenInPrefix := false
	for _, ev := range prefix {
		if ev.Type == eventbus.EventStreamToken {
			tokenInPrefix = true
			break
		}
	}
	if !tokenInPrefix {
		t.Fatalf("no EventStreamToken in prefix %v; the healthy prefix is identical to the silent one and this test is vacuous", prefix)
	}

	watchBus := eventbus.New()
	defer watchBus.Shutdown()
	w := replayPrefixIntoWatcher(history, watchBus)

	if w.ReportedAny() {
		t.Errorf("the dormancy watcher reported on the mid-turn prefix of a healthy native tool-only turn; " +
			"a Sprout streaming tool-call argument fragments must not look dormant")
	}
}
