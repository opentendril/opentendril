package conductor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

type managedRunCapture struct {
	mu       sync.Mutex
	mounts   map[string]string
	sources  map[string]string
	branches map[string]string
	files    map[string]string
	caches   map[string]bool
}

func newManagedRunCapture() *managedRunCapture {
	return &managedRunCapture{
		mounts:   make(map[string]string),
		sources:  make(map[string]string),
		branches: make(map[string]string),
		files:    make(map[string]string),
		caches:   make(map[string]bool),
	}
}

func (capture *managedRunCapture) remember(stepID, mountPath, sourcePath, file string) error {
	branch, err := runGitCommand(context.Background(), mountPath, "branch", "--show-current")
	if err != nil {
		return err
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.mounts[stepID] = mountPath
	capture.sources[stepID] = sourcePath
	capture.branches[stepID] = strings.TrimSpace(branch)
	capture.files[mountPath] = file
	_, cacheErr := os.Stat(filepath.Join(mountPath, "vendor", "cache.txt"))
	capture.caches[stepID] = cacheErr == nil
	return nil
}

func (capture *managedRunCapture) get(stepID string) (mount, source, branch string) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.mounts[stepID], capture.sources[stepID], capture.branches[stepID]
}

func (capture *managedRunCapture) cacheVisible(stepID string) bool {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.caches[stepID]
}

type managedWritingRunner struct {
	workspace        string
	file             string
	contents         string
	generatedFile    string
	generatedContent string
	runErr           error
	boundaryFailure  bool
	started          chan struct{}
	release          chan struct{}
	releaseOn        sync.Once
}

