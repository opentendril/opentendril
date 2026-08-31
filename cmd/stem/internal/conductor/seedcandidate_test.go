package conductor

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

const round16HelloVerify = "printf 'Hello from OpenTendril.\\n' | cmp -s - HELLO.md"

func round16HelloVerifyArgv() []string {
	return []string{"sh", "-c", round16HelloVerify}
}

func stubLocalStoma(t *testing.T) {
	t.Helper()
	orig := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = orig })
	runStomaCommandFn = func(ctx context.Context, execution StomaExecution, _ []terrarium.FilePayload, _ time.Duration) (StomaResult, error) {
		if len(execution.Command) == 0 {
			return StomaResult{}, fmt.Errorf("stoma command is required")
		}
		cmd := exec.CommandContext(ctx, execution.Command[0], execution.Command[1:]...)
		cmd.Dir = execution.Workspace
		out, err := cmd.CombinedOutput()
		result := StomaResult{Stdout: string(out), Stderr: ""}
		if err == nil {
			return result, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return StomaResult{}, err
	}
}

func TestTerrariumBindMountRunAsUserRootlessAndRootful(t *testing.T) {
	origUID := osGetuidFn
	origGID := osGetgidFn
	origRootless := dockerIsRootlessFn
	t.Cleanup(func() {
		osGetuidFn = origUID
		osGetgidFn = origGID
		dockerIsRootlessFn = origRootless
	})
	osGetuidFn = func() int { return 1001 }
	osGetgidFn = func() int { return 1002 }

	dockerIsRootlessFn = func() bool { return true }
	if got := terrariumBindMountRunAsUser(); got != "0:0" {
		t.Fatalf("rootless RunAsUser = %q, want 0:0", got)
	}
	dockerIsRootlessFn = func() bool { return false }
	if got := terrariumBindMountRunAsUser(); got != "1001:1002" {
		t.Fatalf("rootful RunAsUser = %q, want 1001:1002", got)
	}
}

func TestStomaBindMountRunAsUserMatchesSprout(t *testing.T) {
	origUID := osGetuidFn
	origGID := osGetgidFn
	origRootless := dockerIsRootlessFn
	origProvider := terrariumNewProviderFn
	origEnsure := ensureSproutImageFn
	t.Cleanup(func() {
		osGetuidFn = origUID
		osGetgidFn = origGID
		dockerIsRootlessFn = origRootless
		terrariumNewProviderFn = origProvider
		ensureSproutImageFn = origEnsure
	})
	osGetuidFn = func() int { return 1001 }
	osGetgidFn = func() int { return 1002 }
	ensureSproutImageFn = func(context.Context, string) error { return nil }

	workspace := t.TempDir()
	for _, rootless := range []bool{true, false} {
		dockerIsRootlessFn = func() bool { return rootless }
		want := terrariumBindMountRunAsUser()

		var sproutSpec, stomaSpec terrarium.TerrariumSpec
		terrariumNewProviderFn = func(ctx context.Context, name string, observers ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
			return &stubProvider{
				createFn: func(spec terrarium.TerrariumSpec) (terrarium.Terrarium, error) {
					if spec.Command != nil {
						sproutSpec = spec
					} else {
						stomaSpec = spec
					}
					return &stubTerrarium{}, nil
				},
			}, nil
		}

		if _, err := startTerrariumSession(context.Background(), "docker", "test-image", workspace, false, []string{"ls"}, nil, time.Minute); err != nil {
			t.Fatalf("startTerrariumSession rootless=%v: %v", rootless, err)
		}
		if _, err := runStomaCommand(context.Background(), StomaExecution{Workspace: workspace, Command: []string{"true"}}, nil, time.Second); err != nil {
			t.Fatalf("runStomaCommand rootless=%v: %v", rootless, err)
		}
		if sproutSpec.RunAsUser != want {
			t.Fatalf("sprout RunAsUser rootless=%v = %q, want %q", rootless, sproutSpec.RunAsUser, want)
		}
		if stomaSpec.RunAsUser != want {
			t.Fatalf("stoma RunAsUser rootless=%v = %q, want %q (same bind-mount identity as Sprout)", rootless, stomaSpec.RunAsUser, want)
		}
		if sproutSpec.RunAsUser != stomaSpec.RunAsUser {
			t.Fatalf("stoma and sprout identities diverged under rootless=%v: %q vs %q", rootless, stomaSpec.RunAsUser, sproutSpec.RunAsUser)
		}
	}
}

