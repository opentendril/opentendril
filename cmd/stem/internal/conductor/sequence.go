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

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/terrarium"
	"github.com/opentendril/core/roots/llm"
	"gopkg.in/yaml.v3"
)

const (
	sequenceStatusPending  = "pending"
	sequenceStatusComplete = "complete"
	sequenceStatusFailed   = "failed"

	sequenceOnFailureHalt  = "halt"
	sequenceOnFailureRetry = "retry"
	sequenceOnFailurePause = "pause"

	defaultSequenceRetryLimit = 3
)

var ErrRequiresReview = errors.New("script requires review")

// Sequence describes a DAG workflow stored as YAML.
type Sequence struct {
	Name             string         `yaml:"name"`
	System           bool           `yaml:"system,omitempty"`
	Substrate        string         `yaml:"substrate"`
	Branch           string         `yaml:"branch"`
	ConcurrencyLimit int            `yaml:"concurrencyLimit"`
	OnFailure        string         `yaml:"onFailure"`
	MaxRetries       int            `yaml:"maxRetries"`
	Steps            []SequenceStep `yaml:"steps"`
}

// SequenceStep describes one executable node in a sequence.
type SequenceStep struct {
	ID              string   `yaml:"id"`
	Status          string   `yaml:"status"`
	DependsOn       []string `yaml:"dependsOn,omitempty"`
	DependsOnLegacy []string `yaml:"depends_on,omitempty"`
	Transcript      string   `yaml:"transcript"`
	// Command, when set, makes this a deterministic verifier/CI step: the
	// Conductor execs the command directly in the toolchain verifier terrarium
	// (read-only, no LLM, no merge-back) and the exit code is the verdict.
	Command           []string `yaml:"command,omitempty"`
	Parallel          bool     `yaml:"parallel,omitempty"`
	SproutCount       int      `yaml:"sproutCount,omitempty"`
	MergeTranscript   string   `yaml:"mergeTranscript,omitempty"`
	PhenotypesCount   int      `yaml:"phenotypesCount,omitempty"`
	FitnessTest       string   `yaml:"fitnessTest,omitempty"`
	RequiresReasoning bool     `yaml:"requiresReasoning,omitempty"`
	RequiresVision    bool     `yaml:"requiresVision,omitempty"`
	ModelProvider     string   `yaml:"modelProvider,omitempty"`
	ModelName         string   `yaml:"modelName,omitempty"`
	ModelBaseURL      string   `yaml:"modelBaseURL,omitempty"`

	// Selection, when present, promotes a step from single-shot execution to a
	// true generational genetic algorithm (phenotypic selection). See
	// SelectionConfig and selection.go.
	Selection *SelectionConfig `yaml:"selection,omitempty"`
}

// SelectionConfig governs a generational genetic algorithm for a step. When a
// step carries a non-nil Selection, the Stem grows a population of mutated
// Phenotypes per generation, scores each against a numeric fitness metric,
// breeds the fittest survivors into the next generation's Genotypes, and grafts
// the single AlphaPhenotype (the fittest variant discovered across all
// generations) back into the substrate.
type SelectionConfig struct {
	// PopulationSize is the number of parallel Phenotype sprouts grown per
	// generation. Defaults to defaultSelectionPopulation, clamped to
	// [minSelectionPopulation, maxSelectionPopulation].
	PopulationSize int `yaml:"populationSize,omitempty"`
	// MaxGenerations bounds the generational loop. Defaults to
	// defaultSelectionGenerations, clamped to [1, maxSelectionGenerations].
	MaxGenerations int `yaml:"maxGenerations,omitempty"`
	// FitnessTest is the shell command run inside each Phenotype's terrarium
	// (e.g. "make benchmark"). Its stdout/stderr is parsed for a FitnessScore.
	// Falls back to the step-level FitnessTest when empty.
	FitnessTest string `yaml:"fitnessTest,omitempty"`
	// FitnessPattern is an optional regular expression whose first capture group
	// (or whole match) yields the numeric FitnessScore from the test output.
	// When empty the engine looks for a Go "<n> ns/op" benchmark line and then
	// falls back to the last number in the output.
	FitnessPattern string `yaml:"fitnessPattern,omitempty"`
	// FitnessGoal is "minimize" (default, e.g. ns/op or latency) or "maximize"
	// (e.g. throughput or ops/sec).
	FitnessGoal string `yaml:"fitnessGoal,omitempty"`
	// FitnessThreshold, when set, stops evolution early as soon as the
	// AlphaPhenotype's score reaches it (<= for minimize, >= for maximize).
	FitnessThreshold *float64 `yaml:"fitnessThreshold,omitempty"`
	// SurvivorFraction is the top percentile of a generation carried forward as
	// breeding parents. Defaults to defaultSelectionSurvivorFraction, clamped to
	// (0, 1].
	SurvivorFraction float64 `yaml:"survivorFraction,omitempty"`
	// MutationTemperature is the base LLM sampling temperature for the
	// population. Defaults to defaultSelectionMutationTemperature.
	MutationTemperature float64 `yaml:"mutationTemperature,omitempty"`
	// TemperatureSpread widens temperature across the population so lower-index
	// Phenotypes exploit and higher-index Phenotypes explore. Defaults to
	// defaultSelectionTemperatureSpread.
	TemperatureSpread float64 `yaml:"temperatureSpread,omitempty"`
}

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
}

