package conductor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/roots/llm"
)

// observedNativeWrapperReply is the reply that exposed this, recorded verbatim
// from an unattended run on the governed installation. The task was to append
// one blank line to README.md. The mind emitted its native wrapper instead of
// the shape the prose rules teach, never read the file, invented a whole README
// in the second call, and closed with a statement that the work was done.
//
// Nothing ran. The growth ended reporting that final sentence as its answer.
const observedNativeWrapperReply = `<function_calls>
[{"tool": "readFile", "arguments": {"path": "README.md"}}]
</function_calls>
<function_calls>
[{"tool": "writeFile", "arguments": {"path": "README.md", "content": "# Invented\n\nAn entire README it had never read.\n"}}]
</function_calls>

<final>Done. Added a single blank line to the end of README.md.</final>`

// unreadableWrapperReply carries a wrapper marker whose payload cannot be read
// as a call: the Anthropic XML form, which has not been observed here and is
// deliberately not implemented. It must still be refused rather than finalised.
const unreadableWrapperReply = `<function_calls>
<invoke name="writeFile">
<parameter name="path">README.md</parameter>
</invoke>
</function_calls>

<final>Done. Rewrote the file.</final>`

// The claim the issue asks for first: the closing statement in that reply is
// not the growth's answer. A reply that tried to call tools is an attempt.
func TestWrappedToolCallIsNeverTheFinalAnswer(t *testing.T) {
	calls, isToolCall, finalText, _, err := parseModelResponse(observedNativeWrapperReply)
	if err != nil {
		t.Fatalf("parseModelResponse: %v", err)
	}

	if strings.Contains(finalText, "Done. Added a single blank line") {
		t.Errorf("finalText = %q, want the closing statement not to be reported as the answer", finalText)
	}
	if !isToolCall {
		t.Fatal("isToolCall = false, want the wrapped calls to be recognised as calls")
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (readFile then writeFile)", len(calls))
	}
	if calls[0].Tool != "readFile" || calls[1].Tool != "writeFile" {
		t.Errorf("calls = %q/%q, want readFile/writeFile", calls[0].Tool, calls[1].Tool)
	}
	if got := calls[0].Arguments["path"]; got != "README.md" {
		t.Errorf("readFile path = %v, want README.md", got)
	}
}

// A wrapper we cannot read must still not be an answer. This is the half of the
// change that does not depend on the marker list being complete for extraction,
// only on it recognising an attempt.
func TestUnreadableAttemptIsRefusedNotFinalised(t *testing.T) {
	replies := map[string]string{
		"anthropic invoke form":              unreadableWrapperReply,
		"unparseable payload":                "<tool_call>\n{\"tool\": \"writeFile\", \"arguments\": {\"path\": \n</tool_call>",
		"truncated bare object":              `{"tool": "writeFile", "arguments": {"path": "README.md", "content": "half`,
		"provider-shaped object":             `{"name":"createFile","parameters":{"filePath":"HELLO.md","content":"Hello"}}`,
		"provider-shaped near object":        `{"name":"writeFile","parameters={"path":"HELLO","content":"Hello"}}`,
		"provider-shaped object after prose": "explanation text\n{\"name\":\"runCommand\",\"parameters\":{\"command\":\"...\"}}",
	}

	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			calls, isToolCall, finalText, _, err := parseModelResponse(reply)
			if !errors.Is(err, errUnusableReply) {
				t.Errorf("err = %v, want errUnusableReply", err)
			}
			if isToolCall || len(calls) > 0 {
				t.Errorf("calls = %+v, want none — nothing here is executable", calls)
			}
			if strings.TrimSpace(finalText) != "" {
				t.Errorf("finalText = %q, want empty — an unreadable attempt is not an answer", finalText)
			}
		})
	}
}

