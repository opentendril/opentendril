package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

type fruitListOptions struct {
	jsonOutput bool
	help       bool
}

// runFruitCmd is the Botanist-local presentation adapter for Fruit inventory.
// It opens the same persisted HistoryDB projection used by the serving Stem;
// classification, ordering, and counts remain in Core.
func runFruitCmd(ctx context.Context, args []string) {
	opts, err := parseFruitListArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		printFruitUsage()
		os.Exit(1)
	}
	if opts.help {
		printFruitUsage()
		return
	}

	svc, cleanup, err := buildFruitCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Fruit inventory unavailable: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	if err := executeFruitList(ctx, opts, svc, os.Stdout); err != nil {
		if errors.Is(err, core.ErrFruitInventoryNotWired) {
			fmt.Fprintf(os.Stderr, "❌ Fruit inventory unavailable: persistent observation is not wired (%v)\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "❌ Fruit inventory unavailable: %v\n", err)
		}
		os.Exit(1)
	}
}

func parseFruitListArgs(args []string) (fruitListOptions, error) {
	if len(args) == 0 {
		return fruitListOptions{}, fmt.Errorf("fruit requires the list subcommand")
	}
	if len(args) == 1 && isHelpArg(args[0]) {
		return fruitListOptions{help: true}, nil
	}
	if strings.ToLower(strings.TrimSpace(args[0])) != "list" {
		return fruitListOptions{}, fmt.Errorf("unknown fruit subcommand %q", args[0])
	}

	opts := fruitListOptions{}
	for _, arg := range args[1:] {
		switch strings.TrimSpace(arg) {
		case "--json":
			opts.jsonOutput = true
		case "-h", "--help", "help":
			if len(args) != 2 {
				return fruitListOptions{}, fmt.Errorf("fruit list help must be the only argument")
			}
			opts.help = true
		default:
			return fruitListOptions{}, fmt.Errorf("unknown argument %q for fruit list", arg)
		}
	}
	return opts, nil
}

// buildFruitCore wires the local Botanist view to the same durable evidence
// source as the long-lived Stem. A disabled or absent HistoryDB intentionally
// leaves the Core view unwired so callers cannot mistake missing persistence
// for an empty inventory.
func buildFruitCore(ctx context.Context) (core.Core, func(), error) {
	history, err := historydb.OpenFromEnv(ctx, resolveRepoRoot(""))
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() {}
	if history != nil {
		cleanup = func() { _ = history.Close() }
	}
	return core.NewService(nil).WithFruitInventoryObservationSource(fruitInventorySource(history)), cleanup, nil
}

func executeFruitList(ctx context.Context, opts fruitListOptions, observer interface {
	ObserveFruitInventory(context.Context) (core.FruitInventory, error)
}, out io.Writer) error {
	inventory, err := observer.ObserveFruitInventory(ctx)
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(inventory)
	}
	writeFruitInventoryText(out, inventory)
	return nil
}

func writeFruitInventoryText(out io.Writer, inventory core.FruitInventory) {
	fmt.Fprintln(out, "Fruit review pressure:")
	fmt.Fprintf(out, "  outstanding: %d\n", inventory.Counts.Outstanding)
	fmt.Fprintf(out, "  unknown: %d\n", inventory.Counts.Unknown)
	fmt.Fprintf(out, "  closed-unmerged: %d\n", inventory.Counts.ClosedUnmerged)
	fmt.Fprintf(out, "  merged: %d\n", inventory.Counts.Merged)
	fmt.Fprintf(out, "  total: %d\n", inventory.Counts.Total)

	if len(inventory.Items) == 0 {
		fmt.Fprintln(out, "\nNo Fruit inventory items.")
		return
	}

	for index, item := range inventory.Items {
		fmt.Fprintf(out, "\nFruit %d:\n", index+1)
		fmt.Fprintf(out, "  producer kind: %s\n", item.ProducerKind)
		fmt.Fprintf(out, "  producer identity: %s\n", item.ProducerIdentity)
		writeFruitTextField(out, "phytomer identity", item.PhytomerID)
		writeFruitTextField(out, "substrate", item.Substrate)
		fmt.Fprintf(out, "  repository: %s\n", item.Repository)
		fmt.Fprintf(out, "  branch: %s\n", item.Branch)
		fmt.Fprintf(out, "  commit: %s\n", item.Commit)
		fmt.Fprintf(out, "  publication state: %s\n", item.PublicationState)
		fmt.Fprintf(out, "  review state: %s\n", item.ReviewState)
		writeFruitTextField(out, "unknown reason", item.UnknownReason)
		if item.PullRequest != 0 {
			fmt.Fprintf(out, "  pull request: #%d\n", item.PullRequest)
		}
	}
}

func writeFruitTextField(out io.Writer, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(out, "  %s: %s\n", label, value)
}

func printFruitUsage() {
	fmt.Println("Usage: tendril fruit list [--json]")
	fmt.Println("  list       Show the Botanist's deterministic Fruit review inventory")
	fmt.Println("  --json     Emit the Core FruitInventory contract as JSON")
}
