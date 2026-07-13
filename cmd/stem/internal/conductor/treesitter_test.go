package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/rhizome"
	"github.com/opentendril/core/cmd/stem/internal/terrarium"
)

func TestParseTreeSitterOutputMapsSymbols(t *testing.T) {
	result := terrarium.CommandResult{
		ExitCode: 0,
		Stdout: `{"path":"src/service.py","symbols":[{"name":"file_context","type":"file_context","lineStart":1,"lineEnd":1,"stub":"Imports: import os"},{"name":"Distiller","type":"class","lineStart":4,"lineEnd":9,"stub":"class Distiller:"},{"name":"distill","type":"method","lineStart":7,"lineEnd":9,"stub":"def distill(self, repo):"}]}
{"path":"src/widget.tsx","symbols":[{"name":"Widget","type":"function","lineStart":10,"lineEnd":12,"stub":"function Widget(props)"}]}
`,
	}

	symbolsByPath, err := parseTreeSitterOutput(result)
	if err != nil {
		t.Fatalf("parseTreeSitterOutput returned error: %v", err)
	}
	if len(symbolsByPath) != 2 {
		t.Fatalf("file count mismatch: got %d want 2", len(symbolsByPath))
	}

	service := symbolsByPath["src/service.py"]
	if len(service) != 3 {
		t.Fatalf("service.py symbol count mismatch: got %d want 3", len(service))
	}
	if service[1].Name != "Distiller" || service[1].Type != "class" || service[1].LineStart != 4 || service[1].LineEnd != 9 {
		t.Fatalf("unexpected class symbol: %+v", service[1])
	}
	if service[2].Type != "method" {
		t.Fatalf("expected tree-sitter method fidelity, got %+v", service[2])
	}
	if service[2].StubContent != "def distill(self, repo):" {
		t.Fatalf("stub mismatch: %q", service[2].StubContent)
	}
	// ScanRepository stamps these; the pre-pass must leave them empty.
	if service[0].RepositoryName != "" || service[0].FilePath != "" {
		t.Fatalf("pre-pass must not stamp RepositoryName/FilePath: %+v", service[0])
	}
}

func TestParseTreeSitterOutputToleratesMalformedLines(t *testing.T) {
	result := terrarium.CommandResult{
		ExitCode: 0,
		Stdout: `{"path":"good.py","symbols":[{"name":"run","type":"function","lineStart":1,"lineEnd":2,"stub":"def run():"}]}
{"path":"truncated.py","symbols":[{"name":"broken",
not json at all
{"path":"also_good.ts","symbols":[]}
`,
	}

	symbolsByPath, err := parseTreeSitterOutput(result)
	if err != nil {
		t.Fatalf("expected malformed lines to be tolerated, got error: %v", err)
	}
	if len(symbolsByPath) != 2 {
		t.Fatalf("file count mismatch: got %d want 2 (only the well-formed lines)", len(symbolsByPath))
	}
	if _, ok := symbolsByPath["good.py"]; !ok {
		t.Fatal("missing good.py")
	}
	if _, ok := symbolsByPath["truncated.py"]; ok {
		t.Fatal("a truncated line must drop that file so the regex parser catches it")
	}
	if symbols, ok := symbolsByPath["also_good.ts"]; !ok || len(symbols) != 0 {
		t.Fatalf("expected also_good.ts with zero symbols, got %v (present=%v)", symbols, ok)
	}
}

func TestParseTreeSitterOutputDropsUnknownTypesAndClampsLines(t *testing.T) {
	result := terrarium.CommandResult{
		ExitCode: 0,
		Stdout: `{"path":"x.py","symbols":[{"name":"weird","type":"enum_member","lineStart":3,"lineEnd":4,"stub":"?"},{"name":"","type":"function","lineStart":1,"lineEnd":1,"stub":"anon"},{"name":"ok","type":"function","lineStart":0,"lineEnd":-2,"stub":"def ok():"}]}
`,
	}

	symbolsByPath, err := parseTreeSitterOutput(result)
	if err != nil {
		t.Fatalf("parseTreeSitterOutput returned error: %v", err)
	}
	symbols := symbolsByPath["x.py"]
	if len(symbols) != 1 {
		t.Fatalf("expected only the valid symbol to survive, got %+v", symbols)
	}
	if symbols[0].Name != "ok" || symbols[0].LineStart != 1 || symbols[0].LineEnd != 1 {
		t.Fatalf("expected clamped line numbers on the surviving symbol, got %+v", symbols[0])
	}
}

func TestParseTreeSitterOutputRejectsUnsafePaths(t *testing.T) {
	result := terrarium.CommandResult{
		ExitCode: 0,
		Stdout: `{"path":"/etc/passwd","symbols":[]}
{"path":"../outside.py","symbols":[]}
{"path":"nested/../../outside.py","symbols":[]}
{"path":"","symbols":[]}
{"path":"inside/ok.py","symbols":[]}
`,
	}

	symbolsByPath, err := parseTreeSitterOutput(result)
	if err != nil {
		t.Fatalf("parseTreeSitterOutput returned error: %v", err)
	}
	if len(symbolsByPath) != 1 {
		t.Fatalf("expected only the workspace-relative path to survive, got %v", symbolsByPath)
	}
	if _, ok := symbolsByPath["inside/ok.py"]; !ok {
		t.Fatal("missing inside/ok.py")
	}
}

