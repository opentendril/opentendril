package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/llm"
)

const (
	defaultParallelSproutCount = 5
	maxParallelSproutCount     = 8

	// mycelialResponseExcerptLimit caps how much of each sprout response is
	// embedded into the MycelialMerge consensus transcript.
	mycelialResponseExcerptLimit = 4000
)

var (
	// meristemBranchingTimeout bounds the Map-phase coordinator LLM call.
	meristemBranchingTimeout = 2 * time.Minute
	// sproutGrowthTimeout bounds a single parallel sprout, including every
	// LLM call it makes inside its terrarium.
	sproutGrowthTimeout = 20 * time.Minute

	branchSubTasksFn           = branchSubTasks
	newMeristemBranchingClient = func() llmCaller {
		return (&DockerOrchestrator{IsCoordinator: true}).resolveLLMClient()
	}
)

// mycelialSubTask is one Map-phase shard produced by the meristem coordinator.
type mycelialSubTask struct {
	ID         string `json:"id"`
	Transcript string `json:"transcript"`
}

type sproutGrowthResult struct {
	index      int
	subTaskID  string
	branchName string
	response   string
	merged     bool
	err        error
}

// sproutStatusUpdate flows over the status channel so the orchestrator can
// track every concurrent terrarium in real time without joining on it.
type sproutStatusUpdate struct {
	index      int
	branchName string
	phase      eventbus.EventType
	detail     string
}

