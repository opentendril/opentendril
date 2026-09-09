package conductor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
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

type round19WriteSession struct {
	fakeSession
	workspace string
}

func (s *round19WriteSession) Call(_ context.Context, call ToolCall) (ToolResponse, error) {
	s.fakeSession.calls = append(s.fakeSession.calls, call)
	if call.Tool == "writeFile" {
		path, _ := call.Arguments["path"].(string)
		content, _ := call.Arguments["content"].(string)
		if err := os.WriteFile(filepath.Join(s.workspace, path), []byte(content), 0o644); err != nil {
			return ToolResponse{}, err
		}
	} else if call.Tool == "readFile" {
		path, _ := call.Arguments["path"].(string)
		content, err := os.ReadFile(filepath.Join(s.workspace, path))
		if err != nil {
			return ToolResponse{}, err
		}
		return ToolResponse{Status: "success", Output: map[string]any{"path": path, "content": string(content)}}, nil
	}
	return ToolResponse{Status: "success", Output: map[string]any{"tool": call.Tool}}, nil
}

func (s *round19WriteSession) Logs() string { return "round 19 fake terrarium" }

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
	lowerPrompt := strings.ToLower(prompt)
	for _, want := range []string{
		"relative to the repository root",
		"do not prefix tool paths with `repository/`",
		"host runworkspace and other execution-location paths are never exposed or valid tool paths",
	} {
		if !strings.Contains(lowerPrompt, want) {
			t.Fatalf("system prompt omitted path guidance %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "use `HELLO.md`, not `repository/HELLO.md`") {
		t.Fatalf("system prompt omitted the concrete root-file example:\n%s", prompt)
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
	seedTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("seed tip: %v", err)
	}

	report := runSeedVerify(ctx, repo, strings.TrimSpace(seedTip), round16HelloVerifyArgv(), nil)
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

func TestRound19SeedRetryCarriesCandidateEvidenceAndRejectsProviderProse(t *testing.T) {
	restoreSeeds(t)
	stubLocalStoma(t)
	repo := newSeedRepo(t)
	var prompts []string
	var iteration int
	nearMalformedWrite := `{"name":"writeFile","parameters={"path":"HELLO.md","content":"Hello from OpenTendril."}}`
	unknownProviderRun := "explanation text\n{\"name\":\"runCommand\",\"parameters\":{\"command\":\"printf ...\"}}"

	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		iteration++
		if !localBranchExists(repo, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, repo, "branch", orch.SubstrateBranch, orch.SeedStartRevision); err != nil {
				return SproutRunReport{}, err
			}
		}
		if _, err := runGitCommand(ctx, repo, "checkout", orch.SubstrateBranch); err != nil {
			return SproutRunReport{}, err
		}
		defer func() { _, _ = runGitCommand(ctx, repo, "checkout", "main") }()

		var client *nativeFakeLLM
		if iteration == 1 {
			client = &nativeFakeLLM{
				fakeLLM: fakeLLM{responses: []string{
					`{"tool":"writeFile","arguments":{"path":"HELLO.md","content":"Hello from OpenTendril."}}`,
					`{"final":"wrote candidate"}`,
				}},
				nativeResponses: []llm.Result{{Text: nearMalformedWrite}},
			}
		} else {
			client = &nativeFakeLLM{
				fakeLLM:         fakeLLM{response: unknownProviderRun},
				nativeResponses: []llm.Result{{Text: unknownProviderRun}},
			}
		}
		session := &round19WriteSession{
			fakeSession: fakeSession{tools: []ToolDefinition{{Name: "writeFile"}}},
			workspace:   repo,
		}
		sprout, err := newSprout(ctx, repo, repo, "workspace-Sprout", client, session, nil, "round-19", "round-19")
		if err != nil {
			return SproutRunReport{}, err
		}
		result, runErr := sprout.Run(ctx, prompt)
		if runErr != nil {
			return SproutRunReport{}, runErr
		}
		if !result.WroteWorkspace {
			return SproutRunReport{Outcome: SproutOutcomeNoChanges}, nil
		}
		if _, err := runGitCommand(ctx, repo, "add", "HELLO.md"); err != nil {
			return SproutRunReport{}, err
		}
		if _, err := runGitCommand(ctx, repo, "commit", "-m", "round 19 candidate"); err != nil {
			return SproutRunReport{}, err
		}
		checkpoint, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
		if err != nil {
			return SproutRunReport{}, err
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: strings.TrimSpace(checkpoint)}, nil
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repo,
		Goal:          "create HELLO.md",
		Verify:        round16HelloVerifyArgv(),
		MaxIterations: 2,
		SessionID:     "round-19-seed-convergence",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered after the bounded provider correction failure", res.Status)
	}
	if res.Iterations != 2 || len(prompts) != 2 {
		t.Fatalf("iterations/prompts = %d/%d, want 2/2", res.Iterations, len(prompts))
	}
	if res.Branch != "" || res.Commit != "" {
		t.Fatalf("failed Fruit identity = branch %q commit %q, want none for a withered Seed", res.Branch, res.Commit)
	}
	if strings.Contains(prompts[0], seedCandidateDiffHeading) {
		t.Fatalf("initial prompt included candidate evidence:\n%s", prompts[0])
	}
	for _, want := range []string{
		"Verification failed: command exited 1.",
		seedCandidateDiffHeading,
		"HELLO.md",
		"\\ No newline at end of file",
		"This is deterministic Stem-provided candidate evidence.",
	} {
		if !strings.Contains(prompts[1], want) {
			t.Fatalf("retry prompt omitted %q:\n%s", want, prompts[1])
		}
	}
	if !strings.Contains(res.Logs, "sprout withered") || !strings.Contains(res.Logs, "model reply attempted a tool call") {
		t.Fatalf("malformed/provider prose did not remain a bounded Sprout failure:\n%s", res.Logs)
	}
	if len(res.VerificationDiagnostics) != 1 || res.VerificationDiagnostics[0].ExitCode == nil || *res.VerificationDiagnostics[0].ExitCode != 1 {
		t.Fatalf("verification diagnostics = %+v, want the first silent predicate failure", res.VerificationDiagnostics)
	}
}

func TestRunSeedNoChangeVerifiesBaseCandidate(t *testing.T) {
	restoreSeeds(t)
	stubLocalStoma(t)
	repo := newSeedRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	var verifiedCandidate string

	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		return SproutRunReport{Outcome: SproutOutcomeNoChanges}, nil
	}
	seedVerifyFn = func(ctx context.Context, sourcePath, candidate string, verify, egress []string) seedVerifyReport {
		verifiedCandidate = candidate
		return runSeedVerify(ctx, sourcePath, candidate, verify, egress)
	}

	res, err := RunSeed(ctx, SeedExecution{
		Substrate: repo, Goal: "create HELLO.md", Verify: round16HelloVerifyArgv(), MaxIterations: 1,
		SessionID: "seed-no-change-base",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted after a normal predicate failure", res.Status)
	}
	if res.Branch != "" {
		t.Fatalf("no-change Seed created a review branch %q", res.Branch)
	}
	if verifiedCandidate != base {
		t.Fatalf("verified candidate = %q, want base %q", verifiedCandidate, base)
	}
	if len(res.VerificationDiagnostics) != 1 {
		t.Fatalf("verification diagnostics = %+v, want one diagnostic", res.VerificationDiagnostics)
	}
	diagnostic := res.VerificationDiagnostics[0]
	if diagnostic.Outcome != core.SeedVerificationOutcomePredicateFailed {
		t.Fatalf("verification outcome = %q, want predicate-failed", diagnostic.Outcome)
	}
	if diagnostic.ExitCode == nil || *diagnostic.ExitCode != 2 {
		t.Fatalf("verification exit = %v, want 2 for a missing HELLO.md", diagnostic.ExitCode)
	}
}

func TestRunSeedInvalidCheckpointIsInfrastructureFailure(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var verified bool
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: "not-a-commit"}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		verified = true
		return seedVerifyReport{}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "create HELLO.md", Verify: round16HelloVerifyArgv(), MaxIterations: 1,
		SessionID: "seed-invalid-checkpoint",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	if verified {
		t.Fatal("verification ran for an invalid checkpoint")
	}
	if len(res.VerificationDiagnostics) != 1 || res.VerificationDiagnostics[0].Outcome != core.SeedVerificationOutcomeInfrastructureFailed {
		t.Fatalf("verification diagnostics = %+v, want one infrastructure-failed diagnostic", res.VerificationDiagnostics)
	}
}