func TestSproutSystemPromptOmitsManagedRunWorkspaceHostPath(t *testing.T) {
	hostPath := "/home/tendril/.tendril/run-workspaces/ca0d0f46f7bdf5d26af23e9433890534"
	prompt := buildSproutSystemPrompt(hostPath, "", "")
	if strings.Contains(prompt, hostPath) {
		t.Fatalf("system prompt exposed the host RunWorkspace path:\n%s", prompt)
	}
	if strings.Contains(prompt, "run-workspaces") || strings.Contains(prompt, "/home/tendril") {
		t.Fatalf("system prompt leaked execution-location identity:\n%s", prompt)
	}
	if !strings.Contains(prompt, sproutLogicalWorkspaceRoot) {
		t.Fatalf("system prompt omitted the logical workspace root:\n%s", prompt)
	}
	if !strings.Contains(strings.ToLower(prompt), "repository-relative") {
		t.Fatalf("system prompt did not state that tool paths are repository-relative:\n%s", prompt)
	}
}

func TestSproutLoadsGenomeFromHostPathWithoutExposingIt(t *testing.T) {
	workspace := t.TempDir()
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	if err := os.MkdirAll(genomeDir, 0o755); err != nil {
		t.Fatalf("mkdir genome: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genomeDir, "notes.md"), []byte("Genome note from host workspace"), 0o644); err != nil {
		t.Fatalf("write genome: %v", err)
	}
	client := &fakeLLM{responses: []string{`{"final":"done"}`}}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile", Arguments: []ToolArgument{{Name: "path", Type: "string", Required: true}}}}}
	sprout, err := newSprout(context.Background(), workspace, workspace, "", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}
	if _, err := sprout.Run(context.Background(), "done"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(client.calls) == 0 || len(client.calls[0]) == 0 {
		t.Fatal("no system prompt was sent")
	}
	system := client.calls[0][0].Content
	if !strings.Contains(system, "Genome note from host workspace") {
		t.Fatalf("genome was not loaded from the host workspace path:\n%s", system)
	}
	if strings.Contains(system, workspace) {
		t.Fatalf("host workspace path leaked into the system prompt:\n%s", system)
	}
}

func TestRound16HelloPredicatePassesAgainstSeedCandidate(t *testing.T) {
	stubLocalStoma(t)
	repo := newSeedRepo(t)
	ctx := context.Background()
	seedBranch := "tendril/seed-hello"
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", seedBranch); err != nil {
		t.Fatalf("checkout seed branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte("Hello from OpenTendril.\n"), 0o644); err != nil {
		t.Fatalf("write HELLO.md: %v", err)
	}
	for _, args := range [][]string{{"add", "HELLO.md"}, {"commit", "-m", "hello"}, {"checkout", "main"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	report := runSeedVerify(ctx, repo, seedBranch, round16HelloVerifyArgv(), nil)
	if report.Err != nil {
		t.Fatalf("runSeedVerify: %v", report.Err)
	}
	if !report.Passed {
		t.Fatalf("Round 16 HELLO.md predicate failed against the seed candidate: exit=%v output=%q", report.ExitCode, report.Output)
	}
	if report.ExitCode == nil || *report.ExitCode != 0 || report.TimedOut {
		t.Fatalf("diagnostic facts = exit=%v timedOut=%v, want 0/false", report.ExitCode, report.TimedOut)
	}
}

func TestRound16HelloPredicateFailsWhenMissingOrWrong(t *testing.T) {
	stubLocalStoma(t)
	repo := newSeedRepo(t)
	ctx := context.Background()
	seedBranch := "tendril/seed-hello-fail"
	if _, err := runGitCommand(ctx, repo, "branch", seedBranch); err != nil {
		t.Fatalf("create branch: %v", err)
	}

	missing := runSeedVerify(ctx, repo, seedBranch, round16HelloVerifyArgv(), nil)
	if missing.Err != nil {
		t.Fatalf("missing HELLO.md: %v", missing.Err)
	}
	if missing.Passed {
		t.Fatal("missing HELLO.md was reported as passing")
	}
	if missing.ExitCode == nil || *missing.ExitCode == 0 {
		t.Fatalf("missing HELLO.md exit = %v, want non-zero", missing.ExitCode)
	}

	if _, err := runGitCommand(ctx, repo, "checkout", seedBranch); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte("wrong\n"), 0o644); err != nil {
		t.Fatalf("write wrong HELLO.md: %v", err)
	}
	for _, args := range [][]string{{"add", "HELLO.md"}, {"commit", "-m", "wrong"}, {"checkout", "main"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	wrong := runSeedVerify(ctx, repo, seedBranch, round16HelloVerifyArgv(), nil)
	if wrong.Err != nil {
		t.Fatalf("wrong HELLO.md: %v", wrong.Err)
	}
	if wrong.Passed {
		t.Fatal("wrong HELLO.md contents were reported as passing")
	}
	if wrong.ExitCode == nil || *wrong.ExitCode != 1 {
		t.Fatalf("wrong contents exit = %v, want 1 (cmp content mismatch)", wrong.ExitCode)
	}
}

func TestSeedCandidateRejectsNewlyCreatedExecutionLocationPath(t *testing.T) {
	ctx := context.Background()
	repo, workspace, seedBranch, start, leakCommit := seedCheckpointWithNewPath(t, filepath.FromSlash("~/tendril/.tendril/run-workspaces/ca0d0f46f7bdf5d26af23e9433890534/HELLO.md"), "Hello from OpenTendril.\n")

	if err := integrateSeedCheckpoint(ctx, workspace, seedBranch, leakCommit, start); err == nil {
		t.Fatal("integrateSeedCheckpoint accepted a newly created execution-location path")
	} else if !strings.Contains(err.Error(), "execution-location leakage") {
		t.Fatalf("error = %q, want path-integrity failure", err)
	}
	if localBranchExists(repo, seedBranch) {
		t.Fatal("rejected candidate advanced the Seed checkpoint")
	}
}

func TestSeedCandidateRejectsHostRunWorkspaceProjection(t *testing.T) {
	ctx := context.Background()
	repo, workspace, seedBranch, start, _ := seedCheckpointWithNewPath(t, "keep-relative.txt", "ok\n")
	projected := strings.TrimPrefix(filepath.ToSlash(workspace.Path), "/") + "/HELLO.md"
	full := filepath.Join(workspace.Path, filepath.FromSlash(projected))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir projected: %v", err)
	}
	if err := os.WriteFile(full, []byte("leaked\n"), 0o644); err != nil {
		t.Fatalf("write projected: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "projected host path"}} {
		if _, err := runGitCommand(ctx, workspace.Path, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	leakCommit, err := runGitCommand(ctx, workspace.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("projected commit: %v", err)
	}
	leakCommit = strings.TrimSpace(leakCommit)

	if err := integrateSeedCheckpoint(ctx, workspace, seedBranch, leakCommit, start); err == nil {
		t.Fatal("integrateSeedCheckpoint accepted a host RunWorkspace path projected into Git")
	} else if !strings.Contains(err.Error(), "execution-location leakage") {
		t.Fatalf("error = %q, want path-integrity failure", err)
	}
	if localBranchExists(repo, seedBranch) {
		t.Fatal("rejected projected path advanced the Seed checkpoint")
	}
}

func TestSeedCandidateAllowsOrdinaryRepositoryRelativeFile(t *testing.T) {
	ctx := context.Background()
	repo, workspace, seedBranch, start, commit := seedCheckpointWithNewPath(t, "HELLO.md", "Hello from OpenTendril.\n")
	if err := integrateSeedCheckpoint(ctx, workspace, seedBranch, commit, start); err != nil {
		t.Fatalf("ordinary HELLO.md was refused: %v", err)
	}
	tip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("seed branch missing: %v", err)
	}
	if strings.TrimSpace(tip) != commit {
		t.Fatalf("seed tip = %q, want %q", strings.TrimSpace(tip), commit)
	}
}

func TestSeedCandidatePreservesIntentionalExistingPath(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "seed@example.com"}, {"config", "user.name", "Seed Tester"}, {"checkout", "-b", "main"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	existing := filepath.FromSlash("~/legacy-notes.md")
	if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(existing)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, existing), []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "base with intentional path"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	start, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	start = strings.TrimSpace(start)

	isolation := "sprout/task-existing"
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", isolation); err != nil {
		t.Fatalf("isolation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, existing), []byte("legacy updated\n"), 0o644); err != nil {
		t.Fatalf("update existing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte("Hello from OpenTendril.\n"), 0o644); err != nil {
		t.Fatalf("write HELLO.md: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "update existing and add hello"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	commit, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	commit = strings.TrimSpace(commit)
	if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if _, err := runGitCommand(ctx, repo, "worktree", "add", linked, isolation); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	RegisterOwnedRef(OwnedRef{Repository: repo, Branch: isolation, Purpose: PurposeSproutIsolation, Base: start, RunID: "run-existing"})
	ws := RunWorkspace{Path: linked, Repository: repo, Branch: isolation, BaseCommit: start, RunID: "run-existing"}
	if err := integrateSeedCheckpoint(ctx, ws, "tendril/seed-existing", commit, start); err != nil {
		t.Fatalf("intentional existing path was refused: %v", err)
	}
}

func TestSeedVerificationDiagnosticsDistinguishOutcomes(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	seedBuildFn = fakeBuild(new([]string))

	code1 := 1
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "cmp mismatch", Passed: false, ExitCode: &code1}
	}
	failed, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"false"}, MaxIterations: 1,
		SessionID: "seed-diag-predicate",
	})
	if err != nil {
		t.Fatalf("predicate RunSeed: %v", err)
	}
	if len(failed.VerificationDiagnostics) != 1 {
		t.Fatalf("predicate diagnostics = %+v", failed.VerificationDiagnostics)
	}
	if failed.VerificationDiagnostics[0].Outcome != core.SeedVerificationOutcomePredicateFailed {
		t.Fatalf("predicate outcome = %q", failed.VerificationDiagnostics[0].Outcome)
	}
	if failed.VerificationDiagnostics[0].ExitCode == nil || *failed.VerificationDiagnostics[0].ExitCode != 1 {
		t.Fatalf("predicate exit = %v", failed.VerificationDiagnostics[0].ExitCode)
	}
	if failed.VerificationDiagnostics[0].TimedOut {
		t.Fatal("predicate failure was marked timed out")
	}

	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{TimedOut: true, Passed: false}
	}
	timedOut, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"false"}, MaxIterations: 1,
		SessionID: "seed-diag-timeout",
	})
	if err != nil {
		t.Fatalf("timeout RunSeed: %v", err)
	}
	if timedOut.VerificationDiagnostics[0].Outcome != core.SeedVerificationOutcomeInfrastructureFailed || !timedOut.VerificationDiagnostics[0].TimedOut {
		t.Fatalf("timeout diagnostic = %+v", timedOut.VerificationDiagnostics[0])
	}

	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Err: fmt.Errorf("start stoma terrarium: %s", "/home/operator/.tendril/run-workspaces/secret")}
	}
	infra, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"true"}, MaxIterations: 1,
		SessionID: "seed-diag-infra",
	})
	if err != nil {
		t.Fatalf("infra RunSeed: %v", err)
	}
	if infra.Status != SeedStatusWithered {
		t.Fatalf("infra status = %q, want withered", infra.Status)
	}
	diag := infra.VerificationDiagnostics[0]
	if diag.Outcome != core.SeedVerificationOutcomeInfrastructureFailed || diag.TimedOut || diag.ExitCode != nil {
		t.Fatalf("infra diagnostic = %+v", diag)
	}
	if strings.Contains(diag.Message, "/home/operator") || strings.Contains(diag.Message, "run-workspaces") {
		t.Fatalf("infrastructure diagnostic leaked a host path: %q", diag.Message)
	}
}

