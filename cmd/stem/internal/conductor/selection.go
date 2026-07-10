package conductor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

// Phenotypic Selection: a generational genetic algorithm over LLM-authored code.
//
// Taxonomy (see GLOSSARY.md):
//   - Genotype       — the mutation directive + transcript handed to a sprout.
//   - Phenotype      — the compiled/running code a sprout grows on its branch.
//   - Mutation       — one variation, driven by a directive and a temperature.
//   - FitnessScore   — a numeric metric parsed from the fitness test output.
//   - AlphaPhenotype — the single fittest Phenotype found across all generations.
//
// Each generation grows a population of Phenotypes in isolated shadow worktrees
// (reusing the parallelsprouting/phenotypic helpers), scores them against the
// fitness test, breeds the fittest survivors into the next generation's
// Genotypes (FunSearch/ELM-style: parent diffs are sampled back into the
// prompt), and finally grafts the AlphaPhenotype branch back to the host.

const (
	minSelectionPopulation = 2
	// defaultSelectionPopulation balances the marginal probability of finding a
	// fitter mutation against per-generation API cost and local Docker load.
	defaultSelectionPopulation = 6
	maxSelectionPopulation     = 12

	defaultSelectionGenerations = 3
	maxSelectionGenerations     = 10

	defaultSelectionSurvivorFraction    = 0.5
	defaultSelectionMutationTemperature = 0.4
	defaultSelectionTemperatureSpread   = 0.3
	maxSelectionTemperature             = 1.0

	selectionGoalMinimize = "minimize"
	selectionGoalMaximize = "maximize"

	// selectionParentExcerptLimit caps how much of each parent diff is sampled
	// back into an offspring Genotype to keep prompts within budget.
	selectionParentExcerptLimit = 4000
)

var (
	runGeneticSelectionFn      = runGeneticSelection
	runContainerFitnessScoreFn = runContainerFitnessScore
)

// diversityDirectives seed generation zero with structurally distinct search
// directions so the initial population explores the space instead of clustering
// on a single micro-optimization.
var diversityDirectives = []string{
	"Pursue an algorithmically distinct approach: change the core algorithm or data structure rather than micro-optimizing the existing one.",
	"Aggressively optimize the hot path: eliminate allocations, redundant bounds checks, and interface indirection while preserving behavior.",
	"Exploit concurrency or parallelism where the workload allows it, without changing observable results.",
	"Optimize for cache locality: favor contiguous memory layouts and reduce pointer chasing.",
	"Simplify and inline: strip abstraction layers and virtual dispatch that cost cycles on the measured path.",
	"Trade memory for speed: precompute, memoize, or add lookup tables where it lowers the fitness metric.",
}

// phenotypeCandidate is one grown Phenotype and its evaluated fitness.
type phenotypeCandidate struct {
	generation int
	index      int
	branchName string
	directive  string
	response   string
	diff       string
	score      float64
	scored     bool
	err        error
}

// selectionGenotype is one Genotype (mutation) queued for a generation.
type selectionGenotype struct {
	directive   string
	transcript  string
	temperature float64
}

