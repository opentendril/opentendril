package rhizome

// In-process tree-sitter engine (#183 slice 4, the Option-2 endgame).
//
// TreeSitterParser gives non-Go files the same tree-sitter fidelity as the
// terrarium batch pre-pass (sprouts/tree-sitter/parse.js) without a container:
// it drives the gotreesitter pure-Go tree-sitter runtime, so `go build` stays
// cgo-free and plain GOOS/GOARCH cross-compiles keep working. The extraction
// walkers below are a line-for-line port of parse.js — the two engines must
// emit identical symbols, and conductor's native golden test pins that parity
// against the same testdata/treesittergolden.json fixture the container
// golden test pins.
//
// Engine notes:
//   - Grammars are the gotreesitter registry's embedded blobs (parse tables
//     extracted from the upstream tree-sitter-python/-javascript/-typescript
//     grammars pinned in gotreesitter's grammars/languages.lock; external
//     scanners are hand-written Go). Grammar drift against the container's
//     pinned npm versions is caught by the shared golden fixture, not by
//     version-string comparison.
//   - Per-file error isolation: a file the engine cannot parse falls back to
//     the regex extraction INSIDE Parse rather than returning an error,
//     because ScanRepository hard-fails the whole scan on a parser error (the
//     GoParser contract). One odd file must never sink an index run.
//   - Files above maxTreeSitterFileBytes go to the regex fallback, mirroring
//     parse.js MAX_FILE_BYTES ("larger files are left to the host-side regex
//     parser").

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// maxTreeSitterFileBytes mirrors parse.js MAX_FILE_BYTES: anything larger is
// left to the regex fallback.
const maxTreeSitterFileBytes = 2 * 1024 * 1024

// maxTreeSitterStubLength mirrors parse.js MAX_STUB_LENGTH.
const maxTreeSitterStubLength = 300

// treeSitterLanguageByExtension mirrors parse.js LANGUAGE_BY_EXTENSION.
var treeSitterLanguageByExtension = map[string]string{
	".py":  "python",
	".js":  "javascript",
	".jsx": "javascript",
	".mjs": "javascript",
	".cjs": "javascript",
	".ts":  "typescript",
	".mts": "typescript",
	".cts": "typescript",
	".tsx": "tsx",
}

// TreeSitterParser is the in-process tree-sitter Parser for Python,
// JavaScript, TypeScript and TSX. It sits between GoParser and RegexParser in
// DefaultParsers: Go keeps go/ast, covered non-Go files get tree-sitter
// fidelity, everything else still reaches the regex parser through
// first-match precedence.
type TreeSitterParser struct {
	fallback RegexParser

	mu        sync.Mutex
	languages map[string]*sitter.Language
}

// NewTreeSitterParser returns a TreeSitterParser with lazily loaded grammars:
// a language's parse tables are deserialized from the embedded blob on the
// first file that needs them and cached for the rest of the process.
func NewTreeSitterParser() *TreeSitterParser {
	return &TreeSitterParser{
		fallback:  NewRegexParser(),
		languages: make(map[string]*sitter.Language),
	}
}

// Supports mirrors parse.js LANGUAGE_BY_EXTENSION (which is also exactly the
// RegexParser coverage, so the engine never claims a file regex would not).
func (p *TreeSitterParser) Supports(path string) bool {
	_, ok := treeSitterLanguageByExtension[strings.ToLower(filepath.Ext(path))]
	return ok
}

// Parse extracts symbols with the in-process tree-sitter engine. It never
// returns an error for a file-level problem: any failure (oversized file,
// grammar load error, parser error, runtime panic) degrades to the regex
// extraction for just that file, so a scan can never be sunk by one input.
func (p *TreeSitterParser) Parse(path string, content []byte) ([]Symbol, error) {
	if len(content) > maxTreeSitterFileBytes {
		return p.fallback.Parse(path, content)
	}
	languageName, ok := treeSitterLanguageByExtension[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return p.fallback.Parse(path, content)
	}
	symbols, err := p.parseWithEngine(languageName, content)
	if err != nil {
		return p.fallback.Parse(path, content)
	}
	return symbols, nil
}

// parseWithEngine runs the gotreesitter parse and the parse.js-ported symbol
// walk, converting any panic in the third-party runtime into an error so the
// caller can fall back per file.
func (p *TreeSitterParser) parseWithEngine(languageName string, content []byte) (symbols []Symbol, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			symbols = nil
			err = fmt.Errorf("tree-sitter engine panic: %v", recovered)
		}
	}()

	language, err := p.language(languageName)
	if err != nil {
		return nil, err
	}
	parser := sitter.NewParser(language)
	tree, err := parser.Parse(content)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse: %w", err)
	}
	if tree == nil || tree.RootNode() == nil {
		return nil, fmt.Errorf("tree-sitter parse returned no tree")
	}
	walker := &treeSitterWalker{language: language, source: content}
	return walker.extractSymbols(tree.RootNode(), languageName), nil
}

