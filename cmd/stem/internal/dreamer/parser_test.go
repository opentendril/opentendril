package dreamer

import (
	"strings"
	"testing"
)

func TestGoParserExtractsStructuralStubs(t *testing.T) {
	content := []byte(`package sample

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

	if len(symbols) != 3 {
		t.Fatalf("symbol count mismatch: got %d want 3", len(symbols))
	}
	if symbols[0].Name != "Worker" || symbols[0].Type != "interface" {
		t.Fatalf("unexpected first symbol: %+v", symbols[0])
	}
	if !strings.Contains(symbols[2].StubContent, "func (j Job) Execute(count int) error") {
		t.Fatalf("method stub missing signature: %q", symbols[2].StubContent)
	}
}

func TestRegexParserExtractsPythonAndJavaScriptStubs(t *testing.T) {
	parser := NewRegexParser()

	pythonSymbols, err := parser.Parse("module.py", []byte("class Dreamer:\n    pass\n\ndef distill(repo):\n    return repo\n"))
	if err != nil {
		t.Fatalf("Parse python returned error: %v", err)
	}
	if len(pythonSymbols) != 2 {
		t.Fatalf("python symbol count mismatch: got %d want 2", len(pythonSymbols))
	}

	javaScriptSymbols, err := parser.Parse("module.js", []byte("export class Dreamer {}\nexport function distill(repo) { return repo }\n"))
	if err != nil {
		t.Fatalf("Parse js returned error: %v", err)
	}
	if len(javaScriptSymbols) != 2 {
		t.Fatalf("javascript symbol count mismatch: got %d want 2", len(javaScriptSymbols))
	}
}
