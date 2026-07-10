package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/roots/llm"
)

type stubBranchingClient struct {
	response string
	err      error
}

func (s *stubBranchingClient) Call(ctx context.Context, messages []llm.Message) (string, error) {
	return s.response, s.err
}

func (s *stubBranchingClient) CallStream(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (string, error) {
	return s.response, s.err
}

func stubParallelWorktrees(t *testing.T) {
	t.Helper()

	originalCreate := createShadowWorktreeFn
	originalRemove := removeShadowWorktreeFn
	originalInject := injectMycorrhizalCacheFn
	t.Cleanup(func() {
		createShadowWorktreeFn = originalCreate
		removeShadowWorktreeFn = originalRemove
		injectMycorrhizalCacheFn = originalInject
	})

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
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
}

func sproutIndexFromBranch(branchName string) int {
	needle := "-sprout-"
	idx := strings.LastIndex(branchName, needle)
	if idx < 0 {
		return -1
	}
	parsed, err := strconv.Atoi(branchName[idx+len(needle):])
	if err != nil {
		return -1
	}
	return parsed
}

func TestParallelSproutingGrowsFiveConcurrentSprouts(t *testing.T) {
	repo := initPhenotypeTestRepo(t)
	stubParallelWorktrees(t)

	originalBranch := branchSubTasksFn
	originalSproutAtPath := runSequenceSproutAtPathFn
	originalSprout := runSequenceSproutFn
	originalMerge := mergePhloemChannelToHostFn
	t.Cleanup(func() {
		branchSubTasksFn = originalBranch
		runSequenceSproutAtPathFn = originalSproutAtPath
		runSequenceSproutFn = originalSprout
		mergePhloemChannelToHostFn = originalMerge
	})

	branchSubTasksFn = func(ctx context.Context, step *SequenceStep, count int) []mycelialSubTask {
		subTasks := make([]mycelialSubTask, 0, count)
		for i := 0; i < count; i++ {
			subTasks = append(subTasks, mycelialSubTask{ID: "shard-" + strconv.Itoa(i), Transcript: "do shard " + strconv.Itoa(i)})
		}
		return subTasks
	}

	var running atomic.Int32
	var maxConcurrent atomic.Int32
	allEmerged := make(chan struct{})
	var emergedOnce sync.Once
	var started atomic.Int32

	var mu sync.Mutex
	seenBranches := make(map[int]string)
	seenDisable := make(map[int]bool)
	seenTranscripts := make(map[int]string)

	runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
		index := sproutIndexFromBranch(orch.SubstrateBranch)
		current := running.Add(1)
		defer running.Add(-1)
		for {
			prev := maxConcurrent.Load()
			if current <= prev || maxConcurrent.CompareAndSwap(prev, current) {
				break
			}
		}

		mu.Lock()
		seenBranches[index] = orch.SubstrateBranch
		seenDisable[index] = orch.DisableMergeBack
		seenTranscripts[index] = taskPrompt
		mu.Unlock()

		if started.Add(1) == 5 {
			emergedOnce.Do(func() { close(allEmerged) })
		}

		select {
		case <-allEmerged:
		case <-ctx.Done():
			return sproutExecutionResult{}, ctx.Err()
		}
		return sproutExecutionResult{Response: "report-" + strconv.Itoa(index)}, nil
	}

	mergedBranches := make([]string, 0, 5)
	mergePhloemChannelToHostFn = func(ctx context.Context, sourcePath, branchName, stepID string) error {
		mu.Lock()
		defer mu.Unlock()
		mergedBranches = append(mergedBranches, branchName)
		return nil
	}

	var consensusTranscript string
	runSequenceSproutFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt string) (string, error) {
		if !orch.IsCoordinator || orch.Genotype != "meristem" {
			t.Errorf("consensus orch = %+v, want meristem coordinator", orch)
		}
		consensusTranscript = taskPrompt
		return "consensus-result", nil
	}

	bus := eventbus.New()
	var eventMu sync.Mutex
	eventCounts := make(map[eventbus.EventType]int)
	for _, eventType := range eventbus.AllEventTypes() {
		et := eventType
		bus.Subscribe(et, func(event eventbus.Event) {
			eventMu.Lock()
			defer eventMu.Unlock()
			eventCounts[et]++
		})
	}

	step := &SequenceStep{
		ID:         "meristem-parallel",
		Status:     sequenceStatusPending,
		Transcript: "build the whole feature",
		Parallel:   true,
	}
	seq := &Sequence{
		Name:   "parallel-sprouting",
		Branch: "feat/parallel",
		Steps:  []SequenceStep{*step},
	}
	if err := normalizeSequence("parallel.yaml", seq); err != nil {
		t.Fatalf("normalizeSequence failed: %v", err)
	}
	normalized := seq.Steps[0]

	output, err := defaultSequenceStepRunnerWithBus(context.Background(), seq, &normalized, repo, bus)
	if err != nil {
		t.Fatalf("defaultSequenceStepRunnerWithBus failed: %v", err)
	}
	if output != "consensus-result" {
		t.Fatalf("output = %q, want consensus-result", output)
	}

	if normalized.SproutCount != 5 {
		t.Fatalf("SproutCount = %d, want 5", normalized.SproutCount)
	}
	if got := maxConcurrent.Load(); got != 5 {
		t.Fatalf("max concurrent sprouts = %d, want 5", got)
	}

	for i := 0; i < 5; i++ {
		wantBranch := "feat/parallel/meristem-parallel-sprout-" + strconv.Itoa(i)
		if seenBranches[i] != wantBranch {
			t.Fatalf("branch[%d] = %q, want %q", i, seenBranches[i], wantBranch)
		}
		if !seenDisable[i] {
			t.Fatalf("DisableMergeBack[%d] = false, want true", i)
		}
		if seenTranscripts[i] != "do shard "+strconv.Itoa(i) {
			t.Fatalf("transcript[%d] = %q, want shard transcript", i, seenTranscripts[i])
		}
	}

	if len(mergedBranches) != 5 {
		t.Fatalf("merged branch count = %d, want 5", len(mergedBranches))
	}

	for i := 0; i < 5; i++ {
		if !strings.Contains(consensusTranscript, "report-"+strconv.Itoa(i)) {
			t.Fatalf("consensus transcript missing report-%d:\n%s", i, consensusTranscript)
		}
	}

	eventMu.Lock()
	defer eventMu.Unlock()
	if eventCounts[eventbus.EventParallelSprouting] != 1 {
		t.Fatalf("parallel-sprouting events = %d, want 1", eventCounts[eventbus.EventParallelSprouting])
	}
	if eventCounts[eventbus.EventSproutEmerged] != 5 {
		t.Fatalf("sprout-emerged events = %d, want 5", eventCounts[eventbus.EventSproutEmerged])
	}
	if eventCounts[eventbus.EventSproutMatured] != 5 {
		t.Fatalf("sprout-matured events = %d, want 5", eventCounts[eventbus.EventSproutMatured])
	}
	if eventCounts[eventbus.EventMycelialMerge] != 2 {
		t.Fatalf("mycelial-merge events = %d, want 2", eventCounts[eventbus.EventMycelialMerge])
	}
}

