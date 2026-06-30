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
}

func (f *fakeLLM) Call(ctx context.Context, messages []llm.Message) (string, error) {
	callCopy := make([]llm.Message, len(messages))
	copy(callCopy, messages)
	f.calls = append(f.calls, callCopy)

	if len(f.responses) == 0 {
		return "", nil
	}

	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

type fakeSession struct {
	tools []ToolDefinition
	calls []ToolCall
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
		return ToolResponse{Status: "success", Output: map[string]any{"tool": call.Tool}}, nil
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
	if err := os.WriteFile(filepath.Join(genotypeDir, "meristem.json"), []byte(`{"name":"meristem","instructions":"You are the meristem."}`), 0o644); err != nil {
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

	agent, err := newAgent(context.Background(), workspace, workspace, "meristem", client, session)
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
	if !strings.Contains(client.calls[0][0].Content, "You are the meristem.") {
		t.Fatalf("system prompt missing genotype context: %s", client.calls[0][0].Content)
	}
}

func TestParseModelResponseFinalText(t *testing.T) {
	call, isToolCall, final, err := parseModelResponse("plain final response")
	if err != nil {
		t.Fatalf("parseModelResponse returned error: %v", err)
	}
	if isToolCall {
		t.Fatalf("expected plain text to not be parsed as a tool call")
	}
	if final != "plain final response" {
		t.Fatalf("expected final response to echo plain text, got %q", final)
	}
	if call.Tool != "" {
		t.Fatalf("expected no tool call, got %+v", call)
	}
}
