package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/conductor"
	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// runPassthroughCmd is the CLI adapter for the governed passthrough/run
// capability family: a thin projection of the same transport-free core.Core
// the REST and MCP surfaces use. `tendril passthrough run` executes one
// bounded command inside a network-sealed terrarium and prints its output.
//
// A CLI invocation is never delegated (there is no delegation subject), so
// its egress allow-list is always empty: deny-all, the secure default. The
// container is network-sealed either way; only a delegated invocation under a
// grant with egress hosts gains Stem-mediated fetches.
func runPassthroughCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printPassthroughUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printPassthroughUsage()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	command, ok := lookupPassthroughCommand(sub)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown passthrough command: %s\n", args[0])
		printPassthroughUsage()
		os.Exit(1)
	}

	input, err := parsePassthroughArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if origin, _ := input["origin"].(string); strings.TrimSpace(origin) == "" {
		input["origin"] = session.OriginCLI
	}

	svc, err := buildPassthroughCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Passthrough run failed: %v\n", err)
		os.Exit(1)
	}

	runResult, _ := result.(core.PassthroughRunResult)
	if strings.TrimSpace(runResult.Stdout) != "" {
		fmt.Fprintln(os.Stdout, runResult.Stdout)
	}
	if strings.TrimSpace(runResult.Stderr) != "" {
		fmt.Fprintln(os.Stderr, runResult.Stderr)
	}
	fmt.Fprintf(os.Stderr, "🌱 Passthrough %s (exit %d)\n", runResult.Status, runResult.ExitCode)
	if runResult.ExitCode != 0 {
		os.Exit(1)
	}
}

// buildPassthroughCore constructs a Core with the passthrough execution port
// wired, matching the daemon surfaces.
func buildPassthroughCore(ctx context.Context) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	return core.NewService(manager).WithPassthrough(passthroughOps()), nil
}

// passthroughOps binds the passthrough execution port to the conductor's
// sealed-terrarium runner — this wiring lives in the adapter layer precisely
// so the Core never imports the conductor (see internal/core/boundary_test.go).
// It owns named-substrate resolution and the translation between the Core's
// transport-free spec and the conductor's execution request.
func passthroughOps() core.PassthroughOps {
	substratesConfig, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to load substrates config: %v\n", err)
	}

	return core.PassthroughOps{
		Run: func(ctx context.Context, spec core.PassthroughSpec) (core.PassthroughRunResult, error) {
			workspace := spec.Substrate
			if substrateSpec, isName := conductor.ResolveSubstrate(spec.Substrate, substratesConfig); isName && substrateSpec != nil {
				if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
					workspace = trimmedPath
				}
			}
			info, statErr := os.Stat(workspace)
			if statErr != nil || !info.IsDir() {
				return core.PassthroughRunResult{}, fmt.Errorf("substrate %q does not resolve to a local workspace directory (a passthrough runs against a local checkout)", spec.Substrate)
			}

			fetches := make([]conductor.PassthroughFetch, 0, len(spec.Fetch))
			for _, fetch := range spec.Fetch {
				fetches = append(fetches, conductor.PassthroughFetch{URL: fetch.URL, Path: fetch.Path})
			}

			result, err := conductor.RunPassthrough(ctx, conductor.PassthroughExecution{
				Workspace: workspace,
				Command:   spec.Command,
				Fetches:   fetches,
				Egress:    spec.Egress,
				Timeout:   spec.Timeout,
			})
			if err != nil {
				return core.PassthroughRunResult{}, err
			}

			status := "completed"
			if result.TimedOut {
				status = "timed-out"
			}
			return core.PassthroughRunResult{
				Status:     status,
				ExitCode:   result.ExitCode,
				Stdout:     result.Stdout,
				Stderr:     result.Stderr,
				TimedOut:   result.TimedOut,
				DurationMS: result.Duration.Milliseconds(),
			}, nil
		},
	}
}

// passthroughCommand is one subcommand actually registered on the
// `tendril passthrough` command tree.
type passthroughCommand struct {
	name       string // CLI token, e.g. "run"
	capability string // the governed Core capability it invokes
}

