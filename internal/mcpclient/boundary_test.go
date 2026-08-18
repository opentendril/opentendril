package mcpclient

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenImportPrefixes are Stem-authority and control-plane packages this
// client must never depend on. The package owns transport only: address
// resolution, credential-file lookup, owner probing, token mint/refresh, and
// authenticated MCP frame forwarding.
var forbiddenImportPrefixes = []string{
	"github.com/opentendril/opentendril/cmd/stem",
	"github.com/opentendril/opentendril/cmd/stem/internal",
	"github.com/opentendril/opentendril/cmd/stem/internal/core",
	"github.com/opentendril/opentendril/cmd/stem/internal/conductor",
	"github.com/opentendril/opentendril/cmd/stem/internal/session",
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb",
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus",
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium",
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors",
	"github.com/opentendril/opentendril/roots/llm",
}

func TestClientHasNoStemAuthorityImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read mcpclient package dir: %v", err)
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
					t.Errorf("mcpclient/%s imports forbidden package %q — the client must not reach Stem authority", name, path)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no non-test Go files found in the mcpclient package")
	}
}
