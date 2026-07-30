package conductor

import (
	"bytes"
	"context"
	"errors"
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

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
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
// Symbiotic Immune System's end-to-end orchestration proof:
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
				// produce (task 5's simulated scenario).
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

func TestRunSequenceSproutFailClosedIsolation(t *testing.T) {
	origCreateShadowWorktreeFn := createShadowWorktreeFn
	defer func() {
		createShadowWorktreeFn = origCreateShadowWorktreeFn
	}()

	workdir := t.TempDir()
	runGitCommand(context.Background(), workdir, "init")
	runGitCommand(context.Background(), workdir, "commit", "--allow-empty", "-m", "init")

	tests := []struct {
		name      string
		allowHost bool
		wantErr   bool
	}{
		{
			name:      "unset fails closed",
			allowHost: false,
			wantErr:   true,
		},
		{
			name:      "opt-in proceeds on host workspace",
			allowHost: true,
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.allowHost {
				t.Setenv(EnvAllowHostWorkspace, "true")
			} else {
				os.Unsetenv(EnvAllowHostWorkspace)
			}

			createShadowWorktreeFn = func(sourcePath, cloneBranch string) (string, error) {
				return "", fmt.Errorf("simulated sequence isolation failure")
			}

			origRunSequenceSproutAtPathFn := runSequenceSproutAtPathFn
			defer func() {
				runSequenceSproutAtPathFn = origRunSequenceSproutAtPathFn
			}()

			runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
				if tc.allowHost && mountPath != sourcePath {
					t.Errorf("sprout mountPath = %q, want %q (host workspace)", mountPath, sourcePath)
				}
				return sproutExecutionResult{Response: "success", Outcome: SproutOutcomeComplete}, nil
			}

			orch := NewDockerOrchestrator()
			orch.Substrate = workdir
			orch.StepID = "seq-test-step"
			orch.DisableMergeBack = true // simplify testing

			bus := eventbus.New()
			var eventCount int
			bus.Subscribe(eventbus.EventHostExecutionActivated, func(e eventbus.Event) {
				if e.Data["workspace"] == workdir {
					eventCount++
				}
			})
			orch.EventBus = bus

			_, err := runSequenceSprout(context.Background(), orch, "test prompt")

			if tc.allowHost && eventCount != 1 {
				t.Errorf("expected 1 host workspace event, got %d", eventCount)
			} else if !tc.allowHost && eventCount != 0 {
				t.Errorf("expected 0 host workspace events, got %d", eventCount)
			}

			if tc.wantErr {
				if err == nil {
					t.Errorf("expected runSequenceSprout to fail closed, got nil error")
				} else if !strings.Contains(err.Error(), "isolation could not be established") {
					t.Errorf("expected isolation error, got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("expected runSequenceSprout to proceed, got error: %v", err)
				}
			}
		})
	}
}

func TestAppendDynamicStepsDependencyCycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cycle.yaml")
	seq := &Sequence{
		Name: "cycle-test",
		Steps: []SequenceStep{
			{ID: "static-1", Status: sequenceStatusComplete},
		},
	}
	SaveSequence(path, seq)

	runner, err := newSequenceRunner(path, seq, SequenceRunOptions{})
	if err != nil {
		t.Fatalf("newSequenceRunner failed: %v", err)
	}

	stepsBefore := len(runner.seq.Steps)
	readyBefore := len(runner.ready)
	queuedBefore := len(runner.queued)
	remDepsBefore := len(runner.remainingDeps)
	depBefore := len(runner.dependents)
	mapBefore := len(runner.stepByID)

	dynamic := []SequenceStep{
		{ID: "a", DependsOn: []string{"b"}, Transcript: "a"},
		{ID: "b", DependsOn: []string{"a"}, Transcript: "b"},
	}

	err = runner.appendDynamicSteps(dynamic)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}

	// The reported path is deterministic: traversal starts from the first
	// declared step, so accepting either orientation would let a regression to
	// map-order iteration pass unnoticed.
	if !strings.Contains(err.Error(), "forms a dependency cycle: a -> b -> a") {
		t.Errorf("error did not name cycle correctly: %v", err)
	}

	if len(runner.seq.Steps) != stepsBefore {
		t.Errorf("Steps mutated: got %d, want %d", len(runner.seq.Steps), stepsBefore)
	}
	if len(runner.ready) != readyBefore {
		t.Errorf("ready mutated: got %d, want %d", len(runner.ready), readyBefore)
	}
	if len(runner.queued) != queuedBefore {
		t.Errorf("queued mutated: got %d, want %d", len(runner.queued), queuedBefore)
	}
	if len(runner.remainingDeps) != remDepsBefore {
		t.Errorf("remainingDeps mutated: got %d, want %d", len(runner.remainingDeps), remDepsBefore)
	}
	if len(runner.dependents) != depBefore {
		t.Errorf("dependents mutated: got %d, want %d", len(runner.dependents), depBefore)
	}
	if len(runner.stepByID) != mapBefore {
		t.Errorf("stepByID mutated: got %d, want %d", len(runner.stepByID), mapBefore)
	}
}

