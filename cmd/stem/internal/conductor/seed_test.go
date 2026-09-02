package conductor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

func TestSeedGoalPromptPreservesVerifierArgvAndStemOwnership(t *testing.T) {
	verify := []string{
		"sh",
		"-c",
		"printf 'Hello from OpenTendril.\\n' | cmp -s - HELLO.md",
		"line one\nline two",
		`--literal "quotes" && rm -rf`,
	}
	priorFailure := "Verification failed: command exited 2."

	prompt := seedGoalPrompt("create HELLO.md", verify, priorFailure)
	encoded, err := json.Marshal(verify)
	if err != nil {
		t.Fatalf("json.Marshal verifier argv: %v", err)
	}
	wantContext := "Deterministic verification configured by the Stem:\n" + string(encoded)
	if !strings.Contains(prompt, wantContext) {
		t.Fatalf("prompt omitted structured verifier context %q:\n%s", wantContext, prompt)
	}
	if strings.Contains(prompt, strings.Join(verify, " ")) {
		t.Fatalf("prompt flattened verifier argv into a shell command:\n%s", prompt)
	}
	if !strings.Contains(prompt, "The Stem will run this after your changes.") {
		t.Fatalf("prompt did not assign deterministic verification to the Stem:\n%s", prompt)
	}
	if strings.Contains(prompt, "Run it to check your work before finishing.") {
		t.Fatalf("prompt still instructs the Sprout to run the verifier:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not execute it merely to satisfy the Seed protocol.") {
		t.Fatalf("prompt did not forbid Sprout-side verifier execution:\n%s", prompt)
	}
	if !strings.Contains(prompt, priorFailure) {
		t.Fatalf("retry feedback disappeared from the prompt:\n%s", prompt)
	}
}

// newSeedRepo builds a real git repository on branch main with one commit, the
// local checkout RunSeed grows a Seed against.
func newSeedRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "seed@example.com"},
		{"config", "user.name", "Seed Tester"},
		{"checkout", "-b", "main"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return repo
}

// restoreSeeds saves and restores the injectable execution seams so a test's
// overrides never leak into another.
func restoreSeeds(t *testing.T) {
	t.Helper()
	build, verify, candidateDiff := seedBuildFn, seedVerifyFn, seedCandidateDiffFn
	t.Cleanup(func() { seedBuildFn, seedVerifyFn, seedCandidateDiffFn = build, verify, candidateDiff })
}

// fakeBuild simulates the Sprout builder: it creates the seed branch once (so
// the run has a reviewable branch) and records every prompt it was handed.
func fakeBuild(prompts *[]string) func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
	return func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		*prompts = append(*prompts, prompt)
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, Output: "deadbeef"}, nil
	}
}

func TestRunSeedSatisfiedOnFirstVerify(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
		SessionID: "tendril-seed-one",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusSatisfied {
		t.Fatalf("status = %q, want satisfied. log:\n%s", res.Status, res.Logs)
	}
	if res.Iterations != 1 {
		t.Fatalf("iterations = %d, want 1", res.Iterations)
	}
	if res.Branch == "" {
		t.Fatal("no seed branch captured for review")
	}
	if len(prompts) != 1 {
		t.Fatalf("build ran %d time(s), want 1", len(prompts))
	}
	if strings.Contains(prompts[0], "previous attempt") || strings.Contains(prompts[0], "Verification failed") {
		t.Fatalf("successful verification added retry failure feedback: %q", prompts[0])
	}
	if strings.Contains(prompts[0], seedCandidateDiffHeading) {
		t.Fatalf("initial prompt included candidate evidence: %q", prompts[0])
	}
}

// TestRunSeedExhaustedThreadsFeedback: a Seed whose verify never passes spends
// its whole iteration budget (exhausted), and each retry's prompt carries the
// previous deterministic verify failure so the Sprout fixes the real cause.
func TestRunSeedExhaustedThreadsFeedback(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "stdout: expected file differs\nstderr: cmp reported a mismatch", Passed: false}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 3,
		SessionID: "tendril-seed-exhausted",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted", res.Status)
	}
	if res.Iterations != 3 {
		t.Fatalf("iterations = %d, want 3", res.Iterations)
	}
	if len(prompts) != 3 {
		t.Fatalf("build ran %d time(s), want 3", len(prompts))
	}
	if strings.Contains(prompts[0], "stdout: expected file differs") || strings.Contains(prompts[0], "stderr: cmp reported a mismatch") {
		t.Error("first prompt must carry no prior failure")
	}
	for _, want := range []string{"stdout: expected file differs", "stderr: cmp reported a mismatch"} {
		if !strings.Contains(prompts[1], want) {
			t.Errorf("a retry prompt must preserve verifier %s output", want)
		}
	}
}

