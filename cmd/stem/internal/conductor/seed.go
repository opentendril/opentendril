package conductor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// Growing a Seed: the bounded-task executor. A Seed is a bounded intent — a
// goal and iteration/time bounds, with an optional explicit verify predicate.
// It composes the sealed builder path with an optional sealed verifier path:
//
//   - The builder is RunSprout with DisableMergeBack: an agentic Sprout builds
//     toward the goal and commits onto a dedicated seed branch, never touching
//     the host workspace (the work stays a branch for review — the Phloem).
//   - When requested, RunStoma (the stoma.pass executor) runs the verifier
//     deterministically in a network-sealed Terrarium against an immutable
//     candidate. Exit 0 passes, exit 1 is a repairable predicate failure, other
//     completed non-zero exits are configuration-invalid, and inability or
//     timeout are non-repairable verification outcomes.
//
// Each iteration re-bases on the seed branch (RunSprout's shadow worktree is
// created from SubstrateBranch), so a second attempt builds on the first and
// a deterministic predicate failure is fed back into the next prompt. A pure,
// recoverable protocol failure may still leave an immutable checkpoint for the
// verifier and next iteration; other Sprout failures stop cognitive execution.

// Seed growth lifecycle statuses. The string values match core.SeedStatus* so
// the adapter passes them through without translation.
const (
	SeedStatusSettled   = core.SeedStatusSettled
	SeedStatusSatisfied = core.SeedStatusSatisfied // historical compatibility
	SeedStatusExhausted = core.SeedStatusExhausted // historical compatibility
	SeedStatusWithered  = core.SeedStatusWithered  // historical compatibility
)

// seedVerifyTimeout bounds a single deterministic verify run. The whole growth
// is bounded separately by SeedExecution.Timeout via the context.
const seedVerifyTimeout = 5 * time.Minute

// seedBuildFn and seedVerifyFn are the two sealed execution seams the loop
// drives, injectable so the loop's logic (statuses, iteration, feedback) can be
// tested without a real Terrarium or LLM. Production wires the real Sprout
// builder and the deterministic verify.
type seedVerifyReport struct {
	Output   string
	Passed   bool
	ExitCode *int
	TimedOut bool
	Err      error
}

var (
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		return orch.RunSprout(ctx, prompt)
	}
	seedVerifyFn        = runSeedVerify
	seedCandidateDiffFn = seedCandidateDiff
)

// SeedExecution is a fully resolved seed-growth request handed to RunSeed.
type SeedExecution struct {
	// Substrate is the named substrate key or local path of the target
	// workspace; it is resolved the same way every execution path resolves it.
	Substrate string
	// Goal is the intent handed to the Sprout builder.
	Goal string
	// Verify is the argv command whose exit-0 defines success, run
	// deterministically in a sealed Terrarium against the Seed candidate.
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
	// Continuation, when set, is the Core-owned continuation boundary for an
	// opened Seed. The conductor invokes these callbacks; it does not query
	// persistence or decide continuation policy.
	Continuation SeedContinuationBoundary
}

// SeedContinuationBoundary is the transport-free callback contract the
// conductor invokes at deterministic Seed cognitive and settlement edges.
// Zero value means continuation is not in play.
type SeedContinuationBoundary struct {
	DeliverPending         func(ctx context.Context, basePrompt string) (string, error)
	ConfirmDelivery        func(ctx context.Context) error
	AcquireSettlementFence func(ctx context.Context) (bool, error)
	HasUnresolved          func(ctx context.Context) (bool, error)
}

// SeedRunResult is the reviewable outcome of a grown Seed — the Fruit.
type SeedRunResult struct {
	Status                  string
	ExecutionOutcome        string
	VerificationOutcome     string
	Iterations              int
	Branch                  string
	Commit                  string
	Repository              string
	Workspace               string
	PublicationState        string
	CreatedAt               time.Time
	Diff                    string
	Logs                    string
	PublicationDiagnostic   *core.SeedPublicationDiagnostic
	VerificationDiagnostics []core.SeedVerificationDiagnostic
}

