package rhizome

import (
	"testing"
)

func TestPrecomputedParserSupportsOnlyKnownPaths(t *testing.T) {
	parser := NewPrecomputedParser(map[string][]Symbol{
		"src/service.py": {{Name: "Distiller", Type: "class", LineStart: 4, LineEnd: 9}},
		"src/empty.py":   {},
	})

	if !parser.Supports("src/service.py") {
		t.Fatal("expected a covered path to be supported")
	}
	if !parser.Supports("src/empty.py") {
		t.Fatal("expected a covered zero-symbol path to be supported (tree-sitter is authoritative for it)")
	}
	if parser.Supports("src/unknown.py") {
		t.Fatal("expected an uncovered path to be unsupported so the regex parser catches it")
	}
	if parser.Supports("service.py") {
		t.Fatal("expected a different relative path to be unsupported")
	}
}

func TestPrecomputedParserParseReturnsStoredSymbolsAsCopy(t *testing.T) {
	stored := map[string][]Symbol{
		"src/service.py": {{Name: "Distiller", Type: "class", LineStart: 4, LineEnd: 9, StubContent: "class Distiller:"}},
	}
	parser := NewPrecomputedParser(stored)

	symbols, err := parser.Parse("src/service.py", []byte("content is ignored"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "Distiller" || symbols[0].Type != "class" {
		t.Fatalf("unexpected symbols: %+v", symbols)
	}

	// Post-Parse stamping (RepositoryName/FilePath) must not leak back into
	// the shared map across rescans.
	symbols[0].RepositoryName = "stamped"
	symbols[0].FilePath = "stamped"
	if stored["src/service.py"][0].RepositoryName != "" || stored["src/service.py"][0].FilePath != "" {
		t.Fatal("mutating Parse output mutated the precomputed map")
	}

	if _, err := parser.Parse("src/unknown.py", nil); err == nil {
		t.Fatal("expected Parse of an uncovered path to error")
	}
}

func TestPrecomputedParserHandlesNilMap(t *testing.T) {
	parser := NewPrecomputedParser(nil)
	if parser.Supports("anything.py") {
		t.Fatal("expected a nil-map parser to support nothing")
	}
}

func TestParserPrecedenceGoThenPrecomputedThenRegex(t *testing.T) {
	precomputed := NewPrecomputedParser(map[string][]Symbol{
		"src/service.py": {{Name: "Distiller", Type: "class", LineStart: 4, LineEnd: 9}},
		// Even a covered .go path must lose to the native Go parser.
		"main.go": {{Name: "bogus", Type: "function", LineStart: 1, LineEnd: 1}},
	})
	parsers := []Parser{GoParser{}, precomputed, NewRegexParser()}

	if _, ok := parserForPath("main.go", parsers).(GoParser); !ok {
		t.Fatalf("expected GoParser to win for .go files, got %T", parserForPath("main.go", parsers))
	}
	if _, ok := parserForPath("src/service.py", parsers).(PrecomputedParser); !ok {
		t.Fatalf("expected PrecomputedParser to win for covered files, got %T", parserForPath("src/service.py", parsers))
	}
	if _, ok := parserForPath("src/uncovered.ts", parsers).(RegexParser); !ok {
		t.Fatalf("expected RegexParser to catch uncovered files, got %T", parserForPath("src/uncovered.ts", parsers))
	}
	if parserForPath("README.md", parsers) != nil {
		t.Fatal("expected no parser for an unrelated file")
	}
}
