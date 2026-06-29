package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSequenceLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.yaml")

	original := &Sequence{
		Name:             "round-trip",
		Substrate:        "/tmp/workspace",
		Branch:           "feature/round-trip",
		ConcurrencyLimit: 2,
		OnFailure:        sequenceOnFailureRetry,
		MaxRetries:       4,
		Steps: []SequenceStep{
			{
				ID:         "step-a",
				Status:     sequenceStatusPending,
				DependsOn:  []string{},
				Transcript: "do the first thing",
			},
			{
				ID:         "step-b",
				Status:     sequenceStatusComplete,
				DependsOn:  []string{"step-a"},
				Transcript: "do the second thing",
			},
		},
	}

	if err := SaveSequence(path, original); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	loaded, err := LoadSequence(path)
	if err != nil {
		t.Fatalf("LoadSequence failed: %v", err)
	}

	if loaded.Name != original.Name {
		t.Fatalf("loaded Name = %q, want %q", loaded.Name, original.Name)
	}
	if loaded.Branch != original.Branch {
		t.Fatalf("loaded Branch = %q, want %q", loaded.Branch, original.Branch)
	}
	if loaded.ConcurrencyLimit != original.ConcurrencyLimit {
		t.Fatalf("loaded ConcurrencyLimit = %d, want %d", loaded.ConcurrencyLimit, original.ConcurrencyLimit)
	}
	if loaded.OnFailure != original.OnFailure {
		t.Fatalf("loaded OnFailure = %q, want %q", loaded.OnFailure, original.OnFailure)
	}
	if loaded.MaxRetries != original.MaxRetries {
		t.Fatalf("loaded MaxRetries = %d, want %d", loaded.MaxRetries, original.MaxRetries)
	}
	if len(loaded.Steps) != len(original.Steps) {
		t.Fatalf("loaded step count = %d, want %d", len(loaded.Steps), len(original.Steps))
	}
}

func TestRunSequenceParallelDAG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parallel.yaml")

	seq := &Sequence{
		Name:             "parallel",
		ConcurrencyLimit: 2,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
			{ID: "step-b", Status: sequenceStatusPending, Transcript: "b"},
			{ID: "step-c", Status: sequenceStatusPending, DependsOn: []string{"step-a", "step-b"}, Transcript: "c"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var concurrent int32
	var maxConcurrent int32
	var mu sync.Mutex
	var events []string
	started := make(chan string, len(seq.Steps))
	release := make(chan struct{})

	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		current := atomic.AddInt32(&concurrent, 1)
		for {
			prev := atomic.LoadInt32(&maxConcurrent)
			if current <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, current) {
				break
			}
		}

		mu.Lock()
		events = append(events, "start:"+step.ID)
		mu.Unlock()
		started <- step.ID

		if step.ID != "step-c" {
			select {
			case <-release:
			case <-ctx.Done():
				atomic.AddInt32(&concurrent, -1)
				return "", ctx.Err()
			}
		}

		atomic.AddInt32(&concurrent, -1)
		return "ok:" + step.ID, nil
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
			StepRunner:  stepRunner,
		})
	}()

	got := make([]string, 0, 3)
	for len(got) < 2 {
		select {
		case stepID := <-started:
			got = append(got, stepID)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for parallel starts: %v", ctx.Err())
		}
	}

	close(release)

	select {
	case stepID := <-started:
		got = append(got, stepID)
	case <-ctx.Done():
		t.Fatalf("timed out waiting for dependent step: %v", ctx.Err())
	}

	<-done
	if runErr != nil {
		t.Fatalf("RunSequence failed: %v", runErr)
	}

	if atomic.LoadInt32(&maxConcurrent) != 2 {
		t.Fatalf("max concurrent steps = %d, want 2", atomic.LoadInt32(&maxConcurrent))
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 step start events, got %v", events)
	}
	firstTwo := map[string]struct{}{got[0]: {}, got[1]: {}}
	if _, ok := firstTwo["step-a"]; !ok {
		t.Fatalf("step-a was not started in the first wave: %v", got)
	}
	if _, ok := firstTwo["step-b"]; !ok {
		t.Fatalf("step-b was not started in the first wave: %v", got)
	}
	if got[2] != "step-c" {
		t.Fatalf("dependent step started out of order: %v", got)
	}

	if result == nil {
		t.Fatalf("expected sequence result")
	}
	for _, step := range result.Steps {
		if step.Status != sequenceStatusComplete {
			t.Fatalf("step %s status = %s, want complete", step.ID, step.Status)
		}
	}
}

func TestRunSequenceRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retry.yaml")

	seq := &Sequence{
		Name:             "retry",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureRetry,
		MaxRetries:       1,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls int32
	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", fmt.Errorf("transient failure")
		}
		return "ok", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Interactive: false,
		StepRunner:  stepRunner,
	})
	if err != nil {
		t.Fatalf("RunSequence returned error: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("step runner calls = %d, want 2", atomic.LoadInt32(&calls))
	}
	if result == nil || len(result.Steps) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Steps[0].Status != sequenceStatusComplete {
		t.Fatalf("step status = %s, want complete", result.Steps[0].Status)
	}
}

func TestCreateShadowWorktreeUsesBranch(t *testing.T) {
	repo := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (output: %s)", args, err, strings.TrimSpace(string(output)))
		}
	}

	seed := filepath.Join(repo, "seed.txt")
	if err := os.WriteFile(seed, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "add", "seed.txt"); err != nil {
		t.Fatalf("stage seed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	branch := "feature/sequence-worktree-test"
	shadowPath, err := createShadowWorktree(repo, branch)
	if err != nil {
		t.Fatalf("createShadowWorktree failed: %v", err)
	}
	defer removeShadowWorktree(repo, shadowPath)

	cmd := exec.Command("git", "-C", shadowPath, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse failed: %v (output: %s)", err, strings.TrimSpace(string(output)))
	}
	if got := strings.TrimSpace(string(output)); got != branch {
		t.Fatalf("shadow worktree HEAD = %q, want %q", got, branch)
	}
}

func TestIsConductorStep(t *testing.T) {
	tests := []struct {
		name   string
		stepID string
		want   bool
	}{
		{name: "exact", stepID: "conductor", want: true},
		{name: "prefixed", stepID: "Conductor-plan", want: true},
		{name: "worker", stepID: "worker-plan", want: false},
		{name: "embedded", stepID: "worker-conductor", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConductorStep(tt.stepID); got != tt.want {
				t.Fatalf("isConductorStep(%q) = %v, want %v", tt.stepID, got, tt.want)
			}
		})
	}
}