// SeedPublicationFailure is returned alongside the completed Seed result when
// execution reached managed Fruit publication but no authoritative Fruit was
// established. The diagnostic is safe to persist and contains no upstream
// response or credential material.
type SeedPublicationFailure struct {
	Diagnostic core.SeedPublicationDiagnostic
}

func (e *SeedPublicationFailure) Error() string {
	if e == nil {
		return "publish Seed Fruit via API: publication failure"
	}
	if e.Diagnostic.RequestID != "" {
		return fmt.Sprintf("publish Seed Fruit via API: publication failure during %s (%s; GitHub request %s): %s", e.Diagnostic.Phase, e.Diagnostic.Outcome, e.Diagnostic.RequestID, e.Diagnostic.Message)
	}
	return fmt.Sprintf("publish Seed Fruit via API: publication failure during %s (%s): %s", e.Diagnostic.Phase, e.Diagnostic.Outcome, e.Diagnostic.Message)
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
	status := SeedStatusSettled
	iterations := 0
	prompt := seedGoalPrompt(execution.Goal, execution.Verify, "")
	var verificationDiagnostics []core.SeedVerificationDiagnostic
	executionOutcome := ""
	verificationOutcome := ""
	if len(execution.Verify) == 0 {
		verificationOutcome = core.SeedVerificationOutcomeNotRequested
	}
	candidateRevision := base
	candidateEvidenceRevision := ""
	candidateEvidence := ""
	terminalError := error(nil)
	fenceAcquired := execution.Continuation.AcquireSettlementFence == nil

	for i := 0; i < maxIterations; i++ {
		if ctx.Err() != nil {
			fmt.Fprintf(&logs, "\n⏳ Timeout reached before iteration %d.\n", i+1)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				executionOutcome = core.SeedExecutionOutcomeTimedOut
			}
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

		currentStartRevision := candidateRevision
		orch.SeedStartRevision = currentStartRevision

		if execution.PrepareSprout != nil {
			if prepErr := execution.PrepareSprout(ctx, orch, iterations); prepErr != nil {
				return SeedRunResult{}, prepErr
			}
		}

		if execution.Continuation.DeliverPending != nil {
			composed, deliverErr := execution.Continuation.DeliverPending(ctx, prompt)
			if deliverErr != nil {
				return SeedRunResult{}, deliverErr
			}
			prompt = composed
		}

		buildReport, runErr := seedBuildFn(ctx, orch, prompt)
		// RequestsMade is the typed evidence that continued intent crossed the
		// Mycorrhizal/provider boundary. A pre-provider failure must not mark
		// delivering continuation delivered.
		if buildReport.RequestsMade && execution.Continuation.ConfirmDelivery != nil {
			if confirmErr := execution.Continuation.ConfirmDelivery(seedBoundaryContext(ctx)); confirmErr != nil {
				return SeedRunResult{}, confirmErr
			}
		}
		fmt.Fprintf(&logs, "\n🌱 Iteration %d — sprout %s\n", iterations, strings.TrimSpace(buildReport.Outcome))
		candidateCommit := strings.TrimSpace(buildReport.seedCandidateCommit)
		salvageableFailure := runErr != nil && isRecoverableSeedSproutFailure(runErr) && candidateCommit != ""
		if runErr != nil && !salvageableFailure {
			fmt.Fprintf(&logs, "sprout withered: %s\n", boundSeedEvidence(strings.TrimSpace(runErr.Error()), seedVerifyFeedbackBound))
			executionOutcome = seedBuildExecutionOutcome(buildReport, runErr)
			fenced, fenceErr := seedAcquireSettlementFence(ctx, execution)
			if fenceErr != nil {
				return SeedRunResult{}, fenceErr
			}
			fenceAcquired = fenced || execution.Continuation.AcquireSettlementFence == nil
			if !fenceAcquired {
				executionOutcome = core.SeedExecutionOutcomeBoundaryRefused
				terminalError = core.ErrContinuationUndeliverable
				fmt.Fprintln(&logs, terminalError.Error())
			}
			break
		}
		if runErr != nil {
			// The Sprout's terminal failure remains part of its observation and
			// history. Seed growth may continue only because the Stem received a
			// separately materialized immutable checkpoint to verify.
			fmt.Fprintf(&logs, "sprout withered after checkpointing a recoverable candidate: %s\n", boundSeedEvidence(strings.TrimSpace(runErr.Error()), seedVerifyFeedbackBound))
		}

		var candidateErr error
		candidateRevision, candidateErr = seedCandidateRevision(ctx, sourcePath, currentStartRevision, candidateCommit)
		if candidateErr != nil {
			executionOutcome = core.SeedExecutionOutcomeInfrastructureFailed
			if len(execution.Verify) > 0 {
				verifyReport := seedVerifyReport{Err: fmt.Errorf("resolve Seed verification candidate: %w", candidateErr)}
				verificationDiagnostics = append(verificationDiagnostics, seedVerificationDiagnostic(iterations, verifyReport))
				verificationOutcome = core.SeedVerificationOutcomeInfrastructureFailed
			}
			fmt.Fprintf(&logs, "candidate could not be materialized: %v\n", candidateErr)
			fenced, fenceErr := seedAcquireSettlementFence(ctx, execution)
			if fenceErr != nil {
				return SeedRunResult{}, fenceErr
			}
			fenceAcquired = fenced || execution.Continuation.AcquireSettlementFence == nil
			if !fenceAcquired {
				executionOutcome = core.SeedExecutionOutcomeBoundaryRefused
				terminalError = core.ErrContinuationUndeliverable
			}
			break
		}
		executionOutcome = core.SeedExecutionOutcomeCompleted

		if len(execution.Verify) == 0 {
			fmt.Fprintln(&logs, "🔬 verification not requested")
			fenced, fenceErr := seedAcquireSettlementFence(ctx, execution)
			if fenceErr != nil {
				return SeedRunResult{}, fenceErr
			}
			fenceAcquired = fenced || execution.Continuation.AcquireSettlementFence == nil
			if fenceAcquired {
				break
			}
			if iterations < maxIterations && ctx.Err() == nil {
				if candidateRevision != candidateEvidenceRevision {
					candidateEvidence = seedCandidateDiffFn(ctx, sourcePath, base, candidateRevision)
					candidateEvidenceRevision = candidateRevision
				}
				prompt = seedGoalPromptWithCandidateEvidence(execution.Goal, nil, "", candidateEvidence)
				continue
			}
			executionOutcome = core.SeedExecutionOutcomeBoundaryRefused
			terminalError = core.ErrContinuationUndeliverable
			fmt.Fprintln(&logs, terminalError.Error())
			break
		}

		verifyReport := seedVerifyFn(ctx, sourcePath, candidateRevision, execution.Verify, execution.Egress)
		diagnostic := seedVerificationDiagnostic(iterations, verifyReport)
		verificationDiagnostics = append(verificationDiagnostics, diagnostic)
		verificationOutcome = diagnostic.Outcome
		fmt.Fprintf(&logs, "🔬 verify %s\n%s\n", diagnostic.Outcome, verifyReport.Output)
		predicateFailed := diagnostic.Outcome == core.SeedVerificationOutcomePredicateFailed
		if predicateFailed && iterations < maxIterations {
			if candidateRevision != candidateEvidenceRevision {
				candidateEvidence = seedCandidateDiffFn(ctx, sourcePath, base, candidateRevision)
				candidateEvidenceRevision = candidateRevision
			}
			prompt = seedGoalPromptWithCandidateEvidence(execution.Goal, execution.Verify, seedVerificationFeedback(verifyReport), candidateEvidence)
			continue
		}
		if predicateFailed {
			executionOutcome = core.SeedExecutionOutcomeBoundsExhausted
		}
		fenced, fenceErr := seedAcquireSettlementFence(ctx, execution)
		if fenceErr != nil {
			return SeedRunResult{}, fenceErr
		}
		fenceAcquired = fenced || execution.Continuation.AcquireSettlementFence == nil
		if fenceAcquired {
			break
		}
		if diagnostic.Outcome == core.SeedVerificationOutcomePassed && iterations < maxIterations && ctx.Err() == nil {
			if candidateRevision != candidateEvidenceRevision {
				candidateEvidence = seedCandidateDiffFn(ctx, sourcePath, base, candidateRevision)
				candidateEvidenceRevision = candidateRevision
			}
			feedback := ""
			if predicateFailed {
				feedback = seedVerificationFeedback(verifyReport)
			}
			prompt = seedGoalPromptWithCandidateEvidence(execution.Goal, execution.Verify, feedback, candidateEvidence)
			continue
		}
		executionOutcome = core.SeedExecutionOutcomeBoundaryRefused
		terminalError = core.ErrContinuationUndeliverable
		fmt.Fprintln(&logs, terminalError.Error())
		break
	}
	if !fenceAcquired && terminalError == nil && execution.Continuation.AcquireSettlementFence != nil {
		fenced, fenceErr := seedAcquireSettlementFence(ctx, execution)
		if fenceErr != nil {
			return SeedRunResult{}, fenceErr
		}
		fenceAcquired = fenced
		if !fenceAcquired {
			executionOutcome = core.SeedExecutionOutcomeBoundaryRefused
			terminalError = core.ErrContinuationUndeliverable
		}
	}
	if executionOutcome == "" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		executionOutcome = core.SeedExecutionOutcomeTimedOut
	}

	branch, diff, commit := seedFruitIdentity(ctx, sourcePath, seedBranch, base)

	result := func(fruitBranch, fruitCommit string) SeedRunResult {
		return SeedRunResult{
			Status:                  status,
			ExecutionOutcome:        executionOutcome,
			VerificationOutcome:     verificationOutcome,
			Iterations:              iterations,
			Branch:                  fruitBranch,
			Commit:                  fruitCommit,
			Repository:              "",
			Workspace:               "",
			PublicationState:        "",
			Diff:                    diff,
			Logs:                    strings.TrimSpace(logs.String()),
			VerificationDiagnostics: core.CopySeedVerificationDiagnostics(verificationDiagnostics),
		}
	}

	if !fenceAcquired {
		out := result("", "")
		if terminalError != nil {
			return out, terminalError
		}
		if unresolvedErr := seedUnresolvedContinuationError(seedBoundaryContext(ctx), execution, status); unresolvedErr != nil {
			return out, unresolvedErr
		}
		return out, nil
	}
	if len(execution.Verify) > 0 && verificationOutcome != core.SeedVerificationOutcomePassed {
		return result("", ""), terminalError
	}

	if commit != "" && commit != base {
		fruitCreatedAt := time.Now().UTC()
		orchProto := NewDockerOrchestrator()
		orchProto.Substrate = execution.Substrate

		config, configErr := LoadSubstratesConfig("")
		if configErr != nil {
			return seedPublicationFailure(result("", ""), "preparation", "publication-plan-unavailable", false, "", "resolve Seed Fruit publication configuration: configured publication settings could not be resolved")
		}

		plan, planErr := resolveSubstrateExecutionPlan(orchProto, config)
		if planErr != nil {
			message := "resolve Seed Fruit publication plan: configured publication plan could not be resolved"
			if strings.Contains(planErr.Error(), "unknown auth method") {
				message += " (unknown auth method)"
			}
			return seedPublicationFailure(result("", ""), "preparation", "publication-plan-unavailable", false, "", message)
		}

		if plan.credential.CommitMode == CommitModeAPI {
			// The local Seed branch and checkpoint are retained integration state,
			// not Botanist-reviewable Fruit until GitHub publishes them.
			localSeedBranch := branch
			branch = ""
			commit = ""

			publishedOID, pubErr := publishSeedManagedAPIFruit(
				ctx,
				sourcePath,
				localSeedBranch,
				base,
				execution.Goal,
				string(status),
				plan,
				execution.SessionID,
			)
			if pubErr != nil {
				fmt.Fprintf(&logs, "\n⚠️ Failed to publish Seed Fruit via API: %v\n", pubErr)
				return seedPublicationFailureFromError(result("", ""), pubErr)
			}

			branch = localSeedBranch
			commit = publishedOID
			resultWithFruit := result(branch, commit)
			provenance, provenanceErr := captureFruitProvenance(ctx, sourcePath, branch, commit, FruitPublicationPublished, fruitCreatedAt)
			if provenanceErr != nil {
				return seedPublicationFailure(result("", ""), "publication", "provenance-unavailable", false, "", "capture Seed Fruit provenance: repository identity could not be resolved")
			}
			resultWithFruit.Repository = provenance.Repository
			resultWithFruit.Workspace = provenance.Workspace
			resultWithFruit.PublicationState = provenance.PublicationState
			resultWithFruit.CreatedAt = provenance.CreatedAt
			return resultWithFruit, nil
		}

		provenance, provenanceErr := captureFruitProvenance(ctx, sourcePath, branch, commit, FruitPublicationLocalOnly, fruitCreatedAt)
		if provenanceErr != nil {
			return seedPublicationFailure(result("", ""), "publication", "provenance-unavailable", false, "", "capture Seed Fruit provenance: repository identity could not be resolved")
		}
		fruitResult := result(branch, commit)
		fruitResult.Repository = provenance.Repository
		fruitResult.Workspace = provenance.Workspace
		fruitResult.PublicationState = provenance.PublicationState
		fruitResult.CreatedAt = provenance.CreatedAt
		return fruitResult, nil
	}

	return result(branch, commit), nil

}

