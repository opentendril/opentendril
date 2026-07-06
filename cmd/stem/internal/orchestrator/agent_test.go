package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/llm"
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
	if tokenChan != nil {
		tokenChan <- f.response
		close(tokenChan)
	}
	return f.response, nil
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
	if err := os.WriteFile(filepath.Join(genotypeDir, "workspace-agent.json"), []byte(`{"name":"workspace-agent","instructions":"You are the workspace agent."}`), 0o644); err != nil {
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

	agent, err := newAgent(context.Background(), workspace, workspace, "workspace-agent", client, session, nil, "")
	if err != nil {
		t.Fatalf("newAgent returned error: %v", err)
	}

	result, err := agent.Run(context.Background(), "read the README")
	if err != nil {
		t.Fatalf("agent.Run returned error: %v", err)
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
	if !strings.Contains(client.calls[0][0].Content, "You are the workspace agent.") {
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

func TestSystemGenotypePriority(t *testing.T) {
	workspace := t.TempDir()
	genotypeName := "test-priority"

	// Mock UserConfigDir via environment variable for tests
	origConfigHome := os.Getenv("XDG_CONFIG_HOME")
	t.Cleanup(func() { os.Setenv("XDG_CONFIG_HOME", origConfigHome) })

	sysConfigDir := filepath.Join(workspace, "sysconfig")
	os.Setenv("XDG_CONFIG_HOME", sysConfigDir)

	sysGenotypeDir := filepath.Join(sysConfigDir, "opentendril", "genotypes")
	os.MkdirAll(sysGenotypeDir, 0o755)
	sysContent := `{"name":"test-priority","instructions":"I am the system genotype","denyPlasmids":["evilTool"]}`
	os.WriteFile(filepath.Join(sysGenotypeDir, genotypeName+".json"), []byte(sysContent), 0o644)

	workspaceGenotypeDir := filepath.Join(workspace, ".tendril", "genotypes")
	os.MkdirAll(workspaceGenotypeDir, 0o755)
	workspaceContent := `{"name":"test-priority","instructions":"I am the workspace override","denyPlasmids":[]}`
	os.WriteFile(filepath.Join(workspaceGenotypeDir, genotypeName+".json"), []byte(workspaceContent), 0o644)

	genotype, err := loadGenotypeContext(workspace, genotypeName)
	if err != nil {
		t.Fatalf("loadGenotypeContext failed: %v", err)
	}

	if genotype.Instructions != "I am the system genotype" {
		t.Errorf("expected system genotype instructions, got %q", genotype.Instructions)
	}
	if len(genotype.DenyPlasmids) != 1 || genotype.DenyPlasmids[0] != "evilTool" {
		t.Errorf("expected denyPlasmids=[evilTool], got %v", genotype.DenyPlasmids)
	}
	if !genotype.System {
		t.Errorf("expected genotype loaded from system path to be marked system")
	}
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
	workspace := t.TempDir()

	origConfigHome := os.Getenv("XDG_CONFIG_HOME")
	t.Cleanup(func() { os.Setenv("XDG_CONFIG_HOME", origConfigHome) })
	sysConfigDir := filepath.Join(workspace, "sysconfig")
	os.Setenv("XDG_CONFIG_HOME", sysConfigDir)

	sysGenotypeDir := filepath.Join(sysConfigDir, "opentendril", "genotypes")
	os.MkdirAll(sysGenotypeDir, 0o755)
	sysContent := `{"name":"secure","instructions":"I am secure","denyPlasmids":["evilTool","injectPlasmidTarget"]}`
	os.WriteFile(filepath.Join(sysGenotypeDir, "secure.json"), []byte(sysContent), 0o644)

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
	agent, err := newAgent(context.Background(), workspace, workspace, "secure", client, session, nil, "")
	if err != nil {
		t.Fatalf("newAgent returned error: %v", err)
	}

	if _, hasEvil := agent.toolIndex["evilTool"]; hasEvil {
		t.Errorf("evilTool was not filtered out of toolIndex")
	}
	if _, hasSafe := agent.toolIndex["safeTool"]; !hasSafe {
		t.Errorf("safeTool was incorrectly filtered out")
	}

	result, err := agent.Run(context.Background(), "do it")
	if err != nil {
		t.Fatalf("agent.Run failed: %v", err)
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
	responseWithActionResult := `{"final":"I posted the comment. ACTION_RESULT {\"actionType\":\"github_comment\",\"target\":\"https://github.com/opentendril/core/pull/117\",\"summary\":\"Posted changelog comment to PR #117.\",\"success\":true}"}`

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

func TestSystemSequencePathResolution(t *testing.T) {
	// Create a temp dir to act as XDG_CONFIG_HOME
	configHome := t.TempDir()
	origConfigHome := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", configHome)
	t.Cleanup(func() { os.Setenv("XDG_CONFIG_HOME", origConfigHome) })

	// Create a system sequence in the XDG config dir
	sysSeqDir := filepath.Join(configHome, "opentendril", "sequences")
	if err := os.MkdirAll(sysSeqDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sysSeqPath := filepath.Join(sysSeqDir, "pr-close-cycle.yaml")
	seqContent := `name: pr-close-cycle
system: true
steps:
  - id: analyse
    transcript: "analyse commits"
`
	if err := os.WriteFile(sysSeqPath, []byte(seqContent), 0o644); err != nil {
		t.Fatalf("write system sequence: %v", err)
	}

	// workspace has NO .tendril/sequences directory
	workspace := t.TempDir()
	cwd := workspace

	// sequencePathCandidates should include the system path
	candidates := sequencePathCandidates("pr-close-cycle", cwd, workspace)

	found := false
	for _, c := range candidates {
		if c == sysSeqPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected system sequence path %q to be in candidates, got: %v", sysSeqPath, candidates)
	}

	// Verify system path appears before workspace path
	sysIdx := -1
	wsIdx := -1
	wsSeqPath := filepath.Join(workspace, ".tendril", "sequences", "pr-close-cycle.yaml")
	for i, c := range candidates {
		if c == sysSeqPath {
			sysIdx = i
		}
		if c == wsSeqPath {
			wsIdx = i
		}
	}
	if wsIdx != -1 && sysIdx != -1 && sysIdx > wsIdx {
		t.Errorf("expected system sequence (idx %d) to have higher priority than workspace sequence (idx %d)", sysIdx, wsIdx)
	}
}
