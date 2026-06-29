package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureMeristemGenotypeCreatesFile(t *testing.T) {
	root := t.TempDir()

	if err := EnsureMeristemGenotype(root); err != nil {
		t.Fatalf("EnsureMeristemGenotype failed: %v", err)
	}

	genotypePath := filepath.Join(root, ".tendril", "genotypes", "meristem.json")
	content, err := os.ReadFile(genotypePath)
	if err != nil {
		t.Fatalf("read meristem genotype: %v", err)
	}

	var genotype genotypeDefinition
	if err := json.Unmarshal(content, &genotype); err != nil {
		t.Fatalf("decode meristem genotype: %v", err)
	}

	if genotype.Name != "meristem" {
		t.Fatalf("genotype name = %q, want meristem", genotype.Name)
	}
	if !strings.Contains(genotype.Instructions, "OpenTendril Meristem") {
		t.Fatalf("genotype instructions missing meristem persona: %q", genotype.Instructions)
	}
}

func TestEnsureMeristemGenotypeDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	genotypePath := filepath.Join(root, ".tendril", "genotypes", "meristem.json")

	if err := os.MkdirAll(filepath.Dir(genotypePath), 0o755); err != nil {
		t.Fatalf("mkdir genotype directory: %v", err)
	}
	original := []byte(`{"name":"meristem","instructions":"keep me"}`)
	if err := os.WriteFile(genotypePath, original, 0o644); err != nil {
		t.Fatalf("seed meristem genotype: %v", err)
	}

	if err := EnsureMeristemGenotype(root); err != nil {
		t.Fatalf("EnsureMeristemGenotype failed: %v", err)
	}

	content, err := os.ReadFile(genotypePath)
	if err != nil {
		t.Fatalf("read meristem genotype: %v", err)
	}
	if string(content) != string(original) {
		t.Fatalf("meristem genotype was overwritten:\n%s", string(content))
	}
}

func TestEnsureSpecializedGenotypesCreateFiles(t *testing.T) {
	tests := []struct {
		name         string
		ensure       func(string) error
		persona      string
		expectedName string
	}{
		{
			name:         "thinker",
			ensure:       EnsureThinkerGenotype,
			persona:      "OpenTendril Thinker",
			expectedName: "thinker",
		},
		{
			name:         "verifier",
			ensure:       EnsureVerifierGenotype,
			persona:      "OpenTendril Verifier",
			expectedName: "verifier",
		},
		{
			name:         "debugger",
			ensure:       EnsureDebuggerGenotype,
			persona:      "OpenTendril Debugger",
			expectedName: "debugger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()

			if err := tt.ensure(root); err != nil {
				t.Fatalf("%s ensure failed: %v", tt.name, err)
			}

			genotypePath := filepath.Join(root, ".tendril", "genotypes", tt.expectedName+".json")
			content, err := os.ReadFile(genotypePath)
			if err != nil {
				t.Fatalf("read %s genotype: %v", tt.name, err)
			}

			var genotype genotypeDefinition
			if err := json.Unmarshal(content, &genotype); err != nil {
				t.Fatalf("decode %s genotype: %v", tt.name, err)
			}

			if genotype.Name != tt.expectedName {
				t.Fatalf("genotype name = %q, want %q", genotype.Name, tt.expectedName)
			}
			if !strings.Contains(genotype.Instructions, tt.persona) {
				t.Fatalf("genotype instructions missing persona %q: %q", tt.persona, genotype.Instructions)
			}
		})
	}
}