func seedPublicationFailure(result SeedRunResult, phase, outcome string, retrySafe bool, requestID, message string) (SeedRunResult, error) {
	diagnostic := core.SeedPublicationDiagnostic{
		FailureCategory: core.SeedFailureCategoryFruitPublication,
		ExecutionStatus: result.Status,
		Phase:           phase,
		Outcome:         outcome,
		RetrySafe:       retrySafe,
		Message:         message,
		RequestID:       safeGitHubRequestID(requestID),
	}
	result.PublicationDiagnostic = &diagnostic
	return result, &SeedPublicationFailure{Diagnostic: diagnostic}
}

func seedPublicationFailureFromError(result SeedRunResult, err error) (SeedRunResult, error) {
	var publicationErr *apiFruitPublicationFailure
	if errors.As(err, &publicationErr) {
		return seedPublicationFailure(result, publicationErr.Phase, publicationErr.Outcome, publicationErr.RetrySafe, publicationErr.RequestID, publicationErr.Message)
	}
	return seedPublicationFailure(result, "publication", "publication-failed", false, "", "Seed Fruit publication could not be completed")
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

// seedCandidateRevision resolves the immutable candidate commit for one Seed
// iteration. A checkpoint commit reported by the integration path is preferred;
// if no checkpoint was created, the iteration's starting candidate remains
// authoritative.
func seedCandidateRevision(ctx context.Context, sourcePath, startRevision, checkpointCommit string) (string, error) {
	candidate := strings.TrimSpace(checkpointCommit)
	if candidate == "" {
		candidate = strings.TrimSpace(startRevision)
	}
	if candidate == "" {
		return "", fmt.Errorf("Seed candidate commit is empty")
	}
	resolved, err := runGitCommand(ctx, sourcePath, "rev-parse", "--verify", "--end-of-options", candidate+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve Seed candidate commit %q: %w", candidate, err)
	}
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return "", fmt.Errorf("resolve Seed candidate commit %q returned no commit", candidate)
	}
	return resolved, nil
}

