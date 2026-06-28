package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
)

func runGenomeCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printGenomeUsage()
		return
	}

	switch strings.ToLower(args[0]) {
	case "view":
		runGenomeViewCmd()
	case "reduce":
		runGenomeReduceCmd(ctx)
	case "-h", "--help", "help":
		printGenomeUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown genome command: %s\n", args[0])
		printGenomeUsage()
		os.Exit(1)
	}
}

func runGenomeViewCmd() {
	root := resolveRepoRoot("")
	genomeDir := filepath.Join(root, ".tendril", "genome")

	entries, err := os.ReadDir(genomeDir)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "❌ Failed to read genome directory: %v\n", err)
		os.Exit(1)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		files = append(files, filepath.Join(genomeDir, entry.Name()))
	}
	sort.Strings(files)

	if len(files) == 0 {
		fmt.Printf("No genome seeds found in %s\n", genomeDir)
		return
	}

	for i, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to read %s: %v\n", path, err)
			os.Exit(1)
		}

		if i > 0 {
			fmt.Println()
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			relPath = path
		}

		fmt.Printf("========== %s ==========\n", filepath.ToSlash(relPath))
		fmt.Println(strings.TrimSpace(string(content)))
	}
}

func runGenomeReduceCmd(ctx context.Context) {
	root := resolveRepoRoot("")
	chronicler := orchestrator.NewEpigeneticChronicler(root)
	if err := chronicler.ReduceGenomeFile(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to reduce genome: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Reduced genome at %s\n", filepath.Join(root, ".tendril", "genome", "epigenetics.md"))
}

func printGenomeUsage() {
	fmt.Println("Usage: tendril genome <view|reduce>")
	fmt.Println("  view    Print the active genome seeds in alphabetical order")
	fmt.Println("  reduce  Consolidate .tendril/genome/epigenetics.md in place")
}