func TestRunSeedSilentVerificationThreadsExitFeedback(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	exitCode := 2
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{ExitCode: &exitCode}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 2,
		SessionID: "tendril-seed-silent-failure",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted", res.Status)
	}
	if len(prompts) != 2 {
		t.Fatalf("build ran %d time(s), want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "Verification failed: command exited 2.") {
		t.Fatalf("silent verification failure was not fed back with its exit code: %q", prompts[1])
	}
	if len(res.VerificationDiagnostics) != 2 {
		t.Fatalf("verification diagnostics = %+v, want one per failed iteration", res.VerificationDiagnostics)
	}
	for _, diagnostic := range res.VerificationDiagnostics {
		if diagnostic.Outcome != core.SeedVerificationOutcomePredicateFailed || diagnostic.ExitCode == nil || *diagnostic.ExitCode != 2 || diagnostic.TimedOut {
			t.Fatalf("silent verification diagnostic = %+v, want predicate failure with exit 2", diagnostic)
		}
	}
}

func TestSeedVerificationFeedbackTimeoutIsExplicitAndBounded(t *testing.T) {
	feedback := seedVerificationFeedback(seedVerifyReport{
		Output:   strings.Repeat("timeout output ", seedVerifyFeedbackBound),
		TimedOut: true,
	})
	if !strings.Contains(feedback, "Verification failed: command timed out.") {
		t.Fatalf("timeout feedback omitted its deterministic diagnostic: %q", feedback)
	}
	if len(feedback) > seedVerifyFeedbackBound {
		t.Fatalf("timeout feedback length = %d, want <= %d", len(feedback), seedVerifyFeedbackBound)
	}

	if got := seedVerificationFeedback(seedVerifyReport{Err: fmt.Errorf("private infrastructure detail")}); got != "" {
		t.Fatalf("infrastructure failure produced retry feedback: %q", got)
	}
}

func TestSeedCandidateDiffPreservesNoNewlineMarkerAndIsBounded(t *testing.T) {
	repo := newSeedRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	branch := "tendril/seed-candidate-evidence"
	if _, err := runGitCommand(ctx, repo, "branch", branch, base); err != nil {
		t.Fatalf("create candidate branch: %v", err)
	}
	candidate := commitPathOnBranch(t, repo, branch, "HELLO.md", "Hello")
	if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
		t.Fatalf("restore main: %v", err)
	}

	diff := seedCandidateDiff(ctx, repo, base, candidate)
	if !strings.Contains(diff, "\\ No newline at end of file") {
		t.Fatalf("candidate diff omitted Git's no-final-newline marker:\n%s", diff)
	}
	if !strings.Contains(diff, "HELLO.md") {
		t.Fatalf("candidate diff omitted the changed path:\n%s", diff)
	}
	if got := boundSeedCandidateDiff("diff --git a/file b/file\n+trailing spaces   \n"); !strings.Contains(got, "+trailing spaces   ") {
		t.Fatalf("candidate evidence stripped meaningful trailing spaces: %q", got)
	}

	candidate = commitPathOnBranch(t, repo, branch, "large.txt", strings.Repeat("candidate evidence ", seedCandidateDiffBound))
	if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
		t.Fatalf("restore main after large candidate: %v", err)
	}
	bounded := seedCandidateDiff(ctx, repo, base, candidate)
	if len(bounded) > seedCandidateDiffBound {
		t.Fatalf("candidate evidence length = %d, want <= %d", len(bounded), seedCandidateDiffBound)
	}
	if !strings.HasSuffix(bounded, seedEvidenceTruncatedSuffix) {
		t.Fatalf("large candidate evidence omitted truncation marker: %q", bounded[len(bounded)-min(len(bounded), 32):])
	}
}

