package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// runGitCmd is the CLI adapter for the governed git capability family: a thin
// projection of the same transport-free core.Core the REST and MCP surfaces
// use. `tendril git commit` commits a substrate's workspace under its
// configured commit identity, `tendril git push` publishes the branch from the
// Stem, and `tendril git pr` opens the pull request — the full delegated
// ladder, deliberately narrow beyond it (no branch, no checkout, no merge).
//
// A CLI invocation is never delegated (there is no delegation subject); the
// deny-closed attribution rule applies either way — a substrate without a
// configured commit identity is refused before any git command runs.
func runGitCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printGitUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printGitUsage()
		return
	case "setup":
		runGitSetup(ctx, args[1:])
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	command, ok := lookupGitCommand(sub)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown git command: %s\n", args[0])
		printGitUsage()
		os.Exit(1)
	}

	input, err := parseGitArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if origin, _ := input["origin"].(string); strings.TrimSpace(origin) == "" {
		input["origin"] = session.OriginCLI
	}

	svc, err := buildGitCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Git %s failed: %v\n", strings.TrimPrefix(command.capability, "git."), err)
		os.Exit(1)
	}

	switch typed := result.(type) {
	case core.GitCommitResult:
		if typed.Status == "committed" {
			fmt.Fprintf(os.Stderr, "🌱 Committed %s\n", typed.CommitHash)
		} else {
			fmt.Fprintln(os.Stderr, "🌱 Nothing to commit")
		}
	case core.GitPushResult:
		fmt.Fprintf(os.Stderr, "🌱 Pushed %s\n", typed.Branch)
	case core.GitPRResult:
		verb := "Opened"
		if typed.Status == "exists" {
			verb = "Already open"
		}
		fmt.Fprintf(os.Stderr, "🌱 %s pull request #%d (%s → %s) %s\n", verb, typed.Number, typed.Head, typed.Base, typed.URL)
	}
}

// buildGitCore constructs a Core with the delegated git execution port wired,
// matching the daemon surfaces.
func buildGitCore(ctx context.Context) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	return core.NewService(manager).WithGit(gitOperations()), nil
}

