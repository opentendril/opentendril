package conductor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

var ErrRequiresReview = errors.New("script requires review")

// SequenceStepRunner executes a single sequence step.
type SequenceStepRunner func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error)

// SequenceRunOptions controls how a sequence is executed.
type SequenceRunOptions struct {
	Stdout             io.Writer
	Stderr             io.Writer
	Stdin              io.Reader
	Interactive        bool
	Provider           string
	Model              string
	BaseURL            string
	StepRunner         SequenceStepRunner
	ResumePollInterval time.Duration
	CleanupGracePeriod time.Duration
	EventBus           *eventbus.Bus
}

type sequenceRunner struct {
	path          string
	seq           *Sequence
	opts          SequenceRunOptions
	substratePath string

	stepByID      map[string]*SequenceStep
	stepIndex     map[string]int
	dependents    map[string][]string
	remainingDeps map[string]int
	queued        map[string]bool
	retriesLeft   map[string]int
	ready         []string
	completed     int
}

type sequenceStepResult struct {
	stepID string
	output string
	err    error
}

// RunSequence loads and executes a sequence using the provided options.
func RunSequence(ctx context.Context, sequencePath string, opts SequenceRunOptions) (*Sequence, error) {
	resolvedPath, err := ResolveSequencePath(sequencePath)
	if err != nil {
		return nil, err
	}

	seq, err := LoadSequence(resolvedPath)
	if err != nil {
		return nil, err
	}

	opts = normalizeSequenceRunOptions(opts)
	runner, err := newSequenceRunner(resolvedPath, seq, opts)
	if err != nil {
		return seq, err
	}

	return runner.run(ctx)
}

func normalizeSequenceRunOptions(opts SequenceRunOptions) SequenceRunOptions {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.ResumePollInterval <= 0 {
		opts.ResumePollInterval = time.Second
	}
	if opts.CleanupGracePeriod <= 0 {
		opts.CleanupGracePeriod = 30 * time.Second
	}
	if opts.StepRunner == nil {
		bus := opts.EventBus
		provider := opts.Provider
		model := opts.Model
		baseURL := opts.BaseURL
		opts.StepRunner = func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
			return defaultSequenceStepRunnerWithOpts(ctx, seq, step, substratePath, bus, provider, model, baseURL)
		}
	}
	return opts
}

func newSequenceRunner(path string, seq *Sequence, opts SequenceRunOptions) (*sequenceRunner, error) {
	if seq == nil {
		return nil, fmt.Errorf("sequence is nil")
	}

	runner := &sequenceRunner{
		path:          path,
		seq:           seq,
		opts:          opts,
		stepByID:      make(map[string]*SequenceStep, len(seq.Steps)),
		stepIndex:     make(map[string]int, len(seq.Steps)),
		dependents:    make(map[string][]string, len(seq.Steps)),
		remainingDeps: make(map[string]int, len(seq.Steps)),
		queued:        make(map[string]bool, len(seq.Steps)),
		retriesLeft:   make(map[string]int, len(seq.Steps)),
	}

	root := repoRoot(filepath.Dir(path))
	runner.substratePath = resolveSequenceSubstrate(root, seq.Substrate)
	if runner.substratePath == "" {
		runner.substratePath = root
	}

	for i := range seq.Steps {
		step := &seq.Steps[i]
		runner.stepByID[step.ID] = step
		runner.stepIndex[step.ID] = i
		if step.Status == sequenceStatusComplete {
			runner.completed++
		}
	}

	for _, step := range seq.Steps {
		for _, dep := range step.DependsOn {
			if _, ok := runner.stepByID[dep]; !ok {
				return nil, fmt.Errorf("sequence %s step %s depends on unknown step %q", path, step.ID, dep)
			}
			runner.dependents[dep] = append(runner.dependents[dep], step.ID)
			if runner.stepByID[dep].Status != sequenceStatusComplete {
				runner.remainingDeps[step.ID]++
			}
		}
		if step.Status != sequenceStatusComplete && runner.remainingDeps[step.ID] == 0 {
			runner.ready = append(runner.ready, step.ID)
			runner.queued[step.ID] = true
		}
	}

	runner.seedRetryBudgets()

	if cycle := findDependencyCycle(seq.Steps); cycle != nil {
		return nil, fmt.Errorf("sequence %s step %s forms a dependency cycle: %s", path, cycle[0], strings.Join(cycle, " -> "))
	}

	if len(runner.ready) == 0 {
		allDone := true
		for _, step := range seq.Steps {
			if step.Status != sequenceStatusComplete {
				allDone = false
				break
			}
		}
		if !allDone {
			return nil, fmt.Errorf("sequence %s has no runnable steps; check dependencies and prior failures", path)
		}
	}

	runner.sortReady()

	return runner, nil
}

