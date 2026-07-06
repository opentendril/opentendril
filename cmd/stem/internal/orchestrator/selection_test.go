package orchestrator

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestParseFitnessScore(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		pattern string
		want    float64
		wantErr bool
	}{
		{
			name:   "go benchmark ns/op",
			output: "BenchmarkFib-8   \t  123456\t        842.5 ns/op\t     0 B/op",
			want:   842.5,
		},
		{
			name:    "custom capture group",
			output:  "score=17.25 total",
			pattern: `score=([0-9.]+)`,
			want:    17.25,
		},
		{
			name:   "fallback to last number",
			output: "iterations 10 result 99",
			want:   99,
		},
		{
			name:    "pattern with no match",
			output:  "nothing here",
			pattern: `score=([0-9.]+)`,
			wantErr: true,
		},
		{
			name:    "no number at all",
			output:  "no digits present",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFitnessScore(tt.output, tt.pattern)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseFitnessScore(%q, %q) = %v, want error", tt.output, tt.pattern, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFitnessScore(%q, %q) unexpected error: %v", tt.output, tt.pattern, err)
			}
			if got != tt.want {
				t.Fatalf("parseFitnessScore(%q, %q) = %v, want %v", tt.output, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestPopulationTemperatureSpread(t *testing.T) {
	cfg := &SelectionConfig{
		PopulationSize:      4,
		MutationTemperature: 0.2,
		TemperatureSpread:   0.6,
	}
	got := []float64{
		populationTemperature(cfg, 0),
		populationTemperature(cfg, 1),
		populationTemperature(cfg, 2),
		populationTemperature(cfg, 3),
	}
	want := []float64{0.2, 0.4, 0.6, 0.8}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("populationTemperature index %d = %v, want %v", i, got[i], want[i])
		}
	}

	// Temperatures never exceed the clamp ceiling.
	hot := &SelectionConfig{PopulationSize: 2, MutationTemperature: 0.9, TemperatureSpread: 0.9}
	if temp := populationTemperature(hot, 1); temp > maxSelectionTemperature {
		t.Fatalf("populationTemperature exceeded clamp: %v", temp)
	}
}

func TestNormalizeSelectionConfigDefaultsAndValidation(t *testing.T) {
	seq := &Sequence{
		Name: "evolve",
		Steps: []SequenceStep{
			{
				ID:         "optimize",
				Transcript: "make it fast",
				Selection: &SelectionConfig{
					FitnessTest: "make benchmark",
				},
			},
		},
	}
	if err := normalizeSequence("evolve.yaml", seq); err != nil {
		t.Fatalf("normalizeSequence returned error: %v", err)
	}
	cfg := seq.Steps[0].Selection
	if cfg.PopulationSize != defaultSelectionPopulation {
		t.Fatalf("PopulationSize = %d, want %d", cfg.PopulationSize, defaultSelectionPopulation)
	}
	if cfg.MaxGenerations != defaultSelectionGenerations {
		t.Fatalf("MaxGenerations = %d, want %d", cfg.MaxGenerations, defaultSelectionGenerations)
	}
	if cfg.FitnessGoal != selectionGoalMinimize {
		t.Fatalf("FitnessGoal = %q, want %q", cfg.FitnessGoal, selectionGoalMinimize)
	}

	// Selection without any fitness test is rejected.
	bad := &Sequence{
		Name: "evolve",
		Steps: []SequenceStep{
			{ID: "optimize", Transcript: "x", Selection: &SelectionConfig{}},
		},
	}
	if err := normalizeSequence("evolve.yaml", bad); err == nil {
		t.Fatalf("normalizeSequence accepted selection without fitnessTest")
	}
}

func TestTopSurvivorsRanking(t *testing.T) {
	cfg := &SelectionConfig{SurvivorFraction: 0.5}
	scored := []phenotypeCandidate{
		{index: 0, score: 100, scored: true},
		{index: 1, score: 50, scored: true},
		{index: 2, score: 75, scored: true},
		{index: 3, score: 25, scored: true},
	}
	// Pre-sort as the engine does (minimize: lowest first).
	// scored slice here is already the ranked order the caller passes in.
	ranked := []phenotypeCandidate{scored[3], scored[1], scored[2], scored[0]}
	survivors := topSurvivors(ranked, cfg)
	if len(survivors) != 2 {
		t.Fatalf("topSurvivors returned %d survivors, want 2", len(survivors))
	}
	if survivors[0].score != 25 || survivors[1].score != 50 {
		t.Fatalf("survivors = %v, want scores [25 50]", []float64{survivors[0].score, survivors[1].score})
	}
}

