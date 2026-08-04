package dormancy

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// permittedImports is the complete set this package may reach for. It is an
// allowlist rather than a denylist on purpose: a denylist has to anticipate the
// next way someone finds to stop a run, and an allowlist does not.
//
// What is absent is the assertion. There is no os/exec, no syscall, no os
// beyond what tests use, no terrarium, no orchestrator, no network, no process
// or container handle of any kind — so there is no mechanism in this package
// capable of ending a run, however its logic were rewritten. The bus is the one
// outward reach, and the only thing it is used for is publishing a report.
//
// This is the checkable form of "nothing here may end a run". Asserting it as
// behaviour would only ever prove that the paths a test happened to drive did
// not end anything; asserting it as reachability covers the paths nobody
// thought to drive.
var permittedImports = map[string]struct{}{
	"context": {},
	"fmt":     {},
	"io":      {},
	"math":    {},
	"sort":    {},
	"strings": {},
	"sync":    {},
	"time":    {},
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus": {},
}

func TestPackageHoldsNoMeansToStopARun(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory failed: %v", err)
	}

	fileSet := token.NewFileSet()
	inspected := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fileSet, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s failed: %v", name, err)
		}
		inspected++

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquoting import %s failed: %v", name, spec.Path.Value, err)
			}
			if _, ok := permittedImports[path]; !ok {
				t.Fatalf("%s imports %q, which is not on the allowlist. Nothing in this package may be able to end a run, close a session or reach a Terrarium; if this import is genuinely needed, justify it and widen the list deliberately.", name, path)
			}
		}
	}

	// Without this the loop could inspect nothing and pass. The count is a
	// literal the test owns, so deleting a file to make the check green fails
	// it instead.
	if inspected < 3 {
		t.Fatalf("inspected %d non-test files, want at least 3; the allowlist assertion above ran against almost nothing", inspected)
	}
}
