package rhizome

import (
	"bytes"
	"reflect"
	"testing"
)

func TestTreeSitterParserSupportsMirrorsRegexCoverage(t *testing.T) {
	parser := NewTreeSitterParser()
	for _, path := range []string{"a.py", "b.js", "c.jsx", "d.mjs", "e.cjs", "f.ts", "g.tsx", "h.mts", "i.cts", "UPPER.PY"} {
		if !parser.Supports(path) {
			t.Fatalf("expected Supports(%q) to be true", path)
		}
	}
	for _, path := range []string{"main.go", "README.md", "script.rb", "noextension"} {
		if parser.Supports(path) {
			t.Fatalf("expected Supports(%q) to be false", path)
		}
	}
}

func TestTreeSitterParserExtractsPythonSymbols(t *testing.T) {
	source := []byte("import os\n\nclass Point:\n    def magnitude(self):\n        return 0\n\ndef load():\n    pass\n")
	symbols, err := NewTreeSitterParser().Parse("models.py", source)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	byName := make(map[string]Symbol, len(symbols))
	for _, symbol := range symbols {
		byName[symbol.Name] = symbol
	}
	if byName["file_context"].StubContent != "Imports: import os" {
		t.Fatalf("file_context stub mismatch: %+v", byName["file_context"])
	}
	if byName["Point"].Type != "class" {
		t.Fatalf("expected Point to be a class, got %+v", byName["Point"])
	}
	if byName["magnitude"].Type != "method" {
		t.Fatalf("expected magnitude to be a method, got %+v", byName["magnitude"])
	}
	if byName["load"].Type != "function" {
		t.Fatalf("expected load to be a function, got %+v", byName["load"])
	}
	if byName["Point"].LineStart != 3 || byName["Point"].LineEnd != 5 {
		t.Fatalf("Point line span mismatch: %+v", byName["Point"])
	}
}

func TestTreeSitterParserNeverErrorsOnBrokenSource(t *testing.T) {
	// tree-sitter recovers from syntax errors; whatever happens, Parse must
	// not return an error, because ScanRepository fails the whole scan on one.
	broken := []byte("def (((\n  class ??? {{{\n")
	if _, err := NewTreeSitterParser().Parse("broken.py", broken); err != nil {
		t.Fatalf("Parse must degrade, not error; got %v", err)
	}
}

func TestTreeSitterParserFallsBackToRegexOnOversizedFile(t *testing.T) {
	// A file over the parse.js-mirrored 2 MiB cap is left to the regex
	// extraction, so its symbols must be exactly RegexParser's.
	padding := bytes.Repeat([]byte("# filler\n"), (maxTreeSitterFileBytes/9)+1)
	source := append([]byte("def visible():\n    pass\n"), padding...)
	if len(source) <= maxTreeSitterFileBytes {
		t.Fatal("fixture must exceed the size cap")
	}

	got, err := NewTreeSitterParser().Parse("big.py", source)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	want, err := NewRegexParser().Parse("big.py", source)
	if err != nil {
		t.Fatalf("regex Parse returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("oversized file must produce the regex extraction.\ngot  %+v\nwant %+v", got, want)
	}
}
