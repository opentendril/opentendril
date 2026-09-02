package conductor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/roots/llm"
)

func TestNativeToolResultThenCanonicalProseToolCallRecovers(t *testing.T) {
	workspace := t.TempDir()
	client := &nativeFakeLLM{
		fakeLLM: fakeLLM{response: `{"final":"done after recovery"}`},
		nativeResponses: []llm.Result{
			{
				Text: "<thought>read before writing</thought>",
				ToolCalls: []llm.ToolCall{{
					ID:   "native-read",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "readFile",
						Arguments: `{"path":"README.md"}`,
					},
				}},
			},
			{
				Text: `{"tool":"writeFile","arguments":{"path":"HELLO.md","content":"Hello from OpenTendril.\n"}}`,
			},
		},
	}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}, {Name: "writeFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var result sproutResult
	var runErr error
	_ = captureStderr(t, func() {
		result, runErr = sprout.Run(context.Background(), "create HELLO.md")
	})
	if runErr != nil {
		t.Fatalf("Sprout.Run: %v", runErr)
	}
	if result.Response != "done after recovery" {
		t.Fatalf("Response = %q, want the final prose response", result.Response)
	}
	if result.Protocol != "prose" {
		t.Fatalf("Protocol = %q, want prose after native-to-prose recovery", result.Protocol)
	}
	if !result.WroteWorkspace {
		t.Fatal("WroteWorkspace = false, want the recovered write attributed to the Sprout")
	}
	if result.ToolInvocations != 2 {
		t.Fatalf("ToolInvocations = %d, want native read plus recovered write", result.ToolInvocations)
	}
	if len(session.calls) != 2 || session.calls[0].Tool != "readFile" || session.calls[1].Tool != "writeFile" {
		t.Fatalf("tool calls = %+v, want readFile then writeFile", session.calls)
	}
	if got := len(downgradeEvents(bus)); got != 1 {
		t.Fatalf("EventSproutDowngraded count = %d, want 1", got)
	}
	var invoked []eventbus.Event
	for _, event := range bus.History(50) {
		if event.Type == eventbus.EventToolInvoked {
			invoked = append(invoked, event)
		}
	}
	if len(invoked) != 2 {
		t.Fatalf("EventToolInvoked count = %d, want native and recovered calls", len(invoked))
	}
	if got := invoked[1].Data["tool"]; got != "writeFile" {
		t.Fatalf("recovered tool event named %v, want writeFile", got)
	}
}

func TestNativeProviderShapedToolIntentUsesBoundedCorrection(t *testing.T) {
	workspace := t.TempDir()
	providerShape := `{"name":"createFile","parameters":{"filePath":"HELLO.md","content":"Hello from OpenTendril."}}`
	client := &nativeFakeLLM{
		fakeLLM: fakeLLM{responses: []string{
			`{"tool":"writeFile","arguments":{"path":"HELLO.md","content":"Hello from OpenTendril."}}`,
			`{"final":"done after correction"}`,
		}},
		nativeResponses: []llm.Result{{Text: providerShape}},
	}
	session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var result sproutResult
	var runErr error
	_ = captureStderr(t, func() {
		result, runErr = sprout.Run(context.Background(), "create HELLO.md")
	})
	if runErr != nil {
		t.Fatalf("Sprout.Run: %v", runErr)
	}
	if result.Response != "done after correction" {
		t.Fatalf("Response = %q, want corrected final response", result.Response)
	}
	if len(session.calls) != 1 || session.calls[0].Tool != "writeFile" {
		t.Fatalf("tool calls = %+v, want exactly the canonical writeFile call", session.calls)
	}
	if result.ToolInvocations != 1 || !result.WroteWorkspace {
		t.Fatalf("result attribution = invocations %d, wrote %v; want one workspace write", result.ToolInvocations, result.WroteWorkspace)
	}
	if len(client.calls) == 0 {
		t.Fatal("no prose correction turn was sent")
	}
	correction := client.calls[0][len(client.calls[0])-1].Content
	if !strings.Contains(correction, `{"tool":"name","arguments":{...}}`) {
		t.Fatalf("correction = %q, want the canonical prose tool shape", correction)
	}
	if !strings.Contains(correction, "writeFile") {
		t.Fatalf("correction = %q, want the available tool catalog", correction)
	}
	if got := len(downgradeEvents(bus)); got != 1 {
		t.Fatalf("EventSproutDowngraded count = %d, want at most once", got)
	}
}

