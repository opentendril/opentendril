package security

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type TriggerPayload struct {
	Genotype   string `json:"genotype"`
	Transcript string `json:"transcript"`
}

// TriggerMode defines the explicit states of the Hormonal Trigger gate.
type TriggerMode string

const (
	ModeEnforce  TriggerMode = "enforce"
	ModeDisabled TriggerMode = "disabled"
)

// TriggerRunner executes a single trigger script.
type TriggerRunner interface {
	RunTrigger(ctx context.Context, scriptPath string, payload TriggerPayload) error
}

// EvaluateTriggers evaluates the Hormonal Trigger gate against the given payload.
// If mode is disabled, it returns immediately. If mode is enforce, it requires
// the triggers directory to exist and be readable. It executes each executable
// script via the runner; a non-zero exit blocks the run.
func EvaluateTriggers(ctx context.Context, mode TriggerMode, runner TriggerRunner, triggersDir string, payload TriggerPayload) error {
	if mode == ModeDisabled {
		return nil
	}
	
	// Default to enforce for any unrecognized mode value.
	if mode != ModeEnforce {
		mode = ModeEnforce
	}

	entries, err := os.ReadDir(triggersDir)
	if err != nil {
		// Deny (fail-closed) on missing or unreadable directory.
		return fmt.Errorf("Hormonal Trigger gate blocked: failed to read triggers directory: %w", err)
	}

	var scripts []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "README.md" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Check if executable by owner, group, or others
		if info.Mode()&0111 != 0 {
			scripts = append(scripts, filepath.Join(triggersDir, entry.Name()))
		}
	}

	if len(scripts) == 0 {
		log.Printf("Hormonal Triggers: no executable triggers configured in %s", triggersDir)
		return nil
	}

	for _, script := range scripts {
		if err := runner.RunTrigger(ctx, script, payload); err != nil {
			return err // runner formats the block message
		}
	}

	return nil
}