func TestFailedVerificationPreservesSeedCheckpointForNextIteration(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)

	var starts []string
	var seedBranch string
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, _ string) (SproutRunReport, error) {
		starts = append(starts, orch.SeedStartRevision)
		seedBranch = orch.SubstrateBranch
		if !localBranchExists(repo, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, repo, "branch", orch.SubstrateBranch, orch.SeedStartRevision); err != nil {
				return SproutRunReport{}, err
			}
		}
		if _, err := runGitCommand(ctx, repo, "checkout", orch.SubstrateBranch); err != nil {
			return SproutRunReport{}, err
		}
		name := fmt.Sprintf("fruit-%d.txt", len(starts))
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644); err != nil {
			return SproutRunReport{}, err
		}
		for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", name}, {"checkout", "main"}} {
			if _, err := runGitCommand(ctx, repo, args...); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		code := 1
		return seedVerifyReport{Output: "still failing", Passed: false, ExitCode: &code}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "g", Verify: []string{"false"}, MaxIterations: 2,
		SessionID: "seed-preserve-checkpoint",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q", res.Status)
	}
	if len(starts) != 2 {
		t.Fatalf("starts = %v", starts)
	}
	if starts[0] != base {
		t.Fatalf("first start = %q, want base %q", starts[0], base)
	}
	firstTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch+"^")
	if err != nil {
		t.Fatalf("first tip: %v", err)
	}
	if starts[1] != strings.TrimSpace(firstTip) {
		t.Fatalf("second iteration started at %q, want accumulated candidate %q", starts[1], strings.TrimSpace(firstTip))
	}
}

