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
// use. `tendril git branch` gets the workspace onto a feature branch,
// `tendril git commit` commits under the substrate's configured identity,
// `tendril git push` publishes from the Stem, and `tendril git pr` opens the
// pull request — the full delegated ladder, deliberately narrow beyond it (no
// delete, no rename, no merge, no arbitrary checkout).
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
	case core.GitStatusResult:
		printGitStatus(typed)
	case core.GitBranchListResult:
		printGitBranchList(typed)
	case core.GitPruneResult:
		printGitPrune(typed)
	case core.GitBranchResult:
		if typed.Status == "created" {
			fmt.Fprintf(os.Stderr, "🌱 Created branch %s (from %s)\n", typed.Branch, typed.PreviousBranch)
		} else {
			fmt.Fprintf(os.Stderr, "🌱 Switched to branch %s\n", typed.Branch)
		}
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
			workspace, substrateSpec, err := resolveGitWorkspace(ctx, spec.Substrate, substratesConfig)
			if err != nil {
				return core.GitCommitResult{}, err
			}
			defer conductor.LockWorkspace(workspace.Path)()

			// The credential carries the commit identity and signing config; a
			// bare path input resolves to an empty credential, which the
			// conductor's deny-closed identity requirement then refuses.
			credential, configuredBranch, allowDefaultBranchCommit, err := gitSubstrateSettings(substrateSpec, substratesConfig)
			if err != nil {
				return core.GitCommitResult{}, err
			}

			result, err := conductor.RunGitCommit(ctx, conductor.GitCommitExecution{
				Workspace:                workspace.Path,
				Message:                  spec.Message,
				Paths:                    spec.Paths,
				Credential:               credential,
				ConfiguredBranch:         configuredBranch,
				AllowDefaultBranchCommit: allowDefaultBranchCommit,
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
			workspace, substrateSpec, err := resolveGitWorkspace(ctx, spec.Substrate, substratesConfig)
			if err != nil {
				return core.GitPushResult{}, err
			}
			defer conductor.LockWorkspace(workspace.Path)()

			credential, _, _, err := gitSubstrateSettings(substrateSpec, substratesConfig)
			if err != nil {
				return core.GitPushResult{}, err
			}

			result, err := conductor.RunGitPush(ctx, conductor.GitPushExecution{
				Workspace:  workspace.Path,
				Branch:     spec.Branch,
				Credential: credential,
			})
			if err != nil {
				return core.GitPushResult{}, err
			}

			return core.GitPushResult{Status: result.Status, Branch: result.Branch}, nil
		},
		PullRequest: func(ctx context.Context, spec core.GitPRSpec) (core.GitPRResult, error) {
			workspace, substrateSpec, err := resolveGitWorkspace(ctx, spec.Substrate, substratesConfig)
			if err != nil {
				return core.GitPRResult{}, err
			}
			defer conductor.LockWorkspace(workspace.Path)()

			credential, _, _, err := gitSubstrateSettings(substrateSpec, substratesConfig)
			if err != nil {
				return core.GitPRResult{}, err
			}

			result, err := conductor.RunGitPullRequest(ctx, conductor.GitPRExecution{
				Workspace:  workspace.Path,
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
		Status: func(ctx context.Context, spec core.GitStatusSpec) (core.GitStatusResult, error) {
			workspace, substrateSpec, err := resolveGitWorkspace(ctx, spec.Substrate, substratesConfig)
			if err != nil {
				return core.GitStatusResult{}, err
			}
			defer conductor.LockWorkspace(workspace.Path)()

			_, configuredBranch, allowDefaultBranchCommit, err := gitSubstrateSettings(substrateSpec, substratesConfig)
			if err != nil {
				return core.GitStatusResult{}, err
			}

			result, err := conductor.RunGitStatus(ctx, conductor.GitStatusExecution{
				Workspace:                workspace.Path,
				ConfiguredBranch:         configuredBranch,
				AllowDefaultBranchCommit: allowDefaultBranchCommit,
			})
			if err != nil {
				return core.GitStatusResult{}, err
			}

			changes := make([]core.GitStatusChange, 0, len(result.Changes))
			for _, change := range result.Changes {
				changes = append(changes, core.GitStatusChange{Path: change.Path, Kind: change.Kind})
			}
			return core.GitStatusResult{
				Branch:              result.Branch,
				DetachedHead:        result.DetachedHead,
				HasCommits:          result.HasCommits,
				Head:                result.Head,
				DefaultBranch:       result.DefaultBranch,
				DefaultBranchSource: result.DefaultBranchSource,
				Repository:          result.Repository,
				Upstream:            result.Upstream,
				Ahead:               result.Ahead,
				Behind:              result.Behind,
				Clean:               result.Clean,
				ChangeCount:         result.ChangeCount,
				Modified:            result.Modified,
				Added:               result.Added,
				Deleted:             result.Deleted,
				Renamed:             result.Renamed,
				Untracked:           result.Untracked,
				Changes:             changes,
				Truncated:           result.Truncated,
				OnDefaultBranch:     result.OnDefaultBranch,
				CommitAllowed:       result.CommitAllowed,
				BlockedReason:       result.BlockedReason,
				Workspace:           workspace.Path,
				Isolated:            workspace.Isolated,
				Subject:             workspace.Subject,
			}, nil
		},
		BranchList: func(ctx context.Context, spec core.GitBranchListSpec) (core.GitBranchListResult, error) {
			workspace, substrateSpec, err := resolveGitWorkspace(ctx, spec.Substrate, substratesConfig)
			if err != nil {
				return core.GitBranchListResult{}, err
			}
			defer conductor.LockWorkspace(workspace.Path)()

			credential, configuredBranch, _, err := gitSubstrateSettings(substrateSpec, substratesConfig)
			if err != nil {
				return core.GitBranchListResult{}, err
			}

			result, err := conductor.RunGitBranchList(ctx, conductor.GitBranchListExecution{
				Workspace:        workspace.Path,
				ConfiguredBranch: configuredBranch,
				Credential:       credential,
			})
			if err != nil {
				return core.GitBranchListResult{}, err
			}
			return core.GitBranchListResult{
				Branches:      toCoreBranchInfos(result.Branches),
				Verified:      result.Verified,
				DefaultBranch: result.DefaultBranch,
			}, nil
		},
		Prune: func(ctx context.Context, spec core.GitPruneSpec) (core.GitPruneResult, error) {
			workspace, substrateSpec, err := resolveGitWorkspace(ctx, spec.Substrate, substratesConfig)
			if err != nil {
				return core.GitPruneResult{}, err
			}
			defer conductor.LockWorkspace(workspace.Path)()

			credential, configuredBranch, _, err := gitSubstrateSettings(substrateSpec, substratesConfig)
			if err != nil {
				return core.GitPruneResult{}, err
			}

			result, err := conductor.RunGitPrune(ctx, conductor.GitPruneExecution{
				Workspace:        workspace.Path,
				ConfiguredBranch: configuredBranch,
				Credential:       credential,
				Confirm:          spec.Confirm,
			})
			if err != nil {
				return core.GitPruneResult{}, err
			}
			deleted := make([]core.GitPrunedBranch, 0, len(result.Deleted))
			for _, branch := range result.Deleted {
				deleted = append(deleted, core.GitPrunedBranch{Name: branch.Name, Head: branch.Head, PullRequest: branch.PullRequest})
			}
			return core.GitPruneResult{
				Confirmed: result.Confirmed,
				Deleted:   deleted,
				Kept:      toCoreBranchInfos(result.Kept),
				Verified:  result.Verified,
			}, nil
		},
		Branch: func(ctx context.Context, spec core.GitBranchSpec) (core.GitBranchResult, error) {
			workspace, substrateSpec, err := resolveGitWorkspace(ctx, spec.Substrate, substratesConfig)
			if err != nil {
				return core.GitBranchResult{}, err
			}
			defer conductor.LockWorkspace(workspace.Path)()

			credential, configuredBranch, _, err := gitSubstrateSettings(substrateSpec, substratesConfig)
			if err != nil {
				return core.GitBranchResult{}, err
			}

			result, err := conductor.RunGitBranch(ctx, conductor.GitBranchExecution{
				Workspace:        workspace.Path,
				Branch:           spec.Branch,
				ConfiguredBranch: configuredBranch,
				Credential:       credential,
			})
			if err != nil {
				return core.GitBranchResult{}, err
			}

			return core.GitBranchResult{
				Status:         result.Status,
				Branch:         result.Branch,
				PreviousBranch: result.PreviousBranch,
			}, nil
		},
	}
}

// resolveGitWorkspace turns a substrate reference into the directory an
// operation actually runs in, and is the single place the delegated ladder
// decides that. A delegated invocation (one carrying an authorized subject in
// its context) runs in that subject's own worktree; a direct command line run
// carries no subject and uses the substrate's own checkout, so an operator at a
// terminal still sees their working copy.
//
// Every git operation goes through here. Resolving a substrate's raw path for a
// delegated call is exactly the bug this exists to prevent — two agents sharing
// one tree, staging each other's files — so the isolation cannot be bypassed by
// one operation quietly doing its own resolution. TestDelegatedOperationsAreIsolated
// pins that.
func resolveGitWorkspace(ctx context.Context, substrate string, substratesConfig *conductor.SubstratesConfig) (conductor.DelegatedWorkspace, *conductor.SubstrateSpec, error) {
	workspace := substrate
	substrateSpec, isName := conductor.ResolveSubstrate(substrate, substratesConfig)
	if isName && substrateSpec != nil {
		if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
			workspace = trimmedPath
		}
	}
	info, statErr := os.Stat(workspace)
	if statErr != nil || !info.IsDir() {
		return conductor.DelegatedWorkspace{}, nil, fmt.Errorf("substrate %q does not resolve to a local workspace directory (the delegated git ladder runs against a local checkout)", substrate)
	}

	resolved, err := conductor.ResolveDelegatedWorkspace(ctx, substrate, workspace, core.DelegationSubjectFromContext(ctx))
	if err != nil {
		return conductor.DelegatedWorkspace{}, nil, err
	}
	return resolved, substrateSpec, nil
}

// gitSubstrateSettings reads the substrate settings every git operation needs:
// the credential, the configured branch, and whether default-branch protection
// has been knowingly loosened.
func gitSubstrateSettings(spec *conductor.SubstrateSpec, substratesConfig *conductor.SubstratesConfig) (conductor.ResolvedCredential, string, bool, error) {
	if spec == nil {
		return conductor.ResolvedCredential{}, "", false, nil
	}
	credential, err := conductor.ResolveSubstrateCredential(*spec, substratesConfig)
	if err != nil {
		return conductor.ResolvedCredential{}, "", false, err
	}
	allowDefaultBranchCommit := spec.ProtectDefaultBranch != nil && !*spec.ProtectDefaultBranch
	return credential, spec.Branch, allowDefaultBranchCommit, nil
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
	{"branch", core.CapGitBranch},
	{"status", core.CapGitStatus},
	{"branches", core.CapGitBranchList},
	{"prune", core.CapGitPrune},
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
		case "--confirm":
			input["confirm"] = true
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
	if capName == core.CapGitBranch {
		if branch, _ := input["branch"].(string); strings.TrimSpace(branch) == "" {
			return nil, fmt.Errorf("missing branch. Usage: tendril git branch --substrate <path|name> --branch <feature-branch>")
		}
	}
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
	case core.CapGitBranch:
		return " --branch <feature-branch>"
	}
	return ""
}

