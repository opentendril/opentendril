package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

const (
	agentMaxIterations = 20
)

type ToolArgument struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Arguments   []ToolArgument `json:"arguments,omitempty"`
}

type ToolCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type ToolResponse struct {
	Status string `json:"status"`
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type toolSession interface {
	ListAvailableTools(ctx context.Context) ([]ToolDefinition, error)
	Call(ctx context.Context, call ToolCall) (ToolResponse, error)
	Close() error
	Logs() string
}

type llmCaller interface {
	Call(ctx context.Context, messages []llm.Message) (string, error)
}

type Agent struct {
	workspace       string
	genotypeContext string
	genomeContext   string
	client          llmCaller
	session         toolSession
	tools           []ToolDefinition
	toolIndex       map[string]ToolDefinition
	messages        []llm.Message
	transcript      strings.Builder
}

type agentResult struct {
	Response   string
	Transcript string
}

func newAgent(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession) (*Agent, error) {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	if strings.TrimSpace(genotypeRoot) == "" {
		genotypeRoot = workspace
	}
	if client == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if session == nil {
		return nil, fmt.Errorf("tool session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	genomeContext, err := loadGenomeContext(workspace)
	if err != nil {
		return nil, err
	}

	genotypeContext, err := loadGenotypeContext(genotypeRoot, genotypeName)
	if err != nil {
		return nil, err
	}

	tools, err := session.ListAvailableTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover sprout tools: %w", err)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("sprout reported no available tools")
	}

	toolIndex := make(map[string]ToolDefinition, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		toolIndex[tool.Name] = tool
	}
	if len(toolIndex) == 0 {
		return nil, fmt.Errorf("sprout tool discovery returned only empty tool names")
	}

	return &Agent{
		workspace:       workspace,
		genotypeContext: genotypeContext,
		genomeContext:   genomeContext,
		client:          client,
		session:         session,
		tools:           tools,
		toolIndex:       toolIndex,
	}, nil
}

func (a *Agent) Run(ctx context.Context, taskPrompt string) (agentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	systemPrompt := buildAgentSystemPrompt(a.workspace, a.genotypeContext, a.genomeContext, a.tools)
	a.messages = []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: strings.TrimSpace(taskPrompt)},
	}

	a.appendTranscript("system", systemPrompt)
	a.appendTranscript("user", taskPrompt)

	for iteration := 0; iteration < agentMaxIterations; iteration++ {
		response, err := a.client.Call(ctx, a.messages)
		if err != nil {
			return agentResult{}, err
		}

		a.messages = append(a.messages, llm.Message{Role: "assistant", Content: response})
		a.appendTranscript("assistant", response)

		call, isToolCall, finalResponse, err := parseModelResponse(response)
		if err != nil {
			return agentResult{}, err
		}

		if strings.TrimSpace(finalResponse) != "" {
			return agentResult{
				Response:   strings.TrimSpace(finalResponse),
				Transcript: a.transcript.String(),
			}, nil
		}

		if !isToolCall {
			return agentResult{
				Response:   strings.TrimSpace(response),
				Transcript: a.transcript.String(),
			}, nil
		}

		_, observation, err := a.executeTool(ctx, call)
		if err != nil {
			return agentResult{}, err
		}

		a.messages = append(a.messages, llm.Message{Role: "user", Content: observation})
		a.appendTranscript("user", observation)

		// Tool results are part of the conversation history; the next loop
		// iteration lets the model decide whether the task is complete.
	}

	return agentResult{}, fmt.Errorf("agent reached max iterations (%d)", agentMaxIterations)
}

func (a *Agent) appendTranscript(role string, content string) {
	role = strings.TrimSpace(role)
	content = strings.TrimSpace(content)
	if role == "" && content == "" {
		return
	}

	if a.transcript.Len() > 0 {
		a.transcript.WriteString("\n\n")
	}
	a.transcript.WriteString("[")
	a.transcript.WriteString(role)
	a.transcript.WriteString("]\n")
	a.transcript.WriteString(content)
}

func (a *Agent) executeTool(ctx context.Context, call ToolCall) (ToolResponse, string, error) {
	if strings.TrimSpace(call.Tool) == "" {
		return ToolResponse{}, "", fmt.Errorf("empty tool call received from model")
	}
	if _, ok := a.toolIndex[call.Tool]; !ok {
		response := ToolResponse{
			Status: "error",
			Error:  fmt.Sprintf("unsupported tool %q. available tools: %s", call.Tool, strings.Join(a.availableToolNames(), ", ")),
		}
		return response, renderToolObservation(call.Tool, response), nil
	}

	response, err := a.session.Call(ctx, call)
	if err != nil {
		return ToolResponse{}, "", err
	}

	return response, renderToolObservation(call.Tool, response), nil
}

