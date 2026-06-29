package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
)

func runSequenceCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printSequenceUsage()
		return
	}

	switch strings.ToLower(args[0]) {
	case "run":
		runSequenceRunCmd(ctx, args[1:])
	case "list":
		runSequenceListCmd()
	case "dynamic":
		runSequenceDynamicCmd(ctx, args[1:])
	case "-h", "--help", "help":
		printSequenceUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown sequence command: %s\n", args[0])
		printSequenceUsage()
		os.Exit(1)
	}
}

func runSequenceRunCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "❌ Missing sequence path or name")
		printSequenceUsage()
		os.Exit(1)
	}

	interactive := stdinIsTerminal()
	seq, err := orchestrator.RunSequence(ctx, args[0], orchestrator.SequenceRunOptions{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		Interactive: interactive,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Sequence run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stdout, "✅ Sequence %s finished\n", seq.Name)
}

func runSequenceListCmd() {
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}

	files, err := orchestrator.ListSequenceFiles(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list sequences: %v\n", err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Println("No sequence YAML files found in .tendril/sequences/")
		return
	}

	for _, file := range files {
		fmt.Println(file)
	}
}

func runSequenceDynamicCmd(ctx context.Context, args []string) {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "❌ Missing dynamic sequence prompt")
		printSequenceUsage()
		os.Exit(1)
	}

	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	root = resolveRepoRoot(root)

	sequencesDir := filepath.Join(root, ".tendril", "sequences")
	if err := os.MkdirAll(sequencesDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create sequence directory: %v\n", err)
		os.Exit(1)
	}

	tempFile, err := os.CreateTemp(sequencesDir, "dynamic-*.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create temporary sequence: %v\n", err)
		os.Exit(1)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		fmt.Fprintf(os.Stderr, "❌ Failed to close temporary sequence: %v\n", err)
		os.Exit(1)
	}

	seq := &orchestrator.Sequence{
		Steps: []orchestrator.SequenceStep{
			{
				ID:         "conductor",
				Transcript: prompt,
			},
		},
	}
	if err := orchestrator.SaveSequence(tempPath, seq); err != nil {
		_ = os.Remove(tempPath)
		fmt.Fprintf(os.Stderr, "❌ Failed to save temporary sequence: %v\n", err)
		os.Exit(1)
	}

	result, err := orchestrator.RunSequence(ctx, tempPath, orchestrator.SequenceRunOptions{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		Interactive: stdinIsTerminal(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Dynamic sequence run failed: %v\n", err)
		os.Exit(1)
	}

	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to remove temporary sequence %s: %v\n", tempPath, err)
	}

	fmt.Fprintf(os.Stdout, "✅ Sequence %s finished\n", result.Name)
}

func printSequenceUsage() {
	fmt.Println("Usage: tendril sequence <run|list|dynamic> [arguments]")
	fmt.Println("  run <path_or_name>  Run a sequence YAML file from .tendril/sequences/ or a relative path")
	fmt.Println("  list                List available sequence YAML files")
	fmt.Println("  dynamic <prompt>    Bootstrap a conductor sequence that expands from a natural-language prompt")
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