func printGitUsage() {
	fmt.Println("Usage: tendril git <setup|status|branches|branch|commit|push|pr|prune> --substrate <path|name> [flags]")
	fmt.Println()
	fmt.Println("setup --substrate <name> --repo <owner/repo> [--posture app|pat] ...")
	fmt.Println("  Writes a git connection (substrates.yaml) + optional grant and prints the")
	fmt.Println("  agent MCP config. Run `tendril git setup --help` for the full flag list.")
	fmt.Println()
	fmt.Println("status --substrate <path|name>")
	fmt.Println("  Reports the workspace's branch, the resolved default branch, uncommitted")
	fmt.Println("  changes, ahead/behind, and whether a commit would be allowed right now.")
	fmt.Println("  Read-only and offline. Call it before committing to predict a refusal.")
	fmt.Println()
	fmt.Println("branches --substrate <path|name>")
	fmt.Println("  Classifies local branches against GitHub: merged, pull request open or")
	fmt.Println("  closed-without-merging, never pushed, or held by another agent. Read-only.")
	fmt.Println()
	fmt.Println("prune --substrate <path|name> [--confirm]")
	fmt.Println("  Deletes local branches whose pull request MERGED, and nothing else. Without")
	fmt.Println("  --confirm it only reports what it would delete. A squash-merged branch looks")
	fmt.Println("  unmerged to git, so merge state comes from GitHub — never from a branch name.")
	fmt.Println()
	fmt.Println("branch --substrate <path|name> --branch <feature-branch>")
	fmt.Println("  Creates the branch and switches to it — the governed way off the default")
	fmt.Println("  branch before committing. An existing branch is switched to, never reset;")
	fmt.Println("  a branch named as the repository's default branch is refused.")
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
	fmt.Println("Every subcommand is a projection of the shared Core capability registry.")
}