// language returns the cached grammar for languageName, loading it from the
// embedded registry blob on first use.
func (p *TreeSitterParser) language(languageName string) (*sitter.Language, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if language, ok := p.languages[languageName]; ok {
		return language, nil
	}
	var language *sitter.Language
	switch languageName {
	case "python":
		language = grammars.PythonLanguage()
	case "javascript":
		language = grammars.JavascriptLanguage()
	case "typescript":
		language = grammars.TypescriptLanguage()
	case "tsx":
		language = grammars.TsxLanguage()
	}
	if language == nil {
		return nil, fmt.Errorf("tree-sitter grammar %q unavailable", languageName)
	}
	p.languages[languageName] = language
	return language, nil
}

// treeSitterWalker ports the parse.js extraction walkers. Method names and
// traversal order deliberately track the parse.js functions of the same name
// so the two engines stay reviewable side by side.
type treeSitterWalker struct {
	language *sitter.Language
	source   []byte
}

func (w *treeSitterWalker) text(node *sitter.Node) string {
	return string(w.source[node.StartByte():node.EndByte()])
}

func (w *treeSitterWalker) nodeType(node *sitter.Node) string {
	return node.Type(w.language)
}

func (w *treeSitterWalker) namedChildren(node *sitter.Node) []*sitter.Node {
	count := node.NamedChildCount()
	children := make([]*sitter.Node, 0, count)
	for index := 0; index < count; index++ {
		children = append(children, node.NamedChild(index))
	}
	return children
}

func (w *treeSitterWalker) childByField(node *sitter.Node, field string) *sitter.Node {
	return node.ChildByFieldName(field, w.language)
}

func (w *treeSitterWalker) fieldText(node *sitter.Node, field string) string {
	child := w.childByField(node, field)
	if child == nil {
		return ""
	}
	return w.text(child)
}

// previousNamedSibling mirrors web-tree-sitter's previousNamedSibling
// (comments count: they are named "extra" nodes).
func (w *treeSitterWalker) previousNamedSibling(node *sitter.Node) *sitter.Node {
	sibling := node.PrevSibling()
	for sibling != nil && !sibling.IsNamed() {
		sibling = sibling.PrevSibling()
	}
	return sibling
}

func treeSitterFirstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index != -1 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

// treeSitterCapStub mirrors parse.js capStub. parse.js counts JavaScript
// string code units; runes are identical for ASCII and any BMP source.
func treeSitterCapStub(text string) string {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) <= maxTreeSitterStubLength {
		return trimmed
	}
	return string(runes[:maxTreeSitterStubLength]) + "…"
}