func TestRunSeedCandidateEvidenceFailurePreservesLifecycle(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var diffCalls int
	seedBuildFn = fakeBuild(new([]string))
	seedCandidateDiffFn = func(context.Context, string, string, string) string {
		diffCalls++
		return ""
	}
	exitCode := 1
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{ExitCode: &exitCode}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 2,
		SessionID: "seed-candidate-evidence-failure",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if diffCalls != 1 {
		t.Fatalf("candidate diff calls = %d, want one retry-evidence attempt", diffCalls)
	}
	if res.Status != SeedStatusExhausted || res.Iterations != 2 {
		t.Fatalf("status/iterations = %q/%d, want exhausted/2", res.Status, res.Iterations)
	}
	if len(res.VerificationDiagnostics) != 2 {
		t.Fatalf("verification diagnostics = %+v, want the authoritative predicate failure", res.VerificationDiagnostics)
	}
	for _, diagnostic := range res.VerificationDiagnostics {
		if diagnostic.Outcome != core.SeedVerificationOutcomePredicateFailed || diagnostic.ExitCode == nil || *diagnostic.ExitCode != 1 {
			t.Fatalf("verification diagnostic = %+v, want predicate failure with exit 1", diagnostic)
		}
	}
}

func TestRunSeedWitheredOnBuildError(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		return SproutRunReport{}, fmt.Errorf("sprout crashed")
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		t.Fatal("verify must not run after a withered build")
		return seedVerifyReport{}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
		SessionID: "tendril-seed-withered",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	if res.Iterations != 1 {
		t.Fatalf("iterations = %d, want 1", res.Iterations)
	}
}

// TestRunSeedWitheredOnVerifyInfraError: an infrastructure failure producing the
// verdict (not a clean non-zero exit) withers the run rather than being read as
// a failed verification to iterate on.
func TestRunSeedWitheredOnVerifyInfraError(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Err: fmt.Errorf("terrarium unavailable")}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
		SessionID: "tendril-seed-verify-infra",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
}

func TestRunSeedExhaustedCandidateIsNotFruit(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var seedBranch string
	seedBuildFn = committedSeedBuild(t, repo, &seedBranch)
	exitCode := 1
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "candidate differs", ExitCode: &exitCode}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 1,
		SessionID: "seed-exhausted-candidate",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted; logs: %s", res.Status, res.Logs)
	}
	assertSeedCandidateIsNotFruit(t, res, repo, seedBranch)
	if len(res.VerificationDiagnostics) != 1 {
		t.Fatalf("verification diagnostics = %+v, want one diagnostic", res.VerificationDiagnostics)
	}
	diagnostic := res.VerificationDiagnostics[0]
	if diagnostic.Outcome != core.SeedVerificationOutcomePredicateFailed || diagnostic.ExitCode == nil || *diagnostic.ExitCode != exitCode {
		t.Fatalf("verification diagnostic = %+v, want predicate failure with exit %d", diagnostic, exitCode)
	}
}

func TestRunSeedWitheredCandidateIsNotFruit(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var seedBranch string
	seedBuildFn = committedSeedBuild(t, repo, &seedBranch)
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Err: errors.New("terrarium unavailable")}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 1,
		SessionID: "seed-withered-candidate",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	assertSeedCandidateIsNotFruit(t, res, repo, seedBranch)
	if len(res.VerificationDiagnostics) != 1 {
		t.Fatalf("verification diagnostics = %+v, want one diagnostic", res.VerificationDiagnostics)
	}
	if diagnostic := res.VerificationDiagnostics[0]; diagnostic.Outcome != core.SeedVerificationOutcomeInfrastructureFailed || diagnostic.Message == "" {
		t.Fatalf("verification diagnostic = %+v, want retained infrastructure diagnostics", diagnostic)
	}
}