// isRecoverableSeedSproutFailure names the narrow cognitive/protocol failure
// that may be salvaged after a Seed Sprout has already produced work. All
// provider, Terrarium, capability-boundary, and other infrastructure failures
// remain fail-closed because they do not establish trustworthy candidate
// provenance.
func isRecoverableSeedSproutFailure(runErr error) bool {
	if runErr == nil {
		return false
	}

	// A joined error can contain the recoverable protocol sentinel alongside a
	// checkpoint, provider, or other infrastructure failure. That is not a pure
	// convergence failure and must not enter the salvage path.
	if joined, ok := runErr.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		return len(causes) == 1 && isRecoverableSeedSproutFailure(causes[0])
	}
	if wrapped, ok := runErr.(interface{ Unwrap() error }); ok {
		return isRecoverableSeedSproutFailure(wrapped.Unwrap())
	}
	return errors.Is(runErr, errUnusableReply) || errors.Is(runErr, errSproutTurnLimit)
}

// runSeedVerify runs the verify command deterministically against a throwaway
// worktree of the exact candidate commit and reports its deterministic result.
// The workspace is read-only and rooted in the Stem-owned run-workspace boundary so the Docker
// daemon sees the same host path the Stem materialized. A non-nil error is an
// infrastructure failure (the verdict could not be produced), distinct from
// command exits. Only exit 1 is repairable.
func runSeedVerify(ctx context.Context, sourcePath, candidateCommit string, verify, egress []string) seedVerifyReport {
	worktree, err := createSeedVerificationWorktree(ctx, sourcePath, candidateCommit)
	if err != nil {
		return seedVerifyReport{Err: fmt.Errorf("create verify worktree: %w", err)}
	}

	execution := StomaExecution{
		Workspace:         worktree,
		Command:           verify,
		Egress:            egress,
		Timeout:           seedVerifyTimeout,
		ReadOnlyWorkspace: true,
	}
	if err := configureSeedGoVerification(worktree, verify, &execution); err != nil {
		cleanupErr := removeSeedVerificationWorktree(ctx, sourcePath, worktree)
		return seedVerifyReport{Err: errors.Join(err, cleanupErr)}
	}

	var metadata seedGoMetadataSnapshot
	if execution.SkipHostModuleCache {
		var metaErr error
		metadata, metaErr = snapshotSeedGoMetadata(worktree)
		if metaErr != nil {
			cleanupErr := removeSeedVerificationWorktree(ctx, sourcePath, worktree)
			return seedVerifyReport{Err: errors.Join(metaErr, cleanupErr)}
		}
	}

	result, err := RunStoma(ctx, execution)
	if result.TimedOut || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		cleanupErr := removeSeedVerificationWorktree(ctx, sourcePath, worktree)
		output := strings.TrimSpace(strings.TrimSpace(result.Stdout) + "\n" + strings.TrimSpace(result.Stderr))
		return seedVerifyReport{Output: output, TimedOut: true, Err: cleanupErr}
	}
	if err == nil && execution.SkipHostModuleCache {
		err = metadata.assertUnchanged(worktree)
	}
	cleanupErr := removeSeedVerificationWorktree(ctx, sourcePath, worktree)
	if err != nil {
		return seedVerifyReport{Err: errors.Join(err, cleanupErr)}
	}
	if cleanupErr != nil {
		return seedVerifyReport{Err: cleanupErr}
	}
	output := strings.TrimSpace(strings.TrimSpace(result.Stdout) + "\n" + strings.TrimSpace(result.Stderr))
	code := result.ExitCode
	return seedVerifyReport{
		Output:   output,
		Passed:   result.ExitCode == 0 && !result.TimedOut,
		ExitCode: &code,
		TimedOut: result.TimedOut,
	}
}

