package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// runGenotypeCmd is the CLI adapter for the governed genotype capability family.
func runGenotypeCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printGenotypeUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printGenotypeUsage()
		return
	case "create":
		runGenotypeCreateCmd(ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "❌ Unknown genotype command: %s\n", args[0])
		printGenotypeUsage()
		os.Exit(1)
	}
}

func printGenotypeUsage() {
	fmt.Println("Usage: tendril genotype <command> [options]")
	fmt.Println("\nCommands:")
	fmt.Printf("  %-11s  %s\n", "create", "Dynamically create or update a genotype")
}

func runGenotypeCreateCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	name := fs.String("name", "", "The unique name of the genotype")
	instructions := fs.String("instructions", "", "The system prompt or instructions")
	fs.Parse(args)

	if *name == "" || *instructions == "" {
		fmt.Fprintln(os.Stderr, "❌ --name and --instructions are required.")
		fs.Usage()
		os.Exit(1)
	}

	substrateDir := resolveRepoRoot("")

	delegation := newCLIDelegation(ctx)
	defer delegation.Close()
	ctx = delegation.Authorize(ctx, core.CapGenotypeCreate, substrateDir)

	coreSvc, err := buildGenotypeCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	req := core.GenotypeCreateInput{
		Substrate:    substrateDir,
		Name:         *name,
		Instructions: *instructions,
	}

	_, err = coreSvc.Invoke(ctx, core.CapGenotypeCreate, map[string]interface{}{
		"substrate":    req.Substrate,
		"name":         req.Name,
		"instructions": req.Instructions,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ genotype create failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully created genotype '%s'.\n", *name)
}

func buildGenotypeCore(ctx context.Context) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	return core.NewService(manager), nil
}

func genotypeCLICapabilityNames() []string {
	return []string{core.CapGenotypeCreate}
}