func (r *sequenceRunner) run(ctx context.Context) (resultSeq *Sequence, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}

	fmt.Fprintf(r.opts.Stdout, "▶ Sequence %s (%d steps, concurrency %d)\n", r.seq.Name, len(r.seq.Steps), r.seq.ConcurrencyLimit)
	if err := SaveSequence(r.path, r.seq); err != nil {
		return r.seq, err
	}

	concurrencyLimit := r.seq.ConcurrencyLimit
	if concurrencyLimit <= 0 {
		concurrencyLimit = 1
	}

	resultCh := make(chan sequenceStepResult, len(r.seq.Steps))
	inFlight := make(map[string]struct{})
	runCtx, cancelRun := context.WithCancel(ctx)
	defer func() {
		cancelRun()
		r.drainInFlight(inFlight, resultCh)
	}()

	dispatch := func(stepID string) {
		step := r.stepByID[stepID]
		if step == nil {
			return
		}
		r.queued[stepID] = false
		inFlight[stepID] = struct{}{}
		go func(id string, stepSnapshot SequenceStep, seqSnapshot Sequence) {
			// This shallow copy is safe because no step-path code writes to seq,
			// and no step-path code reads seq.Steps (which still shares its backing
			// array). If a dispatched step ever needs to read reference-typed
			// fields like Steps, this will reintroduce the race.
			output, err := r.opts.StepRunner(runCtx, &seqSnapshot, &stepSnapshot, r.substratePath)
			resultCh <- sequenceStepResult{stepID: id, output: output, err: err}
		}(stepID, *step, *r.seq)
	}

	for {
		for len(inFlight) < concurrencyLimit {
			nextID, ok := r.popReady()
			if !ok {
				break
			}
			fmt.Fprintf(r.opts.Stdout, "→ [%s] starting\n", nextID)
			dispatch(nextID)
		}

		if r.completed == len(r.seq.Steps) {
			fmt.Fprintf(r.opts.Stdout, "✅ Sequence %s complete\n", r.seq.Name)
			if err := SaveSequence(r.path, r.seq); err != nil {
				return r.seq, err
			}
			r.publishSequenceEvent(eventbus.EventSequenceComplete, "", nil, map[string]interface{}{
				"sequence": r.seq.Name,
			})
			return r.seq, nil
		}

		if len(inFlight) == 0 && len(r.ready) == 0 {
			msg := r.describeStall()
			if msg == "" {
				msg = fmt.Sprintf("sequence %s stalled", r.seq.Name)
			}
			return r.seq, errors.New(msg)
		}

		select {
		case <-ctx.Done():
			return r.seq, ctx.Err()
		case result := <-resultCh:
			delete(inFlight, result.stepID)
			step := r.stepByID[result.stepID]
			if step == nil {
				continue
			}
			delete(r.queued, result.stepID)

			if result.err == nil {
				if output := strings.TrimSpace(result.output); output != "" {
					fmt.Fprintln(r.opts.Stdout, output)
				}
				if isMeristemStep(result.stepID) {
					dynamicSteps, parseErr := parseDynamicSteps(result.output)
					if parseErr != nil {
						fmt.Fprintf(r.opts.Stderr, "⚠️ Failed to parse dynamic steps from %s: %v\n", result.stepID, parseErr)
					} else if len(dynamicSteps) > 0 {
						if err := r.appendDynamicSteps(dynamicSteps); err != nil {
							return r.seq, err
						}
					}
				}
				fmt.Fprintf(r.opts.Stdout, "✓ [%s] complete\n", result.stepID)
				if err := r.completeStep(result.stepID); err != nil {
					return r.seq, err
				}
				continue
			}

			step.Status = sequenceStatusFailed
			r.publishStepFailure(result.stepID, result.err)

			if errors.Is(result.err, ErrRequiresReview) {
				if err := SaveSequence(r.path, r.seq); err != nil {
					return r.seq, err
				}
				decision, pauseErr := r.handlePause(ctx, result.stepID, result.err, strings.ToLower(strings.TrimSpace(r.seq.OnFailure)))
				if pauseErr != nil {
					return r.seq, pauseErr
				}
				switch decision {
				case "retry":
					step.Status = sequenceStatusPending
					if err := SaveSequence(r.path, r.seq); err != nil {
						return r.seq, err
					}
					r.ready = append(r.ready, result.stepID)
					r.queued[result.stepID] = true
					r.sortReady()
					continue
				case "completed":
					if err := r.completeStep(result.stepID); err != nil {
						return r.seq, err
					}
					continue
				case "halt":
					return r.seq, fmt.Errorf("step %s halted after review requirement: %w", result.stepID, result.err)
				default:
					return r.seq, fmt.Errorf("step %s returned unknown pause decision %q", result.stepID, decision)
				}
			}

			if shouldBudRecursiveDebugger(step) {
				debuggerStepID := fmt.Sprintf("debugger-%s-%d", result.stepID, time.Now().UnixNano())
				debuggerStep := SequenceStep{
					ID:         debuggerStepID,
					Transcript: fmt.Sprintf("Analyze and fix the compiler/test failure in step [%s]. Errors:\n%v", result.stepID, result.err),
					DependsOn:  []string{},
				}
				if err := r.appendDynamicSteps([]SequenceStep{debuggerStep}); err != nil {
					return r.seq, err
				}

				failedStep := r.stepByID[result.stepID]
				if failedStep == nil {
					return r.seq, fmt.Errorf("failed step %s disappeared during debugger sprout", result.stepID)
				}
				failedStep.DependsOn = append(failedStep.DependsOn, debuggerStepID)
				failedStep.Status = sequenceStatusPending
				r.remainingDeps[result.stepID]++
				r.dependents[debuggerStepID] = append(r.dependents[debuggerStepID], result.stepID)

				if err := SaveSequence(r.path, r.seq); err != nil {
					return r.seq, err
				}
				fmt.Fprintf(r.opts.Stdout, "↺ Sprouted recursive Debugger [%s] for failed verifier step [%s]\n", debuggerStepID, result.stepID)
				continue
			}

			if err := SaveSequence(r.path, r.seq); err != nil {
				return r.seq, err
			}

			kind := failureKindStandard
			if stepTimedOut(result.err) {
				kind = failureKindTimeout
			}

			action, actionErr := decideFailureAction(strings.ToLower(strings.TrimSpace(r.seq.OnFailure)), r.retriesLeft[result.stepID], kind)
			if actionErr != nil {
				// spent = resolved budget - remaining (remaining is zero at this point)
				spent := r.resolveRetryBudget() - r.retriesLeft[result.stepID]
				return r.seq, fmt.Errorf("step %s failed after %s: %w", result.stepID, pluralRetries(spent), result.err)
			}

			switch action {
			case failureActionRetry:
				r.retriesLeft[result.stepID]--
				step.Status = sequenceStatusPending
				if err := SaveSequence(r.path, r.seq); err != nil {
					return r.seq, err
				}
				r.ready = append(r.ready, result.stepID)
				r.queued[result.stepID] = true
				r.sortReady()
				fmt.Fprintf(r.opts.Stderr, "↺ [%s] retrying, %s left\n", result.stepID, pluralRetries(r.retriesLeft[result.stepID]))
				continue

			case failureActionPause:
				decision, pauseErr := r.handlePause(ctx, result.stepID, result.err, sequenceOnFailurePause)
				if pauseErr != nil {
					return r.seq, pauseErr
				}
				switch decision {
				case "retry":
					step.Status = sequenceStatusPending
					if err := SaveSequence(r.path, r.seq); err != nil {
						return r.seq, err
					}
					r.ready = append(r.ready, result.stepID)
					r.queued[result.stepID] = true
					r.sortReady()
					continue
				case "completed":
					if err := r.completeStep(result.stepID); err != nil {
						return r.seq, err
					}
					continue
				case "halt":
					return r.seq, fmt.Errorf("step %s halted after failure: %w", result.stepID, result.err)
				default:
					return r.seq, fmt.Errorf("step %s returned unknown pause decision %q", result.stepID, decision)
				}

			case failureActionHalt:
				if kind == failureKindTimeout && strings.ToLower(strings.TrimSpace(r.seq.OnFailure)) == sequenceOnFailureRetry {
					return r.seq, fmt.Errorf("step %s exceeded its time limit; not retrying because a timeout cannot be resolved by repetition. Raise the ceiling or choose a faster model: %w", result.stepID, result.err)
				}
				return r.seq, fmt.Errorf("step %s failed: %w", result.stepID, result.err)

			default:
				return r.seq, fmt.Errorf("sequence %s has unknown onFailure mode %q", r.seq.Name, r.seq.OnFailure)
			}
		}
	}
}

// Drain in-flight step workers before returning. Each worker runs
// StepRunner's deferred host-stash restore (through a WithoutCancel
// cleanup context) and only then sends its result, so waiting for
// those sends guarantees a cancelled run never strands the user's
// stashed workspace. The docker steps are ctx-bound and unwind
// promptly; the CLI's signal handler force-quits as a backstop.
// Draining runs on every exit path to ensure cleanup finishes.
func (r *sequenceRunner) drainInFlight(inFlight map[string]struct{}, resultCh <-chan sequenceStepResult) {
	if len(inFlight) == 0 {
		return
	}

	timer := time.NewTimer(r.opts.CleanupGracePeriod)
	defer timer.Stop()

	for len(inFlight) > 0 {
		select {
		case result := <-resultCh:
			delete(inFlight, result.stepID)
			if result.err != nil {
				fmt.Fprintf(r.opts.Stderr, "⚠️ Step [%s] cleanup error: %v\n", result.stepID, result.err)
			}
		case <-timer.C:
			var remaining []string
			for id := range inFlight {
				remaining = append(remaining, id)
			}
			sort.Strings(remaining)
			fmt.Fprintf(r.opts.Stderr, "⚠️ Timed out waiting for in-flight steps to clean up: %s. You may need to check 'git stash list' for unrestored work.\n", strings.Join(remaining, ", "))
			r.publishSequenceEvent(eventbus.EventSequenceCleanupIncomplete, "", nil, map[string]interface{}{
				"stepIds": remaining,
			})
			return
		}
	}
}

type commandResultCarrier interface {
	CommandResult() terrarium.CommandResult
}

func (r *sequenceRunner) publishStepFailure(stepID string, stepErr error) {
	data := map[string]interface{}{
		"stepId": stepID,
	}
	if stepErr != nil {
		data["error"] = stepErr.Error()
	}

	if result, ok := commandResultFromError(stepErr); ok {
		data["exitCode"] = result.ExitCode
		data["timedOut"] = result.TimedOut
		if result.ExitCode == 137 {
			r.publishSequenceEvent(eventbus.EventTerrariumOOM, stepID, stepErr, map[string]interface{}{
				"stepId":   stepID,
				"exitCode": result.ExitCode,
			})
		}
		if result.TimedOut {
			r.publishSequenceEvent(eventbus.EventTerrariumTimeout, stepID, stepErr, map[string]interface{}{
				"stepId":   stepID,
				"timedOut": result.TimedOut,
			})
		}
	}

	r.publishSequenceEvent(eventbus.EventSequenceFailure, stepID, stepErr, data)
}