func TestRunSeedNoChangeVerifiesAccumulatedCandidate(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var firstCheckpoint string
	var verifiedCandidates []string
	var prompts []string

	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		if firstCheckpoint != "" {
			return SproutRunReport{Outcome: SproutOutcomeNoChanges}, nil
		}
		if _, err := runGitCommand(ctx, repo, "branch", orch.SubstrateBranch, orch.SeedStartRevision); err != nil {
			return SproutRunReport{}, err
		}
		if _, err := runGitCommand(ctx, repo, "checkout", "--detach", orch.SeedStartRevision); err != nil {
			return SproutRunReport{}, err
		}
		if err := os.WriteFile(filepath.Join(repo, "accumulated.txt"), []byte("first iteration\n"), 0o644); err != nil {
			return SproutRunReport{}, err
		}
		for _, args := range [][]string{{"add", "accumulated.txt"}, {"commit", "-m", "accumulated work"}} {
			if _, err := runGitCommand(ctx, repo, args...); err != nil {
				return SproutRunReport{}, err
			}
		}
		checkpoint, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
		if err != nil {
			return SproutRunReport{}, err
		}
		firstCheckpoint = strings.TrimSpace(checkpoint)
		if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
			return SproutRunReport{}, err
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: firstCheckpoint}, nil
	}
	seedVerifyFn = func(_ context.Context, _ string, candidate string, _ []string, _ []string) seedVerifyReport {
		verifiedCandidates = append(verifiedCandidates, candidate)
		code := 1
		return seedVerifyReport{ExitCode: &code}
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "accumulate work", Verify: []string{"false"}, MaxIterations: 2,
		SessionID: "seed-no-change-accumulated",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted", res.Status)
	}
	if len(verifiedCandidates) != 2 {
		t.Fatalf("verified candidates = %v, want one per iteration", verifiedCandidates)
	}
	if verifiedCandidates[0] != firstCheckpoint || verifiedCandidates[1] != firstCheckpoint {
		t.Fatalf("verified candidates = %v, want accumulated candidate %q for both iterations", verifiedCandidates, firstCheckpoint)
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want one per iteration", len(prompts))
	}
	if strings.Contains(prompts[0], seedCandidateDiffHeading) {
		t.Fatalf("initial prompt included candidate evidence: %q", prompts[0])
	}
	for _, want := range []string{
		seedCandidateDiffHeading,
		"accumulated.txt",
		"This is deterministic Stem-provided candidate evidence.",
	} {
		if !strings.Contains(prompts[1], want) {
			t.Fatalf("retry prompt omitted candidate evidence %q:\n%s", want, prompts[1])
		}
	}
}

func TestRunSeedNoChangeCanSatisfyStartingCandidate(t *testing.T) {
	restoreSeeds(t)
	stubLocalStoma(t)
	repo := newSeedRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte("Hello from OpenTendril.\n"), 0o644); err != nil {
		t.Fatalf("write HELLO.md: %v", err)
	}
	for _, args := range [][]string{{"add", "HELLO.md"}, {"commit", "-m", "satisfying base candidate"}} {
		if _, err := runGitCommand(context.Background(), repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		return SproutRunReport{Outcome: SproutOutcomeNoChanges}, nil
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "create HELLO.md", Verify: round16HelloVerifyArgv(), MaxIterations: 1,
		SessionID: "seed-no-change-satisfied",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusSatisfied {
		t.Fatalf("status = %q, want satisfied", res.Status)
	}
	if len(res.VerificationDiagnostics) != 1 || res.VerificationDiagnostics[0].Outcome != core.SeedVerificationOutcomePassed {
		t.Fatalf("verification diagnostics = %+v, want one passed diagnostic", res.VerificationDiagnostics)
	}
}

// TestSeedVerificationUsesRunWorkspaceRootForDockerMount checks the host-side
// boundary that a real Docker Stoma consumes. The injected Stoma seam only
// observes the prepared worktree; TestRound16HelloPredicateThroughRealTerrarium
// below is the governed container regression.
func TestSeedVerificationUsesRunWorkspaceRootForDockerMount(t *testing.T) {
	repo := newSeedRepo(t)
	ctx := context.Background()
	seedBranch := "tendril/seed-visible"
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
	seedTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("seed tip: %v", err)
	}
	seedTip = strings.TrimSpace(seedTip)

	originalStoma := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = originalStoma })
	var mountedWorkspace, mountedTip string
	runStomaCommandFn = func(ctx context.Context, execution StomaExecution, _ []terrarium.FilePayload, _ time.Duration) (StomaResult, error) {
		mountedWorkspace = execution.Workspace
		tip, tipErr := runGitCommand(ctx, execution.Workspace, "rev-parse", "HEAD")
		if tipErr != nil {
			return StomaResult{}, tipErr
		}
		mountedTip = strings.TrimSpace(tip)
		return StomaResult{ExitCode: 0}, nil
	}

	report := runSeedVerify(ctx, repo, seedTip, round16HelloVerifyArgv(), nil)
	if report.Err != nil {
		t.Fatalf("runSeedVerify: %v", report.Err)
	}
	if mountedWorkspace == "" {
		t.Fatal("Stoma did not receive a workspace")
	}
	if !pathIsUnder(mountedWorkspace, runWorkspaceRoot()) {
		t.Fatalf("Stoma workspace = %q, want a path below the Stem run-workspace root %q", mountedWorkspace, runWorkspaceRoot())
	}
	if strings.HasPrefix(mountedWorkspace, filepath.Join(os.TempDir(), "opentendril-terrarium-")) {
		t.Fatalf("Stoma workspace still uses the private temporary namespace: %q", mountedWorkspace)
	}
	if mountedTip != seedTip {
		t.Fatalf("Stoma observed candidate %q, want Seed tip %q", mountedTip, seedTip)
	}
	if _, err := os.Stat(mountedWorkspace); !os.IsNotExist(err) {
		t.Fatalf("verification worktree still exists after runSeedVerify: stat error = %v", err)
	}
}

