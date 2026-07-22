package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/roots/llm"
)

type fakeLLM struct {
	responses []string
	calls     [][]llm.Message
	response  string
}

func (f *fakeLLM) Call(ctx context.Context, messages []llm.Message) (string, error) {
	callCopy := make([]llm.Message, len(messages))
	copy(callCopy, messages)
	f.calls = append(f.calls, callCopy)

	if len(f.responses) == 0 {
		return f.response, nil
	}

	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func (f *fakeLLM) CallStream(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (string, error) {
	callCopy := make([]llm.Message, len(messages))
	copy(callCopy, messages)
	f.calls = append(f.calls, callCopy)

	response := f.response
	if len(f.responses) > 0 {
		response = f.responses[0]
		f.responses = f.responses[1:]
	}
	if tokenChan != nil {
		tokenChan <- response
		close(tokenChan)
	}
	return response, nil
}

type fakeSession struct {
	tools      []ToolDefinition
	calls      []ToolCall
	toolResult string
}

func (f *fakeSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
	return f.tools, nil
}

func (f *fakeSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	f.calls = append(f.calls, call)
	switch call.Tool {
	case "readFile":
		return ToolResponse{
			Status: "success",
			Output: map[string]any{
				"path":    call.Arguments["path"],
				"content": "README contents",
			},
		}, nil
	default:
		return ToolResponse{Status: "success", Output: map[string]any{"tool": call.Tool, "result": f.toolResult}}, nil
	}
}

func (f *fakeSession) Close() error { return nil }

func (f *fakeSession) Logs() string { return "fake logs" }

func TestAgentRunsToolLoop(t *testing.T) {
	workspace := t.TempDir()
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	if err := os.MkdirAll(genomeDir, 0o755); err != nil {
		t.Fatalf("mkdir genome dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genomeDir, "notes.md"), []byte("Genome note"), 0o644); err != nil {
		t.Fatalf("write genome file: %v", err)
	}
	genotypeDir := filepath.Join(workspace, ".tendril", "genotypes")
	if err := os.MkdirAll(genotypeDir, 0o755); err != nil {
		t.Fatalf("mkdir genotype dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genotypeDir, "workspace-Sprout.json"), []byte(`{"name":"workspace-Sprout","instructions":"You are the workspace Sprout."}`), 0o644); err != nil {
		t.Fatalf("write genotype file: %v", err)
	}

	client := &fakeLLM{
		responses: []string{
			`{"tool":"readFile","arguments":{"path":"README.md"}}`,
			`{"final":"done"}`,
		},
	}
	session := &fakeSession{
		tools: []ToolDefinition{
			{
				Name:        "readFile",
				Description: "Read a file",
				Arguments: []ToolArgument{
					{Name: "path", Type: "string", Required: true},
				},
			},
		},
	}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}

	result, err := sprout.Run(context.Background(), "read the README")
	if err != nil {
		t.Fatalf("Sprout.Run returned error: %v", err)
	}

	if result.Response != "done" {
		t.Fatalf("expected final response done, got %q", result.Response)
	}
	if len(session.calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(session.calls))
	}
	if session.calls[0].Tool != "readFile" {
		t.Fatalf("expected readFile tool call, got %q", session.calls[0].Tool)
	}
	if got := session.calls[0].Arguments["path"]; got != "README.md" {
		t.Fatalf("expected README.md path, got %#v", got)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(client.calls))
	}
	lastCall := client.calls[1]
	if len(lastCall) == 0 || lastCall[len(lastCall)-1].Role != "user" {
		t.Fatalf("expected tool observation to be appended as user message, got %#v", lastCall)
	}
	if !strings.Contains(lastCall[len(lastCall)-1].Content, "Tool result for readFile") {
		t.Fatalf("tool observation missing from LLM history: %s", lastCall[len(lastCall)-1].Content)
	}
	if !strings.Contains(client.calls[0][0].Content, "Genome note") {
		t.Fatalf("system prompt missing genome context: %s", client.calls[0][0].Content)
	}
	if !strings.Contains(client.calls[0][0].Content, "You are the workspace Sprout.") {
		t.Fatalf("system prompt missing genotype context: %s", client.calls[0][0].Content)
	}
}

func TestParseModelResponseFinalText(t *testing.T) {
	call, isToolCall, final, actionResult, err := parseModelResponse("plain final response")
	if err != nil {
		t.Fatalf("parseModelResponse returned error: %v", err)
	}
	if isToolCall {
		t.Fatalf("expected plain text to not be parsed as a tool call")
	}
	if final != "plain final response" {
		t.Fatalf("expected final response to echo plain text, got %q", final)
	}
	if len(call) != 0 {
		t.Fatalf("expected no tool call, got %+v", call)
	}
	if actionResult != nil {
		t.Fatalf("expected no ACTION_RESULT for plain text, got %+v", actionResult)
	}
}

func TestParseModelResponseRepairsToolCallMissingClosingBraces(t *testing.T) {
	// Shape captured from a real run: the model's context window filled while
	// it was emitting the call, cutting off the final closing brace.
	truncated := `{"tool":"writeFile","arguments":{"path":"substrates.yaml.example","content":"# identity example\n"}`
	calls, isToolCall, final, _, err := parseModelResponse(truncated)
	if err != nil {
		t.Fatalf("parseModelResponse returned error: %v", err)
	}
	if !isToolCall || len(calls) != 1 {
		t.Fatalf("expected a repaired tool call, got isToolCall=%v final=%q", isToolCall, final)
	}
	if calls[0].Tool != "writeFile" {
		t.Fatalf("expected writeFile, got %q", calls[0].Tool)
	}
	if got := calls[0].Arguments["content"]; got != "# identity example\n" {
		t.Fatalf("repaired arguments must be intact, got %q", got)
	}
}

func TestParseModelResponseDoesNotRepairUnterminatedString(t *testing.T) {
	// A string cut off mid-value cannot be recovered without inventing
	// content, so it must fall through instead of writing a truncated file.
	truncated := `{"tool":"writeFile","arguments":{"path":"a.md","content":"partial conte`
	calls, isToolCall, _, _, err := parseModelResponse(truncated)
	if err != nil {
		t.Fatalf("parseModelResponse returned error: %v", err)
	}
	if isToolCall && len(calls) > 0 && calls[0].Tool == "writeFile" {
		if _, ok := calls[0].Arguments["content"]; ok {
			t.Fatalf("unterminated string must not be repaired into a write, got %+v", calls[0])
		}
	}
}

// inControlPlane runs fn with the process working directory set to a temporary
// one, so the control plane resolves somewhere the test owns and is distinct
// from the workspace it creates.
func inControlPlane(t *testing.T, fn func(controlPlaneRoot string)) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	fn(root)
}