func (r *sequenceRunner) publishSequenceEvent(eventType eventbus.EventType, stepID string, eventErr error, data map[string]interface{}) {
	if r == nil || r.opts.EventBus == nil {
		return
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	if _, ok := data["sequence"]; !ok && r.seq != nil {
		data["sequence"] = r.seq.Name
	}
	if stepID != "" {
		data["stepId"] = stepID
	}
	if eventErr != nil {
		data["error"] = eventErr.Error()
	}

	r.opts.EventBus.Publish(eventbus.Event{
		Type:   eventType,
		Source: "sequence-runner",
		Data:   data,
	})
}

func commandResultFromError(err error) (terrarium.CommandResult, bool) {
	if err == nil {
		return terrarium.CommandResult{}, false
	}
	var carrier commandResultCarrier
	if errors.As(err, &carrier) {
		return carrier.CommandResult(), true
	}
	return terrarium.CommandResult{}, false
}

func stepTimedOut(err error) bool {
	if errors.Is(err, ErrSproutTimedOut) {
		return true
	}
	if result, ok := commandResultFromError(err); ok {
		return result.TimedOut
	}
	return false
}

func shouldBudRecursiveDebugger(step *SequenceStep) bool {
	if step == nil {
		return false
	}

	// Deterministic verifier/CI steps report pass/fail; they do not bud an LLM
	// Debugger. A failed build/test is a CI result to surface, not a prompt to
	// auto-edit the tree.
	if len(step.Command) > 0 {
		return false
	}

	stepID := strings.ToLower(strings.TrimSpace(step.ID))
	// Verifier: LLM-interpreted compiler/test failures. Macrophage: the
	// deterministic fuzz-crash failures from runMacrophageFuzzCheck — both
	// loop back to the same recursive Debugger.
	if !strings.Contains(stepID, "verifier") && !strings.Contains(stepID, "macrophage") {
		return false
	}
	if strings.Count(stepID, "debugger") >= 3 {
		return false
	}

	return debuggerDependencyCount(step.DependsOn) < 3
}

func debuggerDependencyCount(dependsOn []string) int {
	count := 0
	for _, dep := range dependsOn {
		if strings.Contains(strings.ToLower(strings.TrimSpace(dep)), "debugger") {
			count++
		}
	}
	return count
}

func (r *sequenceRunner) handlePause(ctx context.Context, stepID string, stepErr error, pausedUnderMode string) (string, error) {
	if r.opts.Interactive {
		fmt.Fprintf(r.opts.Stderr, "⚠️ Step %s failed. [R]etry or [H]alt? ", stepID)
		reader := bufio.NewReader(r.opts.Stdin)

		type readResult struct {
			line string
			err  error
		}
		resultCh := make(chan readResult, 1)

		// On cancellation the reading goroutine stays parked until stdin yields.
		// This is a deliberate trade-off since reading from os.Stdin cannot be
		// interrupted safely. It is acceptable since the process is unwinding.
		readNext := func() {
			go func() {
				line, err := reader.ReadString('\n')
				resultCh <- readResult{line, err}
			}()
		}
		readNext()

		for {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case res := <-resultCh:
				if res.err != nil && !errors.Is(res.err, io.EOF) {
					return "", fmt.Errorf("read pause decision: %w", res.err)
				}
				if res.line == "" && errors.Is(res.err, io.EOF) {
					return "halt", nil
				}
				choice := strings.ToLower(strings.TrimSpace(res.line))
				switch choice {
				case "", "r", "retry":
					return "retry", nil
				case "h", "halt":
					return "halt", nil
				default:
					fmt.Fprintf(r.opts.Stderr, "Please enter R or H: ")
				}
				if errors.Is(res.err, io.EOF) {
					return "retry", nil
				}
				readNext()
			}
		}
	}

	fmt.Fprintf(r.opts.Stderr, "⚠️ Step %s failed in headless mode. Edit the sequence to change onFailure away from %q, or set the step status to pending or complete.\n", stepID, pausedUnderMode)
	ticker := time.NewTicker(r.opts.ResumePollInterval)
	defer ticker.Stop()

	var lastWarnedDiffKey string

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			latest, err := LoadSequence(r.path)
			if err != nil {
				fmt.Fprintf(r.opts.Stderr, "⚠️ Waiting for resume signal, but reloading %s failed: %v\n", r.path, err)
				continue
			}
			if latest == nil {
				continue
			}

			diffs := unhonouredSequenceDiff(r.seq, latest, stepID)
			if len(diffs) > 0 {
				sort.Strings(diffs)
				diffKey := strings.Join(diffs, ",")
				if diffKey != lastWarnedDiffKey {
					fmt.Fprintf(r.opts.Stderr, "⚠️ Ignored edits to paused sequence: %s. Only onFailure and the paused step's status are read while paused.\n", strings.Join(diffs, ", "))
					lastWarnedDiffKey = diffKey
				}
			}

			if step := latestStepByID(latest.Steps, stepID); step != nil {
				switch step.Status {
				case sequenceStatusComplete:
					return "completed", nil
				case sequenceStatusPending:
					return "retry", nil
				}
			}
			latestMode := strings.ToLower(strings.TrimSpace(latest.OnFailure))
			if latestMode != pausedUnderMode {
				r.seq.OnFailure = latest.OnFailure
				r.seedRetryBudgets()
				return latestMode, nil
			}
		}
	}
}

func unhonouredSequenceDiff(base, latest *Sequence, pausedStepID string) []string {
	var diffs []string
	if base.Name != latest.Name {
		diffs = append(diffs, "name")
	}
	if base.System != latest.System {
		diffs = append(diffs, "system")
	}
	if base.Substrate != latest.Substrate {
		diffs = append(diffs, "substrate")
	}
	if base.Branch != latest.Branch {
		diffs = append(diffs, "branch")
	}
	if base.ConcurrencyLimit != latest.ConcurrencyLimit {
		diffs = append(diffs, "concurrencyLimit")
	}
	if base.MaxRetries != latest.MaxRetries {
		diffs = append(diffs, "maxRetries")
	}

	if len(base.Steps) != len(latest.Steps) {
		diffs = append(diffs, "steps length")
	} else {
		for i := range base.Steps {
			b := &base.Steps[i]
			l := &latest.Steps[i]
			if b.ID != l.ID {
				diffs = append(diffs, fmt.Sprintf("step[%d].id", i))
				continue
			}
			if b.ID != pausedStepID && b.Status != l.Status {
				diffs = append(diffs, fmt.Sprintf("step[%s].status", b.ID))
			}
			if strings.Join(b.DependsOn, ",") != strings.Join(l.DependsOn, ",") {
				diffs = append(diffs, fmt.Sprintf("step[%s].dependsOn", b.ID))
			}
			if b.Transcript != l.Transcript {
				diffs = append(diffs, fmt.Sprintf("step[%s].transcript", b.ID))
			}
			if strings.Join(b.Command, ",") != strings.Join(l.Command, ",") {
				diffs = append(diffs, fmt.Sprintf("step[%s].command", b.ID))
			}
			if b.Parallel != l.Parallel {
				diffs = append(diffs, fmt.Sprintf("step[%s].parallel", b.ID))
			}
			if b.SproutCount != l.SproutCount {
				diffs = append(diffs, fmt.Sprintf("step[%s].sproutCount", b.ID))
			}
			if b.MergeTranscript != l.MergeTranscript {
				diffs = append(diffs, fmt.Sprintf("step[%s].mergeTranscript", b.ID))
			}
			if b.PhenotypesCount != l.PhenotypesCount {
				diffs = append(diffs, fmt.Sprintf("step[%s].phenotypesCount", b.ID))
			}
			if b.FitnessTest != l.FitnessTest {
				diffs = append(diffs, fmt.Sprintf("step[%s].fitnessTest", b.ID))
			}
			if b.RequiresReasoning != l.RequiresReasoning {
				diffs = append(diffs, fmt.Sprintf("step[%s].requiresReasoning", b.ID))
			}
			if b.RequiresVision != l.RequiresVision {
				diffs = append(diffs, fmt.Sprintf("step[%s].requiresVision", b.ID))
			}
			if b.ModelProvider != l.ModelProvider {
				diffs = append(diffs, fmt.Sprintf("step[%s].modelProvider", b.ID))
			}
			if b.ModelName != l.ModelName {
				diffs = append(diffs, fmt.Sprintf("step[%s].modelName", b.ID))
			}
			if b.ModelBaseURL != l.ModelBaseURL {
				diffs = append(diffs, fmt.Sprintf("step[%s].modelBaseURL", b.ID))
			}
			if (b.Selection == nil) != (l.Selection == nil) {
				diffs = append(diffs, fmt.Sprintf("step[%s].selection", b.ID))
			} else if b.Selection != nil && l.Selection != nil {
				if b.Selection.PopulationSize != l.Selection.PopulationSize ||
					b.Selection.MaxGenerations != l.Selection.MaxGenerations ||
					b.Selection.FitnessTest != l.Selection.FitnessTest ||
					b.Selection.FitnessPattern != l.Selection.FitnessPattern ||
					b.Selection.FitnessGoal != l.Selection.FitnessGoal ||
					b.Selection.SurvivorFraction != l.Selection.SurvivorFraction ||
					b.Selection.MutationTemperature != l.Selection.MutationTemperature ||
					b.Selection.TemperatureSpread != l.Selection.TemperatureSpread {
					diffs = append(diffs, fmt.Sprintf("step[%s].selection", b.ID))
				} else {
					if (b.Selection.FitnessThreshold == nil) != (l.Selection.FitnessThreshold == nil) {
						diffs = append(diffs, fmt.Sprintf("step[%s].selection.fitnessThreshold", b.ID))
					} else if b.Selection.FitnessThreshold != nil && *b.Selection.FitnessThreshold != *l.Selection.FitnessThreshold {
						diffs = append(diffs, fmt.Sprintf("step[%s].selection.fitnessThreshold", b.ID))
					}
				}
			}
		}
	}
	return diffs
}

