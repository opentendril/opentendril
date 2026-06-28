package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func runPlasmidCmd(args []string) {
	if len(args) == 0 {
		printPlasmidUsage()
		return
	}

	switch strings.ToLower(args[0]) {
	case "list":
		runPlasmidListCmd()
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
	sourcePath, err := findPlasmidSource(root, name)
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

	if err := copyMarkdownFile(sourcePath, destPath); err != nil {
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

func findPlasmidSource(root, name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("missing plasmid name")
	}

	if filepath.IsAbs(trimmed) {
		if info, err := os.Stat(trimmed); err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(trimmed), ".md") {
			return trimmed, nil
		}
	}

	directCandidates := []string{
		filepath.Join(root, trimmed),
		filepath.Join(root, ".tendril", "genotypes", trimmed),
		filepath.Join(root, ".tendril", "genotypes", "plasmids", trimmed),
	}
	if filepath.Ext(trimmed) == "" {
		directCandidates = append(directCandidates,
			filepath.Join(root, trimmed+".md"),
			filepath.Join(root, ".tendril", "genotypes", trimmed+".md"),
			filepath.Join(root, ".tendril", "genotypes", "plasmids", trimmed+".md"),
		)
	}

	for _, candidate := range directCandidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(candidate), ".md") {
			return candidate, nil
		}
	}

	searchRoots := []string{
		filepath.Join(root, ".tendril", "genotypes", "plasmids"),
		filepath.Join(root, ".tendril", "genotypes"),
	}
	var matches []string
	targetBase := strings.TrimSuffix(filepath.Base(trimmed), filepath.Ext(trimmed))

	for _, searchRoot := range searchRoots {
		if info, err := os.Stat(searchRoot); err != nil || !info.IsDir() {
			continue
		}

		_ = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				return nil
			}

			base := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
			rel, relErr := filepath.Rel(searchRoot, path)
			if relErr != nil {
				rel = path
			}

			if strings.EqualFold(d.Name(), filepath.Base(trimmed)) ||
				strings.EqualFold(base, targetBase) ||
				strings.EqualFold(rel, trimmed) ||
				strings.EqualFold(strings.TrimSuffix(rel, filepath.Ext(rel)), strings.TrimSuffix(trimmed, filepath.Ext(trimmed))) {
				matches = append(matches, path)
			}

			return nil
		})
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("plasmid %q not found in .tendril/genotypes", trimmed)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("plasmid %q is ambiguous; matches: %s", trimmed, strings.Join(matches, ", "))
	}
}

func copyMarkdownFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat plasmid source: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("plasmid source is a directory: %s", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create plasmid destination directory: %w", err)
	}
	_ = os.Remove(dst)

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open plasmid source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create plasmid destination: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy plasmid: %w", err)
	}

	return nil
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
	fmt.Println("Usage: tendril plasmid <list|inject>")
	fmt.Println("  list           List available plasmid Markdown files")
	fmt.Println("  inject <name>  Copy a plasmid into .tendril/genome/")
}