func seedVerificationDiagnostic(iteration int, report seedVerifyReport) core.SeedVerificationDiagnostic {
	diagnostic := core.SeedVerificationDiagnostic{
		Iteration: iteration,
		TimedOut:  report.TimedOut,
		ExitCode:  report.ExitCode,
	}
	switch {
	case report.TimedOut:
		diagnostic.Outcome = core.SeedVerificationOutcomeTimedOut
		diagnostic.Message = boundSeedVerifyDiagnostic("verify command timed out")
	case report.Err != nil:
		diagnostic.Outcome = core.SeedVerificationOutcomeInfrastructureFailed
		diagnostic.Message = boundSeedVerifyDiagnostic("verify infrastructure could not execute")
	case report.ExitCode != nil:
		switch {
		case *report.ExitCode == 0:
			diagnostic.Outcome = core.SeedVerificationOutcomePassed
		case *report.ExitCode == 1:
			diagnostic.Outcome = core.SeedVerificationOutcomePredicateFailed
			diagnostic.Message = boundSeedVerifyDiagnostic("verify command exited 1")
		default:
			diagnostic.Outcome = core.SeedVerificationOutcomeConfigurationInvalid
			diagnostic.Message = boundSeedVerifyDiagnostic("verify command returned a non-predicate exit")
		}
	default:
		// The production Stoma runner always records an exit code for a
		// completed command. The injected seam may provide only its already
		// deterministic pass bit; infrastructure failures use Err above.
		if report.Passed {
			diagnostic.Outcome = core.SeedVerificationOutcomePassed
		} else {
			diagnostic.Outcome = core.SeedVerificationOutcomePredicateFailed
			diagnostic.Message = boundSeedVerifyDiagnostic("verify command did not pass")
		}
	}
	return diagnostic
}