func TestAppendDynamicStepsForwardReferences(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fwd.yaml")
	seq := &Sequence{
		Name: "fwd-test",
		Steps: []SequenceStep{
			{ID: "static-1", Status: sequenceStatusComplete},
		},
	}
	SaveSequence(path, seq)

	runner, err := newSequenceRunner(path, seq, SequenceRunOptions{})
	if err != nil {
		t.Fatalf("newSequenceRunner failed: %v", err)
	}

	dynamic := []SequenceStep{
		{ID: "a", DependsOn: []string{"b"}, Transcript: "a"},
		{ID: "b", Transcript: "b"},
	}

	if err := runner.appendDynamicSteps(dynamic); err != nil {
		t.Fatalf("unexpected error on valid forward reference: %v", err)
	}

	if runner.remainingDeps["a"] != 1 {
		t.Errorf("expected step a to have 1 remaining dep, got %d", runner.remainingDeps["a"])
	}
	if runner.remainingDeps["b"] != 0 {
		t.Errorf("expected step b to have 0 remaining deps, got %d", runner.remainingDeps["b"])
	}
	if len(runner.ready) != 1 || runner.ready[0] != "b" {
		t.Errorf("expected only step b to be ready, got %v", runner.ready)
	}
}

func TestDependencyCyclePathFormat(t *testing.T) {
	steps := []SequenceStep{
		{ID: "a", DependsOn: []string{"b"}, Transcript: "a"},
		{ID: "b", DependsOn: []string{"c"}, Transcript: "b"},
		{ID: "c", DependsOn: []string{"a"}, Transcript: "c"},
	}

	cycle := findDependencyCycle(steps)
	if cycle == nil {
		t.Fatal("expected to find cycle, got nil")
	}

	want := "a -> b -> c -> a"
	got := strings.Join(cycle, " -> ")
	if got != want {
		t.Errorf("cycle path = %q, want %q", got, want)
	}
}

func TestAppendDynamicStepsExistingErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.yaml")
	seq := &Sequence{Name: "errors", Steps: []SequenceStep{{ID: "s1", Status: sequenceStatusComplete}}}
	SaveSequence(path, seq)
	runner, _ := newSequenceRunner(path, seq, SequenceRunOptions{})

	errSelf := runner.appendDynamicSteps([]SequenceStep{
		{ID: "a", DependsOn: []string{"a"}, Transcript: "a"},
	})
	wantSelf := `dynamic sequence step a cannot depend on itself`
	if errSelf == nil || errSelf.Error() != wantSelf {
		t.Errorf("self dependency error = %v, want %q", errSelf, wantSelf)
	}

	errUnknown := runner.appendDynamicSteps([]SequenceStep{
		{ID: "a", DependsOn: []string{"missing"}, Transcript: "a"},
	})
	wantUnknown := `dynamic sequence step a depends on unknown step "missing"`
	if errUnknown == nil || errUnknown.Error() != wantUnknown {
		t.Errorf("unknown dependency error = %v, want %q", errUnknown, wantUnknown)
	}
}

func TestNewSequenceRunnerCycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "static-cycle.yaml")
	seq := &Sequence{
		Name: "static-cycle",
		Steps: []SequenceStep{
			{ID: "x", DependsOn: []string{"y"}},
			{ID: "y", DependsOn: []string{"x"}},
		},
	}
	SaveSequence(path, seq)

	_, err := newSequenceRunner(path, seq, SequenceRunOptions{})
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "forms a dependency cycle: x -> y -> x") {
		t.Errorf("error did not name cycle correctly: %v", err)
	}
}

func TestRunSequenceMeristemCycleFailsRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meristem-cycle.yaml")
	seq := &Sequence{
		Name: "meristem-cycle",
		Steps: []SequenceStep{
			{ID: "meristem-plan", Status: sequenceStatusPending, Transcript: "plan"},
		},
	}
	SaveSequence(path, seq)

	var stderr strings.Builder
	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "```json\n[{\"id\":\"m1\",\"dependsOn\":[\"m2\"],\"transcript\":\"m1\"},{\"id\":\"m2\",\"dependsOn\":[\"m1\"],\"transcript\":\"m2\"}]\n```", nil
	}

	result, err := RunSequence(context.Background(), path, SequenceRunOptions{
		Stdout:      io.Discard,
		Stderr:      &stderr,
		Interactive: false,
		StepRunner:  stepRunner,
	})

	if err == nil {
		t.Fatal("expected sequence to fail on rejected meristem plan, got nil error")
	}

	if !strings.Contains(err.Error(), "forms a dependency cycle: m1 -> m2 -> m1") {
		t.Errorf("error did not name cycle correctly: %v", err)
	}

	if strings.Contains(stderr.String(), "Failed to append dynamic steps from") {
		t.Errorf("stderr contained the old warn-and-continue message:\n%s", stderr.String())
	}

	if result.Steps[0].Status == sequenceStatusComplete {
		t.Errorf("meristem step was marked complete despite rejection")
	}
}

// awaitStepStatus polls the sequence on disk until the named step reaches the
// wanted status, and returns the loaded sequence at that point. Pause-mode tests
// need to act only once the runner has actually reached the pause; sleeping for a
// guessed interval instead lets an assertion pass merely because nothing has
// happened yet.
func awaitStepStatus(t *testing.T, path, stepID, wantStatus string) *Sequence {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		loaded, err := LoadSequence(path)
		if err == nil && loaded != nil {
			if step := latestStepByID(loaded.Steps, stepID); step != nil && step.Status == wantStatus {
				return loaded
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("step %s did not reach status %q on disk within the deadline", stepID, wantStatus)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRunSequenceReviewPauseDoesNotRewriteAuthoredMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review-pause.yaml")

	seq := &Sequence{
		Name:             "review-pause",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureHalt, // authored as halt
		Steps: []SequenceStep{
			{ID: "review-step", Status: sequenceStatusPending, Transcript: "generate a thing"},
			{ID: "next-step", Status: sequenceStatusPending, Transcript: "fail", DependsOn: []string{"review-step"}},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var stepCalls int32
	var runErr error

	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		calls := atomic.AddInt32(&stepCalls, 1)
		if step.ID == "review-step" && calls == 1 {
			return "", ErrRequiresReview
		}
		if step.ID == "next-step" {
			return "", fmt.Errorf("ordinary failure")
		}
		return "ok", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})

	go func() {
		defer close(done)
		_, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:             io.Discard,
			Stderr:             io.Discard,
			Interactive:        false,
			StepRunner:         stepRunner,
			ResumePollInterval: 10 * time.Millisecond,
		})
	}()

	// Wait for the runner to actually reach the pause, rather than guessing at
	// how long that takes. The review branch persists the step as failed before
	// it calls handlePause, so that status appearing on disk is a real
	// observation that the pause has been entered. Without this the assertion
	// below could pass simply because nothing had happened yet — the file would
	// still say halt because the code under test had not run.
	loaded := awaitStepStatus(t, path, "review-step", sequenceStatusFailed)

	// Verify the file on disk STILL says halt
	if loaded.OnFailure != sequenceOnFailureHalt {
		t.Errorf("file on disk has OnFailure = %q, want %q. The review verdict permanently rewrote the authored policy.", loaded.OnFailure, sequenceOnFailureHalt)
	}

	// Unpause it by setting status to complete
	loaded.Steps[0].Status = sequenceStatusComplete
	if err := SaveSequence(path, loaded); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for resume")
	}

	// Verify it eventually failed at next-step, and didn't pause again
	if runErr == nil {
		t.Fatalf("expected sequence to fail at next-step, got nil error")
	}
	if !strings.Contains(runErr.Error(), "step next-step failed") {
		t.Errorf("expected failure at next-step, got %v", runErr)
	}
}