func TestParallelSproutingSurvivesWitheredSprouts(t *testing.T) {
	repo := initPhenotypeTestRepo(t)
	stubParallelWorktrees(t)

	originalBranch := branchSubTasksFn
	originalSproutAtPath := runSequenceSproutAtPathFn
	originalSprout := runSequenceSproutFn
	originalMerge := mergePhloemChannelToHostFn
	t.Cleanup(func() {
		branchSubTasksFn = originalBranch
		runSequenceSproutAtPathFn = originalSproutAtPath
		runSequenceSproutFn = originalSprout
		mergePhloemChannelToHostFn = originalMerge
	})

	branchSubTasksFn = func(ctx context.Context, step *SequenceStep, count int) []mycelialSubTask {
		subTasks := make([]mycelialSubTask, 0, count)
		for i := 0; i < count; i++ {
			subTasks = append(subTasks, mycelialSubTask{ID: "shard-" + strconv.Itoa(i), Transcript: "do shard " + strconv.Itoa(i)})
		}
		return subTasks
	}

	runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
		switch sproutIndexFromBranch(orch.SubstrateBranch) {
		case 0:
			panic("terrarium container crashed")
		case 1:
			return sproutExecutionResult{}, fmt.Errorf("llm api call: %w", context.DeadlineExceeded)
		default:
			return sproutExecutionResult{Response: "survivor-report"}, nil
		}
	}

	var mu sync.Mutex
	var mergedBranches []string
	mergePhloemChannelToHostFn = func(ctx context.Context, sourcePath, branchName, stepID string) error {
		mu.Lock()
		defer mu.Unlock()
		mergedBranches = append(mergedBranches, branchName)
		return nil
	}

	var consensusTranscript string
	runSequenceSproutFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt string) (string, error) {
		consensusTranscript = taskPrompt
		return "healed-consensus", nil
	}

	step := &SequenceStep{
		ID:          "worker-parallel",
		Status:      sequenceStatusPending,
		Transcript:  "resilient task",
		Parallel:    true,
		SproutCount: 4,
	}
	seq := &Sequence{
		Name:   "withered-sprouts",
		Branch: "feat/withered",
		Steps:  []SequenceStep{*step},
	}

	output, err := runParallelSprouting(context.Background(), seq, step, repo, nil)
	if err != nil {
		t.Fatalf("runParallelSprouting failed despite survivors: %v", err)
	}
	if output != "healed-consensus" {
		t.Fatalf("output = %q, want healed-consensus", output)
	}

	if len(mergedBranches) != 2 {
		t.Fatalf("merged branches = %v, want the 2 survivors", mergedBranches)
	}
	if !strings.Contains(consensusTranscript, "WITHERED") {
		t.Fatalf("consensus transcript does not report withered sprouts:\n%s", consensusTranscript)
	}
	if !strings.Contains(consensusTranscript, "panic") {
		t.Fatalf("consensus transcript does not surface the panic:\n%s", consensusTranscript)
	}
}