// runParallelSprouting executes one `parallel: true` sequence step as a
// Map-Reduce: the meristem coordinator branches the transcript into sub-tasks
// (Map), a fixed pool grows one ephemeral sprout terrarium per sub-task on an
// isolated branch (Grow), and a final MycelialMerge consensus sprout
// reconciles the surviving branches into the host substrate (Reduce).
//
// A withered sprout (panic, container crash, LLM timeout) never fails the
// step as long as at least one sibling matures.
func runParallelSprouting(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string, bus *eventbus.Bus) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if seq == nil || step == nil {
		return "", fmt.Errorf("parallel sprouting requires a sequence and a step")
	}

	sourcePath := repoRoot(substratePath)
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = strings.TrimSpace(substratePath)
	}
	if strings.TrimSpace(sourcePath) == "" {
		return "", fmt.Errorf("parallel sprouting requires a substrate path")
	}
	if !isGitRepo(sourcePath) {
		return "", fmt.Errorf("parallel sprouting requires a git repository at %s", sourcePath)
	}

	sproutCount := step.SproutCount
	if sproutCount <= 0 {
		sproutCount = defaultParallelSproutCount
	}
	if sproutCount > maxParallelSproutCount {
		sproutCount = maxParallelSproutCount
	}

	// ── Map ──────────────────────────────────────────────────────────────
	subTasks := branchSubTasksFn(ctx, step, sproutCount)
	if len(subTasks) == 0 {
		return "", fmt.Errorf("parallel sprouting produced no sub-tasks for step %s", step.ID)
	}

	publishParallelSproutingEvent(bus, eventbus.EventParallelSprouting, seq, step, map[string]interface{}{
		"phase":       "map",
		"sproutCount": len(subTasks),
	})

	branchBase := derivedSequenceBranch(seq.Branch, step.ID)
	if branchBase == "" {
		branchBase = sanitizeBranchComponent(step.ID)
		if branchBase == "" {
			branchBase = "parallel-sprouting"
		}
	}

	llmSelection := resolveStepLLMSelection(ctx, step)

	// ── Grow ─────────────────────────────────────────────────────────────
	growthCtx, cancelGrowth := context.WithCancel(ctx)
	defer cancelGrowth()

	jobs := make(chan int, len(subTasks))
	for i := range subTasks {
		jobs <- i
	}
	close(jobs)

	resultCh := make(chan sproutGrowthResult, len(subTasks))
	statusCh := make(chan sproutStatusUpdate, len(subTasks)*4)

	var wg sync.WaitGroup
	for lane := 0; lane < len(subTasks); lane++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				resultCh <- growParallelSprout(growthCtx, step, sourcePath, branchBase, subTasks[index], index, llmSelection, statusCh)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
		close(statusCh)
	}()

	// Multiplex growth results and live terrarium status on one select loop.
	results := make([]sproutGrowthResult, 0, len(subTasks))
	for resultCh != nil || statusCh != nil {
		select {
		case result, ok := <-resultCh:
			if !ok {
				resultCh = nil
				continue
			}
			results = append(results, result)
		case update, ok := <-statusCh:
			if !ok {
				statusCh = nil
				continue
			}
			publishParallelSproutingEvent(bus, update.phase, seq, step, map[string]interface{}{
				"sproutIndex": update.index,
				"branchName":  update.branchName,
				"detail":      update.detail,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].index < results[j].index })

	var withered []sproutGrowthResult
	var matured []sproutGrowthResult
	for _, result := range results {
		if result.err != nil {
			withered = append(withered, result)
			continue
		}
		matured = append(matured, result)
	}

	if len(matured) == 0 {
		witherErrs := make([]error, 0, len(withered))
		for _, result := range withered {
			witherErrs = append(witherErrs, result.err)
		}
		return "", fmt.Errorf("parallel sprouting for step %s failed: all %d sprouts withered: %w", step.ID, len(results), errors.Join(witherErrs...))
	}

	// ── Reduce (MycelialMerge) ───────────────────────────────────────────
	publishParallelSproutingEvent(bus, eventbus.EventMycelialMerge, seq, step, map[string]interface{}{
		"phase":         "reduce",
		"maturedCount":  len(matured),
		"witheredCount": len(withered),
	})

	mergeCtx := context.WithoutCancel(ctx)
	for i := range matured {
		mergeStepID := step.ID + "-sprout-" + strconv.Itoa(matured[i].index)
		if mergeErr := mergePhloemChannelToHostFn(mergeCtx, sourcePath, matured[i].branchName, mergeStepID); mergeErr != nil {
			fmt.Fprintf(os.Stderr, "⚠️ MycelialMerge could not graft branch %s: %v\n", matured[i].branchName, mergeErr)
			continue
		}
		matured[i].merged = true
	}

	consensusTranscript := step.MergeTranscript
	if consensusTranscript == "" {
		consensusTranscript = buildMycelialMergeTranscript(step, subTasks, matured, withered)
	}

	consensusOrch := &DockerOrchestrator{
		Substrate:       sourcePath,
		SubstrateBranch: derivedSequenceBranch(seq.Branch, step.ID+"-mycelial-merge"),
		StepID:          step.ID + "-mycelial-merge",
		IsCoordinator:   true,
		Genotype:        "meristem",
		EventBus:        bus,
	}

	consensus, err := runSequenceSproutFn(ctx, consensusOrch, consensusTranscript)
	if err != nil {
		return "", fmt.Errorf("mycelial merge for step %s failed: %w", step.ID, err)
	}

	publishParallelSproutingEvent(bus, eventbus.EventMycelialMerge, seq, step, map[string]interface{}{
		"phase":         "complete",
		"maturedCount":  len(matured),
		"witheredCount": len(withered),
	})

	return consensus, nil
}