func newManagedWritingRunner(file string) *managedWritingRunner {
	return &managedWritingRunner{
		file:    file,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (runner *managedWritingRunner) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	if runner.file != "" {
		contents := runner.contents
		if contents == "" {
			contents = runner.file + "\n"
		}
		if err := os.WriteFile(filepath.Join(runner.workspace, runner.file), []byte(contents), 0o644); err != nil {
			return sproutResult{}, err
		}
	}
	if runner.generatedFile != "" {
		if err := os.WriteFile(filepath.Join(runner.workspace, runner.generatedFile), []byte(runner.generatedContent), 0o644); err != nil {
			return sproutResult{}, err
		}
	}
	if runner.runErr != nil {
		return sproutResult{Response: "", WroteWorkspace: runner.file != "", BoundaryFailure: runner.boundaryFailure}, runner.runErr
	}
	close(runner.started)
	select {
	case <-runner.release:
		return sproutResult{Response: "managed run complete", WroteWorkspace: runner.file != ""}, nil
	case <-ctx.Done():
		return sproutResult{}, ctx.Err()
	}
}

func TestRunSproutSeedCheckpointRejectsCapabilityBoundaryFailure(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	base, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-boundary-failure"
	stepID := "seed-boundary-failure"
	runner := &managedWritingRunner{
		file:            "HELLO.md",
		contents:        "partial",
		runErr:          errUnusableReply,
		boundaryFailure: true,
	}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	report, runErr := (&DockerOrchestrator{
		Substrate:                 repository,
		StepID:                    stepID,
		SubstrateBranch:           seedBranch,
		SeedIntegrationCheckpoint: true,
		SeedStartRevision:         base,
	}).RunSprout(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("RunSprout error = %v, want the original recoverable failure", runErr)
	}
	if report.FruitCommit != "" || report.FruitBranch != "" || report.seedCandidateCommit != "" {
		t.Fatalf("boundary failure exposed candidate identity: FruitBranch=%q FruitCommit=%q seedCandidateCommit=%q", report.FruitBranch, report.FruitCommit, report.seedCandidateCommit)
	}
	if localBranchExists(repository, seedBranch) {
		t.Fatalf("Seed branch %q exists after a capability-boundary failure", seedBranch)
	}
}

func TestRunSproutSeedCheckpointRejectsMeasuredEmptyChanges(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	base, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-empty-changes"
	stepID := "seed-empty-changes"
	runner := &managedWritingRunner{file: "HELLO.md", contents: "partial", runErr: errUnusableReply}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	collectStageableFilesFn = func(context.Context, string, ...string) ([]string, error) {
		return []string{}, nil
	}

	report, runErr := (&DockerOrchestrator{
		Substrate:                 repository,
		StepID:                    stepID,
		SubstrateBranch:           seedBranch,
		SeedIntegrationCheckpoint: true,
		SeedStartRevision:         base,
	}).RunSprout(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("RunSprout error = %v, want the original recoverable failure", runErr)
	}
	if report.FruitCommit != "" || report.FruitBranch != "" || report.seedCandidateCommit != "" {
		t.Fatalf("measured-empty run exposed candidate identity: FruitBranch=%q FruitCommit=%q seedCandidateCommit=%q", report.FruitBranch, report.FruitCommit, report.seedCandidateCommit)
	}
	if localBranchExists(repository, seedBranch) {
		t.Fatalf("Seed branch %q exists after measured-empty changes", seedBranch)
	}
}

func TestRunSproutSeedCheckpointRejectsCheckpointFailure(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	base, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-checkpoint-failure"
	stepID := "seed-checkpoint-failure"
	runner := &managedWritingRunner{file: "HELLO.md", contents: "partial", runErr: errUnusableReply}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	commitErr := errors.New("checkpoint commit unavailable")
	commitTerrariumExecutionFn = func(context.Context, string, string, string, sproutExecutionStatus, string, ResolvedCredential, bool) (string, error) {
		return "", commitErr
	}

	report, runErr := (&DockerOrchestrator{
		Substrate:                 repository,
		StepID:                    stepID,
		SubstrateBranch:           seedBranch,
		SeedIntegrationCheckpoint: true,
		SeedStartRevision:         base,
	}).RunSprout(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) || !errors.Is(runErr, commitErr) {
		t.Fatalf("RunSprout error = %v, want both the recoverable failure and checkpoint error", runErr)
	}
	if report.FruitCommit != "" || report.FruitBranch != "" || report.seedCandidateCommit != "" {
		t.Fatalf("checkpoint failure exposed candidate identity: FruitBranch=%q FruitCommit=%q seedCandidateCommit=%q", report.FruitBranch, report.FruitCommit, report.seedCandidateCommit)
	}
	if localBranchExists(repository, seedBranch) {
		t.Fatalf("Seed branch %q exists after checkpoint failure", seedBranch)
	}
}

func TestRunSproutSeedCheckpointPreservesFailureWhenGeneratedStateCleanupFails(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	base, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-generated-cleanup-failure"
	stepID := "seed-generated-cleanup-failure"
	runner := &managedWritingRunner{
		file:             "HELLO.md",
		contents:         "partial",
		generatedFile:    filepath.Join(tendrilStateDirectory, "genome", repositoryMapFile),
		generatedContent: "tampered by Sprout\n",
		runErr:           fmt.Errorf("%w after 2 attempts; last reply: model-secret", errUnusableReply),
	}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	report, runErr := (&DockerOrchestrator{
		Substrate:                 repository,
		StepID:                    stepID,
		SubstrateBranch:           seedBranch,
		SeedIntegrationCheckpoint: true,
		SeedStartRevision:         base,
	}).RunSprout(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("RunSprout error = %v, want the original recoverable failure preserved", runErr)
	}
	if !strings.Contains(runErr.Error(), "seed integration generated state cleanup") {
		t.Fatalf("RunSprout error = %v, want generated-state cleanup evidence", runErr)
	}
	if report.FruitCommit != "" || report.FruitBranch != "" || report.seedCandidateCommit != "" {
		t.Fatalf("generated-state cleanup failure exposed candidate identity: FruitBranch=%q FruitCommit=%q seedCandidateCommit=%q", report.FruitBranch, report.FruitCommit, report.seedCandidateCommit)
	}
	if localBranchExists(repository, seedBranch) {
		t.Fatalf("Seed branch %q exists after generated-state cleanup failure", seedBranch)
	}
}

func (runner *managedWritingRunner) setWorkspace(workspace string) {
	runner.workspace = workspace
}

func TestRunSeedRound19SalvagesAndRepairsPartialCandidate(t *testing.T) {
	prepareManagedRunRepository(t)
	stubLocalStoma(t)
	restoreSeeds(t)
	repository := filepath.Join(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT"), "managed")
	var prompts []string
	var verifiedCandidates []string
	var reports []SproutRunReport
	var sessions []*round19WriteSession
	var clients []*refusingLLM
	var sprouts []*Sprout
	iteration := 0

	originalPreflight := runSproutPreflightChecksFn
	originalEnsure := ensureSproutImageFn
	originalStart := startTerrariumSessionFn
	originalNew := newSproutFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		ensureSproutImageFn = originalEnsure
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNew
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
	})
	runSproutPreflightChecksFn = func(context.Context, *llm.Client) error { return nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(_ context.Context, _ string, _ string, mountPath string, _ bool, _ []string, _ []string, _ time.Duration, _ ...terrarium.ActivationObserver) (toolSession, error) {
		session := &round19WriteSession{
			fakeSession: fakeSession{tools: []ToolDefinition{{Name: "writeFile"}, {Name: "readFile"}}},
			workspace:   mountPath,
		}
		sessions = append(sessions, session)
		return session, nil
	}
	generateRepoMapFn = func(context.Context, string) (string, error) { return "# repo map\n", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, _ llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		iteration++
		var responses []string
		if iteration == 1 {
			responses = []string{
				`{"tool":"writeFile","arguments":{"path":"HELLO.md","content":"Hello from OpenTendril."}}`,
				`{"tool":"cmp","arguments":{"path":"HELLO.md"}}`,
				`{"tool":"readFile","arguments":{"path":"HELLO.md"}}`,
				unreadableWrapperReply,
				unreadableWrapperReply,
			}
		} else {
			responses = []string{
				`{"tool":"writeFile","arguments":{"path":"HELLO.md","content":"Hello from OpenTendril.\n"}}`,
				`{"final":"repaired candidate"}`,
			}
		}
		client := &refusingLLM{fakeLLM: fakeLLM{responses: responses}, refusalMessage: "tools unsupported for this model"}
		clients = append(clients, client)
		sprout, err := newSprout(ctx, workspace, genotypeRoot, genotypeName, client, session, bus, stepID, sessionID)
		if err == nil {
			sprouts = append(sprouts, sprout)
		}
		return sprout, err
	}
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		report, err := orch.RunSprout(ctx, prompt)
		reports = append(reports, report)
		return report, err
	}
	seedVerifyFn = func(ctx context.Context, sourcePath, candidate string, verify, egress []string) seedVerifyReport {
		verifiedCandidates = append(verifiedCandidates, candidate)
		return runSeedVerify(ctx, sourcePath, candidate, verify, egress)
	}
	bus := eventbus.New()
	defer bus.Shutdown()

	result, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repository,
		Goal:          "create HELLO.md",
		Verify:        round16HelloVerifyArgv(),
		MaxIterations: 2,
		SessionID:     "seed-round-19-qualification",
		EventBus:      bus,
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if result.Status != SeedStatusSatisfied || result.Iterations != 2 {
		t.Fatalf("result = %+v, want satisfied after two iterations", result)
	}
	if len(prompts) != 2 || len(verifiedCandidates) != 2 || len(reports) != 2 {
		t.Fatalf("prompts/candidates/reports = %d/%d/%d, want 2/2/2", len(prompts), len(verifiedCandidates), len(reports))
	}
	if reports[0].Outcome != SproutOutcomeFailed || reports[0].Output != "" {
		t.Fatalf("first Sprout outcome = %q, want failed so the withered Sprout remains observable", reports[0].Outcome)
	}
	if reports[0].FruitBranch != "" || reports[0].FruitCommit != "" || reports[0].seedCandidateCommit == "" {
		t.Fatalf("first Sprout identity = Fruit %q/%q candidate %q, want an internal Seed candidate only", reports[0].FruitBranch, reports[0].FruitCommit, reports[0].seedCandidateCommit)
	}
	firstContents, err := runGitCommandRawOutput(context.Background(), repository, "show", reports[0].seedCandidateCommit+":HELLO.md")
	if err != nil {
		t.Fatalf("read first internal Seed candidate: %v", err)
	}
	if firstContents != "Hello from OpenTendril." {
		t.Fatalf("first candidate HELLO.md = %q, want the immutable no-newline partial write", firstContents)
	}
	if len(sessions) != 2 || len(sessions[0].calls) != 2 || sessions[0].calls[0].Tool != "writeFile" || sessions[0].calls[1].Tool != "readFile" {
		t.Fatalf("first Terrarium calls = %+v, want writeFile then readFile; cmp must be refused before session execution", sessions)
	}
	if reports[0].ToolInvocations != 3 {
		t.Fatalf("first Sprout tool invocations = %d, want writeFile, refused cmp, and readFile", reports[0].ToolInvocations)
	}
	if len(sprouts) != 2 || sprouts[0].boundaryFailure.Load() {
		t.Fatalf("first Sprout boundary classification = %v, want no fail-closed boundary violation for unavailable cmp", len(sprouts) > 0 && sprouts[0].boundaryFailure.Load())
	}
	if !strings.Contains(sprouts[0].transcript.String(), "unsupported tool") || !strings.Contains(sprouts[0].transcript.String(), "cmp") {
		t.Fatalf("first Sprout transcript omitted the safe cmp refusal:\n%s", sprouts[0].transcript.String())
	}
	if len(sessions[1].calls) != 1 || sessions[1].calls[0].Tool != "writeFile" || sessions[1].calls[0].Arguments["content"] != "Hello from OpenTendril.\n" {
		t.Fatalf("second Terrarium calls = %+v, want the repaired writeFile with a final newline", sessions[1].calls)
	}
	if reports[1].Outcome != SproutOutcomeComplete {
		t.Fatalf("second Sprout outcome = %q, want complete after repair", reports[1].Outcome)
	}
	if reports[1].FruitBranch != "" || reports[1].FruitCommit != "" || reports[1].seedCandidateCommit == "" {
		t.Fatalf("second Sprout identity = Fruit %q/%q candidate %q, want an internal Seed candidate only", reports[1].FruitBranch, reports[1].FruitCommit, reports[1].seedCandidateCommit)
	}
	if reports[0].seedCandidateCommit == reports[1].seedCandidateCommit {
		t.Fatalf("candidate identity did not advance after repair: %v", verifiedCandidates)
	}
	for _, want := range []string{"Current candidate diff against the Seed base:", "\\ No newline at end of file", "This is deterministic Stem-provided candidate evidence."} {
		if !strings.Contains(prompts[1], want) {
			t.Fatalf("next Sprout prompt omitted %q:\n%s", want, prompts[1])
		}
	}
	if !strings.Contains(result.Logs, "model reply attempted a tool call") {
		t.Fatalf("original Sprout failure was not preserved in Seed logs:\n%s", result.Logs)
	}
	if got := len(downgradeEvents(bus)); got != 2 {
		t.Fatalf("provider/prose downgrade events = %d, want one per Sprout iteration", got)
	}
	if len(clients) != 2 {
		t.Fatalf("provider clients = %d, want one per Sprout iteration", len(clients))
	}
	for i, client := range clients {
		if len(client.toolsPerNativeCall) != 2 || len(client.toolsPerNativeCall[0]) == 0 || len(client.toolsPerNativeCall[1]) != 0 {
			t.Fatalf("iteration %d native calls = %d with tool counts %v, want refused-with-tools then successful probe-without-tools", i+1, len(client.toolsPerNativeCall), func() []int {
				counts := make([]int, len(client.toolsPerNativeCall))
				for j, tools := range client.toolsPerNativeCall {
					counts[j] = len(tools)
				}
				return counts
			}())
		}
	}
	if len(clients[0].calls) != 5 {
		t.Fatalf("first provider prose calls = %d, want 5: writeFile, cmp, readFile, then two bounded unusable replies", len(clients[0].calls))
	}
	if len(result.VerificationDiagnostics) != 2 || result.VerificationDiagnostics[0].ExitCode == nil || *result.VerificationDiagnostics[0].ExitCode != 1 || result.VerificationDiagnostics[1].ExitCode == nil || *result.VerificationDiagnostics[1].ExitCode != 0 {
		t.Fatalf("verification diagnostics = %+v, want deterministic exit 1 then pass", result.VerificationDiagnostics)
	}
	content, err := runGitCommandRawOutput(context.Background(), repository, "show", result.Commit+":HELLO.md")
	if err != nil {
		t.Fatalf("read final HELLO.md: %v", err)
	}
	if content != "Hello from OpenTendril.\n" {
		t.Fatalf("final HELLO.md = %q, want a final newline", content)
	}
}

