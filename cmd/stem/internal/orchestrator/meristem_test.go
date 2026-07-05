package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/opentendril/core/data/genotypes"
)

func TestEnsureBuiltinGenotypes(t *testing.T) {
	root := t.TempDir()

	if err := EnsureBuiltinGenotypes(root); err != nil {
		t.Fatalf("EnsureBuiltinGenotypes failed: %v", err)
	}

	names := []string{"core-llm", "debugger", "meristem", "script-reviewer", "sre-monitor", "thinker", "verifier"}
	entries, err := os.ReadDir(filepath.Join(root, ".tendril", "genotypes"))
	if err != nil {
		t.Fatalf("read genotype directory: %v", err)
	}
	if len(entries) != len(names) {
		t.Fatalf("created %d genotype files, want %d", len(entries), len(names))
	}

	for _, name := range names {
		genotypePath := filepath.Join(root, ".tendril", "genotypes", name+".json")
		content, err := os.ReadFile(genotypePath)
		if err != nil {
			t.Fatalf("read %s genotype: %v", name, err)
		}

		var got genotypeDefinition
		if err := json.Unmarshal(content, &got); err != nil {
			t.Fatalf("decode %s genotype: %v", name, err)
		}

		embedded, err := genotypes.FS.ReadFile(name + ".json")
		if err != nil {
			t.Fatalf("read embedded %s genotype: %v", name, err)
		}

		var want genotypeDefinition
		if err := json.Unmarshal(embedded, &want); err != nil {
			t.Fatalf("decode embedded %s genotype: %v", name, err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s genotype = %#v, want %#v", name, got, want)
		}
	}
}