type sequenceStepResult struct {
	stepID string
	output string
	err    error
}

// LoadSequence reads a sequence definition from YAML.
func LoadSequence(path string) (*Sequence, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sequence %s: %w", path, err)
	}

	var seq Sequence
	if err := yaml.Unmarshal(content, &seq); err != nil {
		return nil, fmt.Errorf("decode sequence %s: %w", path, err)
	}

	if err := normalizeSequence(path, &seq); err != nil {
		return nil, err
	}

	return &seq, nil
}

// SaveSequence writes a sequence definition to YAML atomically.
func SaveSequence(path string, seq *Sequence) error {
	if seq == nil {
		return fmt.Errorf("sequence is nil")
	}
	if err := normalizeSequence(path, seq); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create sequence directory: %w", err)
	}

	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp sequence file: %w", err)
	}

	enc := yaml.NewEncoder(tmpFile)
	enc.SetIndent(2)
	if err := enc.Encode(seq); err != nil {
		_ = enc.Close()
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return fmt.Errorf("encode sequence %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return fmt.Errorf("finalize sequence %s: %w", path, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		return fmt.Errorf("close sequence %s: %w", path, err)
	}
	if err := os.Rename(tmpFile.Name(), path); err != nil {
		_ = os.Remove(tmpFile.Name())
		return fmt.Errorf("replace sequence %s: %w", path, err)
	}

	return nil
}

// ResolveSequencePath finds a YAML file in the current repo or by relative path.
func ResolveSequencePath(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", fmt.Errorf("sequence path is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	root := repoRoot(cwd)

	candidates := sequencePathCandidates(trimmed, cwd, root)
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve sequence path %s: %w", candidate, err)
		}
		return abs, nil
	}

	return "", fmt.Errorf("sequence %q not found", trimmed)
}

// ListSequenceFiles returns available YAML files from system configs and .tendril/sequences.
func ListSequenceFiles(basePath string) ([]string, error) {
	root := strings.TrimSpace(basePath)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			root = "."
		} else {
			root = cwd
		}
	}
	root = repoRoot(root)

	var searchDirs []string
	if configDir, err := os.UserConfigDir(); err == nil {
		searchDirs = append(searchDirs, filepath.Join(configDir, "opentendril", "sequences"))
	}
	searchDirs = append(searchDirs, filepath.Join("/etc", "opentendril", "sequences"))
	searchDirs = append(searchDirs, filepath.Join(root, ".tendril", "sequences"))

	fileSet := make(map[string]bool)

	for _, dir := range searchDirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}

		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			lower := strings.ToLower(entry.Name())
			if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
				return nil
			}

			// For system paths, we want to return just the base name since it's global
			// For workspace paths, we can return the relative path
			var rel string
			if strings.HasPrefix(path, root) {
				rel, _ = filepath.Rel(root, path)
				rel = filepath.ToSlash(rel)
			} else {
				rel = entry.Name()
			}

			fileSet[rel] = true
			return nil
		})
	}

	var files []string
	for file := range fileSet {
		files = append(files, file)
	}

	sort.Strings(files)
	return files, nil
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

