package conductor

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestSequenceDispatchDataRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.yaml")

	seq := &Sequence{
		Name:             "race-test",
		Branch:           "feature/race-test",
		ConcurrencyLimit: 2,
		OnFailure:        sequenceOnFailureHalt,
		Steps: []SequenceStep{
			{ID: "step-a", Status: sequenceStatusPending, Transcript: "a"},
			{ID: "step-b", Status: sequenceStatusPending, Transcript: "b"},
		},
	}
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("SaveSequence failed: %v", err)
	}

	releaseB := make(chan struct{})
	stepBReady := make(chan struct{})

	stepRunner := func(ctx context.Context, s *Sequence, step *SequenceStep, substratePath string) (string, error) {
		if step.ID == "step-a" {
			// step-a completes immediately, triggering a SaveSequence on the scheduler thread
			return "ok", nil
		}

		if step.ID == "step-b" {
			close(stepBReady)
			// step-b reads from the sequence in a loop until released
			// These reads represent the racing side.
			var name, branch string
			for {
				select {
				case <-releaseB:
					// Prevent compiler from optimizing out the reads
					if name == "" || branch == "" {
						_ = name
					}
					return "ok", nil
				default:
					name = s.Name
					branch = s.Branch
				}
			}
		}
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		_, runErr = RunSequence(ctx, path, SequenceRunOptions{
			Stdout:      io.Discard,
			Stderr:      io.Discard,
			Interactive: false,
			StepRunner:  stepRunner,
		})
	}()

	// Wait for step-b to actually start its read loop
	select {
	case <-stepBReady:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for step-b to start")
	}

	// Bounded poll of the on-disk sequence to observe step-a completing
	// This guarantees we have hit the scheduler's write path.
	deadline := time.Now().Add(5 * time.Second)
	observedSave := false
	for time.Now().Before(deadline) {
		loaded, err := LoadSequence(path)
		if err != nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if loaded != nil {
			stepA := latestStepByID(loaded.Steps, "step-a")
			if stepA != nil && stepA.Status == sequenceStatusComplete {
				observedSave = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !observedSave {
		t.Fatalf("timed out waiting to observe step-a complete status on disk")
	}

	close(releaseB)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for RunSequence to complete")
	}

	if runErr != nil {
		t.Fatalf("RunSequence failed: %v", runErr)
	}
}
