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

// EnsureMeristemGenotype creates the built-in Meristem genotype if it is missing.
func EnsureMeristemGenotype(root string) error {
	return ensureGenotype(root, "meristem", "You are the OpenTendril Meristem planning node. Analyze the user's dynamic transcript request and generate a list of execution steps to accomplish it as a JSON array. Each step in the array must be an object with: 'id' (string, unique name), 'transcript' (string, detailed task description for the worker), and 'dependsOn' (array of strings, prerequisite step IDs). Output ONLY the raw JSON array inside a ```json ``` code fence block, with no other conversational text.")
}

// EnsureThinkerGenotype creates the built-in thinker genotype if it is missing.
func EnsureThinkerGenotype(root string) error {
	return ensureGenotype(root, "thinker", "You are the OpenTendril Thinker. Analyze the user's task request and the codebase structure, and write a detailed technical execution plan (Markdown format) outlining exactly what files need to change, why, and how. Do not write the actual code.")
}

// EnsureVerifierGenotype creates the built-in verifier genotype if it is missing.
func EnsureVerifierGenotype(root string) error {
	return ensureGenotype(root, "verifier", "You are the OpenTendril Verifier. Run the test suite and inspect the code changes. Analyze compiler/linter errors and test failures, and output a detailed execution summary in JSON explaining if the verification passed, and list any errors found.")
}

// EnsureDebuggerGenotype creates the built-in debugger genotype if it is missing.
func EnsureDebuggerGenotype(root string) error {
	return ensureGenotype(root, "debugger", "You are the OpenTendril Debugger. Ingest the compiler/linter or test error logs, locate the bugs in the codebase, and write targeted code changes or patches to correct the errors.")
}

func ensureGenotype(root, name, instructions string) error {
	root = repoRoot(root)
	genotypePath := filepath.Join(root, ".tendril", "genotypes", name+".json")

	if info, err := os.Stat(genotypePath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s genotype path is a directory: %s", name, genotypePath)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s genotype: %w", name, err)
	}

	if err := os.MkdirAll(filepath.Dir(genotypePath), 0o755); err != nil {
		return fmt.Errorf("create %s genotype directory: %w", name, err)
	}

	payload, err := json.MarshalIndent(genotypeDefinition{
		Name:         name,
		Instructions: instructions,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s genotype: %w", name, err)
	}
	payload = append(payload, '\n')

	if err := os.WriteFile(genotypePath, payload, 0o644); err != nil {
		return fmt.Errorf("write %s genotype: %w", name, err)
	}

	return nil
}
