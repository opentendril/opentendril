package conductor

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/roots/llm"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what was
// written. The warning is one of the three signals decision 2 asks for, so it
// has to be read back rather than discarded — a discarded writer leaves the
// whole announcement channel unasserted while the test still passes.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = writer

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(reader)
		done <- buf.String()
	}()

	fn()

	writer.Close()
	os.Stderr = original
	return <-done
}

// declaredIncapableLLM is an endpoint an operator has told us cannot take tool
// definitions. It fails the test if it is ever offered any.
type declaredIncapableLLM struct {
	fakeLLM
	toolsOffered int
}

func (f *declaredIncapableLLM) ToolDefinitionsCapable() bool { return false }

func (f *declaredIncapableLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	f.toolsOffered++
	if tokenChan != nil {
		close(tokenChan)
	}
	return llm.Result{Text: "should never be reached"}, nil
}

func TestDeclaredIncapableEndpointRunsInProseAndSaysSo(t *testing.T) {
	workspace := t.TempDir()
	client := &declaredIncapableLLM{}
	client.response = `{"final":"done"}`
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var res sproutResult
	var runErr error
	stderr := captureStderr(t, func() {
		res, runErr = sprout.Run(context.Background(), "test")
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	// Signal one: stderr.
	if !strings.Contains(stderr, "falling back to prose protocol") {
		t.Errorf("stderr = %q, want the downgrade warning", stderr)
	}
	if !strings.Contains(stderr, "declared incapable by configuration") {
		t.Errorf("stderr = %q, want it to name the reason", stderr)
	}

	// Signal two: the event.
	events := downgradeEvents(bus)
	if len(events) != 1 {
		t.Fatalf("EventSproutDowngraded count = %d, want 1", len(events))
	}
	if got := events[0].Data["reason"]; got != "declared incapable by configuration" {
		t.Errorf("event reason = %v, want 'declared incapable by configuration'", got)
	}
	if events[0].SessionID != "session-1" {
		t.Errorf("event sessionID = %q, want session-1", events[0].SessionID)
	}

	// Signal three: the run's own record.
	if res.Protocol != "prose" {
		t.Errorf("Protocol = %q, want prose", res.Protocol)
	}

	// And the run is genuinely carried in prose, not merely labelled that way.
	if client.toolsOffered != 0 {
		t.Errorf("the native call was used %d times for an endpoint declared incapable", client.toolsOffered)
	}
	if len(client.calls) == 0 {
		t.Fatal("the prose client was never called")
	}
	if !strings.Contains(client.calls[0][0].Content, proseProtocolRulesHeading) {
		t.Error("the prompt the mind received does not teach the prose protocol")
	}
}

// refusingLLM answers the first native call with a refusal, exactly as the
// client raises one, then behaves as an ordinary prose endpoint.
type refusingLLM struct {
	fakeLLM
	toolsPerNativeCall [][]llm.ToolDefinition
	refusals           int
	refusalsToGive     int
}

func (f *refusingLLM) ToolDefinitionsCapable() bool { return true }

func (f *refusingLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	f.toolsPerNativeCall = append(f.toolsPerNativeCall, tools)
	if tokenChan != nil {
		close(tokenChan)
	}
	if f.refusals < f.refusalsToGive {
		f.refusals++
		return llm.Result{}, llm.ErrToolsRefused
	}
	return llm.Result{Text: "native answer"}, nil
}

func TestRefusedToolDefinitionsDowngradeAndSaySo(t *testing.T) {
	workspace := t.TempDir()
	client := &refusingLLM{refusalsToGive: 1}
	client.response = `{"final":"done"}`
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var res sproutResult
	var runErr error
	stderr := captureStderr(t, func() {
		res, runErr = sprout.Run(context.Background(), "test")
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	if !strings.Contains(stderr, "detected rejection by endpoint") {
		t.Errorf("stderr = %q, want the detected-rejection warning", stderr)
	}

	events := downgradeEvents(bus)
	if len(events) != 1 {
		t.Fatalf("EventSproutDowngraded count = %d, want 1", len(events))
	}
	if got := events[0].Data["reason"]; got != "detected rejection by endpoint" {
		t.Errorf("event reason = %v, want 'detected rejection by endpoint'", got)
	}

	if res.Protocol != "prose" {
		t.Errorf("Protocol = %q, want prose", res.Protocol)
	}

	// The refused turn is the only one that may be offered definitions. Anything
	// after it goes to the prose client, which takes none at all.
	if len(client.toolsPerNativeCall) != 1 {
		t.Errorf("native calls = %d, want 1 — the endpoint must not be asked again", len(client.toolsPerNativeCall))
	}

	// The re-issued turn has to teach the protocol it now expects back.
	if len(client.calls) == 0 {
		t.Fatal("the turn was never re-issued through the prose client")
	}
	if !strings.Contains(client.calls[0][0].Content, proseProtocolRulesHeading) {
		t.Error("the re-issued prompt does not teach the prose protocol")
	}
}

// The assertion whose absence let the downgrade change the label and the prompt
// while leaving the parser native: a mind taught the prose protocol answers
// with a prose tool call, and that call has to be executed. Read natively it is
// not a call at all, so the run returns it as a final answer and matures having
// touched nothing.
func TestProseToolCallAfterDowngradeIsExecuted(t *testing.T) {
	workspace := t.TempDir()
	client := &refusingLLM{refusalsToGive: 1}
	client.responses = []string{
		`{"tool":"readFile","arguments":{"path":"README.md"}}`,
		`{"final":"read it"}`,
	}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var res sproutResult
	var runErr error
	_ = captureStderr(t, func() {
		res, runErr = sprout.Run(context.Background(), "read the README")
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	if len(session.calls) != 1 {
		t.Fatalf("tools invoked = %d, want 1 — the prose tool call was not executed", len(session.calls))
	}
	if session.calls[0].Tool != "readFile" {
		t.Errorf("invoked %q, want readFile", session.calls[0].Tool)
	}
	if res.Response != "read it" {
		t.Errorf("Response = %q, want the answer that followed the tool call", res.Response)
	}
}

// The downgrade is a state change, so it is announced when the state changes —
// once. A second refusal cannot happen, because the native client is gone; if
// one somehow did, the run would still not announce it twice.
func TestDowngradeAnnouncesOncePerRun(t *testing.T) {
	workspace := t.TempDir()
	client := &refusingLLM{refusalsToGive: 5}
	client.response = `{"final":"done"}`
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	stderr := captureStderr(t, func() {
		if _, err := sprout.Run(context.Background(), "test"); err != nil {
			t.Errorf("Run: %v", err)
		}
	})

	if got := strings.Count(stderr, "falling back to prose protocol"); got != 1 {
		t.Errorf("stderr warnings = %d, want 1", got)
	}
	if got := len(downgradeEvents(bus)); got != 1 {
		t.Errorf("EventSproutDowngraded count = %d, want 1", got)
	}
	if client.refusals != 1 {
		t.Errorf("refusals collected = %d, want 1 — the endpoint was asked again after it said no", client.refusals)
	}
}

// The refused turn produced no answer to reason about, so it must not be
// charged against the iteration budget. Without this, every downgraded run
// silently gets one fewer turn than an undowngraded one — a difference nothing
// else in the suite can see, because the budget is only reached at the cap.
func TestRefusedTurnDoesNotSpendAnIteration(t *testing.T) {
	workspace := t.TempDir()
	client := &refusingLLM{refusalsToGive: 1}
	// Never finishes: every prose turn asks for a tool, so the run is driven
	// all the way to the cap and the number of turns it got becomes visible.
	client.response = `{"tool":"readFile","arguments":{"path":"README.md"}}`
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var runErr error
	_ = captureStderr(t, func() {
		_, runErr = sprout.Run(context.Background(), "test")
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "max iterations") {
		t.Fatalf("Run error = %v, want the max-iterations error", runErr)
	}

	if len(client.calls) != sproutMaxIterations {
		t.Errorf("prose turns = %d, want %d — the refused turn was charged to the budget", len(client.calls), sproutMaxIterations)
	}
}

// A run that is never refused says nothing. Loudness that fires anyway is
// indistinguishable from loudness that is broken.
func TestNativeRunPublishesNoDowngrade(t *testing.T) {
	workspace := t.TempDir()
	client := &nativeFakeLLM{nativeResponse: llm.Result{Text: "done"}}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	res, err := sprout.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(downgradeEvents(bus)); got != 0 {
		t.Errorf("EventSproutDowngraded count = %d, want 0", got)
	}
	if res.Protocol != "native" {
		t.Errorf("Protocol = %q, want native", res.Protocol)
	}
}

func downgradeEvents(bus *eventbus.Bus) []eventbus.Event {
	var found []eventbus.Event
	for _, ev := range bus.History(100) {
		if ev.Type == eventbus.EventSproutDowngraded {
			found = append(found, ev)
		}
	}
	return found
}
