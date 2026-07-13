package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/rhizome"
	"github.com/opentendril/core/cmd/stem/internal/terrarium"
)

// treeSitterImage is the batch-parse image that gives Rhizome high-fidelity
// symbols for non-Go languages. See sprouts/tree-sitter/Dockerfile: pinned
// web-tree-sitter wasm grammars for Python/JavaScript/TypeScript/TSX plus the
// parse.js NDJSON emitter.
const treeSitterImage = "opentendril-tree-sitter:latest"

// treeSitterScanTimeout bounds the single batch parse over the whole
// workspace. One container run covers every file, so this is a per-scan
// budget, not per-file.
const treeSitterScanTimeout = 5 * time.Minute

// treeSitterSymbolTypes is the Rhizome symbol vocabulary the pre-pass is
// allowed to emit. Rows with any other type are dropped so a grammar drift in
// the image can never leak novel types into the index or the repo map
// renderer's sorting rules.
var treeSitterSymbolTypes = map[string]struct{}{
	"function":     {},
	"method":       {},
	"class":        {},
	"interface":    {},
	"type":         {},
	"struct":       {},
	"file_context": {},
}

// runTreeSitterScan executes the tree-sitter batch parser once over the
// workspace in an idle terrarium, exactly like a verifier step: workspace
// mounted read-only at /app, no network, hard timeout. It returns the raw
// CommandResult; parseTreeSitterOutput turns it into symbols. No LLM is
// involved and nothing is written back to the host.
func runTreeSitterScan(ctx context.Context, providerName, workspacePath string) (terrarium.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ensureSproutImageFn(ctx, treeSitterImage); err != nil {
		return terrarium.CommandResult{}, fmt.Errorf("build tree-sitter image: %w", err)
	}

	provider, err := terrarium.NewProvider(ctx, providerName)
	if err != nil {
		return terrarium.CommandResult{}, fmt.Errorf("resolve terrarium provider for tree-sitter scan: %w", err)
	}

	spec := terrarium.TerrariumSpec{
		Image:         treeSitterImage,
		WorkingDir:    "/app",
		NetworkMode:   terrarium.NetworkModeNone,
		CPUQuota:      "1.0",
		MemoryLimitMB: 2048,
		PidsLimit:     256,
		Timeout:       treeSitterScanTimeout,
		Mounts: []terrarium.MountSpec{
			// Read-only: the batch parser reads and reports, it never writes
			// the workspace.
			{Source: workspacePath, Target: "/app", ReadOnly: true},
		},
	}

	instance, err := provider.Create(ctx, spec)
	if err != nil {
		return terrarium.CommandResult{}, fmt.Errorf("start tree-sitter terrarium: %w", err)
	}
	defer func() { _ = instance.Stop(context.Background()) }()

	result, runErr := instance.Run(ctx, terrarium.CommandSpec{
		Command:    []string{"node", "/opt/opentendril/parse.js", "/app"},
		WorkingDir: "/app",
		Timeout:    treeSitterScanTimeout,
	})
	if runErr != nil {
		return terrarium.CommandResult{}, fmt.Errorf("run tree-sitter batch parse: %w", runErr)
	}
	return result, nil
}

// treeSitterFileRow mirrors one NDJSON line of the parse.js contract.
type treeSitterFileRow struct {
	Path    string                `json:"path"`
	Symbols []treeSitterSymbolRow `json:"symbols"`
}

type treeSitterSymbolRow struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	LineStart int    `json:"lineStart"`
	LineEnd   int    `json:"lineEnd"`
	Stub      string `json:"stub"`
}

// parseTreeSitterOutput turns a completed batch-parse run into the
// path→symbols map a rhizome.PrecomputedParser wraps. Pure function fed by
// fixtures in tests — no Docker involved.
//
// Tolerance rules: a malformed line, an unsafe path, or a symbol with an
// unknown type drops just that line/symbol (the regex parser still covers the
// file), but a failed run — nonzero exit or timeout — is an error so the
// caller falls back to DefaultParsers entirely. Symbols carry only
// name/type/lines/stub; ScanRepository stamps RepositoryName and FilePath.
func parseTreeSitterOutput(result terrarium.CommandResult) (map[string][]rhizome.Symbol, error) {
	if result.TimedOut {
		return nil, fmt.Errorf("tree-sitter batch parse timed out")
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("tree-sitter batch parse exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	symbolsByPath := make(map[string][]rhizome.Symbol)
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var row treeSitterFileRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			// A malformed or truncated line loses one file, never the batch.
			continue
		}
		relativePath := path.Clean(strings.TrimSpace(row.Path))
		if !isSafeRelativePath(relativePath) {
			continue
		}

		symbols := make([]rhizome.Symbol, 0, len(row.Symbols))
		for _, symbolRow := range row.Symbols {
			if strings.TrimSpace(symbolRow.Name) == "" {
				continue
			}
			if _, known := treeSitterSymbolTypes[symbolRow.Type]; !known {
				continue
			}
			lineStart := symbolRow.LineStart
			if lineStart < 1 {
				lineStart = 1
			}
			lineEnd := symbolRow.LineEnd
			if lineEnd < lineStart {
				lineEnd = lineStart
			}
			symbols = append(symbols, rhizome.Symbol{
				Name:        symbolRow.Name,
				Type:        symbolRow.Type,
				LineStart:   lineStart,
				LineEnd:     lineEnd,
				StubContent: symbolRow.Stub,
			})
		}
		symbolsByPath[relativePath] = symbols
	}
	return symbolsByPath, nil
}

// isSafeRelativePath rejects paths that could not have come from an honest
// walk of the mounted workspace: absolute paths, parent traversal, or empty
// after cleaning.
func isSafeRelativePath(cleaned string) bool {
	if cleaned == "" || cleaned == "." {
		return false
	}
	if strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "..") {
		return false
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// scanRepositoryParsers assembles the parser slice for a Rhizome scan. It
// attempts the tree-sitter batch pre-pass and, on success, injects the
// precomputed symbols between the native Go parser and the regex fallback —
// first-match precedence keeps Go on go/ast, gives covered non-Go files
// tree-sitter fidelity, and leaves everything else to the regex parser.
//
// On ANY failure (no Docker on a released binary, image build failure, bad
// output, timeout) it warns on stderr and returns DefaultParsers, so a bare
// docker-less `tendril repomap` behaves exactly as before this pre-pass
// existed. Workspaces with no tree-sitter-eligible files skip the container
// entirely.
func scanRepositoryParsers(ctx context.Context, mountPath string) []rhizome.Parser {
	if !workspaceHasExtension(mountPath, ".py", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts") {
		return rhizome.DefaultParsers()
	}

	result, err := runTreeSitterScanFn(ctx, "", mountPath)
	if err == nil {
		var symbolsByPath map[string][]rhizome.Symbol
		symbolsByPath, err = parseTreeSitterOutput(result)
		if err == nil {
			return []rhizome.Parser{
				rhizome.GoParser{},
				rhizome.NewPrecomputedParser(symbolsByPath),
				rhizome.NewRegexParser(),
			}
		}
	}

	fmt.Fprintf(os.Stderr, "⚠️  Tree-sitter pre-pass unavailable, falling back to built-in parsers: %v\n", err)
	return rhizome.DefaultParsers()
}
