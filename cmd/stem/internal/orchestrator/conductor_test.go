package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureConductorGenotypeCreatesFile(t *testing.T) {
	root := t.TempDir()

	if err := EnsureConductorGenotype(root); err != nil {
		t.Fatalf("EnsureConductorGenotype failed: %v", err)
	}

	genotypePath := filepath.Join(root, ".tendril", "genotypes", "conductor.json")
	content, err := os.ReadFile(genotypePath)
	if err != nil {
		t.Fatalf("read conductor genotype: %v", err)
	}

	var genotype genotypeDefinition
	if err := json.Unmarshal(content, &genotype); err != nil {
		t.Fatalf("decode conductor genotype: %v", err)
	}

	if genotype.Name != "conductor" {
		t.Fatalf("genotype name = %q, want conductor", genotype.Name)
	}
	if !strings.Contains(genotype.Instructions, "OpenTendril Conductor") {
		t.Fatalf("genotype instructions missing conductor persona: %q", genotype.Instructions)
	}
}

func TestEnsureConductorGenotypeDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	genotypePath := filepath.Join(root, ".tendril", "genotypes", "conductor.json")

	if err := os.MkdirAll(filepath.Dir(genotypePath), 0o755); err != nil {
		t.Fatalf("mkdir genotype directory: %v", err)
	}
	original := []byte(`{"name":"conductor","instructions":"keep me"}`)
	if err := os.WriteFile(genotypePath, original, 0o644); err != nil {
		t.Fatalf("seed conductor genotype: %v", err)
	}

	if err := EnsureConductorGenotype(root); err != nil {
		t.Fatalf("EnsureConductorGenotype failed: %v", err)
	}

	content, err := os.ReadFile(genotypePath)
	if err != nil {
		t.Fatalf("read conductor genotype: %v", err)
	}
	if string(content) != string(original) {
		t.Fatalf("conductor genotype was overwritten:\n%s", string(content))
	}
}
