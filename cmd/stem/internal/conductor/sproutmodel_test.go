package conductor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

// clearLLMEnv removes every provider signal so a test states its own. The local
// endpoint variables count: local availability is decided from configuration
// exactly as a keyed provider's is.
func clearLLMEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"DEFAULT_LLM_PROVIDER", "DEFAULT_MODEL_NAME",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GROK_API_KEY", "OPENROUTER_API_KEY", "NVIDIA_API_KEY",
		"LOCAL_INFERENCE_URL", "LOCAL_MODEL_NAME",
		"GOOGLE_MODEL_NAME", "GOOGLE_PREMIUM_MODEL", "GOOGLE_STANDARD_MODEL", "GOOGLE_CHEAPEST_MODEL",
		"GROK_MODEL_NAME", "GROK_PREMIUM_MODEL", "GROK_STANDARD_MODEL", "GROK_CHEAPEST_MODEL",
	} {
		t.Setenv(key, "")
	}

	// The discovered-model registry is process-global and outlives the test
	// that filled it. A test that ran earlier with only a local endpoint
	// declared leaves a cache containing local models and nothing else, so a
	// later test asking for another provider's model finds none — a failure
	// that depends on test order rather than on the code under test.
	llm.ResetModelRegistryCache()
	t.Cleanup(llm.ResetModelRegistryCache)

	// This repository ships a .tendril/config.yaml pinning a local model, and
	// resolution walks up from the working directory to find it. A resolution
	// test run from the source tree would otherwise be reading the repository's
	// own configuration rather than its own.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// stubSproutRun replaces every seam between RunSprout and a real container, so
// the run reaches its report without Docker. It returns the workspace the run
// was given a mind for.
func stubSproutRun(t *testing.T, capture func(client llmCaller)) {
	t.Helper()

	origPreflight := runSproutPreflightChecksFn
	origProbe := probeProviderAuthFn
	origRepoMap := generateRepoMapFn
	origMemoryMap := generateMemoryMapFn
	origEnsureImage := ensureSproutImageFn
	origStartSession := startTerrariumSessionFn
	origNewSprout := newSproutFn
	origCollectFiles := collectStageableFilesFn
	origCollectDiff := collectGitDiffFn
	origCommit := commitTerrariumExecutionFn
	origMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = origPreflight
		probeProviderAuthFn = origProbe
		generateRepoMapFn = origRepoMap
		generateMemoryMapFn = origMemoryMap
		ensureSproutImageFn = origEnsureImage
		startTerrariumSessionFn = origStartSession
		newSproutFn = origNewSprout
		collectStageableFilesFn = origCollectFiles
		collectGitDiffFn = origCollectDiff
		commitTerrariumExecutionFn = origCommit
		mergeTerrariumCommitFn = origMerge
	})

	runSproutPreflightChecksFn = func(ctx context.Context, _ *llm.Client) error { return nil }
	probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	generateRepoMapFn = func(ctx context.Context, dir string) (string, error) { return "", nil }
	generateMemoryMapFn = func(ctx context.Context, dir string) (string, error) { return "", nil }
	ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return &terrariumToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		if capture != nil {
			capture(client)
		}
		return &mockSproutRunner{response: "done"}, nil
	}
	collectStageableFilesFn = func(ctx context.Context, mountPath string, excludedPaths ...string) ([]string, error) {
		return []string{"a.go"}, nil
	}
	collectGitDiffFn = func(ctx context.Context, mountPath string) (string, error) { return "", nil }
	commitTerrariumExecutionFn = func(ctx context.Context, shadowPath, sourcePath, statusPath string, execution sproutExecutionStatus, prompt string, cred ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		return "hash", nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, hostPath, commitHash string) error { return nil }
}