func TestRunSequenceAuthoredPauseBehaviorUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authored-pause.yaml")

	seq := &Sequence{
		Name:             "authored-pause",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause, // authored as pause
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "fail"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "", fmt.Errorf("ordinary failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	var runErr error

	go func() {
		defer close(done)
		_, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:             io.Discard,
			Stderr:             io.Discard,
			Interactive:        false,
			StepRunner:         stepRunner,
			ResumePollInterval: 10 * time.Millisecond,
		})
	}()

	// Observe the pause rather than guessing at it: the failure path persists the
	// step as failed before calling handlePause.
	loaded := awaitStepStatus(t, path, "step-a", sequenceStatusFailed)

	// Unpause by changing mode to halt
	loaded.OnFailure = sequenceOnFailureHalt
	if err := SaveSequence(path, loaded); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for resume")
	}

	if runErr == nil {
		t.Fatalf("expected sequence to fail, got nil error")
	}
	if !strings.Contains(runErr.Error(), "step step-a halted after failure") {
		t.Errorf("expected halt after failure, got %v", runErr)
	}
}

func TestRunSequenceResumePauseToRetrySeedsBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.yaml")

	seq := &Sequence{
		Name:             "pause-to-retry",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		MaxRetries:       2,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls int32
	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		count := atomic.AddInt32(&calls, 1)
		if count < 3 {
			return "", fmt.Errorf("simulated failure %d", count)
		}
		return "ok", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				loaded, err := LoadSequence(path)
				if err == nil && loaded != nil && len(loaded.Steps) > 0 {
					if loaded.Steps[0].Status == sequenceStatusFailed && loaded.OnFailure == sequenceOnFailurePause {
						loaded.OnFailure = sequenceOnFailureRetry
						SaveSequence(path, loaded)
						return
					}
				}
			}
		}
	}()

	var buf strings.Builder
	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:             io.Discard,
		Stderr:             &buf,
		Interactive:        false,
		StepRunner:         stepRunner,
		ResumePollInterval: 10 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("RunSequence failed: %v\nStderr: %s", err, buf.String())
	}

	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("step runner calls = %d, want 3", atomic.LoadInt32(&calls))
	}
}

func TestRunSequenceExhaustionReportsSpentCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exhaust.yaml")

	seq := &Sequence{
		Name:             "exhaust",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		MaxRetries:       1,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "", fmt.Errorf("persistent failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				loaded, err := LoadSequence(path)
				if err == nil && loaded != nil && len(loaded.Steps) > 0 {
					if loaded.Steps[0].Status == sequenceStatusFailed && loaded.OnFailure == sequenceOnFailurePause {
						loaded.OnFailure = sequenceOnFailureRetry
						SaveSequence(path, loaded)
						return
					}
				}
			}
		}
	}()

	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:             io.Discard,
		Stderr:             io.Discard,
		Interactive:        false,
		StepRunner:         stepRunner,
		ResumePollInterval: 10 * time.Millisecond,
	})

	if err == nil {
		t.Fatalf("expected exhaustion error, got nil")
	}

	if !strings.Contains(err.Error(), "failed after 1 retries") {
		t.Fatalf("error = %q, want it to contain 'failed after 1 retries'", err.Error())
	}
}

func TestRunSequenceExhaustionReportsSpentCountRetryMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exhaust-retry.yaml")

	seq := &Sequence{
		Name:             "exhaust-retry",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureRetry,
		MaxRetries:       3,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls int32
	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", fmt.Errorf("persistent failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:             io.Discard,
		Stderr:             io.Discard,
		Interactive:        false,
		StepRunner:         stepRunner,
		ResumePollInterval: 10 * time.Millisecond,
	})

	if err == nil {
		t.Fatalf("expected exhaustion error, got nil")
	}

	if !strings.Contains(err.Error(), "failed after 3 retries") {
		t.Fatalf("error = %q, want it to contain 'failed after 3 retries'", err.Error())
	}

	if atomic.LoadInt32(&calls) != 4 {
		t.Fatalf("expected step to run 4 times, ran %d times", calls)
	}
}

