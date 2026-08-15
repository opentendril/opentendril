package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/data/genotypes"
	"github.com/opentendril/opentendril/roots/llm"
)

const (
	sproutMaxIterations = 20

	// maxUnusableReplies bounds how many times in a row a mind may answer with
	// a tool call the parser cannot read before the growth is ended. One
	// restatement of the rules is a fair chance to correct a shape; a mind that
	// ignores it twice is not going to comply on the third, and letting it run
	// to the iteration cap would spend the whole budget to reach the same
	// verdict with a vaguer error.
	maxUnusableReplies = 2
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
	CallWithResult(ctx context.Context, messages []llm.Message) (llm.Result, error)
	CallStreamWithResult(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (llm.Result, error)
}

type nativeCaller interface {
	CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error)
	ToolDefinitionsCapable() bool
}

type Sprout struct {
	workspace       string
	genotypeContext string
	genomeContext   string
	client          llmCaller
	nativeClient    nativeCaller
	session         toolSession
	tools           []ToolDefinition
	toolIndex       map[string]ToolDefinition
	denyPlasmids    []string
	msgMu           sync.RWMutex
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
	protocol  string
	// wroteWorkspace records that the model handed the terrarium a tool call
	// that can write to the workspace. It is set at the single seam every tool
	// call passes through, so it describes the model's own actions rather than
	// the state of the mount — which OpenTendril writes into before and after
	// every run, and which therefore cannot say who changed what.
	//
	// Atomic because a dormancy capture may look at a Sprout while its loop
	// goroutine is still running.
	wroteWorkspace atomic.Bool
	// requestBegun is set the first time a Mycorrhizal request is about to
	// be issued, so the structured "cognition has begun" signal fires once.
	requestBegun atomic.Bool
	// toolInvocations counts terrarium tool calls that actually ran.
	toolInvocations atomic.Int64
}

// readOnlyTerrariumTools names the terrarium tools that cannot write to the
// workspace.
//
// Membership is by exclusion deliberately: a tool name this set does not know
// counts as able to write. The two mistakes are not symmetric. Counting a
// harmless tool as a write inflates a run's file list with a diff it did not
// cause, which a reviewer sees and can reject; failing to count a real write
// drops the model's work out of the run's commit, and nobody sees what is not
// there. So an unrecognised tool — a genotype's own, or one added later — is
// credited to the model until someone says otherwise.
var readOnlyTerrariumTools = map[string]struct{}{
	"readFile":           {},
	"listFiles":          {},
	"gitDiff":            {},
	"listAvailableTools": {},
}

// toolCanWriteWorkspace reports whether handing this tool to the terrarium
// could change the workspace.
//
// A shell command counts, because nothing here can tell `ls` from `rm -rf`.
// That is a deliberate over-credit rather than an oversight: see
// readOnlyTerrariumTools for why the error is taken in this direction.
func toolCanWriteWorkspace(toolName string) bool {
	if _, readOnly := readOnlyTerrariumTools[strings.TrimSpace(toolName)]; readOnly {
		return false
	}
	return true
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
	Protocol     string
	// WroteWorkspace reports whether the model asked the terrarium for
	// anything that could change the workspace. It is the evidence behind
	// "did the model change anything", which a diff of the mount cannot
	// answer on its own.
	//
	// It is carried on failed runs too: a run cut off halfway still wrote
	// whatever it wrote, and the post-mortem commits that work.
	WroteWorkspace bool
	Usage          llm.Usage
	// RequestsMade is true when Sprout.Run issued at least one provider
	// request. It is the usageStarted fact, independent of whether Usage
	// fields were supplied.
	RequestsMade    bool
	ToolInvocations int
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

	nativeClient, _ := client.(nativeCaller)

	return &Sprout{
		workspace:       workspace,
		genotypeContext: instructions,
		genomeContext:   genomeContext,
		client:          client,
		nativeClient:    nativeClient,
		session:         session,
		tools:           filteredTools,
		toolIndex:       toolIndex,
		denyPlasmids:    denyPlasmids,
		eventBus:        eventBus,
		stepID:          stepID,
		sessionID:       sessionID,
		protocol:        "native",
	}, nil
}

