package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// runSproutCmd is the CLI adapter for the governed sprout/run capability
// family: a thin projection of the same transport-free core.Core
// the REST and MCP surfaces use. `tendril sprout grow` delegates a one-shot
// task to an autonomous Tendril in a secure terrarium and prints its output —
// the headless CLI equivalent of the MCP sproutTendril tool.
//
// `--detach` is adapter-local: it hands the run to the Stem
// daemon's async endpoint instead of executing in-process.
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

	input, detach, err := parseSproutArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if origin, _ := input["origin"].(string); strings.TrimSpace(origin) == "" {
		input["origin"] = session.OriginCLI
	}

	if detach {
		submitSproutAsync(ctx, input)
		return
	}

	svc, cleanup, err := buildSproutCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		if errors.Is(err, conductor.ErrSproutTimedOut) {
			fmt.Fprintf(os.Stderr, "⏱️ Sprout run timed out before finishing: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "❌ Sprout run failed: %v\n", err)
		}
		cleanup()
		os.Exit(1)
	}

	runResult, _ := result.(core.SproutRunResult)
	if strings.TrimSpace(runResult.Output) != "" {
		fmt.Fprintln(os.Stdout, runResult.Output)
	}
	fmt.Fprintln(os.Stderr, sproutRunFooter(runResult))
}

// sproutRunFooter renders the command line's closing verdict for a finished
// sprout run. It reports the actual outcome — a run that changed nothing must
// say so instead of being dressed as "matured", which is how a run that never
// touched its target file once reported success.
func sproutRunFooter(result core.SproutRunResult) string {
	switch result.Outcome {
	case conductor.SproutOutcomeNoChanges:
		return fmt.Sprintf("🌾 Sprout %s finished without changing any files (session %s). This can be legitimate for investigate-and-report tasks; if files were expected to change, the task did not happen.", result.StepID, result.SessionID)
	case conductor.SproutOutcomeNoEngagement:
		return fmt.Sprintf("🥀 Sprout %s withered: the Sprout produced no response and changed nothing — the Mycorrhizal Network never engaged the task (session %s). This is not a successful run; verify the configured model can drive tools.", result.StepID, result.SessionID)
	case conductor.SproutOutcomeSkipped:
		return fmt.Sprintf("⏭️ Sprout %s skipped: this step already completed in a previous run (session %s)", result.StepID, result.SessionID)
	case conductor.SproutOutcomeComplete:
		if len(result.FilesModified) > 0 {
			return fmt.Sprintf("🌱 Sprout %s matured: %d file(s) changed (session %s)", result.StepID, len(result.FilesModified), result.SessionID)
		}
		return fmt.Sprintf("🌱 Sprout %s matured (session %s)", result.StepID, result.SessionID)
	default:
		// An unknown outcome is reported as-is rather than upgraded to a
		// success claim.
		return fmt.Sprintf("🌱 Sprout %s finished with outcome %q (session %s)", result.StepID, result.Outcome, result.SessionID)
	}
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
	return core.NewService(manager).WithSprout(sproutOperations(history, nil)), cleanup, nil
}

// sproutOperations binds the sprout execution port to the conductor's terrarium
// orchestrator, an event bus, and the history store — this wiring lives in the adapter layer
// precisely so the Core never imports the conductor or historydb (see
// internal/core/boundary_test.go). It owns exactly what the MCP adapter used
// to do inline after translation: named-substrate resolution, status-path
// computation, the terrarium run, and run recording.
// sproutSubstrateWiring is what a sprout run derives from its spec and the
// substrates configuration before the orchestrator is built.
type sproutSubstrateWiring struct {
	// Substrate is passed to the orchestrator exactly as the caller wrote it,
	// name included. See resolveSproutSubstrateWiring for why that matters.
	Substrate string
	URL       string
	Branch    string
	// StatusPath is the one place the local path is wanted, because a status
	// file has to be written somewhere real.
	StatusPath string
}

