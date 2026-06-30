package dreamer

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
	file, err := parser.ParseFile(fset, path, content, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse Go file: %w", err)
	}

	symbols := make([]Symbol, 0)
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
				symbols = append(symbols, Symbol{
					Name:        typeSpec.Name.Name,
					Type:        symbolType,
					LineStart:   fset.Position(typeSpec.Pos()).Line,
					LineEnd:     fset.Position(typeSpec.End()).Line,
					StubContent: renderGoDeclarationStub(fset, node),
				})
			}
		case *ast.FuncDecl:
			symbolType := "function"
			if node.Recv != nil {
				symbolType = "method"
			}
			symbols = append(symbols, Symbol{
				Name:        node.Name.Name,
				Type:        symbolType,
				LineStart:   fset.Position(node.Pos()).Line,
				LineEnd:     fset.Position(node.End()).Line,
				StubContent: renderGoFunctionStub(node),
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
			symbols = append(symbols, Symbol{
				Name:        matches[1],
				Type:        pattern.symbolType,
				LineStart:   lineNumber,
				LineEnd:     lineNumber,
				StubContent: strings.TrimSpace(line),
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
