package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// runSeedCmd is the CLI adapter for the governed seed/grow capability family —
// grow a Seed (a bounded intent) to Fruit: a thin projection of the same
// transport-free core.Core the REST and MCP surfaces use. `tendril seed grow`
// hands off a Seed — build toward a goal and iterate until a verify command
// exits 0 — and prints the reviewable Fruit.
//
// A CLI invocation is never delegated (there is no Pollen), so its egress
// allow-list is always empty: deny-all, the secure default. Only a delegated
// invocation under a grant with egress hosts gains Stem-mediated reach.
func runSeedCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printSeedUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printSeedUsage()
		return
	case "collect":
		runSeedCollect(ctx, args[1:])
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	command, ok := lookupSeedCommand(sub)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown seed command: %s\n", args[0])
		printSeedUsage()
		os.Exit(1)
	}

	rest, async := extractSeedAsyncFlag(args[1:])
	input, err := parseSeedArgs(command.capability, rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if origin, _ := input["origin"].(string); strings.TrimSpace(origin) == "" {
		input["origin"] = session.OriginCLI
	}

	// --async hands the growth to the running daemon and returns a handle,
	// rather than growing in-process and blocking until the Seed settles.
	if async {
		submitSeedAsync(ctx, input)
		return
	}

	svc, err := buildSeedCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Seed grow failed: %v\n", err)
		os.Exit(1)
	}

	growResult, _ := result.(core.SeedGrowResult)
	if strings.TrimSpace(growResult.Logs) != "" {
		fmt.Fprintln(os.Stderr, growResult.Logs)
	}
	if strings.TrimSpace(growResult.Diff) != "" {
		fmt.Fprintln(os.Stdout, growResult.Diff)
	}
	fmt.Fprintf(os.Stderr, "🌱 Seed %s after %d iteration(s)", growResult.Status, growResult.Iterations)
	if strings.TrimSpace(growResult.Branch) != "" {
		fmt.Fprintf(os.Stderr, " on branch %s", growResult.Branch)
	}
	fmt.Fprintln(os.Stderr)
	if growResult.Status != core.SeedStatusSatisfied {
		os.Exit(1)
	}
}

// buildSeedCore constructs a Core with the Seed-growth execution port wired,
// matching the daemon surfaces. The execution port is the stub for now: the
// capability is governed and projected on every surface, but invoking it reports
// that execution is not wired rather than acting.
func buildSeedCore(ctx context.Context) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	return core.NewService(manager).WithSeed(seedOperations()), nil
}

// seedOperations binds the Seed-growth execution port to the conductor's
// bounded-task executor — the Sprout builder loop plus the sealed-Terrarium
// verify run. This wiring lives in the adapter layer precisely so the Core never
// imports the conductor (see internal/core/boundary_test.go); it translates the
// Core's transport-free spec into the conductor's execution request and the
// reviewable Fruit back.
func seedOperations() core.SeedOperations {
	return core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec) (core.SeedGrowResult, error) {
			result, err := conductor.RunSeed(ctx, conductor.SeedExecution{
				Substrate:     spec.Substrate,
				Goal:          spec.Goal,
				Verify:        spec.Verify,
				MaxIterations: spec.MaxIterations,
				Timeout:       spec.Timeout,
				Egress:        spec.Egress,
			})
			if err != nil {
				return core.SeedGrowResult{}, err
			}
			return core.SeedGrowResult{
				Status:     result.Status,
				Iterations: result.Iterations,
				Branch:     result.Branch,
				Diff:       result.Diff,
				Logs:       result.Logs,
			}, nil
		},
	}
}

// seedCommand is one subcommand actually registered on the `tendril seed`
// command tree.
type seedCommand struct {
	name       string // CLI token, e.g. "grow"
	capability string // the governed Core capability it invokes
}

// seedCommands is the CLI command tree for `tendril seed`. Like
// stomaCommands, this registration — NOT core.CapabilityNames() — is the
// source of truth the parity coverage test reads for the CLI arm.
var seedCommands = []seedCommand{
	{"grow", core.CapSeedGrow},
}

