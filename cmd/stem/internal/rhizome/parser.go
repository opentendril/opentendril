package rhizome

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

type Parser interface {
	Parse(path string, content []byte) ([]Symbol, error)
	Supports(path string) bool
}

func DefaultParsers() []Parser {
	return []Parser{
		GoParser{},
		NewRegexParser(),
	}
}

type GoParser struct{}

func (GoParser) Supports(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".go")
}

func (GoParser) Parse(path string, content []byte) ([]Symbol, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse Go file: %w", err)
	}

	symbols := make([]Symbol, 0)

	// Generate file_context pseudo-symbol
	var importPaths []string
	for _, imp := range file.Imports {
		if imp.Path != nil {
			importPaths = append(importPaths, imp.Path.Value)
		}
	}
	fileContextStub := fmt.Sprintf("package %s\nImports: %s", file.Name.Name, strings.Join(importPaths, ", "))
	symbols = append(symbols, Symbol{
		Name:        "file_context",
		Type:        "file_context",
		LineStart:   1,
		LineEnd:     1,
		StubContent: fileContextStub,
	})

	for _, decl := range file.Decls {
		switch node := decl.(type) {
		case *ast.GenDecl:
			if node.Tok != token.TYPE {
				continue
			}
			for _, spec := range node.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				symbolType := "type"
				switch typeSpec.Type.(type) {
				case *ast.StructType:
					symbolType = "struct"
				case *ast.InterfaceType:
					symbolType = "interface"
				}
				doc := ""
				if node.Doc != nil {
					doc = strings.TrimSpace(node.Doc.Text())
				}
				stub := renderGoDeclarationStub(fset, node)
				if doc != "" {
					stub = doc + "\n" + stub
				}
				symbols = append(symbols, Symbol{
					Name:        typeSpec.Name.Name,
					Type:        symbolType,
					LineStart:   fset.Position(typeSpec.Pos()).Line,
					LineEnd:     fset.Position(typeSpec.End()).Line,
					StubContent: stub,
				})
			}
		case *ast.FuncDecl:
			symbolType := "function"
			if node.Recv != nil {
				symbolType = "method"
			}
			doc := ""
			if node.Doc != nil {
				doc = strings.TrimSpace(node.Doc.Text())
			}
			stub := renderGoFunctionStub(node)
			if doc != "" {
				stub = doc + "\n" + stub
			}
			symbols = append(symbols, Symbol{
				Name:        node.Name.Name,
				Type:        symbolType,
				LineStart:   fset.Position(node.Pos()).Line,
				LineEnd:     fset.Position(node.End()).Line,
				StubContent: stub,
			})
		}
	}

	return symbols, nil
}

func renderGoDeclarationStub(fset *token.FileSet, node ast.Node) string {
	var buffer bytes.Buffer
	if err := printer.Fprint(&buffer, fset, node); err != nil {
		return ""
	}
	return strings.TrimSpace(buffer.String())
}

func renderGoFunctionStub(node *ast.FuncDecl) string {
	var builder strings.Builder
	builder.WriteString("func ")
	if node.Recv != nil && len(node.Recv.List) > 0 {
		builder.WriteString("(")
		builder.WriteString(renderGoFieldList(node.Recv.List))
		builder.WriteString(") ")
	}
	builder.WriteString(node.Name.Name)
	if node.Type != nil {
		builder.WriteString(renderGoTypeParameters(node.Type.TypeParams))
		builder.WriteString(renderGoParameters(node.Type.Params))
		builder.WriteString(renderGoResults(node.Type.Results))
	}
	return strings.TrimSpace(builder.String())
}

func renderGoFieldList(fields []*ast.Field) string {
	items := make([]string, 0, len(fields))
	for _, field := range fields {
		if rendered := renderGoField(field); rendered != "" {
			items = append(items, rendered)
		}
	}
	return strings.Join(items, ", ")
}

func renderGoField(field *ast.Field) string {
	if field == nil {
		return ""
	}
	fieldType := renderGoExpression(field.Type)
	if fieldType == "" {
		return ""
	}
	if len(field.Names) == 0 {
		return fieldType
	}
	names := make([]string, 0, len(field.Names))
	for _, name := range field.Names {
		if name != nil {
			names = append(names, name.Name)
		}
	}
	return strings.Join(names, ", ") + " " + fieldType
}

func renderGoTypeParameters(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	return "[" + renderGoFieldList(fields.List) + "]"
}

func renderGoParameters(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return "()"
	}
	return "(" + renderGoFieldList(fields.List) + ")"
}

func renderGoResults(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}

	rendered := renderGoFieldList(fields.List)
	if rendered == "" {
		return ""
	}
	if len(fields.List) == 1 && len(fields.List[0].Names) == 0 {
		return " " + rendered
	}
	return " (" + rendered + ")"
}

