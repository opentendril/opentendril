package main

import (
	"context"
	"fmt"
	"os"
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

func printSequenceUsage() {
	fmt.Println("Usage: tendril sequence <run|list> [arguments]")
	fmt.Println("  run <path_or_name>  Run a sequence YAML file from .tendril/sequences/ or a relative path")
	fmt.Println("  list                List available sequence YAML files")
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
