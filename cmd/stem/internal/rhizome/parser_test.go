package rhizome

import (
	"strings"
	"testing"
)

func TestGoParserExtractsStructuralStubs(t *testing.T) {
	content := []byte(`package sample

// Worker does some work.
type Worker interface {
	Run(ctx context.Context) error
}

type Job struct {
	Name string
}

func (j Job) Execute(count int) error {
	return nil
}
`)

	symbols, err := GoParser{}.Parse("sample.go", content)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if len(symbols) != 4 {
		t.Fatalf("symbol count mismatch: got %d want 4", len(symbols))
	}
	if symbols[0].Name != "file_context" || symbols[0].Type != "file_context" {
		t.Fatalf("unexpected first symbol: %+v", symbols[0])
	}
	if symbols[1].Name != "Worker" || symbols[1].Type != "interface" {
		t.Fatalf("unexpected second symbol: %+v", symbols[1])
	}
	if !strings.Contains(symbols[1].StubContent, "Worker does some work.") {
		t.Fatalf("missing docstring in Worker stub: %q", symbols[1].StubContent)
	}
	if !strings.Contains(symbols[3].StubContent, "func (j Job) Execute(count int) error") {
		t.Fatalf("method stub missing signature: %q", symbols[3].StubContent)
	}
}

func TestRegexParserExtractsPythonAndJavaScriptStubs(t *testing.T) {
	parser := NewRegexParser()

	pythonSymbols, err := parser.Parse("module.py", []byte("import sys\nfrom os import path\nclass Rhizome:\n    \"\"\"A rhizome class.\"\"\"\n    pass\n\ndef distill(repo):\n    return repo\n"))
	if err != nil {
		t.Fatalf("Parse python returned error: %v", err)
	}
	if len(pythonSymbols) != 3 {
		t.Fatalf("python symbol count mismatch: got %d want 3", len(pythonSymbols))
	}
	if pythonSymbols[0].Name != "file_context" {
		t.Fatalf("missing python file_context")
	}
	if !strings.Contains(pythonSymbols[1].StubContent, "\"\"\"A rhizome class.\"\"\"") {
		t.Fatalf("missing python docstring: %q", pythonSymbols[1].StubContent)
	}

	javaScriptSymbols, err := parser.Parse("module.js", []byte("import { x } from 'y'\n/**\n * Rhizome\n */\nexport class Rhizome {}\nexport function distill(repo) { return repo }\n"))
	if err != nil {
		t.Fatalf("Parse js returned error: %v", err)
	}
	if len(javaScriptSymbols) != 3 {
		t.Fatalf("javascript symbol count mismatch: got %d want 3", len(javaScriptSymbols))
	}
	if javaScriptSymbols[0].Name != "file_context" {
		t.Fatalf("missing javascript file_context")
	}
	if !strings.Contains(javaScriptSymbols[1].StubContent, "/**\n * Rhizome\n */") {
		t.Fatalf("missing javascript docstring: %q", javaScriptSymbols[1].StubContent)
	}
}