func TestNativeProviderShapedToolIntentDoesNotMature(t *testing.T) {
	workspace := t.TempDir()
	providerShape := `{"name":"createFile","parameters":{"filePath":"HELLO.md","content":"Hello from OpenTendril."}}`
	client := &nativeFakeLLM{
		fakeLLM:         fakeLLM{response: providerShape},
		nativeResponses: []llm.Result{{Text: providerShape}},
	}
	session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	result, runErr := sprout.Run(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("Sprout.Run error = %v, want bounded unusable-reply error", runErr)
	}
	if strings.TrimSpace(result.Response) != "" {
		t.Fatalf("Response = %q, want empty rather than a matured answer", result.Response)
	}
	if result.ToolInvocations != 0 || result.WroteWorkspace || len(session.calls) != 0 {
		t.Fatalf("unusable provider shape caused execution/attribution: result=%+v calls=%+v", result, session.calls)
	}
	if got := len(downgradeEvents(bus)); got != 1 {
		t.Fatalf("EventSproutDowngraded count = %d, want one bounded downgrade", got)
	}
}

func TestNativeOrdinaryTextAndJSONRemainFinalAnswers(t *testing.T) {
	cases := map[string]string{
		"plain prose":   "The requested work is complete.",
		"ordinary JSON": `{"status":"complete","message":"no tool invocation"}`,
	}

	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			client := &nativeFakeLLM{nativeResponse: llm.Result{Text: response}}
			session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}}
			bus := eventbus.New()
			defer bus.Shutdown()

			sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
			if err != nil {
				t.Fatalf("newSprout: %v", err)
			}
			result, runErr := sprout.Run(context.Background(), "report")
			if runErr != nil {
				t.Fatalf("Sprout.Run: %v", runErr)
			}
			if result.Response != response {
				t.Fatalf("Response = %q, want %q", result.Response, response)
			}
			if result.Protocol != "native" {
				t.Fatalf("Protocol = %q, want native", result.Protocol)
			}
			if result.ToolInvocations != 0 || result.WroteWorkspace || len(session.calls) != 0 {
				t.Fatalf("ordinary final answer caused tool execution/attribution: result=%+v calls=%+v", result, session.calls)
			}
			if got := len(downgradeEvents(bus)); got != 0 {
				t.Fatalf("EventSproutDowngraded count = %d, want 0", got)
			}
		})
	}
}

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

// refusingLLM answers any native call that carries tool definitions with a
// refusal, exactly as the real client does. A call with no definitions (the
// probe) succeeds and returns a parseable final answer. The previous
// count-based design refused on a call count, which would also refuse the
// probe and hide the whole change. The offer-based design matches the real
// client.
type refusingLLM struct {
	fakeLLM
	toolsPerNativeCall [][]llm.ToolDefinition
	refusalMessage     string
}

func (f *refusingLLM) ToolDefinitionsCapable() bool { return true }

func (f *refusingLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	toolsCopy := make([]llm.ToolDefinition, len(tools))
	copy(toolsCopy, tools)
	f.toolsPerNativeCall = append(f.toolsPerNativeCall, toolsCopy)
	if tokenChan != nil {
		close(tokenChan)
	}
	if len(tools) > 0 {
		msg := f.refusalMessage
		if msg == "" {
			msg = "tools not supported by this endpoint"
		}
		return llm.Result{}, fmt.Errorf("%w: llm returned 400: %s", llm.ErrRejectedWithTools, msg)
	}
	return llm.Result{Text: "native answer"}, nil
}