func TestSeedVerificationCleansUpAfterStomaFailure(t *testing.T) {
	repo := newSeedRepo(t)
	ctx := context.Background()
	seedBranch := "tendril/seed-cleanup-error"
	if _, err := runGitCommand(ctx, repo, "branch", seedBranch); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	seedTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("seed tip: %v", err)
	}
	seedTip = strings.TrimSpace(seedTip)

	originalStoma := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = originalStoma })
	var mountedWorkspace string
	runStomaCommandFn = func(_ context.Context, execution StomaExecution, _ []terrarium.FilePayload, _ time.Duration) (StomaResult, error) {
		mountedWorkspace = execution.Workspace
		return StomaResult{}, fmt.Errorf("stoma failed")
	}

	report := runSeedVerify(ctx, repo, seedTip, []string{"false"}, nil)
	if report.Err == nil || report.Passed {
		t.Fatalf("runSeedVerify report = %+v, want an infrastructure error", report)
	}
	if mountedWorkspace == "" {
		t.Fatal("Stoma did not receive a workspace")
	}
	if _, err := os.Stat(mountedWorkspace); !os.IsNotExist(err) {
		t.Fatalf("verification worktree still exists after Stoma failure: stat error = %v", err)
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
	seedTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("seed tip: %v", err)
	}
	seedTip = strings.TrimSpace(seedTip)

	missing := runSeedVerify(ctx, repo, seedTip, round16HelloVerifyArgv(), nil)
	if missing.Err != nil {
		t.Fatalf("missing HELLO.md: %v", missing.Err)
	}
	if missing.Passed {
		t.Fatal("missing HELLO.md was reported as passing")
	}
	if missing.ExitCode == nil || *missing.ExitCode != 2 {
		t.Fatalf("missing HELLO.md exit = %v, want 2 (cmp could not open the file)", missing.ExitCode)
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
	seedTip, err = runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("wrong seed tip: %v", err)
	}
	wrong := runSeedVerify(ctx, repo, strings.TrimSpace(seedTip), round16HelloVerifyArgv(), nil)
	if wrong.Err != nil {
		t.Fatalf("wrong HELLO.md: %v", wrong.Err)
	}
	if wrong.Passed {
		t.Fatal("wrong HELLO.md contents were reported as passing")
	}
	if wrong.ExitCode == nil || *wrong.ExitCode != 1 {
		t.Fatalf("wrong contents exit = %v, want 1 (cmp content mismatch)", wrong.ExitCode)
	}

	if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte("Hello from OpenTendril."), 0o644); err != nil {
		t.Fatalf("write no-newline HELLO.md: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "HELLO.md"); err != nil {
		t.Fatalf("stage no-newline HELLO.md: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "no trailing newline"); err != nil {
		t.Fatalf("commit no-newline HELLO.md: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	seedTip, err = runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("no-newline seed tip: %v", err)
	}
	noNewline := runSeedVerify(ctx, repo, strings.TrimSpace(seedTip), round16HelloVerifyArgv(), nil)
	if noNewline.Err != nil {
		t.Fatalf("no trailing newline HELLO.md: %v", noNewline.Err)
	}
	if noNewline.Passed {
		t.Fatal("HELLO.md without a trailing newline was reported as passing")
	}
	if noNewline.ExitCode == nil || *noNewline.ExitCode != 1 {
		t.Fatalf("no trailing newline exit = %v, want 1 (cmp content mismatch)", noNewline.ExitCode)
	}
}

func TestRound16HelloPredicateThroughRealTerrarium(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("Docker CLI is unavailable: %v", err)
	}
	if os.Getenv("DOCKER_HOST") == "" {
		// TestMain isolates HOME, which hides the user's Docker context. Keep
		// rootless Docker available when its standard per-user socket exists.
		if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
			socket := filepath.Join(runtimeDir, "docker.sock")
			if _, err := os.Stat(socket); err == nil {
				t.Setenv("DOCKER_HOST", "unix://"+socket)
			}
		}
	}
	if output, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("Docker daemon is unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")

	tests := []struct {
		name    string
		content string
		write   bool
		want    int
	}{
		{name: "exact content", content: "Hello from OpenTendril.\n", write: true, want: 0},
		{name: "no trailing newline", content: "Hello from OpenTendril.", write: true, want: 1},
		{name: "wrong contents", content: "wrong\n", write: true, want: 1},
		{name: "missing file", want: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSeedRepo(t)
			ctx := context.Background()
			seedBranch := "tendril/seed-real-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := runGitCommand(ctx, repo, "checkout", "-b", seedBranch); err != nil {
				t.Fatalf("checkout seed branch: %v", err)
			}
			if tc.write {
				if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte(tc.content), 0o644); err != nil {
					t.Fatalf("write HELLO.md: %v", err)
				}
				for _, args := range [][]string{{"add", "HELLO.md"}, {"commit", "-m", tc.name}} {
					if _, err := runGitCommand(ctx, repo, args...); err != nil {
						t.Fatalf("git %v: %v", args, err)
					}
				}
			}
			if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
				t.Fatalf("checkout main: %v", err)
			}
			seedTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
			if err != nil {
				t.Fatalf("seed tip: %v", err)
			}

			report := runSeedVerify(ctx, repo, strings.TrimSpace(seedTip), round16HelloVerifyArgv(), nil)
			if report.Err != nil {
				t.Fatalf("runSeedVerify: %v", report.Err)
			}
			if report.ExitCode == nil || *report.ExitCode != tc.want {
				t.Fatalf("verifier exit = %v, want %d (output=%q)", report.ExitCode, tc.want, report.Output)
			}
			if report.Passed != (tc.want == 0) {
				t.Fatalf("verifier passed = %v, want %v", report.Passed, tc.want == 0)
			}
		})
	}

	t.Run("verifier writes stay outside the candidate", func(t *testing.T) {
		repo := newSeedRepo(t)
		ctx := context.Background()
		seedBranch := "tendril/seed-real-mutation"
		if _, err := runGitCommand(ctx, repo, "checkout", "-b", seedBranch); err != nil {
			t.Fatalf("checkout seed branch: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repo, "HELLO.md"), []byte("Hello from OpenTendril.\n"), 0o644); err != nil {
			t.Fatalf("write HELLO.md: %v", err)
		}
		if _, err := runGitCommand(ctx, repo, "add", "HELLO.md"); err != nil {
			t.Fatalf("stage HELLO.md: %v", err)
		}
		if _, err := runGitCommand(ctx, repo, "commit", "-m", "hello"); err != nil {
			t.Fatalf("commit HELLO.md: %v", err)
		}
		if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
			t.Fatalf("checkout main: %v", err)
		}
		seedTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
		if err != nil {
			t.Fatalf("seed tip: %v", err)
		}
		mainTip, err := runGitCommand(ctx, repo, "rev-parse", "main")
		if err != nil {
			t.Fatalf("main tip: %v", err)
		}

		command := []string{"sh", "-c", "printf 'verifier write\\n' > MUTATED.txt; printf 'Hello from OpenTendril.\\n' | cmp -s - HELLO.md"}
		report := runSeedVerify(ctx, repo, strings.TrimSpace(seedTip), command, nil)
		if report.Err != nil {
			t.Fatalf("runSeedVerify: %v", report.Err)
		}
		if !report.Passed || report.ExitCode == nil || *report.ExitCode != 0 {
			t.Fatalf("mutation verifier result = %+v, want pass", report)
		}
		if _, err := os.Stat(filepath.Join(repo, "MUTATED.txt")); !os.IsNotExist(err) {
			t.Fatalf("verifier write escaped into candidate checkout: stat error = %v", err)
		}
		afterSeedTip, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
		if err != nil {
			t.Fatalf("read Seed tip after verification: %v", err)
		}
		afterMainTip, err := runGitCommand(ctx, repo, "rev-parse", "main")
		if err != nil {
			t.Fatalf("read main tip after verification: %v", err)
		}
		if strings.TrimSpace(afterSeedTip) != strings.TrimSpace(seedTip) || strings.TrimSpace(afterMainTip) != strings.TrimSpace(mainTip) {
			t.Fatalf("verification changed Git refs: Seed %s -> %s, main %s -> %s", seedTip, strings.TrimSpace(afterSeedTip), mainTip, strings.TrimSpace(afterMainTip))
		}
	})
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

func TestSeedCandidateRejectsWhitespaceCollapsedExecutionLocationPath(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "seed@example.com"}, {"config", "user.name", "Seed Tester"}, {"checkout", "-b", "main"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	existing := " ~/tendril/.tendril/run-workspaces/x/HELLO.md"
	writeExactGitPath(t, repo, existing, "legacy\n")
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "base with leading-space path"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	start, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	start = strings.TrimSpace(start)

	isolation := "sprout/task-whitespace-path"
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", isolation); err != nil {
		t.Fatalf("isolation: %v", err)
	}
	leaked := "~/tendril/.tendril/run-workspaces/x/HELLO.md"
	writeExactGitPath(t, repo, leaked, "Hello from OpenTendril.\n")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "add unpadded execution-location path"}} {
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
	RegisterOwnedRef(OwnedRef{Repository: repo, Branch: isolation, Purpose: PurposeSproutIsolation, Base: start, RunID: "run-whitespace-path"})
	ws := RunWorkspace{Path: linked, Repository: repo, Branch: isolation, BaseCommit: start, RunID: "run-whitespace-path"}
	seedBranch := "tendril/seed-whitespace-path"

	if err := integrateSeedCheckpoint(ctx, ws, seedBranch, commit, start); err == nil {
		t.Fatal("integrateSeedCheckpoint accepted a newly created execution-location path that differs from an existing path only by leading whitespace")
	} else if !strings.Contains(err.Error(), "execution-location leakage") {
		t.Fatalf("error = %q, want path-integrity failure", err)
	}
	if localBranchExists(repo, seedBranch) {
		t.Fatal("rejected candidate advanced the Seed checkpoint")
	}
}

