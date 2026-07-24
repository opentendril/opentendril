package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeRunner struct {
	err error
}

func (r fakeRunner) RunTrigger(ctx context.Context, scriptPath string, payload TriggerPayload) error {
	return r.err
}

func TestEvaluateTriggers(t *testing.T) {
	tmpDir := t.TempDir()
	payload := TriggerPayload{Genotype: "test", Transcript: "test"}
	ctx := context.Background()

	t.Run("missing dir under enforce blocks", func(t *testing.T) {
		err := EvaluateTriggers(ctx, ModeEnforce, fakeRunner{}, filepath.Join(tmpDir, "nonexistent"), payload)
		if err == nil {
			t.Errorf("expected error for nonexistent directory under enforce mode")
		}
	})

	triggersDir := filepath.Join(tmpDir, "triggers")
	if err := os.Mkdir(triggersDir, 0755); err != nil {
		t.Fatalf("failed to create triggers dir: %v", err)
	}

	t.Run("unreadable dir under enforce blocks", func(t *testing.T) {
		unreadableDir := filepath.Join(tmpDir, "unreadable")
		if err := os.Mkdir(unreadableDir, 0000); err != nil {
			t.Fatalf("failed to create unreadable dir: %v", err)
		}
		err := EvaluateTriggers(ctx, ModeEnforce, fakeRunner{}, unreadableDir, payload)
		if err == nil {
			t.Errorf("expected error for unreadable directory under enforce mode")
		}
	})

	t.Run("empty dir under enforce allows", func(t *testing.T) {
		err := EvaluateTriggers(ctx, ModeEnforce, fakeRunner{}, triggersDir, payload)
		if err != nil {
			t.Errorf("expected no error for empty directory, got: %v", err)
		}
	})

	// Create an executable script
	scriptPath := filepath.Join(triggersDir, "test.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatalf("failed to create script: %v", err)
	}

	t.Run("disabled mode allows even with blocked trigger", func(t *testing.T) {
		runnerFails := fakeRunner{err: errors.New("blocked")}
		err := EvaluateTriggers(ctx, ModeDisabled, runnerFails, triggersDir, payload)
		if err != nil {
			t.Errorf("expected no error under disabled mode, got: %v", err)
		}
	})

	t.Run("populated trigger returning non-zero blocks", func(t *testing.T) {
		runnerFails := fakeRunner{err: errors.New("blocked")}
		err := EvaluateTriggers(ctx, ModeEnforce, runnerFails, triggersDir, payload)
		if err == nil || err.Error() != "blocked" {
			t.Errorf("expected 'blocked' error, got: %v", err)
		}
	})

	t.Run("isolation unavailable blocks (fail-closed)", func(t *testing.T) {
		runnerInitFails := fakeRunner{err: errors.New("failed to create isolated runner terrarium")}
		err := EvaluateTriggers(ctx, ModeEnforce, runnerInitFails, triggersDir, payload)
		if err == nil || err.Error() != "failed to create isolated runner terrarium" {
			t.Errorf("expected 'failed to create isolated runner terrarium' error, got: %v", err)
		}
	})

	t.Run("invalid mode resolves to enforce and blocks", func(t *testing.T) {
		runnerFails := fakeRunner{err: errors.New("blocked")}
		err := EvaluateTriggers(ctx, TriggerMode("invalid"), runnerFails, triggersDir, payload)
		if err == nil {
			t.Errorf("expected invalid mode to resolve to enforce and block")
		}
	})
}