// seedVerificationFailureFact renders only deterministic facts about a failed
// verification. It deliberately does not guess why a command returned a
// non-zero exit code.
func seedVerificationFailureFact(report seedVerifyReport) string {
	if report.TimedOut {
		return "command timed out"
	}
	if report.ExitCode != nil {
		return fmt.Sprintf("command exited %d", *report.ExitCode)
	}
	return "command did not pass"
}

const seedVerifyFeedbackBound = 4000

// seedCandidateDiffBound limits candidate evidence separately from verifier
// feedback. A failed predicate may have both, and neither source may consume
// the whole retry prompt on its own.
const seedCandidateDiffBound = 4000

const seedCandidateDiffHeading = "Current candidate diff against the Seed base:"

const seedEvidenceTruncatedSuffix = "\n…(truncated)"

// seedVerificationFeedback is the single bounded feedback path from a
// deterministic verifier to the next Sprout prompt. Only predicate failures
// are repairable; configuration, infrastructure, and timeout outcomes stop
// verification-driven convergence.
func seedVerificationFeedback(report seedVerifyReport) string {
	if seedVerificationDiagnostic(0, report).Outcome != core.SeedVerificationOutcomePredicateFailed {
		return ""
	}

	output := strings.TrimSpace(report.Output)
	if report.TimedOut || output == "" {
		diagnostic := "Verification failed: " + seedVerificationFailureFact(report) + "."
		if output == "" {
			output = diagnostic
		} else {
			output = diagnostic + "\n" + output
		}
	}
	return boundSeedVerifyFeedback(output)
}