func latestStepByID(steps []SequenceStep, id string) *SequenceStep {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}

func (r *sequenceRunner) popReady() (string, bool) {
	if len(r.ready) == 0 {
		return "", false
	}
	stepID := r.ready[0]
	r.ready = r.ready[1:]
	if r.queued[stepID] {
		delete(r.queued, stepID)
	}
	return stepID, true
}

func (r *sequenceRunner) sortReady() {
	sort.SliceStable(r.ready, func(i, j int) bool {
		return r.stepIndex[r.ready[i]] < r.stepIndex[r.ready[j]]
	})
}

func (r *sequenceRunner) describeStall() string {
	var blocked []string
	for _, step := range r.seq.Steps {
		if step.Status == sequenceStatusComplete {
			continue
		}
		if r.remainingDeps[step.ID] == 0 {
			continue
		}
		var missing []string
		for _, dep := range step.DependsOn {
			if depStep := r.stepByID[dep]; depStep != nil && depStep.Status != sequenceStatusComplete {
				missing = append(missing, dep)
			}
		}
		if len(missing) > 0 {
			blocked = append(blocked, fmt.Sprintf("%s <- %s", step.ID, strings.Join(missing, ", ")))
		}
	}
	if len(blocked) == 0 {
		return ""
	}
	sort.Strings(blocked)
	return fmt.Sprintf("sequence %s stalled: %s", r.seq.Name, strings.Join(blocked, "; "))
}

func parseDynamicSteps(output string) ([]SequenceStep, error) {
	payload := extractDynamicStepsPayload(output)
	if strings.TrimSpace(payload) == "" {
		return nil, nil
	}

	var steps []SequenceStep
	if err := json.Unmarshal([]byte(payload), &steps); err != nil {
		return nil, fmt.Errorf("decode dynamic steps: %w", err)
	}

	return steps, nil
}

func extractDynamicStepsPayload(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}

	start := strings.Index(trimmed, "```")
	if start < 0 {
		return trimmed
	}

	trimmed = trimmed[start+3:]
	if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
		trimmed = trimmed[newline+1:]
	}
	if end := strings.Index(trimmed, "```"); end >= 0 {
		trimmed = trimmed[:end]
	}

	return strings.TrimSpace(trimmed)
}

func (r *sequenceRunner) appendDynamicSteps(steps []SequenceStep) error {
	if len(steps) == 0 {
		return nil
	}

	knownIDs := make(map[string]struct{}, len(r.stepByID)+len(steps))
	for id := range r.stepByID {
		knownIDs[id] = struct{}{}
	}

	for _, rawStep := range steps {
		id := strings.TrimSpace(rawStep.ID)
		if id == "" {
			return fmt.Errorf("dynamic sequence contains a step with an empty id")
		}
		if _, ok := knownIDs[id]; ok {
			return fmt.Errorf("dynamic sequence contains duplicate step id %q", id)
		}
		knownIDs[id] = struct{}{}
	}

	validated := make([]SequenceStep, 0, len(steps))
	for _, rawStep := range steps {
		step := SequenceStep{
			ID:         strings.TrimSpace(rawStep.ID),
			Transcript: strings.TrimSpace(rawStep.Transcript),
			Status:     sequenceStatusPending,
		}
		if step.Transcript == "" {
			return fmt.Errorf("dynamic sequence step %s has an empty transcript", step.ID)
		}

		deps, err := normalizeDynamicStepDependsOn(step.ID, rawStep.DependsOn, knownIDs)
		if err != nil {
			return err
		}
		step.DependsOn = deps
		validated = append(validated, step)
	}

	allSteps := append([]SequenceStep(nil), r.seq.Steps...)
	allSteps = append(allSteps, validated...)
	if cycle := findDependencyCycle(allSteps); cycle != nil {
		return fmt.Errorf("dynamic sequence step %s forms a dependency cycle: %s", cycle[0], strings.Join(cycle, " -> "))
	}

	baseIndex := len(r.seq.Steps)
	r.seq.Steps = append(r.seq.Steps, validated...)
	r.rebuildStepIndexes()

	for i := range validated {
		step := &r.seq.Steps[baseIndex+i]
		r.remainingDeps[step.ID] = 0
		for _, dep := range step.DependsOn {
			depStep, ok := r.stepByID[dep]
			if !ok {
				return fmt.Errorf("dynamic sequence step %s depends on unknown step %q", step.ID, dep)
			}
			r.dependents[dep] = append(r.dependents[dep], step.ID)
			if depStep.Status != sequenceStatusComplete {
				r.remainingDeps[step.ID]++
			}
		}
		if step.Status != sequenceStatusComplete && r.remainingDeps[step.ID] == 0 {
			r.ready = append(r.ready, step.ID)
			r.queued[step.ID] = true
		}
	}

	r.seedRetryBudgets()
	r.sortReady()
	return nil
}