func TestProviderToolIntentUsesBoundedCorrectionWithoutTranslation(t *testing.T) {
	replies := map[string]string{
		"near-malformed writeFile":    `{"name":"writeFile","parameters={"path":"HELLO.md","content":"Hello"}}`,
		"embedded unknown runCommand": "explanation text\n{\"name\":\"runCommand\",\"parameters\":{\"command\":\"...\"}}",
	}

	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			client := &fakeLLM{responses: []string{reply, `{"final":"corrected"}`}}
			session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}}

			sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
			if err != nil {
				t.Fatalf("newSprout: %v", err)
			}
			result, runErr := sprout.Run(context.Background(), "create HELLO.md")
			if runErr != nil {
				t.Fatalf("Sprout.Run: %v", runErr)
			}
			if result.Response != "corrected" {
				t.Fatalf("Response = %q, want bounded correction to continue", result.Response)
			}
			if len(session.calls) != 0 {
				t.Fatalf("provider-shaped intent was translated or executed: %+v", session.calls)
			}
			if len(client.calls) != 2 {
				t.Fatalf("model turns = %d, want initial reply plus one correction turn", len(client.calls))
			}
			correction := client.calls[1][len(client.calls[1])-1].Content
			if !strings.Contains(correction, `{"tool":"name","arguments":{...}}`) {
				t.Fatalf("correction = %q, want canonical prose protocol shape", correction)
			}
			if !strings.Contains(correction, "writeFile") {
				t.Fatalf("correction = %q, want actual available tool catalog", correction)
			}
		})
	}
}

// The counterweight, and the reason the discriminator is not simply "did the
// decode fail". The protocol rules say a growth may end with "plain final
// text", so refusing every reply the decoder cannot read would end growths that
// had genuinely finished. Only an attempted call is refused.
func TestPlainProseIsStillAFinalAnswer(t *testing.T) {
	replies := map[string]string{
		"plain sentence":               "Done. I appended a blank line to the end of README.md.",
		"mentions the word":            `Done. I used the "tool" you listed to append the line.`,
		"mentions name and parameters": `The name and parameters are documented in the task; the file was already correct.`,
		"describes a function":         "I considered calling a function to do this, but the file already ended with a newline.",
	}

	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			_, isToolCall, finalText, _, err := parseModelResponse(reply)
			if err != nil {
				t.Fatalf("parseModelResponse: %v — plain prose must remain a legal ending", err)
			}
			if isToolCall {
				t.Error("isToolCall = true, want false")
			}
			if strings.TrimSpace(finalText) == "" {
				t.Error("finalText is empty, want the prose returned as the answer")
			}
		})
	}
}

// One bad shape is corrected rather than fatal: the mind is told what was wrong
// and given another turn, and a growth that recovers still succeeds.
func TestOneUnusableReplyIsCorrectedAndTheRunContinues(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{responses: []string{
		unreadableWrapperReply,
		`{"final":"done properly"}`,
	}}
	session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}, {Name: "readFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	res, runErr := sprout.Run(context.Background(), "test")
	if runErr != nil {
		t.Fatalf("Run: %v — one bad shape must be recoverable", runErr)
	}
	if res.Response != "done properly" {
		t.Errorf("Response = %q, want 'done properly'", res.Response)
	}

	// The correction has to reach the mind, and it has to say what to do rather
	// than only that something was wrong. Without this the retry re-asks an
	// identical prompt and the same shape comes back.
	if len(client.calls) < 2 {
		t.Fatalf("turns = %d, want at least 2 — the turn was never re-asked", len(client.calls))
	}
	secondTurn := client.calls[1]
	correction := secondTurn[len(secondTurn)-1].Content
	if !strings.Contains(correction, `{"tool":"name","arguments":{...}}`) {
		t.Errorf("correction = %q, want it to restate the shape", correction)
	}
	if !strings.Contains(correction, "<function_calls>") {
		t.Errorf("correction = %q, want it to name the wrappers to avoid", correction)
	}
	if !strings.Contains(correction, "writeFile") {
		t.Errorf("correction = %q, want the tool catalogue carried with it", correction)
	}
}