func renderGoExpression(expr ast.Expr) string {
	var buffer bytes.Buffer
	if expr == nil || printer.Fprint(&buffer, token.NewFileSet(), expr) != nil {
		return ""
	}
	return strings.TrimSpace(buffer.String())
}

type RegexParser struct {
	patterns []regexPattern
}

type regexPattern struct {
	suffix     string
	symbolType string
	expression *regexp.Regexp
}

func NewRegexParser() RegexParser {
	return RegexParser{patterns: []regexPattern{
		{suffix: ".py", symbolType: "class", expression: regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)`)},
		{suffix: ".py", symbolType: "function", expression: regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)},
		{suffix: ".js", symbolType: "class", expression: regexp.MustCompile(`^\s*(?:export\s+default\s+|export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)`)},
		{suffix: ".js", symbolType: "function", expression: regexp.MustCompile(`^\s*(?:export\s+default\s+|export\s+)?(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)},
		{suffix: ".js", symbolType: "function", expression: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s*)?\(`)},
		{suffix: ".ts", symbolType: "interface", expression: regexp.MustCompile(`^\s*(?:export\s+)?interface\s+([A-Za-z_$][A-Za-z0-9_$]*)`)},
		{suffix: ".ts", symbolType: "type", expression: regexp.MustCompile(`^\s*(?:export\s+)?type\s+([A-Za-z_$][A-Za-z0-9_$]*)`)},
		{suffix: ".ts", symbolType: "class", expression: regexp.MustCompile(`^\s*(?:export\s+default\s+|export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)`)},
		{suffix: ".ts", symbolType: "function", expression: regexp.MustCompile(`^\s*(?:export\s+default\s+|export\s+)?(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)},
		{suffix: ".ts", symbolType: "function", expression: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s*)?\(`)},
	}}
}

func (p RegexParser) Supports(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	return extension == ".py" || extension == ".js" || extension == ".jsx" || extension == ".mjs" || extension == ".cjs" || extension == ".ts" || extension == ".tsx" || extension == ".mts" || extension == ".cts"
}

func (p RegexParser) Parse(path string, content []byte) ([]Symbol, error) {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	extension := normalizeRegexExtension(path)
	symbols := make([]Symbol, 0)

	// 1. Extract file_context (imports)
	var imports []string
	importRegexPy := regexp.MustCompile(`^(?:import\s+|from\s+.*?import\s+)(.+)`)
	importRegexJS := regexp.MustCompile(`^(?:import\s+.*from\s+['"]|require\(['"])([^'"]+)`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if extension == ".py" {
			if matches := importRegexPy.FindStringSubmatch(trimmed); len(matches) > 1 {
				imports = append(imports, matches[1])
			}
		} else {
			if matches := importRegexJS.FindStringSubmatch(trimmed); len(matches) > 1 {
				imports = append(imports, matches[1])
			}
		}
	}

	if len(imports) > 0 {
		fileContextStub := fmt.Sprintf("Imports: %s", strings.Join(imports, ", "))
		symbols = append(symbols, Symbol{
			Name:        "file_context",
			Type:        "file_context",
			LineStart:   1,
			LineEnd:     1,
			StubContent: fileContextStub,
		})
	}

	// 2. Extract symbols and heuristic docstrings
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, pattern := range p.patterns {
			if pattern.suffix != extension {
				continue
			}
			matches := pattern.expression.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}
			lineNumber := index + 1
			stub := trimmed

			// Heuristic docstring extraction
			if extension == ".py" && index+1 < len(lines) {
				nextLine := strings.TrimSpace(lines[index+1])
				if strings.HasPrefix(nextLine, `"""`) || strings.HasPrefix(nextLine, `'''`) {
					stub = nextLine + "\n" + stub
				}
			} else if (extension == ".js" || extension == ".ts") && index > 0 {
				prevLine := strings.TrimSpace(lines[index-1])
				if prevLine == "*/" || strings.HasSuffix(prevLine, "*/") {
					for back := index - 1; back >= 0; back-- {
						backLine := strings.TrimSpace(lines[back])
						if strings.HasPrefix(backLine, "/**") {
							docLines := lines[back:index]
							stub = strings.Join(docLines, "\n") + "\n" + stub
							break
						}
					}
				}
			}

			symbols = append(symbols, Symbol{
				Name:        matches[1],
				Type:        pattern.symbolType,
				LineStart:   lineNumber,
				LineEnd:     lineNumber,
				StubContent: stub,
			})
			break
		}
	}

	return symbols, nil
}

func normalizeRegexExtension(path string) string {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".jsx", ".mjs", ".cjs":
		return ".js"
	case ".tsx", ".mts", ".cts":
		return ".ts"
	default:
		return extension
	}
}