func TestRunSequenceRetryCountdownCountsDown(t *testing.T) {
	// Guards against inverting spent and remaining. The exhaustion error now
	// reports the retries spent, while this line reports what is left — and the
	// message assertions elsewhere cannot tell the two apart, because they only
	// read the error. An implementation that printed the spent count here would
	// satisfy every other test in this file.
	dir := t.TempDir()
	path := filepath.Join(dir, "countdown.yaml")

	seq := &Sequence{
		Name:             "countdown",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureRetry,
		MaxRetries:       3,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "", fmt.Errorf("persistent failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stderr strings.Builder
	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:             io.Discard,
		Stderr:             &stderr,
		Interactive:        false,
		StepRunner:         stepRunner,
		ResumePollInterval: 10 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected exhaustion error, got nil")
	}

	out := stderr.String()

	// Descending, in order. An ascending sequence would mean the countdown is
	// printing what was spent.
	wantOrder := []string{"2 retries left", "1 retries left", "0 retries left"}
	at := 0
	for _, want := range wantOrder {
		idx := strings.Index(out[at:], want)
		if idx < 0 {
			t.Fatalf("stderr missing %q in descending order; got:\n%s", want, out)
		}
		at += idx + len(want)
	}

	if got := strings.Count(out, "retries left"); got != len(wantOrder) {
		t.Errorf("countdown printed %d times, want %d; got:\n%s", got, len(wantOrder), out)
	}
}

func TestRunSequenceExhaustionDefaultRetriesWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exhaust-unset.yaml")

	seq := &Sequence{
		Name:             "exhaust-unset",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureRetry,
		MaxRetries:       0,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "", fmt.Errorf("persistent failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:             io.Discard,
		Stderr:             io.Discard,
		Interactive:        false,
		StepRunner:         stepRunner,
		ResumePollInterval: 10 * time.Millisecond,
	})

	if err == nil {
		t.Fatalf("expected exhaustion error, got nil")
	}

	if !strings.Contains(err.Error(), "failed after 3 retries") {
		t.Fatalf("error = %q, want it to contain 'failed after 3 retries'", err.Error())
	}
}