func TestRunSproutSeedCheckpointSalvagesRecoverableFailure(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	base, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	base = strings.TrimSpace(base)
	seedBranch := "tendril/seed-recoverable-checkpoint"
	stepID := "seed-recoverable-sprout"
	runner := &managedWritingRunner{
		file:     "HELLO.md",
		contents: "Hello from OpenTendril.",
		runErr:   fmt.Errorf("%w after 2 attempts; last reply: model-secret", errUnusableReply),
	}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	// The helper normally supplies a runner by step ID. Seed's checkpoint path
	// has an explicit starting revision and Seed branch, so exercise that path
	// through the real managed RunWorkspace and commit/integration seams.
	report, runErr := (&DockerOrchestrator{
		Substrate:                 repository,
		StepID:                    stepID,
		SubstrateBranch:           seedBranch,
		SeedIntegrationCheckpoint: true,
		SeedStartRevision:         base,
	}).RunSprout(context.Background(), "create HELLO.md")
	if !errors.Is(runErr, errUnusableReply) {
		t.Fatalf("RunSprout error = %v, want the original recoverable failure", runErr)
	}
	if !strings.Contains(runErr.Error(), "model-secret") {
		t.Fatalf("RunSprout error = %v, want the original terminal reply retained", runErr)
	}
	if report.Outcome != SproutOutcomeFailed {
		t.Fatalf("outcome = %q, want failed so the Sprout failure remains observable", report.Outcome)
	}
	if report.FruitCommit != "" || report.FruitBranch != "" {
		t.Fatalf("failed Sprout exposed Fruit identity = branch %q commit %q, want internal candidate only", report.FruitBranch, report.FruitCommit)
	}
	if report.seedCandidateCommit == "" {
		t.Fatal("internal Seed candidate commit is empty")
	}
	resolvedSeed, err := runGitCommand(context.Background(), repository, "rev-parse", seedBranch)
	if err != nil {
		t.Fatalf("resolve Seed candidate: %v", err)
	}
	if strings.TrimSpace(resolvedSeed) != report.seedCandidateCommit {
		t.Fatalf("Seed candidate = %q, want report commit %q", strings.TrimSpace(resolvedSeed), report.seedCandidateCommit)
	}
	content, err := runGitCommandRawOutput(context.Background(), repository, "show", report.seedCandidateCommit+":HELLO.md")
	if err != nil {
		t.Fatalf("read salvaged HELLO.md: %v", err)
	}
	if content != "Hello from OpenTendril." {
		t.Fatalf("salvaged HELLO.md = %q, want the Sprout's governed write without a newline", content)
	}
	message, err := runGitCommand(context.Background(), repository, "show", "-s", "--format=%B", report.seedCandidateCommit)
	if err != nil {
		t.Fatalf("read salvaged checkpoint message: %v", err)
	}
	if strings.Contains(message, "model-secret") {
		t.Fatalf("checkpoint commit message leaked the terminal model reply: %q", message)
	}
	mainTip, err := runGitCommand(context.Background(), repository, "rev-parse", "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if strings.TrimSpace(mainTip) != base {
		t.Fatalf("main moved from %q to %q", base, strings.TrimSpace(mainTip))
	}
}

