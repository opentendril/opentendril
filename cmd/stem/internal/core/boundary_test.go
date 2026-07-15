package core_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenCoreImports are packages the transport-free Core must never depend
// on. If the Core reaches for a transport (net/http, MCP, CLI) or an execution
// internal (orchestrator, terrarium, gateway, mesh), a capability is no longer
// "invokable with zero HTTP/CLI/MCP types in scope" — the boundary the whole
// interface-parity design rests on. This test is that boundary,
// enforced structurally.
var forbiddenCoreImports = []string{
	"net/http",
	"github.com/opentendril/core/cmd/stem/internal/receptors",
	"github.com/opentendril/core/cmd/stem/internal/conductor",
	"github.com/opentendril/core/cmd/stem/internal/terrarium",
	"github.com/opentendril/core/cmd/stem/internal/gateway",
	"github.com/opentendril/core/cmd/stem/internal/mesh",
	"github.com/opentendril/core/cmd/stem/internal/historydb",
}

func TestCoreHasNoTransportOrExecutionImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read core package dir: %v", err)
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
			for _, forbidden := range forbiddenCoreImports {
				if path == forbidden {
					t.Errorf("core/%s imports forbidden package %q — the Core must translate no transport and pull in no execution internals", name, path)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no non-test Go files found in the core package")
	}
}
