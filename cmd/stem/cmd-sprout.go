package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/conductor"
	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// runSproutCmd is the CLI adapter for the governed sprout/run capability
// family (issue #181): a thin projection of the same transport-free core.Core
// the REST and MCP surfaces use. `tendril sprout run` delegates a one-shot
// task to an autonomous Tendril in a secure terrarium and prints its output —
// the headless CLI equivalent of the MCP sproutTendril tool.
func runSproutCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printSproutUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printSproutUsage()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	command, ok := lookupSproutCommand(sub)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown sprout command: %s\n", args[0])
		printSproutUsage()
		os.Exit(1)
	}

	input, err := parseSproutArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if origin, _ := input["origin"].(string); strings.TrimSpace(origin) == "" {
		input["origin"] = session.OriginCLI
	}

	svc, cleanup, err := buildSproutCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Sprout run failed: %v\n", err)
		cleanup()
		os.Exit(1)
	}

	runResult, _ := result.(core.SproutRunResult)
	if strings.TrimSpace(runResult.Output) != "" {
		fmt.Fprintln(os.Stdout, runResult.Output)
	}
	fmt.Fprintf(os.Stderr, "🌱 Sprout %s matured (session %s)\n", runResult.StepID, runResult.SessionID)
}

// buildSproutCore constructs a Core with the sprout execution port wired and
// runs recorded in the persistent history DB (matching the daemon surfaces).
// The returned cleanup releases the history database.
func buildSproutCore(ctx context.Context) (core.Core, func(), error) {
	history, err := historydb.OpenFromEnv(ctx, resolveRepoRoot(""))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ History database unavailable: %v (continuing without persistence)\n", err)
		history = nil
	}

	var store session.Store
	cleanup := func() {}
	if history != nil {
		store = history
		cleanup = func() { _ = history.Close() }
	}

	manager, err := session.NewManager(ctx, store)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return core.NewService(manager).WithSprout(sproutOps(history)), cleanup, nil
}

// sproutOps binds the sprout execution port to the conductor's terrarium
// orchestrator and the history store — this wiring lives in the adapter layer
// precisely so the Core never imports the conductor or historydb (see
// internal/core/boundary_test.go). It owns exactly what the MCP adapter used
// to do inline after translation: named-substrate resolution, status-path
// computation, the terrarium run, and run recording.
func sproutOps(history *historydb.Store) core.SproutOps {
	substratesConfig, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		log.Printf("[Sprout] Failed to load substrates config: %v", err)
	}

	return core.SproutOps{
		Run: func(ctx context.Context, spec core.SproutSpec) (string, error) {
			substrateURL := spec.SubstrateURL
			substrateBranch := spec.SubstrateBranch
			resolvedSubstrate := spec.Substrate
			explicitSubstrateURL := strings.TrimSpace(spec.SubstrateURL)
			substrateIsNamed := false
			substrateHasLocalPath := false
			if substrateSpec, isName := conductor.ResolveSubstrate(spec.Substrate, substratesConfig); isName && substrateSpec != nil {
				substrateIsNamed = true
				if strings.TrimSpace(substrateURL) == "" {
					substrateURL = strings.TrimSpace(substrateSpec.URL)
				}
				if strings.TrimSpace(substrateBranch) == "" {
					substrateBranch = strings.TrimSpace(substrateSpec.Branch)
				}
				if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
					if info, err := os.Stat(trimmedPath); err == nil && info.IsDir() {
						resolvedSubstrate = trimmedPath
						substrateHasLocalPath = true
					}
				}
			}

			statusPath := ""
			if explicitSubstrateURL == "" {
				if !substrateIsNamed || substrateHasLocalPath {
					if resolvedSubstrate != "" {
						statusPath = filepath.Join(resolveRepoRoot(resolvedSubstrate), "tendril-status.json")
					}
				}
			}

			log.Printf("[Sprout] Delegating transcript to Tendril step %s: %s (Substrate: %s, URL: %s)", spec.StepID, spec.Transcript, resolvedSubstrate, substrateURL)
			orch := &conductor.DockerOrchestrator{
				Substrate:       resolvedSubstrate,
				SubstrateURL:    substrateURL,
				SubstrateBranch: substrateBranch,
				StepID:          spec.StepID,
				StatusPath:      statusPath,
				Provider:        spec.Provider,
				Model:           spec.Model,
				Genotype:        spec.Genotype,
			}

			run := historydb.SproutRun{
				RunID:      spec.StepID,
				StepID:     spec.StepID,
				SessionID:  spec.SessionID,
				Origin:     spec.Origin,
				Transcript: spec.Transcript,
				Model:      spec.Model,
				Genotype:   spec.Genotype,
				Status:     "running",
				StartedAt:  time.Now().UTC(),
			}
			recordRun := func() {
				if history == nil {
					return
				}
				if recordErr := history.RecordSproutRun(context.WithoutCancel(ctx), run); recordErr != nil {
					log.Printf("[Sprout] Failed to record sprout run: %v", recordErr)
				}
			}
			recordRun()

			output, err := orch.RunTendril(ctx, spec.Transcript)
			run.FinishedAt = time.Now().UTC()
			if err != nil {
				run.Status = "withered"
				run.Error = err.Error()
			} else {
				run.Status = "matured"
				run.Output = output
			}
			recordRun()

			return output, err
		},
	}
}

