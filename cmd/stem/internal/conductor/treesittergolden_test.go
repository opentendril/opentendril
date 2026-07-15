package conductor

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/rhizome"
)

// updateTreeSitterGolden regenerates testdata/treesittergolden.json from the
// in-process engine's current output:
//
//	go test ./cmd/stem/internal/conductor/ -run TestTreeSitterGoldenFidelity -update-treesitter-golden
//
// The terrarium engine used to own this golden; since it was demoted,
// the pure-Go rhizome.TreeSitterParser is the sole engine, so it also owns the
// fixture. Review the diff after regenerating — the golden is the fidelity
// contract, not a rubber stamp.
var updateTreeSitterGolden = flag.Bool("update-treesitter-golden", false,
	"regenerate the tree-sitter golden fixture from the in-process engine")

// goldenSymbol is the human-reviewable projection of rhizome.Symbol the golden
// file stores. RepositoryName and FilePath are omitted deliberately: the parse
// step leaves them blank (ScanRepository stamps them later), so pinning them
// would only add noise.
type goldenSymbol struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	LineStart int    `json:"lineStart"`
	LineEnd   int    `json:"lineEnd"`
	Stub      string `json:"stub"`
}

// TestTreeSitterGoldenFidelity pins the symbols the in-process pure-Go engine
// (rhizome.TreeSitterParser) extracts from the polyglot fixture repo under
// testdata/repo, so a regression in the engine's fidelity (dropped decorators,
// wrong line spans, lost export modifiers) fails loudly instead of silently
// degrading the repo map. It has no docker gate — it runs on every
// `go test ./cmd/stem/...`, locally and in CI.
func TestTreeSitterGoldenFidelity(t *testing.T) {
	fixtures := filepath.Join("testdata", "repo")

	parser := rhizome.NewTreeSitterParser()
	symbolsByPath := make(map[string][]rhizome.Symbol)
	err := filepath.WalkDir(fixtures, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			// Mirror the scanner skip list for the segments the fixture
			// actually contains.
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

	// The directory skip list must keep vendored code out of the index.
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

	goldenPath := filepath.Join("testdata", "treesittergolden.json")
	if *updateTreeSitterGolden {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update-treesitter-golden): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("in-process tree-sitter symbols drifted from the golden.\nRegenerate with -update-treesitter-golden after reviewing the change.\n\n--- got ---\n%s", got)
	}
}

// projectGolden turns the parsed symbol map into the ordered, JSON-tagged
// projection the golden pins. File keys are sorted by encoding/json on marshal;
// symbol order within a file is preserved as the engine emitted it.
func projectGolden(symbolsByPath map[string][]rhizome.Symbol) map[string][]goldenSymbol {
	out := make(map[string][]goldenSymbol, len(symbolsByPath))
	paths := make([]string, 0, len(symbolsByPath))
	for path := range symbolsByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		rows := symbolsByPath[path]
		projected := make([]goldenSymbol, 0, len(rows))
		for _, symbol := range rows {
			projected = append(projected, goldenSymbol{
				Name:      symbol.Name,
				Type:      symbol.Type,
				LineStart: symbol.LineStart,
				LineEnd:   symbol.LineEnd,
				Stub:      symbol.StubContent,
			})
		}
		out[path] = projected
	}
	return out
}
