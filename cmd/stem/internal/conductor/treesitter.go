package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
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

// treeSitterExtensions is the file-extension coverage of the batch pre-pass;
// it mirrors parse.js LANGUAGE_BY_EXTENSION (and the regex fallback's
// Supports set, so pre-pass coverage never exceeds what regex would claim).
var treeSitterExtensions = []string{".py", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}

// treeSitterSupports reports whether the batch pre-pass can parse path.
func treeSitterSupports(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	for _, candidate := range treeSitterExtensions {
		if extension == candidate {
			return true
		}
	}
	return false
}

// treeSitterCommand builds the parse.js invocation for a scan. A nil
// changedPaths requests the container's own full workspace walk (the
// cold-index path); a non-nil list switches parse.js to --stdin mode with the
// newline-separated root-relative paths as the process stdin, so an
// incremental re-scan parses only the delta. Pure function so tests can pin
// the stdin protocol without Docker.
func treeSitterCommand(changedPaths []string) (command []string, stdin []byte) {
	command = []string{"node", "/opt/opentendril/parse.js", "/app"}
	if changedPaths == nil {
		return command, nil
	}
	command = append(command, "--stdin")
	return command, []byte(strings.Join(changedPaths, "\n") + "\n")
}

// runTreeSitterScan executes the tree-sitter batch parser once over the
// workspace in an idle terrarium, exactly like a verifier step: workspace
// mounted read-only at /app, no network, hard timeout. A nil changedPaths
// parses the whole workspace; a non-nil list parses exactly that subset (see
// treeSitterCommand). It returns the raw CommandResult;
// parseTreeSitterOutput turns it into symbols. No LLM is involved and nothing
// is written back to the host.
func runTreeSitterScan(ctx context.Context, providerName, workspacePath string, changedPaths []string) (terrarium.CommandResult, error) {
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

	command, stdin := treeSitterCommand(changedPaths)
	result, runErr := instance.Run(ctx, terrarium.CommandSpec{
		Command:    command,
		WorkingDir: "/app",
		Stdin:      stdin,
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

// treeSitterBatchParser is the terrarium-backed rhizome.BatchParser: one
// container run parses either the whole workspace (nil paths, the cold-index
// walk) or exactly the changed subset delivered to parse.js over stdin. It is
// the only place the repo-level pre-pass touches Docker — rhizome sees just
// the interface.
type treeSitterBatchParser struct{}

var _ rhizome.BatchParser = treeSitterBatchParser{}

// Supports reports whether the pre-pass covers path (mirrors parse.js
// LANGUAGE_BY_EXTENSION).
func (treeSitterBatchParser) Supports(path string) bool {
	return treeSitterSupports(path)
}

// ParseBatch runs the tree-sitter terrarium over root and returns the parsed
// symbols keyed by root-relative slash path. Per the BatchParser contract, a
// non-nil empty path list parses nothing — no container is started.
func (treeSitterBatchParser) ParseBatch(ctx context.Context, root string, paths []string) (map[string][]rhizome.Symbol, error) {
	if paths != nil && len(paths) == 0 {
		return map[string][]rhizome.Symbol{}, nil
	}
	result, err := runTreeSitterScanFn(ctx, "", root, paths)
	if err != nil {
		return nil, err
	}
	return parseTreeSitterOutput(result)
}

// treeSitterEngineEnv selects which tree-sitter engine feeds a Rhizome scan.
// Unset (or any value other than "terrarium") means the in-process pure-Go
// engine inside rhizome.DefaultParsers — no container at all. "terrarium"
// restores the container batch pre-pass, kept fully usable until in-process
// parity has soaked in the wild (the two engines are pinned to the same
// golden fixture either way).
const treeSitterEngineEnv = "OTTS_TREESITTER_ENGINE"

// scanRepositoryParsers assembles the parser slice for a Rhizome scan.
//
// Default engine (in-process): rhizome.DefaultParsers already carries the
// pure-Go tree-sitter engine, so covered non-Go files get tree-sitter
// fidelity with no docker involved and per-file hash-skip incrementality.
//
// OTTS_TREESITTER_ENGINE=terrarium: attempts the tree-sitter container batch
// pre-pass and, on success, injects the precomputed symbols between the
// native Go parser and the regex fallback — first-match precedence keeps Go
// on go/ast, gives covered non-Go files the container's symbols, and leaves
// everything else to the regex parser.
//
// The pre-pass is incremental: ChangedPaths replays the scanner's own
// hash-vs-store comparison, so the container parses only the files
// ScanRepository is about to re-parse. Three regimes fall out:
//   - cold index (no stored records for eligible files): full container walk,
//     exactly as before this slice;
//   - warm index, nothing changed: no container at all — every eligible file
//     will hash-skip, so precomputing symbols for it would be pure waste;
//   - warm index with a delta: the changed list goes to parse.js over stdin
//     and only that subset is parsed.
//
// On ANY failure (no Docker on a released binary, image build failure, bad
// output, timeout) it warns on stderr and returns DefaultParsers, so a bare
// docker-less `tendril repomap` behaves exactly as before this pre-pass
// existed. Workspaces with no tree-sitter-eligible files skip the container
// entirely.
func scanRepositoryParsers(ctx context.Context, mountPath string, repositoryName string, store rhizome.IndexStore) []rhizome.Parser {
	if os.Getenv(treeSitterEngineEnv) != "terrarium" {
		// In-process engine (the default): DefaultParsers covers Go,
		// tree-sitter and regex with no container.
		return rhizome.DefaultParsers()
	}
	if !workspaceHasExtension(mountPath, treeSitterExtensions...) {
		return rhizome.DefaultParsers()
	}

	batch := treeSitterBatchParser{}
	changed, warmIndex, err := rhizome.ChangedPaths(ctx, mountPath, repositoryName, store, batch.Supports)
	if err == nil {
		if warmIndex && len(changed) == 0 {
			// Incremental no-op: every eligible file is unchanged and will
			// hash-skip inside ScanRepository, so skip the container entirely.
			return rhizome.DefaultParsers()
		}
		paths := changed
		if !warmIndex {
			// Cold index: let parse.js walk the workspace itself instead of
			// shipping the (identical) full file list over stdin.
			paths = nil
		}

		var symbolsByPath map[string][]rhizome.Symbol
		symbolsByPath, err = batch.ParseBatch(ctx, mountPath, paths)
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
