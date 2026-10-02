package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

type workspaceCommandOptions struct {
	operation string
	pollen    string
	substrate string
	dir       string
	confirm   bool
	help      bool
}

// runWorkspaceCmd is a Botanist-local control-plane adapter. These views and
// recovery operations deliberately do not use Invoke or the governed registry.
func runWorkspaceCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printWorkspaceUsage()
		return
	}
	opts, err := parseWorkspaceCommandArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		printWorkspaceUsage()
		os.Exit(1)
	}
	if opts.help {
		printWorkspaceUsage()
		return
	}
	if err := executeWorkspaceCommand(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

func parseWorkspaceCommandArgs(args []string) (workspaceCommandOptions, error) {
	opts := workspaceCommandOptions{}
	if len(args) == 1 && isHelpArg(args[0]) {
		opts.help = true
		return opts, nil
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "abandon") {
		return opts, fmt.Errorf("workspace requires inspect or abandon")
	}
	opts.operation = args[0]
	for index := 1; index < len(args); index++ {
		var err error
		switch args[index] {
		case "-h", "--help", "help":
			if len(args) != 2 {
				return opts, fmt.Errorf("help must be the only operation argument")
			}
			opts.help = true
			return opts, nil
		case "--pollen":
			opts.pollen, err = nextWorkspaceArg(args, &index)
		case "--substrate":
			opts.substrate, err = nextWorkspaceArg(args, &index)
		case "--dir":
			opts.dir, err = nextWorkspaceArg(args, &index)
		case "--confirm":
			if opts.operation != "abandon" {
				return opts, fmt.Errorf("--confirm is only valid for workspace abandon")
			}
			opts.confirm = true
		default:
			return opts, fmt.Errorf("unknown argument %q for workspace %s", args[index], opts.operation)
		}
		if err != nil {
			return opts, err
		}
	}
	if strings.TrimSpace(opts.pollen) == "" || strings.TrimSpace(opts.substrate) == "" {
		return opts, fmt.Errorf("workspace %s requires exact --pollen and --substrate values", opts.operation)
	}
	if opts.pollen != strings.TrimSpace(opts.pollen) || opts.substrate != strings.TrimSpace(opts.substrate) {
		return opts, fmt.Errorf("Pollen and Substrate identifiers must be exact and have no surrounding whitespace")
	}
	if opts.operation == "abandon" && !opts.confirm {
		return opts, fmt.Errorf("workspace abandon requires --confirm")
	}
	return opts, nil
}

func nextWorkspaceArg(args []string, index *int) (string, error) {
	if *index+1 >= len(args) {
		return "", fmt.Errorf("flag %s requires a value", args[*index])
	}
	*index++
	return args[*index], nil
}

func executeWorkspaceCommand(ctx context.Context, opts workspaceCommandOptions) error {
	if rawPollen := os.Getenv(envPollenCLI); rawPollen != "" {
		return fmt.Errorf("workspace %s is Botanist-only and refuses declared Pollen %q before Substrate inspection", opts.operation, rawPollen)
	}
	cfg, err := conductor.LoadSubstratesConfig(opts.dir)
	if err != nil {
		return fmt.Errorf("load Substrate configuration: %w", err)
	}
	substrate, configured := conductor.ResolveSubstrate(opts.substrate, cfg)
	repository, err := conductor.ResolveSubstrateWorkspace(opts.substrate, substrate)
	if err != nil {
		return err
	}
	credential := conductor.ResolvedCredential{}
	if substrate != nil {
		if resolved, credentialErr := conductor.ResolveSubstrateCredential(*substrate, cfg); credentialErr == nil {
			credential = resolved
		}
	}
	configuredBranch := ""
	if configured && substrate != nil {
		configuredBranch = substrate.Branch
	}

	operations := core.DelegatedWorkspaceOperations{
		Inspect: func(callCtx context.Context, target core.DelegatedWorkspaceTargetSpec) (core.DelegatedWorkspaceReport, error) {
			report, err := conductor.InspectDelegatedWorkspace(callCtx, conductor.DelegatedWorkspaceTarget{
				Pollen: target.Pollen, Substrate: target.Substrate, Repository: repository,
				ConfiguredBranch: configuredBranch, Credential: credential,
			})
			return toCoreDelegatedWorkspaceReport(report), err
		},
		Abandon: func(callCtx context.Context, target core.DelegatedWorkspaceTargetSpec, confirm bool) (core.DelegatedWorkspaceAbandonResult, error) {
			result, err := conductor.AbandonDelegatedWorkspace(callCtx, conductor.DelegatedWorkspaceTarget{
				Pollen: target.Pollen, Substrate: target.Substrate, Repository: repository,
				ConfiguredBranch: configuredBranch, Credential: credential,
			}, confirm)
			return core.DelegatedWorkspaceAbandonResult{
				Report: toCoreDelegatedWorkspaceReport(result.Report), WorktreeRemoved: result.WorktreeRemoved,
				BranchDeleted: result.BranchDeleted, BranchPreserved: result.BranchPreserved,
				BranchReason: result.BranchReason,
			}, err
		},
	}
	service := core.NewService(nil).WithDelegatedWorkspace(operations)
	input := core.DelegatedWorkspaceInput{Pollen: opts.pollen, Substrate: opts.substrate}
	var output any
	if opts.operation == "inspect" {
		output, err = service.InspectDelegatedWorkspace(ctx, input)
	} else {
		output, err = service.AbandonDelegatedWorkspace(ctx, core.DelegatedWorkspaceAbandonInput{
			Pollen: opts.pollen, Substrate: opts.substrate, Confirm: opts.confirm,
		})
	}
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		return fmt.Errorf("encode workspace result: %w", err)
	}
	return nil
}

func toCoreDelegatedWorkspaceReport(report conductor.DelegatedWorkspaceReport) core.DelegatedWorkspaceReport {
	return core.DelegatedWorkspaceReport{
		Pollen: report.Pollen, Substrate: report.Substrate, Repository: report.Repository,
		Path: report.Path, CurrentBranch: report.CurrentBranch, Head: report.Head,
		Clean: report.Clean, CleanKnown: report.CleanKnown, FruitState: report.FruitState,
		PullRequest: report.PullRequest, FruitEvidence: report.FruitEvidence,
		WorkspaceVerified: report.WorkspaceVerified, BranchOwned: report.BranchOwned,
		UniqueWork: report.UniqueWork, UniqueWorkKnown: report.UniqueWorkKnown,
		AutoReclaimable: report.AutoReclaimable, Reason: report.Reason,
	}
}

func printWorkspaceUsage() {
	fmt.Println("Botanist-only delegated workspace lifecycle commands:")
	fmt.Println("  tendril workspace inspect --pollen <Pollen> --substrate <Substrate> [--dir <config-root>]")
	fmt.Println("  tendril workspace abandon --pollen <Pollen> --substrate <Substrate> --confirm [--dir <config-root>]")
}