func TestVerifierWritesStayInDisposableWorktree(t *testing.T) {
	orig := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = orig })
	runStomaCommandFn = func(_ context.Context, execution StomaExecution, _ []terrarium.FilePayload, _ time.Duration) (StomaResult, error) {
		if err := os.WriteFile(filepath.Join(execution.Workspace, "MUTATED.txt"), []byte("verifier write\n"), 0o644); err != nil {
			return StomaResult{}, err
		}
		code := 1
		return StomaResult{ExitCode: code, Stderr: "predicate failed"}, nil
	}

	repo := newSeedRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-mutation"
	if _, err := runGitCommand(ctx, repo, "branch", seedBranch); err != nil {
		t.Fatalf("branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte("Hello from OpenTendril.\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", seedBranch); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	for _, args := range [][]string{{"add", "HELLO.md"}, {"commit", "-m", "hello"}, {"checkout", "main"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	checkpoint, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	checkpoint = strings.TrimSpace(checkpoint)

	report := runSeedVerify(ctx, repo, seedBranch, []string{"false"}, nil)
	if report.Err != nil {
		t.Fatalf("verify: %v", report.Err)
	}
	if report.Passed {
		t.Fatal("forced predicate failure passed")
	}
	after, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if strings.TrimSpace(after) != checkpoint {
		t.Fatalf("verifier mutated the Seed checkpoint from %s to %s", checkpoint, strings.TrimSpace(after))
	}
	mainTip, err := runGitCommand(ctx, repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("main: %v", err)
	}
	if strings.TrimSpace(mainTip) != base {
		t.Fatalf("default branch moved from %s to %s", base, strings.TrimSpace(mainTip))
	}
	if _, err := os.Stat(filepath.Join(repo, "MUTATED.txt")); !os.IsNotExist(err) {
		t.Fatal("verifier write escaped into the Substrate checkout")
	}
}

func TestConcurrentSeedsDoNotVerifyEachOthersCandidate(t *testing.T) {
	stubLocalStoma(t)
	repo := newSeedRepo(t)
	ctx := context.Background()

	writeHelloBranch := func(branch, contents string) {
		t.Helper()
		if _, err := runGitCommand(ctx, repo, "checkout", "-b", branch, "main"); err != nil {
			t.Fatalf("checkout %s: %v", branch, err)
		}
		if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", branch, err)
		}
		for _, args := range [][]string{{"add", "HELLO.md"}, {"commit", "-m", branch}, {"checkout", "main"}} {
			if _, err := runGitCommand(ctx, repo, args...); err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
		}
	}
	writeHelloBranch("tendril/seed-a", "Hello from OpenTendril.\n")
	writeHelloBranch("tendril/seed-b", "other seed\n")

	var wg sync.WaitGroup
	errA := make(chan seedVerifyReport, 1)
	errB := make(chan seedVerifyReport, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errA <- runSeedVerify(ctx, repo, "tendril/seed-a", round16HelloVerifyArgv(), nil)
	}()
	go func() {
		defer wg.Done()
		errB <- runSeedVerify(ctx, repo, "tendril/seed-b", round16HelloVerifyArgv(), nil)
	}()
	wg.Wait()
	a := <-errA
	b := <-errB
	if a.Err != nil || b.Err != nil {
		t.Fatalf("concurrent verify errors: %v / %v", a.Err, b.Err)
	}
	if !a.Passed {
		t.Fatalf("seed A should pass its own candidate: %+v", a)
	}
	if b.Passed {
		t.Fatal("seed B passed seed A's HELLO.md predicate; candidates were shared")
	}
}