func TestGitTreePathSetKeepsWhitespaceDistinctPathnames(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "seed@example.com"}, {"config", "user.name", "Seed Tester"}, {"checkout", "-b", "main"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	for _, name := range []string{"keep.txt", " keep.txt", "keep.txt "} {
		writeExactGitPath(t, repo, name, "x\n")
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "whitespace-distinct names"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	rev, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	paths, err := gitTreePathSet(ctx, repo, strings.TrimSpace(rev))
	if err != nil {
		t.Fatalf("gitTreePathSet: %v", err)
	}
	for _, name := range []string{"keep.txt", " keep.txt", "keep.txt "} {
		if _, ok := paths[name]; !ok {
			t.Errorf("missing exact pathname %q in %#v", name, paths)
		}
	}
	if len(paths) != 3 {
		t.Fatalf("whitespace-distinct Git pathnames collapsed: %#v", paths)
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
	if timedOut.Status != SeedStatusWithered || timedOut.Iterations != 1 {
		t.Fatalf("timeout status/iterations = %q/%d, want withered/1", timedOut.Status, timedOut.Iterations)
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
		for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", name}} {
			if _, err := runGitCommand(ctx, repo, args...); err != nil {
				return SproutRunReport{}, err
			}
		}
		checkpoint, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
		if err != nil {
			return SproutRunReport{}, err
		}
		if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
			return SproutRunReport{}, err
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: strings.TrimSpace(checkpoint)}, nil
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

	report := runSeedVerify(ctx, repo, checkpoint, []string{"false"}, nil)
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
	seedA, err := runGitCommand(ctx, repo, "rev-parse", "tendril/seed-a")
	if err != nil {
		t.Fatalf("seed A tip: %v", err)
	}
	seedB, err := runGitCommand(ctx, repo, "rev-parse", "tendril/seed-b")
	if err != nil {
		t.Fatalf("seed B tip: %v", err)
	}
	seedA = strings.TrimSpace(seedA)
	seedB = strings.TrimSpace(seedB)

	var wg sync.WaitGroup
	errA := make(chan seedVerifyReport, 1)
	errB := make(chan seedVerifyReport, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errA <- runSeedVerify(ctx, repo, seedA, round16HelloVerifyArgv(), nil)
	}()
	go func() {
		defer wg.Done()
		errB <- runSeedVerify(ctx, repo, seedB, round16HelloVerifyArgv(), nil)
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
	runSproutPreflightChecksFn = func(_ context.Context, _ *llm.Client) error { return nil }
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

func writeExactGitPath(t *testing.T, repo, relPath, contents string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %q: %v", relPath, err)
	}
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
		{" keep.txt", false},
		{"keep.txt ", false},
		{" ~/not-a-workspace.md", false},
		{"~/not-a-workspace.md", true},
		{"~/tendril/.tendril/run-workspaces/abc123/HELLO.md", true},
		{" ~/tendril/.tendril/run-workspaces/x/HELLO.md", true},
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

type pathBackedHostSnapshot struct {
	branch      string
	head        string
	main        string
	status      string
	stash       string
	tracked     string
	untracked   string
	currentName string
}

type pathBackedSeedRunner struct {
	file              string
	contents          string
	extraFiles        map[string]string
	overwriteExisting string
	overwriteContents string
	overwroteExisting bool
	workspaceCacheDev uint64
	workspaceCacheIno uint64
	runErr            error
	boundaryFailure   bool
	wroteWorkspace    *bool
	workspace         string
	startHEAD         string
	startHELLO        string
}

func (runner *pathBackedSeedRunner) setWorkspace(workspace string) {
	runner.workspace = workspace
}

func (runner *pathBackedSeedRunner) setSeedIntegrationCheckpoint(bool) {}

func (runner *pathBackedSeedRunner) Run(ctx context.Context, _ string) (sproutResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if runner.workspace != "" {
		if head, err := runGitCommand(ctx, runner.workspace, "rev-parse", "HEAD"); err == nil {
			runner.startHEAD = strings.TrimSpace(head)
		}
		if contents, err := os.ReadFile(filepath.Join(runner.workspace, "HELLO.md")); err == nil {
			runner.startHELLO = string(contents)
		}
	}
	wrote := false
	if runner.overwriteExisting != "" {
		full := filepath.Join(runner.workspace, filepath.FromSlash(runner.overwriteExisting))
		info, err := os.Stat(full)
		if err != nil {
			return sproutResult{}, fmt.Errorf("copied cache %s is missing; overwrite requires an existing file: %w", runner.overwriteExisting, err)
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			runner.workspaceCacheDev = st.Dev
			runner.workspaceCacheIno = st.Ino
		}
		if err := os.WriteFile(full, []byte(runner.overwriteContents), 0o644); err != nil {
			return sproutResult{}, err
		}
		runner.overwroteExisting = true
		wrote = true
	}
	if runner.file != "" {
		full := filepath.Join(runner.workspace, filepath.FromSlash(runner.file))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return sproutResult{}, err
		}
		if err := os.WriteFile(full, []byte(runner.contents), 0o644); err != nil {
			return sproutResult{}, err
		}
		wrote = true
	}
	for rel, contents := range runner.extraFiles {
		full := filepath.Join(runner.workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return sproutResult{}, err
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			return sproutResult{}, err
		}
		wrote = true
	}
	if runner.wroteWorkspace != nil {
		wrote = *runner.wroteWorkspace
	}
	if runner.runErr != nil {
		return sproutResult{Response: "", WroteWorkspace: wrote, BoundaryFailure: runner.boundaryFailure}, runner.runErr
	}
	return sproutResult{Response: "path-backed seed complete", WroteWorkspace: wrote}, nil
}

type pathBackedSeedProbe struct {
	mu            sync.Mutex
	shadowCalls   int
	seedTreeCalls int
	stashCalls    int
	mergeCalls    int
	pushCalls     int
	mounts        []string
}

func preparePathBackedGitRepo(t *testing.T) string {
	t.Helper()
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	chdirToTempDir(t)

	repo := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "path-seed@example.invalid"},
		{"config", "user.name", "Path Seed Test"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "keep.txt"}, {"commit", "-q", "-m", "base"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	t.Cleanup(func() {
		_, _ = runGitCommand(context.Background(), repo, "worktree", "prune")
	})
	return repo
}

func installPathBackedSeedSeams(t *testing.T, runners map[string]sproutRunner) *pathBackedSeedProbe {
	t.Helper()
	probe := &pathBackedSeedProbe{}

	originalPreflight := runSproutPreflightChecksFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsure := ensureSproutImageFn
	originalStart := startTerrariumSessionFn
	originalNew := newSproutFn
	originalShadow := createShadowWorktreeFn
	originalSeedTree := createSeedCandidateWorktreeFn
	originalStash := stashHostWorkspaceFn
	originalMerge := mergeTerrariumCommitFn
	originalPush := pushTerrariumCommitFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsure
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNew
		createShadowWorktreeFn = originalShadow
		createSeedCandidateWorktreeFn = originalSeedTree
		stashHostWorkspaceFn = originalStash
		mergeTerrariumCommitFn = originalMerge
		pushTerrariumCommitFn = originalPush
	})

	runSproutPreflightChecksFn = func(context.Context, *llm.Client) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "# path-backed repo map\n", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(context.Context, string, string, string, bool, []string, []string, time.Duration, ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	createShadowWorktreeFn = func(sourcePath, branch string) (string, error) {
		probe.mu.Lock()
		probe.shadowCalls++
		probe.mu.Unlock()
		return originalShadow(sourcePath, branch)
	}
	createSeedCandidateWorktreeFn = func(sourcePath, revision string) (string, error) {
		probe.mu.Lock()
		probe.seedTreeCalls++
		probe.mu.Unlock()
		return originalSeedTree(sourcePath, revision)
	}
	stashHostWorkspaceFn = func(ctx context.Context, root, runID string) (bool, error) {
		probe.mu.Lock()
		probe.stashCalls++
		probe.mu.Unlock()
		return originalStash(ctx, root, runID)
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error {
		probe.mu.Lock()
		probe.mergeCalls++
		probe.mu.Unlock()
		return originalMerge(ctx, sourcePath, commitHash)
	}
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, credential ResolvedCredential, allowDefault bool, stepID string) error {
		probe.mu.Lock()
		probe.pushCalls++
		probe.mu.Unlock()
		return originalPush(ctx, mountPath, branch, credential, allowDefault, stepID)
	}
	newSproutFn = func(_ context.Context, workspace, _ string, _ string, _ llmCaller, _ toolSession, _ *eventbus.Bus, stepID, _ string) (sproutRunner, error) {
		runner, ok := runners[stepID]
		if !ok {
			if fallback, exists := runners["*"]; exists {
				runner = fallback
			} else {
				return nil, fmt.Errorf("missing path-backed test runner for %s", stepID)
			}
		}
		if setter, ok := runner.(interface{ setWorkspace(string) }); ok {
			setter.setWorkspace(workspace)
		}
		probe.mu.Lock()
		probe.mounts = append(probe.mounts, workspace)
		probe.mu.Unlock()
		return runner, nil
	}
	return probe
}

func (probe *pathBackedSeedProbe) counts() (shadow, seedTree, stash, merge, push int) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.shadowCalls, probe.seedTreeCalls, probe.stashCalls, probe.mergeCalls, probe.pushCalls
}

func (probe *pathBackedSeedProbe) mountPaths() []string {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	out := make([]string, len(probe.mounts))
	copy(out, probe.mounts)
	return out
}

func snapshotPathBackedHost(t *testing.T, repo string) pathBackedHostSnapshot {
	t.Helper()
	ctx := context.Background()
	branch, err := runGitCommand(ctx, repo, "branch", "--show-current")
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	main, err := runGitCommand(ctx, repo, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("main: %v", err)
	}
	status, err := runGitCommandRawOutput(ctx, repo, "status", "--porcelain", "-uall")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	stash, err := runGitCommand(ctx, repo, "stash", "list")
	if err != nil {
		t.Fatalf("stash list: %v", err)
	}
	tracked, err := os.ReadFile(filepath.Join(repo, "keep.txt"))
	if err != nil {
		t.Fatalf("read keep.txt: %v", err)
	}
	untracked, _ := os.ReadFile(filepath.Join(repo, "host-untracked.txt"))
	return pathBackedHostSnapshot{
		branch:      strings.TrimSpace(branch),
		head:        strings.TrimSpace(head),
		main:        strings.TrimSpace(main),
		status:      status,
		stash:       stash,
		tracked:     string(tracked),
		untracked:   string(untracked),
		currentName: strings.TrimSpace(branch),
	}
}

func assertPathBackedHostUnchanged(t *testing.T, repo string, before pathBackedHostSnapshot) {
	t.Helper()
	after := snapshotPathBackedHost(t, repo)
	if after.branch != before.branch {
		t.Fatalf("checked-out branch changed: %q -> %q", before.branch, after.branch)
	}
	if after.head != before.head {
		t.Fatalf("checked-out HEAD changed: %q -> %q", before.head, after.head)
	}
	if after.main != before.main {
		t.Fatalf("default branch tip changed: %q -> %q", before.main, after.main)
	}
	if after.status != before.status {
		t.Fatalf("host git status changed:\nbefore=%q\nafter=%q", before.status, after.status)
	}
	if after.stash != before.stash {
		t.Fatalf("host stash changed:\nbefore=%q\nafter=%q", before.stash, after.stash)
	}
	if after.tracked != before.tracked {
		t.Fatalf("dirty tracked file changed: %q -> %q", before.tracked, after.tracked)
	}
	if after.untracked != before.untracked {
		t.Fatalf("untracked host file changed: %q -> %q", before.untracked, after.untracked)
	}
}

