package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/mesh"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// runPlasmidCmd is the CLI adapter for the governed plasmid capability family
// . The list/inject subcommands are thin projections of
// the same transport-free core.Core the REST and MCP surfaces use: they
// resolve the registered capability, call core.Invoke, and render the result
// — no business logic lives here.
//
// `plasmid sign` stays a deliberately ungoverned, CLI-local command: it signs
// files with the node's private signing key, an authority that must not be
// projected onto the network surfaces (see internal/core/plasmid.go).
func runPlasmidCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printPlasmidUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printPlasmidUsage()
		return
	case "sign":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "❌ Missing plasmid path. Usage: tendril plasmid sign <path>")
			os.Exit(1)
		}
		if err := runPlasmidSignCmd(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	command, ok := lookupPlasmidCommand(sub)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown plasmid command: %s\n", args[0])
		printPlasmidUsage()
		os.Exit(1)
	}

	input, err := parsePlasmidArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}

	root := resolveRepoRoot("")
	svc, err := buildPlasmidCore(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}

	renderPlasmidResult(command.capability, root, result)
}

// buildPlasmidCore constructs a Core with the plasmid execution port wired to
// the conductor. The session manager is an ephemeral in-memory one — plasmid
// capabilities touch no session state, and `tendril plasmid list` must not
// create a history DB as a side effect.
func buildPlasmidCore(ctx context.Context, root string) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	return core.NewService(manager).WithPlasmid(plasmidOperations(root)), nil
}

// plasmidOperations binds the plasmid execution port to the conductor — this wiring
// lives in the adapter layer precisely so the Core never imports the
// conductor (see internal/core/boundary_test.go).
func plasmidOperations(root string) core.PlasmidOperations {
	return core.PlasmidOperations{
		Root: root,
		Inject: func(_ context.Context, root, name string) (core.PlasmidInjection, error) {
			source, dest, alreadyActive, err := conductor.InjectPlasmidIntoGenome(root, name)
			if err != nil {
				return core.PlasmidInjection{}, err
			}
			return core.PlasmidInjection{Source: source, Dest: dest, AlreadyActive: alreadyActive}, nil
		},
	}
}

// plasmidCommand is one subcommand actually registered on the
// `tendril plasmid` command tree.
type plasmidCommand struct {
	name       string // CLI token, e.g. "list"
	capability string // the governed Core capability it invokes
}

// plasmidCommands is the CLI command tree for `tendril plasmid` (governed
// subcommands only — `sign` is ungoverned and dispatched separately). Like
// sessionCommands, this registration — NOT core.CapabilityNames() — is the
// source of truth the parity coverage test reads for the CLI arm.
var plasmidCommands = []plasmidCommand{
	{"list", core.CapPlasmidList},
	{"inject", core.CapPlasmidInject},
}

// lookupPlasmidCommand resolves a CLI subcommand token to its registered entry.
func lookupPlasmidCommand(sub string) (plasmidCommand, bool) {
	for _, command := range plasmidCommands {
		if command.name == sub {
			return command, true
		}
	}
	return plasmidCommand{}, false
}

// plasmidCLICapabilityNames returns the capability names the CLI has actually
// registered plasmid subcommands for, sorted. Read by the parity coverage test.
func plasmidCLICapabilityNames() []string {
	names := make([]string, 0, len(plasmidCommands))
	for _, command := range plasmidCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parsePlasmidArgs turns CLI args into a capability input map. Inject takes
// the plasmid name as positional arguments (joined, preserving the historic
// multi-word behavior); `--json '{...}'` is the generic escape hatch,
// mirroring parseSessionArgs.
func parsePlasmidArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --json requires a value")
			}
			i++
			if err := json.Unmarshal([]byte(args[i]), &input); err != nil {
				return nil, fmt.Errorf("invalid --json input: %w", err)
			}
		default:
			if capName == core.CapPlasmidInject {
				positional = append(positional, args[i])
				continue
			}
			return nil, fmt.Errorf("unknown argument %q for plasmid %s", args[i], strings.TrimPrefix(capName, "plasmid."))
		}
	}

	if capName == core.CapPlasmidInject {
		if len(positional) > 0 {
			input["name"] = strings.Join(positional, " ")
		}
		if name, _ := input["name"].(string); strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("Missing plasmid name. Usage: tendril plasmid inject <name>")
		}
	}
	return input, nil
}

// renderPlasmidResult presents a capability result on the terminal,
// preserving the historic `tendril plasmid` output byte-for-byte.
func renderPlasmidResult(capability, root string, result any) {
	switch capability {
	case core.CapPlasmidList:
		paths, _ := result.([]string)
		if len(paths) == 0 {
			fmt.Printf("No plasmids found under %s\n", filepath.Join(root, ".tendril", "genotypes"))
			return
		}
		for _, path := range paths {
			fmt.Println(path)
		}
	case core.CapPlasmidInject:
		injection, _ := result.(core.PlasmidInjection)
		if injection.AlreadyActive {
			fmt.Printf("✅ Plasmid already active: %s\n", injection.Dest)
			return
		}
		fmt.Printf("✅ Injected plasmid %s -> %s\n", injection.Source, injection.Dest)
	}
}

// runPlasmidSignCmd signs a plasmid with the node signing key. Ungoverned by
// design — see the package comment on runPlasmidCmd.
func runPlasmidSignCmd(path string) error {
	root := resolveRepoRoot("")
	privateKey, err := mesh.LoadPrivateKey(root)
	if err != nil {
		return fmt.Errorf("load node private signing key: %w", err)
	}

	sig, err := conductor.SignPlasmid(path, privateKey)
	if err != nil {
		return fmt.Errorf("sign plasmid: %w", err)
	}
	if err := conductor.WritePlasmidSignature(path, sig); err != nil {
		return fmt.Errorf("write plasmid signature: %w", err)
	}

	fmt.Println(path + ".sig")
	return nil
}

func printPlasmidUsage() {
	fmt.Println("Usage: tendril plasmid <list|inject|sign>")
	fmt.Println("  list           List available plasmid Markdown files")
	fmt.Println("  inject <name>  Copy a plasmid into .tendril/genome/")
	fmt.Println("  sign <path>    Sign a plasmid Markdown file")
	fmt.Println()
	fmt.Println("list and inject are projections of the shared Core capability registry.")
}