func assertSeedCandidateIsNotFruit(t *testing.T, res SeedRunResult, repo, seedBranch string) {
	t.Helper()
	if res.Branch != "" || res.Commit != "" {
		t.Fatalf("Fruit identity = branch %q commit %q, want none", res.Branch, res.Commit)
	}
	if !strings.Contains(res.Diff, "fruit.txt") {
		t.Fatalf("candidate diff = %q, want the retained candidate change", res.Diff)
	}
	if seedBranch == "" || !branchExists(t, repo, seedBranch) {
		t.Fatalf("internal candidate branch %q was not retained", seedBranch)
	}
}

func TestRunSeedSalvagesCheckpointedRecoverableSproutFailure(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	ctx := context.Background()
	var prompts []string
	var starts []string
	var candidate string
	var buildCount int

	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		starts = append(starts, orch.SeedStartRevision)
		buildCount++
		if !localBranchExists(repo, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, repo, "branch", orch.SubstrateBranch, orch.SeedStartRevision); err != nil {
				return SproutRunReport{}, err
			}
		}
		if buildCount == 1 {
			candidate = commitPathOnBranch(t, repo, orch.SubstrateBranch, "HELLO.md", "Hello from OpenTendril.")
			if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
				return SproutRunReport{}, err
			}
			return SproutRunReport{
				Outcome:             SproutOutcomeFailed,
				seedCandidateCommit: candidate,
			}, errUnusableReply
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: candidate}, nil
	}

	var verifiedCandidates []string
	seedVerifyFn = func(_ context.Context, _ string, candidateRevision string, _ []string, _ []string) seedVerifyReport {
		verifiedCandidates = append(verifiedCandidates, candidateRevision)
		if len(verifiedCandidates) == 1 {
			code := 1
			return seedVerifyReport{Output: "cmp: \u005c No newline at end of file", ExitCode: &code}
		}
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	res, err := RunSeed(ctx, SeedExecution{
		Substrate: repo, Goal: "create HELLO.md", Verify: round16HelloVerifyArgv(), MaxIterations: 2,
		SessionID: "seed-salvage-recoverable",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusSatisfied {
		t.Fatalf("status = %q, want satisfied; logs:\n%s", res.Status, res.Logs)
	}
	if res.Iterations != 2 || buildCount != 2 || len(prompts) != 2 {
		t.Fatalf("iterations/builds/prompts = %d/%d/%d, want 2/2/2", res.Iterations, buildCount, len(prompts))
	}
	if len(verifiedCandidates) != 2 || verifiedCandidates[0] != candidate || verifiedCandidates[1] != candidate {
		t.Fatalf("verified candidates = %v, want the immutable salvaged candidate %q twice", verifiedCandidates, candidate)
	}
	if len(starts) != 2 || starts[1] != candidate {
		t.Fatalf("iteration starts = %v, want second Sprout to start from salvaged candidate %q", starts, candidate)
	}
	if !strings.Contains(res.Logs, "sprout withered") || !strings.Contains(res.Logs, "model reply attempted a tool call") {
		t.Fatalf("original recoverable Sprout failure was not retained in Seed logs:\n%s", res.Logs)
	}
	for _, want := range []string{"A previous attempt did not pass", "Current candidate diff against the Seed base:", "\\ No newline at end of file"} {
		if !strings.Contains(prompts[1], want) {
			t.Fatalf("retry prompt omitted %q:\n%s", want, prompts[1])
		}
	}
}

func TestRecoverableSeedSproutFailureAllowlistIsExact(t *testing.T) {
	turnLimit := sproutTurnLimitError{limit: sproutMaxIterations}
	wrappedTurnLimit := fmt.Errorf("sprout failed: %w", turnLimit)

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unusable reply", err: errUnusableReply, want: true},
		{name: "turn limit", err: turnLimit, want: true},
		{name: "wrapped turn limit", err: wrappedTurnLimit, want: true},
		{name: "plain legacy text", err: errors.New("Sprout reached max iterations (20)"), want: false},
		{name: "joined turn limit", err: errors.Join(turnLimit, errors.New("checkpoint failed")), want: false},
		{name: "provider failure", err: errors.New("provider unavailable"), want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isRecoverableSeedSproutFailure(testCase.err); got != testCase.want {
				t.Fatalf("isRecoverableSeedSproutFailure(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

func TestRunSeedDoesNotSalvageRecoverableFailureWithoutCheckpoint(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var verifyCalled bool
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		return SproutRunReport{Outcome: SproutOutcomeFailed}, errUnusableReply
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		verifyCalled = true
		return seedVerifyReport{Passed: true}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "create HELLO.md", Verify: round16HelloVerifyArgv(), MaxIterations: 2,
		SessionID: "seed-salvage-no-checkpoint",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	if res.Iterations != 1 {
		t.Fatalf("iterations = %d, want 1 after fail-closed salvage refusal", res.Iterations)
	}
	if verifyCalled {
		t.Fatal("verification ran without an immutable checkpoint")
	}
}

func TestRunSeedDoesNotSalvageJoinedTurnLimitFailure(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	checkpointErr := errors.New("checkpoint materialization failed")
	var verifyCalled bool
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		return SproutRunReport{Outcome: SproutOutcomeFailed, seedCandidateCommit: "untrusted-checkpoint"}, errors.Join(sproutTurnLimitError{limit: sproutMaxIterations}, checkpointErr)
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		verifyCalled = true
		return seedVerifyReport{Passed: true}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "create HELLO.md", Verify: round16HelloVerifyArgv(), MaxIterations: 2,
		SessionID: "seed-salvage-joined-turn-limit-failure",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered || res.Iterations != 1 {
		t.Fatalf("status/iterations = %q/%d, want withered/1", res.Status, res.Iterations)
	}
	if verifyCalled {
		t.Fatal("verification ran after a recoverable failure was joined with checkpoint failure")
	}
}

func TestRunSeedRequiresGitSubstrate(t *testing.T) {
	restoreSeeds(t)
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		t.Fatal("build must not run for a non-git substrate")
		return SproutRunReport{}, nil
	}
	dir := t.TempDir() // a directory, but not a git repository

	if _, err := RunSeed(context.Background(), SeedExecution{
		Substrate: dir, Goal: "g", Verify: []string{"true"}, MaxIterations: 2,
		SessionID: "tendril-seed-nongit",
	}); err == nil {
		t.Fatal("a non-git substrate was accepted; seed.grow needs a branch + diff")
	}
}

func TestRunSeedRequiresPhytomer(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	if _, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"true"}, MaxIterations: 1,
	}); err == nil {
		t.Fatal("a sessionless Seed was accepted")
	}
}

func TestRunSeedIterationsShareOnePhytomer(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var seen []string
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		seen = append(seen, orch.SessionID)
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, Output: "ok"}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "still failing"}
	}

	const phytomer = "tendril-seed-shared"
	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 3,
		SessionID: phytomer,
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Iterations != 3 || len(seen) != 3 {
		t.Fatalf("iterations=%d builds=%d, want 3/3", res.Iterations, len(seen))
	}
	for i, id := range seen {
		if id != phytomer {
			t.Fatalf("iteration %d sessionID = %q, want %q", i+1, id, phytomer)
		}
	}
}