func writeDefinition(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// A control-plane genotype wins over a workspace one of the same name, and only
// it is marked System.
func TestControlPlaneGenotypeWinsAndIsTrusted(t *testing.T) {
	inControlPlane(t, func(controlPlaneRoot string) {
		workspace := t.TempDir()
		genotypeName := "test-priority"

		writeDefinition(t, filepath.Join(controlPlaneRoot, ".tendril", "genotypes"),
			genotypeName+".json",
			`{"name":"test-priority","instructions":"I am the trusted genotype","denyPlasmids":["evilTool"]}`)
		writeDefinition(t, filepath.Join(workspace, ".tendril", "genotypes"),
			genotypeName+".json",
			`{"name":"test-priority","instructions":"I am the workspace override","denyPlasmids":[]}`)

		genotype, err := loadGenotypeContext(workspace, genotypeName)
		if err != nil {
			t.Fatalf("loadGenotypeContext failed: %v", err)
		}
		if genotype.Instructions != "I am the trusted genotype" {
			t.Errorf("expected the control-plane genotype, got %q", genotype.Instructions)
		}
		if len(genotype.DenyPlasmids) != 1 || genotype.DenyPlasmids[0] != "evilTool" {
			t.Errorf("expected denyPlasmids=[evilTool], got %v", genotype.DenyPlasmids)
		}
		if !genotype.System {
			t.Error("a control-plane genotype must be marked System")
		}
	})
}

// A workspace genotype is never System, whatever it contains.
func TestWorkspaceGenotypeIsNeverTrusted(t *testing.T) {
	inControlPlane(t, func(controlPlaneRoot string) {
		workspace := t.TempDir()
		writeDefinition(t, filepath.Join(workspace, ".tendril", "genotypes"), "only-here.json",
			`{"name":"only-here","instructions":"workspace","denyPlasmids":[]}`)

		genotype, err := loadGenotypeContext(workspace, "only-here")
		if err != nil {
			t.Fatalf("loadGenotypeContext failed: %v", err)
		}
		if genotype.System {
			t.Error("a workspace genotype was marked System")
		}
	})
}

// When the control plane IS the workspace, nothing there may be trusted: a
// Sprout editing that workspace could write it.
func TestCollapsedTiersTrustNothing(t *testing.T) {
	inControlPlane(t, func(controlPlaneRoot string) {
		writeDefinition(t, filepath.Join(controlPlaneRoot, ".tendril", "genotypes"), "collapsed.json",
			`{"name":"collapsed","instructions":"same directory","denyPlasmids":["evilTool"]}`)

		genotype, err := loadGenotypeContext(controlPlaneRoot, "collapsed")
		if err != nil {
			t.Fatalf("loadGenotypeContext failed: %v", err)
		}
		if genotype == nil {
			t.Fatal("the definition should still be found, just not trusted")
		}
		if genotype.System {
			t.Error("a definition was trusted while the control plane and workspace are the same directory")
		}
	})
}

func TestEmbeddedGenotypePriority(t *testing.T) {
	workspace := t.TempDir()
	genotypeName := "code-writer"

	origConfigHome := os.Getenv("XDG_CONFIG_HOME")
	t.Cleanup(func() { os.Setenv("XDG_CONFIG_HOME", origConfigHome) })
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(workspace, "sysconfig"))

	workspaceGenotypeDir := filepath.Join(workspace, ".tendril", "genotypes")
	if err := os.MkdirAll(workspaceGenotypeDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace genotype dir: %v", err)
	}
	workspaceContent := `{"name":"code-writer","instructions":"I am a mutable workspace override","denyPlasmids":[]}`
	if err := os.WriteFile(filepath.Join(workspaceGenotypeDir, genotypeName+".json"), []byte(workspaceContent), 0o644); err != nil {
		t.Fatalf("write workspace genotype: %v", err)
	}

	genotype, err := loadGenotypeContext(workspace, genotypeName)
	if err != nil {
		t.Fatalf("loadGenotypeContext failed: %v", err)
	}
	if genotype == nil {
		t.Fatalf("expected embedded genotype, got nil")
	}
	if genotype.Instructions == "I am a mutable workspace override" {
		t.Fatalf("workspace genotype override took priority over embedded system genotype")
	}
	if !genotype.System {
		t.Fatalf("expected embedded genotype to be marked system")
	}
}