func (a *Agent) availableToolNames() []string {
	names := make([]string, 0, len(a.toolIndex))
	for name := range a.toolIndex {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildAgentSystemPrompt(workspace string, genotypeContext string, genomeContext string, tools []ToolDefinition) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(`
You are the OpenTendril host-side ReAct loop.
You reason about tasks, choose tools, and stop when the task is complete.

Rules:
- Use only the listed tools.
- When you need a tool, respond with exactly one JSON object and nothing else.
- Tool calls must use the shape: {"tool":"name","arguments":{...}}.
- When the task is complete, respond with exactly one JSON object containing {"final":"..."} or plain final text.
- Do not reveal private chain-of-thought.
- Prefer concise, high-signal actions and responses.
`))
	builder.WriteString("\n\nWorkspace root:\n")
	builder.WriteString(strings.TrimSpace(workspace))

	if strings.TrimSpace(genotypeContext) != "" {
		builder.WriteString("\n\nLoaded genotype context:\n")
		builder.WriteString(strings.TrimSpace(genotypeContext))
	}

	builder.WriteString("\n\nAvailable tools:\n")
	builder.WriteString(formatToolCatalog(tools))

	if strings.TrimSpace(genomeContext) != "" {
		builder.WriteString("\n\nLoaded genome context:\n")
		builder.WriteString(strings.TrimSpace(genomeContext))
	} else {
		builder.WriteString("\n\nLoaded genome context:\n(no genome files found)")
	}

	return strings.TrimSpace(builder.String())
}

func formatToolCatalog(tools []ToolDefinition) string {
	if len(tools) == 0 {
		return "(none)"
	}

	var builder strings.Builder
	for _, tool := range tools {
		builder.WriteString("- ")
		builder.WriteString(tool.Name)
		if strings.TrimSpace(tool.Description) != "" {
			builder.WriteString(": ")
			builder.WriteString(strings.TrimSpace(tool.Description))
		}
		if len(tool.Arguments) > 0 {
			builder.WriteString("\n  arguments:\n")
			for _, argument := range tool.Arguments {
				builder.WriteString("  - ")
				builder.WriteString(argument.Name)
				builder.WriteString(" (")
				builder.WriteString(argument.Type)
				builder.WriteString(")")
				if argument.Required {
					builder.WriteString(" required")
				}
				if strings.TrimSpace(argument.Description) != "" {
					builder.WriteString(": ")
					builder.WriteString(strings.TrimSpace(argument.Description))
				}
				builder.WriteString("\n")
			}
		}
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String())
}

type modelResponse struct {
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Final     string         `json:"final,omitempty"`
}

func parseModelResponse(content string) (ToolCall, bool, string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ToolCall{}, false, "", nil
	}

	candidate := stripCodeFences(trimmed)
	var decoded modelResponse
	if err := json.Unmarshal([]byte(candidate), &decoded); err != nil {
		return ToolCall{}, false, trimmed, nil
	}

	if strings.TrimSpace(decoded.Tool) != "" {
		if decoded.Arguments == nil {
			decoded.Arguments = map[string]any{}
		}
		return ToolCall{Tool: decoded.Tool, Arguments: decoded.Arguments}, true, "", nil
	}

	if strings.TrimSpace(decoded.Final) != "" {
		return ToolCall{}, false, decoded.Final, nil
	}

	return ToolCall{}, false, trimmed, nil
}

func stripCodeFences(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}

	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 {
		return trimmed
	}

	if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		return strings.Join(lines[1:len(lines)-1], "\n")
	}
	return trimmed
}

func renderToolObservation(toolName string, response ToolResponse) string {
	pretty, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		pretty = []byte(fmt.Sprintf(`{"status":"error","error":"failed to marshal tool response: %v"}`, err))
	}

	return fmt.Sprintf("Tool result for %s:\n%s", toolName, string(pretty))
}

func loadGenomeContext(workspace string) (string, error) {
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	entries, err := os.ReadDir(genomeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read genome directory: %w", err)
	}

	type genomeFile struct {
		name    string
		content string
	}

	files := make([]genomeFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(genomeDir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read genome file %s: %w", path, err)
		}
		files = append(files, genomeFile{name: entry.Name(), content: string(content)})
	}

	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].name) < strings.ToLower(files[j].name)
	})

	if len(files) == 0 {
		return "", nil
	}

	var builder strings.Builder
	for idx, file := range files {
		if idx > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString("### ")
		builder.WriteString(file.name)
		builder.WriteString("\n")
		builder.WriteString(strings.TrimSpace(file.content))
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String()), nil
}

func loadGenotypeContext(workspace string, genotypeName string) (string, error) {
	genotypeName = strings.TrimSpace(genotypeName)
	if genotypeName == "" {
		return "", nil
	}

	genotypePath := filepath.Join(workspace, ".tendril", "genotypes", genotypeName+".json")
	content, err := os.ReadFile(genotypePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read genotype file %s: %w", genotypePath, err)
	}

	var genotype genotypeDefinition
	if err := json.Unmarshal(content, &genotype); err != nil {
		return "", fmt.Errorf("decode genotype %s: %w", genotypePath, err)
	}

	return strings.TrimSpace(genotype.Instructions), nil
}