// announceDowngrade records that this run is no longer carried natively, and
// says so three ways: on stderr for whoever is watching the Stem, as an event
// for whoever is watching the bus, and in the run's own outcome so a growth
// that produced nothing can answer "was the protocol to blame?" from its record.
//
// endpointMessage is the provider's own text and is surfaced alongside the
// stable reason so an operator reads what the endpoint said, not what we
// concluded. For the declared-incapable path it is empty: that classification
// comes from the operator's own configuration and needs no evidence.
//
// It also performs the demotion rather than only reporting it. Clearing
// nativeClient is what the rest of the turn loop reads to mean prose — it
// selects parseModelResponse over the native tool calls, and it stops the tool
// catalogue from being withheld from the prompt — so announcing without
// clearing would leave the prompt teaching one protocol while the parser
// expects the other, and the run would mature on its first prose tool call.
//
// Callers reach here at most once per run, because the only two paths into it
// both require a non-nil nativeClient.
func (a *Sprout) announceDowngrade(reason string, endpointMessage string) {
	if endpointMessage != "" {
		fmt.Fprintf(os.Stderr, "warning: endpoint rejected tool definitions (%s), falling back to prose protocol: %s\n", reason, endpointMessage)
	} else {
		fmt.Fprintf(os.Stderr, "warning: endpoint rejected tool definitions (%s), falling back to prose protocol\n", reason)
	}

	if a.eventBus != nil {
		data := map[string]interface{}{
			"reason": reason,
		}
		if endpointMessage != "" {
			data["endpointMessage"] = endpointMessage
		}
		a.eventBus.Publish(eventbus.Event{
			Type:      eventbus.EventSproutDowngraded,
			Source:    a.stepID,
			SessionID: a.sessionID,
			Data:      data,
		})
	}

	a.msgMu.Lock()
	a.protocol = "prose"
	// A turn already composed for the native protocol never taught the prose
	// rules, so the mind has to be taught them before it is asked again.
	// Re-teaching a prompt that already carries them would say everything twice.
	if len(a.messages) > 0 && !strings.Contains(a.messages[0].Content, proseProtocolRulesHeading) {
		a.messages[0].Content += "\n\n" + buildProseProtocolRules(a.tools)
	}
	a.msgMu.Unlock()

	a.nativeClient = nil
}