// Two in a row ends the growth, and the error names the cause. Letting it run
// to the iteration cap would reach the same verdict twenty turns later under
// the wrong name.
func TestTwoConsecutiveUnusableRepliesEndTheRun(t *testing.T) {
	workspace := t.TempDir()
	// No responses list, so the fallback answers every turn with the same shape.
	client := &fakeLLM{response: unreadableWrapperReply}
	session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	res, runErr := sprout.Run(context.Background(), "test")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("Run error = %v, want it to satisfy errors.Is(err, errUnusableReply)", runErr)
	}
	if strings.Contains(runErr.Error(), "max iterations") {
		t.Errorf("Run error = %v, want the cause named rather than the budget", runErr)
	}
	// Bounded: two attempts, not the whole budget. The literal is deliberate —
	// comparing against maxUnusableReplies would make this agree with whatever
	// the constant happens to say, including a value that lets a stuck growth
	// run to the iteration cap.
	if len(client.calls) != 2 {
		t.Errorf("turns spent = %d, want 2 — one correction, then the growth ends", len(client.calls))
	}
	// A growth that failed this way did not answer, and must not look as if it
	// had. This is the field the old fallthrough filled with the raw reply.
	if strings.TrimSpace(res.Response) != "" {
		t.Errorf("Response = %q, want empty on a failed growth", res.Response)
	}
}

func TestUnusableFailureAfterUnknownToolRefusalRemainsSalvageable(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{responses: []string{
		`{"tool":"writeFile","arguments":{"path":"HELLO.md","content":"partial"}}`,
		`{"tool":"cmp","arguments":{"path":"HELLO.md"}}`,
		unreadableWrapperReply,
		unreadableWrapperReply,
	}}
	session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	result, runErr := sprout.Run(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("Run error = %v, want bounded unusable-reply failure", runErr)
	}
	if !result.WroteWorkspace {
		t.Fatal("WroteWorkspace = false, want the governed write retained as evidence")
	}
	if result.BoundaryFailure {
		t.Fatal("BoundaryFailure = true, want a safely refused unknown tool to leave provenance salvageable")
	}
	if len(session.calls) != 1 || session.calls[0].Tool != "writeFile" {
		t.Fatalf("session calls = %+v, want only the governed write; refused cmp must not execute", session.calls)
	}
}

func TestMalformedNativeUnknownToolRefusalDoesNotPoisonSalvage(t *testing.T) {
	workspace := t.TempDir()
	client := &nativeFakeLLM{
		fakeLLM: fakeLLM{responses: []string{unreadableWrapperReply, unreadableWrapperReply}},
		nativeResponses: []llm.Result{
			{ToolCalls: []llm.ToolCall{{ID: "write", Type: "function", Function: llm.ToolCallFunction{
				Name: "writeFile", Arguments: `{"path":"HELLO.md","content":"partial"}`,
			}}}},
			{ToolCalls: []llm.ToolCall{{ID: "unsupported", Type: "function", Function: llm.ToolCallFunction{
				Name: "unsupportedTool", Arguments: `{malformed}`,
			}}}},
			{Text: unreadableWrapperReply},
			{Text: unreadableWrapperReply},
		},
	}
	session := &fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	result, runErr := sprout.Run(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("Run error = %v, want bounded unusable-reply failure", runErr)
	}
	if !result.WroteWorkspace {
		t.Fatal("WroteWorkspace = false, want the governed write retained as evidence")
	}
	if result.BoundaryFailure {
		t.Fatal("BoundaryFailure = true, want malformed unknown tool refusal to leave provenance salvageable")
	}
	if len(session.calls) != 1 || session.calls[0].Tool != "writeFile" {
		t.Fatalf("session calls = %+v, want only the governed write; malformed cmp must not execute", session.calls)
	}
}

// The counter is consecutive. A growth that slips, recovers, works for a while
// and slips again must not be ended on two unrelated slips far apart.
func TestUnusableCounterResetsOnAGoodTurn(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{responses: []string{
		unreadableWrapperReply,
		`{"tool":"readFile","arguments":{"path":"README.md"}}`,
		unreadableWrapperReply,
		`{"final":"recovered twice"}`,
	}}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}, {Name: "writeFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	res, runErr := sprout.Run(context.Background(), "test")
	if runErr != nil {
		t.Fatalf("Run: %v — two slips separated by a good turn must not end the growth", runErr)
	}
	if res.Response != "recovered twice" {
		t.Errorf("Response = %q, want 'recovered twice'", res.Response)
	}
	if len(session.calls) != 1 {
		t.Errorf("tools invoked = %d, want 1 — the good turn between the slips must still run", len(session.calls))
	}
}
