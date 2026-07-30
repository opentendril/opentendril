package conductor

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

func createTestSequenceDrain(t *testing.T) (string, *Sequence) {
	t.Helper()
	seq := &Sequence{
		Name:             "drain-test",
		ConcurrencyLimit: 2,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "stepA", Transcript: "A"},
			{ID: "stepB", Transcript: "B"},
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.yaml")
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("failed to save sequence: %v", err)
	}
	return path, seq
}

func TestRunSequence_DrainWaitsForInFlight(t *testing.T) {
	path, _ := createTestSequenceDrain(t)
	var stderr bytes.Buffer
	bus := eventbus.New()
	defer bus.Shutdown()

	bStarted := make(chan struct{})
	bRelease := make(chan struct{})
	aFailed := make(chan struct{})

	bus.Subscribe(eventbus.EventSequenceFailure, func(e eventbus.Event) {
		if stepID, ok := e.Data["stepId"].(string); ok && stepID == "stepA" {
			close(aFailed)
		}
	})

	var bFlag atomic.Bool
	errA := errors.New("halt from A")

	runner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substrate string) (string, error) {
		if step.ID == "stepA" {
			return "", errA
		}
		if step.ID == "stepB" {
			close(bStarted)
			<-bRelease
			bFlag.Store(true)
			return "", nil
		}
		return "", nil
	}

	opts := SequenceRunOptions{
		Stdout:             &bytes.Buffer{},
		Stderr:             &stderr,
		Interactive:        false,
		StepRunner:         runner,
		EventBus:           bus,
		CleanupGracePeriod: 10 * time.Second,
	}

	returned := make(chan error, 1)
	go func() {
		_, err := RunSequence(context.Background(), path, opts)
		returned <- err
	}()

	<-bStarted

	select {
	case <-aFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stepA failure event")
	}

	// Bounded wait to prove run() does not return while B is in-flight.
	// This wait can never fail spuriously on correct code because run() genuinely cannot return until bRelease is closed.
	select {
	case <-returned:
		t.Fatal("RunSequence returned prematurely before B was drained")
	case <-time.After(500 * time.Millisecond):
		// Asserted that it did not return.
	}

	close(bRelease)

	select {
	case err := <-returned:
		if err == nil || !strings.Contains(err.Error(), errA.Error()) {
			t.Fatalf("expected halt error %v, got %v", errA, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSequence failed to return after B was released")
	}

	if !bFlag.Load() {
		t.Error("expected B's ordering flag to be true")
	}
}

func TestRunSequence_DrainCleanupErrorDoesNotShadowRunError(t *testing.T) {
	path, _ := createTestSequenceDrain(t)
	var stderr bytes.Buffer
	bus := eventbus.New()
	defer bus.Shutdown()

	bStarted := make(chan struct{})
	bRelease := make(chan struct{})
	aFailed := make(chan struct{})

	bus.Subscribe(eventbus.EventSequenceFailure, func(e eventbus.Event) {
		if stepID, ok := e.Data["stepId"].(string); ok && stepID == "stepA" {
			close(aFailed)
		}
	})

	errA := errors.New("halt from A")
	errB := errors.New("cleanup error from B")

	runner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substrate string) (string, error) {
		if step.ID == "stepA" {
			return "", errA
		}
		if step.ID == "stepB" {
			close(bStarted)
			<-bRelease
			return "", errB
		}
		return "", nil
	}

	opts := SequenceRunOptions{
		Stdout:             &bytes.Buffer{},
		Stderr:             &stderr,
		Interactive:        false,
		StepRunner:         runner,
		EventBus:           bus,
		CleanupGracePeriod: 10 * time.Second,
	}

	returned := make(chan error, 1)
	go func() {
		_, err := RunSequence(context.Background(), path, opts)
		returned <- err
	}()

	<-bStarted

	select {
	case <-aFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stepA failure event")
	}

	close(bRelease)

	select {
	case err := <-returned:
		if err == nil || !strings.Contains(err.Error(), errA.Error()) {
			t.Fatalf("expected halt error %v, got %v", errA, err)
		}
		// The drain reports a step's unwind error; it must not fold it into
		// the run's own error. Asserting the halt error is present is not
		// enough — a joined error would contain it too.
		if strings.Contains(err.Error(), errB.Error()) {
			t.Errorf("B's cleanup error leaked into the run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSequence failed to return")
	}

	out := stderr.String()
	if !strings.Contains(out, "Step [stepB] cleanup error") {
		t.Errorf("stderr does not mention step B's cleanup error: %q", out)
	}
	if !strings.Contains(out, errB.Error()) {
		t.Errorf("stderr does not mention errB: %q", out)
	}
}

func TestRunSequence_DrainBackstopFires(t *testing.T) {
	path, _ := createTestSequenceDrain(t)
	var stderr bytes.Buffer
	bus := eventbus.New()
	defer bus.Shutdown()

	bStarted := make(chan struct{})
	bRelease := make(chan struct{})
	aFailed := make(chan struct{})

	// Release parked goroutine on test cleanup
	t.Cleanup(func() { close(bRelease) })

	bus.Subscribe(eventbus.EventSequenceFailure, func(e eventbus.Event) {
		if stepID, ok := e.Data["stepId"].(string); ok && stepID == "stepA" {
			close(aFailed)
		}
	})

	var incompleteEvents int32
	bus.Subscribe(eventbus.EventSequenceCleanupIncomplete, func(e eventbus.Event) {
		atomic.AddInt32(&incompleteEvents, 1)
		stepIds, ok := e.Data["stepIds"].([]string)
		if !ok || len(stepIds) != 1 || stepIds[0] != "stepB" {
			t.Errorf("unexpected stepIds in event: %v", e.Data["stepIds"])
		}
	})

	errA := errors.New("halt from A")

	runner := func(ctx context.Context, seq *Sequence, step *SequenceStep, substrate string) (string, error) {
		if step.ID == "stepA" {
			return "", errA
		}
		if step.ID == "stepB" {
			close(bStarted)
			<-bRelease
			return "", nil
		}
		return "", nil
	}

	opts := SequenceRunOptions{
		Stdout:             &bytes.Buffer{},
		Stderr:             &stderr,
		Interactive:        false,
		StepRunner:         runner,
		EventBus:           bus,
		CleanupGracePeriod: 50 * time.Millisecond,
	}

	returned := make(chan error, 1)
	go func() {
		_, err := RunSequence(context.Background(), path, opts)
		returned <- err
	}()

	<-bStarted

	select {
	case <-aFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stepA failure event")
	}

	select {
	case err := <-returned:
		if err == nil || !strings.Contains(err.Error(), errA.Error()) {
			t.Fatalf("expected halt error %v, got %v", errA, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSequence failed to return (backstop did not fire)")
	}

	out := stderr.String()
	if !strings.Contains(out, "stepB") || !strings.Contains(out, "git stash list") {
		t.Errorf("stderr does not contain expected backstop message: %q", out)
	}

	// No wait is needed before this assertion: Bus.Publish invokes subscriber
	// handlers synchronously in the publishing goroutine, so the counter is
	// already incremented by the time RunSequence returns.
	if atomic.LoadInt32(&incompleteEvents) != 1 {
		t.Errorf("expected exactly 1 EventSequenceCleanupIncomplete event, got %d", atomic.LoadInt32(&incompleteEvents))
	}
}
