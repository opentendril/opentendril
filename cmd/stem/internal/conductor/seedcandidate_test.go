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
	if call.Tool == "writeFile" {
		path, _ := call.Arguments["path"].(string)
		content, _ := call.Arguments["content"].(string)
		if err := os.WriteFile(filepath.Join(s.workspace, path), []byte(content), 0o644); err != nil {
			return ToolResponse{}, err
		}
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
		return SproutRunReport{Outcome: SproutOutcomeComplete, FruitCommit: strings.TrimSpace(checkpoint)}, nil
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
	if res.Branch == "" || res.Commit == "" {
		t.Fatalf("failed candidate identity = branch %q commit %q, want the first iteration's candidate preserved", res.Branch, res.Commit)
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
		return SproutRunReport{Outcome: SproutOutcomeComplete, FruitCommit: "not-a-commit"}, nil
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
		return SproutRunReport{Outcome: SproutOutcomeComplete, FruitCommit: firstCheckpoint}, nil
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
		return SproutRunReport{Outcome: SproutOutcomeComplete, FruitCommit: strings.TrimSpace(checkpoint)}, nil
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
