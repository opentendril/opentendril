package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/data/genotypes"
	"github.com/opentendril/opentendril/roots/llm"
)

const (
	sproutMaxIterations = 20
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

type Sprout struct {
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
	// sessionID correlates every published event with the run's session so
	// the per-session "explain a run" query (historydb.LoadEvents) can retrieve
	// them. Without it the Sprout's tokens, thoughts, and tool calls are
	// persisted with an empty sessionId and orphaned from the run they belong
	// to — present in the table, invisible to the surface meant to show them.
	sessionID string
}

type ActionResult struct {
	ActionType string   `json:"actionType"`
	Target     string   `json:"target"`
	Summary    string   `json:"summary"`
	Success    bool     `json:"success"`
	Verdict    string   `json:"verdict,omitempty"`
	Risks      []string `json:"risks,omitempty"`
}

type sproutResult struct {
	Response     string
	Transcript   string
	ActionResult *ActionResult
}

func newSprout(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string, sessionID string) (*Sprout, error) {
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

	return &Sprout{
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
		sessionID:       sessionID,
	}, nil
}

func (a *Sprout) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Publish the assembled conversation once on every exit path — a completed
	// run, an early error, or hitting max iterations — so the run is explainable
	// after the fact as a single transcript, not only as a token stream.
	defer a.publishTranscript()

	systemPrompt := buildSproutSystemPrompt(a.workspace, a.genotypeContext, a.genomeContext, a.tools)
	a.messages = []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: strings.TrimSpace(taskPrompt)},
	}

	a.appendTranscript("system", systemPrompt)
	a.appendTranscript("user", taskPrompt)

	for iteration := 0; iteration < sproutMaxIterations; iteration++ {
		var tokenChan chan string
		var response string
		var err error

		if a.eventBus != nil {
			tokenChan = make(chan string, 100)
			// Publishing happens on another goroutine, so the turn must wait
			// for it to drain before moving on. Without the wait a token could
			// be published after the events that conclude the run, or dropped
			// entirely when a short-lived caller shuts the bus down — which
			// makes the liveness signal exactly as untrustworthy as no signal.
			// CallStream closes the channel on every path, so this cannot hang.
			tokensPublished := make(chan struct{})
			go func() {
				defer close(tokensPublished)
				for token := range tokenChan {
					a.eventBus.Publish(eventbus.Event{
						Type:      eventbus.EventStreamToken,
						Source:    a.stepID,
						SessionID: a.sessionID,
						Data: map[string]interface{}{
							"token": token,
						},
					})
				}
			}()
			response, err = a.client.CallStream(ctx, a.messages, tokenChan)
			<-tokensPublished
		} else {
			response, err = a.client.Call(ctx, a.messages)
		}

		if err != nil {
			return sproutResult{}, err
		}

		thoughtContent := extractThought(response)
		if thoughtContent != "" && a.eventBus != nil {
			a.eventBus.Publish(eventbus.Event{
				Type:      eventbus.EventThoughtBranch,
				Source:    a.stepID,
				SessionID: a.sessionID,
				Data: map[string]interface{}{
					"thought": thoughtContent,
				},
			})
		}

		a.messages = append(a.messages, llm.Message{Role: "assistant", Content: response})
		a.appendTranscript("assistant", response)

		calls, isToolCall, finalResponse, actionResult, err := parseModelResponse(response)
		if err != nil {
			return sproutResult{}, err
		}

		if strings.TrimSpace(finalResponse) != "" {
			return sproutResult{
				Response:     strings.TrimSpace(finalResponse),
				Transcript:   a.transcript.String(),
				ActionResult: actionResult,
			}, nil
		}

		if !isToolCall {
			return sproutResult{
				Response:     strings.TrimSpace(response),
				Transcript:   a.transcript.String(),
				ActionResult: actionResult,
			}, nil
		}

		var combinedObservation strings.Builder
		for _, call := range calls {
			response, obs, err := a.executeTool(ctx, call)
			if err != nil {
				return sproutResult{}, err
			}
			a.publishToolInvoked(call, response, obs)
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

	return sproutResult{}, fmt.Errorf("Sprout reached max iterations (%d)", sproutMaxIterations)
}

func (a *Sprout) appendTranscript(role string, content string) {
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

func (a *Sprout) executeTool(ctx context.Context, call ToolCall) (ToolResponse, string, error) {
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

	// Committing is the orchestrator's job, not the Sprout's. Every run's file
	// changes are committed and merged back after the Sprout finishes
	// (commitTerrariumExecution), with the substrate's configured identity and
	// signing. The in-terrarium gitCommit tool also cannot work: the workspace
	// is mounted as a git worktree whose .git file points at a host gitdir that
	// does not exist inside the container, so git reports "not a git
	// repository". Rather than let the Sprout hit that cryptic error and burn
	// turns retrying, answer here with the policy: the commit is handled, keep
	// editing.
	if call.Tool == "gitCommit" {
		response := managedGitCommitResponse()
		return response, renderToolObservation(call.Tool, response), nil
	}

	response, err := a.session.Call(ctx, call)
	if err != nil {
		return ToolResponse{}, "", err
	}

	return response, renderToolObservation(call.Tool, response), nil
}

// managedGitCommitResponse answers a gitCommit tool call with the run's git
// policy: OpenTendril commits and merges the changes after the run, so the
// Sprout neither needs to nor can commit inside the terrarium. It is a success,
// not an error — the Sprout's intent (make the changes durable) is satisfied by
// the orchestrator — so the loop moves on instead of retrying a commit that
// cannot work.
func managedGitCommitResponse() ToolResponse {
	return ToolResponse{
		Status: "success",
		Output: map[string]any{
			"committed": false,
			"managedBy": "opentendril",
			"message": "OpenTendril automatically commits and merges this run's changes after it finishes, " +
				"using the substrate's configured identity and signing. A manual commit inside the terrarium is " +
				"neither needed nor supported — your edited files are already captured. Keep editing; do not retry committing.",
		},
	}
}

// maxToolObservationEventBytes bounds the observation carried on a
// tool-invoked event so a single large tool result (e.g. a full file read)
// cannot bloat the event stream or the history row.
const maxToolObservationEventBytes = 2000

// publishToolInvoked emits one tool-invoked event per action the Sprout takes,
// so a run's actual actions are observable live and in history rather than
// leaving only the sprout-emerged/sprout-matured bookends. It is a no-op when
// no bus is wired (workspace and test callers), matching the other publishers.
func (a *Sprout) publishToolInvoked(call ToolCall, response ToolResponse, observation string) {
	if a.eventBus == nil {
		return
	}
	status := strings.TrimSpace(response.Status)
	if status == "" {
		status = "unknown"
	}
	obs := strings.TrimSpace(observation)
	if len(obs) > maxToolObservationEventBytes {
		obs = obs[:maxToolObservationEventBytes] + "…"
	}
	a.eventBus.Publish(eventbus.Event{
		Type:      eventbus.EventToolInvoked,
		Source:    a.stepID,
		SessionID: a.sessionID,
		Data: map[string]interface{}{
			"tool":        call.Tool,
			"arguments":   call.Arguments,
			"status":      status,
			"observation": obs,
		},
	})
}

// publishTranscript emits the Sprout's assembled conversation once when a run
// ends, correlated to the run's session so the per-session "explain a run"
// query can return one readable record. It is a no-op without a bus (the
// workspace and test callers) or an empty transcript.
func (a *Sprout) publishTranscript() {
	if a.eventBus == nil {
		return
	}
	transcript := strings.TrimSpace(a.transcript.String())
	if transcript == "" {
		return
	}
	a.eventBus.Publish(eventbus.Event{
		Type:      eventbus.EventSproutTranscript,
		Source:    a.stepID,
		SessionID: a.sessionID,
		Data: map[string]interface{}{
			"transcript": transcript,
		},
	})
}

func (a *Sprout) availableToolNames() []string {
	names := make([]string, 0, len(a.toolIndex))
	for name := range a.toolIndex {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildSproutSystemPrompt(workspace string, genotypeContext string, genomeContext string, tools []ToolDefinition) string {
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
		repaired, ok := repairToolCallMissingBraces(candidate)
		if ok {
			decoded = repaired
		} else {
			// FALLBACK: If JSON parsing fails, attempt to extract markdown code blocks as synthetic ToolCalls.
			syntheticCalls := extractMarkdownSyntheticCalls(content)
			if len(syntheticCalls) > 0 {
				return syntheticCalls, true, "", nil, nil
			}
			return nil, false, trimmed, nil, nil
		}
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

// repairToolCallMissingBraces recovers a tool call whose trailing closing
// braces were cut off. A local model whose context window fills mid-generation
// stops wherever it stands; when everything but the final braces made it out,
// the call is unambiguous, and dropping it would end the run with a false
// "nothing changed". Only braces are appended — never a quote — so a string
// cut off mid-value can never be silently completed with truncated content.
func repairToolCallMissingBraces(candidate string) (modelResponse, bool) {
	if !strings.HasPrefix(candidate, "{") {
		return modelResponse{}, false
	}
	repaired := candidate
	for range 4 {
		repaired += "}"
		var decoded modelResponse
		if err := json.Unmarshal([]byte(repaired), &decoded); err != nil {
			continue
		}
		if strings.TrimSpace(decoded.Tool) != "" {
			return decoded, true
		}
		return modelResponse{}, false
	}
	return modelResponse{}, false
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

// Byte budgets for genome context injected into the system prompt.
//
// The genome directory can hold files far larger than a local model's context
// window. Local inference servers silently truncate an oversized prompt FROM THE
// FRONT, deleting the Sprout rules and tool catalog and leaving the genome tail,
// so the model answers in prose instead of calling tools.
//
// Measured against a 4096-token window: prompts up to roughly eleven kilobytes
// drive tools reliably, past seventeen they never do. Fixed rules, workspace and
// tool catalog cost about two kilobytes, so an eight-kilobyte genome budget
// leaves room for the task and the first observations.
//
// Files stay complete on disk; each truncation marker tells the Sprout where to
// readFile the rest.
const (
	genomePerFileByteBudget = 4 * 1024
	genomeTotalByteBudget   = 8 * 1024
	// Below this many remaining bytes a fragment carries no signal, so the
	// file is omitted (with a pointer to its path) rather than truncated.
	genomeMinimumFragmentBytes = 256
)

// isGeneratedGenomeFile reports whether a genome file is a machine-generated
// map OpenTendril writes for itself rather than guidance curated for the
// Sprout. Generated maps are never inlined into the system prompt — only named
// with their on-disk path. Inlining even a small fragment of the repository
// map measurably degraded tool use on a weaker local model (2 of 3 and then 0
// of 2 first turns became prose documents instead of tool calls, against 3 of
// 3 tool calls with curated files alone): a symbol dump right before the
// model's turn primes document-writing while carrying almost no task signal.
// The full map stays on disk where the Sprout can read exactly the part it
// needs.
func isGeneratedGenomeFile(name string) bool {
	switch strings.ToLower(name) {
	case repositoryMapFile, memoryMapFile:
		return true
	}
	return false
}

// truncateGenomeContent cuts content to fit budget on a line boundary and
// points the Sprout at the on-disk file, which remains complete and readable
// through the readFile tool.
func truncateGenomeContent(name string, content string, budget int) string {
	if len(content) <= budget {
		return content
	}
	cut := content[:budget]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		cut = cut[:idx]
	}
	return cut + "\n[truncated — read .tendril/genome/" + name + " for the full content]"
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
	remaining := genomeTotalByteBudget
	var onDiskOnly []string
	for _, file := range files {
		if isGeneratedGenomeFile(file.name) {
			onDiskOnly = append(onDiskOnly, ".tendril/genome/"+file.name)
			continue
		}
		content := strings.TrimSpace(file.content)
		if remaining < genomeMinimumFragmentBytes {
			onDiskOnly = append(onDiskOnly, ".tendril/genome/"+file.name)
			continue
		}
		budget := genomePerFileByteBudget
		if remaining < budget {
			budget = remaining
		}
		content = truncateGenomeContent(file.name, content, budget)
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString("### ")
		builder.WriteString(file.name)
		builder.WriteString("\n")
		builder.WriteString(content)
		builder.WriteString("\n")
		remaining -= len(content)
	}
	if len(onDiskOnly) > 0 {
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString("Additional genome files on disk (use readFile if needed): ")
		builder.WriteString(strings.Join(onDiskOnly, ", "))
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