// resolveSproutSubstrateWiring fills in a spec's blanks from a named
// substrate's configuration — its URL and branch — and works out where the
// status file belongs.
//
// It deliberately does NOT replace the substrate with the local path it
// resolves. resolveSubstrateExecutionPlan looks the spec up by name to apply
// its identity, signing, auth and readonly settings, and resolves the path
// itself; handing the orchestrator a path leaves that lookup nothing to match,
// and every one of those settings is then silently skipped. A substrate marked
// readonly would be written to and merged back.
//
// The local path is still needed for StatusPath, which is why it is resolved
// here and kept out of the substrate.
func resolveSproutSubstrateWiring(spec core.SproutSpec, config *conductor.SubstratesConfig) sproutSubstrateWiring {
	wiring := sproutSubstrateWiring{
		Substrate: spec.Substrate,
		URL:       spec.SubstrateURL,
		Branch:    spec.SubstrateBranch,
	}

	explicitURL := strings.TrimSpace(spec.SubstrateURL)
	localPath := strings.TrimSpace(spec.Substrate)
	isNamed := false
	hasLocalPath := false

	if substrateSpec, isName := conductor.ResolveSubstrate(spec.Substrate, config); isName && substrateSpec != nil {
		isNamed = true
		if strings.TrimSpace(wiring.URL) == "" {
			wiring.URL = strings.TrimSpace(substrateSpec.URL)
		}
		if strings.TrimSpace(wiring.Branch) == "" {
			wiring.Branch = strings.TrimSpace(substrateSpec.Branch)
		}
		if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
			if info, err := os.Stat(trimmedPath); err == nil && info.IsDir() {
				localPath = trimmedPath
				hasLocalPath = true
			}
		}
	}

	if explicitURL == "" && (!isNamed || hasLocalPath) && localPath != "" {
		wiring.StatusPath = filepath.Join(resolveRepoRoot(localPath), "tendril-status.json")
	}

	return wiring
}

func sproutOperations(history *historydb.Store, ambientBus *eventbus.Bus) core.SproutOperations {
	substratesConfig, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		log.Printf("[Sprout] Failed to load substrates config: %v", err)
	}

	return core.SproutOperations{
		Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			wiring := resolveSproutSubstrateWiring(spec, substratesConfig)

			// A Sprout run always has a bus. The Sprout streams only when it
			// has one to publish to, so without it the run emits nothing for
			// its whole duration — no tokens, no reasoning, no lifecycle — and
			// a wall clock is the only thing left to judge it by.
			//
			// A surface that owns a live bus (the daemon) passes it in, so the
			// run's events reach live subscribers (/ws, telemetry) as well as
			// the history sink already attached to it. A one-shot surface (the
			// command line, the MCP stdio server) passes nil and gets a
			// per-run bus with the history store attached as a sink, so the
			// events outlive the process and the run can be explained
			// afterwards.
			bus := ambientBus
			if bus == nil {
				bus = eventbus.New()
				if history != nil {
					bus.AttachSink(history, 0)
				}
				defer bus.Shutdown()
			}

			log.Printf("[Sprout] Delegating transcript to Tendril step %s: %s (Substrate: %s, URL: %s)", spec.StepID, spec.Transcript, wiring.Substrate, wiring.URL)
			orch := &conductor.DockerOrchestrator{
				Substrate:       wiring.Substrate,
				SubstrateURL:    wiring.URL,
				SubstrateBranch: wiring.Branch,
				StepID:          spec.StepID,
				StatusPath:      wiring.StatusPath,
				Provider:        spec.Provider,
				Model:           spec.Model,
				Genotype:        spec.Genotype,
				EventBus:        bus,
				SessionID:       spec.SessionID,
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

			sproutReport, err := orch.RunSprout(ctx, spec.Transcript)
			run.FinishedAt = time.Now().UTC()
			if err != nil {
				run.Status = "withered"
				run.Error = err.Error()
			} else {
				run.Status = "matured"
				// A run that never engaged the task is not a success, even
				// though the Sprout's loop returned no error.
				if sproutReport.Outcome == conductor.SproutOutcomeNoEngagement {
					run.Status = "withered"
				}
				run.Output = sproutReport.Output
			}
			recordRun()

			return core.SproutRunReport{
				Output:        sproutReport.Output,
				Outcome:       sproutReport.Outcome,
				FilesModified: sproutReport.FilesModified,
			}, err
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
	{"grow", core.CapSproutGrow},
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

// parseSproutArgs turns CLI args into a capability input map plus the
// adapter-local detach flag. Positional args are the transcript (joined), with
// flags for the rest; `--json '{...}'` is the generic escape hatch, mirroring
// parseSessionArgs / parseSequenceArgs.
func parseSproutArgs(capName string, args []string) (map[string]any, bool, error) {
	input := map[string]any{}
	detach := false
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
				return nil, false, fmt.Errorf("flag --json requires a value")
			}
			i++
			if jsonErr := json.Unmarshal([]byte(args[i]), &input); jsonErr != nil {
				return nil, false, fmt.Errorf("invalid --json input: %w", jsonErr)
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
		case "--detach", "-d":
			detach = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, false, fmt.Errorf("unknown argument %q for sprout %s", args[i], strings.TrimPrefix(capName, "sprout."))
			}
			positional = append(positional, args[i])
		}
		if err != nil {
			return nil, false, err
		}
	}

	if len(positional) > 0 {
		input["transcript"] = strings.Join(positional, " ")
	}
	if transcript, _ := input["transcript"].(string); strings.TrimSpace(transcript) == "" {
		return nil, false, fmt.Errorf("missing transcript. Usage: tendril sprout grow --substrate <path|name> <transcript>")
	}
	if substrate, _ := input["substrate"].(string); strings.TrimSpace(substrate) == "" {
		return nil, false, fmt.Errorf("missing substrate. Usage: tendril sprout grow --substrate <path|name> <transcript>")
	}
	return input, detach, nil
}