// runGeneticSelection executes the full generational loop for one selection
// step and returns a human-readable AlphaPhenotype report.
func runGeneticSelection(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string, bus *eventbus.Bus) (result string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if seq == nil || step == nil || step.Selection == nil {
		return "", fmt.Errorf("phenotypic selection requires a sequence, step, and selection config")
	}
	cfg := step.Selection

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
	if strings.TrimSpace(cfg.FitnessTest) == "" {
		return "", fmt.Errorf("phenotypic selection for step %s requires a fitnessTest", step.ID)
	}

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

	baseSHA, err := runGitCommand(ctx, sourcePath, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("phenotypic selection could not resolve base commit: %w", err)
	}

	branchBase := derivedSequenceBranch(seq.Branch, step.ID)
	if branchBase == "" {
		branchBase = sanitizeBranchComponent(step.ID)
		if branchBase == "" {
			branchBase = "phenotype"
		}
	}

	llmSelection := resolveStepLLMSelection(ctx, step)

	publishSelectionEvent(bus, seq, step, map[string]interface{}{
		"phase":          "start",
		"populationSize": cfg.PopulationSize,
		"maxGenerations": cfg.MaxGenerations,
		"fitnessGoal":    cfg.FitnessGoal,
	})

	var alpha *phenotypeCandidate
	var parents []phenotypeCandidate
	var allBranches []string

	for generation := 0; generation < cfg.MaxGenerations; generation++ {
		genotypes := buildGenerationGenotypes(step, cfg, generation, parents)

		publishSelectionEvent(bus, seq, step, map[string]interface{}{
			"phase":      "generation",
			"generation": generation,
			"population": len(genotypes),
		})

		candidates := growPopulation(ctx, step, cfg, sourcePath, branchBase, baseSHA, generation, genotypes, llmSelection)
		for i := range candidates {
			allBranches = append(allBranches, candidates[i].branchName)
		}

		scored := make([]phenotypeCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.scored {
				scored = append(scored, candidate)
			}
		}

		sort.SliceStable(scored, func(i, j int) bool {
			return fitterScore(scored[i].score, scored[j].score, cfg.FitnessGoal)
		})

		if len(scored) > 0 {
			best := scored[0]
			if alpha == nil || fitterScore(best.score, alpha.score, cfg.FitnessGoal) {
				winner := best
				alpha = &winner
			}
			publishSelectionEvent(bus, seq, step, map[string]interface{}{
				"phase":       "evaluated",
				"generation":  generation,
				"survivors":   len(scored),
				"withered":    len(candidates) - len(scored),
				"bestScore":   best.score,
				"alphaScore":  alpha.score,
				"alphaBranch": alpha.branchName,
			})
		} else {
			publishSelectionEvent(bus, seq, step, map[string]interface{}{
				"phase":      "evaluated",
				"generation": generation,
				"survivors":  0,
				"withered":   len(candidates),
			})
		}

		if alpha != nil && thresholdReached(cfg, alpha.score) {
			break
		}

		parents = topSurvivors(scored, cfg)
	}

	if alpha == nil {
		bestEffortDeleteBranches(cleanupCtx, sourcePath, allBranches, "")
		return "", fmt.Errorf("phenotypic selection for step %s produced no viable phenotype across %d generation(s)", step.ID, cfg.MaxGenerations)
	}

	mergeCtx := context.WithoutCancel(ctx)
	if mergeErr := mergePhenotypeBranchToHostFn(mergeCtx, sourcePath, alpha.branchName); mergeErr != nil {
		return "", fmt.Errorf("phenotypic selection could not graft AlphaPhenotype %s: %w", alpha.branchName, mergeErr)
	}

	bestEffortDeleteBranches(mergeCtx, sourcePath, allBranches, alpha.branchName)

	publishSelectionEvent(bus, seq, step, map[string]interface{}{
		"phase":       "complete",
		"alphaBranch": alpha.branchName,
		"alphaScore":  alpha.score,
	})

	return formatAlphaReport(step, cfg, alpha), nil
}

// growPopulation grows every Genotype of one generation in parallel, each in an
// isolated shadow worktree branched from the substrate HEAD. Every goroutine
// always returns a candidate; a sprout crash, timeout, or fitness failure is
// recorded as an unscored (withered) candidate and never halts the generation.
func growPopulation(ctx context.Context, step *SequenceStep, cfg *SelectionConfig, sourcePath, branchBase, baseSHA string, generation int, genotypes []selectionGenotype, llmSelection stepLLMSelection) []phenotypeCandidate {
	resultsCh := make(chan phenotypeCandidate, len(genotypes))
	var wg sync.WaitGroup

	for i := range genotypes {
		index := i
		genotype := genotypes[i]
		branchName := fmt.Sprintf("%s-gen%d-pheno%d", branchBase, generation, index)
		wg.Add(1)
		go func() {
			defer wg.Done()
			resultsCh <- growPhenotype(ctx, step, cfg, sourcePath, branchName, baseSHA, generation, index, genotype, llmSelection)
		}()
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	candidates := make([]phenotypeCandidate, 0, len(genotypes))
	for candidate := range resultsCh {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].index < candidates[j].index })
	return candidates
}