func TestRefusedToolDefinitionsDowngradeAndSaySo(t *testing.T) {
	workspace := t.TempDir()
	client := &refusingLLM{refusalMessage: "temperature is deprecated for this model"}
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

	// The probe found that removing definitions made the call succeed, so the
	// downgrade is announced with what was proven, not a hypothesis.
	if !strings.Contains(stderr, "accepted without tool definitions") {
		t.Errorf("stderr = %q, want the proven-reason in the warning", stderr)
	}
	if !strings.Contains(stderr, "falling back to prose protocol") {
		t.Errorf("stderr = %q, want the downgrade warning", stderr)
	}
	// The endpoint's own message must reach the operator.
	if !strings.Contains(stderr, "temperature is deprecated for this model") {
		t.Errorf("stderr = %q, want the endpoint's own message", stderr)
	}

	events := downgradeEvents(bus)
	if len(events) != 1 {
		t.Fatalf("EventSproutDowngraded count = %d, want 1", len(events))
	}
	if got := events[0].Data["reason"]; got != "accepted without tool definitions" {
		t.Errorf("event reason = %v, want 'accepted without tool definitions'", got)
	}
	if got, ok := events[0].Data["endpointMessage"].(string); !ok || !strings.Contains(got, "temperature is deprecated for this model") {
		t.Errorf("event endpointMessage = %v, want the endpoint's message", events[0].Data["endpointMessage"])
	}

	if res.Protocol != "prose" {
		t.Errorf("Protocol = %q, want prose", res.Protocol)
	}

	// Two native calls must have been made: the refused turn (with tools) and
	// the probe (without tools).
	if len(client.toolsPerNativeCall) != 2 {
		t.Fatalf("native calls = %d, want 2 (refused turn + probe)", len(client.toolsPerNativeCall))
	}
	if len(client.toolsPerNativeCall[0]) == 0 {
		t.Errorf("first native call (refused) carried no tools, want the refused turn to carry tools")
	}
	if len(client.toolsPerNativeCall[1]) != 0 {
		t.Errorf("probe carried %d tools, want zero", len(client.toolsPerNativeCall[1]))
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
	client := &refusingLLM{}
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
// once. A second refusal cannot happen after a proven downgrade, because the
// native client is cleared and the endpoint is not asked again.
func TestDowngradeAnnouncesOncePerRun(t *testing.T) {
	workspace := t.TempDir()
	client := &refusingLLM{}
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
	// The refusingLLM refuses on len(tools)>0. After the probe succeeds and
	// the downgrade fires, nativeClient is nil — the endpoint is never asked
	// again with definitions.
	if len(client.toolsPerNativeCall) > 2 {
		t.Errorf("native calls = %d, want at most 2 (refused + probe) — endpoint was asked again after downgrade", len(client.toolsPerNativeCall))
	}
}

// The refused turn produced no answer to reason about, so it must not be
// charged against the iteration budget. Without this, every downgraded run
// silently gets one fewer turn than an undowngraded one — a difference nothing
// else in the suite can see, because the budget is only reached at the cap.
func TestRefusedTurnDoesNotSpendAnIteration(t *testing.T) {
	workspace := t.TempDir()
	client := &refusingLLM{}
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

// alwaysRefusingLLM refuses every native call regardless of whether tool
// definitions are present, simulating a request error unrelated to tools
// (e.g. a deprecated model parameter). probeMessage is the error text
// returned on the no-tools probe.
type alwaysRefusingLLM struct {
	fakeLLM
	toolsPerNativeCall [][]llm.ToolDefinition
	probeMessage       string
}

func (f *alwaysRefusingLLM) ToolDefinitionsCapable() bool { return true }

func (f *alwaysRefusingLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	toolsCopy := make([]llm.ToolDefinition, len(tools))
	copy(toolsCopy, tools)
	f.toolsPerNativeCall = append(f.toolsPerNativeCall, toolsCopy)
	if tokenChan != nil {
		close(tokenChan)
	}
	if len(tools) > 0 {
		// First call (with definitions): raise ErrRejectedWithTools so the
		// Sprout believes it may need to probe.
		return llm.Result{}, fmt.Errorf("%w: llm returned 400: tools not allowed", llm.ErrRejectedWithTools)
	}
	// Probe (no tools) also fails — the cause was not the definitions.
	msg := f.probeMessage
	if msg == "" {
		msg = "request failed"
	}
	return llm.Result{}, fmt.Errorf("llm returned 400: %s", msg)
}

// TestProbeFailsNoDowngrade is the critical test. When the probe also fails
// the definitions were not the cause, and the run must return the probe's
// error without any downgrade announcement on any of the three channels.
//
// Mutation that must make this test fail: remove the `if probeErr != nil {
// return }` branch in the turn loop (i.e. let the code reach announceDowngrade
// regardless of probe outcome). Confirm no other test fails on that mutation.
func TestProbeFailsNoDowngrade(t *testing.T) {
	workspace := t.TempDir()
	probeErrMsg := "temperature is deprecated for this model"
	client := &alwaysRefusingLLM{probeMessage: probeErrMsg}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var runErr error
	stderr := captureStderr(t, func() {
		_, runErr = sprout.Run(context.Background(), "test")
	})

	// The probe failed — no downgrade must be announced on any channel.
	if got := len(downgradeEvents(bus)); got != 0 {
		t.Errorf("EventSproutDowngraded count = %d, want 0 — probe failure must not cause downgrade", got)
	}
	if strings.Contains(stderr, "falling back to prose protocol") {
		t.Errorf("stderr contains downgrade warning but probe failed — no downgrade must be announced: %q", stderr)
	}
	if runErr == nil {
		t.Fatal("Run returned nil error, want the probe's error")
	}
	if !strings.Contains(runErr.Error(), probeErrMsg) {
		t.Errorf("runErr = %v, want to contain the probe's error message %q", runErr, probeErrMsg)
	}
}

// recordingAndRefusingLLM refuses any native call with tools, accepts the
// probe (no tools) with a parseable final answer, and records all native calls
// for inspection.
type recordingAndRefusingLLM struct {
	fakeLLM
	nativeCalls []struct {
		messages []llm.Message
		tools    []llm.ToolDefinition
	}
	probeAnswer string
}

func (f *recordingAndRefusingLLM) ToolDefinitionsCapable() bool { return true }

func (f *recordingAndRefusingLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	msgCopy := make([]llm.Message, len(messages))
	copy(msgCopy, messages)
	toolCopy := make([]llm.ToolDefinition, len(tools))
	copy(toolCopy, tools)
	f.nativeCalls = append(f.nativeCalls, struct {
		messages []llm.Message
		tools    []llm.ToolDefinition
	}{msgCopy, toolCopy})
	if tokenChan != nil {
		close(tokenChan)
	}
	if len(tools) > 0 {
		return llm.Result{}, fmt.Errorf("%w: llm returned 400: tools not supported", llm.ErrRejectedWithTools)
	}
	// Probe succeeds: return a parseable answer that must be discarded.
	return llm.Result{Text: f.probeAnswer}, nil
}

// TestProbeSendsCorrectMessages asserts that the probe carries the same
// messages as the refused turn and zero tool definitions, and that the probe's
// answer is discarded — the run continues in prose and returns the prose
// client's answer, not the probe's.
//
// Mutation for messages check: modify the probe call to send a different
// message set — the message-equality assertions fail. Mutation for discarded-
// answer: to verify the property is not already structurally guaranteed,
// assign the probe result to `response` in the turn loop — the response and
// transcript checks fail.
func TestProbeSendsCorrectMessages(t *testing.T) {
	workspace := t.TempDir()

	probeAnswerText := "probe answer that must be discarded"
	rec := &recordingAndRefusingLLM{probeAnswer: probeAnswerText}
	// The prose client returns the real final answer after downgrade.
	rec.fakeLLM.response = `{"final":"real answer"}`

	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", rec, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var res sproutResult
	var runErr error
	_ = captureStderr(t, func() {
		res, runErr = sprout.Run(context.Background(), "test task")
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	// Two native calls must have been made: refused turn and probe.
	if len(rec.nativeCalls) != 2 {
		t.Fatalf("native calls = %d, want 2 (refused + probe)", len(rec.nativeCalls))
	}

	// First call (refused) must carry tools.
	if len(rec.nativeCalls[0].tools) == 0 {
		t.Errorf("first native call (refused) carried no tools, want non-empty")
	}

	// Second call (probe) must carry zero tools.
	if len(rec.nativeCalls[1].tools) != 0 {
		t.Errorf("probe carried %d tools, want zero", len(rec.nativeCalls[1].tools))
	}

	// Both calls must carry the same messages — the probe is the same turn
	// without the definitions, not a different turn.
	msgs0 := rec.nativeCalls[0].messages
	msgs1 := rec.nativeCalls[1].messages
	if len(msgs0) != len(msgs1) {
		t.Fatalf("refused call had %d messages, probe had %d — they must match", len(msgs0), len(msgs1))
	}
	for i := range msgs0 {
		if msgs0[i].Role != msgs1[i].Role || msgs0[i].Content != msgs1[i].Content {
			t.Errorf("message[%d] differs: refused=%+v probe=%+v", i, msgs0[i], msgs1[i])
		}
	}

	// The probe asked a question; it did not take a turn. Its answer must not
	// reach the record of what the mind said.
	//
	// The transcript is the assertion that can catch this. A companion check on
	// res.Response was removed because it could not fail: the probe is followed
	// by a continue, which reassigns response before anything reads it, so the
	// answer is unreachable from there whatever the code does. An assertion
	// that cannot go red reads as coverage without being any.
	if strings.Contains(sprout.transcript.String(), probeAnswerText) {
		t.Errorf("probe answer leaked into transcript: %s", sprout.transcript.String())
	}

	// The run must have continued in prose and returned the prose client's answer.
	if res.Response != "real answer" {
		t.Errorf("Response = %q, want 'real answer'", res.Response)
	}
}

// TestAnnouncedReasonCarriesEndpointMessage asserts that the endpointMessage
// field in the downgrade event and on stderr carries the provider's own text,
// not a fixed string we invented. The reason field must remain the stable
// short value.
//
// Mutation: pass "" as endpointMessage to announceDowngrade in the proven-cause
// branch — both the stderr check and the event endpointMessage check fail.
func TestAnnouncedReasonCarriesEndpointMessage(t *testing.T) {
	workspace := t.TempDir()
	providerText := "tool_use is not supported for this model"
	client := &refusingLLM{refusalMessage: providerText}
	client.response = `{"final":"done"}`
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	var runErr error
	stderr := captureStderr(t, func() {
		_, runErr = sprout.Run(context.Background(), "test")
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}

	// The endpoint's own sentence must appear on stderr.
	if !strings.Contains(stderr, providerText) {
		t.Errorf("stderr = %q, want to contain the endpoint's own message %q", stderr, providerText)
	}

	// The endpoint's own sentence must appear in the event's endpointMessage field.
	events := downgradeEvents(bus)
	if len(events) != 1 {
		t.Fatalf("EventSproutDowngraded count = %d, want 1", len(events))
	}
	got, ok := events[0].Data["endpointMessage"].(string)
	if !ok {
		t.Fatalf("endpointMessage field missing or wrong type: %v", events[0].Data["endpointMessage"])
	}
	if !strings.Contains(got, providerText) {
		t.Errorf("endpointMessage = %q, want to contain %q", got, providerText)
	}
	// The reason field must remain the stable short value, not the provider's text.
	if reason := events[0].Data["reason"]; reason != "accepted without tool definitions" {
		t.Errorf("reason = %v, want 'accepted without tool definitions'", reason)
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