func printSproutUsage() {
	fmt.Println("Usage: tendril sprout grow --substrate <path|name> [flags] <transcript...>")
	fmt.Println("  --substrate         The absolute path or named substrate key of the target workspace (required)")
	fmt.Println("  --session ID        Bind the run to an existing Tendril session (its preferences shape the sprout)")
	fmt.Println("  --step ID           Pin a stable step identifier")
	fmt.Println("  --substrate-url U   Remote repository URL override to clone dynamically")
	fmt.Println("  --substrate-branch B Branch to clone when --substrate-url is set")
	fmt.Println("  --detach, -d        Run in background (requires daemon)")
	fmt.Println()
	fmt.Println("run is a projection of the shared Core capability registry.")
}

// submitSproutAsync POSTs a detached sprout run to the Stem daemon,
// mirroring submitSequenceAsync.
func submitSproutAsync(ctx context.Context, input map[string]any) {
	// Keep only fields the async REST body accepts; origin is set by the adapter.
	body := map[string]any{}
	for _, key := range []string{"transcript", "substrate", "stepId", "sessionId", "substrateUrl", "substrateBranch"} {
		if v, ok := input[key]; ok {
			body[key] = v
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to encode sprout request: %v\n", err)
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	url := fmt.Sprintf("http://localhost:%s/v1/phytomers/new/sprout/grow", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to build request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("OPENTENDRIL_API_KEY")); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else if key := strings.TrimSpace(os.Getenv("TENDRIL_API_KEY")); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := http.DefaultClient.Do(req)
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
		StepID    string `json:"stepId"`
		SessionID string `json:"sessionId"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to decode daemon response: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stdout, "🚀 Sprout submitted for detached execution.")
	fmt.Fprintf(os.Stdout, "   Step ID:    %s\n", result.StepID)
	fmt.Fprintf(os.Stdout, "   Session ID: %s\n", result.SessionID)
	fmt.Fprintf(os.Stdout, "   Watch:      GET /v1/sessions/%s/sprout-runs\n", result.SessionID)
}