// treeSitterFirstJoined mirrors parse.js firstJoined: every line trimmed,
// blanks dropped, joined with single spaces.
func treeSitterFirstJoined(raw string) string {
	lines := strings.Split(raw, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

// signatureOf renders the declaration head — everything before the body —
// collapsed onto one line, so multi-line parameter lists stay readable stubs.
func (w *treeSitterWalker) signatureOf(node *sitter.Node) string {
	end := node.EndByte()
	if body := w.childByField(node, "body"); body != nil {
		end = body.StartByte()
	}
	return treeSitterCapStub(treeSitterFirstJoined(string(w.source[node.StartByte():end])))
}

// makeSymbolRange records a symbol whose line span starts at startNode and
// ends at endNode. They differ when a declaration carries decorators or an
// `export` prefix: the stub and LineStart come from the widened head node
// while LineEnd still tracks the declaration body.
func makeTreeSitterSymbol(name, symbolType string, startNode, endNode *sitter.Node, stub string) Symbol {
	return Symbol{
		Name:        name,
		Type:        symbolType,
		LineStart:   int(startNode.StartPoint().Row) + 1,
		LineEnd:     int(endNode.EndPoint().Row) + 1,
		StubContent: stub,
	}
}

// --- Python -----------------------------------------------------------------

func (w *treeSitterWalker) pythonDocstring(node *sitter.Node) string {
	body := w.childByField(node, "body")
	if body == nil || body.NamedChildCount() == 0 {
		return ""
	}
	candidate := body.NamedChild(0)
	// Upstream tree-sitter-python wraps a docstring statement as
	// expression_statement > string; gotreesitter's materialized tree collapses
	// the single-expression statement to a bare string. Accept both shapes so
	// the walk matches parse.js output on either engine.
	if w.nodeType(candidate) == "expression_statement" {
		if candidate.NamedChildCount() == 0 {
			return ""
		}
		candidate = candidate.NamedChild(0)
	}
	if w.nodeType(candidate) != "string" {
		return ""
	}
	return treeSitterFirstLine(w.text(candidate))
}

func (w *treeSitterWalker) walkPython(node *sitter.Node, insideClass bool, symbols *[]Symbol, imports *[]string) {
	switch w.nodeType(node) {
	case "import_statement", "import_from_statement":
		*imports = append(*imports, treeSitterFirstLine(w.text(node)))
		return
	case "decorated_definition":
		// The decorators are siblings of the wrapped definition; fold their
		// source lines into the stub and let the decorated_definition node own
		// the line span so LineStart points at the first decorator.
		var decorators []string
		var definition *sitter.Node
		for _, child := range w.namedChildren(node) {
			switch w.nodeType(child) {
			case "decorator":
				decorators = append(decorators, treeSitterFirstLine(w.text(child)))
			case "class_definition", "function_definition":
				if definition == nil {
					definition = child
				}
			}
		}
		if definition != nil {
			w.emitPythonDefinition(definition, node, insideClass, decorators, symbols, imports)
		}
		return
	case "class_definition", "function_definition":
		w.emitPythonDefinition(node, node, insideClass, nil, symbols, imports)
		return
	default:
		// Every other container falls through here so nested declarations keep
		// their enclosing-class context.
		for _, child := range w.namedChildren(node) {
			w.walkPython(child, insideClass, symbols, imports)
		}
	}
}

// emitPythonDefinition records one class/function symbol and then walks its
// body for nested declarations. rangeNode is the decorated_definition when
// decorators are present (so the line span covers the decorator lines),
// otherwise the definition itself. A class body yields methods; a function
// body yields functions.
func (w *treeSitterWalker) emitPythonDefinition(definition, rangeNode *sitter.Node, insideClass bool, decorators []string, symbols *[]Symbol, imports *[]string) {
	isClass := w.nodeType(definition) == "class_definition"
	name := w.fieldText(definition, "name")
	if name != "" {
		stub := w.signatureOf(definition)
		if len(decorators) > 0 {
			stub = strings.Join(decorators, "\n") + "\n" + stub
		}
		if doc := w.pythonDocstring(definition); doc != "" {
			stub = doc + "\n" + stub
		}
		symbolType := "function"
		if isClass {
			symbolType = "class"
		} else if insideClass {
			symbolType = "method"
		}
		*symbols = append(*symbols, makeTreeSitterSymbol(name, symbolType, rangeNode, rangeNode, stub))
	}
	if body := w.childByField(definition, "body"); body != nil {
		for _, child := range w.namedChildren(body) {
			w.walkPython(child, isClass, symbols, imports)
		}
	}
}

// --- JavaScript / TypeScript / TSX -------------------------------------------

func (w *treeSitterWalker) scriptDocComment(node *sitter.Node) string {
	anchor := node
	if parent := anchor.Parent(); parent != nil && w.nodeType(parent) == "export_statement" {
		anchor = parent
	}
	// A JSDoc block sits above any method decorators, so step past them.
	sibling := w.previousNamedSibling(anchor)
	for sibling != nil && w.nodeType(sibling) == "decorator" {
		sibling = w.previousNamedSibling(sibling)
	}
	if sibling != nil && w.nodeType(sibling) == "comment" && strings.HasPrefix(w.text(sibling), "/**") {
		return treeSitterCapStub(w.text(sibling))
	}
	return ""
}

// precedingDecorators returns the contiguous decorator nodes immediately
// before node, in source order.
func (w *treeSitterWalker) precedingDecorators(node *sitter.Node) []*sitter.Node {
	var decorators []*sitter.Node
	sibling := w.previousNamedSibling(node)
	for sibling != nil && w.nodeType(sibling) == "decorator" {
		decorators = append([]*sitter.Node{sibling}, decorators...)
		sibling = w.previousNamedSibling(sibling)
	}
	return decorators
}

// scriptHeadNode widens node to the syntax that should open its stub: an
// enclosing `export`/`export default` statement (which also encloses class
// decorators), or the first of any preceding decorator siblings.
func (w *treeSitterWalker) scriptHeadNode(node *sitter.Node) *sitter.Node {
	if parent := node.Parent(); parent != nil && w.nodeType(parent) == "export_statement" {
		return parent
	}
	if decorators := w.precedingDecorators(node); len(decorators) > 0 {
		return decorators[0]
	}
	return node
}

// arrowHeadNode widens an arrow/function-expression variable_declarator to the
// declaration keyword and any `export` prefix. It refuses to widen a
// multi-binding declaration (`const a = ..., b = ...`), where a shared head
// would wrongly fold every binding into each symbol's stub.
func (w *treeSitterWalker) arrowHeadNode(declarator *sitter.Node) *sitter.Node {
	declaration := declarator.Parent()
	if declaration == nil {
		return declarator
	}
	declarationType := w.nodeType(declaration)
	if declarationType != "lexical_declaration" && declarationType != "variable_declaration" {
		return declarator
	}
	bindings := 0
	for _, child := range w.namedChildren(declaration) {
		if w.nodeType(child) == "variable_declarator" {
			bindings++
		}
	}
	if bindings != 1 {
		return declarator
	}
	if parent := declaration.Parent(); parent != nil && w.nodeType(parent) == "export_statement" {
		return parent
	}
	return declaration
}

func (w *treeSitterWalker) pushScriptSymbol(symbols *[]Symbol, name, symbolType string, node *sitter.Node) {
	head := w.scriptHeadNode(node)
	end := node.EndByte()
	if body := w.childByField(node, "body"); body != nil {
		end = body.StartByte()
	}
	stub := treeSitterCapStub(treeSitterFirstJoined(string(w.source[head.StartByte():end])))
	if doc := w.scriptDocComment(node); doc != "" {
		stub = doc + "\n" + stub
	}
	*symbols = append(*symbols, makeTreeSitterSymbol(name, symbolType, head, node, stub))
}

func (w *treeSitterWalker) walkScript(node *sitter.Node, symbols *[]Symbol, imports *[]string) {
	switch w.nodeType(node) {
	case "import_statement":
		*imports = append(*imports, treeSitterFirstLine(w.text(node)))
		return
	case "class_declaration", "abstract_class_declaration":
		if name := w.fieldText(node, "name"); name != "" {
			w.pushScriptSymbol(symbols, name, "class", node)
		}
	case "method_definition":
		if name := w.fieldText(node, "name"); name != "" {
			w.pushScriptSymbol(symbols, name, "method", node)
		}
	case "function_declaration", "generator_function_declaration", "function_signature":
		if name := w.fieldText(node, "name"); name != "" {
			w.pushScriptSymbol(symbols, name, "function", node)
		}
	case "interface_declaration":
		if name := w.fieldText(node, "name"); name != "" {
			w.pushScriptSymbol(symbols, name, "interface", node)
		}
	case "type_alias_declaration", "enum_declaration":
		if name := w.fieldText(node, "name"); name != "" {
			w.pushScriptSymbol(symbols, name, "type", node)
		}
	case "variable_declarator":
		value := w.childByField(node, "value")
		name := w.fieldText(node, "name")
		if name != "" && value != nil {
			valueType := w.nodeType(value)
			if valueType == "arrow_function" || valueType == "function_expression" || valueType == "function" {
				// Widen to the `const`/`let` (and any `export`) so the stub
				// reads `export const identity = (value) =>`, matching
				// function-declaration fidelity.
				head := w.arrowHeadNode(node)
				bodyStart := value.EndByte()
				if body := w.childByField(value, "body"); body != nil {
					bodyStart = body.StartByte()
				}
				stub := treeSitterCapStub(treeSitterFirstJoined(string(w.source[head.StartByte():bodyStart])))
				docAnchor := node.Parent()
				if docAnchor == nil {
					docAnchor = node
				}
				if doc := w.scriptDocComment(docAnchor); doc != "" {
					stub = doc + "\n" + stub
				}
				*symbols = append(*symbols, makeTreeSitterSymbol(name, "function", head, node, stub))
			}
		}
	}
	for _, child := range w.namedChildren(node) {
		w.walkScript(child, symbols, imports)
	}
}

// --- Extraction ----------------------------------------------------------------

// extractSymbols mirrors parse.js extractSymbols: language-specific walk, then
// a file_context pseudo-symbol prepended when any imports were collected.
func (w *treeSitterWalker) extractSymbols(root *sitter.Node, languageName string) []Symbol {
	symbols := make([]Symbol, 0)
	imports := make([]string, 0)
	if languageName == "python" {
		w.walkPython(root, false, &symbols, &imports)
	} else {
		w.walkScript(root, &symbols, &imports)
	}
	if len(imports) > 0 {
		fileContext := Symbol{
			Name:        "file_context",
			Type:        "file_context",
			LineStart:   1,
			LineEnd:     1,
			StubContent: treeSitterCapStub("Imports: " + strings.Join(imports, ", ")),
		}
		symbols = append([]Symbol{fileContext}, symbols...)
	}
	return symbols
}