func TestPassingVerificationMatchesManagedAPIFruitPaths(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)
	restoreSeeds(t)
	repo := newSeedRepo(t)

	keyPath := writeSeedTestAppKey(t)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  seed-api-identity:\n    url: "+repo+"\n    branch: main\n    checkout:\n      mode: managed\n    commit: api\n    auth:\n      method: app\n      appId: \"1234\"\n      privateKeyPath: "+keyPath+"\n")

	origMaterialize := materializeManagedCheckoutFn
	t.Cleanup(func() { materializeManagedCheckoutFn = origMaterialize })
	materializeManagedCheckoutFn = func(name, dest, url, branch string, _ ResolvedCredential, _ []string) error {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		_, err := runGitCommand(context.Background(), filepath.Dir(dest), "clone", "-q", repo, dest)
		return err
	}
	dest := filepath.Join(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT"), "seed-api-identity")
	if err := materializeManagedCheckoutFn("seed-api-identity", dest, repo, "main", ResolvedCredential{}, nil); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	publishedOID := "feedfacefeedfacefeedfacefeedfacefeedface"
	fake := startAPIFruitFake(t, 201, publishedOID)
	origStart := startTerrariumSessionFn
	t.Cleanup(func() { startTerrariumSessionFn = origStart })
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return stubCountingSession(t), nil
	}
	origSprout := newSproutFn
	t.Cleanup(func() { newSproutFn = origSprout })
	newSproutFn = func(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string, sessionID string) (sproutRunner, error) {
		return &testSproutRunner{run: func(ctx context.Context, taskPrompt string) (sproutResult, error) {
			if err := os.WriteFile(filepath.Join(workspace, "HELLO.md"), []byte("Hello from OpenTendril.\n"), 0o644); err != nil {
				return sproutResult{}, err
			}
			return sproutResult{Response: "wrote hello", WroteWorkspace: true}, nil
		}}, nil
	}
	origPreflight := runSproutPreflightChecksFn
	t.Cleanup(func() { runSproutPreflightChecksFn = origPreflight })
	runSproutPreflightChecksFn = func(context.Context) error { return nil }
	origEnsure := ensureSproutImageFn
	t.Cleanup(func() { ensureSproutImageFn = origEnsure })
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	stubLocalStoma(t)

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: "seed-api-identity", Goal: "Create HELLO.md", Verify: round16HelloVerifyArgv(), MaxIterations: 1,
		SessionID: "tendril-seed-api-identity",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusSatisfied {
		t.Fatalf("status = %q logs=%s", res.Status, res.Logs)
	}
	if res.Commit != publishedOID {
		t.Fatalf("published OID = %q, want %q (GitHub-created OID may differ from the local checkpoint)", res.Commit, publishedOID)
	}
	localTip, err := runGitCommand(context.Background(), dest, "rev-parse", res.Branch)
	if err != nil {
		t.Fatalf("local checkpoint: %v", err)
	}
	localTip = strings.TrimSpace(localTip)
	if localTip == publishedOID {
		t.Fatal("expected the GitHub-created OID to differ from the local checkpoint OID")
	}
	if !strings.Contains(fake.graphQLBody, base64.StdEncoding.EncodeToString([]byte("Hello from OpenTendril.\n"))) {
		t.Fatalf("managed API Fruit did not receive the verified HELLO.md contents: %s", fake.graphQLBody)
	}
	if strings.Contains(fake.graphQLBody, "run-workspaces") || strings.Contains(fake.graphQLBody, "~/") {
		t.Fatalf("managed API Fruit included execution-location leakage: %s", fake.graphQLBody)
	}
	mainTip, err := runGitCommand(context.Background(), dest, "rev-parse", "main")
	if err != nil {
		t.Fatalf("main: %v", err)
	}
	base, err := runGitCommand(context.Background(), dest, "rev-parse", res.Branch+"^")
	if err != nil {
		t.Fatalf("parent: %v", err)
	}
	if strings.TrimSpace(mainTip) != strings.TrimSpace(base) {
		t.Fatalf("default branch moved: main=%s parent=%s", strings.TrimSpace(mainTip), strings.TrimSpace(base))
	}
}