func boundSeedVerifyFeedback(message string) string {
	return boundSeedEvidence(strings.TrimSpace(message), seedVerifyFeedbackBound)
}

// seedCandidateDiff returns the cumulative candidate diff that the next
// Sprout needs to inspect after a failed predicate. Both revisions are
// immutable identities established by the Seed lifecycle, so this describes
// base -> current candidate rather than only the latest iteration delta.
// Auxiliary evidence is best-effort: a Git failure must not replace the
// authoritative verifier verdict or change the execution outcome.
func seedCandidateDiff(ctx context.Context, sourcePath, baseRevision, candidateRevision string) string {
	baseRevision = strings.TrimSpace(baseRevision)
	candidateRevision = strings.TrimSpace(candidateRevision)
	if baseRevision == "" || candidateRevision == "" || baseRevision == candidateRevision {
		return ""
	}
	diffLimit := seedCandidateDiffBound - len(seedEvidenceTruncatedSuffix)
	diff, truncated, err := runGitCommandBoundedRawOutput(ctx, sourcePath, diffLimit, "diff", "--no-color", baseRevision, candidateRevision)
	if err != nil {
		return ""
	}
	if truncated {
		diff += seedEvidenceTruncatedSuffix
	}
	return boundSeedCandidateDiff(diff)
}