func TestTwoSeedsUseDistinctPhytomers(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	seedBuildFn = fakeBuild(new([]string))
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	first, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "one", Verify: []string{"true"}, MaxIterations: 1,
		SessionID: "tendril-seed-a",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "two", Verify: []string{"true"}, MaxIterations: 1,
		SessionID: "tendril-seed-b",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Status != SeedStatusSatisfied || second.Status != SeedStatusSatisfied {
		t.Fatalf("statuses = %q / %q", first.Status, second.Status)
	}
}

func TestPrepareSproutRunsBeforeTerrariumWork(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var order []string
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		order = append(order, "build:"+orch.StepID)
		return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	_, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"true"}, MaxIterations: 2,
		SessionID: "tendril-seed-prep",
		PrepareSprout: func(_ context.Context, orch *DockerOrchestrator, iteration int) error {
			orch.StepID = "iter-" + itoa(iteration)
			order = append(order, "prep:"+orch.StepID)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if len(order) < 2 || order[0] != "prep:iter-1" || order[1] != "build:iter-1" {
		t.Fatalf("order = %v, want prep before build", order)
	}
}

func TestSeedFruitCommitIsNotInventedFromUnchangedBranch(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	seedBuildFn = fakeBuild(new([]string))
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"true"}, MaxIterations: 1,
		SessionID: "tendril-seed-nochange",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Branch == "" {
		t.Fatal("expected a reviewable seed branch even when unchanged")
	}
	if res.Commit != "" {
		t.Fatalf("invented commit %q from a no-change seed branch", res.Commit)
	}
}