func TestSeedRetryBudgetsPreservesExistingEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed-budgets.yaml")

	seq := &Sequence{
		Name:       "seed-budgets",
		OnFailure:  sequenceOnFailureRetry,
		MaxRetries: 2,
		Steps: []SequenceStep{
			{ID: "partly-spent", Status: sequenceStatusPending, Transcript: "a"},
			{ID: "exhausted", Status: sequenceStatusPending, Transcript: "b"},
			{ID: "fresh", Status: sequenceStatusPending, Transcript: "c"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	runner, err := newSequenceRunner(path, seq, SequenceRunOptions{})
	if err != nil {
		t.Fatalf("newSequenceRunner failed: %v", err)
	}

	// Construction already seeded every step because the mode is retry. Spend
	// some of two budgets, then drop the third entry entirely to stand in for a
	// step that has never been seeded.
	runner.retriesLeft["partly-spent"] = 1
	runner.retriesLeft["exhausted"] = 0
	delete(runner.retriesLeft, "fresh")

	runner.seedRetryBudgets()

	// A budget of 0 is the case that matters: the step legitimately spent its
	// whole allowance, and re-seeding it would turn a bounded retry into an
	// unbounded one. Presence, not value, has to decide.
	if got := runner.retriesLeft["exhausted"]; got != 0 {
		t.Errorf("exhausted budget was re-seeded to %d, want it left at 0", got)
	}
	if got := runner.retriesLeft["partly-spent"]; got != 1 {
		t.Errorf("partly spent budget = %d, want 1 (unchanged)", got)
	}
	if got := runner.retriesLeft["fresh"]; got != 2 {
		t.Errorf("unseeded step budget = %d, want 2", got)
	}
}

func TestSeedRetryBudgetsIsNoOpOutsideRetryMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed-budgets-halt.yaml")

	seq := &Sequence{
		Name:       "seed-budgets-halt",
		OnFailure:  sequenceOnFailureHalt,
		MaxRetries: 2,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	runner, err := newSequenceRunner(path, seq, SequenceRunOptions{})
	if err != nil {
		t.Fatalf("newSequenceRunner failed: %v", err)
	}

	runner.seedRetryBudgets()

	if _, seeded := runner.retriesLeft["step-a"]; seeded {
		t.Errorf("budget was seeded under mode %q, want no entry", seq.OnFailure)
	}
}

func TestRunSequenceRetriesThroughFullBudgetAfterResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retry-cycle.yaml")

	seq := &Sequence{
		Name:             "retry-cycle",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		MaxRetries:       2,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "fail"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var stepACalls int32
	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		atomic.AddInt32(&stepACalls, 1)
		return "", fmt.Errorf("persistent failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		loaded := awaitStepStatus(t, path, "step-a", sequenceStatusFailed)
		loaded.OnFailure = sequenceOnFailureRetry
		if err := SaveSequence(path, loaded); err != nil {
			t.Errorf("SaveSequence failed: %v", err)
		}
	}()

	var buf strings.Builder
	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:             io.Discard,
		Stderr:             &buf,
		Interactive:        false,
		StepRunner:         stepRunner,
		ResumePollInterval: 10 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected the sequence to halt once the budget was spent, got nil")
	}

	// One initial failure that triggers the pause, then two retries funded by
	// MaxRetries, then the failure that exhausts the budget.
	if calls := atomic.LoadInt32(&stepACalls); calls != 4 {
		t.Fatalf("step ran %d times, want 4 (1 pause + 2 funded retries + 1 exhausting failure); Stderr: %s", calls, buf.String())
	}
}

// lockedBuffer is a Stderr sink that can be read while the runner is still
// writing to it. The pause tests need that: they have to observe one warning
// before making the next edit, because two edits collapsing into a single poll
// would produce one combined warning instead of two.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// awaitStderr waits until want appears in the sink, so an edit is known to have
// been polled before the next one is written.
func awaitStderr(t *testing.T, sink *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(sink.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stderr never contained %q within the deadline; got:\n%s", want, sink.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRunSequenceHeadlessPauseWarningContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "warning-contract.yaml")

	originalSeq := &Sequence{
		Name:             "warning-contract",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		MaxRetries:       0,
		Steps: []SequenceStep{
			{ID: "step-1", Status: sequenceStatusPending, Transcript: "initial"},
			{ID: "step-2", Status: sequenceStatusPending, Transcript: "untouched"},
		},
	}
	if err := SaveSequence(path, originalSeq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls int32
	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", fmt.Errorf("initial failure to trigger pause")
		}
		return "success", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf strings.Builder

	done := make(chan struct{})
	var result *Sequence
	var runErr error
	go func() {
		defer close(done)
		result, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:             io.Discard,
			Stderr:             &buf,
			Interactive:        false,
			StepRunner:         stepRunner,
			ResumePollInterval: 10 * time.Millisecond,
		})
	}()

	// Wait for runner to pause
	_ = awaitStepStatus(t, path, "step-1", sequenceStatusFailed)

	// The resume interval is short (10ms). Wait for many ticks to elapse to ensure
	// the false-positive guard holds (no warnings emitted when only honoured fields change,
	// or when nothing has changed).
	time.Sleep(100 * time.Millisecond)

	// Now introduce an unhonoured edit along with a mode change, simulating a single save.
	loaded, err := LoadSequence(path)
	if err != nil {
		t.Fatalf("LoadSequence failed: %v", err)
	}

	// Unhonoured edit
	loaded.Steps[1].Transcript = "operator discarded edit"
	// Honoured edit (trigger resume)
	loaded.OnFailure = sequenceOnFailureHalt

	if err := SaveSequence(path, loaded); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for resume: %v", ctx.Err())
	}

	if runErr == nil || !strings.Contains(runErr.Error(), "halted after failure") {
		t.Fatalf("expected RunSequence to fail with halt, got %v", runErr)
	}

	// Assert the resume worked
	if result.OnFailure != sequenceOnFailureHalt {
		t.Fatalf("expected resumed sequence to have onFailure %q, got %q", sequenceOnFailureHalt, result.OnFailure)
	}

	// The unrelated edit is discarded in the result
	if result.Steps[1].Transcript != "untouched" {
		t.Fatalf("expected unrelated edit to be discarded, got %q", result.Steps[1].Transcript)
	}

	output := buf.String()

	// Assert the warning fired exactly once for the distinct edit
	warningCount := strings.Count(output, "⚠️ Ignored edits to paused sequence: step[step-2].transcript.")
	if warningCount != 1 {
		t.Fatalf("expected exactly 1 warning about step-2.transcript, got %d. Output:\n%s", warningCount, output)
	}

	// Verify no other warnings (the false-positive guard)
	totalWarnings := strings.Count(output, "⚠️ Ignored edits to paused sequence")
	if totalWarnings != 1 {
		t.Fatalf("expected exactly 1 total warning about ignored edits, got %d. Output:\n%s", totalWarnings, output)
	}
}