func newSproutWorkspace(t *testing.T) string {
	t.Helper()

	workdir := t.TempDir()
	ctx := context.Background()
	if _, err := runGitCommand(ctx, workdir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := runGitCommand(ctx, workdir, "config", "user.email", "sprout@example.invalid"); err != nil {
		t.Fatalf("git config user.email: %v", err)
	}
	if _, err := runGitCommand(ctx, workdir, "config", "user.name", "Sprout"); err != nil {
		t.Fatalf("git config user.name: %v", err)
	}
	if _, err := runGitCommand(ctx, workdir, "commit", "--allow-empty", "-m", "init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return workdir
}

func TestRunSproutLocalModelUnavailableStopsBeforeTerrarium(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_MODEL_NAME", "missing-model")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"present-model"}]}`))
	}))
	defer server.Close()
	t.Setenv("LOCAL_INFERENCE_URL", server.URL+"/v1")

	stubSproutRun(t, nil)
	preflightCalled := false
	runSproutPreflightChecksFn = func(ctx context.Context, mind *llm.Client) error {
		preflightCalled = true
		return checkLocalInferenceReachable(ctx, mind)
	}
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		t.Fatal("Terrarium started before the selected local model was proven available")
		return nil, nil
	}

	orch := NewDockerOrchestrator()
	orch.Substrate = newSproutWorkspace(t)
	orch.StepID = "step-missing-local-model"
	orch.DisableMergeBack = true
	_, err := orch.RunSprout(context.Background(), "do the thing")
	if err == nil {
		t.Fatal("RunSprout() error = nil, want model-unavailable preflight failure")
	}
	if !preflightCalled {
		t.Fatal("local provider preflight was not called")
	}
	var reachabilityErr *llm.ProviderReachabilityError
	if !errors.As(err, &reachabilityErr) {
		t.Fatalf("RunSprout() error = %v, want typed reachability error", err)
	}
	if reachabilityErr.FailureClass() != llm.ReachabilityFailureModelUnavailable {
		t.Fatalf("failure class = %q, want model-unavailable", reachabilityErr.FailureClass())
	}
}

// A run that pinned no model must still be able to say which model carried it,
// and that name must be the one the client will actually put on the wire. A
// report that merely echoed the request would read null for every autonomous
// run — which is exactly what the published measurement could not answer.
func TestRunSproutReportsTheModelItsClientWillUse(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	var clientProvider, clientModel string
	stubSproutRun(t, func(client llmCaller) {
		resolved, ok := client.(*llm.Client)
		if !ok {
			t.Fatalf("sprout received a %T, want the resolved *llm.Client", client)
		}
		clientProvider = resolved.Provider()
		clientModel = resolved.Model()
	})

	orch := NewDockerOrchestrator()
	orch.Substrate = newSproutWorkspace(t)
	orch.StepID = "step-model"
	orch.DisableMergeBack = true

	report, err := orch.RunSprout(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Model == "" {
		t.Fatal("report names no model; a run whose model is unknown cannot be checked against a provider's usage")
	}
	// The cheapest tool-capable model google serves, because this run set no
	// tier and the unconfigured default no longer reaches for premium.
	if report.Provider != "google" || report.Model != "gemini-3.5-flash" {
		t.Fatalf("report = %s/%s, want google/gemini-3.5-flash", report.Provider, report.Model)
	}
	// The report is only worth anything if it names the mind that ran. Asserting
	// the field alone would pass just as well if the report were assembled from
	// a second, independent resolution that happened to agree.
	if report.Provider != clientProvider || report.Model != clientModel {
		t.Fatalf("report = %s/%s but the sprout was handed %s/%s", report.Provider, report.Model, clientProvider, clientModel)
	}
}

// A configured provider that resolves to nothing must stop the run, and stop it
// before anything has been built: no terrarium, no worktree, no stashed host
// workspace. Resolving elsewhere instead is the defect, and doing it quietly at
// the far end of a container start is the expensive version of it.
func TestRunSproutRefusesAnUnresolvableProviderBeforeBuildingAnything(t *testing.T) {
	clearLLMEnv(t)
	// grok is named and its key is absent, so the provider is not available at
	// any tier — the ordinary shape of this mistake, and one no relaxation can
	// rescue. anthropic, whose key IS present, is where an unconstrained
	// fallback would go instead.
	t.Setenv("DEFAULT_LLM_PROVIDER", "grok")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	built := false
	stubSproutRun(t, func(llmCaller) { built = true })

	runSproutPreflightChecksFn = func(ctx context.Context, _ *llm.Client) error { return nil }
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		t.Fatal("a terrarium was started for a run with no resolvable model")
		return nil, nil
	}

	// The refusal is ordered by what it protects, not by how early it can be
	// made. Reads may precede it — preflight and substrate resolution both
	// spend nothing, and their failures are more specific than "no model
	// resolved", so a run misconfigured in both ways must report theirs.
	// Nothing that MUTATES may precede it, and these are the mutations.
	worktreeCreated := false
	createShadowWorktreeFn = func(repoPath, branch string) (string, error) {
		worktreeCreated = true
		return "", fmt.Errorf("no shadow worktree should be created for a run with no resolvable model")
	}
	hostStashed := false
	stashHostWorkspaceFn = func(ctx context.Context, workspace, stepID string) (bool, error) {
		hostStashed = true
		return false, fmt.Errorf("the host workspace should not be stashed for a run with no resolvable model")
	}

	orch := NewDockerOrchestrator()
	orch.Substrate = newSproutWorkspace(t)
	orch.StepID = "step-unresolvable"
	orch.Tier = llm.TierCheapest
	orch.DisableMergeBack = true

	report, err := orch.RunSprout(context.Background(), "do the thing")
	if err == nil {
		t.Fatalf("RunSprout succeeded with report %+v, want a refusal naming grok", report)
	}
	if !errors.Is(err, llm.ErrNoModelAvailable) {
		t.Fatalf("err = %v, want it to wrap llm.ErrNoModelAvailable", err)
	}
	if !strings.Contains(err.Error(), "grok") {
		t.Fatalf("err = %v, want it to name the configured provider", err)
	}
	if built {
		t.Fatal("a sprout was constructed for a run with no resolvable model")
	}
	if worktreeCreated {
		t.Fatal("a shadow worktree was created for a run with no resolvable model")
	}
	if hostStashed {
		t.Fatal("the host workspace was stashed for a run with no resolvable model")
	}
	if report.Provider != "grok" {
		t.Fatalf("report.Provider = %q, want grok so the failure names where it was pointed", report.Provider)
	}
}

