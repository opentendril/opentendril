package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// runSessionCmd is the CLI adapter for the governed session-lifecycle
// capabilities. Every subcommand is a thin projection of the same
// transport-free core.Core the REST and MCP surfaces use: it
// decodes flags/JSON into a capability input map and calls core.Invoke — no
// business logic lives here.
//
// Subcommand names are the capability names with the "session." prefix
// stripped (e.g. capability "phytomer.list" → `tendril session list`). The
// mapping is derived from core.CapabilityNames() so the CLI can never advertise
// a subcommand the registry does not declare.
func runSessionCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printSessionUsage()
		os.Exit(1)
	}
	switch strings.TrimSpace(args[0]) {
	case "-h", "--help", "help":
		printSessionUsage()
		return
	}

	sub := strings.TrimSpace(args[0])
	command, ok := lookupSessionCommand(sub)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown session subcommand: %s\n", sub)
		printSessionUsage()
		os.Exit(1)
	}
	capName := command.capability

	input, err := parseSessionArgs(capName, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	svc, cleanup, err := buildSessionCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open session store: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	result, err := svc.Invoke(ctx, capName, input)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			fmt.Fprintln(os.Stderr, "session not found")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", capName, err)
		os.Exit(1)
	}

	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode result: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(payload))
}

// buildSessionCore constructs the Core over a SessionManager backed by the same
// .tendril/history.db the daemon uses, so the CLI reads and writes the same
// persisted sessions. With TENDRIL_DB_LOGGING=false it falls back to an
// ephemeral in-memory manager.
func buildSessionCore(ctx context.Context) (core.Core, func(), error) {
	history, err := historydb.OpenFromEnv(ctx, resolveRepoRoot(""))
	if err != nil {
		return nil, func() {}, err
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
	return core.NewService(manager).WithGenome(genomeOperations(resolveRepoRoot(""))), cleanup, nil
}

// sessionCommand is one subcommand actually registered on the `tendril session`
// command tree.
type sessionCommand struct {
	name       string // CLI token, e.g. "create"
	capability string // the governed Core capability it invokes
}

// sessionCommands is the CLI command tree: the explicit set of subcommands
// `tendril session` dispatches. This registration — NOT core.CapabilityNames()
// — is the source of truth the parity coverage test reads for the CLI arm, so
// dropping an entry here both removes the subcommand and makes the CLI arm
// diverge from the canonical registry.
var sessionCommands = []sessionCommand{
	{"create", core.CapCreatePhytomer},
	{"list", core.CapListPhytomers},
	{"get", core.CapGetPhytomer},
	{"update", core.CapUpdatePhytomer},
	{"delete", core.CapDeletePhytomer},
	{"history", core.CapPhytomerHistory},
}

// lookupSessionCommand resolves a CLI subcommand token to its registered entry.
func lookupSessionCommand(sub string) (sessionCommand, bool) {
	for _, command := range sessionCommands {
		if command.name == sub {
			return command, true
		}
	}
	return sessionCommand{}, false
}

// sessionCLICapabilityNames returns the capability names the CLI has actually
// registered subcommands for, sorted. Read by the parity coverage test.
func sessionCLICapabilityNames() []string {
	names := make([]string, 0, len(sessionCommands))
	for _, command := range sessionCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parseSessionArgs turns CLI flags into a capability input map. It accepts
// positional/flag forms for the common fields and a `--json '{...}'` escape
// hatch for the full input.
func parseSessionArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	prefs := map[string]any{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--json":
			raw, err := next()
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				return nil, fmt.Errorf("invalid --json input: %w", err)
			}
			return input, nil
		case "--id", "--session":
			v, err := next()
			if err != nil {
				return nil, err
			}
			input["sessionId"] = v
		case "--origin":
			v, err := next()
			if err != nil {
				return nil, err
			}
			input["origin"] = v
		case "--provider":
			v, err := next()
			if err != nil {
				return nil, err
			}
			prefs["provider"] = v
		case "--model":
			v, err := next()
			if err != nil {
				return nil, err
			}
			prefs["model"] = v
		case "--genotype":
			v, err := next()
			if err != nil {
				return nil, err
			}
			prefs["genotype"] = v
		case "--limit":
			v, err := next()
			if err != nil {
				return nil, err
			}
			n, convErr := strconv.Atoi(v)
			if convErr != nil {
				return nil, fmt.Errorf("--limit must be an integer: %w", convErr)
			}
			input["limit"] = n
		default:
			// A bare positional token is treated as the session id for the
			// capabilities that need one.
			if !strings.HasPrefix(arg, "-") && input["sessionId"] == nil {
				input["sessionId"] = arg
				continue
			}
			return nil, fmt.Errorf("unknown flag %q for session %s", arg, strings.TrimPrefix(capName, "session."))
		}
	}

	if len(prefs) > 0 {
		input["preferences"] = prefs
	}
	if input["origin"] == nil && capName == core.CapCreatePhytomer {
		input["origin"] = session.OriginCLI
	}
	return input, nil
}

var sessionCommandHelp = map[string]string{
	core.CapCreatePhytomer:  "create a new session   (--provider --model --genotype --origin)",
	core.CapListPhytomers:   "list live sessions",
	core.CapGetPhytomer:     "get one session        (<id> | --id <id>)",
	core.CapUpdatePhytomer:  "update preferences     (<id> --provider --model --genotype)",
	core.CapDeletePhytomer:  "delete a session       (<id>)",
	core.CapPhytomerHistory: "show chat history      (<id> --limit N)",
}

func printSessionUsage() {
	fmt.Println("Usage: tendril phytomer <subcommand> [flags]   (alias: tendril session)")
	fmt.Println()
	fmt.Println("Subcommands (projections of the shared Core capability registry):")
	for _, command := range sessionCommands {
		fmt.Printf("  %-9s %s\n", command.name, sessionCommandHelp[command.capability])
	}
	fmt.Println()
	fmt.Println("Any subcommand also accepts --json '{...}' for the raw capability input.")
}