// gitOperations binds the delegated git execution port to the conductor's
// commit runner — this wiring lives in the adapter layer precisely so the
// Core never imports the conductor (see internal/core/boundary_test.go). It
// owns named-substrate resolution, credential resolution (so the configured
// commit identity flows to the conductor), and the translation between the
// Core's transport-free spec and the conductor's execution request.
func gitOperations() core.GitOperations {
	substratesConfig, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to load substrates config: %v\n", err)
	}

	return core.GitOperations{
		Commit: func(ctx context.Context, spec core.GitCommitSpec) (core.GitCommitResult, error) {
			workspace := spec.Substrate
			substrateSpec, isName := conductor.ResolveSubstrate(spec.Substrate, substratesConfig)
			if isName && substrateSpec != nil {
				if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
					workspace = trimmedPath
				}
			}
			info, statErr := os.Stat(workspace)
			if statErr != nil || !info.IsDir() {
				return core.GitCommitResult{}, fmt.Errorf("substrate %q does not resolve to a local workspace directory (a delegated git commit runs against a local checkout)", spec.Substrate)
			}

			// Resolve the substrate's credential so the configured commit
			// identity (and signing configuration) reaches the conductor. A
			// bare path input resolves to an empty credential, which the
			// conductor's deny-closed identity requirement then refuses.
			credential := conductor.ResolvedCredential{}
			if substrateSpec != nil {
				resolved, credentialErr := conductor.ResolveSubstrateCredential(*substrateSpec, substratesConfig)
				if credentialErr != nil {
					return core.GitCommitResult{}, credentialErr
				}
				credential = resolved
			}

			result, err := conductor.RunGitCommit(ctx, conductor.GitCommitExecution{
				Workspace:  workspace,
				Message:    spec.Message,
				Paths:      spec.Paths,
				Credential: credential,
			})
			if err != nil {
				return core.GitCommitResult{}, err
			}

			return core.GitCommitResult{
				Status:     result.Status,
				CommitHash: result.CommitHash,
			}, nil
		},
		Push: func(ctx context.Context, spec core.GitPushSpec) (core.GitPushResult, error) {
			workspace := spec.Substrate
			substrateSpec, isName := conductor.ResolveSubstrate(spec.Substrate, substratesConfig)
			if isName && substrateSpec != nil {
				if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
					workspace = trimmedPath
				}
			}
			info, statErr := os.Stat(workspace)
			if statErr != nil || !info.IsDir() {
				return core.GitPushResult{}, fmt.Errorf("substrate %q does not resolve to a local workspace directory (a delegated git push runs against a local checkout)", spec.Substrate)
			}

			// Resolve the substrate's credential so the configured
			// authentication material reaches the conductor's authenticated
			// push. A bare path input resolves to an empty credential; the push
			// then relies on whatever ambient auth the remote accepts (and fails
			// clearly if none does).
			credential := conductor.ResolvedCredential{}
			if substrateSpec != nil {
				resolved, credentialErr := conductor.ResolveSubstrateCredential(*substrateSpec, substratesConfig)
				if credentialErr != nil {
					return core.GitPushResult{}, credentialErr
				}
				credential = resolved
			}

			result, err := conductor.RunGitPush(ctx, conductor.GitPushExecution{
				Workspace:  workspace,
				Branch:     spec.Branch,
				Credential: credential,
			})
			if err != nil {
				return core.GitPushResult{}, err
			}

			return core.GitPushResult{Status: result.Status, Branch: result.Branch}, nil
		},
		PullRequest: func(ctx context.Context, spec core.GitPRSpec) (core.GitPRResult, error) {
			workspace := spec.Substrate
			substrateSpec, isName := conductor.ResolveSubstrate(spec.Substrate, substratesConfig)
			if isName && substrateSpec != nil {
				if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
					workspace = trimmedPath
				}
			}
			info, statErr := os.Stat(workspace)
			if statErr != nil || !info.IsDir() {
				return core.GitPRResult{}, fmt.Errorf("substrate %q does not resolve to a local workspace directory (a delegated pull request reads the workspace's origin remote and current branch)", spec.Substrate)
			}

			// Resolve the substrate's credential so the connection's GitHub API
			// token reaches the conductor. A bare path input resolves to an
			// empty credential, which the conductor's deny-closed posture check
			// then refuses with an error naming the postures that work.
			credential := conductor.ResolvedCredential{}
			if substrateSpec != nil {
				resolved, credentialErr := conductor.ResolveSubstrateCredential(*substrateSpec, substratesConfig)
				if credentialErr != nil {
					return core.GitPRResult{}, credentialErr
				}
				credential = resolved
			}

			result, err := conductor.RunGitPullRequest(ctx, conductor.GitPRExecution{
				Workspace:  workspace,
				Title:      spec.Title,
				Body:       spec.Body,
				Head:       spec.Head,
				Base:       spec.Base,
				Draft:      spec.Draft,
				Credential: credential,
			})
			if err != nil {
				return core.GitPRResult{}, err
			}

			return core.GitPRResult{
				Status: result.Status,
				Number: result.Number,
				URL:    result.URL,
				Head:   result.Head,
				Base:   result.Base,
			}, nil
		},
	}
}

// gitCommand is one subcommand actually registered on the `tendril git`
// command tree.
type gitCommand struct {
	name       string // CLI token, e.g. "commit"
	capability string // the governed Core capability it invokes
}

// gitCommands is the CLI command tree for `tendril git`. Like
// passthroughCommands, this registration — NOT core.CapabilityNames() — is
// the source of truth the parity coverage test reads for the CLI arm.
var gitCommands = []gitCommand{
	{"commit", core.CapGitCommit},
	{"push", core.CapGitPush},
	{"pr", core.CapGitPR},
}

// lookupGitCommand resolves a CLI subcommand token to its registered entry.
func lookupGitCommand(sub string) (gitCommand, bool) {
	for _, command := range gitCommands {
		if command.name == sub {
			return command, true
		}
	}
	return gitCommand{}, false
}