func TestAgentDenyPlasmidsFilter(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	controlPlaneRoot := t.TempDir()
	if err := os.Chdir(controlPlaneRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	workspace := t.TempDir()
	writeDefinition(t, filepath.Join(controlPlaneRoot, ".tendril", "genotypes"), "secure.json",
		`{"name":"secure","instructions":"I am secure","denyPlasmids":["evilTool","injectPlasmidTarget"]}`)

	client := &fakeLLM{
		responses: []string{
			`{"tool":"evilTool"}`,
			`{"tool":"injectPlasmid","arguments":{"name":"injectPlasmidTarget"}}`,
			`{"final":"done"}`,
		},
	}
	session := &fakeSession{
		tools: []ToolDefinition{
			{Name: "evilTool"},
			{Name: "safeTool"},
			{Name: "injectPlasmid"},
		},
	}
	sprout, err := newSprout(context.Background(), workspace, workspace, "secure", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}

	if _, hasEvil := sprout.toolIndex["evilTool"]; hasEvil {
		t.Errorf("evilTool was not filtered out of toolIndex")
	}
	if _, hasSafe := sprout.toolIndex["safeTool"]; !hasSafe {
		t.Errorf("safeTool was incorrectly filtered out")
	}

	result, err := sprout.Run(context.Background(), "do it")
	if err != nil {
		t.Fatalf("Sprout.Run failed: %v", err)
	}

	if !strings.Contains(result.Transcript, "unsupported tool") || !strings.Contains(result.Transcript, "evilTool") {
		t.Errorf("expected transcript to contain error about unsupported tool evilTool, got: %s", result.Transcript)
	}
	if !strings.Contains(result.Transcript, "restricted by the active system genotype") {
		t.Errorf("expected transcript to contain error about restricted injectPlasmid target, got: %s", result.Transcript)
	}
}

func TestParseActionResult(t *testing.T) {
	// A response that includes an embedded ACTION_RESULT block.
	responseWithActionResult := `{"final":"I posted the comment. ACTION_RESULT {\"actionType\":\"github_comment\",\"target\":\"https://github.com/opentendril/opentendril/pull/117\",\"summary\":\"Posted changelog comment to PR #117.\",\"success\":true}"}`

	_, isToolCall, final, actionResult, err := parseModelResponse(responseWithActionResult)
	if err != nil {
		t.Fatalf("parseModelResponse returned error: %v", err)
	}
	if isToolCall {
		t.Fatalf("expected not a tool call")
	}
	if actionResult == nil {
		t.Fatalf("expected ACTION_RESULT to be parsed, got nil")
	}
	if actionResult.ActionType != "github_comment" {
		t.Errorf("expected actionType=github_comment, got %q", actionResult.ActionType)
	}
	if !actionResult.Success {
		t.Errorf("expected success=true")
	}
	// The ACTION_RESULT block should be stripped from the final response text
	if strings.Contains(final, "ACTION_RESULT") {
		t.Errorf("expected ACTION_RESULT to be stripped from final text, got: %q", final)
	}
}

func TestSequenceSystemFlag(t *testing.T) {
	tmp := t.TempDir()
	seqPath := filepath.Join(tmp, "system-seq.yaml")

	content := `
name: system-seq
system: true
steps:
  - id: step1
    transcript: "do something"
`
	if err := os.WriteFile(seqPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write sequence: %v", err)
	}

	seq, err := LoadSequence(seqPath)
	if err != nil {
		t.Fatalf("LoadSequence: %v", err)
	}
	if !seq.System {
		t.Errorf("expected System=true for sequence with system: true")
	}
	if seq.Name != "system-seq" {
		t.Errorf("expected name=system-seq, got %q", seq.Name)
	}
}

// A control-plane sequence is searched before a workspace one of the same name.
func TestControlPlaneSequenceTakesPriority(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	controlPlaneRoot := t.TempDir()
	if err := os.Chdir(controlPlaneRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	writeDefinition(t, filepath.Join(controlPlaneRoot, ".tendril", "sequences"), "pr-close-cycle.yaml",
		"name: pr-close-cycle\nsystem: true\nsteps:\n  - id: analyse\n    transcript: \"analyse commits\"\n")

	workspace := t.TempDir()
	candidates := sequencePathCandidates("pr-close-cycle", workspace, workspace)

	trustedPath := filepath.Join(".tendril", "sequences", "pr-close-cycle.yaml")
	workspacePath := filepath.Join(workspace, ".tendril", "sequences", "pr-close-cycle.yaml")

	trustedIdx, workspaceIdx := -1, -1
	for i, candidate := range candidates {
		if candidate == trustedPath && trustedIdx == -1 {
			trustedIdx = i
		}
		if candidate == workspacePath && workspaceIdx == -1 {
			workspaceIdx = i
		}
	}
	if trustedIdx == -1 {
		t.Fatalf("the control-plane sequence path is not a candidate: %v", candidates)
	}
	if workspaceIdx != -1 && trustedIdx > workspaceIdx {
		t.Errorf("control-plane sequence (idx %d) must precede the workspace one (idx %d)", trustedIdx, workspaceIdx)
	}
}

// A bus is not decoration: the Sprout streams only when it has one, so a nil bus
// makes a run emit nothing at all — no tokens, no reasoning, no way to tell a
// working sprout from a stuck one except a wall clock. Both sprout execution
// paths passed nil, so this pins the behaviour the wiring depends on.
func TestAgentPublishesProgressWhenGivenABus(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{response: "<thought>considering it</thought>\ndone"}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile", Description: "read a file"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}
	if _, err := sprout.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Sprout.Run returned error: %v", err)
	}

	// No sleep: the turn waits for its tokens to be published before it
	// returns, so the events are all here by now. A sleep would mean the
	// delivery is racy and the liveness signal untrustworthy.
	history := bus.History(100)
	if len(history) == 0 {
		t.Fatalf("the run published nothing; a supervisor would have nothing to observe")
	}

	published := map[eventbus.EventType]int{}
	for _, event := range history {
		published[event.Type]++
	}
	if published[eventbus.EventStreamToken] == 0 {
		t.Errorf("no %s events: the Sprout did not stream, so liveness is unobservable (got %v)", eventbus.EventStreamToken, published)
	}
	if published[eventbus.EventThoughtBranch] == 0 {
		t.Errorf("no %s events: reasoning is unobservable (got %v)", eventbus.EventThoughtBranch, published)
	}
}

// A run's actual actions must be observable, not just its bookends. Before
// tool-invoked events existed, a sprout could read, edit, and run commands and
// an observer would see only sprout-emerged/sprout-matured — no way to watch
// WHAT it did. This pins that every tool call the Sprout makes is published.
func TestAgentPublishesToolInvokedEvents(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{
		responses: []string{
			`{"tool":"readFile","arguments":{"path":"README.md"}}`,
			`{"final":"done"}`,
		},
	}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile", Description: "read a file"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}
	if _, err := sprout.Run(context.Background(), "read the readme"); err != nil {
		t.Fatalf("Sprout.Run returned error: %v", err)
	}

	var toolEvents []eventbus.Event
	for _, event := range bus.History(100) {
		if event.Type == eventbus.EventToolInvoked {
			toolEvents = append(toolEvents, event)
		}
	}
	if len(toolEvents) != 1 {
		t.Fatalf("expected exactly one %s event, got %d", eventbus.EventToolInvoked, len(toolEvents))
	}
	got := toolEvents[0].Data
	if got["tool"] != "readFile" {
		t.Errorf("tool-invoked event tool = %v, want readFile", got["tool"])
	}
	if got["status"] != "success" {
		t.Errorf("tool-invoked event status = %v, want success", got["status"])
	}
	if _, ok := got["observation"]; !ok {
		t.Errorf("tool-invoked event missing observation (got %v)", got)
	}
	// The event must be correlated to the run's session, or the per-session
	// "explain a run" query cannot retrieve it — the exact orphaning that left
	// Sprout telemetry present in the table but invisible to the surface.
	if toolEvents[0].SessionID != "session-1" {
		t.Errorf("tool-invoked event sessionID = %q, want session-1", toolEvents[0].SessionID)
	}
}

// A run must be explainable after the fact as one readable transcript, not only
// as a token stream a reviewer has to stitch back together. This pins that the
// Sprout publishes its assembled conversation once, correlated to the session.
func TestAgentPublishesTranscriptEvent(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{
		responses: []string{
			`{"tool":"readFile","arguments":{"path":"README.md"}}`,
			`{"final":"all done"}`,
		},
	}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile", Description: "read a file"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}
	if _, err := sprout.Run(context.Background(), "read the readme"); err != nil {
		t.Fatalf("Sprout.Run returned error: %v", err)
	}

	var transcripts []eventbus.Event
	for _, event := range bus.History(100) {
		if event.Type == eventbus.EventSproutTranscript {
			transcripts = append(transcripts, event)
		}
	}
	if len(transcripts) != 1 {
		t.Fatalf("expected exactly one %s event, got %d", eventbus.EventSproutTranscript, len(transcripts))
	}
	if transcripts[0].SessionID != "session-1" {
		t.Errorf("transcript event sessionID = %q, want session-1", transcripts[0].SessionID)
	}
	transcript, _ := transcripts[0].Data["transcript"].(string)
	// The transcript must actually carry the conversation: the task prompt, the
	// tool the Sprout called, and its final answer.
	for _, want := range []string{"read the readme", "readFile", "all done"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript missing %q; got:\n%s", want, transcript)
		}
	}
}