func (a *Sprout) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Publish the assembled conversation once on every exit path — a completed
	// run, an early error, or hitting max iterations — so the run is explainable
	// after the fact as a single transcript, not only as a token stream.
	defer a.publishTranscript()

	// A declared-incapable endpoint is known before the first turn is built, so
	// this one is settled here and the prompt below is composed for the prose
	// protocol from the start. A refusal discovered at request time is handled
	// in the turn loop, through the same method.
	if a.nativeClient != nil && !a.nativeClient.ToolDefinitionsCapable() {
		a.announceDowngrade("declared incapable by configuration", "")
		a.nativeClient = nil
	}

	systemPrompt := buildSproutSystemPrompt(a.workspace, a.genotypeContext, a.genomeContext)
	if a.nativeClient == nil {
		systemPrompt += "\n\n" + buildProseProtocolRules(a.tools)
		a.msgMu.Lock()
		a.protocol = "prose"
		a.msgMu.Unlock()
	}
	a.msgMu.Lock()
	a.messages = []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: strings.TrimSpace(taskPrompt)},
	}
	a.msgMu.Unlock()

	a.appendTranscript("system", systemPrompt)
	a.appendTranscript("user", taskPrompt)

	var mappedTools []llm.ToolDefinition
	if a.nativeClient != nil {
		mappedTools = mapToolsToNative(a.tools)
	}

	var runUsage llm.Usage
	var usageStarted bool

	unusableReplies := 0

	for iteration := 0; iteration < sproutMaxIterations; iteration++ {
		a.beginMycorrhizalRequest()
		var tokenChan chan string
		var response string
		var nativeToolCalls []llm.ToolCall
		var err error

		var requestUsage llm.Usage

		if a.eventBus != nil {
			tokenChan = make(chan string, 100)
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

			if a.nativeClient != nil {
				res, errCall := a.nativeClient.CallWithTools(ctx, a.messages, mappedTools, tokenChan)
				response = res.Text
				nativeToolCalls = res.ToolCalls
				requestUsage = res.Usage
				err = errCall
			} else {
				res, errCall := a.client.CallStreamWithResult(ctx, a.messages, tokenChan)
				response = res.Text
				requestUsage = res.Usage
				err = errCall
			}
			<-tokensPublished
		} else {
			if a.nativeClient != nil {
				res, errCall := a.nativeClient.CallWithTools(ctx, a.messages, mappedTools, nil)
				response = res.Text
				nativeToolCalls = res.ToolCalls
				requestUsage = res.Usage
				err = errCall
			} else {
				res, errCall := a.client.CallWithResult(ctx, a.messages)
				response = res.Text
				requestUsage = res.Usage
				err = errCall
			}
		}

		aggregateUsage(&runUsage, requestUsage, !usageStarted)
		usageStarted = true

		// The endpoint returned a client error on a request that carried tool
		// definitions. That is not proof the definitions were the cause — probe
		// the same turn without them to find out.
		//
		// Probe succeeds → the definitions were the cause. Announce the
		// downgrade with the original error's text as evidence, and put the
		// same turn again in prose. The refused attempt must not spend an
		// iteration: it produced nothing to reason about.
		//
		// Probe fails → the definitions were not the cause. Return the probe's
		// error. Nothing has been mutated at this point — announceDowngrade has
		// not run — so no demotion or announcement is made.
		if errors.Is(err, llm.ErrRejectedWithTools) {
			originalErrMsg := err.Error()
			a.msgMu.RLock()
			currentMessages := a.messages
			a.msgMu.RUnlock()
			a.beginMycorrhizalRequest()
			probeRes, probeErr := a.nativeClient.CallWithTools(ctx, currentMessages, nil, nil)
			aggregateUsage(&runUsage, probeRes.Usage, !usageStarted)
			usageStarted = true
			if probeErr != nil {
				// Probe also failed: definitions were not the cause.
				return a.finishedResult(runUsage, usageStarted), probeErr
			}
			// Probe succeeded: definitions were the cause.
			a.announceDowngrade("accepted without tool definitions", originalErrMsg)
			iteration--
			continue
		}

		if err != nil {
			return a.finishedResult(runUsage, usageStarted), err
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

		a.msgMu.Lock()
		a.messages = append(a.messages, llm.Message{
			Role:      "assistant",
			Content:   response,
			ToolCalls: nativeToolCalls,
		})
		a.msgMu.Unlock()
		a.appendTranscript("assistant", response)

		var calls []ToolCall
		var argErrors []string
		var isToolCall bool
		var finalResponse string
		var actionResult *ActionResult

		if a.nativeClient != nil {
			if len(nativeToolCalls) > 0 {
				isToolCall = true
				// Arguments that will not parse are recorded alongside the call
				// rather than inside it. A sentinel key smuggled through
				// Arguments would reach the tool-invoked event and the persisted
				// history as though the mind had really sent it, which makes the
				// evidence trail describe a call that was never made.
				argErrors = make([]string, len(nativeToolCalls))
				for i, ntc := range nativeToolCalls {
					var args map[string]any
					if err := json.Unmarshal([]byte(ntc.Function.Arguments), &args); err != nil {
						argErrors[i] = fmt.Sprintf("failed to parse arguments JSON: %v", err)
					}
					calls = append(calls, ToolCall{
						Tool:      ntc.Function.Name,
						Arguments: args,
					})
				}
			} else {
				// No tool calls means it's a final text natively.
				isToolCall = false
				finalResponse = response
			}
		} else {
			var parseErr error
			calls, isToolCall, finalResponse, actionResult, parseErr = parseModelResponse(response)
			if errors.Is(parseErr, errUnusableReply) {
				// The mind tried to call a tool in a shape we cannot execute.
				// Restating the rules is a fair chance to correct it, and this
				// turn is charged to the budget because the mind did answer —
				// unlike a refused request, which produced nothing.
				//
				// A second consecutive failure ends the growth naming the
				// cause. One restatement is a fair chance; a mind that ignores
				// it twice will not comply on the third, and the alternative is
				// spending the whole budget discovering that.
				unusableReplies++
				if unusableReplies >= maxUnusableReplies {
					return a.finishedResult(runUsage, usageStarted), fmt.Errorf("%w after %d consecutive attempts; last reply: %s", errUnusableReply, unusableReplies, strings.TrimSpace(response))
				}
				observation := buildUnusableReplyObservation(a.tools)
				a.msgMu.Lock()
				a.messages = append(a.messages, llm.Message{Role: "user", Content: observation})
				a.msgMu.Unlock()
				a.appendTranscript("user", observation)
				continue
			}
			if parseErr != nil {
				return a.finishedResult(runUsage, usageStarted), parseErr
			}
			// Counted consecutively: a mind that recovers has not failed twice,
			// and carrying the tally across a good turn would end a growth on
			// two unrelated slips many turns apart.
			unusableReplies = 0
		}

		if a.nativeClient != nil && !isToolCall {
			if strings.TrimSpace(finalResponse) != "" {
				finalResponse = stripThoughtBlock(finalResponse)
				finalResponse, actionResult = extractActionResult(finalResponse)
			}
		}

		if strings.TrimSpace(finalResponse) != "" {
			a.msgMu.RLock()
			reportedProtocol := a.protocol
			a.msgMu.RUnlock()
			result := a.finishedResult(runUsage, usageStarted)
			result.Response = strings.TrimSpace(finalResponse)
			result.Transcript = a.transcript.String()
			result.ActionResult = actionResult
			result.Protocol = reportedProtocol
			return result, nil
		}

		if !isToolCall {
			a.msgMu.RLock()
			reportedProtocol := a.protocol
			a.msgMu.RUnlock()
			result := a.finishedResult(runUsage, usageStarted)
			result.Response = strings.TrimSpace(response)
			result.Transcript = a.transcript.String()
			result.ActionResult = actionResult
			result.Protocol = reportedProtocol
			return result, nil
		}

		var combinedObservation strings.Builder
		for i, call := range calls {
			var resp ToolResponse
			var obs string

			if i < len(argErrors) && argErrors[i] != "" {
				resp = ToolResponse{
					Status: "error",
					Error:  argErrors[i],
				}
				obs = renderToolObservation(call.Tool, resp)
				// No tool-invoked event: nothing was invoked. The observation
				// still reaches the mind and the transcript, so the failure is
				// recorded — but a sign of life is not forged for a tool that
				// never ran, which is the one signal a supervisor may not be
				// misled about.
			} else {
				var err error
				resp, obs, err = a.executeTool(ctx, call)
				if err != nil {
					return a.finishedResult(runUsage, usageStarted), err
				}
				a.publishToolInvoked(call, resp, obs)
			}
			if combinedObservation.Len() > 0 {
				combinedObservation.WriteString("\n\n")
			}
			combinedObservation.WriteString(obs)

			if a.nativeClient != nil {
				// Native tools require observations to be tied to the ToolCallID
				a.msgMu.Lock()
				a.messages = append(a.messages, llm.Message{
					Role:       "tool",
					Content:    obs,
					ToolCallID: nativeToolCalls[i].ID,
				})
				a.msgMu.Unlock()
			}
		}

		if a.nativeClient == nil {
			a.msgMu.Lock()
			a.messages = append(a.messages, llm.Message{Role: "user", Content: combinedObservation.String()})
			a.msgMu.Unlock()
		}
		a.appendTranscript("user", combinedObservation.String())

		// Tool results are part of the conversation history; the next loop
		// iteration lets the model decide whether the task is complete.
	}

	return a.finishedResult(runUsage, usageStarted), fmt.Errorf("Sprout reached max iterations (%d)", sproutMaxIterations)
}

