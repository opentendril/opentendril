package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Challenge struct {
	File     string
	Line     int
	Func     string
	Type     string
	Desc     string
	Status   string // "Detected", "Undetected", "Inert", "Uncovered", "Skipped"
	Duration time.Duration
}

type Report struct {
	Total       int
	Invocations int
	Detected    int
	Undetected  int
	Inert       int
	Uncovered   int
	Skipped     int
	Challenges  []Challenge
	WallClock   time.Duration
}

type LineRange struct {
	Start int
	End   int
}

type Mutation struct {
	Line        int
	Func        string
	Type        string
	Desc        string
	StartOffset int
	EndOffset   int
	Replacement []byte
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: inoculator <base_ref>")
		os.Exit(1)
	}
	baseRef := os.Args[1]

	start := time.Now()

	diffCmd := exec.Command("git", "diff", "--unified=0", baseRef, "HEAD", "--", "*.go")
	diffOut, err := diffCmd.Output()
	if err != nil {
		fmt.Printf("Error running git diff: %v\n", err)
		os.Exit(1)
	}

	changedLines := parseGitDiff(string(diffOut))
	if len(changedLines) == 0 {
		fmt.Println("No changed Go lines found.")
		os.Exit(0)
	}

	report := Report{}
	fset := token.NewFileSet()

	// 1. Collect all packages that have changes
	pkgs := make(map[string]bool)
	for file := range changedLines {
		if !strings.HasSuffix(file, "_test.go") {
			pkgDir := filepath.Dir(file)
			pkgs[pkgDir] = true
		}
	}

	// 2. Take baseline coverage profile per package
	coverageMap := make(map[string]map[int]bool) // file -> line -> covered
	for pkgDir := range pkgs {
		fmt.Printf("Taking baseline coverage for %s...\n", pkgDir)
		covFile := filepath.Join(pkgDir, "coverage.out")
		pDir := pkgDir
		if pDir == "." {
			pDir = "./"
		} else {
			pDir = "./" + pDir
		}

		cmd := exec.Command("go", "test", "-coverprofile=coverage.out", pDir)
		cmd.Dir = pkgDir // run inside the pkg dir to drop coverage.out there, or just pass full path
		// Actually, let's just run it from root
		cmd = exec.Command("go", "test", "-coverprofile="+covFile, pDir)
		cmd.CombinedOutput() // We don't care if it fails, some tests might fail on baseline? Should be green.

		covData, err := os.ReadFile(covFile)
		if err == nil {
			parseCoverage(string(covData), coverageMap)
			os.Remove(covFile)
		} else {
			fmt.Printf("Warning: could not read coverage profile for %s: %v\n", pDir, err)
		}
	}

	// 3. Find mutations and group by function
	funcGroups := make(map[string][]Mutation)
	fileContents := make(map[string][]byte)

	for file, lines := range changedLines {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		fileContents[file] = content

		node, err := parser.ParseFile(fset, file, content, parser.ParseComments)
		if err != nil {
			continue
		}

		ast.Inspect(node, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok {
				// Find mutations inside this function
				ast.Inspect(fd.Body, func(bn ast.Node) bool {
					if bn == nil {
						return true
					}
					pos := fset.Position(bn.Pos())
					end := fset.Position(bn.End())

					overlap := false
					for _, lr := range lines {
						if (pos.Line >= lr.Start && pos.Line <= lr.End) || (end.Line >= lr.Start && end.Line <= lr.End) || (pos.Line <= lr.Start && end.Line >= lr.End) {
							overlap = true
							break
						}
					}
					if !overlap {
						return true
					}

					var mut *Mutation
					switch x := bn.(type) {
					case *ast.BinaryExpr:
						mut = mutateBinaryExprWithFset(x, pos, content, fset)
					case *ast.ReturnStmt:
						mut = mutateReturnStmt(x, pos, end, content, fset)
					case *ast.ExprStmt:
						mut = &Mutation{
							Line:        pos.Line,
							Type:        "Statement Removal",
							Desc:        "Removed expression statement",
							StartOffset: fset.Position(x.Pos()).Offset,
							EndOffset:   fset.Position(x.End()).Offset,
							Replacement: []byte(`/* removed */`),
						}
					}

					if mut != nil {
						mut.Func = fd.Name.Name
						if fd.Recv != nil && len(fd.Recv.List) > 0 {
							if star, ok := fd.Recv.List[0].Type.(*ast.StarExpr); ok {
								if id, ok := star.X.(*ast.Ident); ok {
									mut.Func = id.Name + "." + mut.Func
								}
							} else if id, ok := fd.Recv.List[0].Type.(*ast.Ident); ok {
								mut.Func = id.Name + "." + mut.Func
							}
						}
						// prefix with file to ensure uniqueness across files
						funcKey := file + ":" + mut.Func
						funcGroups[funcKey] = append(funcGroups[funcKey], *mut)
						report.Total++
					}
					return true
				})
				return false // don't descend further, we handled the body
			}
			return true
		})
	}

	// 4. Execute mutations per function group
	for funcKey, mutations := range funcGroups {
		file := strings.Split(funcKey, ":")[0]
		content := fileContents[file]
		pkgDir := filepath.Dir(file)
		if pkgDir == "." {
			pkgDir = "./"
		} else {
			pkgDir = "./" + pkgDir
		}

		detectedInGroup := false

		for _, m := range mutations {
			c := Challenge{
				File: file,
				Line: m.Line,
				Func: m.Func,
				Type: m.Type,
				Desc: m.Desc,
			}

			if detectedInGroup {
				c.Status = "Skipped"
				report.Skipped++
				report.Challenges = append(report.Challenges, c)
				continue
			}

			// Check coverage
			// Check coverage
			// So we can just check suffix
			covered := false
			for covFile, lines := range coverageMap {
				if strings.HasSuffix(covFile, file) {
					if lines[m.Line] {
						covered = true
					}
					break
				}
			}

			if !covered {
				c.Status = "Uncovered"
				report.Uncovered++
				fmt.Printf("Applying challenge in %s:%d (%s) -> Uncovered\n", file, m.Line, m.Func)
				report.Challenges = append(report.Challenges, c)
				continue
			}

			fmt.Printf("Applying challenge in %s:%d (%s: %s)...\n", file, m.Line, m.Type, m.Desc)

			mutatedContent := append(append([]byte{}, content[:m.StartOffset]...), m.Replacement...)
			mutatedContent = append(mutatedContent, content[m.EndOffset:]...)

			err = os.WriteFile(file, mutatedContent, 0644)
			if err != nil {
				continue
			}

			mStart := time.Now()
			report.Invocations++
			testCmd := exec.Command("go", "test", "-failfast", "-timeout=30s", pkgDir)
			testOut, err := testCmd.CombinedOutput()
			c.Duration = time.Since(mStart)

			if err != nil {
				if strings.Contains(string(testOut), "build failed") || strings.Contains(string(testOut), "syntax error") || strings.Contains(string(testOut), "undefined") || strings.Contains(string(testOut), "declared and not used") || strings.Contains(string(testOut), "non-bool") {
					c.Status = "Inert"
					report.Inert++
					fmt.Printf("  -> Inert (compile error)\n")
				} else {
					c.Status = "Detected"
					report.Detected++
					fmt.Printf("  -> Detected! (test failed)\n")
					detectedInGroup = true // Short circuit!
				}
			} else {
				c.Status = "Undetected"
				report.Undetected++
				fmt.Printf("  -> Undetected!\n")
			}
			report.Challenges = append(report.Challenges, c)

			os.WriteFile(file, content, 0644)
		}
	}

	report.WallClock = time.Since(start)

	out, _ := json.MarshalIndent(report, "", "  ")
	// Save report to file if second arg is provided, else stdout
	if len(os.Args) > 2 {
		os.WriteFile(os.Args[2], out, 0644)
	} else {
		fmt.Println(string(out))
	}
}