type managedLockProbeRunner struct {
	repository string
	observed   chan bool
}

func (runner *managedLockProbeRunner) Run(context.Context, string) (sproutResult, error) {
	mutex := runWorkspaceGitMutexFor(runner.repository)
	available := mutex.TryLock()
	if available {
		mutex.Unlock()
	}
	runner.observed <- available
	return sproutResult{Response: "lock probe"}, nil
}

type managedTrackedCacheProbeRunner struct {
	workspace string
	observed  chan error
}

func (runner *managedTrackedCacheProbeRunner) setWorkspace(workspace string) {
	runner.workspace = workspace
}

func (runner *managedTrackedCacheProbeRunner) Run(context.Context, string) (sproutResult, error) {
	var probeErr error
	if _, err := os.Stat(filepath.Join(runner.workspace, "vendor", "vendor")); err == nil {
		probeErr = errors.New("tracked vendor cache was nested as vendor/vendor")
	} else if !os.IsNotExist(err) {
		probeErr = fmt.Errorf("inspect nested tracked vendor cache: %w", err)
	}
	if probeErr == nil {
		contents, err := os.ReadFile(filepath.Join(runner.workspace, "vendor", "cache.txt"))
		if err != nil {
			probeErr = fmt.Errorf("read tracked vendor cache: %w", err)
		} else if string(contents) != "tracked dependency\n" {
			probeErr = fmt.Errorf("tracked vendor cache = %q", contents)
		}
	}
	runner.observed <- probeErr
	if probeErr != nil {
		return sproutResult{}, probeErr
	}
	return sproutResult{Response: "tracked cache preserved"}, nil
}

func (runner *managedWritingRunner) releaseRun() {
	runner.releaseOn.Do(func() { close(runner.release) })
}

func prepareManagedRunRepository(t *testing.T) string {
	t.Helper()

	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	repository := filepath.Join(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT"), "managed")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatalf("create managed checkout: %v", err)
	}
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "managed@example.invalid"},
		{"config", "user.name", "Managed Run Test"},
	} {
		if _, err := runGitCommand(ctx, repository, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "seed.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := runGitCommand(ctx, repository, "add", "seed.txt"); err != nil {
		t.Fatalf("stage seed: %v", err)
	}
	if _, err := runGitCommand(ctx, repository, "commit", "-q", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return repository
}

func installManagedRunSeams(t *testing.T, capture *managedRunCapture, runners map[string]sproutRunner) {
	t.Helper()

	originalPreflight := runSproutPreflightChecksFn
	originalProbe := probeProviderAuthFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsure := ensureSproutImageFn
	originalStart := startTerrariumSessionFn
	originalNew := newSproutFn
	originalCollect := collectStageableFilesFn
	originalDiff := collectGitDiffFn
	originalCommit := commitTerrariumExecutionFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		probeProviderAuthFn = originalProbe
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsure
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNew
		collectStageableFilesFn = originalCollect
		collectGitDiffFn = originalDiff
		commitTerrariumExecutionFn = originalCommit
		mergeTerrariumCommitFn = originalMerge
	})

	runSproutPreflightChecksFn = func(_ context.Context, _ *llm.Client) error { return nil }
	probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "# managed repo map\n", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(context.Context, string, string, string, bool, []string, []string, time.Duration, ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, sourcePath, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		runner, ok := runners[stepID]
		if !ok {
			return nil, errors.New("missing managed test runner for " + stepID)
		}
		if setter, ok := runner.(interface{ setWorkspace(string) }); ok {
			setter.setWorkspace(workspace)
		}
		file := ""
		if writing, ok := runner.(*managedWritingRunner); ok {
			file = writing.file
		}
		if err := capture.remember(stepID, workspace, sourcePath, file); err != nil {
			return nil, err
		}
		return runner, nil
	}
	collectStageableFilesFn = func(_ context.Context, mountPath string, _ ...string) ([]string, error) {
		capture.mu.Lock()
		file := capture.files[mountPath]
		capture.mu.Unlock()
		if file == "" {
			return []string{}, nil
		}
		if _, err := os.Stat(filepath.Join(mountPath, file)); err != nil {
			return []string{}, nil
		}
		return []string{file}, nil
	}
	collectGitDiffFn = func(context.Context, string) (string, error) { return "", nil }
	commitTerrariumExecutionFn = originalCommit
	mergeTerrariumCommitFn = func(context.Context, string, string) error { return nil }
}

