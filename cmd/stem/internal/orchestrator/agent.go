package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/llm"
	"github.com/opentendril/core/data/genotypes"
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
	CallStream(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (string, error)
}

type Agent struct {
	workspace       string
	genotypeContext string
	genomeContext   string
	client          llmCaller
	session         toolSession
	tools           []ToolDefinition
	toolIndex       map[string]ToolDefinition
	denyPlasmids    []string
	messages        []llm.Message
	transcript      strings.Builder
	eventBus        *eventbus.Bus
	stepID          string
}

type ActionResult struct {
	ActionType string   `json:"actionType"`
	Target     string   `json:"target"`
	Summary    string   `json:"summary"`
	Success    bool     `json:"success"`
	Verdict    string   `json:"verdict,omitempty"`
	Risks      []string `json:"risks,omitempty"`
}

type agentResult struct {
	Response     string
	Transcript   string
	ActionResult *ActionResult
}

func newAgent(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string) (*Agent, error) {
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
	var instructions string
	var denyPlasmids []string
	if genotypeContext != nil {
		instructions = strings.TrimSpace(genotypeContext.Instructions)
		denyPlasmids = genotypeContext.DenyPlasmids
	}

	tools, err := session.ListAvailableTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover sprout tools: %w", err)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("sprout reported no available tools")
	}

	toolIndex := make(map[string]ToolDefinition)
	var filteredTools []ToolDefinition
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}

		denied := false
		for _, deniedName := range denyPlasmids {
			if strings.EqualFold(tool.Name, deniedName) {
				denied = true
				break
			}
		}

		if !denied {
			toolIndex[tool.Name] = tool
			filteredTools = append(filteredTools, tool)
		}
	}
	if len(toolIndex) == 0 {
		return nil, fmt.Errorf("sprout tool discovery returned only empty or denied tool names")
	}

	return &Agent{
		workspace:       workspace,
		genotypeContext: instructions,
		genomeContext:   genomeContext,
		client:          client,
		session:         session,
		tools:           filteredTools,
		toolIndex:       toolIndex,
		denyPlasmids:    denyPlasmids,
		eventBus:        eventBus,
		stepID:          stepID,
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
		var tokenChan chan string
		var response string
		var err error

		if a.eventBus != nil {
			tokenChan = make(chan string, 100)
			go func() {
				for token := range tokenChan {
					a.eventBus.Publish(eventbus.Event{
						Type:   eventbus.EventStreamToken,
						Source: a.stepID,
						Data: map[string]interface{}{
							"token": token,
						},
					})
				}
			}()
			response, err = a.client.CallStream(ctx, a.messages, tokenChan)
		} else {
			response, err = a.client.Call(ctx, a.messages)
		}

		if err != nil {
			return agentResult{}, err
		}

		thoughtContent := extractThought(response)
		if thoughtContent != "" && a.eventBus != nil {
			a.eventBus.Publish(eventbus.Event{
				Type:   eventbus.EventThoughtBranch,
				Source: a.stepID,
				Data: map[string]interface{}{
					"thought": thoughtContent,
				},
			})
		}

		a.messages = append(a.messages, llm.Message{Role: "assistant", Content: response})
		a.appendTranscript("assistant", response)

		calls, isToolCall, finalResponse, actionResult, err := parseModelResponse(response)
		if err != nil {
			return agentResult{}, err
		}

		if strings.TrimSpace(finalResponse) != "" {
			return agentResult{
				Response:     strings.TrimSpace(finalResponse),
				Transcript:   a.transcript.String(),
				ActionResult: actionResult,
			}, nil
		}

		if !isToolCall {
			return agentResult{
				Response:     strings.TrimSpace(response),
				Transcript:   a.transcript.String(),
				ActionResult: actionResult,
			}, nil
		}

		var combinedObservation strings.Builder
		for _, call := range calls {
			_, obs, err := a.executeTool(ctx, call)
			if err != nil {
				return agentResult{}, err
			}
			if combinedObservation.Len() > 0 {
				combinedObservation.WriteString("\n\n")
			}
			combinedObservation.WriteString(obs)
		}

		a.messages = append(a.messages, llm.Message{Role: "user", Content: combinedObservation.String()})
		a.appendTranscript("user", combinedObservation.String())

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

func extractThought(response string) string {
	start := strings.Index(response, "<thought>")
	if start == -1 {
		return ""
	}
	end := strings.Index(response, "</thought>")
	if end == -1 {
		return strings.TrimSpace(response[start+9:])
	}
	return strings.TrimSpace(response[start+9 : end])
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

	if call.Tool == "injectPlasmid" {
		if nameRaw, ok := call.Arguments["name"]; ok {
			if name, ok := nameRaw.(string); ok {
				for _, denied := range a.denyPlasmids {
					if strings.EqualFold(name, denied) {
						response := ToolResponse{
							Status: "error",
							Error:  fmt.Sprintf("access denied: plasmid %q is restricted by the active system genotype", name),
						}
						return response, renderToolObservation(call.Tool, response), nil
					}
				}
			}
		}
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
- You should think about the problem before taking action. Enclose your reasoning inside <thought> and </thought> tags. Explain alternatives you considered and why you rejected them.
- Use only the listed tools.
- When you need a tool, respond with exactly one JSON object and nothing else (after your thought block).
- Tool calls must use the shape: {"tool":"name","arguments":{...}}.
- When the task is complete, respond with exactly one JSON object containing {"final":"..."} or plain final text.
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

func parseModelResponse(content string) ([]ToolCall, bool, string, *ActionResult, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, false, "", nil, nil
	}

	for {
		start := strings.Index(trimmed, "<thought>")
		end := strings.Index(trimmed, "</thought>")
		if start != -1 {
			if end != -1 {
				trimmed = strings.TrimSpace(trimmed[:start] + trimmed[end+10:])
			} else {
				trimmed = strings.TrimSpace(trimmed[:start])
			}
		} else {
			break
		}
	}

	candidate := stripCodeFences(trimmed)
	var decoded modelResponse
	if err := json.Unmarshal([]byte(candidate), &decoded); err != nil {
		// FALLBACK: If JSON parsing fails, attempt to extract markdown code blocks as synthetic ToolCalls.
		syntheticCalls := extractMarkdownSyntheticCalls(content)
		if len(syntheticCalls) > 0 {
			return syntheticCalls, true, "", nil, nil
		}
		return nil, false, trimmed, nil, nil
	}

	if strings.TrimSpace(decoded.Tool) != "" {
		if decoded.Arguments == nil {
			decoded.Arguments = map[string]any{}
		}
		return []ToolCall{{Tool: decoded.Tool, Arguments: decoded.Arguments}}, true, "", nil, nil
	}

	if strings.TrimSpace(decoded.Final) != "" {
		finalText := decoded.Final
		var actionResult *ActionResult

		// Look for `ACTION_RESULT` block in the final text
		// format: ```json ACTION_RESULT ... ``` or just embedded JSON after ACTION_RESULT
		idx := strings.Index(finalText, "ACTION_RESULT")
		if idx != -1 {
			// Find the JSON block after ACTION_RESULT
			openBrace := strings.Index(finalText[idx:], "{")
			if openBrace != -1 {
				openBrace += idx
				// Find matching close brace. A naive string index could fail if there are nested braces,
				// but for a flat struct this is often sufficient, or we just take the last '}'
				closeBrace := strings.LastIndex(finalText[openBrace:], "}")
				if closeBrace != -1 {
					closeBrace += openBrace
					jsonStr := finalText[openBrace : closeBrace+1]
					var ar ActionResult
					if err := json.Unmarshal([]byte(jsonStr), &ar); err == nil {
						actionResult = &ar
						// Optionally strip the ACTION_RESULT block from the final text
						finalText = strings.TrimSpace(finalText[:idx])
					}
				}
			}
		}
		return nil, false, finalText, actionResult, nil
	}

	return nil, false, trimmed, nil, nil
}

var markdownBlockRegex = regexp.MustCompile("(?s)```[a-zA-Z0-9]*\n(.*?)\n```")
var filePathRegex = regexp.MustCompile(`(?i)(?:^|//|#|/\*|<!--)\s*(?:path|file)?\s*:?\s*([a-zA-Z0-9_\-\./\\]+\.[a-zA-Z0-9]+)`)

func extractMarkdownSyntheticCalls(content string) []ToolCall {
	var calls []ToolCall
	matches := markdownBlockRegex.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		code := match[1]
		
		// Attempt to infer the file path from the first few lines
		lines := strings.SplitN(code, "\n", 5)
		var path string
		for _, line := range lines {
			if m := filePathRegex.FindStringSubmatch(line); len(m) > 1 {
				path = m[1]
				break
			}
		}
		
		if path != "" {
			calls = append(calls, ToolCall{
				Tool: "writeFile",
				Arguments: map[string]any{
					"path":    path,
					"content": code,
					"append":  false,
				},
			})
		}
	}
	return calls
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

func getSystemGenotypePaths(name string) []string {
	var paths []string
	if configDir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(configDir, "opentendril", "genotypes", name+".json"))
	}
	paths = append(paths, filepath.Join("/etc", "opentendril", "genotypes", name+".json"))
	return paths
}