// growParallelSprout grows one sprout terrarium on its own shadow worktree
// branch. It always returns a result — panics from a crashed container or a
// misbehaving terrarium provider are recovered and reported as a withered
// sprout, so a single failure can never bring down the Stem.
func growParallelSprout(ctx context.Context, step *SequenceStep, sourcePath, branchBase string, subTask mycelialSubTask, index int, llmSelection stepLLMSelection, statusCh chan<- sproutStatusUpdate) (result sproutGrowthResult) {
	branchName := branchBase + "-sprout-" + strconv.Itoa(index)
	result = sproutGrowthResult{index: index, subTaskID: subTask.ID, branchName: branchName}

	defer func() {
		if r := recover(); r != nil {
			result.err = fmt.Errorf("sprout %d (%s) withered from panic: %v", index, branchName, r)
			statusCh <- sproutStatusUpdate{index: index, branchName: branchName, phase: eventbus.EventSproutWithered, detail: result.err.Error()}
		}
	}()

	statusCh <- sproutStatusUpdate{index: index, branchName: branchName, phase: eventbus.EventSproutEmerged, detail: subTask.ID}

	sproutCtx, cancel := context.WithTimeout(ctx, sproutGrowthTimeout)
	defer cancel()

	shadowPath, err := createShadowWorktreeFn(sourcePath, branchName)
	if err != nil {
		result.err = fmt.Errorf("create sprout worktree %s: %w", branchName, err)
		statusCh <- sproutStatusUpdate{index: index, branchName: branchName, phase: eventbus.EventSproutWithered, detail: result.err.Error()}
		return result
	}
	injectMycorrhizalCacheFn(sourcePath, shadowPath)
	defer removeShadowWorktreeFn(sourcePath, shadowPath)

	orch := &DockerOrchestrator{
		Substrate:        sourcePath,
		SubstrateBranch:  branchName,
		StepID:           step.ID + "-sprout-" + strconv.Itoa(index),
		Genotype:         stepGenotype(step.ID),
		DisableMergeBack: true,
	}
	applyStepLLMSelection(orch, llmSelection)

	runResult, runErr := runSequenceSproutAtPathFn(sproutCtx, orch, subTask.Transcript, sourcePath, shadowPath)
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) && ctx.Err() == nil {
			result.err = fmt.Errorf("sprout %d (%s) withered: growth timed out after %s: %w", index, branchName, sproutGrowthTimeout, runErr)
		} else {
			result.err = fmt.Errorf("sprout %d (%s) withered: %w", index, branchName, runErr)
		}
		statusCh <- sproutStatusUpdate{index: index, branchName: branchName, phase: eventbus.EventSproutWithered, detail: result.err.Error()}
		return result
	}

	result.response = runResult.Response
	statusCh <- sproutStatusUpdate{index: index, branchName: branchName, phase: eventbus.EventSproutMatured}
	return result
}

// branchSubTasks is the Map phase: the meristem coordinator model splits the
// step transcript into count independent sub-tasks. The coordinator call is
// bounded by meristemBranchingTimeout; on timeout, API failure, or an
// unparseable reply it degrades gracefully to deterministic replicate shards
// instead of failing the step.
func branchSubTasks(ctx context.Context, step *SequenceStep, count int) []mycelialSubTask {
	if ctx == nil {
		ctx = context.Background()
	}

	branchCtx, cancel := context.WithTimeout(ctx, meristemBranchingTimeout)
	defer cancel()

	systemPrompt := fmt.Sprintf(`You are the meristem coordinator of the OpenTendril Stem.
Branch the task below into exactly %d independent, parallelizable sub-tasks so that %d sprout terrariums can each grow one sub-task simultaneously without editing the same files.
Respond ONLY with a JSON array of objects shaped as {"id": "<kebab-case-id>", "transcript": "<self-contained sub-task instructions>"}.
Every transcript must be self-contained: a sprout sees only its own transcript, never its siblings'.`, count, count)

	client := newMeristemBranchingClient()
	raw, err := client.Call(branchCtx, []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: step.Transcript},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Meristem branching for step %s degraded to replicate shards: %v\n", step.ID, err)
		return replicateSubTasks(step, count)
	}

	subTasks, parseErr := parseMycelialSubTasks(raw)
	if parseErr != nil || len(subTasks) == 0 {
		fmt.Fprintf(os.Stderr, "⚠️ Meristem branching for step %s returned an unparseable plan, using replicate shards: %v\n", step.ID, parseErr)
		return replicateSubTasks(step, count)
	}

	if len(subTasks) > count {
		subTasks = subTasks[:count]
	}
	return subTasks
}