func TestSeedFruitCommitIsTheBranchTipWhenWorkExists(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		if _, err := runGitCommand(ctx, orch.Substrate, "checkout", orch.SubstrateBranch); err != nil {
			return SproutRunReport{}, err
		}
		path := filepath.Join(orch.Substrate, "fruit.txt")
		if err := os.WriteFile(path, []byte("grown\n"), 0o644); err != nil {
			return SproutRunReport{}, err
		}
		if _, err := runGitCommand(ctx, orch.Substrate, "add", "fruit.txt"); err != nil {
			return SproutRunReport{}, err
		}
		if _, err := runGitCommand(ctx, orch.Substrate, "commit", "-m", "seed fruit"); err != nil {
			return SproutRunReport{}, err
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"true"}, MaxIterations: 1,
		SessionID: "tendril-seed-fruit",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Commit == "" {
		t.Fatal("expected the real Fruit commit SHA")
	}
	tip, err := runGitCommand(context.Background(), repo, "rev-parse", res.Branch)
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if res.Commit != strings.TrimSpace(tip) {
		t.Fatalf("commit = %q, want branch tip %q", res.Commit, strings.TrimSpace(tip))
	}
	head, err := runGitCommand(context.Background(), repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("rev-parse main: %v", err)
	}
	if res.Commit == strings.TrimSpace(head) {
		t.Fatal("Fruit commit was the pre-run HEAD")
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func TestRunSeedManagedAPIFruit(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)
	restoreSeeds(t)
	repo := newSeedRepo(t)

	keyPath := filepath.Join(t.TempDir(), "fake.pem")
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	os.WriteFile(keyPath, pemBytes, 0o644)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  seed-api-test:\n    url: "+repo+"\n    branch: main\n    checkout:\n      mode: managed\n    commit: api\n    auth:\n      method: app\n      appId: \"1234\"\n      privateKeyPath: "+keyPath+"\n")

	// Stub materializeManagedCheckoutFn so the actual clone uses the local repo URL
	origMaterialize := materializeManagedCheckoutFn
	t.Cleanup(func() { materializeManagedCheckoutFn = origMaterialize })
	materializeManagedCheckoutFn = func(name, dest, url, branch string, _ ResolvedCredential, _ []string) error {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if _, err := runGitCommand(context.Background(), filepath.Dir(dest), "clone", "-q", repo, dest); err != nil {
			return err
		}
		return nil
	}

	// Materialize the checkout manually before RunSeed, since RunSeed expects the directory to exist
	dest := filepath.Join(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT"), "seed-api-test")
	if err := materializeManagedCheckoutFn("seed-api-test", dest, repo, "main", ResolvedCredential{}, nil); err != nil {
		t.Fatalf("materialize test repo: %v", err)
	}

	// Start the fake API server
	fake := startAPIFruitFake(t, 201, "abcd1234abcd1234abcd1234abcd1234abcd1234")

	var prompts []string

	origStart := startTerrariumSessionFn
	t.Cleanup(func() { startTerrariumSessionFn = origStart })
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return stubCountingSession(t), nil
	}

	origSprout := newSproutFn
	t.Cleanup(func() { newSproutFn = origSprout })
	newSproutFn = func(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string, sessionID string) (sproutRunner, error) {
		return &testSproutRunner{
			run: func(ctx context.Context, taskPrompt string) (sproutResult, error) {
				prompts = append(prompts, taskPrompt)
				filename := fmt.Sprintf("fruit-%d.txt", len(prompts))
				if err := os.WriteFile(filepath.Join(workspace, filename), []byte("content"), 0o644); err != nil {
					return sproutResult{}, err
				}
				return sproutResult{Response: "I did the thing", WroteWorkspace: true}, nil
			},
		}, nil
	}

	origPreflight := runSproutPreflightChecksFn
	t.Cleanup(func() { runSproutPreflightChecksFn = origPreflight })
	runSproutPreflightChecksFn = func(ctx context.Context, _ *llm.Client) error {
		return nil
	}

	origEnsure := ensureSproutImageFn
	t.Cleanup(func() { ensureSproutImageFn = origEnsure })
	ensureSproutImageFn = func(ctx context.Context, imageName string) error {
		return nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		// Pass on the second iteration
		if len(prompts) < 2 {
			return seedVerifyReport{Output: "failed"}
		}
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: "seed-api-test", Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
		SessionID: "tendril-seed-api",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusSatisfied {
		t.Fatalf("status = %q, want satisfied, logs: %s", res.Status, res.Logs)
	}
	if res.Iterations != 2 {
		t.Fatalf("iterations = %d, want 2", res.Iterations)
	}
	if res.Commit != "abcd1234abcd1234abcd1234abcd1234abcd1234" {
		t.Fatalf("commit = %q, want published OID from GraphQL mock", res.Commit)
	}

	if fake.graphQLCalled != 1 {
		t.Fatalf("API publication was called %d times, want exactly 1", fake.graphQLCalled)
	}
}

func TestRunSeedExhaustedManagedAPIFruitIsNotPublished(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	chdirToTempDir(t)
	restoreSeeds(t)

	repo := newSeedRepo(t)
	keyPath := writeSeedTestAppKey(t)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  seed-api-exhausted:\n    url: "+repo+"\n    branch: main\n    checkout:\n      mode: managed\n    commit: api\n    auth:\n      method: app\n      appId: \"1234\"\n      privateKeyPath: "+keyPath+"\n")

	origMaterialize := materializeManagedCheckoutFn
	t.Cleanup(func() { materializeManagedCheckoutFn = origMaterialize })
	materializeManagedCheckoutFn = func(name, dest, url, branch string, _ ResolvedCredential, _ []string) error {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if _, err := runGitCommand(context.Background(), filepath.Dir(dest), "clone", "-q", repo, dest); err != nil {
			return err
		}
		for _, args := range [][]string{
			{"config", "user.email", "seed@example.com"},
			{"config", "user.name", "Seed Tester"},
		} {
			if _, err := runGitCommand(context.Background(), dest, args...); err != nil {
				return err
			}
		}
		return nil
	}
	dest := filepath.Join(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT"), "seed-api-exhausted")
	if err := materializeManagedCheckoutFn("seed-api-exhausted", dest, repo, "main", ResolvedCredential{}, nil); err != nil {
		t.Fatalf("materialize test repo: %v", err)
	}

	fake := startAPIFruitFake(t, http.StatusCreated, "unused")
	var seedBranch string
	seedBuildFn = committedSeedBuild(t, dest, &seedBranch)
	exitCode := 1
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "candidate differs", ExitCode: &exitCode}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: "seed-api-exhausted", Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 1,
		SessionID: "seed-api-exhausted",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted; logs: %s", res.Status, res.Logs)
	}
	if res.Branch != "" || res.Commit != "" {
		t.Fatalf("Fruit identity = branch %q commit %q, want none", res.Branch, res.Commit)
	}
	if !strings.Contains(res.Diff, "fruit.txt") {
		t.Fatalf("candidate diff = %q, want the retained candidate change", res.Diff)
	}
	if fake.installCalled != 0 || fake.tokenCalled != 0 || fake.createRefCalled != 0 || fake.graphQLCalled != 0 {
		t.Fatalf("managed API publication was attempted: install=%d token=%d ref=%d graphql=%d", fake.installCalled, fake.tokenCalled, fake.createRefCalled, fake.graphQLCalled)
	}
}

