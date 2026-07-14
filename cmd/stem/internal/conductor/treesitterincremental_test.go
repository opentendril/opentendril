package conductor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/terrarium"
)

// TestTreeSitterIncrementalScanEndToEnd drives the full repo-map flow (facade
// → changed-path delta → real container → PrecomputedParser → ScanRepository
// → rendered map) through its three incremental regimes:
//
//  1. cold index — the container walks the whole workspace itself;
//  2. warm index, nothing changed — no container run at all;
//  3. warm index with a delta — the container receives exactly the changed
//     file over stdin, and the map still carries the unchanged files' symbols
//     from the store.
//
// Docker-gated like the golden tests, but plain-skip only: the CI golden job
// pins fidelity; this test exists to prove the plumbing end-to-end wherever
// the image is available.
func TestTreeSitterIncrementalScanEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; cannot run incremental end-to-end test")
	}
	if err := exec.Command("docker", "image", "inspect", treeSitterImage).Run(); err != nil {
		t.Skipf("%s not built; run `docker build -t %s sprouts/tree-sitter/` to exercise this test", treeSitterImage, treeSitterImage)
	}
	// This test exists to prove the container plumbing; the in-process engine
	// (the default since slice 4) would bypass it entirely.
	t.Setenv(treeSitterEngineEnv, "terrarium")

	workspace := t.TempDir()
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile("alpha.py", "def alpha():\n    return 1\n")
	writeFile("beta.ts", "export function beta(): number {\n  return 2\n}\n")

	// Record every delta handed to the container while delegating to the real
	// terrarium run, so the assertions observe genuine end-to-end behavior.
	original := runTreeSitterScanFn
	defer func() { runTreeSitterScanFn = original }()
	var deltas [][]string
	runTreeSitterScanFn = func(ctx context.Context, providerName, workspacePath string, changedPaths []string) (terrarium.CommandResult, error) {
		var recorded []string
		if changedPaths != nil {
			recorded = append([]string{}, changedPaths...)
		}
		deltas = append(deltas, recorded)
		return runTreeSitterScan(ctx, providerName, workspacePath, changedPaths)
	}

	ctx := context.Background()

	// 1. Cold scan: full container walk, both files indexed.
	repoMap, err := GenerateRepoMap(ctx, workspace)
	if err != nil {
		t.Fatalf("cold GenerateRepoMap: %v", err)
	}
	if len(deltas) != 1 || deltas[0] != nil {
		t.Fatalf("cold scan must run one full-walk batch (nil delta), got %v", deltas)
	}
	for _, expected := range []string{"alpha", "beta"} {
		if !strings.Contains(repoMap, expected) {
			t.Fatalf("cold repo map missing %q:\n%s", expected, repoMap)
		}
	}

	// 2. Unchanged re-scan: the container must not run at all.
	repoMap, err = GenerateRepoMap(ctx, workspace)
	if err != nil {
		t.Fatalf("unchanged GenerateRepoMap: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("an unchanged re-scan must not start a container, got batches %v", deltas)
	}
	for _, expected := range []string{"alpha", "beta"} {
		if !strings.Contains(repoMap, expected) {
			t.Fatalf("unchanged repo map missing %q:\n%s", expected, repoMap)
		}
	}

	// 3. Delta re-scan: only the modified file goes to the container; the
	// untouched file's symbols survive via the store.
	writeFile("alpha.py", "def alpha():\n    return 1\n\n\ndef gamma():\n    return 3\n")
	repoMap, err = GenerateRepoMap(ctx, workspace)
	if err != nil {
		t.Fatalf("delta GenerateRepoMap: %v", err)
	}
	if len(deltas) != 2 || !reflect.DeepEqual(deltas[1], []string{"alpha.py"}) {
		t.Fatalf("delta scan must ship exactly the changed file, got batches %v", deltas)
	}
	for _, expected := range []string{"alpha", "gamma", "beta"} {
		if !strings.Contains(repoMap, expected) {
			t.Fatalf("delta repo map missing %q:\n%s", expected, repoMap)
		}
	}
}
