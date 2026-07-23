package conductor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// Growing a Seed: the bounded-task executor. A Seed is a bounded intent — a
// goal, a verify predicate, and iteration/time bounds — and growing it means
// converging on the goal until the predicate holds. It composes two sealed
// execution paths already in the conductor, changing neither:
//
//   - The builder is RunSprout with DisableMergeBack: an agentic Sprout builds
//     toward the goal and commits onto a dedicated seed branch, never touching
//     the host workspace (the work stays a branch for review — the Phloem).
//   - The verdict is RunStoma (the stoma.pass executor): the verify command is
//     run deterministically in a network-sealed Terrarium against the seed
//     branch. Its exit code — never the Sprout's self-report — is the
//     authoritative pass/fail. Trust the builder to try; verify the result.
//
// Each iteration re-bases on the seed branch (RunSprout's shadow worktree is
// created from SubstrateBranch), so a second attempt builds on the first and
// the deterministic verify failure is fed back into the next prompt. The loop
// ends satisfied (verify passed), exhausted (bounds spent), or withered (the
// Sprout itself failed and was Abscised).

// Seed growth terminal statuses. The string values match core.SeedStatus* so
// the adapter passes the verdict straight through without translation.
const (
	SeedStatusSatisfied = "satisfied"
	SeedStatusExhausted = "exhausted"
	SeedStatusWithered  = "withered"
)

// seedVerifyTimeout bounds a single deterministic verify run. The whole growth
// is bounded separately by SeedExecution.Timeout via the context.
const seedVerifyTimeout = 5 * time.Minute

// seedBuildFn and seedVerifyFn are the two sealed execution seams the loop
// drives, injectable so the loop's logic (statuses, iteration, feedback) can be
// tested without a real Terrarium or LLM. Production wires the real Sprout
// builder and the deterministic verify.
var (
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		return orch.RunSprout(ctx, prompt)
	}
	seedVerifyFn = runSeedVerify
)

// SeedExecution is a fully resolved seed-growth request handed to RunSeed.
type SeedExecution struct {
	// Substrate is the named substrate key or local path of the target
	// workspace; it is resolved the same way every execution path resolves it.
	Substrate string
	// Goal is the intent handed to the Sprout builder.
	Goal string
	// Verify is the argv command whose exit-0 defines success, run
	// deterministically in a sealed Terrarium against the seed branch.
	Verify []string
	// MaxIterations bounds how many build/verify passes the loop may take.
	MaxIterations int
	// Timeout bounds the whole growth's wall-clock.
	Timeout time.Duration
	// Egress is the delegation grant's host allow-list bounding the verify run's
	// Stem-mediated reach; empty means deny-all.
	Egress []string
	// Provider, Model, Genotype optionally steer the Sprout; empty falls back to
	// the substrate/environment defaults RunSprout already resolves.
	Provider string
	Model    string
	Genotype string
	// EventBus, when set, receives the Sprout lifecycle events of each pass.
	EventBus *eventbus.Bus
}

// SeedRunResult is the reviewable outcome of a grown Seed — the Fruit.
type SeedRunResult struct {
	Status     string
	Iterations int
	Branch     string
	Diff       string
	Logs       string
}