func (a *Sprout) finishedResult(usage llm.Usage, started bool) sproutResult {
	return sproutResult{
		WroteWorkspace:  a.wroteWorkspace.Load(),
		Usage:           usage,
		RequestsMade:    started,
		ToolInvocations: int(a.toolInvocations.Load()),
	}
}

// beginMycorrhizalRequest publishes the structured "first provider request
// has begun" signal exactly once per Sprout.Run. It is the live EventBus
// half of providerRequestAttempted.
func (a *Sprout) beginMycorrhizalRequest() {
	if a == nil || !a.requestBegun.CompareAndSwap(false, true) {
		return
	}
	if a.eventBus == nil {
		return
	}
	a.eventBus.Publish(eventbus.Event{
		Type:      eventbus.EventMycorrhizalRequestBegun,
		Source:    a.stepID,
		SessionID: a.sessionID,
		Data: map[string]interface{}{
			"stepId":                   a.stepID,
			"providerRequestAttempted": true,
		},
	})
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

// LastExchange returns the last request the Stem sent to the model and the
// last response it received, as raw strings. It reads the message history
// under a lock so it is safe to call from a capture goroutine concurrent with
// the model turn. Either value may be empty if the exchange has not yet
// completed.
func (a *Sprout) LastExchange() (request, response string) {
	if a == nil {
		return "", ""
	}
	a.msgMu.RLock()
	msgs := a.messages
	a.msgMu.RUnlock()

	// Walk backwards: the most recent assistant turn is the last response,
	// and the user message just before it is the request that prompted it.
	for i := len(msgs) - 1; i >= 0; i-- {
		if response == "" && msgs[i].Role == "assistant" {
			response = msgs[i].Content
			continue
		}
		if response != "" && msgs[i].Role == "user" {
			request = msgs[i].Content
			break
		}
	}
	return request, response
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

	// Recorded before the call, not after it: a tool call the terrarium never
	// finished — a watchdog kill mid-write — has still written, and the
	// post-mortem commits what it left behind. Everything above this line
	// returned without reaching the workspace, so nothing above it counts.
	if toolCanWriteWorkspace(call.Tool) {
		a.wroteWorkspace.Store(true)
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
	a.toolInvocations.Add(1)
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

// aggregateUsage folds one finalized request Usage into the run aggregate.
// A field is available on the run only when every request supplied it.
// Run-level cost is available only when amount, unit, and provenance are all
// present and match; an incomplete first request can never be completed later.
func aggregateUsage(run *llm.Usage, req llm.Usage, isFirst bool) {
	if isFirst {
		*run = req
		if run.CostAmount == nil || run.CostUnit == nil || run.CostProvenance == nil {
			run.CostAmount = nil
			run.CostUnit = nil
			run.CostProvenance = nil
		}
		return
	}

	if run.PromptTokens != nil && req.PromptTokens != nil {
		sum := *run.PromptTokens + *req.PromptTokens
		run.PromptTokens = &sum
	} else {
		run.PromptTokens = nil
	}

	if run.CompletionTokens != nil && req.CompletionTokens != nil {
		sum := *run.CompletionTokens + *req.CompletionTokens
		run.CompletionTokens = &sum
	} else {
		run.CompletionTokens = nil
	}

	if run.TotalTokens != nil && req.TotalTokens != nil {
		sum := *run.TotalTokens + *req.TotalTokens
		run.TotalTokens = &sum
	} else {
		run.TotalTokens = nil
	}

	if run.CostAmount != nil && req.CostAmount != nil &&
		run.CostUnit != nil && req.CostUnit != nil &&
		run.CostProvenance != nil && req.CostProvenance != nil &&
		*run.CostUnit == *req.CostUnit && *run.CostProvenance == *req.CostProvenance {
		sum, err := addDecimalStrings(*run.CostAmount, *req.CostAmount)
		if err == nil {
			run.CostAmount = &sum
		} else {
			run.CostAmount = nil
			run.CostUnit = nil
			run.CostProvenance = nil
		}
	} else {
		run.CostAmount = nil
		run.CostUnit = nil
		run.CostProvenance = nil
	}
}

// addDecimalStrings sums two provider-native decimal strings exactly.
// Parsing goes through big.Rat.SetString so scientific-notation JSON numbers
// such as 1e-6 stay exact; nothing is converted through float64 or big.Float.
func addDecimalStrings(a, b string) (string, error) {
	rA, ok := new(big.Rat).SetString(strings.TrimSpace(a))
	if !ok {
		return "", fmt.Errorf("invalid decimal: %s", a)
	}
	rB, ok := new(big.Rat).SetString(strings.TrimSpace(b))
	if !ok {
		return "", fmt.Errorf("invalid decimal: %s", b)
	}
	rA.Add(rA, rB)
	places := decimalPlacesOf(a)
	if p := decimalPlacesOf(b); p > places {
		places = p
	}
	return rA.FloatString(places), nil
}

// decimalPlacesOf is the number of fractional digits implied by a JSON number
// in either ordinary or scientific form. "1e-6" has 6 places, so the sum is
// formatted as 0.000001 rather than collapsing to 0.
func decimalPlacesOf(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	exp := 0
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		if e, err := strconv.Atoi(s[i+1:]); err == nil {
			exp = e
		}
		s = s[:i]
	}
	places := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		places = len(s) - i - 1
	}
	places -= exp
	if places < 0 {
		return 0
	}
	return places
}

// jsonSchemaProperty renders one sprout tool argument as a JSON Schema property.
//
// The sprout runtimes describe argument types in their own short vocabulary —
// "string", "number", "boolean", "string[]". Providers validate tool definitions
// against JSON Schema draft 2020-12, which has a closed set of type names and no
// array shorthand: a list of strings is {"type":"array","items":{"type":"string"}}.
// Passing the sprout's spelling through unchanged emitted "type":"string[]", which
// is not a JSON Schema type, and the provider rejected the whole request — so no
// tool definition reached it and every growth fell back to the prose protocol.
//
// An unrecognised type yields a property carrying its description and no type
// constraint. That is valid and permissive: the argument stays usable and the
// request stays valid, rather than one unknown spelling disabling native tool
// calling wholesale — which is the failure this replaces. Recognising a new type
// properly is the job of the vocabulary test, not of a runtime guess.
func jsonSchemaProperty(arg ToolArgument) map[string]any {
	prop := map[string]any{}
	switch arg.Type {
	case "string", "number", "integer", "boolean", "object":
		prop["type"] = arg.Type
	case "string[]":
		prop["type"] = "array"
		prop["items"] = map[string]any{"type": "string"}
	case "number[]":
		prop["type"] = "array"
		prop["items"] = map[string]any{"type": "number"}
	}
	if arg.Description != "" {
		prop["description"] = arg.Description
	}
	return prop
}

func mapToolsToNative(tools []ToolDefinition) []llm.ToolDefinition {
	var mapped []llm.ToolDefinition
	for _, tool := range tools {
		properties := make(map[string]any)
		var required []string
		for _, arg := range tool.Arguments {
			prop := jsonSchemaProperty(arg)
			properties[arg.Name] = prop
			if arg.Required {
				required = append(required, arg.Name)
			}
		}
		parameters := map[string]any{
			"type":       "object",
			"properties": properties,
		}
		if len(required) > 0 {
			parameters["required"] = required
		}
		mapped = append(mapped, llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return mapped
}

func buildSproutSystemPrompt(workspace string, genotypeContext string, genomeContext string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(`
You are the OpenTendril host-side ReAct loop.
You reason about tasks, choose tools, and stop when the task is complete.

Rules:
- You should think about the problem before taking action. Enclose your reasoning inside <thought> and </thought> tags. Explain alternatives you considered and why you rejected them.
- Prefer concise, high-signal actions and responses.
`))
	builder.WriteString("\n\nWorkspace root:\n")
	builder.WriteString(strings.TrimSpace(workspace))

	if strings.TrimSpace(genotypeContext) != "" {
		builder.WriteString("\n\nLoaded genotype context:\n")
		builder.WriteString(strings.TrimSpace(genotypeContext))
	}

	if strings.TrimSpace(genomeContext) != "" {
		builder.WriteString("\n\nLoaded genome context:\n")
		builder.WriteString(strings.TrimSpace(genomeContext))
	} else {
		builder.WriteString("\n\nLoaded genome context:\n(no genome files found)")
	}

	return strings.TrimSpace(builder.String())
}

// proseProtocolRulesHeading opens the block buildProseProtocolRules emits. It
// is named so that a prompt can be asked whether it has already been taught
// the prose protocol without that question depending on the wording below.
const proseProtocolRulesHeading = "Protocol Rules:"

// buildUnusableReplyObservation tells the mind what was wrong with the reply it
// just gave, in the same channel a tool observation arrives on. It restates the
// shape rather than only complaining, because the failure it answers is a mind
// reaching for a wrapper it was trained on instead of the shape it was taught —
// which a reminder can fix and a rebuke cannot. The tool catalogue goes with it
// so the correction does not depend on the system prompt still being attended
// to many turns later.
func buildUnusableReplyObservation(tools []ToolDefinition) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(`
Your last reply looked like a tool call but could not be read as one, so nothing was run.
Do not wrap tool calls in tags such as <function_calls>, <invoke> or <tool_call>.
Respond with exactly one JSON object and nothing else: {"tool":"name","arguments":{...}}.
If the task is already complete, respond with {"final":"..."} instead.
`))
	builder.WriteString("\n\nAvailable tools:\n")
	builder.WriteString(formatToolCatalog(tools))
	return strings.TrimSpace(builder.String())
}

