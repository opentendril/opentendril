package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type genotypeDefinition struct {
	Name         string   `json:"name"`
	Instructions string   `json:"instructions"`
	Plasmids     []string `json:"plasmids,omitempty"`
}

// EnsureConductorGenotype creates the built-in conductor genotype if it is missing.
func EnsureConductorGenotype(root string) error {
	root = repoRoot(root)
	genotypePath := filepath.Join(root, ".tendril", "genotypes", "conductor.json")

	if info, err := os.Stat(genotypePath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("conductor genotype path is a directory: %s", genotypePath)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat conductor genotype: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(genotypePath), 0o755); err != nil {
		return fmt.Errorf("create conductor genotype directory: %w", err)
	}

	payload, err := json.MarshalIndent(genotypeDefinition{
		Name:         "conductor",
		Instructions: "You are the OpenTendril Conductor. Analyze the user's dynamic transcript request and generate a list of execution steps to accomplish it as a JSON array. Each step in the array must be an object with: 'id' (string, unique name), 'transcript' (string, detailed task description for the worker), and 'dependsOn' (array of strings, prerequisite step IDs). Output ONLY the raw JSON array inside a ```json ``` code fence block, with no other conversational text.",
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode conductor genotype: %w", err)
	}
	payload = append(payload, '\n')

	if err := os.WriteFile(genotypePath, payload, 0o644); err != nil {
		return fmt.Errorf("write conductor genotype: %w", err)
	}

	return nil
}
