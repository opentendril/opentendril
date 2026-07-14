package conductor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/rhizome"
)

// TestTreeSitterNativeGoldenFidelity is the slice-4 dual-engine gate: the
// in-process pure-Go engine (rhizome.TreeSitterParser) must produce exactly
// the symbols the terrarium engine pins in testdata/treesittergolden.json for
// the same fixture repo. The fixture is regenerated only from the container
// run (see TestTreeSitterGoldenFidelity and -update-treesitter-golden), so
// this test can never drift the golden toward the native engine — the
// container output stays the reference and the native engine must match it.
//
// Unlike the container golden test this one has no docker gate: it runs on
// every `go test ./cmd/stem/...`, locally and in CI.
func TestTreeSitterNativeGoldenFidelity(t *testing.T) {
	root, err := repoSourceRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	fixtures := filepath.Join(root, "sprouts", "tree-sitter", "testdata", "repo")

	parser := rhizome.NewTreeSitterParser()
	symbolsByPath := make(map[string][]rhizome.Symbol)
	err = filepath.WalkDir(fixtures, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			// Mirror the scanner/parse.js skip list for the segments the
			// fixture actually contains.
			if entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		relativePath, err := filepath.Rel(fixtures, path)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(relativePath)
		if !parser.Supports(relativePath) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		symbols, err := parser.Parse(relativePath, content)
		if err != nil {
			return err
		}
		symbolsByPath[relativePath] = symbols
		return nil
	})
	if err != nil {
		t.Fatalf("walk fixture repo: %v", err)
	}

	for path := range symbolsByPath {
		if strings.Contains(path, "node_modules/") {
			t.Fatalf("node_modules path leaked into symbols: %q", path)
		}
	}

	got, err := json.MarshalIndent(projectGolden(symbolsByPath), "", "  ")
	if err != nil {
		t.Fatalf("marshal golden projection: %v", err)
	}
	got = append(got, '\n')

	want, err := os.ReadFile(filepath.Join("testdata", "treesittergolden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("in-process tree-sitter symbols diverge from the container golden.\nThe golden is only ever regenerated from the container engine; fix the native port instead.\n\n--- got ---\n%s", got)
	}
}