func parseCoverage(covData string, coverageMap map[string]map[int]bool) {
	lines := strings.Split(covData, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		// format: github.com/repo/file.go:10.1,12.2 1 1
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}
		file := parts[0]
		rest := strings.Split(parts[1], " ")
		if len(rest) < 3 {
			continue
		}
		blocks := strings.Split(rest[0], ",")
		if len(blocks) < 2 {
			continue
		}
		startParts := strings.Split(blocks[0], ".")
		endParts := strings.Split(blocks[1], ".")
		startLine, _ := strconv.Atoi(startParts[0])
		endLine, _ := strconv.Atoi(endParts[0])
		count, _ := strconv.Atoi(rest[2])

		if count > 0 {
			if coverageMap[file] == nil {
				coverageMap[file] = make(map[int]bool)
			}
			for i := startLine; i <= endLine; i++ {
				coverageMap[file][i] = true
			}
		}
	}
}

func mutateBinaryExprWithFset(x *ast.BinaryExpr, pos token.Position, content []byte, fset *token.FileSet) *Mutation {
	var replacement string
	var desc string
	var mutType string

	switch x.Op {
	case token.LSS:
		replacement = ">="
		desc = "< to >="
		mutType = "Comparison Inversion"
	case token.GTR:
		replacement = "<="
		desc = "> to <="
		mutType = "Comparison Inversion"
	case token.LEQ:
		replacement = ">"
		desc = "<= to >"
		mutType = "Comparison Inversion"
	case token.GEQ:
		replacement = "<"
		desc = ">= to <"
		mutType = "Comparison Inversion"
	case token.EQL:
		replacement = "!="
		desc = "== to !="
		mutType = "Comparison Inversion"
	case token.NEQ:
		replacement = "=="
		desc = "!= to =="
		mutType = "Comparison Inversion"
	case token.LAND:
		replacement = "||"
		desc = "&& to ||"
		mutType = "Boolean Swap"
	case token.LOR:
		replacement = "&&"
		desc = "|| to &&"
		mutType = "Boolean Swap"
	default:
		return nil
	}

	opPos := fset.Position(x.OpPos).Offset
	opEnd := opPos + len(x.Op.String())

	return &Mutation{
		Line:        pos.Line,
		Type:        mutType,
		Desc:        desc,
		StartOffset: opPos,
		EndOffset:   opEnd,
		Replacement: []byte(replacement),
	}
}

