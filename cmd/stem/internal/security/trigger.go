package security

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type TriggerPayload struct {
	Genotype string `json:"genotype"`
	Transcript string `json:"transcript"`
}

// EvaluateTriggers executes all scripts in the given triggers directory.
// It serializes the payload to a temp file and passes the path to each script.
// If any script returns > 0, it returns an error with the script's stderr.
func EvaluateTriggers(ctx context.Context, triggersDir string, payload TriggerPayload) error {
	// Check if directory exists
	if _, err := os.Stat(triggersDir); os.IsNotExist(err) {
		// No triggers configured, allow execution
		return nil
	}

	entries, err := os.ReadDir(triggersDir)
	if err != nil {
		return fmt.Errorf("failed to read triggers directory: %w", err)
	}

	// Filter executable scripts
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
		return nil // No executable triggers
	}

	// Create temp JSON file
	tmpFile, err := os.CreateTemp("", "tendril-trigger-*.json")
	if err != nil {
		return fmt.Errorf("failed to create temp file for trigger payload: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if err := json.NewEncoder(tmpFile).Encode(payload); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to encode trigger payload: %w", err)
	}
	tmpFile.Close()

	// Execute each script
	for _, script := range scripts {
		cmd := exec.CommandContext(ctx, script, tmpFile.Name())
		
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = io.Discard // Discard stdout as per requirements, or we could capture it.

		if err := cmd.Run(); err != nil {
			// Trigger failed! Return the stderr.
			errMsg := stderr.String()
			if errMsg == "" {
				errMsg = err.Error()
			}
			return fmt.Errorf("Hormonal Trigger Blocked: script '%s' failed.\nReason: %s", filepath.Base(script), errMsg)
		}
	}

	return nil
}