// A session preference that names a provider and no model is still a choice of
// provider. It used to be discarded unless a model came with it, which sent the
// run to tier resolution and, from there, potentially to a different provider
// entirely.
func TestResolveLLMClientHonoursAProviderWithoutAModel(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("DEFAULT_LLM_PROVIDER", "anthropic")

	orch := NewDockerOrchestrator()
	orch.Provider = "google"

	client := orch.resolveLLMClient()
	if err := client.ResolutionError(); err != nil {
		t.Fatalf("ResolutionError = %v, want nil", err)
	}
	if client.Provider() != "google" {
		t.Fatalf("client provider = %q, want google (the preference, not the environment default)", client.Provider())
	}
	if client.Model() == "" {
		t.Fatal("client has no model; a provider preference must still select one")
	}
	if !strings.HasPrefix(client.Model(), "gemini") {
		t.Fatalf("client model = %q, want a google model", client.Model())
	}
}

// An unattended run nobody configured must not land on the most expensive model
// its provider serves. The cost ceiling now means what it says, so the default
// tier decides what an operator is billed for a run they never chose a model
// for — and that default is the cheapest model that can still drive tools.
func TestUnconfiguredAutonomousRunStartsOnTheCheapestToolCapableModel(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	orch := NewDockerOrchestrator()
	if orch.Tier != "" {
		t.Fatalf("orch.Tier = %q, want empty — this test is about the DEFAULT", orch.Tier)
	}

	client := orch.resolveLLMClient()
	if err := client.ResolutionError(); err != nil {
		t.Fatalf("ResolutionError = %v, want nil", err)
	}
	if client.Model() != "claude-haiku-4-5" {
		t.Fatalf("default model = %q, want claude-haiku-4-5 (the cheapest tool-capable model)", client.Model())
	}

	// A tier set on the step still governs, so escalation stays available and
	// deliberate rather than being the thing that happens by itself.
	orch.Tier = llm.TierPremium
	if got := orch.resolveLLMClient().Model(); got != "claude-opus-4-8" {
		t.Fatalf("with an explicit premium tier, model = %q, want claude-opus-4-8", got)
	}
}