func mutateReturnStmt(x *ast.ReturnStmt, pos, end token.Position, content []byte, fset *token.FileSet) *Mutation {
	for _, res := range x.Results {
		if id, ok := res.(*ast.Ident); ok {
			if id.Name == "nil" {
				return &Mutation{
					Line:        pos.Line,
					Type:        "Return Swap",
					Desc:        "nil to errors.New(\"mutated\")",
					StartOffset: fset.Position(id.Pos()).Offset,
					EndOffset:   fset.Position(id.End()).Offset,
					Replacement: []byte(`errors.New("mutated")`),
				}
			}
		}
	}
	return nil
}

func parseGitDiff(diff string) map[string][]LineRange {
	res := make(map[string][]LineRange)
	var currentFile string
	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ b/") {
			currentFile = strings.TrimPrefix(line, "+++ b/")
		} else if strings.HasPrefix(line, "@@ ") && currentFile != "" {
			parts := strings.Split(line, " ")
			if len(parts) >= 3 {
				plusPart := parts[2]
				plusPart = strings.TrimPrefix(plusPart, "+")
				sp := strings.Split(plusPart, ",")
				start, _ := strconv.Atoi(sp[0])
				length := 1
				if len(sp) > 1 {
					length, _ = strconv.Atoi(sp[1])
				}
				if length == 0 {
					continue
				}
				res[currentFile] = append(res[currentFile], LineRange{Start: start, End: start + length - 1})
			}
		}
	}
	return res
}