// lookupSeedCommand resolves a CLI subcommand token to its registered entry.
func lookupSeedCommand(sub string) (seedCommand, bool) {
	for _, command := range seedCommands {
		if command.name == sub {
			return command, true
		}
	}
	return seedCommand{}, false
}

// seedCLICapabilityNames returns the capability names the CLI has actually
// registered seed subcommands for, sorted. Read by the parity coverage test.
func seedCLICapabilityNames() []string {
	names := make([]string, 0, len(seedCommands))
	for _, command := range seedCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parseSeedArgs turns CLI args into a capability input map. Flags come first;
// every argument after a bare `--` (and every non-flag positional) is a verify
// command token, so `tendril seed grow --substrate . --goal "fix tests" -- go
// test ./...` carries the verify command's own flags untouched. `--json '{...}'`
// is the generic escape hatch, mirroring parseStomaArgs.
func parseSeedArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	var verify []string
	verifyOnly := false
	stringFlag := func(i *int, key string) error {
		if *i+1 >= len(args) {
			return fmt.Errorf("flag %s requires a value", args[*i])
		}
		*i++
		input[key] = args[*i]
		return nil
	}
	intFlag := func(i *int, key, unit string) error {
		if *i+1 >= len(args) {
			return fmt.Errorf("flag %s requires a value (%s)", args[*i], unit)
		}
		*i++
		var n int
		if _, scanErr := fmt.Sscanf(args[*i], "%d", &n); scanErr != nil {
			return fmt.Errorf("invalid %s value %q (want %s)", key, args[*i], unit)
		}
		input[key] = n
		return nil
	}
	for i := 0; i < len(args); i++ {
		if verifyOnly {
			verify = append(verify, args[i])
			continue
		}
		var err error
		switch args[i] {
		case "--":
			verifyOnly = true
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
		case "--goal":
			err = stringFlag(&i, "goal")
		case "--max-iterations":
			err = intFlag(&i, "maxIterations", "count")
		case "--timeout":
			err = intFlag(&i, "timeoutSeconds", "seconds")
		case "--origin":
			err = stringFlag(&i, "origin")
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, fmt.Errorf("unknown argument %q for seed %s (use `--` before the verify command's own flags)", args[i], strings.TrimPrefix(capName, "seed."))
			}
			verify = append(verify, args[i])
		}
		if err != nil {
			return nil, err
		}
	}

	if len(verify) > 0 {
		tokens := make([]any, 0, len(verify))
		for _, token := range verify {
			tokens = append(tokens, token)
		}
		input["verify"] = tokens
	}
	if substrate, _ := input["substrate"].(string); strings.TrimSpace(substrate) == "" {
		return nil, fmt.Errorf("missing substrate. Usage: tendril seed grow --substrate <path|name> --goal <goal> -- <verify command...>")
	}
	if goal, _ := input["goal"].(string); strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("missing goal. Usage: tendril seed grow --substrate <path|name> --goal <goal> -- <verify command...>")
	}
	if _, ok := input["verify"]; !ok {
		return nil, fmt.Errorf("missing verify command. Usage: tendril seed grow --substrate <path|name> --goal <goal> -- <verify command...>")
	}
	return input, nil
}

// extractSeedAsyncFlag removes a --async flag appearing before the `--`
// separator (never from the verify command's own tokens) and reports whether
// async dispatch was requested.
func extractSeedAsyncFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	async := false
	pastSeparator := false
	for _, arg := range args {
		if !pastSeparator && arg == "--async" {
			async = true
			continue
		}
		if arg == "--" {
			pastSeparator = true
		}
		out = append(out, arg)
	}
	return out, async
}

// seedDaemonRequest issues an authenticated request to the local Stem daemon —
// the same bearer-authenticated client path the detached sprout and sequence
// commands use. Async dispatch and collection are daemon operations because the
// growth and its durable handle live in the persistent serve process, not in a
// one-shot CLI invocation.
func seedDaemonRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://localhost:%s%s", port, path), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := strings.TrimSpace(os.Getenv(EnvBotanistKey)); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return http.DefaultClient.Do(req)
}