func runPathBackedSeedSprout(t *testing.T, repo, stepID, seedBranch, start string, runner sproutRunner) (SproutRunReport, error) {
	t.Helper()
	return (&DockerOrchestrator{
		Substrate:                 repo,
		StepID:                    stepID,
		SubstrateBranch:           seedBranch,
		DisableMergeBack:          true,
		SeedIntegrationCheckpoint: true,
		SeedStartRevision:         start,
		AwaitsRunEnding:           true,
	}).RunSprout(context.Background(), "create HELLO.md")
}

func TestPathBackedSeedCheckpointCreatesImmutableCandidate(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-path-create"
	stepID := "path-seed-create"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "Hello from OpenTendril.\n"}
	probe := installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
	before := snapshotPathBackedHost(t, repo)

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
	if runErr != nil {
		t.Fatalf("RunSprout: %v", runErr)
	}
	if report.seedCandidateCommit == "" {
		t.Fatal("path-backed Seed checkpoint did not return seedCandidateCommit")
	}
	if report.FruitBranch != "" || report.FruitCommit != "" {
		t.Fatalf("path-backed Seed exposed Fruit identity %q/%q", report.FruitBranch, report.FruitCommit)
	}
	resolved, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("seed ref: %v", err)
	}
	if strings.TrimSpace(resolved) != report.seedCandidateCommit {
		t.Fatalf("seed ref = %q, want %q", strings.TrimSpace(resolved), report.seedCandidateCommit)
	}
	content, err := runGitCommandRawOutput(ctx, repo, "show", report.seedCandidateCommit+":HELLO.md")
	if err != nil {
		t.Fatalf("show candidate HELLO.md: %v", err)
	}
	if content != "Hello from OpenTendril.\n" {
		t.Fatalf("candidate HELLO.md = %q", content)
	}
	parent, err := runGitCommand(ctx, repo, "rev-parse", report.seedCandidateCommit+"^")
	if err != nil {
		t.Fatalf("candidate parent: %v", err)
	}
	if strings.TrimSpace(parent) != base {
		t.Fatalf("candidate parent = %q, want start revision %q", strings.TrimSpace(parent), base)
	}
	assertPathBackedHostUnchanged(t, repo, before)
	shadow, seedTree, stash, merge, push := probe.counts()
	if shadow != 0 || seedTree != 1 || stash != 0 || merge != 0 || push != 0 {
		t.Fatalf("isolation/publication counts shadow=%d seed=%d stash=%d merge=%d push=%d", shadow, seedTree, stash, merge, push)
	}
	if localBranchExists(repo, "sprout/task-"+stepID) {
		t.Fatal("path-backed Seed created a reviewable isolation branch on the host")
	}
}

func TestPathBackedSeedCheckpointStartsFromExactSeedStartRevision(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	start, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start = strings.TrimSpace(start)
	if err := os.WriteFile(filepath.Join(repo, "only-on-head.txt"), []byte("incidental HEAD\n"), 0o644); err != nil {
		t.Fatalf("write head-only file: %v", err)
	}
	for _, args := range [][]string{{"add", "only-on-head.txt"}, {"commit", "-q", "-m", "incidental host HEAD"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	head = strings.TrimSpace(head)
	if head == start {
		t.Fatal("setup failed: host HEAD still equals SeedStartRevision")
	}

	seedBranch := "tendril/seed-path-exact-start"
	stepID := "path-seed-exact-start"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "from start revision\n"}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, start, runner)
	if runErr != nil {
		t.Fatalf("RunSprout: %v", runErr)
	}
	if strings.TrimSpace(runner.startHEAD) != start {
		t.Fatalf("candidate worktree HEAD at Sprout start = %q, want SeedStartRevision %q (host HEAD was %q)", runner.startHEAD, start, head)
	}
	if _, err := runGitCommand(ctx, repo, "cat-file", "-e", report.seedCandidateCommit+":only-on-head.txt"); err == nil {
		t.Fatal("candidate contains the incidental host HEAD file; worktree was based on HEAD rather than SeedStartRevision")
	}
	parent, err := runGitCommand(ctx, repo, "rev-parse", report.seedCandidateCommit+"^")
	if err != nil {
		t.Fatalf("candidate parent: %v", err)
	}
	if strings.TrimSpace(parent) != start {
		t.Fatalf("candidate parent = %q, want SeedStartRevision %q", strings.TrimSpace(parent), start)
	}
	hostHEAD, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("host HEAD after run: %v", err)
	}
	if strings.TrimSpace(hostHEAD) != head {
		t.Fatalf("host HEAD moved from %q to %q", head, strings.TrimSpace(hostHEAD))
	}
}

func TestPathBackedSeedVerificationSeesCheckpointMutation(t *testing.T) {
	restoreSeeds(t)
	stubLocalStoma(t)
	repo := preparePathBackedGitRepo(t)
	stepID := "path-seed-verify"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "Hello from OpenTendril.\n"}
	installPathBackedSeedSeams(t, map[string]sproutRunner{"*": runner, stepID: runner})

	var verifiedCandidates []string
	var verifiedContents []string
	var reports []SproutRunReport
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		report, err := orch.RunSprout(ctx, prompt)
		reports = append(reports, report)
		return report, err
	}
	seedVerifyFn = func(ctx context.Context, sourcePath, candidate string, verify, egress []string) seedVerifyReport {
		verifiedCandidates = append(verifiedCandidates, candidate)
		content, err := runGitCommandRawOutput(ctx, sourcePath, "show", candidate+":HELLO.md")
		if err != nil {
			t.Fatalf("verify candidate missing HELLO.md: %v", err)
		}
		verifiedContents = append(verifiedContents, content)
		return runSeedVerify(ctx, sourcePath, candidate, verify, egress)
	}

	result, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repo,
		Goal:          "create HELLO.md",
		Verify:        round16HelloVerifyArgv(),
		MaxIterations: 1,
		SessionID:     "path-seed-verify-sees-candidate",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if result.Status != SeedStatusSatisfied {
		t.Fatalf("status = %q, want satisfied; logs:\n%s", result.Status, result.Logs)
	}
	if len(reports) != 1 || reports[0].seedCandidateCommit == "" {
		t.Fatalf("builder reports = %+v, want one checkpointed candidate", reports)
	}
	if len(verifiedCandidates) != 1 || verifiedCandidates[0] != reports[0].seedCandidateCommit {
		t.Fatalf("verified candidate = %v, want %q", verifiedCandidates, reports[0].seedCandidateCommit)
	}
	if len(verifiedContents) != 1 || verifiedContents[0] != "Hello from OpenTendril.\n" {
		t.Fatalf("verify received HELLO.md = %q, want the checkpointed mutation", verifiedContents)
	}
	if result.Commit == "" || result.Branch == "" {
		t.Fatalf("satisfied Seed omitted Fruit identity: branch=%q commit=%q", result.Branch, result.Commit)
	}
	main, err := runGitCommand(context.Background(), repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("main: %v", err)
	}
	base, err := runGitCommand(context.Background(), repo, "rev-parse", result.Commit+"^")
	if err != nil {
		t.Fatalf("fruit parent: %v", err)
	}
	if strings.TrimSpace(main) != strings.TrimSpace(base) {
		t.Fatalf("default branch moved: main=%s fruit-parent=%s", strings.TrimSpace(main), strings.TrimSpace(base))
	}
}

func TestPathBackedSeedSecondIterationInheritsCheckpoint(t *testing.T) {
	restoreSeeds(t)
	stubLocalStoma(t)
	repo := preparePathBackedGitRepo(t)
	var runners []*pathBackedSeedRunner
	var startRevisions []string
	iteration := 0
	installPathBackedSeedSeams(t, nil)
	newSproutFn = func(_ context.Context, workspace, _ string, _ string, _ llmCaller, _ toolSession, _ *eventbus.Bus, _, _ string) (sproutRunner, error) {
		iteration++
		runner := &pathBackedSeedRunner{workspace: workspace}
		if iteration == 1 {
			runner.file = "HELLO.md"
			runner.contents = "Hello from OpenTendril."
		} else {
			runner.file = "HELLO.md"
			runner.contents = "Hello from OpenTendril.\n"
		}
		runners = append(runners, runner)
		return runner, nil
	}

	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		startRevisions = append(startRevisions, strings.TrimSpace(orch.SeedStartRevision))
		return orch.RunSprout(ctx, prompt)
	}

	result, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repo,
		Goal:          "create HELLO.md",
		Verify:        round16HelloVerifyArgv(),
		MaxIterations: 2,
		SessionID:     "path-seed-two-iteration-inheritance",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if result.Status != SeedStatusSatisfied || result.Iterations != 2 {
		t.Fatalf("result = %+v, want satisfied after two iterations; logs:\n%s", result, result.Logs)
	}
	if len(runners) != 2 || len(startRevisions) != 2 {
		t.Fatalf("iterations = runners %d starts %d, want 2", len(runners), len(startRevisions))
	}
	if runners[1].startHELLO != "Hello from OpenTendril." {
		t.Fatalf("iteration 2 inherited HELLO.md = %q, want iteration 1's checkpointed partial write", runners[1].startHELLO)
	}
	if startRevisions[1] == startRevisions[0] {
		t.Fatalf("iteration 2 started from the original base %q rather than iteration 1's checkpoint", startRevisions[0])
	}
	if strings.TrimSpace(runners[1].startHEAD) != startRevisions[1] {
		t.Fatalf("iteration 2 worktree HEAD = %q, want SeedStartRevision %q", runners[1].startHEAD, startRevisions[1])
	}
}