// growPhenotype grows a single Phenotype and evaluates its FitnessScore. It
// recovers panics from a crashed terrarium so one failure can never bring down
// the generation.
func growPhenotype(ctx context.Context, step *SequenceStep, cfg *SelectionConfig, sourcePath, branchName, baseSHA string, generation, index int, genotype selectionGenotype, llmSelection stepLLMSelection) (candidate phenotypeCandidate) {
	candidate = phenotypeCandidate{
		generation: generation,
		index:      index,
		branchName: branchName,
		directive:  genotype.directive,
	}

	defer func() {
		if r := recover(); r != nil {
			candidate.scored = false
			candidate.err = fmt.Errorf("phenotype %d withered from panic: %v", index, r)
		}
	}()

	shadowPath, err := createShadowWorktreeFn(sourcePath, branchName)
	if err != nil {
		candidate.err = fmt.Errorf("create phenotype worktree %s: %w", branchName, err)
		return candidate
	}
	injectMycorrhizalCacheFn(sourcePath, shadowPath)
	defer removeShadowWorktreeFn(sourcePath, shadowPath)

	genotypeName := stepGenotype(step.ID)
	if isMeristemStep(step.ID) {
		genotypeName = "meristem"
	}

	orch := &DockerOrchestrator{
		Substrate:        sourcePath,
		SubstrateBranch:  branchName,
		StepID:           step.ID,
		IsCoordinator:    isMeristemStep(step.ID),
		Genotype:         genotypeName,
		Temperature:      genotype.temperature,
		DisableMergeBack: true,
	}
	applyStepLLMSelection(orch, llmSelection)

	runResult, runErr := runSequenceSproutAtPathFn(ctx, orch, genotype.transcript, sourcePath, shadowPath)
	if runErr != nil {
		candidate.err = fmt.Errorf("phenotype %d (%s) sprout failed: %w", index, branchName, runErr)
		return candidate
	}
	candidate.response = runResult.Response

	if diff, diffErr := capturePhenotypeDiff(ctx, shadowPath, baseSHA); diffErr == nil {
		candidate.diff = diff
	}

	score, output, fitErr := runContainerFitnessScoreFn(ctx, runResult.ImageName, shadowPath, cfg.FitnessTest, cfg.FitnessPattern)
	if fitErr != nil {
		candidate.err = fmt.Errorf("phenotype %d (%s) fitness evaluation failed: %w (output: %s)", index, branchName, fitErr, excerptForMerge(output))
		return candidate
	}

	candidate.score = score
	candidate.scored = true
	return candidate
}

// buildGenerationGenotypes produces the Genotypes for one generation. Generation
// zero spreads the diversity directives across the population; later generations
// breed offspring by sampling the fittest parents' diffs back into the prompt.
func buildGenerationGenotypes(step *SequenceStep, cfg *SelectionConfig, generation int, parents []phenotypeCandidate) []selectionGenotype {
	genotypes := make([]selectionGenotype, 0, cfg.PopulationSize)
	for index := 0; index < cfg.PopulationSize; index++ {
		directive := diversityDirectives[index%len(diversityDirectives)]
		var transcript string
		if generation == 0 || len(parents) == 0 {
			transcript = seedTranscript(step.Transcript, cfg, directive)
		} else {
			parent := parents[index%len(parents)]
			transcript = offspringTranscript(step.Transcript, cfg, directive, parent)
		}
		genotypes = append(genotypes, selectionGenotype{
			directive:   directive,
			transcript:  transcript,
			temperature: populationTemperature(cfg, index),
		})
	}
	return genotypes
}

