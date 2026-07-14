package conductor

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/rhizome"
)

// updateTreeSitterGolden regenerates testdata/treesittergolden.json from a
// live container run instead of comparing against it:
//
//	go test ./cmd/stem/internal/conductor/ -run TestTreeSitterGoldenFidelity -update-treesitter-golden
//
// Regenerate only after rebuilding the image, since ensureSproutImage never
// rebuilds an existing tag (the "stale :latest" gotcha):
//
//	docker build -t opentendril-tree-sitter:latest sprouts/tree-sitter/
var updateTreeSitterGolden = flag.Bool("update-treesitter-golden", false,
	"regenerate the tree-sitter golden fixture from a live container run")

// goldenSymbol is the human-reviewable projection of rhizome.Symbol the golden
// file stores. RepositoryName and FilePath are omitted deliberately: the batch
// pre-pass leaves them blank (ScanRepository stamps them later), so pinning
// them would only add noise.
type goldenSymbol struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	LineStart int    `json:"lineStart"`
	LineEnd   int    `json:"lineEnd"`
	Stub      string `json:"stub"`
}

// TestTreeSitterGoldenFidelity runs the real tree-sitter terrarium over the
// polyglot fixture repo and pins the extracted symbols, so a regression in
// parse.js fidelity (dropped decorators, wrong line spans, lost export
// modifiers) fails loudly instead of silently degrading the repo map.
//
// It is docker-gated by design. When docker is missing, or the image has not
// been built locally, it skips rather than building — that keeps the standard
// Go CI job (which has no prebuilt image) fast and green while still enforcing
// fidelity on any machine that has run the pre-pass. Build the image first to
// exercise it: `docker build -t opentendril-tree-sitter:latest sprouts/tree-sitter/`.
func TestTreeSitterGoldenFidelity(t *testing.T) {
	// OTTS_REQUIRE_GOLDEN=1 turns unmet preconditions into failures instead of
	// skips. CI sets it so a missing docker binary or an un-built image can
	// never masquerade as a pass — a green run must mean the fidelity was
	// actually checked. Locally the test still skips gracefully.
	requireGolden := os.Getenv("OTTS_REQUIRE_GOLDEN") == "1"
	skipOrFail := func(format string, args ...any) {
		t.Helper()
		if requireGolden {
			t.Fatalf("OTTS_REQUIRE_GOLDEN=1 but "+format, args...)
		}
		t.Skipf(format, args...)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		skipOrFail("docker not on PATH; cannot run tree-sitter golden fidelity test")
		return
	}
	if err := exec.Command("docker", "image", "inspect", treeSitterImage).Run(); err != nil {
		skipOrFail("%s not built; run `docker build -t %s sprouts/tree-sitter/` to exercise this test", treeSitterImage, treeSitterImage)
		return
	}

	root, err := repoSourceRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	fixtures := filepath.Join(root, "sprouts", "tree-sitter", "testdata", "repo")

	result, err := runTreeSitterScan(context.Background(), "", fixtures)
	if err != nil {
		t.Fatalf("run tree-sitter scan over fixtures: %v", err)
	}

	symbolsByPath, err := parseTreeSitterOutput(result)
	if err != nil {
		t.Fatalf("parse tree-sitter output: %v", err)
	}

	// The directory skip list must keep vendored code out of the index.
	for path := range symbolsByPath {
		if strings.Contains(path, "node_modules/") {
			t.Fatalf("node_modules path leaked into symbols: %q", path)
		}
	}

	got, err := json.MarshalIndent(projectGolden(symbolsByPath), "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
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
		t.Fatalf("tree-sitter symbols drifted from golden.\nRebuild the image, then regenerate with -update-treesitter-golden.\n\n--- got ---\n%s", got)
	}
}

// projectGolden turns the parsed symbol map into the ordered, JSON-tagged
// projection the golden pins. File keys are sorted by encoding/json on marshal;
// symbol order within a file is preserved as parse.js emitted it.
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
