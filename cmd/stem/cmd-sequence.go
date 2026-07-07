package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
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
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	provider := fs.String("provider", "", "LLM provider override")
	model := fs.String("model", "", "LLM model override")
	baseURL := fs.String("base-url", "", "LLM base URL override")
	detach := fs.Bool("detach", false, "Run in background (requires daemon)")
	fs.BoolVar(detach, "d", false, "Run in background (shorthand)")
	_ = fs.Parse(args)

	positional := fs.Args()
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "❌ Missing sequence path or name")
		printSequenceUsage()
		os.Exit(1)
	}

	if *detach {
		submitSequenceAsync(ctx, positional[0], *provider, *model, *baseURL)
		return
	}

	interactive := stdinIsTerminal()
	seq, err := orchestrator.RunSequence(ctx, positional[0], orchestrator.SequenceRunOptions{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		Interactive: interactive,
		Provider:    *provider,
		Model:       *model,
		BaseURL:     *baseURL,
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
	fs := flag.NewFlagSet("dynamic", flag.ExitOnError)
	provider := fs.String("provider", "", "LLM provider override")
	model := fs.String("model", "", "LLM model override")
	baseURL := fs.String("base-url", "", "LLM base URL override")
	detach := fs.Bool("detach", false, "Run in background (requires daemon)")
	fs.BoolVar(detach, "d", false, "Run in background (shorthand)")
	_ = fs.Parse(args)

	positional := fs.Args()
	prompt := strings.TrimSpace(strings.Join(positional, " "))
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
				ID:         "meristem",
				Transcript: prompt,
			},
		},
	}
	if err := orchestrator.SaveSequence(tempPath, seq); err != nil {
		_ = os.Remove(tempPath)
		fmt.Fprintf(os.Stderr, "❌ Failed to save temporary sequence: %v\n", err)
		os.Exit(1)
	}

	if *detach {
		submitSequenceAsync(ctx, tempPath, *provider, *model, *baseURL)
		return
	}

	result, err := orchestrator.RunSequence(ctx, tempPath, orchestrator.SequenceRunOptions{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		Interactive: stdinIsTerminal(),
		Provider:    *provider,
		Model:       *model,
		BaseURL:     *baseURL,
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
	fmt.Println("    --provider        LLM provider override (e.g. local)")
	fmt.Println("    --model           LLM model override (e.g. llama3.2)")
	fmt.Println("    --base-url        LLM base URL override (e.g. http://host:11434/v1)")
	fmt.Println("    --detach, -d      Run in background (requires daemon)")
	fmt.Println("  list                List available sequence YAML files")
	fmt.Println("  dynamic <prompt>    Bootstrap a meristem sequence that expands from a natural-language prompt")
	fmt.Println("    --provider        LLM provider override")
	fmt.Println("    --model           LLM model override")
	fmt.Println("    --base-url        LLM base URL override")
	fmt.Println("    --detach, -d      Run in background (requires daemon)")
}

func submitSequenceAsync(ctx context.Context, pathOrName, provider, model, baseURL string) {
	// Send request to Stem daemon
	type runReq struct {
		PathOrName string `json:"pathOrName"`
		Provider   string `json:"provider,omitempty"`
		Model      string `json:"model,omitempty"`
		BaseURL    string `json:"baseURL,omitempty"`
	}
	
	payload, _ := json.Marshal(runReq{
		PathOrName: pathOrName,
		Provider:   provider,
		Model:      model,
		BaseURL:    baseURL,
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	resp, err := http.Post(fmt.Sprintf("http://localhost:%s/v1/sessions/new/sequences/run", port), "application/json", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Stem daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "Please ensure the OpenTendril daemon is running (`tendril serve`) to use --detach.")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "❌ Stem daemon rejected run request (status %d)\n", resp.StatusCode)
		os.Exit(1)
	}

	var result struct {
		RunID     string `json:"runId"`
		SessionID string `json:"sessionId"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	fmt.Fprintf(os.Stdout, "🚀 Sequence %s submitted for asynchronous execution.\n", pathOrName)
	fmt.Fprintf(os.Stdout, "   Session ID: %s\n", result.SessionID)
	fmt.Fprintf(os.Stdout, "   Run ID:     %s\n", result.RunID)
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