func seedTranscript(baseTranscript string, cfg *SelectionConfig, directive string) string {
	var b strings.Builder
	b.WriteString(baseTranscript)
	b.WriteString("\n\n--- Phenotypic Selection: Mutation Directive ---\n")
	b.WriteString(directive)
	b.WriteString("\nOptimize for the fitness metric measured by `")
	b.WriteString(cfg.FitnessTest)
	b.WriteString("` (goal: ")
	b.WriteString(cfg.FitnessGoal)
	b.WriteString(" the score). Preserve all existing behavior and keep the code compiling and its tests passing.")
	return b.String()
}

func offspringTranscript(baseTranscript string, cfg *SelectionConfig, directive string, parent phenotypeCandidate) string {
	var b strings.Builder
	b.WriteString(baseTranscript)
	b.WriteString("\n\n--- Phenotypic Selection: Offspring Breeding ---\n")
	fmt.Fprintf(&b, "A parent phenotype already reached a fitness score of %s (goal: %s the score).\n", formatScore(parent.score), cfg.FitnessGoal)
	if strings.TrimSpace(parent.diff) != "" {
		b.WriteString("Its winning change (parent genes) was:\n```diff\n")
		b.WriteString(excerptForSelection(parent.diff))
		b.WriteString("\n```\n")
	}
	b.WriteString("Breed a superior offspring: build on the parent's approach, then apply this mutation to push the score further.\nMutation directive: ")
	b.WriteString(directive)
	b.WriteString("\nKeep the code compiling and its tests passing.")
	return b.String()
}

// populationTemperature spreads sampling temperature across the population so
// low-index phenotypes exploit (near MutationTemperature) and high-index
// phenotypes explore (up to MutationTemperature + TemperatureSpread).
func populationTemperature(cfg *SelectionConfig, index int) float64 {
	if cfg.PopulationSize <= 1 {
		return clampTemperature(cfg.MutationTemperature)
	}
	step := cfg.TemperatureSpread / float64(cfg.PopulationSize-1)
	return clampTemperature(cfg.MutationTemperature + step*float64(index))
}

func clampTemperature(t float64) float64 {
	if t < 0 {
		return 0
	}
	if t > maxSelectionTemperature {
		return maxSelectionTemperature
	}
	return t
}

// topSurvivors returns the fittest SurvivorFraction of a ranked, scored
// generation (at least one when any survived) as breeding parents.
func topSurvivors(scored []phenotypeCandidate, cfg *SelectionConfig) []phenotypeCandidate {
	if len(scored) == 0 {
		return nil
	}
	count := int(float64(len(scored)) * cfg.SurvivorFraction)
	if count < 1 {
		count = 1
	}
	if count > len(scored) {
		count = len(scored)
	}
	survivors := make([]phenotypeCandidate, count)
	copy(survivors, scored[:count])
	return survivors
}

// fitterScore reports whether a is a better FitnessScore than b under the goal.
func fitterScore(a, b float64, goal string) bool {
	if goal == selectionGoalMaximize {
		return a > b
	}
	return a < b
}

func thresholdReached(cfg *SelectionConfig, score float64) bool {
	if cfg.FitnessThreshold == nil {
		return false
	}
	if cfg.FitnessGoal == selectionGoalMaximize {
		return score >= *cfg.FitnessThreshold
	}
	return score <= *cfg.FitnessThreshold
}

// capturePhenotypeDiff returns the phenotype's change as a diff against the
// substrate base commit, so it can be sampled into offspring Genotypes.
func capturePhenotypeDiff(ctx context.Context, shadowPath, baseSHA string) (string, error) {
	return runGitCommand(ctx, shadowPath, "diff", "--no-color", baseSHA, "HEAD")
}

// bestEffortDeleteBranches prunes losing phenotype branches after selection.
// It never returns an error: leftover refs are cosmetic, not correctness.
func bestEffortDeleteBranches(ctx context.Context, sourcePath string, branches []string, keep string) {
	seen := make(map[string]struct{}, len(branches))
	for _, branch := range branches {
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == keep {
			continue
		}
		if _, ok := seen[branch]; ok {
			continue
		}
		seen[branch] = struct{}{}
		_, _ = runGitCommand(ctx, sourcePath, "branch", "-D", branch)
	}
}

