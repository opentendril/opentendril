package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/conductor"
	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// runSequenceCmd is the CLI adapter for the governed sequence capability
// family. The run/list subcommands are thin projections
// of the same transport-free core.Core the REST and MCP surfaces use.
//
// `dynamic` is CLI-local sugar: it synthesizes a one-step sequence file from
// a natural-language prompt and then invokes the same governed sequence.run
// capability on it. `--detach` is likewise adapter-local: it hands the run to
// the Stem daemon's async endpoint instead of executing in-process.
func runSequenceCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printSequenceUsage()
		return
	}

	switch strings.ToLower(args[0]) {
	case "run":
		runSequenceRunCmd(ctx, args[1:])
	case "list":
		runSequenceListCmd(ctx)
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
	input, detach, err := parseSequenceArgs(core.CapSequenceRun, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	pathOrName, _ := input["pathOrName"].(string)
	if strings.TrimSpace(pathOrName) == "" {
		fmt.Fprintln(os.Stderr, "❌ Missing sequence path or name")
		printSequenceUsage()
		os.Exit(1)
	}

	if detach {
		provider, _ := input["provider"].(string)
		model, _ := input["model"].(string)
		baseURL, _ := input["baseURL"].(string)
		submitSequenceAsync(ctx, pathOrName, provider, model, baseURL)
		return
	}

	svc, err := buildSequenceCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, core.CapSequenceRun, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Sequence run failed: %v\n", err)
		os.Exit(1)
	}
	renderSequenceRunResult(result)
}

func runSequenceListCmd(ctx context.Context) {
	svc, err := buildSequenceCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, core.CapSequenceList, map[string]any{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list sequences: %v\n", err)
		os.Exit(1)
	}

	files, _ := result.([]string)
	if len(files) == 0 {
		fmt.Println("No sequence YAML files found in .tendril/sequences/")
		return
	}
	for _, file := range files {
		fmt.Println(file)
	}
}

func runSequenceDynamicCmd(ctx context.Context, args []string) {
	input, detach, err := parseSequenceArgs(core.CapSequenceRun, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	// For dynamic, the positional args are the prompt, not a path.
	prompt, _ := input["pathOrName"].(string)
	prompt = strings.TrimSpace(prompt)
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

	seq := &conductor.Sequence{
		Steps: []conductor.SequenceStep{
			{
				ID:         "meristem",
				Transcript: prompt,
			},
		},
	}
	if err := conductor.SaveSequence(tempPath, seq); err != nil {
		_ = os.Remove(tempPath)
		fmt.Fprintf(os.Stderr, "❌ Failed to save temporary sequence: %v\n", err)
		os.Exit(1)
	}

	input["pathOrName"] = tempPath
	if detach {
		provider, _ := input["provider"].(string)
		model, _ := input["model"].(string)
		baseURL, _ := input["baseURL"].(string)
		submitSequenceAsync(ctx, tempPath, provider, model, baseURL)
		return
	}

	svc, err := buildSequenceCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, core.CapSequenceRun, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Dynamic sequence run failed: %v\n", err)
		os.Exit(1)
	}

	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to remove temporary sequence %s: %v\n", tempPath, err)
	}

	renderSequenceRunResult(result)
}

// renderSequenceRunResult preserves the historic success line.
func renderSequenceRunResult(result any) {
	runResult, _ := result.(core.SequenceRunResult)
	fmt.Fprintf(os.Stdout, "✅ Sequence %s finished\n", runResult.Name)
}

// buildSequenceCore constructs a Core with the sequence execution port wired
// for terminal use: the run streams to this process's stdio and may be
// interactive. The session manager is an ephemeral in-memory one.
func buildSequenceCore(ctx context.Context) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	return core.NewService(manager).WithSequence(cliSequenceOps(root)), nil
}

// cliSequenceOps binds the sequence execution port to the conductor with
// terminal I/O — this wiring lives in the adapter layer precisely so the Core
// never imports the conductor (see internal/core/boundary_test.go). The
// serve/MCP surfaces bind the same port with server I/O instead (see
// serveSequenceOps).
func cliSequenceOps(root string) core.SequenceOps {
	return core.SequenceOps{
		Root: root,
		List: func(_ context.Context, root string) ([]string, error) {
			return conductor.ListSequenceFiles(root)
		},
		Run: func(ctx context.Context, in core.SequenceRunInput) (core.SequenceRunResult, error) {
			seq, err := conductor.RunSequence(ctx, in.PathOrName, conductor.SequenceRunOptions{
				Stdout:      os.Stdout,
				Stderr:      os.Stderr,
				Stdin:       os.Stdin,
				Interactive: stdinIsTerminal(),
				Provider:    in.Provider,
				Model:       in.Model,
				BaseURL:     in.BaseURL,
			})
			return sequenceRunResultFromConductor(seq), err
		},
	}
}