// printGitStatus renders a status result for a human at a terminal. The
// predictive line comes first when a commit is blocked: that is the fact the
// reader most needs, and burying it under counts would defeat the purpose of
// having a read-side at all.
func printGitStatus(status core.GitStatusResult) {
	if !status.CommitAllowed {
		fmt.Fprintf(os.Stderr, "⛔ Commit blocked: %s\n", status.BlockedReason)
	}

	branch := status.Branch
	switch {
	case status.DetachedHead:
		branch = "(detached head)"
	case !status.HasCommits:
		branch = "(no commits yet)"
	}
	defaultBranch := status.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "undetermined"
	}
	fmt.Fprintf(os.Stderr, "🌱 %s", branch)
	if status.Repository != "" {
		fmt.Fprintf(os.Stderr, " · %s", status.Repository)
	}
	fmt.Fprintf(os.Stderr, " · default: %s (%s)\n", defaultBranch, status.DefaultBranchSource)
	if status.Isolated {
		fmt.Fprintf(os.Stderr, "   workspace: isolated for subject %q at %s\n", status.Subject, status.Workspace)
	}

	if status.Upstream == "" {
		fmt.Fprintln(os.Stderr, "   upstream: none (branch not pushed yet)")
	} else {
		fmt.Fprintf(os.Stderr, "   upstream: %s · ahead %d, behind %d\n", status.Upstream, status.Ahead, status.Behind)
	}

	if status.Clean {
		fmt.Fprintln(os.Stderr, "   workspace: clean")
		return
	}
	fmt.Fprintf(os.Stderr, "   workspace: %d change(s) — %d modified, %d added, %d deleted, %d renamed, %d untracked\n",
		status.ChangeCount, status.Modified, status.Added, status.Deleted, status.Renamed, status.Untracked)
	for _, change := range status.Changes {
		fmt.Fprintf(os.Stderr, "     %-9s %s\n", change.Kind, change.Path)
	}
	if status.Truncated {
		fmt.Fprintf(os.Stderr, "     … %d more not shown\n", status.ChangeCount-len(status.Changes))
	}
}