// gitCLICapabilityNames returns the capability names the CLI has actually
// registered git subcommands for, sorted. Read by the parity coverage test.
func gitCLICapabilityNames() []string {
	names := make([]string, 0, len(gitCommands))
	for _, command := range gitCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parseGitArgs turns CLI args into a capability input map. `--path` may be
// repeated to limit staging; omitting it stages all changes. `--json '{...}'`
// is the generic escape hatch, mirroring parsePassthroughArgs.
func parseGitArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	var paths []any
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
		case "--message":
			err = stringFlag(&i, "message")
		case "--branch":
			err = stringFlag(&i, "branch")
		case "--title":
			err = stringFlag(&i, "title")
		case "--body":
			err = stringFlag(&i, "body")
		case "--head":
			err = stringFlag(&i, "head")
		case "--base":
			err = stringFlag(&i, "base")
		case "--draft":
			input["draft"] = true
		case "--path":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --path requires a value")
			}
			i++
			paths = append(paths, args[i])
		case "--origin":
			err = stringFlag(&i, "origin")
		default:
			return nil, fmt.Errorf("unknown argument %q for git %s", args[i], strings.TrimPrefix(capName, "git."))
		}
		if err != nil {
			return nil, err
		}
	}

	if len(paths) > 0 {
		input["paths"] = paths
	}
	if substrate, _ := input["substrate"].(string); strings.TrimSpace(substrate) == "" {
		return nil, fmt.Errorf("missing substrate. Usage: tendril git %s --substrate <path|name>%s", strings.TrimPrefix(capName, "git."), gitUsageSuffix(capName))
	}
	// A commit message is required only for commit; push takes no message.
	if capName == core.CapGitCommit {
		if message, _ := input["message"].(string); strings.TrimSpace(message) == "" {
			return nil, fmt.Errorf("missing message. Usage: tendril git commit --substrate <path|name> --message <message>")
		}
	}
	// A pull request needs a title; head and base are resolved (never assumed)
	// when omitted.
	if capName == core.CapGitPR {
		if title, _ := input["title"].(string); strings.TrimSpace(title) == "" {
			return nil, fmt.Errorf("missing title. Usage: tendril git pr --substrate <path|name> --title <title> [--body B] [--head H] [--base B] [--draft]")
		}
	}
	return input, nil
}

// gitUsageSuffix returns the capability-specific flag hint appended to a
// missing-substrate error so each git subcommand shows its own required flags.
func gitUsageSuffix(capName string) string {
	switch capName {
	case core.CapGitCommit:
		return " --message <message>"
	case core.CapGitPR:
		return " --title <title>"
	}
	return ""
}

func printGitUsage() {
	fmt.Println("Usage: tendril git <setup|commit|push|pr> --substrate <path|name> [flags]")
	fmt.Println()
	fmt.Println("setup --substrate <name> --repo <owner/repo> [--posture app|pat] ...")
	fmt.Println("  Writes a git connection (substrates.yaml) + optional grant and prints the")
	fmt.Println("  agent MCP config. Run `tendril git setup --help` for the full flag list.")
	fmt.Println()
	fmt.Println("commit --substrate <path|name> --message <message> [--path P ...]")
	fmt.Println("  Commits the current state of a substrate's workspace under the substrate's")
	fmt.Println("  configured commit identity. Deny-closed: a substrate without a configured")
	fmt.Println("  identity is refused — an unattributable delegated commit is never created.")
	fmt.Println()
	fmt.Println("push --substrate <path|name> [--branch B]")
	fmt.Println("  Pushes the substrate's branch (current branch if --branch is omitted) to its")
	fmt.Println("  remote using the substrate's configured credential. The push runs on the Stem,")
	fmt.Println("  never inside a sealed Sprout; the token travels only in the process environment.")
	fmt.Println()
	fmt.Println("pr --substrate <path|name> --title <title> [--body B] [--head H] [--base B] [--draft]")
	fmt.Println("  Opens a pull request for an already-pushed branch (it never pushes — push and")
	fmt.Println("  pr are separately grantable). The base branch is READ from the repository when")
	fmt.Println("  --base is omitted, never assumed to be \"main\"; the head branch defaults to the")
	fmt.Println("  workspace's current branch. Opening from the default branch is refused, and an")
	fmt.Println("  existing open pull request for the same head is returned instead of duplicated.")
	fmt.Println()
	fmt.Println("  --json '{...}'      Full JSON input (the generic escape hatch)")
	fmt.Println()
	fmt.Println("commit, push and pr are projections of the shared Core capability registry.")
}
