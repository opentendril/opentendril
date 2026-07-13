package rhizome

import (
	"fmt"
	"path/filepath"
)

// PrecomputedParser serves symbols that were computed ahead of a scan by a
// repo-level batch pre-pass (the Conductor's tree-sitter terrarium) through
// the standard Parser seam. It holds a finished map of relative path →
// symbols, so Parse never spawns anything: Supports answers membership, Parse
// returns the stored entry. Rhizome itself stays Docker-free — whoever built
// the map dealt with containers; this type only replays the result.
//
// Keys are slash-separated paths relative to the repository root, exactly as
// ScanRepository normalizes them (filepath.ToSlash of the root-relative path).
type PrecomputedParser struct {
	symbolsByPath map[string][]Symbol
}

// NewPrecomputedParser wraps a finished path→symbols map in a Parser. Only
// Name/Type/LineStart/LineEnd/StubContent need to be populated on the
// symbols; ScanRepository stamps RepositoryName and FilePath itself.
func NewPrecomputedParser(symbolsByPath map[string][]Symbol) PrecomputedParser {
	if symbolsByPath == nil {
		symbolsByPath = map[string][]Symbol{}
	}
	return PrecomputedParser{symbolsByPath: symbolsByPath}
}

// Supports reports whether the pre-pass produced symbols for this path. Files
// the pre-pass skipped (parse failure, size cap, unknown extension) are not
// claimed, so scanner precedence lets the regex parser catch them.
func (p PrecomputedParser) Supports(path string) bool {
	_, ok := p.symbolsByPath[filepath.ToSlash(path)]
	return ok
}

// Parse returns the precomputed symbols for path. The file content is ignored
// — parsing already happened in the terrarium. A copy is returned so the
// caller's post-Parse stamping never mutates the shared map.
func (p PrecomputedParser) Parse(path string, _ []byte) ([]Symbol, error) {
	entry, ok := p.symbolsByPath[filepath.ToSlash(path)]
	if !ok {
		return nil, fmt.Errorf("no precomputed symbols for %q", path)
	}
	symbols := make([]Symbol, len(entry))
	copy(symbols, entry)
	return symbols, nil
}
