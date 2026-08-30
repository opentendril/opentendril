package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
)

// gitBootstrapOptions contains only the Botanist-facing adapter inputs. The
// conductor owns posture validation, remote inspection, branch policy, and
// publication safety.
type gitBootstrapOptions struct {
	substrate string
	branch    string
	dir       string
	confirm   bool
	help      bool
}

func runGitBootstrap(ctx context.Context, args []string) {
	opts, err := parseGitBootstrapArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		printGitBootstrapUsage()
		os.Exit(1)
	}
	if opts.help {
		printGitBootstrapUsage()
		return
	}
	if err := executeGitBootstrap(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

func parseGitBootstrapArgs(args []string) (gitBootstrapOptions, error) {
	opts := gitBootstrapOptions{dir: "."}
	need := func(i *int) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires a value", args[*i])
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch args[i] {
		case "-h", "--help", "help":
			opts.help = true
			return opts, nil
		case "--substrate":
			opts.substrate, err = need(&i)
		case "--branch":
			opts.branch, err = need(&i)
		case "--dir":
			opts.dir, err = need(&i)
		case "--confirm":
			opts.confirm = true
		default:
			return opts, fmt.Errorf("unknown argument %q for git bootstrap", args[i])
		}
		if err != nil {
			return opts, err
		}
	}
	if strings.TrimSpace(opts.substrate) == "" {
		return opts, fmt.Errorf("--substrate <name> is required")
	}
	return opts, nil
}

// executeGitBootstrap is separated from the process-exiting adapter so tests
// can prove the Pollen boundary and confirmation behavior without a child CLI.
func executeGitBootstrap(ctx context.Context, opts gitBootstrapOptions) error {
	if pollen := strings.TrimSpace(os.Getenv(envPollenCLI)); pollen != "" {
		return fmt.Errorf("git bootstrap is Botanist-only and refuses declared Pollen %q before repository inspection", pollen)
	}

	cfg, err := conductor.LoadSubstratesConfig(opts.dir)
	if err != nil {
		return fmt.Errorf("load substrates config: %w", err)
	}
	spec, isName := conductor.ResolveSubstrate(opts.substrate, cfg)
	if !isName || spec == nil {
		return fmt.Errorf("named Substrate %q was not found (run tendril git setup first, or pass --dir)", opts.substrate)
	}
	cred, err := conductor.ResolveSubstrateCredential(*spec, cfg)
	if err != nil {
		return fmt.Errorf("resolve credential: %w", err)
	}

	plan, err := conductor.PrepareGitBootstrap(ctx, *spec, cred, opts.branch)
	if err != nil {
		return err
	}
	printGitBootstrapPlan(plan)
	if !confirmGitBootstrapTarget(opts) {
		return fmt.Errorf("Botanist declined bootstrap; no repository mutation was attempted")
	}

	result, err := conductor.RunGitBootstrap(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("✅ Bootstrapped repository setup: %s\n", result.Repository)
	fmt.Printf("   branch:      %s\n", result.Branch)
	fmt.Printf("   root commit: %s\n", result.CommitOID)
	fmt.Println("   result:       setup state only (not Fruit)")
	return nil
}

func printGitBootstrapPlan(plan conductor.GitBootstrapPlan) {
	fmt.Println("About to bootstrap:")
	fmt.Printf("  repository:   %s/%s\n", plan.Owner, plan.Repo)
	fmt.Printf("  branch:       %s\n", plan.Branch)
	fmt.Println("  remote state:  repository contains no commits")
	fmt.Println("  mutation:      create exactly one root commit with an empty Git tree")
	fmt.Printf("  message:       %s\n", conductor.GitBootstrapCommitMessage)
	fmt.Printf("  attribution:   %s <%s>\n", conductor.GitBootstrapAuthorName, conductor.GitBootstrapAuthorEmail)
	fmt.Println("  result:        setup state only; not Fruit")
}

func confirmGitBootstrapTarget(opts gitBootstrapOptions) bool {
	if opts.confirm {
		return true
	}
	if !isTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "\n❌ Explicit Botanist confirmation is required.")
		fmt.Fprintln(os.Stderr, "   Re-run with --confirm, or run this command from a terminal to answer the prompt.")
		return false
	}

	fmt.Print("\nCreate this empty root commit? (y/n): ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func printGitBootstrapUsage() {
	fmt.Println("Usage: tendril git bootstrap --substrate <name> [--branch <branch>] [--confirm]")
	fmt.Println()
	fmt.Println("Botanist-only: bootstrap an empty managed GitHub App/API Substrate with one")
	fmt.Println("empty-tree root commit. Existing refs are never overwritten; the result is not Fruit.")
	fmt.Println()
	fmt.Println("  --substrate <name>  Named configured Substrate (required)")
	fmt.Println("  --branch <branch>   Botanist branch input, used only when config and GitHub provide none")
	fmt.Println("  --confirm           Explicitly confirm the displayed one-commit mutation")
	fmt.Println("  --dir <path>        Config directory (default: current directory)")
}