func buildProseProtocolRules(tools []ToolDefinition) string {
	var builder strings.Builder
	builder.WriteString(proseProtocolRulesHeading + "\n")
	builder.WriteString(strings.TrimSpace(`
- Use only the listed tools.
- When you need a tool, respond with exactly one JSON object and nothing else (after your thought block).
- Tool calls must use the shape: {"tool":"name","arguments":{...}}.
- When the task is complete, respond with exactly one JSON object containing {"final":"..."} or plain final text.
`))
	builder.WriteString("\n\nAvailable tools:\n")
	builder.WriteString(formatToolCatalog(tools))
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

// errUnusableReply reports a reply that tried to call a tool and could not be
// read as one. It is distinct from a final answer, which is the whole point:
// the protocol rules invite "plain final text" as an ending, so every reply the
// parser cannot decode used to arrive at the same return as a finished run. A
// reply carrying a half-formed writeFile then ended the growth reporting
// success, having written nothing.
//
// It is not returned for every parse failure. Plain prose is still a legal
// ending, so only a reply showing an attempted call is refused — see
// looksLikeToolCallAttempt for what counts and what that deliberately misses.
var errUnusableReply = errors.New("model reply attempted a tool call that could not be read")

func parseModelResponse(content string) ([]ToolCall, bool, string, *ActionResult, error) {
	trimmed := stripThoughtBlock(strings.TrimSpace(content))

	// Checked before anything else: a reply carrying both a tool call and a
	// closing statement is asking for the call, and reading the statement
	// instead is how a growth ends "done" without doing it. An attempt outranks
	// a final in the same reply.
	if wrapped := extractWrappedToolCalls(trimmed); len(wrapped) > 0 {
		return wrapped, true, "", nil, nil
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
			if looksLikeToolCallAttempt(trimmed) {
				return nil, false, "", nil, errUnusableReply
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
		finalText, actionResult := extractActionResult(decoded.Final)
		return nil, false, finalText, actionResult, nil
	}

	// Decoded as JSON but named neither a tool nor a final. The same question
	// applies as above: an object shaped like a call the decoder could not bind
	// is an attempt, not an answer.
	if looksLikeToolCallAttempt(trimmed) {
		return nil, false, "", nil, errUnusableReply
	}

	return nil, false, trimmed, nil, nil
}

// toolCallKeyRegex matches the "tool" key of our own protocol shape. The colon
// is required so that a final answer mentioning the word in quotes — "I used
// the \"tool\" you listed" — is not mistaken for a call.
var toolCallKeyRegex = regexp.MustCompile(`"tool"\s*:`)

// toolCallWrapperMarkers are the openings of wrappers a mind reaches for when
// it has native tool-calling trained into it and is being asked to speak the
// prose protocol instead. They are listed because each was chosen by a model
// rather than by us, which is also their limit: a wrapper not named here still
// falls through to "plain final text". That is a gap in a list we own and can
// extend, not the parser's designed behaviour, and the work-detection condition
// still refuses to count a growth in which nothing was written.
var toolCallWrapperMarkers = []string{"<function_calls", "<invoke", "<tool_call"}

// looksLikeToolCallAttempt reports whether a reply the parser could not decode
// was nonetheless trying to call a tool. The question is structural — does this
// carry a tool-invocation shape — and every shape it asks about is either our
// own protocol's or a wrapper we have seen emitted. It never asks what the text
// says.
func looksLikeToolCallAttempt(content string) bool {
	if toolCallKeyRegex.MatchString(content) {
		return true
	}
	for _, marker := range toolCallWrapperMarkers {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

var wrappedToolCallRegex = regexp.MustCompile(`(?s)<(?:function_calls|tool_call)>(.*?)</(?:function_calls|tool_call)>`)

// extractWrappedToolCalls reads calls out of the wrappers a native-trained mind
// emits under the prose protocol. It handles the JSON payload that has actually
// been observed — an array or a single object of our own {tool, arguments}
// shape — and deliberately does not implement the XML <invoke>/<parameter> form,
// which has not been seen here. That form still trips looksLikeToolCallAttempt,
// so it fails the turn loudly instead of being read as a finished answer, which
// is the behaviour that matters until there is a real reply to build from.
func extractWrappedToolCalls(content string) []ToolCall {
	var calls []ToolCall
	for _, match := range wrappedToolCallRegex.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		payload := stripCodeFences(strings.TrimSpace(match[1]))

		var batch []modelResponse
		if strings.HasPrefix(payload, "[") {
			if err := json.Unmarshal([]byte(payload), &batch); err != nil {
				continue
			}
		} else {
			var single modelResponse
			if err := json.Unmarshal([]byte(payload), &single); err != nil {
				continue
			}
			batch = []modelResponse{single}
		}

		for _, decoded := range batch {
			if strings.TrimSpace(decoded.Tool) == "" {
				continue
			}
			if decoded.Arguments == nil {
				decoded.Arguments = map[string]any{}
			}
			calls = append(calls, ToolCall{Tool: decoded.Tool, Arguments: decoded.Arguments})
		}
	}
	return calls
}

func extractActionResult(finalText string) (string, *ActionResult) {
	idx := strings.Index(finalText, "ACTION_RESULT")
	if idx != -1 {
		openBrace := strings.Index(finalText[idx:], "{")
		if openBrace != -1 {
			openBrace += idx
			closeBrace := strings.LastIndex(finalText[openBrace:], "}")
			if closeBrace != -1 {
				closeBrace += openBrace
				jsonStr := finalText[openBrace : closeBrace+1]
				var ar ActionResult
				if err := json.Unmarshal([]byte(jsonStr), &ar); err == nil {
					return strings.TrimSpace(finalText[:idx]), &ar
				}
			}
		}
	}
	return finalText, nil
}

func stripThoughtBlock(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
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
	return trimmed
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

// getSystemGenotypePaths returns the trusted locations for a named genotype.
// It is empty when the control plane is not distinct from the workspace, so a
// workspace genotype can never be marked System.
func getSystemGenotypePaths(workspace, name string) []string {
	dirs := TrustedDefinitionDirs(workspace, DefinitionKindGenotypes)
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(dir, name+".json"))
	}
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

	for _, p := range getSystemGenotypePaths(workspace, genotypeName) {
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
		genotypePath = filepath.Join(workspace, controlPlaneDirName, DefinitionKindGenotypes, genotypeName+".json")
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