func normalizeSequence(path string, seq *Sequence) error {
	if seq == nil {
		return fmt.Errorf("sequence is nil")
	}

	if strings.TrimSpace(seq.Name) == "" {
		base := filepath.Base(path)
		seq.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	seq.Name = strings.TrimSpace(seq.Name)
	seq.Substrate = strings.TrimSpace(seq.Substrate)
	seq.Branch = strings.TrimSpace(seq.Branch)
	seq.OnFailure = strings.ToLower(strings.TrimSpace(seq.OnFailure))
	if seq.OnFailure == "" {
		seq.OnFailure = sequenceOnFailureHalt
	}
	switch seq.OnFailure {
	case sequenceOnFailureHalt, sequenceOnFailureRetry, sequenceOnFailurePause:
	default:
		return fmt.Errorf("sequence %s has invalid onFailure value %q", path, seq.OnFailure)
	}

	if seq.ConcurrencyLimit <= 0 {
		seq.ConcurrencyLimit = 1
	}
	if seq.MaxRetries < 0 {
		seq.MaxRetries = 0
	}

	seen := make(map[string]struct{}, len(seq.Steps))
	for i := range seq.Steps {
		step := &seq.Steps[i]
		step.ID = strings.TrimSpace(step.ID)
		if step.ID == "" {
			return fmt.Errorf("sequence %s contains a step with an empty id", path)
		}
		if _, ok := seen[step.ID]; ok {
			return fmt.Errorf("sequence %s contains duplicate step id %q", path, step.ID)
		}
		seen[step.ID] = struct{}{}

		step.Status = strings.ToLower(strings.TrimSpace(step.Status))
		if step.Status == "" {
			step.Status = sequenceStatusPending
		}
		switch step.Status {
		case sequenceStatusPending, sequenceStatusComplete, sequenceStatusFailed:
		default:
			return fmt.Errorf("sequence %s step %s has invalid status %q", path, step.ID, step.Status)
		}

		dependsOn := step.DependsOn
		if len(dependsOn) == 0 && len(step.DependsOnLegacy) > 0 {
			dependsOn = append([]string(nil), step.DependsOnLegacy...)
		}

		deps := make([]string, 0, len(dependsOn))
		depSeen := make(map[string]struct{}, len(dependsOn))
		for _, dep := range dependsOn {
			trimmed := strings.TrimSpace(dep)
			if trimmed == "" {
				continue
			}
			if trimmed == step.ID {
				return fmt.Errorf("sequence %s step %s cannot depend on itself", path, step.ID)
			}
			if _, ok := depSeen[trimmed]; ok {
				continue
			}
			depSeen[trimmed] = struct{}{}
			deps = append(deps, trimmed)
		}
		step.DependsOn = deps
		step.DependsOnLegacy = nil
		step.Transcript = strings.TrimSpace(step.Transcript)
		step.MergeTranscript = strings.TrimSpace(step.MergeTranscript)
		if step.Parallel {
			if step.SproutCount <= 0 {
				step.SproutCount = defaultParallelSproutCount
			}
			if step.SproutCount > maxParallelSproutCount {
				step.SproutCount = maxParallelSproutCount
			}
		}
		if step.PhenotypesCount <= 0 {
			step.PhenotypesCount = 1
		}
		step.FitnessTest = strings.TrimSpace(step.FitnessTest)
		if err := normalizeSelectionConfig(path, step); err != nil {
			return err
		}
	}

	return nil
}

func normalizeSelectionConfig(path string, step *SequenceStep) error {
	cfg := step.Selection
	if cfg == nil {
		return nil
	}

	if cfg.PopulationSize <= 0 {
		cfg.PopulationSize = defaultSelectionPopulation
	}
	if cfg.PopulationSize < minSelectionPopulation {
		cfg.PopulationSize = minSelectionPopulation
	}
	if cfg.PopulationSize > maxSelectionPopulation {
		cfg.PopulationSize = maxSelectionPopulation
	}

	if cfg.MaxGenerations <= 0 {
		cfg.MaxGenerations = defaultSelectionGenerations
	}
	if cfg.MaxGenerations > maxSelectionGenerations {
		cfg.MaxGenerations = maxSelectionGenerations
	}

	cfg.FitnessTest = strings.TrimSpace(cfg.FitnessTest)
	if cfg.FitnessTest == "" {
		cfg.FitnessTest = step.FitnessTest
	}
	if cfg.FitnessTest == "" {
		return fmt.Errorf("sequence %s step %s enables selection but sets no fitnessTest", path, step.ID)
	}

	cfg.FitnessPattern = strings.TrimSpace(cfg.FitnessPattern)
	cfg.FitnessGoal = strings.ToLower(strings.TrimSpace(cfg.FitnessGoal))
	switch cfg.FitnessGoal {
	case "":
		cfg.FitnessGoal = selectionGoalMinimize
	case selectionGoalMinimize, selectionGoalMaximize:
	default:
		return fmt.Errorf("sequence %s step %s has invalid selection fitnessGoal %q", path, step.ID, cfg.FitnessGoal)
	}

	if cfg.SurvivorFraction <= 0 {
		cfg.SurvivorFraction = defaultSelectionSurvivorFraction
	}
	if cfg.SurvivorFraction > 1 {
		cfg.SurvivorFraction = 1
	}

	if cfg.MutationTemperature <= 0 {
		cfg.MutationTemperature = defaultSelectionMutationTemperature
	}
	if cfg.MutationTemperature > maxSelectionTemperature {
		cfg.MutationTemperature = maxSelectionTemperature
	}
	if cfg.TemperatureSpread < 0 {
		cfg.TemperatureSpread = 0
	}
	if cfg.TemperatureSpread == 0 {
		cfg.TemperatureSpread = defaultSelectionTemperatureSpread
	}

	return nil
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
		if seq.OnFailure == sequenceOnFailureRetry {
			retries := seq.MaxRetries
			if retries <= 0 {
				retries = defaultSequenceRetryLimit
			}
			runner.retriesLeft[step.ID] = retries
		}
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

func (r *sequenceRunner) run(ctx context.Context) (*Sequence, error) {
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
	running := 0
	completed := 0
	for _, step := range r.seq.Steps {
		if step.Status == sequenceStatusComplete {
			completed++
		}
	}

	dispatch := func(stepID string) {
		step := r.stepByID[stepID]
		if step == nil {
			return
		}
		r.queued[stepID] = false
		running++
		go func(id string, snapshot SequenceStep) {
			output, err := r.opts.StepRunner(ctx, r.seq, &snapshot, r.substratePath)
			resultCh <- sequenceStepResult{stepID: id, output: output, err: err}
		}(stepID, *step)
	}

	for {
		for running < concurrencyLimit {
			nextID, ok := r.popReady()
			if !ok {
				break
			}
			fmt.Fprintf(r.opts.Stdout, "→ [%s] starting\n", nextID)
			dispatch(nextID)
		}

		if completed == len(r.seq.Steps) {
			fmt.Fprintf(r.opts.Stdout, "✅ Sequence %s complete\n", r.seq.Name)
			if err := SaveSequence(r.path, r.seq); err != nil {
				return r.seq, err
			}
			r.publishSequenceEvent(eventbus.EventSequenceComplete, "", nil, map[string]interface{}{
				"sequence": r.seq.Name,
			})
			return r.seq, nil
		}

		if running == 0 && len(r.ready) == 0 {
			msg := r.describeStall()
			if msg == "" {
				msg = fmt.Sprintf("sequence %s stalled", r.seq.Name)
			}
			return r.seq, errors.New(msg)
		}

		select {
		case <-ctx.Done():
			// Drain in-flight step workers before returning. Each worker runs
			// StepRunner's deferred host-stash restore (through a WithoutCancel
			// cleanup context) and only then sends its result, so waiting for
			// those sends guarantees a cancelled run never strands the user's
			// stashed workspace. The docker steps are ctx-bound and unwind
			// promptly; the CLI's signal handler force-quits as a backstop.
			for running > 0 {
				<-resultCh
				running--
			}
			return r.seq, ctx.Err()
		case result := <-resultCh:
			running--
			step := r.stepByID[result.stepID]
			if step == nil {
				continue
			}
			delete(r.queued, result.stepID)

			if result.err == nil {
				step.Status = sequenceStatusComplete
				completed++
				if output := strings.TrimSpace(result.output); output != "" {
					fmt.Fprintln(r.opts.Stdout, output)
				}
				if isMeristemStep(result.stepID) {
					dynamicSteps, parseErr := parseDynamicSteps(result.output)
					if parseErr != nil {
						fmt.Fprintf(r.opts.Stderr, "⚠️ Failed to parse dynamic steps from %s: %v\n", result.stepID, parseErr)
					} else if len(dynamicSteps) > 0 {
						if err := r.appendDynamicSteps(dynamicSteps); err != nil {
							fmt.Fprintf(r.opts.Stderr, "⚠️ Failed to append dynamic steps from %s: %v\n", result.stepID, err)
						}
					}
				}
				fmt.Fprintf(r.opts.Stdout, "✓ [%s] complete\n", result.stepID)
				if err := SaveSequence(r.path, r.seq); err != nil {
					return r.seq, err
				}
				for _, dependentID := range r.dependents[result.stepID] {
					r.remainingDeps[dependentID]--
					if r.remainingDeps[dependentID] <= 0 && r.stepByID[dependentID].Status != sequenceStatusComplete && !r.queued[dependentID] {
						r.ready = append(r.ready, dependentID)
						r.queued[dependentID] = true
					}
				}
				r.sortReady()
				continue
			}

			step.Status = sequenceStatusFailed
			r.publishStepFailure(result.stepID, result.err)

			if errors.Is(result.err, ErrRequiresReview) {
				r.seq.OnFailure = sequenceOnFailurePause
				if err := SaveSequence(r.path, r.seq); err != nil {
					return r.seq, err
				}
				decision, pauseErr := r.handlePause(ctx, result.stepID, result.err)
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
					step.Status = sequenceStatusComplete
					completed++
					if err := SaveSequence(r.path, r.seq); err != nil {
						return r.seq, err
					}
					for _, dependentID := range r.dependents[result.stepID] {
						r.remainingDeps[dependentID]--
						if r.remainingDeps[dependentID] <= 0 && r.stepByID[dependentID].Status != sequenceStatusComplete && !r.queued[dependentID] {
							r.ready = append(r.ready, dependentID)
							r.queued[dependentID] = true
						}
					}
					r.sortReady()
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

			switch strings.ToLower(strings.TrimSpace(r.seq.OnFailure)) {
			case sequenceOnFailureRetry:
				if r.retriesLeft[result.stepID] > 0 {
					r.retriesLeft[result.stepID]--
					step.Status = sequenceStatusPending
					if err := SaveSequence(r.path, r.seq); err != nil {
						return r.seq, err
					}
					r.ready = append(r.ready, result.stepID)
					r.queued[result.stepID] = true
					r.sortReady()
					fmt.Fprintf(r.opts.Stderr, "↺ [%s] retrying, %d retries left\n", result.stepID, r.retriesLeft[result.stepID])
					continue
				}
				return r.seq, fmt.Errorf("step %s failed after %d retries: %w", result.stepID, r.retriesLeft[result.stepID], result.err)

			case sequenceOnFailurePause:
				decision, pauseErr := r.handlePause(ctx, result.stepID, result.err)
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
					step.Status = sequenceStatusComplete
					completed++
					if err := SaveSequence(r.path, r.seq); err != nil {
						return r.seq, err
					}
					for _, dependentID := range r.dependents[result.stepID] {
						r.remainingDeps[dependentID]--
						if r.remainingDeps[dependentID] <= 0 && r.stepByID[dependentID].Status != sequenceStatusComplete && !r.queued[dependentID] {
							r.ready = append(r.ready, dependentID)
							r.queued[dependentID] = true
						}
					}
					r.sortReady()
					continue
				case "halt":
					return r.seq, fmt.Errorf("step %s halted after failure: %w", result.stepID, result.err)
				default:
					return r.seq, fmt.Errorf("step %s returned unknown pause decision %q", result.stepID, decision)
				}

			case sequenceOnFailureHalt:
				return r.seq, fmt.Errorf("step %s failed: %w", result.stepID, result.err)

			default:
				return r.seq, fmt.Errorf("sequence %s has unknown onFailure mode %q", r.seq.Name, r.seq.OnFailure)
			}
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
	// deterministic fuzz-crash failures from runMacrophageFuzzCheck (issue
	//) — both loop back to the same recursive Debugger.
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

func (r *sequenceRunner) handlePause(ctx context.Context, stepID string, stepErr error) (string, error) {
	if r.opts.Interactive {
		fmt.Fprintf(r.opts.Stderr, "⚠️ Step %s failed. [R]etry or [H]alt? ", stepID)
		reader := bufio.NewReader(r.opts.Stdin)
		for {
			line, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", fmt.Errorf("read pause decision: %w", err)
			}
			choice := strings.ToLower(strings.TrimSpace(line))
			switch choice {
			case "", "r", "retry":
				return "retry", nil
			case "h", "halt":
				return "halt", nil
			default:
				fmt.Fprintf(r.opts.Stderr, "Please enter R or H: ")
			}
			if errors.Is(err, io.EOF) {
				return "retry", nil
			}
		}
	}

	fmt.Fprintf(r.opts.Stderr, "⚠️ Step %s failed in headless mode. Edit the sequence to switch onFailure to retry or halt.\n", stepID)
	ticker := time.NewTicker(r.opts.ResumePollInterval)
	defer ticker.Stop()

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
			if step := latestStepByID(latest.Steps, stepID); step != nil {
				switch step.Status {
				case sequenceStatusComplete:
					return "completed", nil
				case sequenceStatusPending:
					return "retry", nil
				}
			}
			if latest.OnFailure != sequenceOnFailurePause {
				r.seq.OnFailure = latest.OnFailure
				return strings.ToLower(strings.TrimSpace(latest.OnFailure)), nil
			}
		}
	}
}

func stepGenotype(stepID string) string {
	trimmed := strings.TrimSpace(stepID)
	normalized := strings.ToLower(trimmed)

	switch {
	case isMeristemStep(trimmed):
		return "meristem"
	case strings.Contains(normalized, "debugger"):
		return "debugger"
	case strings.Contains(normalized, "macrophage"):
		return "macrophage"
	case strings.Contains(normalized, "verifier"):
		return "verifier"
	case strings.Contains(normalized, "thinker"):
		return "thinker"
	default:
		return trimmed
	}
}

func fallbackStepModelTier(stepID string) llm.ModelTier {
	normalized := strings.ToLower(strings.TrimSpace(stepID))
	switch {
	case isMeristemStep(stepID):
		return llm.TierPremium
	case strings.Contains(normalized, "verifier"):
		return llm.TierStandard
	case strings.Contains(normalized, "macrophage"):
		return llm.TierStandard
	case strings.Contains(normalized, "debugger"):
		return llm.TierStandard
	case strings.Contains(normalized, "compiler"):
		return llm.TierStandard
	case strings.Contains(normalized, "compile"):
		return llm.TierStandard
	default:
		return llm.TierPremium
	}
}

type stepLLMSelection struct {
	Tier     llm.ModelTier
	Provider string
	Model    string
	BaseURL  string
}

func resolveStepLLMSelection(ctx context.Context, step *SequenceStep) stepLLMSelection {
	if step == nil {
		return stepLLMSelection{Tier: llm.TierPremium}
	}

	if provider := strings.TrimSpace(step.ModelProvider); provider != "" {
		model := strings.TrimSpace(step.ModelName)
		baseURL := strings.TrimSpace(step.ModelBaseURL)
		if model != "" || baseURL != "" {
			return stepLLMSelection{Provider: provider, Model: model, BaseURL: baseURL, Tier: llm.TierPremium}
		}
	}

	if isMeristemStep(step.ID) {
		return stepLLMSelection{Tier: llm.TierPremium}
	}

	fallbackTier := fallbackStepModelTier(step.ID)
	if fallbackTier != llm.TierPremium {
		return stepLLMSelection{Tier: fallbackTier}
	}

	caps := llm.Capabilities{}
	if step.RequiresReasoning {
		caps.RequiresReasoning = true
	}
	if step.RequiresVision {
		caps.RequiresVision = true
	}

	registry := llm.GetModelRegistry(ctx)
	if selection, err := RouteTask(ctx, step.Transcript, caps, registry); err == nil {
		if strings.TrimSpace(selection.Provider) != "" && strings.TrimSpace(selection.Model) != "" {
			return stepLLMSelection{
				Provider: selection.Provider,
				Model:    selection.Model,
				Tier:     inferSelectionTier(registry, selection),
			}
		}
	}

	assessedTier, err := AssessTaskComplexity(ctx, step.Transcript)
	if err != nil {
		return stepLLMSelection{Tier: llm.TierPremium}
	}
	switch assessedTier {
	case llm.TierPremium, llm.TierStandard, llm.TierCheapest:
		return stepLLMSelection{Tier: assessedTier}
	default:
		return stepLLMSelection{Tier: llm.TierPremium}
	}
}

func inferSelectionTier(registry []llm.ModelDefinition, selection llm.RouteSelection) llm.ModelTier {
	for _, model := range registry {
		if strings.EqualFold(model.Provider, selection.Provider) && model.Name == selection.Model {
			switch model.CostTier {
			case llm.TierCheapest:
				return llm.TierCheapest
			case llm.TierStandard:
				return llm.TierStandard
			default:
				return llm.TierPremium
			}
		}
	}
	return llm.TierPremium
}

func resolveStepModelTier(ctx context.Context, step *SequenceStep) llm.ModelTier {
	return resolveStepLLMSelection(ctx, step).Tier
}

func applyStepLLMSelection(orch *DockerOrchestrator, selection stepLLMSelection) {
	if orch == nil {
		return
	}
	if selection.Provider != "" {
		orch.Provider = selection.Provider
		orch.Model = selection.Model
		orch.BaseURL = selection.BaseURL
	} else if selection.Tier != "" {
		orch.Tier = selection.Tier
	}
}

func newEpigeneticChroniclerForTier(workspace string, tier llm.ModelTier) *EpigeneticChronicler {
	chronicler := NewEpigeneticChronicler(workspace)
	if chronicler == nil {
		return nil
	}
	chronicler.client = llm.NewClientForTier(tier)
	return chronicler
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

	baseIndex := len(r.seq.Steps)
	r.seq.Steps = append(r.seq.Steps, validated...)
	r.rebuildStepIndexes()

	for i := range validated {
		step := &r.seq.Steps[baseIndex+i]
		r.remainingDeps[step.ID] = 0
		if r.seq.OnFailure == sequenceOnFailureRetry {
			retries := r.seq.MaxRetries
			if retries <= 0 {
				retries = defaultSequenceRetryLimit
			}
			r.retriesLeft[step.ID] = retries
		}
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
	FilesModified []string
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
		return runVerifierCommandFn(ctx, resolveTerrariumProviderName(nil), repoRoot(substratePath), step.Command)
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
		Provider:        provider,
		Model:           model,
		BaseURL:         baseURL,
		// The sequence bus, not nil: the agent streams only when it has a bus
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
		DisableMergeBack: true,
	}
	applyStepLLMSelection(orch, resolveStepLLMSelection(ctx, step))

	result, err := runSequenceSproutAtPathFn(ctx, orch, step.Transcript, substratePath, shadowPath)
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
				Temperature:      0.1 + float64(index)*0.3,
				DisableMergeBack: true,
			}
			applyStepLLMSelection(orch, llmSelection)

			runResult, runErr := runSequenceSproutAtPathFn(selectionCtx, orch, step.Transcript, sourcePath, shadowPath)
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
	defer func() {
		outcome := executionOutcome
		// A failure anywhere (including commit or merge-back after a clean
		// agent turn) must reclassify: the run's provisional verdict cannot
		// stand once its results failed to land.
		if err != nil || outcome == "" {
			outcome = classifySproutOutcome(err, executionFiles, false, response)
		}
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		publishSproutTerminal(orch.EventBus, stepID, orch.SessionID, outcome, executionFiles, reason)
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
		shadowPath, err := createShadowWorktree(sourcePath, orch.SubstrateBranch)
		if err == nil {
			mountPath = shadowPath
			injectMycorrhizalCache(sourcePath, shadowPath)
			cleanup = func() {
				removeShadowWorktree(sourcePath, shadowPath)
			}
		} else {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to create shadow worktree: %v. Using active workspace.\n", err)
		}
	}

	if cleanup != nil {
		defer cleanup()
	}

	executionResult, err := runSequenceSproutAtPathFn(ctx, orch, taskPrompt, sourcePath, mountPath)
	executionOutcome = executionResult.Outcome
	executionFiles = executionResult.FilesModified
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
	hostStashed := false
	if gitRepo && !orch.DisableMergeBack {
		hostStashed, err = stashHostWorkspaceFn(ctx, sourcePath, stepID)
		if err != nil {
			return result, err
		}
		defer func() {
			if !hostStashed {
				return
			}
			if restoreErr := restoreHostStashFn(cleanupCtx, sourcePath); restoreErr != nil {
				err = errors.Join(err, restoreErr)
			}
		}()
	}

	if orch.Genotype != "" {
		if err := stagePlasmidsForGenotype(sourcePath, mountPath, orch.Genotype); err != nil {
			return result, err
		}
	}

	// Note: even for a "macrophage" step, the agent's own session below still
	// uses the ordinary per-language image (opentendril-go:latest for a Go
	// workspace) to write the fuzz test file via the normal tool-call
	// protocol. The deterministic fuzz-*execution* half after the agent turn
	// runs in a separate, Go-toolchain-enabled terrarium (macrophageFuzzImage,
	// sprouts/go-fuzz/Dockerfile) — see runMacrophageFuzzCheck below.
	imageName := orch.resolveImageName(mountPath)
	result.ImageName = imageName
	if err := ensureSproutImage(ctx, imageName); err != nil {
		return result, err
	}

	substratesConfig, _ := LoadSubstratesConfig("")
	sequencePlan, planErr := resolveSubstrateExecutionPlan(orch, substratesConfig)

	providerName := resolveTerrariumProviderName(orch)
	if planErr == nil && sequencePlan != nil && sequencePlan.provider != "" {
		providerName = sequencePlan.provider
	}

	var command []string
	if sequencePlan != nil {
		command = sequencePlan.command
	}

	session, err := startTerrariumSessionFn(ctx, providerName, imageName, mountPath, command)
	if err != nil {
		return result, err
	}
	defer session.Close()

	// The orchestrator's bus, not nil: the agent streams only when it has one
	// to publish to, so passing nil made every sequence sprout step — a
	// delegated Codex run among them — silent for its whole duration, leaving a
	// wall clock as the only way to judge it.
	agent, err := newAgentFn(ctx, mountPath, sourcePath, orch.Genotype, orch.resolveLLMClient(), session, orch.EventBus, orch.StepID, orch.SessionID)
	if err != nil {
		return result, err
	}

	agentResult, runErr := agent.Run(ctx, taskPrompt)
	if err := session.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Sprout session shutdown issue: %v\n", err)
	}

	result.Response = agentResult.Response
	if agentResult.ActionResult != nil {
		verdict := strings.ToUpper(strings.TrimSpace(agentResult.ActionResult.Verdict))
		switch verdict {
		case "DANGEROUS":
			if quarantineErr := quarantineScriptPrompt(stepID, taskPrompt); quarantineErr != nil {
				return result, errors.Join(fmt.Errorf("script quarantined: %v", agentResult.ActionResult.Risks), quarantineErr)
			}
			return result, fmt.Errorf("script quarantined: %v", agentResult.ActionResult.Risks)
		case "REVIEW":
			return result, ErrRequiresReview
		case "SAFE":
		case "":
		default:
			return result, fmt.Errorf("unknown script review verdict %q", agentResult.ActionResult.Verdict)
		}
	}

	// Symbiotic Immune System: once the Macrophage's agent turn
	// has written its fuzz test, deterministically run it — no LLM judgment
	// call — and treat a crash exactly like a Verifier compiler/test failure,
	// so shouldBudRecursiveDebugger sprouts a Debugger to fix it and retries.
	// Skipped if the agent turn itself already failed; nothing to fuzz.
	if runErr == nil && orch.Genotype == "macrophage" {
		if fuzzErr := runMacrophageFuzzCheckFn(ctx, providerName, mountPath); fuzzErr != nil {
			runErr = fuzzErr
		}
	}

	if !gitRepo {
		if runErr != nil {
			return result, runErr
		}
		return result, nil
	}

	modifiedFiles, diffErr := collectStageableFilesFn(ctx, mountPath, "tendril-status.json")
	if diffErr != nil {
		return result, diffErr
	}

	var gitDiff string
	if !orch.DisableMergeBack {
		gitDiff, diffErr = collectGitDiffFn(ctx, mountPath)
		if diffErr != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to collect git diff for epigenetic chronicler: %v\n", diffErr)
		}
	}

	executionStatus := sproutExecutionStatus{
		StepID:        stepID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		FilesModified: modifiedFiles,
		Status:        classifySproutOutcome(runErr, modifiedFiles, true, agentResult.Response),
	}
	if runErr != nil {
		executionStatus.Error = runErr.Error()
	}
	result.Outcome = executionStatus.Status
	result.FilesModified = modifiedFiles

	var sequenceCredential ResolvedCredential
	if sequencePlan != nil {
		sequenceCredential = sequencePlan.credential
	}
	commitHash, commitErr := commitTerrariumExecutionFn(ctx, mountPath, sourcePath, "", executionStatus, taskPrompt, sequenceCredential)
	if commitErr != nil {
		if runErr != nil {
			return result, errors.Join(runErr, commitErr)
		}
		return result, commitErr
	}

	result.CommitHash = commitHash

	if orch.DisableMergeBack {
		return result, runErr
	}

	mergeErr := mergeSequenceTerrariumCommit(ctx, sourcePath, commitHash)
	if mergeErr != nil {
		if runErr != nil {
			return result, errors.Join(runErr, mergeErr)
		}
		return result, mergeErr
	}

	if gitDiff != "" && runErr == nil {
		chronicler := newEpigeneticChroniclerForTier(sourcePath, llm.TierCheapest)
		if err := chronicler.TranscribeLearnings(ctx, agentResult.Transcript, gitDiff, session.Logs()); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Epigenetic chronicler skipped: %v\n", err)
		}
	}

	if fitErr := RecordGenomicFitness(sourcePath, runErr == nil); fitErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Genome fitness record skipped: %v\n", fitErr)
	}

	if runErr != nil {
		return result, runErr
	}

	return result, nil
}