func TestParseTreeSitterOutputFailsOnNonzeroExit(t *testing.T) {
	if _, err := parseTreeSitterOutput(terrarium.CommandResult{ExitCode: 1, Stderr: "boom"}); err == nil {
		t.Fatal("expected a nonzero exit to be an error so the caller falls back")
	}
}

func TestParseTreeSitterOutputFailsOnTimeout(t *testing.T) {
	if _, err := parseTreeSitterOutput(terrarium.CommandResult{TimedOut: true, ExitCode: -1}); err == nil {
		t.Fatal("expected a timeout to be an error so the caller falls back")
	}
}

// polyglotWorkspace lays down a workspace with a tree-sitter-eligible file so
// scanRepositoryParsers actually attempts the pre-pass.
func polyglotWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "service.py"), []byte("def run():\n    pass\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root
}

func TestScanRepositoryParsersInjectsPrecomputedOnSuccess(t *testing.T) {
	original := runTreeSitterScanFn
	defer func() { runTreeSitterScanFn = original }()
	runTreeSitterScanFn = func(ctx context.Context, providerName, workspacePath string) (terrarium.CommandResult, error) {
		return terrarium.CommandResult{
			ExitCode: 0,
			Stdout:   `{"path":"service.py","symbols":[{"name":"run","type":"function","lineStart":1,"lineEnd":2,"stub":"def run():"}]}` + "\n",
		}, nil
	}

	parsers := scanRepositoryParsers(context.Background(), polyglotWorkspace(t))
	if len(parsers) != 3 {
		t.Fatalf("parser count mismatch: got %d want 3", len(parsers))
	}
	if _, ok := parsers[0].(rhizome.GoParser); !ok {
		t.Fatalf("expected GoParser first, got %T", parsers[0])
	}
	precomputed, ok := parsers[1].(rhizome.PrecomputedParser)
	if !ok {
		t.Fatalf("expected PrecomputedParser second, got %T", parsers[1])
	}
	if !precomputed.Supports("service.py") {
		t.Fatal("expected the precomputed parser to cover the scanned file")
	}
	if _, ok := parsers[2].(rhizome.RegexParser); !ok {
		t.Fatalf("expected RegexParser last, got %T", parsers[2])
	}
}

func TestScanRepositoryParsersFallsBackWhenScanFails(t *testing.T) {
	original := runTreeSitterScanFn
	defer func() { runTreeSitterScanFn = original }()
	runTreeSitterScanFn = func(ctx context.Context, providerName, workspacePath string) (terrarium.CommandResult, error) {
		return terrarium.CommandResult{}, fmt.Errorf("docker daemon unreachable")
	}

	parsers := scanRepositoryParsers(context.Background(), polyglotWorkspace(t))
	if len(parsers) != 2 {
		t.Fatalf("expected DefaultParsers on batch failure, got %d parsers", len(parsers))
	}
	if _, ok := parsers[0].(rhizome.GoParser); !ok {
		t.Fatalf("expected GoParser first, got %T", parsers[0])
	}
	if _, ok := parsers[1].(rhizome.RegexParser); !ok {
		t.Fatalf("expected RegexParser second, got %T", parsers[1])
	}
}

func TestScanRepositoryParsersFallsBackOnBadOutput(t *testing.T) {
	original := runTreeSitterScanFn
	defer func() { runTreeSitterScanFn = original }()
	runTreeSitterScanFn = func(ctx context.Context, providerName, workspacePath string) (terrarium.CommandResult, error) {
		return terrarium.CommandResult{ExitCode: 137, Stderr: "OOM-killed"}, nil
	}

	parsers := scanRepositoryParsers(context.Background(), polyglotWorkspace(t))
	if len(parsers) != 2 {
		t.Fatalf("expected DefaultParsers on unusable batch output, got %d parsers", len(parsers))
	}
}

func TestScanRepositoryParsersSkipsContainerForNonEligibleWorkspace(t *testing.T) {
	original := runTreeSitterScanFn
	defer func() { runTreeSitterScanFn = original }()
	called := false
	runTreeSitterScanFn = func(ctx context.Context, providerName, workspacePath string) (terrarium.CommandResult, error) {
		called = true
		return terrarium.CommandResult{}, nil
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	parsers := scanRepositoryParsers(context.Background(), root)
	if called {
		t.Fatal("expected a Go-only workspace to skip the tree-sitter container entirely")
	}
	if len(parsers) != 2 {
		t.Fatalf("expected DefaultParsers for a Go-only workspace, got %d parsers", len(parsers))
	}
}
