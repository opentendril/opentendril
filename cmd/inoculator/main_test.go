package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestParseGitDiff(t *testing.T) {
	diff := `+++ b/cmd/stem/internal/conductor/sequence.go
@@ -1,2 +3,4 @@
+func foo() {
+}
+++ b/other/file.go
@@ -10,0 +11,1 @@
+var x int
`
	res := parseGitDiff(diff)
	if len(res) != 2 {
		t.Fatalf("expected 2 files, got %d", len(res))
	}

	seqLines := res["cmd/stem/internal/conductor/sequence.go"]
	if len(seqLines) != 1 || seqLines[0].Start != 3 || seqLines[0].End != 6 {
		t.Errorf("unexpected lines for sequence.go: %+v", seqLines)
	}

	otherLines := res["other/file.go"]
	if len(otherLines) != 1 || otherLines[0].Start != 11 || otherLines[0].End != 11 {
		t.Errorf("unexpected lines for file.go: %+v", otherLines)
	}
}

func TestMutateBinaryExpr(t *testing.T) {
	src := `package main
func foo() bool {
	return 1 < 2
}
`
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	var found *ast.BinaryExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if x, ok := n.(*ast.BinaryExpr); ok {
			found = x
			return false
		}
		return true
	})

	if found == nil {
		t.Fatal("no binary expr found")
	}

	m := mutateBinaryExprWithFset(found, fset.Position(found.Pos()), []byte(src), fset)
	if m == nil {
		t.Fatal("mutation was nil")
	}

	if m.Type != "Comparison Inversion" {
		t.Errorf("expected Comparison Inversion, got %s", m.Type)
	}

	if string(m.Replacement) != ">=" {
		t.Errorf("expected >=, got %s", string(m.Replacement))
	}
}

func TestMutateReturnStmt(t *testing.T) {
	src := `package main
func foo() error {
	return nil
}
`
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	var found *ast.ReturnStmt
	ast.Inspect(node, func(n ast.Node) bool {
		if x, ok := n.(*ast.ReturnStmt); ok {
			found = x
			return false
		}
		return true
	})

	if found == nil {
		t.Fatal("no return stmt found")
	}

	m := mutateReturnStmt(found, fset.Position(found.Pos()), fset.Position(found.End()), []byte(src), fset)
	if m == nil {
		t.Fatal("mutation was nil")
	}

	if string(m.Replacement) != `errors.New("mutated")` {
		t.Errorf("expected errors.New(\"mutated\"), got %s", string(m.Replacement))
	}
}