func TestRunSequenceHeadlessPauseMultipleWarnings(t *testing.T) {
	// Tests that multiple distinct edits across separate saves warn correctly,
	// but the same unchanged difference does not re-report.
	dir := t.TempDir()
	path := filepath.Join(dir, "multi-warning.yaml")

	originalSeq := &Sequence{
		Name:             "multi-warning",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		MaxRetries:       0,
		Steps: []SequenceStep{
			{ID: "step-1", Status: sequenceStatusPending, Transcript: "initial"},
		},
	}
	if err := SaveSequence(path, originalSeq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls int32
	stepRunner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substratePath string) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", fmt.Errorf("initial failure to trigger pause")
		}
		return "success", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf lockedBuffer

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:             io.Discard,
			Stderr:             &buf,
			Interactive:        false,
			StepRunner:         stepRunner,
			ResumePollInterval: 10 * time.Millisecond,
		})
	}()

	_ = awaitStepStatus(t, path, "step-1", sequenceStatusFailed)

	// Edit 1: change maxRetries
	loaded, _ := LoadSequence(path)
	loaded.MaxRetries = 5
	_ = SaveSequence(path, loaded)

	// Wait for the edit to actually be polled before writing the next one. If
	// both landed in one poll the runner would emit a single combined warning
	// and the count below would be wrong for a reason unrelated to the code.
	awaitStderr(t, &buf, "Ignored edits to paused sequence: maxRetries.")

	// Dwell so that several further polls see the same unchanged difference.
	// This widens a detection window rather than synchronising anything: too
	// short only makes the not-once-per-tick assertion weaker, never wrong.
	time.Sleep(60 * time.Millisecond)

	// Edit 2: change branch, leaving maxRetries changed too
	loaded, _ = LoadSequence(path)
	loaded.Branch = "new-branch"
	_ = SaveSequence(path, loaded)

	awaitStderr(t, &buf, "Ignored edits to paused sequence: branch, maxRetries.")

	// Resume via step status
	loaded, _ = LoadSequence(path)
	loaded.Steps[0].Status = sequenceStatusPending
	_ = SaveSequence(path, loaded)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for resume: %v", ctx.Err())
	}

	output := buf.String()

	// Should see a warning for maxRetries, then a warning for branch, maxRetries
	if !strings.Contains(output, "⚠️ Ignored edits to paused sequence: maxRetries.") {
		t.Errorf("missing first warning. Output:\n%s", output)
	}
	if !strings.Contains(output, "⚠️ Ignored edits to paused sequence: branch, maxRetries.") {
		t.Errorf("missing second warning. Output:\n%s", output)
	}
	totalWarnings := strings.Count(output, "⚠️ Ignored edits to paused sequence")
	if totalWarnings != 2 {
		t.Fatalf("expected exactly 2 total warnings, got %d. Output:\n%s", totalWarnings, output)
	}
}