func formatAlphaReport(step *SequenceStep, cfg *SelectionConfig, alpha *phenotypeCandidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🏆 AlphaPhenotype for step %s selected from generation %d, phenotype %d.\n", step.ID, alpha.generation, alpha.index)
	fmt.Fprintf(&b, "FitnessScore: %s (goal: %s) via `%s`.\n", formatScore(alpha.score), cfg.FitnessGoal, cfg.FitnessTest)
	fmt.Fprintf(&b, "Mutation directive: %s\n", alpha.directive)
	fmt.Fprintf(&b, "Grafted branch %s back into the substrate.", alpha.branchName)
	if strings.TrimSpace(alpha.response) != "" {
		fmt.Fprintf(&b, "\n\nPhenotype summary:\n%s", excerptForSelection(alpha.response))
	}
	return b.String()
}

// runContainerFitnessScore runs the fitness test inside the phenotype's
// terrarium and parses a numeric FitnessScore from its combined output. A
// non-zero exit (failed compile, benchmark, or test) is treated as unfit.
func runContainerFitnessScore(ctx context.Context, imageName, shadowPath, fitnessTest, pattern string) (float64, string, error) {
	if strings.TrimSpace(fitnessTest) == "" {
		return 0, "", fmt.Errorf("fitness test command is empty")
	}
	if strings.TrimSpace(imageName) == "" {
		return 0, "", fmt.Errorf("fitness test image name is empty")
	}
	if strings.TrimSpace(shadowPath) == "" {
		return 0, "", fmt.Errorf("fitness test shadow path is empty")
	}

	args := []string{
		"run", "--rm",
		"-v", fmt.Sprintf("%s:/app", shadowPath),
		"-w", "/app",
		imageName,
		"sh", "-c", fitnessTest,
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return 0, text, fmt.Errorf("docker fitness test failed: %w", err)
	}

	score, parseErr := parseFitnessScore(text, pattern)
	if parseErr != nil {
		return 0, text, parseErr
	}
	return score, text, nil
}

var (
	fitnessNsPerOpPattern = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s+ns/op`)
	fitnessNumberPattern  = regexp.MustCompile(`-?[0-9]+(?:\.[0-9]+)?`)
)

// parseFitnessScore extracts the numeric FitnessScore from fitness test output.
// A caller-supplied pattern wins (first capture group, else whole match);
// otherwise the parser prefers a Go benchmark "<n> ns/op" line and falls back
// to the last number in the output.
func parseFitnessScore(output, pattern string) (float64, error) {
	if strings.TrimSpace(pattern) != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return 0, fmt.Errorf("invalid fitnessPattern %q: %w", pattern, err)
		}
		match := re.FindStringSubmatch(output)
		if match == nil {
			return 0, fmt.Errorf("fitnessPattern %q found no score in output", pattern)
		}
		token := match[0]
		if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			token = match[1]
		}
		return strconv.ParseFloat(strings.TrimSpace(token), 64)
	}

	if match := fitnessNsPerOpPattern.FindStringSubmatch(output); match != nil {
		return strconv.ParseFloat(match[1], 64)
	}

	numbers := fitnessNumberPattern.FindAllString(output, -1)
	if len(numbers) == 0 {
		return 0, fmt.Errorf("no numeric fitness score found in output")
	}
	return strconv.ParseFloat(numbers[len(numbers)-1], 64)
}

func formatScore(score float64) string {
	return strconv.FormatFloat(score, 'f', -1, 64)
}

func excerptForSelection(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(empty)"
	}
	if len(value) <= selectionParentExcerptLimit {
		return value
	}
	return value[:selectionParentExcerptLimit] + "\n[... truncated ...]"
}

func publishSelectionEvent(bus *eventbus.Bus, seq *Sequence, step *SequenceStep, data map[string]interface{}) {
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
		Type:   eventbus.EventPhenotypicSelection,
		Source: "phenotypic-selection",
		Data:   data,
	})
}
