package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var forbiddenImportPrefixes = []string{
	"github.com/opentendril/opentendril/cmd/stem",
	"github.com/opentendril/opentendril/roots/llm",
}

func TestClientHasNoStemAuthorityImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read tendril-mcp package dir: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++

		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", name, err)
			}
			for _, prefix := range forbiddenImportPrefixes {
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					t.Errorf("tendril-mcp/%s imports forbidden package %q — this executable must not reach Stem authority", name, path)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no non-test Go files found in cmd/tendril-mcp")
	}
}

func TestDependencyGraphHasNoStemAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "github.com/opentendril/opentendril/cmd/tendril-mcp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Split(string(out), "\n") {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		for _, prefix := range forbiddenImportPrefixes {
			if pkg == prefix || strings.HasPrefix(pkg, prefix+"/") {
				t.Errorf("forbidden dependency %q — no path in tendril-mcp may construct a Stem", pkg)
			}
		}
	}
}

func TestProductionDoesNotReadInProcessEnv(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read tendril-mcp package dir: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.Contains(src, []byte("TENDRIL_MCP_IN_PROCESS")) {
			t.Errorf("%s names TENDRIL_MCP_IN_PROCESS — this executable must not read it", name)
		}

		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "os" {
				return true
			}
			if sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv" {
				return true
			}
			if len(call.Args) != 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if val == "TENDRIL_MCP_IN_PROCESS" {
				t.Errorf("%s calls os.%s(%q)", name, sel.Sel.Name, val)
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal("no non-test Go files found in cmd/tendril-mcp")
	}
}