func quarantineScriptPrompt(stepID, taskPrompt string) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve user config dir: %w", err)
	}

	quarantineDir := filepath.Join(configDir, "opentendril", "quarantine")
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
	if _, err := runGitCommand(ctx, sourcePath, "merge", "--no-ff", "--no-edit", commitHash); err != nil {
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

	if _, err = runGitCommand(cleanupCtx, sourcePath, "merge", "--ff-only", branchName); err != nil {
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
	if _, err = runGitCommand(cleanupCtx, sourcePath, "merge", "--no-ff", "-m", mergeMessage, branchName); err != nil {
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

func resolveSequenceSubstrate(root, substrate string) string {
	trimmed := strings.TrimSpace(substrate)
	if trimmed == "" {
		return root
	}

	if config, err := LoadSubstratesConfig(""); err == nil {
		if spec, isName := ResolveSubstrate(trimmed, config); isName && spec != nil {
			return trimmed
		}
	}

	if filepath.IsAbs(trimmed) {
		return repoRoot(trimmed)
	}

	base := filepath.Base(root)
	if trimmed == base {
		return root
	}

	candidates := []string{
		filepath.Join(root, trimmed),
		filepath.Join(filepath.Dir(root), trimmed),
		filepath.Join(".", trimmed),
	}
	for _, candidate := range candidates {
		if isGitRepo(candidate) {
			return repoRoot(candidate)
		}
	}

	return repoRoot(filepath.Join(root, trimmed))
}

func sequencePathCandidates(input, cwd, root string) []string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}

	var candidates []string
	seen := make(map[string]struct{})
	add := func(path string) {
		if path == "" {
			return
		}
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}

	ext := strings.ToLower(filepath.Ext(trimmed))
	baseNoExt := strings.TrimSuffix(trimmed, filepath.Ext(trimmed))
	hasExt := ext == ".yaml" || ext == ".yml"

	if filepath.IsAbs(trimmed) {
		add(trimmed)
		if !hasExt {
			add(trimmed + ".yaml")
			add(trimmed + ".yml")
		}
		return candidates
	}

	add(trimmed)
	add(filepath.Join(cwd, trimmed))
	add(filepath.Join(root, trimmed))

	// System sequence directories take priority before workspace sequences
	if configDir, err := os.UserConfigDir(); err == nil {
		sysUserDir := filepath.Join(configDir, "opentendril", "sequences")
		if !strings.Contains(trimmed, string(filepath.Separator)) {
			add(filepath.Join(sysUserDir, trimmed))
		}
		if !hasExt {
			add(filepath.Join(sysUserDir, baseNoExt+".yaml"))
			add(filepath.Join(sysUserDir, baseNoExt+".yml"))
		}
	}

	sysEtcDir := filepath.Join("/etc", "opentendril", "sequences")
	if !strings.Contains(trimmed, string(filepath.Separator)) {
		add(filepath.Join(sysEtcDir, trimmed))
	}
	if !hasExt {
		add(filepath.Join(sysEtcDir, baseNoExt+".yaml"))
		add(filepath.Join(sysEtcDir, baseNoExt+".yml"))
	}

	if !strings.Contains(trimmed, string(filepath.Separator)) {
		add(filepath.Join(root, ".tendril", "sequences", trimmed))
	}

	if !hasExt {
		add(trimmed + ".yaml")
		add(trimmed + ".yml")
		add(filepath.Join(cwd, trimmed+".yaml"))
		add(filepath.Join(cwd, trimmed+".yml"))
		add(filepath.Join(root, trimmed+".yaml"))
		add(filepath.Join(root, trimmed+".yml"))
		add(filepath.Join(root, ".tendril", "sequences", baseNoExt+".yaml"))
		add(filepath.Join(root, ".tendril", "sequences", baseNoExt+".yml"))
	}

	return candidates
}
