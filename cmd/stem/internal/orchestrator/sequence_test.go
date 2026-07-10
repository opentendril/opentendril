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

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/terrarium"
	"github.com/opentendril/core/roots/llm"
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

type sequenceCommandResultError struct {
	err    error
	result terrarium.CommandResult
}

func (e sequenceCommandResultError) Error() string {
	return e.err.Error()
}

func (e sequenceCommandResultError) Unwrap() error {
	return e.err
}

func (e sequenceCommandResultError) CommandResult() terrarium.CommandResult {
	return e.result
}

func TestRunSequencePublishesFailureEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "failure-events.yaml")

	seq := &Sequence{
		Name:             "failure-events",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	bus := eventbus.New()
	stepErr := sequenceCommandResultError{
		err: fmt.Errorf("killed"),
		result: terrarium.CommandResult{
			ExitCode: 137,
			TimedOut: true,
		},
	}

	_, err := RunSequence(context.Background(), path, SequenceRunOptions{
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		EventBus: bus,
		StepRunner: func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
			return "", stepErr
		},
	})
	if err == nil {
		t.Fatal("RunSequence returned nil error, want failure")
	}

	history := bus.History(10)
	if len(history) != 3 {
		t.Fatalf("event count = %d, want 3", len(history))
	}
	wantTypes := []eventbus.EventType{
		eventbus.EventTerrariumOOM,
		eventbus.EventTerrariumTimeout,
		eventbus.EventSequenceFailure,
	}
	for i, want := range wantTypes {
		if history[i].Type != want {
			t.Fatalf("event %d type = %q, want %q", i, history[i].Type, want)
		}
		if history[i].Data["stepId"] != "step-a" {
			t.Fatalf("event %d stepId = %v, want step-a", i, history[i].Data["stepId"])
		}
	}
}

func TestRunSequencePublishesCompleteEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "complete-events.yaml")

	seq := &Sequence{
		Name:             "complete-events",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	bus := eventbus.New()
	_, err := RunSequence(context.Background(), path, SequenceRunOptions{
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		EventBus: bus,
		StepRunner: func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("RunSequence failed: %v", err)
	}

	history := bus.History(1)
	if len(history) != 1 {
		t.Fatalf("event count = %d, want 1", len(history))
	}
	if history[0].Type != eventbus.EventSequenceComplete {
		t.Fatalf("event type = %q, want %q", history[0].Type, eventbus.EventSequenceComplete)
	}
}

func TestRunSequenceBudsRecursiveDebuggerOnVerifierFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verifier.yaml")

	seq := &Sequence{
		Name:             "verifier-loop",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "verifier", Status: sequenceStatusPending, Transcript: "run verification"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var verifierCalls int32
	var mu sync.Mutex
	var calls []string
	debuggerStarted := make(chan string, 1)
	releaseDebugger := make(chan struct{})

	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		mu.Lock()
		calls = append(calls, step.ID)
		mu.Unlock()

		switch {
		case strings.HasPrefix(step.ID, "debugger-"):
			select {
			case debuggerStarted <- step.ID:
			default:
			}
			select {
			case <-releaseDebugger:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "patched", nil
		case step.ID == "verifier":
			if atomic.AddInt32(&verifierCalls, 1) == 1 {
				return "", fmt.Errorf("compiler failure")
			}
			return "verification passed", nil
		default:
			return "", fmt.Errorf("unexpected step %s", step.ID)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	var result *Sequence
	var runErr error
	go func() {
		defer close(done)
		result, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:      io.Discard,
			Stderr:      io.Discard,
			Interactive: false,
			StepRunner:  stepRunner,
		})
	}()

	var debuggerID string
	select {
	case debuggerID = <-debuggerStarted:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for recursive debugger sprout: %v", ctx.Err())
	}

	func() {
		defer close(releaseDebugger)

		loaded, err := LoadSequence(path)
		if err != nil {
			t.Fatalf("LoadSequence failed: %v", err)
		}
		if len(loaded.Steps) != 2 {
			t.Fatalf("loaded step count = %d, want 2", len(loaded.Steps))
		}

		verifierStep := latestStepByID(loaded.Steps, "verifier")
		if verifierStep == nil {
			t.Fatalf("verifier step missing after debugger sprout")
		}
		if verifierStep.Status != sequenceStatusPending {
			t.Fatalf("verifier status = %s, want pending", verifierStep.Status)
		}
		if len(verifierStep.DependsOn) != 1 || verifierStep.DependsOn[0] != debuggerID {
			t.Fatalf("verifier dependsOn = %#v, want [%s]", verifierStep.DependsOn, debuggerID)
		}

		debuggerStep := latestStepByID(loaded.Steps, debuggerID)
		if debuggerStep == nil {
			t.Fatalf("debugger step %s missing after sprout", debuggerID)
		}
		if debuggerStep.Status != sequenceStatusPending {
			t.Fatalf("debugger status = %s, want pending", debuggerStep.Status)
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for recursive debugger run: %v", ctx.Err())
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

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("step call count = %d, want 3", len(calls))
	}
	if calls[0] != "verifier" {
		t.Fatalf("first call = %q, want verifier", calls[0])
	}
	if !strings.HasPrefix(calls[1], "debugger-") {
		t.Fatalf("second call = %q, want recursive debugger", calls[1])
	}
	if calls[2] != "verifier" {
		t.Fatalf("third call = %q, want verifier retry", calls[2])
	}
	if atomic.LoadInt32(&verifierCalls) != 2 {
		t.Fatalf("verifier call count = %d, want 2", atomic.LoadInt32(&verifierCalls))
	}
}

// TestRunSequenceBudsRecursiveDebuggerOnMacrophageFuzzFailure is the
// Symbiotic Immune System's (issue #154) end-to-end orchestration proof:
// simulate a Worker having generated a function with a panic condition — the
// stand-in "macrophage" step here plays the role runMacrophageFuzzCheck
// would in production, returning a macrophageFuzzError the first time it
// runs (as if the fuzzer found the crash) — and assert the sequence sprouts
// a recursive Debugger to patch it, then retries and succeeds, exactly like
// a Verifier compiler/test failure does today. No Docker/Go toolchain
// involved: this proves the DAG retry/reject wiring
// (shouldBudRecursiveDebugger's new "macrophage" branch), which is the part
// that actually decides whether a crash blocks the merge.
func TestRunSequenceBudsRecursiveDebuggerOnMacrophageFuzzFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "macrophage.yaml")

	seq := &Sequence{
		Name:             "macrophage-loop",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "macrophage", Status: sequenceStatusPending, Transcript: "fuzz the recently generated code"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var macrophageCalls int32
	var mu sync.Mutex
	var calls []string
	debuggerStarted := make(chan string, 1)
	releaseDebugger := make(chan struct{})

	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		mu.Lock()
		calls = append(calls, step.ID)
		mu.Unlock()

		switch {
		case strings.HasPrefix(step.ID, "debugger-"):
			select {
			case debuggerStarted <- step.ID:
			default:
			}
			select {
			case <-releaseDebugger:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "patched the panic condition", nil
		case step.ID == "macrophage":
			if atomic.AddInt32(&macrophageCalls, 1) == 1 {
				// Stand-in for runMacrophageFuzzCheck finding a crash: same
				// hard error type, same failure shape a real fuzz run would
				// produce (issue #154 task 5's simulated scenario).
				return "", &macrophageFuzzError{
					summary: "fuzzer triggered a panic:\npanic: runtime error: division by zero",
					result:  terrarium.CommandResult{ExitCode: 2},
				}
			}
			return "fuzz verification passed", nil
		default:
			return "", fmt.Errorf("unexpected step %s", step.ID)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	var result *Sequence
	var runErr error
	go func() {
		defer close(done)
		result, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:      io.Discard,
			Stderr:      io.Discard,
			Interactive: false,
			StepRunner:  stepRunner,
		})
	}()

	var debuggerID string
	select {
	case debuggerID = <-debuggerStarted:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for recursive debugger sprout: %v", ctx.Err())
	}

	func() {
		defer close(releaseDebugger)

		loaded, err := LoadSequence(path)
		if err != nil {
			t.Fatalf("LoadSequence failed: %v", err)
		}
		if len(loaded.Steps) != 2 {
			t.Fatalf("loaded step count = %d, want 2", len(loaded.Steps))
		}

		macrophageStep := latestStepByID(loaded.Steps, "macrophage")
		if macrophageStep == nil {
			t.Fatalf("macrophage step missing after debugger sprout")
		}
		if macrophageStep.Status != sequenceStatusPending {
			t.Fatalf("macrophage status = %s, want pending (the crashing merge must not be accepted yet)", macrophageStep.Status)
		}
		if len(macrophageStep.DependsOn) != 1 || macrophageStep.DependsOn[0] != debuggerID {
			t.Fatalf("macrophage dependsOn = %#v, want [%s]", macrophageStep.DependsOn, debuggerID)
		}

		debuggerStep := latestStepByID(loaded.Steps, debuggerID)
		if debuggerStep == nil {
			t.Fatalf("debugger step %s missing after sprout", debuggerID)
		}
		if debuggerStep.Status != sequenceStatusPending {
			t.Fatalf("debugger status = %s, want pending", debuggerStep.Status)
		}
		if !strings.Contains(debuggerStep.Transcript, "panic: runtime error: division by zero") {
			t.Fatalf("debugger transcript = %q, want it to carry the fuzz crash detail so it can actually fix it", debuggerStep.Transcript)
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for recursive debugger run: %v", ctx.Err())
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

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("step call count = %d, want 3", len(calls))
	}
	if calls[0] != "macrophage" {
		t.Fatalf("first call = %q, want macrophage", calls[0])
	}
	if !strings.HasPrefix(calls[1], "debugger-") {
		t.Fatalf("second call = %q, want recursive debugger", calls[1])
	}
	if calls[2] != "macrophage" {
		t.Fatalf("third call = %q, want macrophage retry (re-fuzzing the patched code)", calls[2])
	}
	if atomic.LoadInt32(&macrophageCalls) != 2 {
		t.Fatalf("macrophage call count = %d, want 2", atomic.LoadInt32(&macrophageCalls))
	}
}

// TestShouldBudRecursiveDebuggerCoversMacrophage locks in the specific
// substring-matching contract shouldBudRecursiveDebugger relies on: a step
// whose ID merely contains "macrophage" gets the recursive-debugger retry
// loop, exactly like "verifier", and the existing 3-generation debugger cap
// still applies to it.
func TestShouldBudRecursiveDebuggerCoversMacrophage(t *testing.T) {
	cases := []struct {
		name string
		step *SequenceStep
		want bool
	}{
		{"plain macrophage step", &SequenceStep{ID: "macrophage"}, true},
		{"namespaced macrophage step", &SequenceStep{ID: "macrophage-fuzz-1"}, true},
		{"unrelated step", &SequenceStep{ID: "worker"}, false},
		{
			"debugger cap already exhausted",
			&SequenceStep{ID: "macrophage-debugger-debugger-debugger"},
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldBudRecursiveDebugger(tc.step); got != tc.want {
				t.Fatalf("shouldBudRecursiveDebugger(%q) = %v, want %v", tc.step.ID, got, tc.want)
			}
		})
	}
}

func TestRunSequenceAppendsDynamicSteps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dynamic.yaml")

	seq := &Sequence{
		Name:             "dynamic",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "meristem", Transcript: "design the next steps"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls []string
	var mu sync.Mutex
	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		mu.Lock()
		calls = append(calls, step.ID)
		mu.Unlock()

		switch step.ID {
		case "meristem":
			return "```json\n[{\"id\":\"step-a\",\"dependsOn\":[\"meristem\"],\"transcript\":\"do the first thing\"},{\"id\":\"step-b\",\"dependsOn\":[\"step-a\"],\"transcript\":\"do the second thing\"}]\n```", nil
		case "step-a":
			return "alpha", nil
		case "step-b":
			return "beta", nil
		default:
			return "", fmt.Errorf("unexpected step %s", step.ID)
		}
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
		t.Fatalf("RunSequence failed: %v", err)
	}
	if result == nil {
		t.Fatalf("expected sequence result")
	}
	if len(result.Steps) != 3 {
		t.Fatalf("result step count = %d, want 3", len(result.Steps))
	}
	for _, step := range result.Steps {
		if step.Status != sequenceStatusComplete {
			t.Fatalf("step %s status = %s, want complete", step.ID, step.Status)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("step call count = %d, want 3", len(calls))
	}
	if calls[0] != "meristem" || calls[1] != "step-a" || calls[2] != "step-b" {
		t.Fatalf("unexpected step call order: %v", calls)
	}

	loaded, err := LoadSequence(path)
	if err != nil {
		t.Fatalf("LoadSequence failed: %v", err)
	}
	if len(loaded.Steps) != 3 {
		t.Fatalf("persisted step count = %d, want 3", len(loaded.Steps))
	}
	if loaded.Steps[1].ID != "step-a" || loaded.Steps[2].ID != "step-b" {
		t.Fatalf("persisted dynamic steps out of order: %#v", loaded.Steps)
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

func TestIsMeristemStep(t *testing.T) {
	tests := []struct {
		name   string
		stepID string
		want   bool
	}{
		{name: "exact", stepID: "meristem", want: true},
		{name: "prefixed", stepID: "Meristem-plan", want: true},
		{name: "worker", stepID: "worker-plan", want: false},
		{name: "embedded", stepID: "worker-meristem", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMeristemStep(tt.stepID); got != tt.want {
				t.Fatalf("isMeristemStep(%q) = %v, want %v", tt.stepID, got, tt.want)
			}
		})
	}
}

func TestFallbackStepModelTier(t *testing.T) {
	tests := []struct {
		name   string
		stepID string
		want   llm.ModelTier
	}{
		{name: "meristem", stepID: "meristem", want: llm.TierPremium},
		{name: "worker", stepID: "worker-sprout", want: llm.TierPremium},
		{name: "verifier", stepID: "verifier-check", want: llm.TierStandard},
		{name: "debugger", stepID: "recursive-debugger", want: llm.TierStandard},
		{name: "compiler", stepID: "compiler-check", want: llm.TierStandard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fallbackStepModelTier(tt.stepID); got != tt.want {
				t.Fatalf("fallbackStepModelTier(%q) = %q, want %q", tt.stepID, got, tt.want)
			}
		})
	}
}