func boundSeedCandidateDiff(message string) string {
	return boundSeedEvidence(strings.TrimRight(message, "\r\n"), seedCandidateDiffBound)
}

func boundSeedEvidence(message string, bound int) string {
	if len(message) <= bound {
		return message
	}
	return message[:bound-len(seedEvidenceTruncatedSuffix)] + seedEvidenceTruncatedSuffix
}

func boundSeedVerifyDiagnostic(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if len(message) > seedVerifyDiagnosticBound {
		return message[:seedVerifyDiagnosticBound]
	}
	return message
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
func seedBoundaryContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if ctx.Err() != nil {
		return context.WithoutCancel(ctx)
	}
	return ctx
}

func seedUnresolvedContinuationError(ctx context.Context, execution SeedExecution, _ string) error {
	if execution.Continuation.HasUnresolved == nil {
		return nil
	}
	unresolved, err := execution.Continuation.HasUnresolved(ctx)
	if err != nil {
		return err
	}
	if unresolved {
		return core.ErrContinuationUndeliverable
	}
	return nil
}

func seedGoalPrompt(goal string, verify []string, priorFailure string) string {
	return seedGoalPromptWithCandidateEvidence(goal, verify, priorFailure, "")
}

func seedGoalPromptWithCandidateEvidence(goal string, verify []string, priorFailure, candidateDiff string) string {
	var b strings.Builder
	if len(verify) == 0 {
		fmt.Fprintf(&b, "%s\n\nNo deterministic verification command was requested. Do not claim that the Stem verified the objective.", strings.TrimSpace(goal))
	} else {
		verifyJSON, _ := json.Marshal(verify)
		fmt.Fprintf(&b, "%s\n\nDeterministic verification configured by the Stem:\n%s\n\nThe Stem will run this after your changes. Do not execute it merely to satisfy the Seed protocol.",
			strings.TrimSpace(goal), verifyJSON)
	}
	if fail := strings.TrimSpace(priorFailure); fail != "" {
		fail = boundSeedVerifyFeedback(fail)
		fmt.Fprintf(&b, "\n\nA previous attempt did not pass. The verification command failed with:\n%s\n\nFind and fix the cause, then make it pass.", fail)
	}
	if diff := boundSeedCandidateDiff(candidateDiff); diff != "" {
		fmt.Fprintf(&b, "\n\n%s\n%s\n\nThis is deterministic Stem-provided candidate evidence. The next Sprout starts from this candidate.", seedCandidateDiffHeading, diff)
	}
	return b.String()
}

func verifyVerdict(passed bool) string {
	if passed {
		return "PASSED"
	}
	return "FAILED"
}

func seedAcquireSettlementFence(ctx context.Context, execution SeedExecution) (bool, error) {
	if execution.Continuation.AcquireSettlementFence == nil {
		return true, nil
	}
	return execution.Continuation.AcquireSettlementFence(seedBoundaryContext(ctx))
}

func seedBuildExecutionOutcome(report SproutRunReport, runErr error) string {
	if errors.Is(runErr, context.DeadlineExceeded) || report.Outcome == SproutOutcomeTimedOut {
		return core.SeedExecutionOutcomeTimedOut
	}
	switch core.FailureCategory(report.FailureCategory) {
	case core.FailureCategoryExecutionFailed, core.FailureCategoryNoEngagement:
		return core.SeedExecutionOutcomeSproutFailed
	case core.FailureCategoryTerrariumRuntime, core.FailureCategoryProviderAuthRejected, core.FailureCategoryProviderRequestRejected:
		return core.SeedExecutionOutcomeInfrastructureFailed
	default:
		return core.SeedExecutionOutcomeInfrastructureFailed
	}
}