// submitSeedAsync dispatches a Seed to the daemon and prints the durable handle
// the operator collects the Fruit by later.
func submitSeedAsync(ctx context.Context, input map[string]any) {
	body := map[string]any{}
	for _, key := range []string{"substrate", "goal", "verify", "maxIterations", "timeoutSeconds", "origin"} {
		if v, ok := input[key]; ok {
			body[key] = v
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to encode seed request: %v\n", err)
		os.Exit(1)
	}

	resp, err := seedDaemonRequest(ctx, http.MethodPost, "/v1/seeds/grow/async", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Stem daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "Ensure the OpenTendril daemon is running (`tendril serve`) to dispatch a Seed asynchronously.")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "❌ Stem daemon rejected the dispatch (status %d)\n", resp.StatusCode)
		os.Exit(1)
	}

	var accepted struct {
		Handle string `json:"handle"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to decode daemon response: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stdout, "🌱 Seed dispatched for growth.")
	fmt.Fprintf(os.Stdout, "   Handle:  %s\n", accepted.Handle)
	fmt.Fprintf(os.Stdout, "   Collect: tendril seed collect %s\n", accepted.Handle)
}

// runSeedCollect fetches the reviewable Fruit for a dispatched growth by handle.
func runSeedCollect(ctx context.Context, args []string) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(os.Stderr, "Usage: tendril seed collect <handle>")
		os.Exit(1)
	}
	handle := strings.TrimSpace(args[0])

	resp, err := seedDaemonRequest(ctx, http.MethodGet, "/v1/seeds/runs/"+handle, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Stem daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "Ensure the OpenTendril daemon is running (`tendril serve`).")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintf(os.Stderr, "❌ No seed run for handle %s\n", handle)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "❌ Collect failed (status %d)\n", resp.StatusCode)
		os.Exit(1)
	}

	var run struct {
		Status     string `json:"status"`
		Iterations int    `json:"iterations"`
		Branch     string `json:"branch"`
		Diff       string `json:"diff"`
		Logs       string `json:"logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to decode Fruit: %v\n", err)
		os.Exit(1)
	}

	if strings.TrimSpace(run.Logs) != "" {
		fmt.Fprintln(os.Stderr, run.Logs)
	}
	if strings.TrimSpace(run.Diff) != "" {
		fmt.Fprintln(os.Stdout, run.Diff)
	}
	fmt.Fprintf(os.Stderr, "🌱 Seed %s after %d iteration(s)", run.Status, run.Iterations)
	if strings.TrimSpace(run.Branch) != "" {
		fmt.Fprintf(os.Stderr, " on branch %s", run.Branch)
	}
	fmt.Fprintln(os.Stderr)

	// A still-growing Seed is not an error — collect again later. A settled Seed
	// that did not reach satisfied exits non-zero so scripts can branch on it.
	if run.Status == "running" {
		return
	}
	if run.Status != core.SeedStatusSatisfied {
		os.Exit(1)
	}
}

func printSeedUsage() {
	fmt.Println("Usage: tendril seed grow --substrate <path|name> --goal <goal> [flags] -- <verify command...>")
	fmt.Println("       tendril seed collect <handle>")
	fmt.Println("  --substrate          The absolute path or named substrate key of the target workspace (required)")
	fmt.Println("  --goal               The intent handed to the builder (required)")
	fmt.Println("  --max-iterations N   Maximum build/verify passes (default 3, maximum 10)")
	fmt.Println("  --timeout N          Whole-growth wall-clock bound in seconds (default 900, maximum 3600)")
	fmt.Println("  --async              Dispatch to the running daemon and return a handle instead of blocking; collect the Fruit later with `tendril seed collect <handle>`")
	fmt.Println("  --json '{...}'       Full JSON input (the generic escape hatch)")
	fmt.Println()
	fmt.Println("Grows a Seed: builds toward the goal and iterates until the verify command")
	fmt.Println("exits 0, inside a network-sealed terrarium. The Fruit is returned for review;")
	fmt.Println("nothing is merged. Stem-mediated reach requires a delegation grant with an")
	fmt.Println("egress allow-list (deny-all by default).")
	fmt.Println()
	fmt.Println("grow is a projection of the shared Core capability registry.")
}