func TestParallelSproutingFailsWhenAllSproutsWither(t *testing.T) {
	repo := initPhenotypeTestRepo(t)
	stubParallelWorktrees(t)

	originalBranch := branchSubTasksFn
	originalSproutAtPath := runSequenceSproutAtPathFn
	originalSprout := runSequenceSproutFn
	t.Cleanup(func() {
		branchSubTasksFn = originalBranch
		runSequenceSproutAtPathFn = originalSproutAtPath
		runSequenceSproutFn = originalSprout
	})

	branchSubTasksFn = func(ctx context.Context, step *SequenceStep, count int) []mycelialSubTask {
		return []mycelialSubTask{
			{ID: "shard-0", Transcript: "doomed"},
			{ID: "shard-1", Transcript: "doomed"},
		}
	}
	runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
		return sproutExecutionResult{}, fmt.Errorf("container exited 137")
	}
	runSequenceSproutFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt string) (string, error) {
		t.Error("consensus sprout must not run when every sprout withered")
		return "", nil
	}

	step := &SequenceStep{
		ID:          "doomed-parallel",
		Transcript:  "hopeless task",
		Parallel:    true,
		SproutCount: 2,
	}
	seq := &Sequence{Name: "all-withered", Branch: "feat/doomed", Steps: []SequenceStep{*step}}

	_, err := runParallelSprouting(context.Background(), seq, step, repo, nil)
	if err == nil {
		t.Fatalf("expected error when all sprouts wither")
	}
	if !strings.Contains(err.Error(), "all 2 sprouts withered") {
		t.Fatalf("error = %v, want all-withered failure", err)
	}
}

func TestBranchSubTasksParsesCoordinatorPlan(t *testing.T) {
	originalClient := newMeristemBranchingClient
	t.Cleanup(func() { newMeristemBranchingClient = originalClient })

	newMeristemBranchingClient = func() llmCaller {
		return &stubBranchingClient{response: "```json\n[{\"id\": \"map-api\", \"transcript\": \"update the api\"}, {\"id\": \"map-ui\", \"transcript\": \"update the ui\"}]\n```"}
	}

	step := &SequenceStep{ID: "meristem-map", Transcript: "big task"}
	subTasks := branchSubTasksFn(context.Background(), step, 5)

	if len(subTasks) != 2 {
		t.Fatalf("sub-task count = %d, want 2", len(subTasks))
	}
	if subTasks[0].ID != "map-api" || subTasks[0].Transcript != "update the api" {
		t.Fatalf("subTasks[0] = %+v", subTasks[0])
	}
}

func TestBranchSubTasksDegradesGracefullyOnLLMTimeout(t *testing.T) {
	originalClient := newMeristemBranchingClient
	originalTimeout := meristemBranchingTimeout
	t.Cleanup(func() {
		newMeristemBranchingClient = originalClient
		meristemBranchingTimeout = originalTimeout
	})

	meristemBranchingTimeout = 10 * time.Millisecond
	newMeristemBranchingClient = func() llmCaller {
		return &stubBranchingClient{err: context.DeadlineExceeded}
	}

	step := &SequenceStep{ID: "meristem-map", Transcript: "big task"}
	subTasks := branchSubTasksFn(context.Background(), step, 3)

	if len(subTasks) != 3 {
		t.Fatalf("fallback sub-task count = %d, want 3", len(subTasks))
	}
	for i, subTask := range subTasks {
		if !strings.Contains(subTask.Transcript, "big task") {
			t.Fatalf("fallback shard %d lost the original transcript: %q", i, subTask.Transcript)
		}
	}
}

func TestNormalizeSequenceClampsSproutCount(t *testing.T) {
	seq := &Sequence{
		Name: "clamp",
		Steps: []SequenceStep{
			{ID: "default-count", Transcript: "work", Parallel: true},
			{ID: "clamped-count", Transcript: "work", Parallel: true, SproutCount: 50},
			{ID: "not-parallel", Transcript: "work", SproutCount: 50},
		},
	}

	if err := normalizeSequence("clamp.yaml", seq); err != nil {
		t.Fatalf("normalizeSequence failed: %v", err)
	}
	if seq.Steps[0].SproutCount != defaultParallelSproutCount {
		t.Fatalf("default SproutCount = %d, want %d", seq.Steps[0].SproutCount, defaultParallelSproutCount)
	}
	if seq.Steps[1].SproutCount != maxParallelSproutCount {
		t.Fatalf("clamped SproutCount = %d, want %d", seq.Steps[1].SproutCount, maxParallelSproutCount)
	}
	if seq.Steps[2].SproutCount != 50 {
		t.Fatalf("non-parallel SproutCount = %d, want untouched 50", seq.Steps[2].SproutCount)
	}
}