func TestPathBackedSeedCheckpointPreservesDirtyHostWorktree(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", "work"); err != nil {
		t.Fatalf("checkout work: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("dirty tracked\n"), 0o644); err != nil {
		t.Fatalf("dirty tracked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "host-untracked.txt"), []byte("dirty untracked\n"), 0o644); err != nil {
		t.Fatalf("dirty untracked: %v", err)
	}
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-path-dirty-host"
	stepID := "path-seed-dirty-host"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "isolated mutation\n"}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
	before := snapshotPathBackedHost(t, repo)

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
	if runErr != nil {
		t.Fatalf("RunSprout: %v", runErr)
	}
	if report.seedCandidateCommit == "" {
		t.Fatal("dirty host prevented path-backed Seed checkpoint")
	}
	assertPathBackedHostUnchanged(t, repo, before)
	if _, err := os.Stat(filepath.Join(repo, "HELLO.md")); !os.IsNotExist(err) {
		t.Fatal("Seed mutation leaked into the Botanist checkout")
	}
}

func TestPathBackedSeedNoChangeCreatesNoCandidate(t *testing.T) {
	restoreSeeds(t)
	stubLocalStoma(t)
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	stepID := "path-seed-no-change"
	runner := &pathBackedSeedRunner{}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner, "*": runner})

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, "tendril/seed-path-no-change", base, runner)
	if runErr != nil {
		t.Fatalf("RunSprout: %v", runErr)
	}
	if report.seedCandidateCommit != "" || report.FruitBranch != "" || report.FruitCommit != "" {
		t.Fatalf("no-change exposed identity Fruit %q/%q candidate %q", report.FruitBranch, report.FruitCommit, report.seedCandidateCommit)
	}
	if localBranchExists(repo, "tendril/seed-path-no-change") {
		t.Fatal("no-change iteration created a Seed ref")
	}

	result, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repo,
		Goal:          "create HELLO.md",
		Verify:        round16HelloVerifyArgv(),
		MaxIterations: 1,
		SessionID:     "path-seed-no-change-fruit",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if result.Branch != "" || result.Commit != "" {
		t.Fatalf("no-change Seed fabricated Fruit %q/%q", result.Branch, result.Commit)
	}
}

func TestPathBackedSeedFailuresDoNotCheckpoint(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)

	t.Run("capability-boundary", func(t *testing.T) {
		seedBranch := "tendril/seed-path-boundary"
		stepID := "path-seed-boundary"
		runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "partial\n", runErr: errUnusableReply, boundaryFailure: true}
		installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
		report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
		if !errors.Is(runErr, errUnusableReply) {
			t.Fatalf("error = %v, want unusable reply", runErr)
		}
		if report.seedCandidateCommit != "" || report.FruitCommit != "" {
			t.Fatalf("boundary failure exposed candidate %q fruit %q", report.seedCandidateCommit, report.FruitCommit)
		}
		if localBranchExists(repo, seedBranch) {
			t.Fatal("boundary failure created a Seed ref")
		}
	})

	t.Run("non-recoverable", func(t *testing.T) {
		seedBranch := "tendril/seed-path-nonrecoverable"
		stepID := "path-seed-nonrecoverable"
		providerErr := errors.New("provider exploded")
		runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "partial\n", runErr: providerErr}
		installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
		report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
		if !errors.Is(runErr, providerErr) {
			t.Fatalf("error = %v, want provider failure", runErr)
		}
		if report.seedCandidateCommit != "" || report.FruitCommit != "" {
			t.Fatalf("non-recoverable failure exposed candidate %q fruit %q", report.seedCandidateCommit, report.FruitCommit)
		}
		if localBranchExists(repo, seedBranch) {
			t.Fatal("non-recoverable failure created a Seed ref")
		}
	})

	t.Run("commit-failure", func(t *testing.T) {
		seedBranch := "tendril/seed-path-commit-failure"
		stepID := "path-seed-commit-failure"
		runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "partial\n"}
		installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
		commitErr := errors.New("checkpoint commit unavailable")
		originalCommit := commitTerrariumExecutionFn
		t.Cleanup(func() { commitTerrariumExecutionFn = originalCommit })
		commitTerrariumExecutionFn = func(context.Context, string, string, string, sproutExecutionStatus, string, ResolvedCredential, bool) (string, error) {
			return "", commitErr
		}
		report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
		if !errors.Is(runErr, commitErr) {
			t.Fatalf("error = %v, want commit failure", runErr)
		}
		if report.seedCandidateCommit != "" || report.FruitCommit != "" {
			t.Fatalf("commit failure exposed candidate %q fruit %q", report.seedCandidateCommit, report.FruitCommit)
		}
		if localBranchExists(repo, seedBranch) {
			t.Fatal("commit failure created a Seed ref")
		}
	})
}

func TestPathBackedSeedRecoverableFailureMayCheckpoint(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-path-salvage"
	stepID := "path-seed-salvage"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "Hello from OpenTendril.", runErr: errUnusableReply}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("error = %v, want recoverable failure", runErr)
	}
	if report.seedCandidateCommit == "" {
		t.Fatal("recoverable failure with stageable work did not checkpoint")
	}
	if report.FruitCommit != "" || report.FruitBranch != "" {
		t.Fatalf("salvage exposed Fruit %q/%q", report.FruitBranch, report.FruitCommit)
	}
	content, err := runGitCommandRawOutput(ctx, repo, "show", report.seedCandidateCommit+":HELLO.md")
	if err != nil {
		t.Fatalf("salvaged HELLO.md: %v", err)
	}
	if content != "Hello from OpenTendril." {
		t.Fatalf("salvaged HELLO.md = %q", content)
	}
}

func TestPathBackedSeedRefRaceFailsClosed(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	if _, err := runGitCommand(ctx, repo, "commit", "--allow-empty", "-q", "-m", "unexpected seed tip"); err != nil {
		t.Fatalf("unexpected commit: %v", err)
	}
	unexpected, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("unexpected tip: %v", err)
	}
	unexpected = strings.TrimSpace(unexpected)
	if _, err := runGitCommand(ctx, repo, "update-ref", "refs/heads/main", base); err != nil {
		t.Fatalf("restore main: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "reset", "--hard", base); err != nil {
		t.Fatalf("reset worktree: %v", err)
	}
	seedBranch := "tendril/seed-path-race"
	if _, err := runGitCommand(ctx, repo, "update-ref", "refs/heads/"+seedBranch, unexpected); err != nil {
		t.Fatalf("plant unexpected seed ref: %v", err)
	}

	stepID := "path-seed-race"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "should not land\n"}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
	beforeMain, err := runGitCommand(ctx, repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("main: %v", err)
	}

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
	if runErr == nil {
		t.Fatal("expected CAS failure when the Seed ref tip does not match SeedStartRevision")
	}
	if report.seedCandidateCommit != "" || report.FruitCommit != "" {
		t.Fatalf("CAS failure exposed candidate %q fruit %q", report.seedCandidateCommit, report.FruitCommit)
	}
	still, err := runGitCommand(ctx, repo, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("seed ref after race: %v", err)
	}
	if strings.TrimSpace(still) != unexpected {
		t.Fatalf("Seed ref was overwritten: got %q, want unexpected tip %q", strings.TrimSpace(still), unexpected)
	}
	main, err := runGitCommand(ctx, repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("main after race: %v", err)
	}
	if strings.TrimSpace(main) != strings.TrimSpace(beforeMain) {
		t.Fatalf("main moved during CAS failure")
	}
}

func TestPathBackedSeedCandidateRejectsExecutionLocationLeak(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-path-leak"
	stepID := "path-seed-leak"
	runner := &pathBackedSeedRunner{
		extraFiles: map[string]string{
			"~/tendril/.tendril/run-workspaces/ca0d0f46f7bdf5d26af23e9433890534/HELLO.md": "leaked\n",
		},
	}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
	if runErr == nil {
		t.Fatal("expected path-integrity failure for execution-location leakage")
	}
	if !strings.Contains(runErr.Error(), "execution-location leakage") {
		t.Fatalf("error = %q, want path-integrity failure", runErr)
	}
	if report.seedCandidateCommit != "" || report.FruitCommit != "" {
		t.Fatalf("leaked candidate was accepted: %q fruit %q", report.seedCandidateCommit, report.FruitCommit)
	}
	if localBranchExists(repo, seedBranch) {
		t.Fatal("leaked candidate advanced the Seed ref")
	}
}