func normalizeDynamicStepDependsOn(stepID string, dependsOn []string, knownIDs map[string]struct{}) ([]string, error) {
	deps := make([]string, 0, len(dependsOn))
	seen := make(map[string]struct{}, len(dependsOn))
	for _, dep := range dependsOn {
		trimmed := strings.TrimSpace(dep)
		if trimmed == "" {
			continue
		}
		if trimmed == stepID {
			return nil, fmt.Errorf("dynamic sequence step %s cannot depend on itself", stepID)
		}
		if _, ok := knownIDs[trimmed]; !ok {
			return nil, fmt.Errorf("dynamic sequence step %s depends on unknown step %q", stepID, trimmed)
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		deps = append(deps, trimmed)
	}
	return deps, nil
}

func (r *sequenceRunner) rebuildStepIndexes() {
	r.stepByID = make(map[string]*SequenceStep, len(r.seq.Steps))
	r.stepIndex = make(map[string]int, len(r.seq.Steps))
	for i := range r.seq.Steps {
		step := &r.seq.Steps[i]
		r.stepByID[step.ID] = step
		r.stepIndex[step.ID] = i
	}
}

// pluralRetries renders a retry count with its noun agreeing, so an operator
// reading a failure does not see "after 1 retries". Both the exhaustion error
// and the countdown line go through it, so the two cannot drift apart.
func pluralRetries(n int) string {
	if n == 1 {
		return "1 retry"
	}
	return fmt.Sprintf("%d retries", n)
}

func (r *sequenceRunner) resolveRetryBudget() int {
	retries := r.seq.MaxRetries
	if retries <= 0 {
		retries = defaultSequenceRetryLimit
	}
	return retries
}

func (r *sequenceRunner) seedRetryBudgets() {
	if strings.ToLower(strings.TrimSpace(r.seq.OnFailure)) != sequenceOnFailureRetry {
		return
	}
	retries := r.resolveRetryBudget()
	for i := range r.seq.Steps {
		stepID := r.seq.Steps[i].ID
		if _, exists := r.retriesLeft[stepID]; !exists {
			r.retriesLeft[stepID] = retries
		}
	}
}

func findDependencyCycle(steps []SequenceStep) []string {
	stepMap := make(map[string]*SequenceStep, len(steps))
	for i := range steps {
		stepMap[steps[i].ID] = &steps[i]
	}

	visited := make(map[string]bool)
	onStack := make(map[string]bool)
	var path []string

	var dfs func(stepID string) []string
	dfs = func(stepID string) []string {
		visited[stepID] = true
		onStack[stepID] = true
		path = append(path, stepID)

		if step, ok := stepMap[stepID]; ok {
			for _, dep := range step.DependsOn {
				if !visited[dep] {
					if cycle := dfs(dep); cycle != nil {
						return cycle
					}
				} else if onStack[dep] {
					var cycle []string
					for i, id := range path {
						if id == dep {
							cycle = append([]string(nil), path[i:]...)
							cycle = append(cycle, dep)
							return cycle
						}
					}
				}
			}
		}

		onStack[stepID] = false
		path = path[:len(path)-1]
		return nil
	}

	for _, step := range steps {
		if !visited[step.ID] {
			if cycle := dfs(step.ID); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

var (
	runSequenceSproutFn          = runSequenceSprout
	runSequenceSproutAtPathFn    = runSequenceSproutAtPath
	mergePhenotypeBranchToHostFn = mergePhenotypeBranchToHost
	mergePhloemChannelToHostFn   = mergePhloemChannelToHost
)

type sproutExecutionResult struct {
	Response   string
	CommitHash string
	ImageName  string
	// Outcome is the SproutOutcome* verdict on what the run actually did;
	// FilesModified is the evidence behind it (nil when unmeasurable, e.g. a
	// non-git workspace).
	Outcome       string
	Protocol      string
	FilesModified []string
	// FilesUnmeasured explains why FilesModified is unknown on a run that
	// should have been able to measure it, so an unmeasurable substrate and a
	// measurement cut short stay distinguishable.
	FilesUnmeasured string
	// WroteWorkspace reports whether the model asked the terrarium for
	// anything that could change the workspace. It travels with the result so
	// a caller that has to reclassify a run — a failure after the turn, a
	// detachment that ended elsewhere — still knows whether the model did any
	// work, which the file list alone cannot say.
	WroteWorkspace bool
	// RequestsMade / ToolInvocations travel with the result so a sequence
	// terminal event can carry the same observation facts the direct path
	// does, including a withered run that never invoked a tool.
	RequestsMade    bool
	ToolInvocations int
	// DetachedEnd delivers what a detached run finally did, once it has
	// actually ended, and is non-nil only when Outcome is
	// SproutOutcomeDetached. The caller still holds teardown the run needs —
	// above all the worktree the Sprout is still writing to — so it waits on
	// this rather than undoing that work from under a run that has not
	// finished. It carries one value and is then closed, so a caller that only
	// needs the moment can wait without reading the result.
	DetachedEnd <-chan detachedRunEnding
}

// detachedRunEnding is what a detached run reports when it finally ends: the
// same pair the call would have returned had anyone still been waiting for it.
type detachedRunEnding struct {
	result sproutExecutionResult
	err    error
}

// awaitDetachedRun turns a detached run back into a finished one for a caller
// that cannot leave it alone. Most of this package's sprout call sites own the
// worktree the Sprout is still writing to and remove it the moment they return,
// so detaching from them would tear the run down anyway — waiting is what
// honesty costs there, and what comes back is the run's real result rather than
// an empty one dressed as matured.
func awaitDetachedRun(result sproutExecutionResult, err error) (sproutExecutionResult, error) {
	if result.DetachedEnd == nil {
		return result, err
	}

	ending := <-result.DetachedEnd
	return ending.result, ending.err
}

type phenotypeRunResult struct {
	index      int
	branchName string
	response   string
	err        error
}

func defaultSequenceStepRunner(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
	return defaultSequenceStepRunnerWithBus(ctx, seq, step, substratePath, nil)
}

func defaultSequenceStepRunnerWithBus(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string, bus *eventbus.Bus) (string, error) {
	return defaultSequenceStepRunnerWithOpts(ctx, seq, step, substratePath, bus, "", "", "")
}

func defaultSequenceStepRunnerWithOpts(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string, bus *eventbus.Bus, provider, model, baseURL string) (string, error) {
	// A step carrying an explicit command is a deterministic verifier/CI step:
	// exec it directly in the toolchain terrarium (read-only, no LLM, no
	// merge-back). Its exit code is the verdict.
	if len(step.Command) > 0 {
		return runVerifierCommandFn(ctx, resolveTerrariumProviderName(ctx, nil), repoRoot(substratePath), step.Command)
	}

	genotype := stepGenotype(step.ID)
	if step.Parallel {
		return runParallelSprouting(ctx, seq, step, substratePath, bus)
	}

	if step.Selection != nil {
		return runGeneticSelectionFn(ctx, seq, step, substratePath, bus)
	}

	if seq.ConcurrencyLimit > 1 {
		return runParallelSequenceStep(ctx, seq, step, substratePath, genotype)
	}

	if step.PhenotypesCount > 1 {
		return runPhenotypicSelection(ctx, seq, step, substratePath)
	}

	orch := &DockerOrchestrator{
		Substrate:       substratePath,
		SubstrateBranch: derivedSequenceBranch(seq.Branch, step.ID),
		StepID:          step.ID,
		IsCoordinator:   isMeristemStep(step.ID),
		Genotype:        genotype,
		Investigation:   step.Investigation,
		Provider:        provider,
		Model:           model,
		BaseURL:         baseURL,
		// The sequence bus, not nil: the Sprout streams only when it has a bus
		// to publish to, and the run's lifecycle events travel the same way. A
		// nil bus here made every plain sequence sprout step silent for its
		// whole duration.
		EventBus: bus,
	}
	applyStepLLMSelection(orch, resolveStepLLMSelection(ctx, step))
	if provider != "" {
		orch.Provider = provider
	}
	if model != "" {
		orch.Model = model
	}
	if baseURL != "" {
		orch.BaseURL = baseURL
	}
	return runSequenceSproutFn(ctx, orch, step.Transcript)
}

func runParallelSequenceStep(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath, genotype string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	branchName := derivedSequenceBranch(seq.Branch, step.ID)
	shadowPath, err := createShadowWorktreeFn(substratePath, branchName)
	if err != nil {
		return "", fmt.Errorf("create parallel worktree %s: %w", branchName, err)
	}
	injectMycorrhizalCacheFn(substratePath, shadowPath)
	defer removeShadowWorktreeFn(substratePath, shadowPath)

	orch := &DockerOrchestrator{
		Substrate:        shadowPath,
		SubstrateBranch:  branchName,
		StepID:           step.ID,
		IsCoordinator:    isMeristemStep(step.ID),
		Genotype:         genotype,
		Investigation:    step.Investigation,
		DisableMergeBack: true,
		AwaitsRunEnding:  true,
	}
	applyStepLLMSelection(orch, resolveStepLLMSelection(ctx, step))

	result, err := awaitDetachedRun(runSequenceSproutAtPathFn(ctx, orch, step.Transcript, substratePath, shadowPath))
	if err != nil {
		return result.Response, err
	}

	if err := mergePhloemChannelToHostFn(ctx, substratePath, branchName, step.ID); err != nil {
		return result.Response, err
	}

	return result.Response, nil
}

func runPhenotypicSelection(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (result string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	sourcePath := repoRoot(substratePath)
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = strings.TrimSpace(substratePath)
	}
	if strings.TrimSpace(sourcePath) == "" {
		return "", fmt.Errorf("phenotypic selection requires a substrate path")
	}
	if !isGitRepo(sourcePath) {
		return "", fmt.Errorf("phenotypic selection requires a git repository at %s", sourcePath)
	}

	selectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cleanupCtx := context.WithoutCancel(ctx)

	hostStashed, err := stashHostWorkspaceFn(ctx, sourcePath, step.ID)
	if err != nil {
		return "", err
	}
	defer func() {
		if !hostStashed {
			return
		}
		if restoreErr := restoreHostStashFn(cleanupCtx, sourcePath); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()

	branchBase := derivedSequenceBranch(seq.Branch, step.ID)
	if branchBase == "" {
		branchBase = sanitizeBranchComponent(step.ID)
		if branchBase == "" {
			branchBase = "phenotype"
		}
	}

	phenotypeCount := step.PhenotypesCount
	if phenotypeCount <= 0 {
		phenotypeCount = 1
	}
	llmSelection := resolveStepLLMSelection(ctx, step)

	resultsCh := make(chan phenotypeRunResult, phenotypeCount)
	var wg sync.WaitGroup
	for i := 0; i < phenotypeCount; i++ {
		index := i
		branchName := branchBase + "-phenotype-" + strconv.Itoa(index)
		wg.Add(1)
		go func() {
			defer wg.Done()

			shadowPath, err := createShadowWorktreeFn(sourcePath, branchName)
			if err != nil {
				resultsCh <- phenotypeRunResult{
					index:      index,
					branchName: branchName,
					err:        fmt.Errorf("create phenotype worktree %s: %w", branchName, err),
				}
				return
			}
			injectMycorrhizalCacheFn(sourcePath, shadowPath)
			defer removeShadowWorktreeFn(sourcePath, shadowPath)

			genotype := stepGenotype(step.ID)
			if isMeristemStep(step.ID) {
				genotype = "meristem"
			}

			orch := &DockerOrchestrator{
				Substrate:        sourcePath,
				SubstrateBranch:  branchName,
				StepID:           step.ID,
				IsCoordinator:    isMeristemStep(step.ID),
				Genotype:         genotype,
				Investigation:    step.Investigation,
				Temperature:      0.1 + float64(index)*0.3,
				DisableMergeBack: true,
				AwaitsRunEnding:  true,
			}
			applyStepLLMSelection(orch, llmSelection)

			runResult, runErr := awaitDetachedRun(runSequenceSproutAtPathFn(selectionCtx, orch, step.Transcript, sourcePath, shadowPath))
			if runErr != nil {
				resultsCh <- phenotypeRunResult{
					index:      index,
					branchName: branchName,
					err:        fmt.Errorf("phenotype %d (%s) sprout failed: %w", index, branchName, runErr),
				}
				return
			}

			if fitnessTest := strings.TrimSpace(step.FitnessTest); fitnessTest != "" {
				if fitnessErr := runContainerFitnessTestFn(selectionCtx, runResult.ImageName, shadowPath, fitnessTest); fitnessErr != nil {
					resultsCh <- phenotypeRunResult{
						index:      index,
						branchName: branchName,
						err:        fmt.Errorf("phenotype %d (%s) fitness test failed: %w", index, branchName, fitnessErr),
					}
					return
				}
			}

			resultsCh <- phenotypeRunResult{
				index:      index,
				branchName: branchName,
				response:   runResult.Response,
			}
		}()
	}

	defer func() {
		wg.Wait()
	}()
	defer cancel()

	var firstErr error
	var lastErr error
	for completed := 0; completed < phenotypeCount; completed++ {
		result := <-resultsCh
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			lastErr = result.err
			continue
		}

		cancel()
		mergeCtx := context.WithoutCancel(ctx)
		if mergeErr := mergePhenotypeBranchToHostFn(mergeCtx, sourcePath, result.branchName); mergeErr != nil {
			return "", mergeErr
		}

		return result.response, nil
	}

	if lastErr != nil {
		return "", lastErr
	}
	if firstErr != nil {
		return "", firstErr
	}

	return "", fmt.Errorf("phenotypic selection failed without a concrete error")
}

func isMeristemStep(stepID string) bool {
	stepID = strings.ToLower(strings.TrimSpace(stepID))
	return stepID == "meristem" || strings.HasPrefix(stepID, "meristem-")
}

func runSequenceSprout(ctx context.Context, orch *DockerOrchestrator, taskPrompt string) (response string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	stepID := strings.TrimSpace(orch.StepID)
	if stepID == "" {
		stepID = newSproutExecutionID("step")
		orch.StepID = stepID
	}

	// One terminal lifecycle event per sequence sprout, published from the
	// place the run happens — the same contract RunSprout keeps. Parallel
	// sub-sprouts call runSequenceSproutAtPathFn directly and report through
	// their own status channel, so publishing here cannot double-emit them.
	var executionOutcome string
	var executionFiles []string
	var executionFilesUnmeasured string
	// What the model asked for, carried alongside what was measured, because a
	// reclassification below has to answer "did the model change anything" and
	// the file list cannot: it is empty both when the model did nothing and
	// when the run never got far enough to look.
	var executionWroteWorkspace bool
	var executionRequestsMade bool
	var executionToolInvocations int
	defer func() {
		outcome := executionOutcome
		// A detached run has not ended, so it has no terminal event yet. The
		// goroutine still holding the run publishes it when the work ends;
		// publishing here would spend the run's one terminal event announcing
		// an ending that has not happened.
		if outcome == SproutOutcomeDetached && err == nil {
			return
		}
		// A failure anywhere (including commit or merge-back after a clean
		// Sprout turn) must reclassify: the run's provisional verdict cannot
		// stand once its results failed to land.
		if err != nil || outcome == "" {
			outcome = classifySproutOutcome(err, changeEvidence{
				modelWrote:    executionWroteWorkspace,
				measuredFiles: executionFiles,
			}, response, orch.Investigation)
		}
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		terminal := SproutRunReport{
			Outcome:         outcome,
			FilesModified:   executionFiles,
			FilesUnmeasured: executionFilesUnmeasured,
			RequestsMade:    executionRequestsMade,
			ToolInvocations: executionToolInvocations,
		}
		applyObservation(&terminal, err)
		publishSproutTerminal(orch.EventBus, stepID, orch.SessionID, terminal, reason)
	}()
	publishSproutEmerged(orch.EventBus, stepID, orch.SessionID, orch.Substrate)

	sourcePath := orch.Substrate

	if config, _ := LoadSubstratesConfig(""); config != nil {
		if plan, err := resolveSubstrateExecutionPlan(orch, config); err == nil && plan != nil && plan.hostPath != "" {
			sourcePath = plan.hostPath
		}
	}

	if sourcePath == "" {
		if wd, err := os.Getwd(); err == nil {
			sourcePath = wd
		} else {
			sourcePath = "."
		}
	}
	sourcePath = repoRoot(sourcePath)
	gitRepo := isGitRepo(sourcePath)

	mountPath := sourcePath
	var cleanup func()
	if gitRepo {
		shadowPath, err := createShadowWorktreeFn(sourcePath, orch.SubstrateBranch)
		if err == nil {
			mountPath = shadowPath
			injectMycorrhizalCacheFn(sourcePath, shadowPath)
			cleanup = func() {
				removeShadowWorktreeFn(sourcePath, shadowPath)
			}
		} else if allowHostWorkspace() {
			fmt.Fprintf(os.Stderr, "⚠️  Failed to create shadow worktree: %v. Using active workspace (%s).\n", err, EnvAllowHostWorkspace)
			if orch.EventBus != nil {
				orch.EventBus.Publish(eventbus.Event{
					Type:      eventbus.EventHostExecutionActivated,
					Source:    stepID,
					SessionID: orch.SessionID,
					Data: map[string]interface{}{
						"workspace": sourcePath,
						"stepId":    stepID,
					},
				})
			}
		} else {
			return "", fmt.Errorf("isolation could not be established (create shadow worktree: %w); set %s=true to run in the active workspace", err, EnvAllowHostWorkspace)
		}
	}

	var detachedEnd <-chan detachedRunEnding
	if cleanup != nil {
		defer func() {
			if detachedEnd != nil {
				// The Sprout is still growing in this worktree. Removing it now
				// would delete the work out from under a run that has not
				// finished, so the removal waits for the run to end — on a
				// goroutine, because this call is no longer waiting either.
				go func() {
					<-detachedEnd
					cleanup()
				}()
				return
			}
			cleanup()
		}()
	}

	executionResult, err := runSequenceSproutAtPathFn(ctx, orch, taskPrompt, sourcePath, mountPath)
	detachedEnd = executionResult.DetachedEnd
	executionOutcome = executionResult.Outcome
	executionFiles = executionResult.FilesModified
	executionFilesUnmeasured = executionResult.FilesUnmeasured
	executionWroteWorkspace = executionResult.WroteWorkspace
	executionRequestsMade = executionResult.RequestsMade
	executionToolInvocations = executionResult.ToolInvocations
	if err != nil {
		if orch.DisableMergeBack && strings.TrimSpace(executionResult.CommitHash) != "" {
			return executionResult.CommitHash, err
		}
		return "", err
	}

	if orch.DisableMergeBack && strings.TrimSpace(executionResult.CommitHash) != "" {
		return executionResult.CommitHash, nil
	}

	if executionResult.Response != "" {
		return executionResult.Response, nil
	}

	return executionResult.CommitHash, nil
}

func runSequenceSproutAtPath(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (result sproutExecutionResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if orch == nil {
		orch = &DockerOrchestrator{}
	}

	stepID := strings.TrimSpace(orch.StepID)
	if stepID == "" {
		stepID = newSproutExecutionID("step")
		orch.StepID = stepID
	}

	sourcePath = repoRoot(sourcePath)
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = "."
	}
	gitRepo := isGitRepo(sourcePath)
	cleanupCtx := context.WithoutCancel(ctx)

	// Teardown a detached run must NOT take with it — the terrarium it is still
	// growing in and the host workspace state it still owns. Collected rather
	// than deferred one by one so the goroutine that outlives a detached call
	// runs the same sequence, in the same order, once the work has ended. Same
	// shape as RunSprout, for the same reason.
	detached := false
	var teardown []func()
	var teardownErr error
	runTeardown := func() {
		for index := len(teardown) - 1; index >= 0; index-- {
			teardown[index]()
		}
	}
	defer func() {
		if detached {
			return
		}
		runTeardown()
		err = errors.Join(err, teardownErr)
	}()

	hostStashed := false
	if gitRepo && !orch.DisableMergeBack {
		hostStashed, err = stashHostWorkspaceFn(ctx, sourcePath, stepID)
		if err != nil {
			return result, err
		}
		teardown = append(teardown, func() {
			if !hostStashed {
				return
			}
			if restoreErr := restoreHostStashFn(cleanupCtx, sourcePath); restoreErr != nil {
				teardownErr = errors.Join(teardownErr, restoreErr)
			}
		})
	}

	if orch.Genotype != "" {
		if err := stagePlasmidsForGenotype(sourcePath, mountPath, orch.Genotype); err != nil {
			return result, err
		}
	}

	// Note: even for a "macrophage" step, the Sprout's own session below still
	// uses the ordinary per-language image (opentendril-go:latest for a Go
	// workspace) to write the fuzz test file via the normal tool-call
	// protocol. The deterministic fuzz-*execution* half after the Sprout turn
	// runs in a separate, Go-toolchain-enabled terrarium (macrophageFuzzImage,
	// toolchains/go-fuzz/Dockerfile) — see runMacrophageFuzzCheck below.
	imageName := orch.resolveImageName(mountPath)
	result.ImageName = imageName
	if err := ensureSproutImageFn(ctx, imageName); err != nil {
		return result, err
	}

	substratesConfig, _ := LoadSubstratesConfig("")
	sequencePlan, planErr := resolveSubstrateExecutionPlan(orch, substratesConfig)

	// The caller's own context, kept before the growth budget narrows ctx: the
	// work below hangs off it and outlives the wait, exactly as in RunSprout.
	callerCtx := ctx

	// Bound the WAIT by bounding the CONTEXT this function blocks on.
	// context.WithTimeoutCause never extends an existing parent deadline, so
	// this path's own tighter budget still wins when it has one, and the cause
	// records whose clock expired — only the growth budget's own expiry may
	// detach. cleanupCtx above stays unbounded so the host stash is restored
	// even after the budget is spent.
	growthBudget := time.Duration(0)
	if planErr == nil && sequencePlan != nil {
		growthBudget = sequencePlan.growthBudget
	}
	if growthBudget > 0 {
		var cancelGrowth context.CancelFunc
		ctx, cancelGrowth = context.WithTimeoutCause(ctx, growthBudget, errGrowthBudgetSpent)
		defer cancelGrowth()
	}

	providerName := resolveTerrariumProviderName(ctx, orch)
	if planErr == nil && sequencePlan != nil && sequencePlan.provider != "" {
		providerName = sequencePlan.provider
	}

	var command []string
	if sequencePlan != nil {
		command = sequencePlan.command
	}

	obs := terrarium.ActivationObserver(func(name string) {
		if orch.EventBus != nil {
			orch.EventBus.Publish(eventbus.Event{
				Type:      eventbus.EventHostExecutionActivated,
				Source:    stepID,
				SessionID: orch.SessionID,
				Data: map[string]interface{}{
					"provider": name,
					"stepId":   stepID,
				},
			})
		}
	})

	// The terrarium belongs to the WORK, not to the wait, so it is created on
	// the work's own context — see the equivalent point in RunSprout. Starting
	// it on the growth budget would kill the container the moment the Stem
	// stopped waiting, which is a kill wearing the word detach.
	reapBudget := time.Duration(0)
	if planErr == nil && sequencePlan != nil {
		reapBudget = sequencePlan.reapBudget
	}
	workCtx, releaseWork := newSproutWorkContext(callerCtx, reapBudget)
	defer func() {
		if detached {
			return
		}
		releaseWork(nil)
	}()

	session, err := startTerrariumSessionFn(workCtx, providerName, imageName, mountPath, orch.Investigation, command, nil, deriveWatchdogTimeout(workCtx), obs)
	if err != nil {
		return result, err
	}
	teardown = append(teardown, func() { _ = session.Close() })

	// The orchestrator's bus, not nil: the Sprout streams only when it has one
	// to publish to, so passing nil made every sequence sprout step — a
	// delegated Codex run among them — silent for its whole duration, leaving a
	// wall clock as the only way to judge it.
	sprout, err := newSproutFn(workCtx, mountPath, sourcePath, orch.Genotype, orch.resolveLLMClient(), session, orch.EventBus, orch.StepID, orch.SessionID)
	if err != nil {
		return result, err
	}

	// The dormancy watcher runs on the WORK's context and is stopped by the
	// teardown sequence, not by this function returning: a detached call
	// returns while the Sprout is still growing, and a watcher torn down at
	// that moment would go blind at exactly the point nobody else is looking.
	scratchInterval := time.Duration(0)
	if planErr == nil && sequencePlan != nil {
		scratchInterval = sequencePlan.scratchInterval
	}
	var dormancyInspector terrariumInspector
	if insp, ok := session.(terrariumInspector); ok {
		dormancyInspector = insp
	}
	var dormancySnapshot sproutSnapshot
	if snap, ok := sprout.(sproutSnapshot); ok {
		dormancySnapshot = snap
	}
	teardown = append(teardown, watchDormancy(workCtx, orch.EventBus, scratchInterval, mountPath, dormancyInspector, dormancySnapshot))

	// completeRun measures, classifies and records what the run did. It is a
	// closure rather than the tail of this function because a detached call
	// reaches it from the goroutine below, long after this function returned —
	// so it owns every value it touches and shares nothing with the caller's
	// result, which by then belongs to somebody else.
	completeRun := func(sproutResult sproutResult, runErr error) (result sproutExecutionResult, changes changeEvidence, err error) {
		result.ImageName = imageName
		// Recorded before every exit below: what the model asked the terrarium
		// to do is known the moment the turn ends, and each of those exits is
		// asked whether the run did any work.
		changes.modelWrote = sproutResult.WroteWorkspace
		result.WroteWorkspace = sproutResult.WroteWorkspace

		// The post-mortem runs on its own clock, for the reason given at the
		// equivalent point in RunSprout: this is the path that actually carries
		// the growth budget, so it is the path where a spent context would
		// otherwise take the run's evidence down with it.
		postMortemCtx, cancelPostMortem := context.WithTimeout(cleanupCtx, sproutPostMortemBudget)
		defer cancelPostMortem()
		if err := session.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Sprout session shutdown issue: %v\n", err)
		}

		result.Response = sproutResult.Response
		result.Protocol = sproutResult.Protocol
		result.RequestsMade = sproutResult.RequestsMade
		result.ToolInvocations = sproutResult.ToolInvocations
		if sproutResult.ActionResult != nil {
			verdict := strings.ToUpper(strings.TrimSpace(sproutResult.ActionResult.Verdict))
			switch verdict {
			case "DANGEROUS":
				if quarantineErr := quarantineScriptPrompt(stepID, taskPrompt); quarantineErr != nil {
					return result, changes, errors.Join(fmt.Errorf("script quarantined: %v", sproutResult.ActionResult.Risks), quarantineErr)
				}
				return result, changes, fmt.Errorf("script quarantined: %v", sproutResult.ActionResult.Risks)
			case "REVIEW":
				return result, changes, ErrRequiresReview
			case "SAFE":
			case "":
			default:
				return result, changes, fmt.Errorf("unknown script review verdict %q", sproutResult.ActionResult.Verdict)
			}
		}

		// Symbiotic Immune System: once the Macrophage's Sprout turn
		// has written its fuzz test, deterministically run it — no LLM judgment
		// call — and treat a crash exactly like a Verifier compiler/test failure,
		// so shouldBudRecursiveDebugger sprouts a Debugger to fix it and retries.
		// Skipped if the Sprout turn itself already failed; nothing to fuzz.
		if runErr == nil && orch.Genotype == "macrophage" {
			if fuzzErr := runMacrophageFuzzCheckFn(workCtx, providerName, mountPath); fuzzErr != nil {
				runErr = fuzzErr
			}
		}

		if !gitRepo {
			if runErr != nil {
				return result, changes, runErr
			}
			return result, changes, nil
		}

		// A measurement that fails is not a run that failed — see the equivalent
		// point in RunSprout. Record why the evidence is missing and carry on to
		// the record-keeping rather than returning and losing all of it.
		measuredFiles, diffErr := collectStageableFilesFn(postMortemCtx, mountPath, "tendril-status.json")
		if diffErr != nil {
			result.FilesUnmeasured = diffErr.Error()
			fmt.Fprintf(os.Stderr, "⚠️ Could not measure the files this run changed: %v\n", diffErr)
		} else {
			changes.measured = true
			changes.measuredFiles = measuredFiles
		}

		// The diff of the mount is not the run's change set — see the
		// equivalent point in RunSprout. What the run is answerable for is
		// what the model did.
		modifiedFiles := changes.attributedFiles()

		var gitDiff string
		if !orch.DisableMergeBack {
			gitDiff, diffErr = collectGitDiffFn(postMortemCtx, mountPath)
			if diffErr != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to collect git diff for epigenetic chronicler: %v\n", diffErr)
			}
		}

		executionStatus := sproutExecutionStatus{
			StepID:          stepID,
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			FilesModified:   modifiedFiles,
			FilesUnmeasured: result.FilesUnmeasured,
			Status:          classifySproutOutcome(runErr, changes, sproutResult.Response, orch.Investigation),
		}
		if runErr != nil {
			executionStatus.Error = runErr.Error()
		}
		result.Outcome = executionStatus.Status
		result.FilesModified = modifiedFiles

		isReviewableFruit := executionStatus.Status == SproutOutcomeComplete &&
			len(modifiedFiles) > 0 &&
			runErr == nil &&
			!orch.Investigation &&
			changes.measured && result.FilesUnmeasured == ""

		if isReviewableFruit {
			var sequenceCredential ResolvedCredential
			if sequencePlan != nil {
				sequenceCredential = sequencePlan.credential
			}
			commitHash, commitErr := commitTerrariumExecutionFn(postMortemCtx, mountPath, sourcePath, "", executionStatus, taskPrompt, sequenceCredential)
			if commitErr != nil {
				if runErr != nil {
					return result, changes, errors.Join(runErr, commitErr)
				}
				return result, changes, commitErr
			}

			result.CommitHash = commitHash

			if orch.DisableMergeBack {
				return result, changes, runErr
			}

			mergeErr := mergeSequenceTerrariumCommit(postMortemCtx, sourcePath, commitHash)
			if mergeErr != nil {
				if runErr != nil {
					return result, changes, errors.Join(runErr, mergeErr)
				}
				return result, changes, mergeErr
			}

			if gitDiff != "" && runErr == nil {
				chronicler := newRunChroniclerFn(sourcePath, llm.TierCheapest)
				if _, err := chronicler.TranscribeLearnings(postMortemCtx, sproutResult.Transcript, gitDiff, session.Logs()); err != nil {
					fmt.Fprintf(os.Stderr, "⚠️ Epigenetic chronicler skipped: %v\n", err)
				}
			}
		} else {
			if runErr != nil {
				return result, changes, runErr
			}
		}

		if fitErr := RecordGenomicFitness(sourcePath, runErr == nil); fitErr != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Genome fitness record skipped: %v\n", fitErr)
		}

		return result, changes, nil
	}

	// The Sprout turn runs on the work's clock, in its own goroutine, so this
	// function can stop waiting without stopping it — the same separation
	// RunSprout makes, and the one this path needs most, because this is where
	// the parallel budget actually flows.
	turns := make(chan sproutTurn, 1)
	go func() {
		sproutResult, runErr := sprout.Run(workCtx, taskPrompt)
		turns <- sproutTurn{result: sproutResult, err: runErr}
	}()

	var turn sproutTurn
	select {
	case turn = <-turns:
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errGrowthBudgetSpent) && !orch.AwaitsRunEnding {
			// The Stem stops waiting; the Sprout keeps growing. Nothing here
			// closes the session or restores the host stash — the run still
			// owns both — and the caller is told to hold its own teardown too,
			// through the channel below, because the worktree it would remove
			// is the one the Sprout is still writing to.
			detached = true
			detachedEnd := make(chan detachedRunEnding, 1)
			publishSproutDetached(orch.EventBus, stepID, orch.SessionID, growthBudget)
			fmt.Fprintf(os.Stderr, "🌿 Growth budget %s spent; detaching from %s. The Sprout keeps growing.\n", growthBudget, stepID)
			go func() {
				defer close(detachedEnd)
				finished := <-turns
				// Named before the work's context is released: the cause is
				// what says which clock ended the run, and releasing overwrites
				// it with a bare cancellation.
				runErr := attributeSproutEnding(workCtx, finished.err)
				detachedResult, detachedChanges, detachedErr := completeRun(finished.result, runErr)
				releaseWork(nil)
				runTeardown()
				detachedErr = errors.Join(detachedErr, teardownErr)
				// The single terminal event for a detached run is published
				// here rather than by runSequenceSprout, which returned long
				// ago. That caller skips its own publish for exactly this
				// outcome, so the run still gets one terminal event and gets it
				// when the work actually ended.
				outcome := detachedResult.Outcome
				if detachedResult.Outcome == "" {
					if detachedResult.Response == "" && detachedChanges.measured && len(detachedResult.FilesModified) == 0 && detachedErr == nil {
						outcome = SproutOutcomeNoEngagement
					} else {
						outcome = classifySproutOutcome(detachedErr, detachedChanges, detachedResult.Response, orch.Investigation)
					}
				}
				reason := ""
				if detachedErr != nil {
					reason = detachedErr.Error()
				}
				detachedResult.Outcome = outcome
				terminal := SproutRunReport{
					Outcome:         outcome,
					FilesModified:   detachedResult.FilesModified,
					FilesUnmeasured: detachedResult.FilesUnmeasured,
					RequestsMade:    detachedResult.RequestsMade,
					ToolInvocations: detachedResult.ToolInvocations,
				}
				applyObservation(&terminal, detachedErr)
				publishSproutTerminal(orch.EventBus, stepID, orch.SessionID, terminal, reason)
				detachedEnd <- detachedRunEnding{result: detachedResult, err: detachedErr}
			}()
			result.ImageName = imageName
			result.Outcome = SproutOutcomeDetached
			result.DetachedEnd = detachedEnd
			return result, nil
		}
		// Not the growth budget: the caller's own deadline expired or the
		// caller cancelled, and nothing else will carry the work on. End it
		// here and account for it here — detaching would report a run as still
		// growing when nothing is growing it.
		releaseWork(context.Cause(ctx))
		turn = <-turns
	}

	result, _, err = completeRun(turn.result, attributeSproutEnding(workCtx, turn.err))
	return result, err
}

