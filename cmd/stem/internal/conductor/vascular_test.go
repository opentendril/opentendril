package conductor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type vascularSproutRecord struct {
	stepID           string
	branchName       string
	shadowPath       string
	sourcePath       string
	substrate        string
	disableMergeBack bool
}

func TestMeristemStepParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vascular.yaml")

	content := []byte(`name: vascular
steps:
  - id: root-meristem
    transcript: establish the root
  - id: bud-alpha
    depends_on:
      - root-meristem
    transcript: grow the first bud
  - id: bud-beta
    dependsOn:
      - bud-alpha
    transcript: grow the second bud
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write vascular sequence: %v", err)
	}

	seq, err := LoadSequence(path)
	if err != nil {
		t.Fatalf("LoadSequence failed: %v", err)
	}
	if len(seq.Steps) != 3 {
		t.Fatalf("step count = %d, want 3", len(seq.Steps))
	}

	tests := []struct {
		name string
		step SequenceStep
		want []string
	}{
		{
			name: "legacy snake case",
			step: seq.Steps[1],
			want: []string{"root-meristem"},
		},
		{
			name: "camel case",
			step: seq.Steps[2],
			want: []string{"bud-alpha"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.step.DependsOn, tt.want) {
				t.Fatalf("DependsOn = %#v, want %#v", tt.step.DependsOn, tt.want)
			}
			if len(tt.step.DependsOnLegacy) != 0 {
				t.Fatalf("DependsOnLegacy = %#v, want empty after normalization", tt.step.DependsOnLegacy)
			}
		})
	}
}

func TestVascularParallelStepExecution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vascular.yaml")

	seq := &Sequence{
		Name:             "vascular",
		Branch:           "feature/vascular",
		ConcurrencyLimit: 2,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "step-alpha", Status: sequenceStatusPending, Transcript: "grow alpha"},
			{ID: "step-beta", Status: sequenceStatusPending, Transcript: "grow beta"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	originalSprout := runSequenceSproutAtPathFn
	originalMerge := mergePhloemChannelToHostFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalInjectCache := injectMycorrhizalCacheFn

	t.Cleanup(func() {
		runSequenceSproutAtPathFn = originalSprout
		mergePhloemChannelToHostFn = originalMerge
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		injectMycorrhizalCacheFn = originalInjectCache
	})

	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	var mu sync.Mutex
	records := make(map[string]vascularSproutRecord, 2)
	mergeCalls := make(chan string, 2)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	shadowRoot := filepath.Join(dir, "shadows")

	createShadowWorktreeFn = func(sourcePath, branchName string) (string, error) {
		shadowPath := filepath.Join(shadowRoot, strings.ReplaceAll(branchName, "/", "-"))
		if err := os.MkdirAll(shadowPath, 0o755); err != nil {
			return "", err
		}
		return shadowPath, nil
	}
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {
		_ = os.RemoveAll(shadowPath)
	}
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
	mergePhloemChannelToHostFn = func(ctx context.Context, sourcePath, branchName, stepID string) error {
		mergeCalls <- branchName
		return nil
	}
	runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
		current := concurrent.Add(1)
		for {
			prev := maxConcurrent.Load()
			if current <= prev || maxConcurrent.CompareAndSwap(prev, current) {
				break
			}
		}

		mu.Lock()
		records[orch.StepID] = vascularSproutRecord{
			stepID:           orch.StepID,
			branchName:       orch.SubstrateBranch,
			shadowPath:       mountPath,
			sourcePath:       sourcePath,
			substrate:        orch.Substrate,
			disableMergeBack: orch.DisableMergeBack,
		}
		mu.Unlock()

		started <- struct{}{}
		defer concurrent.Add(-1)

		select {
		case <-release:
		case <-ctx.Done():
			return sproutExecutionResult{}, ctx.Err()
		}

		return sproutExecutionResult{
			Response:   "response-" + orch.StepID,
			ImageName:  "vascular-image",
			CommitHash: "commit-" + orch.StepID,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	var runErr error
	var result *Sequence
	go func() {
		defer close(done)
		result, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:      io.Discard,
			Stderr:      io.Discard,
			Interactive: false,
		})
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for parallel step start: %v", ctx.Err())
		}
	}

	close(release)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for sequence completion: %v", ctx.Err())
	}

	if runErr != nil {
		t.Fatalf("RunSequence failed: %v", runErr)
	}
	if result == nil {
		t.Fatalf("expected sequence result")
	}
	if len(result.Steps) != 2 {
		t.Fatalf("result step count = %d, want 2", len(result.Steps))
	}
	for _, step := range result.Steps {
		if step.Status != sequenceStatusComplete {
			t.Fatalf("step %s status = %s, want complete", step.ID, step.Status)
		}
	}
	if maxConcurrent.Load() != 2 {
		t.Fatalf("max concurrent sprout runs = %d, want 2", maxConcurrent.Load())
	}

	alpha, ok := records["step-alpha"]
	if !ok {
		t.Fatalf("missing step-alpha record: %#v", records)
	}
	beta, ok := records["step-beta"]
	if !ok {
		t.Fatalf("missing step-beta record: %#v", records)
	}

	if alpha.branchName != "feature/vascular/step-alpha" {
		t.Fatalf("step-alpha branch = %q, want feature/vascular/step-alpha", alpha.branchName)
	}
	if beta.branchName != "feature/vascular/step-beta" {
		t.Fatalf("step-beta branch = %q, want feature/vascular/step-beta", beta.branchName)
	}
	if alpha.shadowPath == beta.shadowPath {
		t.Fatalf("shadow worktrees collided: %q", alpha.shadowPath)
	}
	if alpha.substrate != alpha.shadowPath {
		t.Fatalf("step-alpha substrate = %q, want shadow path %q", alpha.substrate, alpha.shadowPath)
	}
	if beta.substrate != beta.shadowPath {
		t.Fatalf("step-beta substrate = %q, want shadow path %q", beta.substrate, beta.shadowPath)
	}
	if alpha.sourcePath != dir || beta.sourcePath != dir {
		t.Fatalf("source paths = %q and %q, want %q", alpha.sourcePath, beta.sourcePath, dir)
	}
	if !alpha.disableMergeBack || !beta.disableMergeBack {
		t.Fatalf("expected DisableMergeBack to be true for parallel sprouts")
	}

	merged := map[string]struct{}{}
	for i := 0; i < 2; i++ {
		select {
		case branchName := <-mergeCalls:
			merged[branchName] = struct{}{}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for merge calls: %v", ctx.Err())
		}
	}
	if _, ok := merged["feature/vascular/step-alpha"]; !ok {
		t.Fatalf("step-alpha merge was not observed: %#v", merged)
	}
	if _, ok := merged["feature/vascular/step-beta"]; !ok {
		t.Fatalf("step-beta merge was not observed: %#v", merged)
	}
}

func TestVascularMergeConflictHandling(t *testing.T) {
	baseDir := t.TempDir()
	binDir := filepath.Join(baseDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake git bin dir: %v", err)
	}

	logFile := filepath.Join(baseDir, "git.log")
	scriptPath := filepath.Join(binDir, "git")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$GIT_LOG"
case "$*" in
  *"merge --abort"*)
    exit 0
    ;;
  *"merge --no-ff "*)
    printf '%s\n' "simulated merge conflict" >&2
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git script: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_LOG", logFile)

	originalStash := stashHostWorkspaceFn
	originalRestore := restoreHostStashFn
	t.Cleanup(func() {
		stashHostWorkspaceFn = originalStash
		restoreHostStashFn = originalRestore
	})

	var stashRunID string
	var restoreCalled atomic.Bool
	stashHostWorkspaceFn = func(ctx context.Context, root, runID string) (bool, error) {
		stashRunID = runID
		return true, nil
	}
	restoreHostStashFn = func(ctx context.Context, root string) error {
		restoreCalled.Store(true)
		return nil
	}

	repo := filepath.Join(baseDir, "workspace")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	err := mergePhloemChannelToHost(context.Background(), repo, "feature/vascular/step-alpha", "step-alpha")
	if err == nil {
		t.Fatalf("mergePhloemChannelToHost succeeded, want conflict error")
	}
	if !strings.Contains(err.Error(), "simulated merge conflict") {
		t.Fatalf("merge error = %v, want simulated merge conflict", err)
	}
	if stashRunID != "phloem-merge-step-alpha" {
		t.Fatalf("stash run ID = %q, want phloem-merge-step-alpha", stashRunID)
	}
	if !restoreCalled.Load() {
		t.Fatalf("restoreHostStash was not called")
	}

	logBytes, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatalf("read fake git log: %v", readErr)
	}
	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	if len(lines) != 3 {
		t.Fatalf("git command count = %d, want 3 (%v)", len(lines), lines)
	}
	if !strings.Contains(lines[0], "rev-parse --show-toplevel") {
		t.Fatalf("first git command = %q, want repo-root probe", lines[0])
	}
	if !strings.Contains(lines[1], "merge --no-ff") {
		t.Fatalf("second git command = %q, want merge invocation", lines[1])
	}
	if !strings.Contains(lines[2], "merge --abort") {
		t.Fatalf("third git command = %q, want merge --abort", lines[2])
	}
}