// serveSequenceOps binds the sequence execution port for the daemon and MCP
// surfaces: output is discarded (never written into a protocol stream) and
// runs are non-interactive — exactly the semantics the MCP runSequence tool
// has always had. The daemon threads its EventBus through so every sequence
// run — manual or scheduled — emits its lifecycle telemetry to the Command
// Center; surfaces without a bus (the MCP stdio server) pass nil.
func serveSequenceOps(root string, bus *eventbus.Bus) core.SequenceOps {
	return core.SequenceOps{
		Root: root,
		List: func(_ context.Context, root string) ([]string, error) {
			return conductor.ListSequenceFiles(root)
		},
		Run: func(ctx context.Context, in core.SequenceRunInput) (core.SequenceRunResult, error) {
			seq, err := conductor.RunSequence(ctx, in.PathOrName, conductor.SequenceRunOptions{
				Stdout:      io.Discard,
				Stderr:      os.Stderr,
				Interactive: false,
				Provider:    in.Provider,
				Model:       in.Model,
				BaseURL:     in.BaseURL,
				EventBus:    bus,
			})
			return sequenceRunResultFromConductor(seq), err
		},
	}
}

// sequenceRunResultFromConductor maps the conductor's sequence state onto the
// Core's transport-free result type, preserving partial step states on
// failure.
func sequenceRunResultFromConductor(seq *conductor.Sequence) core.SequenceRunResult {
	if seq == nil {
		return core.SequenceRunResult{}
	}
	result := core.SequenceRunResult{
		Name:      seq.Name,
		Substrate: seq.Substrate,
		Branch:    seq.Branch,
	}
	for _, step := range seq.Steps {
		result.Steps = append(result.Steps, core.SequenceStepOutcome{
			ID:         step.ID,
			Status:     step.Status,
			Transcript: step.Transcript,
		})
	}
	return result
}

// sequenceCommand is one governed subcommand actually registered on the
// `tendril sequence` command tree.
type sequenceCommand struct {
	name       string // CLI token, e.g. "run"
	capability string // the governed Core capability it invokes
}

// sequenceCommands is the CLI command tree for the governed half of
// `tendril sequence` (dynamic is adapter-local sugar over sequence.run). Like
// sessionCommands, this registration — NOT core.CapabilityNames() — is the
// source of truth the parity coverage test reads for the CLI arm.
var sequenceCommands = []sequenceCommand{
	{"run", core.CapSequenceRun},
	{"list", core.CapSequenceList},
}

// lookupSequenceCommand resolves a CLI subcommand token to its registered
// entry.
func lookupSequenceCommand(sub string) (sequenceCommand, bool) {
	for _, command := range sequenceCommands {
		if command.name == sub {
			return command, true
		}
	}
	return sequenceCommand{}, false
}

// sequenceCLICapabilityNames returns the capability names the CLI has
// actually registered sequence subcommands for, sorted. Read by the parity
// coverage test.
func sequenceCLICapabilityNames() []string {
	names := make([]string, 0, len(sequenceCommands))
	for _, command := range sequenceCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parseSequenceArgs turns CLI args into a capability input map plus the
// adapter-local detach flag. Positional arguments become pathOrName (joined,
// so `sequence dynamic` can reuse this for multi-word prompts); `--json
// '{...}'` is the generic escape hatch, mirroring parseSessionArgs.
func parseSequenceArgs(capName string, args []string) (map[string]any, bool, error) {
	input := map[string]any{}
	detach := false
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("flag --json requires a value")
			}
			i++
			if err := json.Unmarshal([]byte(args[i]), &input); err != nil {
				return nil, false, fmt.Errorf("invalid --json input: %w", err)
			}
		case "--provider":
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("flag --provider requires a value")
			}
			i++
			input["provider"] = args[i]
		case "--model":
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("flag --model requires a value")
			}
			i++
			input["model"] = args[i]
		case "--base-url":
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("flag --base-url requires a value")
			}
			i++
			input["baseURL"] = args[i]
		case "--detach", "-d":
			detach = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, false, fmt.Errorf("unknown argument %q for sequence %s", args[i], strings.TrimPrefix(capName, "sequence."))
			}
			positional = append(positional, args[i])
		}
	}
	if len(positional) > 0 {
		input["pathOrName"] = strings.Join(positional, " ")
	}
	return input, detach, nil
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
	fmt.Println()
	fmt.Println("run and list are projections of the shared Core capability registry.")
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