// sproutCommand is one subcommand actually registered on the
// `tendril sprout` command tree.
type sproutCommand struct {
	name       string // CLI token, e.g. "run"
	capability string // the governed Core capability it invokes
}

// sproutCommands is the CLI command tree for `tendril sprout`. Like
// sessionCommands, this registration — NOT core.CapabilityNames() — is the
// source of truth the parity coverage test reads for the CLI arm.
var sproutCommands = []sproutCommand{
	{"run", core.CapSproutRun},
}

// lookupSproutCommand resolves a CLI subcommand token to its registered entry.
func lookupSproutCommand(sub string) (sproutCommand, bool) {
	for _, command := range sproutCommands {
		if command.name == sub {
			return command, true
		}
	}
	return sproutCommand{}, false
}

// sproutCLICapabilityNames returns the capability names the CLI has actually
// registered sprout subcommands for, sorted. Read by the parity coverage test.
func sproutCLICapabilityNames() []string {
	names := make([]string, 0, len(sproutCommands))
	for _, command := range sproutCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parseSproutArgs turns CLI args into a capability input map: the positional
// args are the transcript (joined), with flags for the rest; `--json '{...}'`
// is the generic escape hatch, mirroring parseSessionArgs.
func parseSproutArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	var positional []string
	stringFlag := func(i *int, key string) error {
		if *i+1 >= len(args) {
			return fmt.Errorf("flag %s requires a value", args[*i])
		}
		*i++
		input[key] = args[*i]
		return nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch args[i] {
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
		case "--step":
			err = stringFlag(&i, "stepId")
		case "--session":
			err = stringFlag(&i, "sessionId")
		case "--substrate-url":
			err = stringFlag(&i, "substrateUrl")
		case "--substrate-branch":
			err = stringFlag(&i, "substrateBranch")
		case "--origin":
			err = stringFlag(&i, "origin")
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, fmt.Errorf("unknown argument %q for sprout %s", args[i], strings.TrimPrefix(capName, "sprout."))
			}
			positional = append(positional, args[i])
		}
		if err != nil {
			return nil, err
		}
	}

	if len(positional) > 0 {
		input["transcript"] = strings.Join(positional, " ")
	}
	if transcript, _ := input["transcript"].(string); strings.TrimSpace(transcript) == "" {
		return nil, fmt.Errorf("missing transcript. Usage: tendril sprout run --substrate <path|name> <transcript>")
	}
	if substrate, _ := input["substrate"].(string); strings.TrimSpace(substrate) == "" {
		return nil, fmt.Errorf("missing substrate. Usage: tendril sprout run --substrate <path|name> <transcript>")
	}
	return input, nil
}

func printSproutUsage() {
	fmt.Println("Usage: tendril sprout run --substrate <path|name> [flags] <transcript...>")
	fmt.Println("  --substrate         The absolute path or named substrate key of the target workspace (required)")
	fmt.Println("  --session ID        Bind the run to an existing Tendril session (its preferences shape the sprout)")
	fmt.Println("  --step ID           Pin a stable step identifier")
	fmt.Println("  --substrate-url U   Remote repository URL override to clone dynamically")
	fmt.Println("  --substrate-branch B Branch to clone when --substrate-url is set")
	fmt.Println()
	fmt.Println("run is a projection of the shared Core capability registry.")
}
