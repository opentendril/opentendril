package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

func TestRunSeedRound19SalvagesAndRepairsPartialCandidateAfterTurnLimit(t *testing.T) {
	prepareManagedRunRepository(t)
	stubLocalStoma(t)
	restoreSeeds(t)
	repository := filepath.Join(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT"), "managed")
	base, err := runGitCommand(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	base = strings.TrimSpace(base)

	var prompts []string
	var verifiedCandidates []string
	var reports []SproutRunReport
	var runErrors []error
	var startRevisions []string
	var sessions []*round19WriteSession
	var clients []*refusingLLM
	var sprouts []*Sprout
	iteration := 0
	turnLimitCall := `{"tool":"execCommand","arguments":{"command":"continue"}}`

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
				`{"final":"wrote candidate"}`,
			}
		} else {
			responses = []string{
				`{"tool":"writeFile","arguments":{"path":"HELLO.md","content":"Hello from OpenTendril.\n"}}`,
			}
		}
		client := &refusingLLM{
			fakeLLM:        fakeLLM{responses: responses, response: turnLimitCall},
			refusalMessage: "tools unsupported for this model",
		}
		clients = append(clients, client)
		sprout, err := newSprout(ctx, workspace, genotypeRoot, genotypeName, client, session, bus, stepID, sessionID)
		if err == nil {
			sprouts = append(sprouts, sprout)
		}
		return sprout, err
	}
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		startRevisions = append(startRevisions, strings.TrimSpace(orch.SeedStartRevision))
		prompts = append(prompts, prompt)
		report, err := orch.RunSprout(ctx, prompt)
		reports = append(reports, report)
		runErrors = append(runErrors, err)
		return report, err
	}
	seedVerifyFn = func(ctx context.Context, sourcePath, candidate string, verify, egress []string) seedVerifyReport {
		verifiedCandidates = append(verifiedCandidates, candidate)
		return runSeedVerify(ctx, sourcePath, candidate, verify, egress)
	}

	bus := eventbus.New()
	defer bus.Shutdown()
	events := recordSproutLifecycle(bus)

	result, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repository,
		Goal:          "create HELLO.md",
		Verify:        round16HelloVerifyArgv(),
		MaxIterations: 2,
		SessionID:     "seed-round-19-turn-limit",
		EventBus:      bus,
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if result.Status != SeedStatusSatisfied || result.Iterations != 2 {
		t.Fatalf("result = %+v, want satisfied after exactly two iterations", result)
	}
	if iteration != 2 || len(prompts) != 2 || len(verifiedCandidates) != 2 || len(reports) != 2 || len(startRevisions) != 2 || len(sessions) != 2 || len(clients) != 2 || len(sprouts) != 2 {
		t.Fatalf("Seed execution counts = iterations:%d prompts:%d candidates:%d reports:%d starts:%d sessions:%d clients:%d sprouts:%d, want exactly two of each", iteration, len(prompts), len(verifiedCandidates), len(reports), len(startRevisions), len(sessions), len(clients), len(sprouts))
	}
	if runErrors[0] != nil || !errors.Is(runErrors[1], errSproutTurnLimit) || runErrors[1].Error() != "Sprout reached max iterations (20)" {
		t.Fatalf("Sprout errors = %v, want a clean first run and Sprout.Run's typed 20-turn limit", runErrors)
	}
	if len(clients[1].calls) != sproutMaxIterations {
		t.Fatalf("second Sprout provider turns = %d, want the real %d-turn budget", len(clients[1].calls), sproutMaxIterations)
	}
	if reports[0].Outcome != SproutOutcomeComplete || reports[0].FruitBranch != "" || reports[0].FruitCommit != "" || reports[0].seedCandidateCommit == "" {
		t.Fatalf("first Sprout report = %+v, want a completed internal Seed candidate", reports[0])
	}
	if reports[1].Outcome != SproutOutcomeFailed || reports[1].FruitBranch != "" || reports[1].FruitCommit != "" || reports[1].seedCandidateCommit == "" {
		t.Fatalf("second Sprout report = %+v, want a failed internal Seed candidate", reports[1])
	}
	firstCandidate := reports[0].seedCandidateCommit
	secondCandidate := reports[1].seedCandidateCommit
	if startRevisions[0] != base || startRevisions[1] != firstCandidate {
		t.Fatalf("Seed start revisions = %q, want base %q then immutable first candidate %q", startRevisions, base, firstCandidate)
	}
	if verifiedCandidates[0] != firstCandidate || verifiedCandidates[1] != secondCandidate {
		t.Fatalf("verified candidates = %q, want the two salvaged checkpoint commits %q and %q", verifiedCandidates, firstCandidate, secondCandidate)
	}
	if firstCandidate == secondCandidate {
		t.Fatalf("candidate identity did not advance after repair: %q", firstCandidate)
	}
	firstContents, err := runGitCommandRawOutput(context.Background(), repository, "show", firstCandidate+":HELLO.md")
	if err != nil {
		t.Fatalf("read first immutable Seed candidate: %v", err)
	}
	secondContents, err := runGitCommandRawOutput(context.Background(), repository, "show", secondCandidate+":HELLO.md")
	if err != nil {
		t.Fatalf("read repaired Seed candidate: %v", err)
	}
	if firstContents != "Hello from OpenTendril." || secondContents != "Hello from OpenTendril.\n" {
		t.Fatalf("candidate contents = %q then %q, want immutable partial then repaired final-newline content", firstContents, secondContents)
	}
	if len(sessions[0].calls) != 1 || sessions[0].calls[0].Tool != "writeFile" || len(sessions[1].calls) != 1 || sessions[1].calls[0].Tool != "writeFile" || sessions[1].calls[0].Arguments["content"] != "Hello from OpenTendril.\n" {
		t.Fatalf("Terrarium calls = first:%+v second:%+v, want one partial write then one repaired write", sessions[0].calls, sessions[1].calls)
	}
	if reports[1].ToolInvocations != sproutMaxIterations || sprouts[1].boundaryFailure.Load() {
		t.Fatalf("second Sprout execution = tool invocations:%d boundary failure:%v, want %d handled calls and no boundary failure", reports[1].ToolInvocations, sprouts[1].boundaryFailure.Load(), sproutMaxIterations)
	}
	if !strings.Contains(result.Logs, "sprout withered") || !strings.Contains(result.Logs, "Sprout reached max iterations (20)") {
		t.Fatalf("turn-limit Sprout failure was not preserved in Seed logs:\n%s", result.Logs)
	}
	withered := filterEvents(*events, eventbus.EventSproutWithered)
	if len(withered) != 1 || withered[0].Data["outcome"] != SproutOutcomeFailed {
		t.Fatalf("withered lifecycle events = %+v, want one failed Sprout event", withered)
	}
	if len(result.VerificationDiagnostics) != 2 || result.VerificationDiagnostics[0].ExitCode == nil || *result.VerificationDiagnostics[0].ExitCode != 1 || result.VerificationDiagnostics[1].ExitCode == nil || *result.VerificationDiagnostics[1].ExitCode != 0 {
		t.Fatalf("verification diagnostics = %+v, want deterministic exit 1 then 0", result.VerificationDiagnostics)
	}
	if result.Branch == "" || result.Commit != secondCandidate {
		t.Fatalf("Seed Fruit identity = branch:%q commit:%q, want the repaired checkpoint commit %q", result.Branch, result.Commit, secondCandidate)
	}
	content, err := runGitCommandRawOutput(context.Background(), repository, "show", result.Commit+":HELLO.md")
	if err != nil {
		t.Fatalf("read final HELLO.md: %v", err)
	}
	if content != "Hello from OpenTendril.\n" {
		t.Fatalf("final HELLO.md = %q, want a final newline", content)
	}
}