func assertManagedBaseClean(t *testing.T, repository string) {
	t.Helper()
	branch, err := runGitCommand(context.Background(), repository, "branch", "--show-current")
	if err != nil {
		t.Fatalf("read managed base branch: %v", err)
	}
	if strings.TrimSpace(branch) != "main" {
		t.Fatalf("managed base branch = %q, want main", strings.TrimSpace(branch))
	}
	assertGitClean(t, repository)
}

func TestRunSproutManagedRunUsesIndependentWorkspaceAndBackingSource(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepID := "managed-source"
	baseCommit, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve managed base commit: %v", err)
	}
	runner := newManagedWritingRunner("sprout.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	originalCreateWorkspace := createRunWorkspaceFn
	t.Cleanup(func() { createRunWorkspaceFn = originalCreateWorkspace })
	createRunWorkspaceFn = func(ctx context.Context, sourcePath, gotStepID, startRevision string) (RunWorkspace, error) {
		if sourcePath != repository || gotStepID != stepID || strings.TrimSpace(startRevision) != strings.TrimSpace(baseCommit) {
			t.Fatalf("CreateRunWorkspace args = (%q, %q, %q), want (%q, %q, %q)", sourcePath, gotStepID, startRevision, repository, stepID, strings.TrimSpace(baseCommit))
		}
		return CreateRunWorkspace(ctx, sourcePath, gotStepID, startRevision)
	}

	stashCalls := 0
	mergeCalls := 0
	originalStash := stashHostWorkspaceFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		stashHostWorkspaceFn = originalStash
		mergeTerrariumCommitFn = originalMerge
	})
	stashHostWorkspaceFn = func(context.Context, string, string) (bool, error) {
		stashCalls++
		return false, nil
	}
	mergeTerrariumCommitFn = func(context.Context, string, string) error {
		mergeCalls++
		return nil
	}

	report, err := (&DockerOrchestrator{
		Substrate:  repository,
		StepID:     stepID,
		StatusPath: filepath.Join(repository, "tendril-status.json"),
	}).RunSprout(context.Background(), "write one file")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}

	mount, source, branch := capture.get(stepID)
	if mount == "" || mount == repository {
		t.Fatalf("mount = %q, want a run workspace distinct from %q", mount, repository)
	}
	if source != repository {
		t.Fatalf("source = %q, want persistent managed checkout %q", source, repository)
	}
	if branch != "sprout/task-"+stepID {
		t.Fatalf("run branch = %q, want sprout/task-%s", branch, stepID)
	}
	if stashCalls != 0 {
		t.Fatalf("managed run stashed its base %d time(s)", stashCalls)
	}
	if mergeCalls != 0 {
		t.Fatalf("managed run published Fruit directly to its persistent base %d time(s)", mergeCalls)
	}
	assertManagedBaseClean(t, repository)
	if _, err := os.Stat(filepath.Join(repository, "sprout.txt")); !os.IsNotExist(err) {
		t.Fatalf("Sprout file reached managed base, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, ".tendril", "genome", repositoryMapFile)); !os.IsNotExist(err) {
		t.Fatalf("repo map reached managed base, stat err = %v", err)
	}
	statusPath := filepath.Join(repository, "tendril-status.json")
	if status, err := os.ReadFile(statusPath); err != nil {
		t.Fatalf("managed run status was not written at the caller path: %v", err)
	} else if !strings.Contains(string(status), stepID) {
		t.Fatalf("managed run status missing step ID %q: %s", stepID, status)
	}
	if _, err := runGitCommand(context.Background(), repository, "show", "sprout/task-"+stepID+":sprout.txt"); err != nil {
		t.Fatalf("run Fruit branch does not contain Sprout file: %v", err)
	}
}