func quarantineScriptPrompt(stepID, taskPrompt string) error {
	quarantineDir := QuarantineDir()
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		return fmt.Errorf("create quarantine directory: %w", err)
	}

	fileStepID := sanitizeBranchComponent(stepID)
	if fileStepID == "" {
		fileStepID = "step"
	}
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	quarantinePath := filepath.Join(quarantineDir, fileStepID+"-"+timestamp+".txt")
	if err := os.WriteFile(quarantinePath, []byte(taskPrompt), 0o600); err != nil {
		return fmt.Errorf("write quarantine file: %w", err)
	}

	return nil
}

func mergeSequenceTerrariumCommit(ctx context.Context, sourcePath, commitHash string) error {
	if _, err := runGitCommand(ctx, sourcePath, "merge", "--no-ff", "--no-edit", "--", commitHash); err != nil {
		return err
	}
	return nil
}

func mergePhenotypeBranchToHost(ctx context.Context, sourcePath, branchName string) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	sourcePath = repoRoot(sourcePath)
	branchName = strings.TrimSpace(branchName)
	if strings.TrimSpace(sourcePath) == "" {
		return fmt.Errorf("source path is empty")
	}
	if branchName == "" {
		return fmt.Errorf("branch name is empty")
	}

	cleanupCtx := context.WithoutCancel(ctx)
	hostStashed, err := stashHostWorkspaceFn(ctx, sourcePath, "phenotype-merge-"+sanitizeBranchComponent(branchName))
	if err != nil {
		return err
	}
	if hostStashed {
		defer func() {
			if restoreErr := restoreHostStashFn(cleanupCtx, sourcePath); restoreErr != nil {
				err = errors.Join(err, restoreErr)
			}
		}()
	}

	if _, err = runGitCommand(cleanupCtx, sourcePath, "merge", "--ff-only", "--", branchName); err != nil {
		return err
	}

	return nil
}

