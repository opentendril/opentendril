package conductor

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
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
	// SessionID is the Seed's canonical Phytomer. Every builder Sprout for
	// this growth is attributed to it. Required: a sessionless Seed cannot
	// be observed.
	SessionID string
	// PrepareSprout runs after the iteration's orchestrator is configured and
	// before Terrarium work, so the adapter can persist opening ownership.
	PrepareSprout func(ctx context.Context, orch *DockerOrchestrator, iteration int) error
}

// SeedRunResult is the reviewable outcome of a grown Seed — the Fruit.
type SeedRunResult struct {
	Status     string
	Iterations int
	Branch     string
	Commit     string
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

	sessionID := strings.TrimSpace(execution.SessionID)
	if sessionID == "" {
		return SeedRunResult{}, fmt.Errorf("seed.grow requires a phytomer sessionId")
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
		orch.AwaitsRunEnding = true
		orch.Provider = execution.Provider
		orch.Model = execution.Model
		orch.Genotype = execution.Genotype
		orch.EventBus = execution.EventBus
		orch.SessionID = sessionID
		orch.SeedIntegrationCheckpoint = true

		currentStartRevision := base
		if tip, err := runGitCommand(ctx, sourcePath, "rev-parse", seedBranch); err == nil {
			if tip = strings.TrimSpace(tip); tip != "" {
				currentStartRevision = tip
			}
		}
		orch.SeedStartRevision = currentStartRevision

		if execution.PrepareSprout != nil {
			if prepErr := execution.PrepareSprout(ctx, orch, iterations); prepErr != nil {
				return SeedRunResult{}, prepErr
			}
		}

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

	branch, diff, commit := seedFruitIdentity(ctx, sourcePath, seedBranch, base)

	if commit != "" && commit != base {
		orchProto := NewDockerOrchestrator()
		orchProto.Substrate = execution.Substrate
		if config, err := LoadSubstratesConfig(""); err == nil {
			if plan, err := resolveSubstrateExecutionPlan(orchProto, config); err == nil {
				if plan.credential.CommitMode == CommitModeAPI {
					// In API mode, local checkpoints are not Fruit identity.
					commit = "" // Clear the local SHA from the final result.

					publishedOID, pubErr := publishSeedManagedAPIFruit(ctx, sourcePath, branch, base, execution.Goal, string(status), plan, execution.SessionID)
					if pubErr != nil {
						fmt.Fprintf(&logs, "\n⚠️ Failed to publish Seed Fruit via API: %v\n", pubErr)
						// local work is preserved on branch, but we return empty Commit since we failed to publish.
					} else {
						commit = publishedOID
					}
				}
			}
		}
	}

	return SeedRunResult{
		Status:     status,
		Iterations: iterations,
		Branch:     branch,
		Commit:     commit,
		Diff:       diff,
		Logs:       strings.TrimSpace(logs.String()),
	}, nil
}

func publishSeedManagedAPIFruit(ctx context.Context, sourcePath, branch, baseCommit, taskPrompt, status string, plan *substrateExecutionPlan, sessionID string) (string, error) {
	diffStatus, err := runGitCommandRawOutput(ctx, sourcePath, "diff", "--name-status", "-z", baseCommit, branch)
	if err != nil {
		return "", fmt.Errorf("api fruit publication: list modified files: %w", err)
	}
	if diffStatus == "" {
		return "", fmt.Errorf("api fruit publication: nothing to commit")
	}

	worktree, err := createShadowWorktree(sourcePath, branch)
	if err != nil {
		return "", fmt.Errorf("api fruit publication: create worktree for changes: %w", err)
	}
	defer removeShadowWorktree(sourcePath, worktree)

	var additions []apiCommitFileAddition
	var deletions []apiCommitFileDeletion

	entries := strings.Split(diffStatus, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) == 0 {
			continue
		}
		code := entry[0]
		i++
		if i >= len(entries) {
			break
		}
		path := filepath.ToSlash(entries[i])

		if code == 'R' || code == 'C' {
			oldPath := path
			i++
			if i >= len(entries) {
				break
			}
			path = filepath.ToSlash(entries[i])
			if oldPath != "" {
				deletions = append(deletions, apiCommitFileDeletion{Path: oldPath})
			}
			contents, readErr := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(path)))
			if readErr != nil {
				return "", fmt.Errorf("api fruit publication: read %s: %w", path, readErr)
			}
			additions = append(additions, apiCommitFileAddition{
				Path:     path,
				Contents: base64.StdEncoding.EncodeToString(contents),
			})
		} else if code == 'D' {
			deletions = append(deletions, apiCommitFileDeletion{Path: path})
		} else {
			contents, readErr := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(path)))
			if readErr != nil {
				return "", fmt.Errorf("api fruit publication: read %s: %w", path, readErr)
			}
			additions = append(additions, apiCommitFileAddition{
				Path:     path,
				Contents: base64.StdEncoding.EncodeToString(contents),
			})
		}
	}

	commitMessage := buildSproutCommitMessage("seed-"+sessionID, taskPrompt, status, "")
	return publishAPIFruit(ctx, sourcePath, branch, baseCommit, plan.credential.App, additions, deletions, commitMessage)
}

// seedFruitIdentity reports the reviewable Seed branch, its diff against the
// pre-run HEAD, and a Fruit commit SHA only when that SHA is independently
// identifiable as Seed work — never the default branch, pre-run HEAD, or a
// no-change branch.
func seedFruitIdentity(ctx context.Context, sourcePath, seedBranch, base string) (branch, diff, commit string) {
	if !localBranchExists(sourcePath, seedBranch) {
		return "", "", ""
	}
	branch = seedBranch
	if raw, derr := runGitCommandRawOutput(ctx, sourcePath, "diff", "--no-color", base, seedBranch); derr == nil {
		diff = raw
	}
	tip, err := runGitCommand(ctx, sourcePath, "rev-parse", seedBranch)
	if err != nil {
		return branch, diff, ""
	}
	tip = strings.TrimSpace(tip)
	if tip == "" || tip == strings.TrimSpace(base) {
		return branch, diff, ""
	}
	if strings.TrimSpace(diff) == "" {
		return branch, diff, ""
	}
	return branch, diff, tip
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
	var spec *SubstrateSpec
	if config, err := LoadSubstratesConfig(""); err == nil {
		if s, isName := ResolveSubstrate(substrate, config); isName && s != nil {
			spec = s
		}
	}
	return ResolveSubstrateWorkspace(substrate, spec)
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