func TestCopyMycorrhizalCacheCopiesAbsentDestination(t *testing.T) {
	source := t.TempDir()
	runPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "vendor"), 0o755); err != nil {
		t.Fatalf("create source cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "vendor", "cache.txt"), []byte("host cache\n"), 0o644); err != nil {
		t.Fatalf("write source cache: %v", err)
	}

	copied, err := copyMycorrhizalCache(context.Background(), source, runPath)
	if err != nil {
		t.Fatalf("copy absent cache: %v", err)
	}
	want := filepath.Join(runPath, "vendor")
	if len(copied) != 1 || copied[0] != want {
		t.Fatalf("copied cache paths = %v, want [%q]", copied, want)
	}
	if _, err := os.Stat(filepath.Join(runPath, "vendor", "vendor")); !os.IsNotExist(err) {
		t.Fatalf("absent destination was nested as vendor/vendor, stat err = %v", err)
	}
	assertFileContents(t, filepath.Join(runPath, "vendor", "cache.txt"), "host cache\n")

	state, err := newRunWorkspaceCacheState(runPath, want)
	if err != nil {
		t.Fatalf("snapshot copied cache: %v", err)
	}
	if err := state.cleanup(); err != nil {
		t.Fatalf("cleanup copied cache: %v", err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("copied cache was not removed, stat err = %v", err)
	}
}

func TestCopyMycorrhizalCacheLeavesExistingDestinationUntouched(t *testing.T) {
	source := t.TempDir()
	runPath := t.TempDir()
	for _, root := range []string{source, runPath} {
		if err := os.MkdirAll(filepath.Join(root, "vendor"), 0o755); err != nil {
			t.Fatalf("create %s vendor: %v", root, err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "vendor", "cache.txt"), []byte("host cache\n"), 0o644); err != nil {
		t.Fatalf("write source cache: %v", err)
	}
	existing := filepath.Join(runPath, "vendor", "cache.txt")
	if err := os.WriteFile(existing, []byte("tracked dependency\n"), 0o644); err != nil {
		t.Fatalf("write existing run-worktree cache: %v", err)
	}

	copied, err := copyMycorrhizalCache(context.Background(), source, runPath)
	if err != nil {
		t.Fatalf("copy over existing cache: %v", err)
	}
	if len(copied) != 0 {
		t.Fatalf("existing cache was registered as disposable: %v", copied)
	}
	if _, err := os.Stat(filepath.Join(runPath, "vendor", "vendor")); !os.IsNotExist(err) {
		t.Fatalf("existing vendor was nested as vendor/vendor, stat err = %v", err)
	}
	assertFileContents(t, existing, "tracked dependency\n")
}

func TestManagedRunCopiesMycorrhizalCacheWithoutMutatingBase(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	cachePath := filepath.Join(repository, "vendor", "cache.txt")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("create cache directory: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("cached dependency\n"), 0o644); err != nil {
		t.Fatalf("write cache fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".git", "info", "exclude"), []byte("/vendor/\n"), 0o644); err != nil {
		t.Fatalf("ignore cache fixture: %v", err)
	}

	stepID := "managed-cache"
	runner := newManagedWritingRunner("cache-consumer.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	if _, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "use cache"); err != nil {
		t.Fatalf("managed cache RunSprout: %v", err)
	}
	if !capture.cacheVisible(stepID) {
		mount, _, _ := capture.get(stepID)
		entries, _ := os.ReadDir(filepath.Join(mount, "vendor"))
		t.Fatalf("managed RunWorkspace did not receive the copied Mycorrhizal cache at %q: %v", mount, entries)
	}
	mount, _, _ := capture.get(stepID)
	if _, err := os.Stat(mount); !os.IsNotExist(err) {
		t.Fatalf("unchanged copied cache left its run workspace behind, stat err = %v", err)
	}
	assertFileContents(t, cachePath, "cached dependency\n")
	assertManagedBaseClean(t, repository)
}

func TestManagedRunDoesNotCopyOverTrackedVendor(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	cachePath := filepath.Join(repository, "vendor", "cache.txt")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("create tracked vendor directory: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("tracked dependency\n"), 0o644); err != nil {
		t.Fatalf("write tracked vendor cache: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repository, "add", "vendor/cache.txt"); err != nil {
		t.Fatalf("stage tracked vendor cache: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repository, "commit", "-q", "-m", "tracked vendor"); err != nil {
		t.Fatalf("commit tracked vendor cache: %v", err)
	}

	var copied []string
	originalCopy := copyMycorrhizalCacheFn
	t.Cleanup(func() { copyMycorrhizalCacheFn = originalCopy })
	copyMycorrhizalCacheFn = func(ctx context.Context, sourcePath, runPath string) ([]string, error) {
		paths, err := originalCopy(ctx, sourcePath, runPath)
		copied = append([]string(nil), paths...)
		return paths, err
	}

	stepID := "tracked-vendor"
	probe := &managedTrackedCacheProbeRunner{observed: make(chan error, 1)}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: probe})
	if _, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "use tracked vendor"); err != nil {
		t.Fatalf("managed tracked vendor RunSprout: %v", err)
	}
	if err := <-probe.observed; err != nil {
		t.Fatal(err)
	}
	for _, path := range copied {
		if filepath.Base(path) == "vendor" {
			t.Fatalf("tracked vendor was registered as a disposable cache: %v", copied)
		}
	}
	mount, _, _ := capture.get(stepID)
	if _, err := os.Stat(mount); !os.IsNotExist(err) {
		t.Fatalf("tracked vendor run left its workspace behind, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, "vendor", "vendor")); !os.IsNotExist(err) {
		t.Fatalf("managed base grew a nested vendor/vendor, stat err = %v", err)
	}
	assertFileContents(t, cachePath, "tracked dependency\n")
	assertManagedBaseClean(t, repository)
}

func TestManagedRunPreservesModifiedCopiedCache(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	cachePath := filepath.Join(repository, "vendor", "cache.txt")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("create ignored vendor directory: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("host cache\n"), 0o644); err != nil {
		t.Fatalf("write ignored vendor cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".git", "info", "exclude"), []byte("/vendor/\n"), 0o644); err != nil {
		t.Fatalf("ignore vendor cache: %v", err)
	}

	stepID := "modified-copied-cache"
	runner := newManagedWritingRunner("vendor/cache.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	if _, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "modify copied cache"); err == nil {
		t.Fatal("modified copied cache cleanup succeeded; it should preserve the run workspace")
	}
	mount, _, _ := capture.get(stepID)
	if _, err := os.Stat(mount); err != nil {
		t.Fatalf("modified copied cache workspace was removed: %v", err)
	}
	assertFileContents(t, cachePath, "host cache\n")
	assertManagedBaseClean(t, repository)

	// This workspace is intentionally preserved for review; remove only the
	// known run-owned worktree after restoring its generated cache content.
	if err := os.WriteFile(filepath.Join(mount, "vendor", "cache.txt"), []byte("host cache\n"), 0o644); err != nil {
		t.Fatalf("restore preserved cache before cleanup: %v", err)
	}
	baseCommit, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve managed base commit for cleanup: %v", err)
	}
	workspace := RunWorkspace{
		Repository: repository,
		Path:       mount,
		Branch:     "sprout/task-" + stepID,
		BaseCommit: strings.TrimSpace(baseCommit),
	}
	owned, ok := runWorkspaceOwnedRef(repository, workspace.Branch, workspace.BaseCommit)
	if !ok {
		t.Fatal("preserved run workspace ownership was not recorded")
	}
	workspace.RunID = owned.RunID
	if err := workspace.Cleanup(context.Background(), ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup preserved cache workspace after restoring it: %v", err)
	}
}

func TestManagedRunDoesNotHoldBaseGitLockDuringSproutExecution(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepID := "lock-free-execution"
	probe := &managedLockProbeRunner{repository: repository, observed: make(chan bool, 1)}
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: probe})
	if _, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "probe execution lock"); err != nil {
		t.Fatalf("managed lock probe RunSprout: %v", err)
	}
	if !<-probe.observed {
		t.Fatal("RunWorkspace Git metadata lock remained held during autonomous Sprout execution")
	}
}