func mergePhloemChannelToHost(ctx context.Context, sourcePath, branchName, stepID string) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	sourcePath = repoRoot(sourcePath)
	branchName = strings.TrimSpace(branchName)
	stepID = strings.TrimSpace(stepID)
	if strings.TrimSpace(sourcePath) == "" {
		return fmt.Errorf("source path is empty")
	}
	if branchName == "" {
		return fmt.Errorf("branch name is empty")
	}

	cleanupCtx := context.WithoutCancel(ctx)
	hostStashed, err := stashHostWorkspaceFn(ctx, sourcePath, "phloem-merge-"+sanitizeBranchComponent(stepID))
	if err != nil {
		return err
	}
	if hostStashed {
		defer func() {
			if restoreErr := restoreHostStashFn(cleanupCtx, sourcePath); restoreErr != nil {
				err = errors.Join(err, restoreErr)
			}
		}()
	}

	mergeMessage := fmt.Sprintf("chore: merge parallel step %s", stepID)
	if _, err = runGitCommand(cleanupCtx, sourcePath, "merge", "--no-ff", "-m", mergeMessage, "--", branchName); err != nil {
		if _, abortErr := runGitCommand(cleanupCtx, sourcePath, "merge", "--abort"); abortErr != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to abort parallel merge for %s: %v\n", stepID, abortErr)
		}
		return err
	}

	return nil
}

func derivedSequenceBranch(baseBranch, stepID string) string {
	base := strings.TrimSpace(baseBranch)
	if base == "" {
		return ""
	}

	component := sanitizeBranchComponent(stepID)
	if component == "" {
		return base
	}

	if strings.HasSuffix(base, "/") {
		return base + component
	}
	return base + "/" + component
}

func sanitizeBranchComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}

	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}

	sanitized := strings.Trim(builder.String(), "-")
	return sanitized
}