type testSproutRunner struct {
	run func(ctx context.Context, taskPrompt string) (sproutResult, error)
}

func (r *testSproutRunner) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	return r.run(ctx, taskPrompt)
}

func (r *testSproutRunner) setSeedIntegrationCheckpoint(bool) {}

func committedSeedBuild(t *testing.T, repo string, capturedBranch *string) func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
	t.Helper()
	return func(ctx context.Context, orch *DockerOrchestrator, _ string) (SproutRunReport, error) {
		*capturedBranch = orch.SubstrateBranch
		if !localBranchExists(repo, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, repo, "checkout", "-b", orch.SubstrateBranch, "main"); err != nil {
				return SproutRunReport{}, err
			}
			if err := os.WriteFile(filepath.Join(repo, "fruit.txt"), []byte("reviewable work\n"), 0o644); err != nil {
				return SproutRunReport{}, err
			}
			for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "seed work"}, {"checkout", "main"}} {
				if _, err := runGitCommand(ctx, repo, args...); err != nil {
					return SproutRunReport{}, err
				}
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, Output: "done"}, nil
	}
}

func writeSeedTestAppKey(t *testing.T) string {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test App key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write test App key: %v", err)
	}
	return keyPath
}

func TestRunSeedManagedAPIPublicationFailureReportsNoFruit(t *testing.T) {
	restoreSeeds(t)
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	repo := newSeedRepo(t)
	if _, err := runGitCommand(context.Background(), repo, "remote", "add", "origin", "https://github.com/owner/repo.git"); err != nil {
		t.Fatalf("add origin: %v", err)
	}
	base, err := runGitCommand(context.Background(), repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	base = strings.TrimSpace(base)

	keyPath := writeSeedTestAppKey(t)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		fmt.Sprintf("substrates:\n  seed-api-failure:\n    path: %s\n    commit: api\n    auth:\n      method: app\n      appId: \"1234\"\n      privateKeyPath: %s\n", repo, keyPath))

	fake := startAPIFruitFake(t, 201, "")
	fake.graphQLError = "publication denied"

	var seedBranch string
	seedBuildFn = committedSeedBuild(t, repo, &seedBranch)
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	res, runErr := RunSeed(context.Background(), SeedExecution{
		Substrate:     "seed-api-failure",
		Goal:          "make reviewable work",
		Verify:        []string{"true"},
		MaxIterations: 1,
		SessionID:     "seed-api-failure-session",
	})
	if runErr == nil {
		t.Fatal("RunSeed succeeded despite final API publication failure")
	}
	if !strings.Contains(runErr.Error(), "publish Seed Fruit via API") {
		t.Fatalf("RunSeed error = %q, want publication failure", runErr)
	}
	if res.Branch != "" || res.Commit != "" {
		t.Fatalf("Fruit identity = branch %q commit %q, want none after failed API publication", res.Branch, res.Commit)
	}
	if seedBranch == "" || !branchExists(t, repo, seedBranch) {
		t.Fatalf("local Seed branch %q was not preserved", seedBranch)
	}
	if fake.graphQLCalled != 1 {
		t.Fatalf("GraphQL calls = %d, want exactly 1 final publication attempt", fake.graphQLCalled)
	}

	mainTip, err := runGitCommand(context.Background(), repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("resolve main after failure: %v", err)
	}
	if strings.TrimSpace(mainTip) != base {
		t.Fatalf("main moved from %q to %q", base, strings.TrimSpace(mainTip))
	}
}