func TestManagedRunWorkspacesOverlapAndCleanupIsIndependent(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	firstID := "overlap-a"
	secondID := "overlap-b"
	first := newManagedWritingRunner("a.txt")
	second := newManagedWritingRunner("b.txt")
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{firstID: first, secondID: second})

	type result struct {
		report SproutRunReport
		err    error
	}
	results := make(chan result, 2)
	for _, stepID := range []string{firstID, secondID} {
		go func(stepID string) {
			report, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "overlap")
			results <- result{report: report, err: err}
		}(stepID)
	}

	<-first.started
	<-second.started
	firstMount, firstSource, firstBranch := capture.get(firstID)
	secondMount, secondSource, secondBranch := capture.get(secondID)
	if firstMount == "" || secondMount == "" || firstMount == secondMount {
		t.Fatalf("live mounts = %q and %q, want distinct run workspaces", firstMount, secondMount)
	}
	if firstSource != repository || secondSource != repository {
		t.Fatalf("live sources = %q and %q, want managed base %q", firstSource, secondSource, repository)
	}
	if firstBranch != "sprout/task-"+firstID || secondBranch != "sprout/task-"+secondID {
		t.Fatalf("live branches = %q and %q, want step-scoped branches", firstBranch, secondBranch)
	}
	assertManagedBaseClean(t, repository)

	first.releaseRun()
	second.releaseRun()
	for range []int{0, 1} {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("overlapping RunSprout: %v", outcome.err)
		}
		if outcome.report.Outcome != SproutOutcomeComplete {
			t.Fatalf("overlapping outcome = %q, want %q", outcome.report.Outcome, SproutOutcomeComplete)
		}
	}
	if _, err := os.Stat(firstMount); !os.IsNotExist(err) {
		t.Fatalf("cleanup of run A left its workspace at %q, stat err = %v", firstMount, err)
	}
	if _, err := os.Stat(secondMount); !os.IsNotExist(err) {
		t.Fatalf("cleanup of run B left its workspace at %q, stat err = %v", secondMount, err)
	}
	if _, err := runGitCommand(context.Background(), repository, "show", "sprout/task-"+firstID+":a.txt"); err != nil {
		t.Fatalf("run A Fruit missing after run B cleanup: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repository, "show", "sprout/task-"+secondID+":b.txt"); err != nil {
		t.Fatalf("run B Fruit missing after run A cleanup: %v", err)
	}
	assertManagedBaseClean(t, repository)
}

func TestManagedRunDetachedWorkspaceLivesUntilTerminalEnding(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), `
substrates:
  managed:
    checkout:
      mode: managed
    patience:
      growth: 1s
`)
	runner := newManagedWritingRunner("")
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{"detached-managed": runner})
	terminal := make(chan struct{}, 1)

	report, err := (&DockerOrchestrator{
		Substrate:  "managed",
		StepID:     "detached-managed",
		OnTerminal: func(SproutRunReport, error) { terminal <- struct{}{} },
	}).RunSprout(context.Background(), "hold managed run")
	if err != nil {
		t.Fatalf("detached managed RunSprout: %v", err)
	}
	if report.Outcome != SproutOutcomeDetached {
		t.Fatalf("outcome = %q, want %q", report.Outcome, SproutOutcomeDetached)
	}
	mount, source, branch := capture.get("detached-managed")
	if mount == "" || source != repository || branch != "sprout/task-detached-managed" {
		t.Fatalf("detached capture = mount %q source %q branch %q", mount, source, branch)
	}
	if _, err := os.Stat(mount); err != nil {
		t.Fatalf("detached RunWorkspace disappeared before terminal ending: %v", err)
	}
	assertManagedBaseClean(t, repository)

	runner.releaseRun()
	select {
	case <-terminal:
	case <-time.After(5 * time.Second):
		t.Fatal("detached managed run never reached terminal ending")
	}
	if _, err := os.Stat(mount); !os.IsNotExist(err) {
		t.Fatalf("detached RunWorkspace was not cleaned after terminal ending, stat err = %v", err)
	}
}

func TestManagedRunFailureCleansItsGeneratedState(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepID := "managed-preflight-failure"
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{})
	originalRepoMap := generateRepoMapFn
	t.Cleanup(func() { generateRepoMapFn = originalRepoMap })
	generateRepoMapFn = func(_ context.Context, mountPath string) (string, error) {
		path := filepath.Join(mountPath, tendrilStateDirectory, "genome", repositoryMapFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte("generated before failure\n"), 0o644); err != nil {
			return "", err
		}
		return "", errors.New("controlled repository-map failure")
	}

	if _, err := (&DockerOrchestrator{Substrate: repository, StepID: stepID, DisableMergeBack: true}).RunSprout(context.Background(), "fail after allocation"); err == nil {
		t.Fatal("managed preflight failure returned nil")
	}
	if branchExists(t, repository, "sprout/task-"+stepID) {
		t.Fatal("managed preflight failure left its run branch behind")
	}
	assertManagedBaseClean(t, repository)
	if _, err := os.Stat(filepath.Join(repository, ".tendril")); !os.IsNotExist(err) {
		t.Fatalf("managed preflight failure left generated Tendril state in base, stat err = %v", err)
	}
}