// toCoreBranchInfos translates the conductor's branch classification into the
// Core's transport-free shape.
func toCoreBranchInfos(branches []conductor.GitBranchInfo) []core.GitBranchInfo {
	out := make([]core.GitBranchInfo, 0, len(branches))
	for _, branch := range branches {
		out = append(out, core.GitBranchInfo{
			Name:           branch.Name,
			Head:           branch.Head,
			Upstream:       branch.Upstream,
			Classification: branch.Classification,
			PullRequest:    branch.PullRequest,
			Deletable:      branch.Deletable,
			Reason:         branch.Reason,
		})
	}
	return out
}

// printGitBranchList renders the classification, deletable branches first —
// that is what the reader is deciding about.
func printGitBranchList(result core.GitBranchListResult) {
	if !result.Verified {
		fmt.Fprintln(os.Stderr, "⚠️  Merge state could not be established (no GitHub API credential on this connection).")
		fmt.Fprintln(os.Stderr, "   Nothing is deletable without evidence — a squash-merged branch looks unmerged to git.")
	}
	deletable := 0
	for _, branch := range result.Branches {
		if branch.Deletable {
			deletable++
		}
	}
	fmt.Fprintf(os.Stderr, "🌱 %d branch(es), %d safe to prune\n", len(result.Branches), deletable)
	for _, branch := range result.Branches {
		marker := " "
		if branch.Deletable {
			marker = "✓"
		}
		fmt.Fprintf(os.Stderr, " %s %-40s %-22s %s\n", marker, branch.Name, branch.Classification, branch.Reason)
	}
}

// printGitPrune leads with whether anything was actually deleted, because that
// is the difference between a report and a destructive act.
func printGitPrune(result core.GitPruneResult) {
	if !result.Confirmed {
		fmt.Fprintf(os.Stderr, "🔍 Report only — nothing was deleted. %d branch(es) would be removed; re-run with --confirm.\n", len(result.Deleted))
	} else {
		fmt.Fprintf(os.Stderr, "🌱 Deleted %d branch(es).\n", len(result.Deleted))
	}
	for _, branch := range result.Deleted {
		fmt.Fprintf(os.Stderr, "   %-40s %s", branch.Name, branch.Head)
		if branch.PullRequest > 0 {
			fmt.Fprintf(os.Stderr, "  (pull request %d)", branch.PullRequest)
		}
		fmt.Fprintln(os.Stderr)
	}
	if result.Confirmed && len(result.Deleted) > 0 {
		fmt.Fprintln(os.Stderr, "   Restore any of them with: git branch <name> <head>")
	}
	if len(result.Kept) > 0 {
		fmt.Fprintf(os.Stderr, "   Kept %d branch(es):\n", len(result.Kept))
		for _, branch := range result.Kept {
			fmt.Fprintf(os.Stderr, "     %-40s %s\n", branch.Name, branch.Reason)
		}
	}
}
