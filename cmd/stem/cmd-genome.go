package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// runGenomeCmd is the CLI adapter for the governed genome capability family
// (issue #181, slice 1). Every subcommand is a thin projection of the same
// transport-free core.Core the REST and MCP surfaces use: it resolves the
// registered capability, calls core.Invoke, and renders the result — no
// business logic lives here.
func runGenomeCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printGenomeUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printGenomeUsage()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	command, ok := lookupGenomeCommand(sub)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown genome command: %s\n", args[0])
		printGenomeUsage()
		os.Exit(1)
	}

	input, err := parseGenomeArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	root := resolveRepoRoot("")
	svc, err := buildGenomeCore(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		switch command.capability {
		case core.CapGenomeReduce:
			fmt.Fprintf(os.Stderr, "❌ Failed to reduce genome: %v\n", err)
		case core.CapGenomeEvolve:
			fmt.Fprintf(os.Stderr, "❌ Failed to evolve genome: %v\n", err)
		default:
			fmt.Fprintf(os.Stderr, "❌ %s failed: %v\n", command.capability, err)
		}
		os.Exit(1)
	}

	renderGenomeResult(command.capability, root, result)
}

// buildGenomeCore constructs a Core with the genome execution port wired to
// the orchestrator's Epigenetic Chronicler. The session manager is an
// ephemeral in-memory one — genome capabilities touch no session state, and
// `tendril genome view` must not create a history DB as a side effect.
func buildGenomeCore(ctx context.Context, root string) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	return core.NewService(manager).WithGenome(genomeOps(root)), nil
}

// genomeOps binds the genome execution port to the orchestrator — this wiring
// lives in the adapter layer precisely so the Core never imports the
// orchestrator (see internal/core/boundary_test.go).
func genomeOps(root string) core.GenomeOps {
	return core.GenomeOps{
		Root: root,
		Reduce: func(ctx context.Context, root string) error {
			return orchestrator.NewEpigeneticChronicler(root).ReduceGenomeFile(ctx)
		},
		Evolve: orchestrator.EvolveGenome,
	}
}

// genomeCommand is one subcommand actually registered on the `tendril genome`
// command tree.
type genomeCommand struct {
	name       string // CLI token, e.g. "view"
	capability string // the governed Core capability it invokes
}

// genomeCommands is the CLI command tree for `tendril genome`. Like
// sessionCommands, this registration — NOT core.CapabilityNames() — is the
// source of truth the parity coverage test reads for the CLI arm.
var genomeCommands = []genomeCommand{
	{"view", core.CapGenomeView},
	{"reduce", core.CapGenomeReduce},
	{"evolve", core.CapGenomeEvolve},
}

// lookupGenomeCommand resolves a CLI subcommand token to its registered entry.
func lookupGenomeCommand(sub string) (genomeCommand, bool) {
	for _, command := range genomeCommands {
		if command.name == sub {
			return command, true
		}
	}
	return genomeCommand{}, false
}

// genomeCLICapabilityNames returns the capability names the CLI has actually
// registered genome subcommands for, sorted. Read by the parity coverage test.
func genomeCLICapabilityNames() []string {
	names := make([]string, 0, len(genomeCommands))
	for _, command := range genomeCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parseGenomeArgs turns CLI args into a capability input map. The genome
// capabilities take no input today; only `--json '{...}'` is accepted as the
// generic escape hatch, mirroring parseSessionArgs.
func parseGenomeArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
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
			return nil, fmt.Errorf("unknown argument %q for genome %s", args[i], strings.TrimPrefix(capName, "genome."))
		}
	}
	return input, nil
}

// renderGenomeResult presents a capability result on the terminal, preserving
// the historic `tendril genome` output byte-for-byte.
func renderGenomeResult(capability, root string, result any) {
	switch capability {
	case core.CapGenomeView:
		seeds, _ := result.([]core.GenomeSeed)
		if len(seeds) == 0 {
			fmt.Printf("No genome seeds found in %s\n", filepath.Join(root, ".tendril", "genome"))
			return
		}
		for i, seed := range seeds {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("========== %s ==========\n", seed.Path)
			fmt.Println(strings.TrimSpace(seed.Content))
		}
	case core.CapGenomeReduce:
		fmt.Printf("✅ Reduced genome at %s\n", genomeResultPath(result, root))
	case core.CapGenomeEvolve:
		fmt.Printf("✅ Evolved genome at %s\n", genomeResultPath(result, root))
	}
}

func genomeResultPath(result any, root string) string {
	if m, ok := result.(map[string]any); ok {
		if path, ok := m["path"].(string); ok && strings.TrimSpace(path) != "" {
			return path
		}
	}
	return filepath.Join(root, ".tendril", "genome", "epigenetics.md")
}

func printGenomeUsage() {
	fmt.Println("Usage: tendril genome <view|reduce|evolve>")
	fmt.Println("  view    Print the active genome seeds in alphabetical order")
	fmt.Println("  reduce  Consolidate .tendril/genome/epigenetics.md in place")
	fmt.Println("  evolve  Prune low-fitness genome material and rewrite epigenetics.md")
	fmt.Println()
	fmt.Println("Each subcommand is a projection of the shared Core capability registry.")
}