// passthroughCommands is the CLI command tree for `tendril passthrough`. Like
// sproutCommands, this registration — NOT core.CapabilityNames() — is the
// source of truth the parity coverage test reads for the CLI arm.
var passthroughCommands = []passthroughCommand{
	{"run", core.CapPassthroughRun},
}

// lookupPassthroughCommand resolves a CLI subcommand token to its registered
// entry.
func lookupPassthroughCommand(sub string) (passthroughCommand, bool) {
	for _, command := range passthroughCommands {
		if command.name == sub {
			return command, true
		}
	}
	return passthroughCommand{}, false
}

// passthroughCLICapabilityNames returns the capability names the CLI has
// actually registered passthrough subcommands for, sorted. Read by the parity
// coverage test.
func passthroughCLICapabilityNames() []string {
	names := make([]string, 0, len(passthroughCommands))
	for _, command := range passthroughCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parsePassthroughArgs turns CLI args into a capability input map. Flags come
// first; every argument after a bare `--` (and every non-flag positional) is
// a command token, so `tendril passthrough run --substrate . -- go test ./...`
// carries the command's own flags untouched. `--json '{...}'` is the generic
// escape hatch, mirroring parseSproutArgs.
func parsePassthroughArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	var command []string
	commandOnly := false
	stringFlag := func(i *int, key string) error {
		if *i+1 >= len(args) {
			return fmt.Errorf("flag %s requires a value", args[*i])
		}
		*i++
		input[key] = args[*i]
		return nil
	}
	for i := 0; i < len(args); i++ {
		if commandOnly {
			command = append(command, args[i])
			continue
		}
		var err error
		switch args[i] {
		case "--":
			commandOnly = true
		case "--json":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --json requires a value")
			}
			i++
			if jsonErr := json.Unmarshal([]byte(args[i]), &input); jsonErr != nil {
				return nil, fmt.Errorf("invalid --json input: %w", jsonErr)
			}
		case "--substrate":
			err = stringFlag(&i, "substrate")
		case "--timeout":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --timeout requires a value (seconds)")
			}
			i++
			var seconds int
			if _, scanErr := fmt.Sscanf(args[i], "%d", &seconds); scanErr != nil {
				return nil, fmt.Errorf("invalid --timeout value %q (want seconds)", args[i])
			}
			input["timeoutSeconds"] = seconds
		case "--origin":
			err = stringFlag(&i, "origin")
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, fmt.Errorf("unknown argument %q for passthrough %s (use `--` before the command's own flags)", args[i], strings.TrimPrefix(capName, "passthrough."))
			}
			command = append(command, args[i])
		}
		if err != nil {
			return nil, err
		}
	}

	if len(command) > 0 {
		tokens := make([]any, 0, len(command))
		for _, token := range command {
			tokens = append(tokens, token)
		}
		input["command"] = tokens
	}
	if _, ok := input["command"]; !ok {
		return nil, fmt.Errorf("missing command. Usage: tendril passthrough run --substrate <path|name> -- <command...>")
	}
	if substrate, _ := input["substrate"].(string); strings.TrimSpace(substrate) == "" {
		return nil, fmt.Errorf("missing substrate. Usage: tendril passthrough run --substrate <path|name> -- <command...>")
	}
	return input, nil
}

func printPassthroughUsage() {
	fmt.Println("Usage: tendril passthrough run --substrate <path|name> [flags] -- <command...>")
	fmt.Println("  --substrate         The absolute path or named substrate key of the target workspace (required)")
	fmt.Println("  --timeout N         Execution bound in seconds (default 300, maximum 1800)")
	fmt.Println("  --json '{...}'      Full JSON input (the generic escape hatch; supports fetch entries)")
	fmt.Println()
	fmt.Println("Runs one bounded command inside a network-sealed terrarium. The command has")
	fmt.Println("no network reach; Stem-mediated fetches require a delegation grant with an")
	fmt.Println("egress allow-list (deny-all by default).")
	fmt.Println()
	fmt.Println("run is a projection of the shared Core capability registry.")
}