func parseMycelialSubTasks(raw string) ([]mycelialSubTask, error) {
	payload := extractDynamicStepsPayload(raw)
	if strings.TrimSpace(payload) == "" {
		return nil, fmt.Errorf("empty branching payload")
	}

	var subTasks []mycelialSubTask
	if err := json.Unmarshal([]byte(payload), &subTasks); err != nil {
		return nil, fmt.Errorf("decode mycelial sub-tasks: %w", err)
	}

	validated := make([]mycelialSubTask, 0, len(subTasks))
	for i, subTask := range subTasks {
		transcript := strings.TrimSpace(subTask.Transcript)
		if transcript == "" {
			continue
		}
		id := sanitizeBranchComponent(subTask.ID)
		if id == "" {
			id = "sub-task-" + strconv.Itoa(i)
		}
		validated = append(validated, mycelialSubTask{ID: id, Transcript: transcript})
	}
	if len(validated) == 0 {
		return nil, fmt.Errorf("branching payload contained no usable sub-tasks")
	}
	return validated, nil
}

// replicateSubTasks is the graceful degradation path: every sprout receives
// the full transcript and the MycelialMerge consensus reconciles the
// redundant results, mirroring phenotypic selection semantics.
func replicateSubTasks(step *SequenceStep, count int) []mycelialSubTask {
	subTasks := make([]mycelialSubTask, 0, count)
	for i := 0; i < count; i++ {
		subTasks = append(subTasks, mycelialSubTask{
			ID:         "replicate-" + strconv.Itoa(i),
			Transcript: fmt.Sprintf("You are sprout %d of %d growing the same task in parallel. Complete it independently and thoroughly.\n\n%s", i+1, count, step.Transcript),
		})
	}
	return subTasks
}

func buildMycelialMergeTranscript(step *SequenceStep, subTasks []mycelialSubTask, matured, withered []sproutGrowthResult) string {
	var builder strings.Builder
	builder.WriteString("You are the MycelialMerge consensus coordinator. Parallel sprouts each completed a shard of the task below; their branches that could be grafted have already been merged into this substrate.\n\n")
	builder.WriteString("Original task:\n")
	builder.WriteString(step.Transcript)
	builder.WriteString("\n\nSprout results:\n")

	transcriptByID := make(map[string]string, len(subTasks))
	for _, subTask := range subTasks {
		transcriptByID[subTask.ID] = subTask.Transcript
	}

	for _, result := range matured {
		mergeState := "branch merged into substrate"
		if !result.merged {
			mergeState = "branch could NOT be merged (conflict); its changes exist only in the report below"
		}
		fmt.Fprintf(&builder, "\n--- Sprout %d [%s] (%s) ---\nSub-task: %s\nReport:\n%s\n",
			result.index, result.subTaskID, mergeState,
			excerptForMerge(transcriptByID[result.subTaskID]),
			excerptForMerge(result.response))
	}
	for _, result := range withered {
		fmt.Fprintf(&builder, "\n--- Sprout %d [%s] WITHERED ---\nSub-task: %s\nFailure: %v\n",
			result.index, result.subTaskID,
			excerptForMerge(transcriptByID[result.subTaskID]),
			result.err)
	}

	builder.WriteString("\nReconcile these results into one coherent final outcome: resolve contradictions between sprouts, repair anything a conflicted or withered sprout left incomplete, verify the substrate compiles and its tests pass, and finish with a consolidated summary of the completed task.")
	return builder.String()
}

func excerptForMerge(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(empty)"
	}
	if len(value) <= mycelialResponseExcerptLimit {
		return value
	}
	return value[:mycelialResponseExcerptLimit] + "\n[... truncated ...]"
}

func publishParallelSproutingEvent(bus *eventbus.Bus, eventType eventbus.EventType, seq *Sequence, step *SequenceStep, data map[string]interface{}) {
	if bus == nil {
		return
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	if seq != nil {
		data["sequence"] = seq.Name
	}
	if step != nil {
		data["stepId"] = step.ID
	}
	bus.Publish(eventbus.Event{
		Type:   eventType,
		Source: "parallel-sprouting",
		Data:   data,
	})
}