func TestRunSequenceHeadlessPauseDoesNotWarnOnHonouredEdits(t *testing.T) {
	// The false-positive guard. Changing only an honoured field must produce no
	// warning at all -- if the comparison included onFailure, or reported a
	// difference created by the runner's own save, every ordinary resume would
	// warn. The resume itself proves a poll read the file, so this cannot pass
	// by never looking.
	dir := t.TempDir()
	path := filepath.Join(dir, "no-false-warning.yaml")

	seq := &Sequence{
		Name:             "no-false-warning",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		Steps: []SequenceStep{
			{ID: "step-1", Status: sequenceStatusPending, Transcript: "initial"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls int32
	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", fmt.Errorf("initial failure to trigger pause")
		}
		return "success", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf lockedBuffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:             io.Discard,
			Stderr:             &buf,
			Interactive:        false,
			StepRunner:         stepRunner,
			ResumePollInterval: 10 * time.Millisecond,
		})
	}()

	_ = awaitStepStatus(t, path, "step-1", sequenceStatusFailed)

	// Let several polls run against the runner's own saved file before touching
	// it, so a comparison that reported spurious differences would have shown up.
	time.Sleep(60 * time.Millisecond)

	loaded, err := LoadSequence(path)
	if err != nil {
		t.Fatalf("LoadSequence failed: %v", err)
	}
	loaded.OnFailure = sequenceOnFailureHalt
	if err := SaveSequence(path, loaded); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for resume: %v", ctx.Err())
	}

	if got := strings.Count(buf.String(), "Ignored edits to paused sequence"); got != 0 {
		t.Errorf("expected no ignored-edit warning, got %d:\n%s", got, buf.String())
	}
}

func TestRunSequenceInteractiveClosedStdinHalts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "interactive-closed.yaml")

	seq := &Sequence{
		Name:             "interactive-closed",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		Steps: []SequenceStep{
			{ID: "step-1", Status: sequenceStatusPending, Transcript: "fail"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "", fmt.Errorf("persistent failure")
	}

	pr, pw := io.Pipe()
	pw.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var buf strings.Builder
	result, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:      io.Discard,
		Stderr:      &buf,
		Interactive: true,
		Stdin:       pr,
		StepRunner:  stepRunner,
	})

	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "halted after failure") {
		t.Fatalf("expected sequence to halt due to closed stdin, got: %v", err)
	}
	if result != nil && result.Steps[0].Status != sequenceStatusFailed {
		t.Errorf("expected step to remain failed, got %s", result.Steps[0].Status)
	}
}

func TestRunSequenceInteractiveEmptyLineRetries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "interactive-empty.yaml")

	seq := &Sequence{
		Name:             "interactive-empty",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		Steps: []SequenceStep{
			{ID: "step-1", Status: sequenceStatusPending, Transcript: "fail"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	var calls int32
	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", fmt.Errorf("first failure")
		}
		return "success", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdin := strings.NewReader("\n")

	var buf strings.Builder
	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:      io.Discard,
		Stderr:      &buf,
		Interactive: true,
		Stdin:       stdin,
		StepRunner:  stepRunner,
	})

	if err != nil {
		t.Fatalf("expected success on retry, got error: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", atomic.LoadInt32(&calls))
	}
}

func TestRunSequenceInteractiveDataAtEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "interactive-eof-data.yaml")

	seq := &Sequence{
		Name:             "interactive-eof-data",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		Steps: []SequenceStep{
			{ID: "step-1", Status: sequenceStatusPending, Transcript: "fail"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "", fmt.Errorf("persistent failure")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdin := strings.NewReader("h")

	var buf strings.Builder
	_, err := RunSequence(ctx, path, SequenceRunOptions{
		Stdout:      io.Discard,
		Stderr:      &buf,
		Interactive: true,
		Stdin:       stdin,
		StepRunner:  stepRunner,
	})

	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "halted after failure") {
		t.Fatalf("expected sequence to halt due to 'h' at EOF, got: %v", err)
	}
}

func TestRunSequenceInteractiveCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "interactive-cancel.yaml")

	seq := &Sequence{
		Name:             "interactive-cancel",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailurePause,
		Steps: []SequenceStep{
			{ID: "step-1", Status: sequenceStatusPending, Transcript: "fail"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		return "", fmt.Errorf("persistent failure")
	}

	pr, pw := io.Pipe()
	defer pw.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var buf lockedBuffer
	done := make(chan struct{})
	var runErr error

	go func() {
		defer close(done)
		_, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:      io.Discard,
			Stderr:      &buf,
			Interactive: true,
			Stdin:       pr,
			StepRunner:  stepRunner,
		})
	}()

	awaitStderr(t, &buf, "⚠️ Step step-1 failed. [R]etry or [H]alt?")
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for RunSequence to return on cancellation")
	}

	if runErr == nil {
		t.Fatalf("expected context cancellation error, got nil")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", runErr)
	}
}