func seedCheckpointWithNewPath(t *testing.T, relPath, contents string) (repo string, workspace RunWorkspace, seedBranch, start, commit string) {
	t.Helper()
	ctx := context.Background()
	repo = t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "seed@example.com"}, {"config", "user.name", "Seed Tester"}, {"checkout", "-b", "main"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "base"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	startBytes, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	start = strings.TrimSpace(startBytes)

	isolation := "sprout/task-seedpath"
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", isolation); err != nil {
		t.Fatalf("isolation: %v", err)
	}
	commit = commitPathOnBranch(t, repo, isolation, relPath, contents)
	if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if _, err := runGitCommand(ctx, repo, "worktree", "add", linked, isolation); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	RegisterOwnedRef(OwnedRef{Repository: repo, Branch: isolation, Purpose: PurposeSproutIsolation, Base: start, RunID: "run-seedpath"})
	workspace = RunWorkspace{Path: linked, Repository: repo, Branch: isolation, BaseCommit: start, RunID: "run-seedpath"}
	seedBranch = "tendril/seed-path"
	return repo, workspace, seedBranch, start, commit
}

func commitPathOnBranch(t *testing.T, repo, branch, relPath, contents string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := runGitCommand(ctx, repo, "checkout", branch); err != nil {
		t.Fatalf("checkout %s: %v", branch, err)
	}
	full := filepath.Join(repo, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "candidate path"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	commit, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(commit)
}

func TestSeedCandidateExecutionLeakReasonCoversBoundaryShapes(t *testing.T) {
	host := "/home/tendril/.tendril/run-workspaces/abc123"
	cases := []struct {
		path string
		want bool
	}{
		{"HELLO.md", false},
		{"docs/HELLO.md", false},
		{"~/tendril/.tendril/run-workspaces/abc123/HELLO.md", true},
		{".tendril/run-workspaces/abc123/HELLO.md", true},
		{"/app/HELLO.md", true},
		{"/workspace/HELLO.md", true},
		{"home/tendril/.tendril/run-workspaces/abc123/HELLO.md", true},
	}
	for _, tc := range cases {
		reason := seedCandidateExecutionLeakReason(tc.path, host)
		if tc.want && reason == "" {
			t.Errorf("%q was not classified as execution-location leakage", tc.path)
		}
		if !tc.want && reason != "" {
			t.Errorf("%q was classified as leakage (%s)", tc.path, reason)
		}
	}
}
