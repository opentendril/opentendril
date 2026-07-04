package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
)

func runPlasmidCmd(args []string) {
	if len(args) == 0 {
		printPlasmidUsage()
		return
	}

	switch strings.ToLower(args[0]) {
	case "list":
		runPlasmidListCmd()
	case "sign":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "❌ Missing plasmid path. Usage: tendril plasmid sign <path>")
			os.Exit(1)
		}
		if err := runPlasmidSignCmd(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	case "inject":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "❌ Missing plasmid name. Usage: tendril plasmid inject <name>")
			os.Exit(1)
		}
		if err := runPlasmidInjectCmd(strings.Join(args[1:], " ")); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		printPlasmidUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown plasmid command: %s\n", args[0])
		printPlasmidUsage()
		os.Exit(1)
	}
}

func runPlasmidSignCmd(path string) error {
	key, err := orchestrator.NodeSigningKey()
	if err != nil {
		return fmt.Errorf("load node signing key: %w", err)
	}

	sig, err := orchestrator.SignPlasmid(path, key)
	if err != nil {
		return fmt.Errorf("sign plasmid: %w", err)
	}
	if err := orchestrator.WritePlasmidSignature(path, sig); err != nil {
		return fmt.Errorf("write plasmid signature: %w", err)
	}

	fmt.Println(path + ".sig")
	return nil
}

func runPlasmidListCmd() {
	root := resolveRepoRoot("")
	plasmidRoot := filepath.Join(root, ".tendril", "genotypes", "plasmids")
	genotypeRoot := filepath.Join(root, ".tendril", "genotypes")

	files := collectMarkdownFiles(plasmidRoot)
	if len(files) == 0 {
		files = collectMarkdownFiles(genotypeRoot)
	}

	if len(files) == 0 {
		fmt.Printf("No plasmids found under %s\n", genotypeRoot)
		return
	}

	sort.Strings(files)
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		fmt.Println(filepath.ToSlash(rel))
	}
}

func runPlasmidInjectCmd(name string) error {
	root := resolveRepoRoot("")
	sourcePath, err := orchestrator.FindPlasmidSource(root, name)
	if err != nil {
		return err
	}

	destDir := filepath.Join(root, ".tendril", "genome")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create genome directory: %w", err)
	}

	destPath := filepath.Join(destDir, filepath.Base(sourcePath))
	if samePath(sourcePath, destPath) {
		fmt.Printf("✅ Plasmid already active: %s\n", filepath.ToSlash(mustRel(root, destPath)))
		return nil
	}

	if err := orchestrator.CopyMarkdownFile(sourcePath, destPath); err != nil {
		return err
	}

	fmt.Printf("✅ Injected plasmid %s -> %s\n", filepath.ToSlash(mustRel(root, sourcePath)), filepath.ToSlash(mustRel(root, destPath)))
	return nil
}

func collectMarkdownFiles(root string) []string {
	var files []string
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return files
	}

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		files = append(files, path)
		return nil
	})

	return files
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	aAbs, err := filepath.Abs(a)
	if err != nil {
		aAbs = filepath.Clean(a)
	}
	bAbs, err := filepath.Abs(b)
	if err != nil {
		bAbs = filepath.Clean(b)
	}

	return filepath.Clean(aAbs) == filepath.Clean(bAbs)
}

func mustRel(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return target
	}
	return rel
}

func printPlasmidUsage() {
	fmt.Println("Usage: tendril plasmid <list|inject|sign>")
	fmt.Println("  list           List available plasmid Markdown files")
	fmt.Println("  inject <name>  Copy a plasmid into .tendril/genome/")
	fmt.Println("  sign <path>    Sign a plasmid Markdown file")
}