func TestPathBackedSeedDoesNotFallBackToHostWorkspace(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	t.Setenv(EnvAllowHostWorkspace, "true")
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	stepID := "path-seed-no-host-fallback"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "must not run\n"}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
	originalSeedTree := createSeedCandidateWorktreeFn
	t.Cleanup(func() { createSeedCandidateWorktreeFn = originalSeedTree })
	createSeedCandidateWorktreeFn = func(string, string) (string, error) {
		return "", fmt.Errorf("simulated seed worktree failure")
	}

	_, runErr := runPathBackedSeedSprout(t, repo, stepID, "tendril/seed-path-no-host-fallback", strings.TrimSpace(base), runner)
	if runErr == nil {
		t.Fatal("expected fail-closed isolation error")
	}
	if !strings.Contains(runErr.Error(), "does not fall back to the active workspace") {
		t.Fatalf("error = %q, want Seed host-fallback refusal", runErr)
	}
}

func TestPathBackedSeedCopiedCacheDoesNotShareSourceInode(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-q", "-m", "ignore dependency cache"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	base = strings.TrimSpace(base)

	const hostCache = "HOST_CACHE_ORIGINAL"
	cacheRel := filepath.ToSlash(filepath.Join("node_modules", "pkg", "cache.txt"))
	sourceCache := filepath.Join(repo, filepath.FromSlash(cacheRel))
	if err := os.MkdirAll(filepath.Dir(sourceCache), 0o755); err != nil {
		t.Fatalf("mkdir host cache: %v", err)
	}
	if err := os.WriteFile(sourceCache, []byte(hostCache), 0o644); err != nil {
		t.Fatalf("write host cache: %v", err)
	}
	sourceDev, sourceIno := fileIdent(t, sourceCache)

	seedBranch := "tendril/seed-path-cache-inode"
	stepID := "path-seed-cache-inode"
	runner := &pathBackedSeedRunner{
		file:              "HELLO.md",
		contents:          "Hello from OpenTendril.\n",
		overwriteExisting: cacheRel,
		overwriteContents: "TERRARIUM_MUTATED_CACHE\n",
	}
	installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
	before := snapshotPathBackedHost(t, repo)

	report, _ := runPathBackedSeedSprout(t, repo, stepID, seedBranch, base, runner)
	if !runner.overwroteExisting {
		t.Fatal("Sprout did not overwrite an existing copied cache file")
	}
	if runner.workspaceCacheDev == 0 && runner.workspaceCacheIno == 0 {
		t.Fatal("workspace cache identity was not recorded")
	}
	if runner.workspaceCacheDev == sourceDev && runner.workspaceCacheIno == sourceIno {
		t.Fatal("copied Seed cache shares a writable inode with the Botanist checkout")
	}

	got, err := os.ReadFile(sourceCache)
	if err != nil {
		t.Fatalf("read host cache after Seed: %v", err)
	}
	if string(got) != hostCache {
		t.Fatalf("host cache = %q, want %q (Seed worktree mutation leaked through a shared inode)", got, hostCache)
	}
	afterDev, afterIno := fileIdent(t, sourceCache)
	if afterDev != sourceDev || afterIno != sourceIno {
		t.Fatalf("host cache identity changed: dev/ino %d/%d -> %d/%d", sourceDev, sourceIno, afterDev, afterIno)
	}
	assertPathBackedHostUnchanged(t, repo, before)

	assertTreeOmitsCopiedCache := func(rev string) {
		t.Helper()
		listing, err := runGitCommandRawOutput(ctx, repo, "ls-tree", "-r", "--name-only", rev)
		if err != nil {
			t.Fatalf("ls-tree %s: %v", rev, err)
		}
		for _, name := range strings.Split(listing, "\n") {
			normalized := filepath.ToSlash(strings.TrimSpace(name))
			if normalized == cacheRel || strings.HasPrefix(normalized, "node_modules/") {
				t.Fatalf("copied dependency cache %q appeared in revision %s", normalized, rev)
			}
		}
	}
	if report.seedCandidateCommit != "" {
		assertTreeOmitsCopiedCache(report.seedCandidateCommit)
	}
	if localBranchExists(repo, seedBranch) {
		assertTreeOmitsCopiedCache(seedBranch)
	}
	if report.FruitCommit != "" {
		assertTreeOmitsCopiedCache(report.FruitCommit)
	}
}

func fileIdent(t *testing.T, path string) (dev, ino uint64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		t.Fatalf("stat %s: syscall.Stat_t unavailable", path)
	}
	return st.Dev, st.Ino
}

func TestNonSeedPathBackedShadowAndMergeUnchanged(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	ctx := context.Background()
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", "dev"); err != nil {
		t.Fatalf("checkout dev: %v", err)
	}
	stepID := "non-seed-path-merge"
	runner := &pathBackedSeedRunner{file: "sprout.txt", contents: "ordinary fruit\n"}
	probe := installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})

	report, runErr := (&DockerOrchestrator{
		Substrate:        repo,
		StepID:           stepID,
		DisableMergeBack: false,
		AwaitsRunEnding:  true,
	}).RunSprout(context.Background(), "write sprout.txt")
	if runErr != nil {
		t.Fatalf("RunSprout: %v", runErr)
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("outcome = %q, want complete", report.Outcome)
	}
	shadow, seedTree, _, merge, _ := probe.counts()
	if seedTree != 0 {
		t.Fatalf("non-Seed path used the Seed candidate worktree helper %d time(s)", seedTree)
	}
	if shadow == 0 {
		t.Fatal("non-Seed path did not use the ordinary shadow worktree helper")
	}
	if merge == 0 {
		t.Fatal("non-Seed path did not merge back from the shadow worktree")
	}
	got, err := os.ReadFile(filepath.Join(repo, "sprout.txt"))
	if err != nil {
		t.Fatalf("merged file missing from host checkout: %v", err)
	}
	if string(got) != "ordinary fruit\n" {
		t.Fatalf("merged file = %q", got)
	}
	branch, err := runGitCommand(ctx, repo, "branch", "--show-current")
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if strings.TrimSpace(branch) != "dev" {
		t.Fatalf("non-Seed run left host on %q, want dev", strings.TrimSpace(branch))
	}
}

func prepareSeedCandidateWorktreeRepo(t *testing.T) (repo, revision string) {
	t.Helper()
	repo = t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "seed-candidate@example.invalid"},
		{"config", "user.name", "Seed Candidate Test"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "keep.txt"}, {"commit", "-q", "-m", "base"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	t.Cleanup(func() {
		_, _ = runGitCommand(context.Background(), repo, "worktree", "prune")
	})
	return repo, strings.TrimSpace(head)
}

func snapshotSeedCandidateSource(t *testing.T, repo string) (head, status, keep string) {
	t.Helper()
	ctx := context.Background()
	headOut, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("source HEAD: %v", err)
	}
	statusOut, err := runGitCommandRawOutput(ctx, repo, "status", "--porcelain", "-uall")
	if err != nil {
		t.Fatalf("source status: %v", err)
	}
	keepBytes, err := os.ReadFile(filepath.Join(repo, "keep.txt"))
	if err != nil {
		t.Fatalf("read keep.txt: %v", err)
	}
	return strings.TrimSpace(headOut), statusOut, string(keepBytes)
}

func assertSeedCandidateSourceUnchanged(t *testing.T, repo, head, status, keep string) {
	t.Helper()
	gotHead, gotStatus, gotKeep := snapshotSeedCandidateSource(t, repo)
	if gotHead != head {
		t.Fatalf("source HEAD changed: %q -> %q", head, gotHead)
	}
	if gotStatus != status {
		t.Fatalf("source git status changed:\nbefore=%q\nafter=%q", status, gotStatus)
	}
	if gotKeep != keep {
		t.Fatalf("source keep.txt changed: %q -> %q", keep, gotKeep)
	}
}