func TestRunGeneticSelectionEvolvesAndGraftsAlpha(t *testing.T) {
	repo := initPhenotypeTestRepo(t)

	originalSprout := runSequenceSproutAtPathFn
	originalMerge := mergePhenotypeBranchToHostFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalInject := injectMycorrhizalCacheFn
	originalStash := stashHostWorkspaceFn
	originalRestore := restoreHostStashFn
	originalFitness := runContainerFitnessScoreFn
	t.Cleanup(func() {
		runSequenceSproutAtPathFn = originalSprout
		mergePhenotypeBranchToHostFn = originalMerge
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		injectMycorrhizalCacheFn = originalInject
		stashHostWorkspaceFn = originalStash
		restoreHostStashFn = originalRestore
		runContainerFitnessScoreFn = originalFitness
	})

	stashHostWorkspaceFn = func(ctx context.Context, root, runID string) (bool, error) { return false, nil }
	restoreHostStashFn = func(ctx context.Context, root string) error { return nil }
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
	createShadowWorktreeFn = func(sourcePath, branchName string) (string, error) { return t.TempDir(), nil }
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {}

	// Each sprout tags its terrarium image with its own branch name, so the
	// fitness stub can hand back a deterministic score per phenotype. Later
	// generations score lower (fitter, minimize) so evolution must progress.
	temps := &sync.Map{}
	scoreByBranch := &sync.Map{}
	runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
		if !orch.DisableMergeBack {
			t.Errorf("phenotype sprout ran with merge-back enabled")
		}
		temps.Store(orch.Temperature, struct{}{})
		var gen float64
		if strings.Contains(orch.SubstrateBranch, "-gen1-") {
			gen = 1
		}
		score := 100.0 - gen*90.0 // gen0 -> 100, gen1 -> 10
		scoreByBranch.Store(orch.SubstrateBranch, score)
		return sproutExecutionResult{Response: "grew " + orch.SubstrateBranch, ImageName: orch.SubstrateBranch}, nil
	}
	runContainerFitnessScoreFn = func(ctx context.Context, imageName, shadowPath, fitnessTest, pattern string) (float64, string, error) {
		v, ok := scoreByBranch.Load(imageName)
		if !ok {
			return 0, "", nil
		}
		return v.(float64), "ok", nil
	}

	var mergedBranch atomic.Value
	mergePhenotypeBranchToHostFn = func(ctx context.Context, sourcePath, branchName string) error {
		mergedBranch.Store(branchName)
		return nil
	}

	threshold := 20.0
	step := &SequenceStep{
		ID:         "optimize",
		Status:     sequenceStatusPending,
		Transcript: "make the hot loop faster",
		Selection: &SelectionConfig{
			PopulationSize:   3,
			MaxGenerations:   3,
			FitnessTest:      "make benchmark",
			FitnessGoal:      selectionGoalMinimize,
			FitnessThreshold: &threshold,
			SurvivorFraction: 0.5,
		},
	}
	seq := &Sequence{Name: "evolve", Branch: "feat/phenotypic-selection", Steps: []SequenceStep{*step}}
	if err := normalizeSequence("evolve.yaml", seq); err != nil {
		t.Fatalf("normalizeSequence: %v", err)
	}
	normalized := &seq.Steps[0]

	report, err := runGeneticSelection(context.Background(), seq, normalized, repo, nil)
	if err != nil {
		t.Fatalf("runGeneticSelection failed: %v", err)
	}

	branch, _ := mergedBranch.Load().(string)
	if !strings.Contains(branch, "-gen1-") {
		t.Fatalf("grafted branch = %q, want a generation-1 AlphaPhenotype", branch)
	}
	if !strings.Contains(report, "AlphaPhenotype") || !strings.Contains(report, "FitnessScore") {
		t.Fatalf("report missing alpha summary: %q", report)
	}

	// Population spread should have produced more than one distinct temperature.
	distinct := 0
	temps.Range(func(_, _ any) bool { distinct++; return true })
	if distinct < 2 {
		t.Fatalf("expected multiple distinct temperatures across the population, got %d", distinct)
	}
}
