package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/roots/llm"
)

func TestResolveLLMClientAppliesTemperatureOverride(t *testing.T) {
	var seenRequest struct {
		Model       string        `json:"model"`
		Temperature float64       `json:"temperature"`
		Messages    []llm.Message `json:"messages"`
		Stream      bool          `json:"stream"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "ok",
					},
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("COORDINATOR_LLM_PROVIDER", "local")
	t.Setenv("COORDINATOR_LOCAL_INFERENCE_URL", server.URL+"/v1")
	t.Setenv("COORDINATOR_MODEL_NAME", "temperature-check")

	client := (&DockerOrchestrator{
		IsCoordinator: true,
		Temperature:   0.85,
	}).resolveLLMClient()

	if client == nil {
		t.Fatalf("resolveLLMClient returned nil")
	}

	content, err := client.Call(context.Background(), []llm.Message{
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("client.Call failed: %v", err)
	}
	if content != "ok" {
		t.Fatalf("client.Call content = %q, want ok", content)
	}

	if seenRequest.Model != "temperature-check" {
		t.Fatalf("model = %q, want temperature-check", seenRequest.Model)
	}
	if seenRequest.Temperature != 0.85 {
		t.Fatalf("temperature = %v, want 0.85", seenRequest.Temperature)
	}
	if len(seenRequest.Messages) != 1 || seenRequest.Messages[0].Content != "hello" {
		t.Fatalf("messages = %#v, want one user message", seenRequest.Messages)
	}
}

func TestPhenotypicSelectionDispatchesConcurrentSprouts(t *testing.T) {
	repo := initPhenotypeTestRepo(t)
	substratePath := repo

	originalSprout := runSequenceSproutAtPathFn
	originalMerge := mergePhenotypeBranchToHostFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn

	t.Cleanup(func() {
		runSequenceSproutAtPathFn = originalSprout
		mergePhenotypeBranchToHostFn = originalMerge
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
	})

	var started atomic.Int32
	var maxConcurrent atomic.Int32
	var running atomic.Int32
	var mu sync.Mutex
	temperatures := make([]float64, 3)
	disableFlags := make([]bool, 3)
	branches := make([]string, 3)
	allowWinner := make(chan struct{})
	terrariumRoot := filepath.Join(t.TempDir(), "terrariumes")

	runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
		index := phenotypeIndexFromBranch(orch.SubstrateBranch)
		current := running.Add(1)
		for {
			prev := maxConcurrent.Load()
			if current <= prev || maxConcurrent.CompareAndSwap(prev, current) {
				break
			}
		}

		started.Add(1)
		mu.Lock()
		temperatures[index] = orch.Temperature
		disableFlags[index] = orch.DisableMergeBack
		branches[index] = orch.SubstrateBranch
		mu.Unlock()

		defer running.Add(-1)

		switch index {
		case 0:
			select {
			case <-allowWinner:
			case <-ctx.Done():
				return sproutExecutionResult{}, ctx.Err()
			}
			return sproutExecutionResult{Response: "winner-0", ImageName: "sprout-image"}, nil
		default:
			select {
			case <-ctx.Done():
				return sproutExecutionResult{}, ctx.Err()
			}
		}
	}

	mergeCalls := make(chan string, 1)
	mergePhenotypeBranchToHostFn = func(ctx context.Context, sourcePath, branchName string) error {
		mergeCalls <- branchName
		return nil
	}

	createShadowWorktreeFn = func(sourcePath, branchName string) (string, error) {
		path := filepath.Join(terrariumRoot, strings.ReplaceAll(branchName, "/", "-"))
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", err
		}
		return path, nil
	}
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {
		_ = os.RemoveAll(shadowPath)
	}

	step := &SequenceStep{
		ID:              "step-alpha",
		Status:          sequenceStatusPending,
		Transcript:      "build the best phenotype",
		PhenotypesCount: 3,
	}
	seq := &Sequence{
		Name:   "phenotype-select",
		Branch: "feature/phenotype",
		Steps:  []SequenceStep{*step},
	}

	done := make(chan struct{})
	var runErr error
	var output string
	go func() {
		defer close(done)
		output, runErr = defaultSequenceStepRunner(context.Background(), seq, step, substratePath)
	}()

	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for started.Load() < 3 {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for concurrent sprouts, started=%d", started.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(allowWinner)

	select {
	case branch := <-mergeCalls:
		if branch != "feature/phenotype/step-alpha-phenotype-0" {
			t.Fatalf("merged branch = %q, want feature/phenotype/step-alpha-phenotype-0", branch)
		}
	case <-deadline.C:
		t.Fatalf("timed out waiting for winner merge")
	}

	select {
	case <-done:
	case <-deadline.C:
		t.Fatalf("timed out waiting for phenotypic selection to finish")
	}

	if runErr != nil {
		t.Fatalf("defaultSequenceStepRunner failed: %v", runErr)
	}
	if output != "winner-0" {
		t.Fatalf("unexpected output %q", output)
	}

	if maxConcurrent.Load() < 2 {
		t.Fatalf("max concurrent runs = %d, want at least 2", maxConcurrent.Load())
	}

	expectedTemperatures := []float64{0.1, 0.4, 0.7}
	for i, want := range expectedTemperatures {
		if temperatures[i] != want {
			t.Fatalf("temperature[%d] = %v, want %v", i, temperatures[i], want)
		}
		if !disableFlags[i] {
			t.Fatalf("disableFlags[%d] = false, want true", i)
		}
	}

	expectedBranches := []string{
		"feature/phenotype/step-alpha-phenotype-0",
		"feature/phenotype/step-alpha-phenotype-1",
		"feature/phenotype/step-alpha-phenotype-2",
	}
	for i, want := range expectedBranches {
		if branches[i] != want {
			t.Fatalf("branch[%d] = %q, want %q", i, branches[i], want)
		}
	}
}

func TestPhenotypicSelectionRunsFitnessTests(t *testing.T) {
	repo := initPhenotypeTestRepo(t)
	substratePath := repo

	originalSprout := runSequenceSproutAtPathFn
	originalMerge := mergePhenotypeBranchToHostFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalFitness := runContainerFitnessTestFn

	t.Cleanup(func() {
		runSequenceSproutAtPathFn = originalSprout
		mergePhenotypeBranchToHostFn = originalMerge
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		runContainerFitnessTestFn = originalFitness
	})

	fitnessCalls := make(chan string, 2)
	mergeCalls := make(chan string, 1)
	winnerReady := make(chan struct{})
	terrariumRoot := filepath.Join(t.TempDir(), "terrariumes")

	createShadowWorktreeFn = func(sourcePath, branchName string) (string, error) {
		path := filepath.Join(terrariumRoot, strings.ReplaceAll(branchName, "/", "-"))
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", err
		}
		return path, nil
	}
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {
		_ = os.RemoveAll(shadowPath)
	}
	runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
		index := phenotypeIndexFromBranch(orch.SubstrateBranch)
		if index == 1 {
			select {
			case <-winnerReady:
			case <-ctx.Done():
				return sproutExecutionResult{}, ctx.Err()
			}
		}
		return sproutExecutionResult{
			Response:   fmt.Sprintf("response-%d", index),
			CommitHash: fmt.Sprintf("commit-%d", index),
			ImageName:  "fitness-image",
		}, nil
	}
	mergePhenotypeBranchToHostFn = func(ctx context.Context, sourcePath, branchName string) error {
		mergeCalls <- branchName
		return nil
	}
	runContainerFitnessTestFn = func(ctx context.Context, imageName, shadowPath, fitnessTest string) error {
		fitnessCalls <- shadowPath
		if !strings.HasSuffix(filepath.Base(shadowPath), "phenotype-1") {
			return fmt.Errorf("fitness rejected %s", shadowPath)
		}
		if imageName != "fitness-image" {
			return fmt.Errorf("imageName = %s, want fitness-image", imageName)
		}
		if fitnessTest != "make fitness" {
			return fmt.Errorf("fitnessTest = %q, want make fitness", fitnessTest)
		}
		return nil
	}

	step := &SequenceStep{
		ID:              "step-beta",
		Status:          sequenceStatusPending,
		Transcript:      "build the best phenotype",
		PhenotypesCount: 2,
		FitnessTest:     "make fitness",
	}
	seq := &Sequence{
		Name:   "phenotype-fitness",
		Branch: "feature/fitness",
		Steps:  []SequenceStep{*step},
	}

	done := make(chan struct{})
	var runErr error
	var output string
	go func() {
		defer close(done)
		output, runErr = defaultSequenceStepRunner(context.Background(), seq, step, substratePath)
	}()

	var seenFitness []string
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()

	for len(seenFitness) < 1 {
		select {
		case shadowPath := <-fitnessCalls:
			seenFitness = append(seenFitness, shadowPath)
		case <-deadline.C:
			t.Fatalf("timed out waiting for failing fitness test")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	close(winnerReady)

	select {
	case <-done:
	case <-deadline.C:
		t.Fatalf("timed out waiting for fitness selection to finish")
	}

	if runErr != nil {
		t.Fatalf("defaultSequenceStepRunner failed: %v", runErr)
	}
	if output != "response-1" {
		t.Fatalf("output = %q, want response-1", output)
	}

	select {
	case branch := <-mergeCalls:
		if branch != "feature/fitness/step-beta-phenotype-1" {
			t.Fatalf("merged branch = %q, want feature/fitness/step-beta-phenotype-1", branch)
		}
	case <-deadline.C:
		t.Fatalf("timed out waiting for phenotype merge")
	}

	if len(seenFitness) != 1 {
		t.Fatalf("fitness call count = %d, want 1", len(seenFitness))
	}
	if !strings.Contains(seenFitness[0], "phenotype-0") {
		t.Fatalf("first fitness call = %q, want phenotype-0", seenFitness[0])
	}
}

func TestNormalizeSequenceDefaultsPhenotypesCount(t *testing.T) {
	seq := &Sequence{
		Name: "normalize",
		Steps: []SequenceStep{
			{
				ID:              "step-gamma",
				Status:          sequenceStatusPending,
				Transcript:      "do work",
				PhenotypesCount: 0,
				FitnessTest:     "  make test  ",
			},
		},
	}

	if err := normalizeSequence("normalize.yaml", seq); err != nil {
		t.Fatalf("normalizeSequence failed: %v", err)
	}
	if seq.Steps[0].PhenotypesCount != 1 {
		t.Fatalf("PhenotypesCount = %d, want 1", seq.Steps[0].PhenotypesCount)
	}
	if seq.Steps[0].FitnessTest != "make test" {
		t.Fatalf("FitnessTest = %q, want make test", seq.Steps[0].FitnessTest)
	}
}

func TestMergePhenotypeBranchToHostRestoresDirtyWorkspace(t *testing.T) {
	repo := initPhenotypeTestRepo(t)

	currentBranch, err := runGitCommand(context.Background(), repo, "branch", "--show-current")
	if err != nil {
		t.Fatalf("read current branch: %v", err)
	}

	phenotypeBranch := "feature/merge-helper-test"
	if _, err := runGitCommand(context.Background(), repo, "checkout", "-b", phenotypeBranch); err != nil {
		t.Fatalf("checkout phenotype branch: %v", err)
	}

	winnerPath := filepath.Join(repo, "winner.txt")
	if err := os.WriteFile(winnerPath, []byte("winner\n"), 0o644); err != nil {
		t.Fatalf("write winner file: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "add", "winner.txt"); err != nil {
		t.Fatalf("stage winner file: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "commit", "-m", "winner"); err != nil {
		t.Fatalf("commit winner file: %v", err)
	}

	winnerCommit, err := runGitCommand(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read winner commit: %v", err)
	}

	if _, err := runGitCommand(context.Background(), repo, "checkout", currentBranch); err != nil {
		t.Fatalf("checkout host branch: %v", err)
	}

	scratchPath := filepath.Join(repo, "scratch.txt")
	if err := os.WriteFile(scratchPath, []byte("scratch\n"), 0o644); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}

	if err := mergePhenotypeBranchToHost(context.Background(), repo, phenotypeBranch); err != nil {
		t.Fatalf("mergePhenotypeBranchToHost failed: %v", err)
	}

	headCommit, err := runGitCommand(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read head commit: %v", err)
	}
	if headCommit != winnerCommit {
		t.Fatalf("HEAD = %s, want %s", headCommit, winnerCommit)
	}

	if _, err := os.Stat(scratchPath); err != nil {
		t.Fatalf("scratch file missing after stash restore: %v", err)
	}

	status, err := runGitCommand(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, "scratch.txt") {
		t.Fatalf("expected restored scratch file in status, got %q", status)
	}
}

func initPhenotypeTestRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := execCommand(t, root, args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (output: %s)", args, err, strings.TrimSpace(string(output)))
		}
	}

	seedPath := filepath.Join(root, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "add", "seed.txt"); err != nil {
		t.Fatalf("stage seed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	return root
}

func execCommand(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}

func phenotypeIndexFromBranch(branchName string) int {
	branchName = strings.TrimSpace(branchName)
	needle := "-phenotype-"
	idx := strings.LastIndex(branchName, needle)
	if idx < 0 {
		return -1
	}

	value := branchName[idx+len(needle):]
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}