func loadGenotypeContext(workspace string, genotypeName string) (*genotypeDefinition, error) {
	genotypeName = strings.TrimSpace(genotypeName)
	if genotypeName == "" {
		return nil, nil
	}

	var content []byte
	var err error
	var genotypePath string
	var systemGenotype bool

	for _, p := range getSystemGenotypePaths(genotypeName) {
		if c, errRead := os.ReadFile(p); errRead == nil {
			content = c
			genotypePath = p
			systemGenotype = true
			break
		}
	}

	if content == nil {
		embeddedPath := genotypeName + ".json"
		if c, errRead := genotypes.FS.ReadFile(embeddedPath); errRead == nil {
			content = c
			genotypePath = "embedded:" + embeddedPath
			systemGenotype = true
		}
	}

	if content == nil {
		genotypePath = filepath.Join(workspace, ".tendril", "genotypes", genotypeName+".json")
		content, err = os.ReadFile(genotypePath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("read genotype file %s: %w", genotypePath, err)
		}
	}

	var genotype genotypeDefinition
	if err := json.Unmarshal(content, &genotype); err != nil {
		return nil, fmt.Errorf("decode genotype %s: %w", genotypePath, err)
	}
	if systemGenotype {
		genotype.System = true
	}

	return &genotype, nil
}
