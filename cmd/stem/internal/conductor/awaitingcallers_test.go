package conductor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryAwaitingCallerDeclaresItAwaits pins the wiring, not the gate.
//
// The gate itself — a spent growth budget ending the run rather than detaching
// when the caller will not carry it on — is exercised elsewhere with a
// hand-built orchestrator. That test passes whether or not any real caller sets
// the flag, so on its own the entire change can be reverted with the suite
// staying green: removing `AwaitsRunEnding: true` from all four call sites
// breaks nothing any test observes.
//
// This is the same shape that has caught this plan before, where a fix was
// applied at one call site and the other kept the defect while the work looked
// done. The invariant is checked structurally rather than by driving each
// caller, for two reasons: driving four parallel-execution paths needs four
// heavyweight fixtures to assert one boolean, and a per-caller test would not
// notice a *fifth* awaiting caller added later. Reachability covers the callers
// nobody thought to drive.
//
// The rule: a function that awaits a possibly-detached run must say so on the
// orchestrator it hands to that run. If a future caller obtains its
// orchestrator from elsewhere rather than building one, this test will fail and
// should be taught the new shape deliberately — not deleted.
func TestEveryAwaitingCallerDeclaresItAwaits(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory failed: %v", err)
	}

	fileSet := token.NewFileSet()
	awaitingFunctions := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(fileSet, filepath.Join(".", name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s failed: %v", name, parseErr)
		}

		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			if !callsAwaitDetachedRun(function.Body) {
				continue
			}
			awaitingFunctions++

			if !declaresAwaitsRunEnding(function.Body) {
				t.Errorf("%s: %s awaits a run through awaitDetachedRun but builds no orchestrator declaring AwaitsRunEnding: true.\n"+
					"A caller that blocks until the run finishes must say so, or a spent growth budget detaches into an immediate await and bounds nothing.",
					name, function.Name.Name)
			}
		}
	}

	// Without this the loop could match nothing and pass. The count is a
	// literal the test owns, so renaming the helper out from under it fails
	// here rather than silently disabling the check.
	if awaitingFunctions < 4 {
		t.Fatalf("found %d functions awaiting a detachable run, want at least 4; the search matched almost nothing and proves nothing", awaitingFunctions)
	}
}

// callsAwaitDetachedRun reports whether the body calls the await shim anywhere,
// including inside a nested closure — two of the callers build their
// orchestrator and await inside one.
func callsAwaitDetachedRun(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "awaitDetachedRun" {
			found = true
			return false
		}
		return true
	})
	return found
}

// declaresAwaitsRunEnding reports whether the body builds a DockerOrchestrator
// literal that sets AwaitsRunEnding to true.
func declaresAwaitsRunEnding(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		composite, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		identifier, ok := composite.Type.(*ast.Ident)
		if !ok || identifier.Name != "DockerOrchestrator" {
			return true
		}
		for _, element := range composite.Elts {
			keyed, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := keyed.Key.(*ast.Ident)
			if !ok || key.Name != "AwaitsRunEnding" {
				continue
			}
			if value, ok := keyed.Value.(*ast.Ident); ok && value.Name == "true" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