func TestRunSeedPublicationPlanFailureReportsNoFruit(t *testing.T) {
	restoreSeeds(t)
	chdirToTempDir(t)

	repo := newSeedRepo(t)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		fmt.Sprintf("substrates:\n  seed-plan-failure:\n    path: %s\n    auth:\n      method: definitely-not-a-real-method\n", repo))

	var seedBranch string
	seedBuildFn = committedSeedBuild(t, repo, &seedBranch)
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}

	res, runErr := RunSeed(context.Background(), SeedExecution{
		Substrate:     "seed-plan-failure",
		Goal:          "make reviewable work",
		Verify:        []string{"true"},
		MaxIterations: 1,
		SessionID:     "seed-plan-failure-session",
	})
	if runErr == nil {
		t.Fatal("RunSeed succeeded despite publication plan resolution failure")
	}
	if !strings.Contains(runErr.Error(), "resolve Seed Fruit publication plan") ||
		!strings.Contains(runErr.Error(), "unknown auth method") {
		t.Fatalf("RunSeed error = %q, want explicit publication plan failure", runErr)
	}
	if res.Branch != "" || res.Commit != "" {
		t.Fatalf("Fruit identity = branch %q commit %q, want none after plan failure", res.Branch, res.Commit)
	}
	if seedBranch == "" || !branchExists(t, repo, seedBranch) {
		t.Fatalf("local Seed branch %q was not preserved", seedBranch)
	}
}
