package conductor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// syntheticModuleGraph installs a small synthetic module as the package
// lister for the duration of the test:
//
//	example.com/scope/internal/core       (imports nothing)
//	example.com/scope/internal/receptors  (imports internal/core)
//	example.com/scope/cmd/stem            (imports internal/receptors)
//	example.com/scope/internal/isolated   (imports nothing, imported by nobody)
//	example.com/scope/internal/probe      (test files import internal/core)
//
// so a change to internal/core must transitively pull in internal/receptors,
// cmd/stem, and probe, but never isolated.
func syntheticModuleGraph(t *testing.T) {
	t.Helper()
	original := listModulePackagesFn
	t.Cleanup(func() { listModulePackagesFn = original })
	listModulePackagesFn = func(ctx context.Context, moduleRoot string) ([]modulePackage, error) {
		return []modulePackage{
			{ImportPath: "example.com/scope/internal/core", Directory: "internal/core"},
			{ImportPath: "example.com/scope/internal/receptors", Directory: "internal/receptors",
				Imports: []string{"example.com/scope/internal/core", "fmt"}},
			{ImportPath: "example.com/scope/cmd/stem", Directory: "cmd/stem",
				Imports: []string{"example.com/scope/internal/receptors", "os"}},
			{ImportPath: "example.com/scope/internal/isolated", Directory: "internal/isolated"},
			{ImportPath: "example.com/scope/internal/probe", Directory: "internal/probe",
				TestImports: []string{"example.com/scope/internal/core"}},
		}, nil
	}
}

func assertScope(t *testing.T, changedFiles []string, wantPackages []string, wantWholeModule bool) {
	t.Helper()
	packages, wholeModule, err := TestScopeForChanges(context.Background(), "/module", changedFiles)
	if err != nil {
		t.Fatalf("TestScopeForChanges returned error: %v", err)
	}
	if wholeModule != wantWholeModule {
		t.Fatalf("wholeModule = %v, want %v (packages %v)", wholeModule, wantWholeModule, packages)
	}
	sort.Strings(wantPackages)
	if fmt.Sprint(packages) != fmt.Sprint(wantPackages) {
		t.Fatalf("packages = %v, want %v", packages, wantPackages)
	}
}

// A change to a low-level package must scope to that package plus every
// transitive reverse-dependent — including a package whose only edge is a
// test-file import — and must leave unrelated packages out.
func TestScopeIncludesTransitiveReverseDependents(t *testing.T) {
	syntheticModuleGraph(t)
	assertScope(t,
		[]string{"internal/core/thing.go"},
		[]string{
			"example.com/scope/internal/core",
			"example.com/scope/internal/receptors",
			"example.com/scope/cmd/stem",
			"example.com/scope/internal/probe",
		},
		false)
}

// A change to a leaf package with no reverse-dependents scopes to exactly
// that package.
func TestScopeLeafPackageStaysNarrow(t *testing.T) {
	syntheticModuleGraph(t)
	assertScope(t,
		[]string{"internal/isolated/only.go"},
		[]string{"example.com/scope/internal/isolated"},
		false)
}

// A changed module definition file rewrites the dependency universe, so the
// scope must widen to the whole module without consulting the lister at all.
func TestScopeModuleDefinitionChangeWidensToWholeModule(t *testing.T) {
	original := listModulePackagesFn
	t.Cleanup(func() { listModulePackagesFn = original })
	listModulePackagesFn = func(ctx context.Context, moduleRoot string) ([]modulePackage, error) {
		t.Fatalf("package lister must not run when the module definition changed")
		return nil, nil
	}
	for _, definitionFile := range []string{"go.mod", "go.sum", "go.work", "go.work.sum"} {
		packages, wholeModule, err := TestScopeForChanges(context.Background(), "/module", []string{definitionFile})
		if err != nil {
			t.Fatalf("TestScopeForChanges(%s) returned error: %v", definitionFile, err)
		}
		if !wholeModule || len(packages) != 0 {
			t.Fatalf("TestScopeForChanges(%s) = (%v, %v), want whole module", definitionFile, packages, wholeModule)
		}
	}
}

// A failing package lister leaves every narrowing decision a guess, so the
// scope must fail closed to the whole module instead of returning an error.
func TestScopeListerErrorFailsClosedToWholeModule(t *testing.T) {
	original := listModulePackagesFn
	t.Cleanup(func() { listModulePackagesFn = original })
	listModulePackagesFn = func(ctx context.Context, moduleRoot string) ([]modulePackage, error) {
		return nil, fmt.Errorf("synthetic lister failure")
	}
	packages, wholeModule, err := TestScopeForChanges(context.Background(), "/module", []string{"internal/core/thing.go"})
	if err != nil {
		t.Fatalf("TestScopeForChanges returned error: %v", err)
	}
	if !wholeModule || len(packages) != 0 {
		t.Fatalf("got (%v, %v), want fail-closed whole module", packages, wholeModule)
	}
}