func TestCreateSeedCandidateWorktreeUsesRunWorkspaceRootNotTMPDIR(t *testing.T) {
	repo, revision := prepareSeedCandidateWorktreeRepo(t)
	privateTmp := t.TempDir()
	t.Setenv("TMPDIR", privateTmp)
	beforeHead, beforeStatus, beforeKeep := snapshotSeedCandidateSource(t, repo)

	candidate, err := createSeedCandidateWorktree(repo, revision)
	if err != nil {
		t.Fatalf("createSeedCandidateWorktree: %v", err)
	}
	t.Cleanup(func() { removeShadowWorktree(repo, candidate) })

	root := runWorkspaceRoot()
	if !pathIsUnder(candidate, root) {
		t.Fatalf("candidate = %q, want a path below the Stem run-workspace root %q", candidate, root)
	}
	if !strings.HasPrefix(filepath.Base(candidate), seedCandidateWorktreePrefix) {
		t.Fatalf("candidate = %q, want prefix %q", candidate, seedCandidateWorktreePrefix)
	}
	if pathIsUnder(candidate, privateTmp) || sameFilePath(candidate, privateTmp) {
		t.Fatalf("candidate = %q was placed under TMPDIR %q", candidate, privateTmp)
	}
	if strings.HasPrefix(candidate, filepath.Join(privateTmp, seedCandidateWorktreePrefix)) {
		t.Fatalf("candidate still uses process temporary storage: %q", candidate)
	}

	head, err := runGitCommand(context.Background(), candidate, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		t.Fatalf("candidate HEAD: %v", err)
	}
	if strings.TrimSpace(head) != revision {
		t.Fatalf("candidate HEAD = %q, want SeedStartRevision %q", strings.TrimSpace(head), revision)
	}
	abbrev, err := runGitCommand(context.Background(), candidate, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("candidate symbolic HEAD: %v", err)
	}
	if strings.TrimSpace(abbrev) != "HEAD" {
		t.Fatalf("candidate is not detached: abbrev-ref HEAD = %q", strings.TrimSpace(abbrev))
	}
	assertSeedCandidateSourceUnchanged(t, repo, beforeHead, beforeStatus, beforeKeep)
}

func TestCreateSeedCandidateWorktreeDetachesAtExactStartRevision(t *testing.T) {
	repo, start := prepareSeedCandidateWorktreeRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatalf("write later.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "later.txt"}, {"commit", "-q", "-m", "later"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	head = strings.TrimSpace(head)
	if head == start {
		t.Fatal("setup failed: host HEAD still equals SeedStartRevision")
	}
	beforeHead, beforeStatus, beforeKeep := snapshotSeedCandidateSource(t, repo)

	candidate, err := createSeedCandidateWorktree(repo, start)
	if err != nil {
		t.Fatalf("createSeedCandidateWorktree: %v", err)
	}
	t.Cleanup(func() { removeShadowWorktree(repo, candidate) })

	got, err := runGitCommand(ctx, candidate, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		t.Fatalf("candidate HEAD: %v", err)
	}
	if strings.TrimSpace(got) != start {
		t.Fatalf("candidate HEAD = %q, want SeedStartRevision %q (host HEAD was %q)", strings.TrimSpace(got), start, head)
	}
	if _, err := os.Stat(filepath.Join(candidate, "later.txt")); !os.IsNotExist(err) {
		t.Fatal("candidate contains the later host commit; worktree was not detached at SeedStartRevision")
	}
	abbrev, err := runGitCommand(ctx, candidate, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("candidate symbolic HEAD: %v", err)
	}
	if strings.TrimSpace(abbrev) != "HEAD" {
		t.Fatalf("candidate is not detached: abbrev-ref HEAD = %q", strings.TrimSpace(abbrev))
	}
	assertSeedCandidateSourceUnchanged(t, repo, beforeHead, beforeStatus, beforeKeep)
}

func TestCreateSeedCandidateWorktreeRefusesSourceSubstrate(t *testing.T) {
	repo, revision := prepareSeedCandidateWorktreeRepo(t)
	home := filepath.Join(repo, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("create HOME inside source: %v", err)
	}
	t.Setenv("HOME", home)
	beforeHead, beforeStatus, beforeKeep := snapshotSeedCandidateSource(t, repo)

	candidate, err := createSeedCandidateWorktree(repo, revision)
	if err == nil {
		t.Cleanup(func() { removeShadowWorktree(repo, candidate) })
		t.Fatalf("accepted candidate %q inside the Botanist source Substrate", candidate)
	}
	if !strings.Contains(err.Error(), "must not be the Botanist source Substrate") {
		t.Fatalf("error = %v, want source Substrate refusal", err)
	}
	if _, statErr := os.Lstat(filepath.Join(home, ".tendril")); !os.IsNotExist(statErr) {
		t.Fatalf("refusal created Tendril state inside the source Substrate: %v", statErr)
	}
	assertSeedCandidateSourceUnchanged(t, repo, beforeHead, beforeStatus, beforeKeep)
}

func TestCreateSeedCandidateWorktreeFailsClosedOnUnresolvableRoot(t *testing.T) {
	repo, revision := prepareSeedCandidateWorktreeRepo(t)
	blocked := filepath.Join(t.TempDir(), "blocked-home")
	if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("write blocked HOME: %v", err)
	}
	t.Setenv("HOME", blocked)
	beforeHead, beforeStatus, beforeKeep := snapshotSeedCandidateSource(t, repo)

	if _, err := createSeedCandidateWorktree(repo, revision); err == nil {
		t.Fatal("unresolvable owned root was accepted")
	}
	assertSeedCandidateSourceUnchanged(t, repo, beforeHead, beforeStatus, beforeKeep)
}

func TestRemoveShadowWorktreeRemovesOnlyExactSeedCandidate(t *testing.T) {
	repo, revision := prepareSeedCandidateWorktreeRepo(t)
	first, err := createSeedCandidateWorktree(repo, revision)
	if err != nil {
		t.Fatalf("create first candidate: %v", err)
	}
	second, err := createSeedCandidateWorktree(repo, revision)
	if err != nil {
		t.Fatalf("create second candidate: %v", err)
	}
	root := runWorkspaceRoot()
	sentinel := filepath.Join(root, "unrelated-keep")
	if err := os.MkdirAll(sentinel, 0o700); err != nil {
		t.Fatalf("create sentinel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sentinel, "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	t.Cleanup(func() {
		removeShadowWorktree(repo, first)
		removeShadowWorktree(repo, second)
		_ = os.RemoveAll(sentinel)
	})
	beforeHead, beforeStatus, beforeKeep := snapshotSeedCandidateSource(t, repo)

	removeShadowWorktree(repo, first)

	if _, err := os.Lstat(first); !os.IsNotExist(err) {
		t.Fatalf("exact candidate still exists after cleanup: stat error = %v", err)
	}
	listing, err := runGitCommand(context.Background(), repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if strings.Contains(listing, first) {
		t.Fatalf("git still lists removed candidate %q:\n%s", first, listing)
	}
	if _, err := os.Lstat(second); err != nil {
		t.Fatalf("cleanup removed another candidate: %v", err)
	}
	if !strings.Contains(listing, second) {
		t.Fatalf("git lost the remaining candidate %q:\n%s", second, listing)
	}
	if _, err := os.Lstat(sentinel); err != nil {
		t.Fatalf("cleanup removed unrelated path under the run-workspace root: %v", err)
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("cleanup removed the run-workspace root: %v", err)
	}
	assertSeedCandidateSourceUnchanged(t, repo, beforeHead, beforeStatus, beforeKeep)
}

func TestPathBackedSeedCandidateUsesRunWorkspaceRoot(t *testing.T) {
	repo := preparePathBackedGitRepo(t)
	privateTmp := t.TempDir()
	t.Setenv("TMPDIR", privateTmp)
	ctx := context.Background()
	start, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start = strings.TrimSpace(start)
	seedBranch := "tendril/seed-path-runworkspace"
	stepID := "path-seed-runworkspace"
	runner := &pathBackedSeedRunner{file: "HELLO.md", contents: "Hello from OpenTendril.\n"}
	probe := installPathBackedSeedSeams(t, map[string]sproutRunner{stepID: runner})
	before := snapshotPathBackedHost(t, repo)

	report, runErr := runPathBackedSeedSprout(t, repo, stepID, seedBranch, start, runner)
	if runErr != nil {
		t.Fatalf("RunSprout: %v", runErr)
	}
	if report.seedCandidateCommit == "" {
		t.Fatal("path-backed Seed checkpoint did not return seedCandidateCommit")
	}

	mounts := probe.mountPaths()
	if len(mounts) != 1 {
		t.Fatalf("mounts = %v, want exactly one Seed candidate workspace", mounts)
	}
	mounted := mounts[0]
	root := runWorkspaceRoot()
	if !pathIsUnder(mounted, root) {
		t.Fatalf("mounted candidate = %q, want a path below the Stem run-workspace root %q", mounted, root)
	}
	if pathIsUnder(mounted, privateTmp) || sameFilePath(mounted, privateTmp) {
		t.Fatalf("mounted candidate = %q was placed under TMPDIR %q", mounted, privateTmp)
	}
	if strings.TrimSpace(runner.startHEAD) != start {
		t.Fatalf("candidate worktree HEAD at Sprout start = %q, want SeedStartRevision %q", runner.startHEAD, start)
	}
	if _, err := os.Lstat(mounted); !os.IsNotExist(err) {
		t.Fatalf("candidate still exists after RunSprout: stat error = %v", err)
	}
	assertPathBackedHostUnchanged(t, repo, before)
	_, seedTree, stash, merge, push := probe.counts()
	if seedTree != 1 || stash != 0 || merge != 0 || push != 0 {
		t.Fatalf("isolation/publication counts seed=%d stash=%d merge=%d push=%d", seedTree, stash, merge, push)
	}
}
