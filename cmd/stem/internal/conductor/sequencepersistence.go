package conductor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	Investigation     bool     `yaml:"investigation,omitempty"`
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

// LoadSequence reads a sequence definition from YAML.
func LoadSequence(path string) (*Sequence, error) {
	if err := validateSequenceFilePath(path); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open sequence root %s: %w", filepath.Dir(path), err)
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("open sequence %s: %w", path, err)
	}
	content, err := io.ReadAll(file)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
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
	if err := validateSequenceFilePath(path); err != nil {
		return err
	}
	if err := normalizeSequence(path, seq); err != nil {
		return err
	}

	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open sequence root %s: %w", filepath.Dir(path), err)
	}
	defer root.Close()
	baseName := filepath.Base(path)
	tmpName := fmt.Sprintf(".%s.%d.tmp", baseName, time.Now().UnixNano())
	tmpFile, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temp sequence file: %w", err)
	}

	enc := yaml.NewEncoder(tmpFile)
	enc.SetIndent(2)
	if err := enc.Encode(seq); err != nil {
		_ = enc.Close()
		_ = tmpFile.Close()
		_ = root.Remove(tmpName)
		return fmt.Errorf("encode sequence %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		_ = tmpFile.Close()
		_ = root.Remove(tmpName)
		return fmt.Errorf("finalize sequence %s: %w", path, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = root.Remove(tmpName)
		return fmt.Errorf("close sequence %s: %w", path, err)
	}
	if err := root.Rename(tmpName, baseName); err != nil {
		_ = root.Remove(tmpName)
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
	if !filepath.IsAbs(trimmed) && !filepath.IsLocal(trimmed) {
		return "", fmt.Errorf("sequence path must not escape its workspace")
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	root := repoRoot(cwd)

	candidates := sequencePathCandidates(trimmed, cwd, root)
	for _, candidate := range candidates {
		root, rootErr := os.OpenRoot(filepath.Dir(candidate))
		if rootErr != nil {
			continue
		}
		info, err := root.Stat(filepath.Base(candidate))
		root.Close()
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

// validateSequenceFilePath rejects relative traversal while preserving the
// CLI's supported absolute paths for explicitly selected local sequence files.
func validateSequenceFilePath(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("sequence path is required")
	}
	if !filepath.IsAbs(trimmed) && !filepath.IsLocal(trimmed) {
		return fmt.Errorf("sequence path must not escape its workspace")
	}
	return nil
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

	searchDirs, _ := DefinitionSearchPath(root, DefinitionKindSequences)

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

	// Trusted sequence directories take priority over workspace sequences.
	for _, dir := range TrustedDefinitionDirs(root, DefinitionKindSequences) {
		if !strings.Contains(trimmed, string(filepath.Separator)) {
			add(filepath.Join(dir, trimmed))
		}
		if !hasExt {
			add(filepath.Join(dir, baseNoExt+".yaml"))
			add(filepath.Join(dir, baseNoExt+".yml"))
		}
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