// RunSeed grows a Seed to Fruit: it drives the build/verify loop and returns the
// reviewable branch, diff, and logs. It never merges to the host — the work
// stays on the seed branch for a human (or a later tier) to adopt.
func RunSeed(ctx context.Context, execution SeedExecution) (SeedRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if execution.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, execution.Timeout)
		defer cancel()
	}

	sourcePath, err := resolveSeedWorkspace(execution.Substrate)
	if err != nil {
		return SeedRunResult{}, err
	}

	base, err := runGitCommand(ctx, sourcePath, "rev-parse", "HEAD")
	if err != nil {
		return SeedRunResult{}, fmt.Errorf("seed.grow needs a git substrate (branch + diff are the reviewable Fruit): %w", err)
	}
	base = strings.TrimSpace(base)

	seedBranch := "tendril/" + newSproutExecutionID("seed")

	maxIterations := execution.MaxIterations
	if maxIterations < 1 {
		maxIterations = 1
	}

	var logs strings.Builder
	status := SeedStatusExhausted
	iterations := 0
	prompt := seedGoalPrompt(execution.Goal, execution.Verify, "")

	for i := 0; i < maxIterations; i++ {
		if ctx.Err() != nil {
			fmt.Fprintf(&logs, "\n⏳ Timeout reached before iteration %d.\n", i+1)
			break
		}
		iterations = i + 1

		orch := NewDockerOrchestrator()
		orch.Substrate = execution.Substrate
		orch.SubstrateBranch = seedBranch
		orch.DisableMergeBack = true
		orch.Provider = execution.Provider
		orch.Model = execution.Model
		orch.Genotype = execution.Genotype
		orch.EventBus = execution.EventBus

		report, runErr := seedBuildFn(ctx, orch, prompt)
		fmt.Fprintf(&logs, "\n🌱 Iteration %d — sprout %s\n", iterations, strings.TrimSpace(report.Outcome))
		if runErr != nil {
			status = SeedStatusWithered
			fmt.Fprintf(&logs, "sprout withered: %v\n", runErr)
			break
		}

		verifyOut, passed, verifyErr := seedVerifyFn(ctx, sourcePath, seedBranch, execution.Verify, execution.Egress)
		if verifyErr != nil {
			status = SeedStatusWithered
			fmt.Fprintf(&logs, "🔬 verify could not run: %v\n", verifyErr)
			break
		}
		fmt.Fprintf(&logs, "🔬 verify %s\n%s\n", verifyVerdict(passed), verifyOut)
		if passed {
			status = SeedStatusSatisfied
			break
		}
		prompt = seedGoalPrompt(execution.Goal, execution.Verify, verifyOut)
	}

	branch := ""
	diff := ""
	if localBranchExists(sourcePath, seedBranch) {
		branch = seedBranch
		if raw, derr := runGitCommandRawOutput(ctx, sourcePath, "diff", "--no-color", base, seedBranch); derr == nil {
			diff = raw
		}
	}

	return SeedRunResult{
		Status:     status,
		Iterations: iterations,
		Branch:     branch,
		Diff:       diff,
		Logs:       strings.TrimSpace(logs.String()),
	}, nil
}

// runSeedVerify runs the verify command deterministically against a throwaway
// worktree of the seed branch and reports whether it passed. A non-nil error is
// an infrastructure failure (the verdict could not be produced), distinct from
// a clean non-zero exit (a normal failed verification the loop iterates on).
func runSeedVerify(ctx context.Context, sourcePath, seedBranch string, verify, egress []string) (string, bool, error) {
	worktree, err := createShadowWorktree(sourcePath, seedBranch)
	if err != nil {
		return "", false, fmt.Errorf("create verify worktree: %w", err)
	}
	defer removeShadowWorktree(sourcePath, worktree)

	result, err := RunStoma(ctx, StomaExecution{
		Workspace: worktree,
		Command:   verify,
		Egress:    egress,
		Timeout:   seedVerifyTimeout,
	})
	if err != nil {
		return "", false, err
	}
	output := strings.TrimSpace(strings.TrimSpace(result.Stdout) + "\n" + strings.TrimSpace(result.Stderr))
	return output, result.ExitCode == 0 && !result.TimedOut, nil
}

// resolveSeedWorkspace resolves a substrate name or path to a local workspace
// directory, exactly as the stoma adapter does.
func resolveSeedWorkspace(substrate string) (string, error) {
	substrate = strings.TrimSpace(substrate)
	if substrate == "" {
		return "", fmt.Errorf("substrate is required")
	}
	workspace := substrate
	if config, err := LoadSubstratesConfig(""); err == nil {
		if spec, isName := ResolveSubstrate(substrate, config); isName && spec != nil {
			if path := strings.TrimSpace(spec.Path); path != "" {
				workspace = path
			}
		}
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("substrate %q does not resolve to a local workspace directory (seed.grow builds against a local checkout)", substrate)
	}
	return workspace, nil
}

// seedGoalPrompt composes the Sprout's task prompt: the goal, the verify
// predicate it must satisfy, and — on a retry — the previous deterministic
// verify failure so the Sprout fixes the real cause rather than guessing.
func seedGoalPrompt(goal string, verify []string, priorFailure string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nThe task is complete only when `%s` exits 0. Run it to check your work before finishing.",
		strings.TrimSpace(goal), strings.Join(verify, " "))
	if fail := strings.TrimSpace(priorFailure); fail != "" {
		if len(fail) > 4000 {
			fail = fail[:4000] + "\n…(truncated)"
		}
		fmt.Fprintf(&b, "\n\nA previous attempt did not pass. The verification command failed with:\n%s\n\nFind and fix the cause, then make it pass.", fail)
	}
	return b.String()
}

func verifyVerdict(passed bool) string {
	if passed {
		return "PASSED"
	}
	return "FAILED"
}