func TestRunSproutRemoteManagedCheckoutUsesRunWorkspace(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	managedRoot := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	source := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "remote@example.invalid"},
		{"config", "user.name", "Remote Managed Test"},
	} {
		if _, err := runGitCommand(ctx, source, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatalf("write remote seed: %v", err)
	}
	if _, err := runGitCommand(ctx, source, "add", "remote.txt"); err != nil {
		t.Fatalf("stage remote seed: %v", err)
	}
	if _, err := runGitCommand(ctx, source, "commit", "-q", "-m", "remote seed"); err != nil {
		t.Fatalf("commit remote seed: %v", err)
	}
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), "substrates:\n  remote:\n    url: "+source+"\n    branch: main\n    checkout:\n      mode: managed\n")

	stepID := "remote-managed"
	runner := newManagedWritingRunner("")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	if _, err := (&DockerOrchestrator{Substrate: "remote", StepID: stepID, DisableMergeBack: true}).RunSprout(ctx, "read remote"); err != nil {
		t.Fatalf("remote managed RunSprout: %v", err)
	}

	mount, backing, branch := capture.get(stepID)
	wantBacking := filepath.Join(managedRoot, "remote")
	if backing != wantBacking {
		t.Fatalf("remote source = %q, want persistent managed checkout %q", backing, wantBacking)
	}
	if mount == backing {
		t.Fatalf("remote managed mount reused persistent checkout %q", mount)
	}
	if branch != "sprout/task-"+stepID {
		t.Fatalf("remote run branch = %q, want sprout/task-%s", branch, stepID)
	}
	assertManagedBaseClean(t, backing)
}

func TestRunSproutRemoteEphemeralCheckoutKeepsExistingLifecycle(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	chdirToTempDir(t)

	source := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "ephemeral@example.invalid"},
		{"config", "user.name", "Ephemeral Test"},
		{"commit", "--allow-empty", "-q", "-m", "seed"},
	} {
		if _, err := runGitCommand(ctx, source, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), "substrates:\n  ephemeral:\n    url: "+source+"\n    branch: main\n    checkout:\n      mode: ephemeral\n")

	stepID := "remote-ephemeral"
	runner := newManagedWritingRunner("")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})
	originalCreateWorkspace := createRunWorkspaceFn
	t.Cleanup(func() { createRunWorkspaceFn = originalCreateWorkspace })
	createRunWorkspaceFn = func(context.Context, string, string, string) (RunWorkspace, error) {
		t.Fatal("ephemeral checkout was routed through RunWorkspace")
		return RunWorkspace{}, nil
	}

	if _, err := (&DockerOrchestrator{Substrate: "ephemeral", StepID: stepID, DisableMergeBack: true}).RunSprout(ctx, "read ephemeral"); err != nil {
		t.Fatalf("ephemeral RunSprout: %v", err)
	}
	mount, sourcePath, _ := capture.get(stepID)
	if mount == "" || !strings.HasPrefix(mount, os.TempDir()) {
		t.Fatalf("ephemeral mount = %q, want a temporary checkout", mount)
	}
	if sourcePath != mount {
		t.Fatalf("ephemeral source = %q, mount = %q; existing ephemeral path should remain direct", sourcePath, mount)
	}
	if isManagedCheckoutPath(mount) {
		t.Fatalf("ephemeral mount %q was classified as managed", mount)
	}
	if _, err := os.Stat(mount); !os.IsNotExist(err) {
		t.Fatalf("ephemeral checkout was not removed after the run, stat err = %v", err)
	}
}

func TestCheckoutPathStillUsesShadowWorktree(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	repository := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "path@example.invalid"},
		{"config", "user.name", "Path Checkout Test"},
		{"commit", "--allow-empty", "-q", "-m", "seed"},
	} {
		if _, err := runGitCommand(ctx, repository, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	managedRoot := os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT")
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"), "substrates:\n  local:\n    path: "+repository+"\n    checkout:\n      mode: path\n      path: "+repository+"\n")

	originalPreflight := runSproutPreflightChecksFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsure := ensureSproutImageFn
	originalShadow := createShadowWorktreeFn
	originalRemove := removeShadowWorktreeFn
	originalStart := startTerrariumSessionFn
	originalNew := newSproutFn
	originalCollect := collectStageableFilesFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsure
		createShadowWorktreeFn = originalShadow
		removeShadowWorktreeFn = originalRemove
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNew
		collectStageableFilesFn = originalCollect
	})
	runSproutPreflightChecksFn = func(_ context.Context, _ *llm.Client) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	shadowPath := filepath.Join(t.TempDir(), "shadow")
	if err := os.MkdirAll(shadowPath, 0o755); err != nil {
		t.Fatalf("mkdir shadow: %v", err)
	}
	shadowCalls := 0
	createShadowWorktreeFn = func(sourcePath, branch string) (string, error) {
		shadowCalls++
		return shadowPath, nil
	}
	removeShadowWorktreeFn = func(sourcePath, path string) {}
	var mounted string
	startTerrariumSessionFn = func(_ context.Context, _ string, _ string, mountPath string, _ bool, _ []string, _ []string, _ time.Duration, _ ...terrarium.ActivationObserver) (toolSession, error) {
		mounted = mountPath
		return &stubToolSession{}, nil
	}
	newSproutFn = func(context.Context, string, string, string, llmCaller, toolSession, *eventbus.Bus, string, string) (sproutRunner, error) {
		return &stubSproutRunner{result: sproutResult{Response: "path"}}, nil
	}
	collectStageableFilesFn = func(context.Context, string, ...string) ([]string, error) { return []string{}, nil }

	if _, err := (&DockerOrchestrator{Substrate: "local", StepID: "path-unchanged", DisableMergeBack: true}).RunSprout(ctx, "path"); err != nil {
		t.Fatalf("path RunSprout: %v", err)
	}
	if shadowCalls != 1 || mounted != shadowPath {
		t.Fatalf("path execution used shadow %q %d time(s), want %q exactly once; managed root was %q", mounted, shadowCalls, shadowPath, managedRoot)
	}
}