// Committing is the orchestrator's job. The in-terrarium gitCommit tool also
// cannot work (the mount is a git worktree whose gitdir is outside the
// container), so a gitCommit call must be answered with the managed-git policy
// and never forwarded to the tool session to fail.
func TestAgentInterceptsGitCommit(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{
		responses: []string{
			`{"tool":"gitCommit","arguments":{"message":"my work"}}`,
			`{"final":"done"}`,
		},
	}
	session := &fakeSession{tools: []ToolDefinition{
		{Name: "writeFile", Description: "write a file"},
		{Name: "gitCommit", Description: "Stage files and create a git commit."},
	}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}
	result, err := sprout.Run(context.Background(), "make and commit a change")
	if err != nil {
		t.Fatalf("Sprout.Run returned error: %v", err)
	}
	if result.Response != "done" {
		t.Fatalf("expected final response done, got %q", result.Response)
	}
	// The gitCommit call must never reach the tool session.
	for _, call := range session.calls {
		if call.Tool == "gitCommit" {
			t.Fatalf("gitCommit was forwarded to the tool session instead of being intercepted")
		}
	}
	// The Sprout's transcript must carry the managed-git policy so the model is
	// told commits are automatic rather than seeing a git error.
	if !strings.Contains(sprout.transcript.String(), "automatically commits") {
		t.Errorf("expected managed-git policy in the transcript, got:\n%s", sprout.transcript.String())
	}
}

// The other half of the contract: without a bus the Sprout takes the blocking
// path and publishes nothing. This documents why nil was never a neutral
// default.
func TestAgentWithoutABusIsSilent(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{response: "done"}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile", Description: "read a file"}}}
	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}
	if _, err := sprout.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Sprout.Run returned error: %v", err)
	}
}