// A changed testdata fixture belongs to its enclosing package, and that
// package's reverse-dependents ride along.
func TestScopeTestdataChangeAttributesToEnclosingPackage(t *testing.T) {
	syntheticModuleGraph(t)
	assertScope(t,
		[]string{"internal/receptors/testdata/nested/fixture.txt"},
		[]string{
			"example.com/scope/internal/receptors",
			"example.com/scope/cmd/stem",
		},
		false)
}

// Attribution walks up to the NEAREST package directory: an asset nested
// under intermediate non-package directories still lands on its package, and
// a file inside a nested package lands on the nested package, not its parent.
func TestScopeAttributionWalksUpToNearestPackage(t *testing.T) {
	syntheticModuleGraph(t)
	// internal/isolated/assets/deep is not a package: walk up to isolated.
	assertScope(t,
		[]string{"internal/isolated/assets/deep/logo.svg"},
		[]string{"example.com/scope/internal/isolated"},
		false)
	// internal/core sits under internal (not a package): the nearest
	// enclosing package is internal/core itself, reached without widening.
	assertScope(t,
		[]string{"internal/core/embedded.tmpl"},
		[]string{
			"example.com/scope/internal/core",
			"example.com/scope/internal/receptors",
			"example.com/scope/cmd/stem",
			"example.com/scope/internal/probe",
		},
		false)
}

// A change touching only files outside every package directory — pure
// documentation and tooling configuration — is known inert: the scope is
// legitimately empty, and it must NOT widen to the whole module.
func TestScopePureDocumentationChangeYieldsEmptyScope(t *testing.T) {
	syntheticModuleGraph(t)
	assertScope(t,
		[]string{"README.md", "docs/design/overview.md", ".github/workflows/verify.yml"},
		nil,
		false)
}

// A Go source file with no enclosing package — for example one whose whole
// package this change deleted — plausibly affects the build and cannot be
// attributed, so the scope must fail closed to the whole module.
func TestScopeUnattributableGoFileFailsClosedToWholeModule(t *testing.T) {
	syntheticModuleGraph(t)
	packages, wholeModule, err := TestScopeForChanges(context.Background(), "/module",
		[]string{"internal/removed/gone.go"})
	if err != nil {
		t.Fatalf("TestScopeForChanges returned error: %v", err)
	}
	if !wholeModule || len(packages) != 0 {
		t.Fatalf("got (%v, %v), want fail-closed whole module", packages, wholeModule)
	}
}

// A changed-file path that escapes the module root means the change set and
// the module root disagree — uncertainty, so the scope fails closed.
func TestScopePathOutsideModuleRootFailsClosedToWholeModule(t *testing.T) {
	syntheticModuleGraph(t)
	packages, wholeModule, err := TestScopeForChanges(context.Background(), "/module",
		[]string{"../elsewhere/README.md"})
	if err != nil {
		t.Fatalf("TestScopeForChanges returned error: %v", err)
	}
	if !wholeModule || len(packages) != 0 {
		t.Fatalf("got (%v, %v), want fail-closed whole module", packages, wholeModule)
	}
}

// Absolute changed-file paths are normalized against the module root before
// attribution, so both spellings scope identically.
func TestScopeAcceptsAbsoluteChangedFilePaths(t *testing.T) {
	syntheticModuleGraph(t)
	assertScope(t,
		[]string{filepath.Join("/module", "internal/isolated/only.go")},
		[]string{"example.com/scope/internal/isolated"},
		false)
}

// An empty module root is caller error, the one condition reported as an
// error rather than a widened scope.
func TestScopeEmptyModuleRootIsAnError(t *testing.T) {
	syntheticModuleGraph(t)
	if _, _, err := TestScopeForChanges(context.Background(), "  ", []string{"internal/core/thing.go"}); err == nil {
		t.Fatalf("expected an error for an empty module root")
	}
}

// Integration: the real `go list` over this repository and the seam-driven
// logic must agree on a known relationship — the conductor package imports
// the eventbus package, so a change inside eventbus must scope in both.
func TestScopeAgainstRealModuleGraph(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real go list invocation in short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	moduleRoot := realModuleRoot(t)

	packages, wholeModule, err := TestScopeForChanges(context.Background(), moduleRoot,
		[]string{"cmd/stem/internal/eventbus/eventbus.go"})
	if err != nil {
		t.Fatalf("TestScopeForChanges returned error: %v", err)
	}
	if wholeModule {
		t.Fatalf("unexpected whole-module fallback for a single in-package change")
	}
	want := map[string]bool{
		"github.com/opentendril/core/cmd/stem/internal/eventbus":  false,
		"github.com/opentendril/core/cmd/stem/internal/conductor": false,
	}
	for _, importPath := range packages {
		if _, tracked := want[importPath]; tracked {
			want[importPath] = true
		}
	}
	for importPath, found := range want {
		if !found {
			t.Fatalf("expected %s in scope, got %v", importPath, packages)
		}
	}
}

// realModuleRoot walks up from the test's working directory to the directory
// containing go.mod.
func realModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not determine working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("no go.mod found above the test working directory")
		}
		directory = parent
	}
}
