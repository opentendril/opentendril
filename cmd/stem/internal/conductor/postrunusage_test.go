package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/roots/llm"
)

type usagePromptFake struct {
	results  []llm.Result
	errs     []error
	provider string
	model    string
	calls    int
}

func (f *usagePromptFake) CallPromptWithResult(ctx context.Context, systemPrompt, userPrompt string) (llm.Result, error) {
	if f.calls >= len(f.results) {
		return llm.Result{}, errors.New("out of mocks")
	}
	res := f.results[f.calls]
	var err error
	if f.calls < len(f.errs) {
		err = f.errs[f.calls]
	}
	f.calls++
	return res, err
}

func (f *usagePromptFake) Provider() string { return f.provider }
func (f *usagePromptFake) Model() string    { return f.model }

func transcribeDiff() string {
	return "diff --git a/pkg/thing.go b/pkg/thing.go\n+changed\n"
}

func seedGenome(t *testing.T, workspace, body string) {
	t.Helper()
	path := filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir genome: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed genome: %v", err)
	}
}

func TestTranscribeLearningsOneCallCountsRequest(t *testing.T) {
	workspace := t.TempDir()
	fake := &usagePromptFake{
		results: []llm.Result{
			{Text: "- one durable learning", Usage: completeUsage(10, 5, 15, "1.00", "USD", "openrouter")},
		},
		provider: "nvidia",
		model:    "meta/llama-3.1-8b-instruct",
	}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}

	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", transcribeDiff(), "logs")
	if err != nil {
		t.Fatalf("TranscribeLearnings: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("provider requests = %d, want 1", fake.calls)
	}
	if !post.RequestsMade {
		t.Fatal("RequestsMade = false after a transcribe request")
	}
	assertIntPtr(t, post.Usage.PromptTokens, 10, "PromptTokens")
	assertStringPtr(t, post.Usage.CostAmount, "1.00", "CostAmount")
	if post.Provider != "nvidia" {
		t.Fatalf("Provider = %q, want nvidia (chronicler mind)", post.Provider)
	}
	if post.Model != "meta/llama-3.1-8b-instruct" {
		t.Fatalf("Model = %q, want the chronicler model", post.Model)
	}
}

func TestTranscribeLearningsChroniclerAndReductionAggregate(t *testing.T) {
	t.Setenv("TENDRIL_GENOME_MAX_TOKENS", "1")
	workspace := t.TempDir()
	seedGenome(t, workspace, epigeneticGenomeHeader+"\n\n- existing oversized rule that forces reduction\n")
	fake := &usagePromptFake{
		results: []llm.Result{
			{Text: "- new learning", Usage: completeUsage(10, 5, 15, "1.00", "USD", "openrouter")},
			{Text: "- reduced principle", Usage: completeUsage(4, 2, 6, "0.50", "USD", "openrouter")},
		},
		provider: "nvidia",
		model:    "meta/llama-3.1-8b-instruct",
	}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}

	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", transcribeDiff(), "logs")
	if err != nil {
		t.Fatalf("TranscribeLearnings: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("provider requests = %d, want 2 (transcribe + reduction)", fake.calls)
	}
	if !post.RequestsMade {
		t.Fatal("RequestsMade = false after transcribe + reduction")
	}
	assertIntPtr(t, post.Usage.PromptTokens, 14, "PromptTokens")
	assertIntPtr(t, post.Usage.CompletionTokens, 7, "CompletionTokens")
	assertIntPtr(t, post.Usage.TotalTokens, 21, "TotalTokens")
	assertStringPtr(t, post.Usage.CostAmount, "1.50", "CostAmount")
	if post.Provider != "nvidia" || post.Model != "meta/llama-3.1-8b-instruct" {
		t.Fatalf("attribution = %s/%s, want chronicler mind", post.Provider, post.Model)
	}
}

func TestTranscribeLearningsMissingUsageKeepsRequestsMade(t *testing.T) {
	workspace := t.TempDir()
	fake := &usagePromptFake{
		results:  []llm.Result{{Text: "- learning with no accounting"}},
		provider: "nvidia",
		model:    "meta/llama-3.1-8b-instruct",
	}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}

	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", transcribeDiff(), "logs")
	if err != nil {
		t.Fatalf("TranscribeLearnings: %v", err)
	}
	if !post.RequestsMade {
		t.Fatal("RequestsMade = false; a request occurred even though usage fields are nil")
	}
	assertUsageAbsent(t, post.Usage)
	if post.Provider != "nvidia" {
		t.Fatalf("Provider = %q, want nvidia", post.Provider)
	}
}

func TestTranscribeLearningsErrorPreservesUsage(t *testing.T) {
	workspace := t.TempDir()
	fake := &usagePromptFake{
		results:  []llm.Result{{Usage: completeUsage(10, 5, 15, "1.00", "USD", "api")}},
		errs:     []error{errors.New("provider down")},
		provider: "nvidia",
		model:    "cheap-model",
	}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}

	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", transcribeDiff(), "logs")
	if err == nil {
		t.Fatal("TranscribeLearnings succeeded, want the provider error")
	}
	if !post.RequestsMade {
		t.Fatal("RequestsMade = false after a failed provider request")
	}
	assertIntPtr(t, post.Usage.PromptTokens, 10, "PromptTokens")
	assertStringPtr(t, post.Usage.CostAmount, "1.00", "CostAmount")
	if _, statErr := os.Stat(filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")); !os.IsNotExist(statErr) {
		t.Fatalf("genome should not be written when transcribe fails, stat err=%v", statErr)
	}
}

func TestTranscribeLearningsReductionErrorKeepsBothUsagesAndSucceeds(t *testing.T) {
	t.Setenv("TENDRIL_GENOME_MAX_TOKENS", "1")
	workspace := t.TempDir()
	seedGenome(t, workspace, epigeneticGenomeHeader+"\n\n- existing oversized rule that forces reduction\n")
	fake := &usagePromptFake{
		results: []llm.Result{
			{Text: "- new learning", Usage: completeUsage(10, 5, 15, "1.00", "USD", "api")},
			{Usage: completeUsage(4, 2, 6, "0.50", "USD", "api")},
		},
		errs:     []error{nil, errors.New("reduction failed")},
		provider: "nvidia",
		model:    "cheap-model",
	}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}

	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", transcribeDiff(), "logs")
	if err != nil {
		t.Fatalf("TranscribeLearnings returned %v; reduction errors are skipped", err)
	}
	if fake.calls != 2 {
		t.Fatalf("provider requests = %d, want 2", fake.calls)
	}
	if !post.RequestsMade {
		t.Fatal("RequestsMade = false after transcribe + failed reduction")
	}
	assertIntPtr(t, post.Usage.PromptTokens, 14, "PromptTokens")
	assertStringPtr(t, post.Usage.CostAmount, "1.50", "CostAmount")
}

func TestTranscribeLearningsMismatchedCostMakesComponentCostUnavailable(t *testing.T) {
	t.Setenv("TENDRIL_GENOME_MAX_TOKENS", "1")
	workspace := t.TempDir()
	seedGenome(t, workspace, epigeneticGenomeHeader+"\n\n- existing oversized rule that forces reduction\n")
	fake := &usagePromptFake{
		results: []llm.Result{
			{Text: "- new learning", Usage: completeUsage(10, 5, 15, "1.00", "USD", "openrouter")},
			{Text: "- reduced", Usage: completeUsage(4, 2, 6, "2.00", "credits", "nvidia")},
		},
		provider: "nvidia",
		model:    "cheap-model",
	}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}

	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", transcribeDiff(), "logs")
	if err != nil {
		t.Fatalf("TranscribeLearnings: %v", err)
	}
	assertIntPtr(t, post.Usage.PromptTokens, 14, "PromptTokens")
	assertCostAbsent(t, post.Usage)
	if !post.RequestsMade {
		t.Fatal("RequestsMade = false after two requests with mismatched cost semantics")
	}
}

func TestPostRunAttributionDoesNotUseSproutMind(t *testing.T) {
	workspace := t.TempDir()
	fake := &usagePromptFake{
		results:  []llm.Result{{Text: "- learning", Usage: completeUsage(1, 1, 2, "0.01", "USD", "api")}},
		provider: "nvidia",
		model:    "cheap-model",
	}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}

	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", transcribeDiff(), "logs")
	if err != nil {
		t.Fatalf("TranscribeLearnings: %v", err)
	}
	if post.Provider == "anthropic" || post.Model == "claude-opus-4-8" {
		t.Fatalf("post-run attribution copied a Sprout mind: %s/%s", post.Provider, post.Model)
	}
	if post.Provider != "nvidia" || post.Model != "cheap-model" {
		t.Fatalf("attribution = %s/%s, want chronicler client", post.Provider, post.Model)
	}
}

func stubReviewableFruitRun(t *testing.T, root string, runner sproutRunner) {
	t.Helper()
	stubRunSproutCollaborators(t, root, runner, []string{"pkg/thing.go"})
	collectGitDiffFn = func(ctx context.Context, mountPath string) (string, error) {
		return transcribeDiff(), nil
	}
}

func stubRunChronicler(t *testing.T, client promptResultCaller) {
	t.Helper()
	original := newRunChroniclerFn
	t.Cleanup(func() { newRunChroniclerFn = original })
	newRunChroniclerFn = func(workspace string, tier llm.ModelTier) *EpigeneticChronicler {
		return &EpigeneticChronicler{workspace: workspace, client: client}
	}
}

func runReviewableFruit(t *testing.T, root string, runner sproutRunner) SproutRunReport {
	t.Helper()
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "fruit", root, "")
	stubReviewableFruitRun(t, root, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := (&DockerOrchestrator{
		Substrate: "fruit",
		StepID:    "post-run",
	}).RunSprout(ctx, "task")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	return report
}

func TestSproutRunReportCarriesSeparatePostRunComponent(t *testing.T) {
	root := newOutcomeTestRepo(t)
	execution := completeUsage(30, 15, 45, "4.00", "USD", "api")
	stubRunChronicler(t, &usagePromptFake{
		results:  []llm.Result{{Text: "- learning", Usage: completeUsage(10, 5, 15, "1.00", "USD", "openrouter")}},
		provider: "nvidia",
		model:    "cheap-model",
	})

	report := runReviewableFruit(t, root, usageReportRunner{
		result: sproutResult{Response: "done", WroteWorkspace: true, Usage: execution, RequestsMade: true},
	})

	assertIntPtr(t, report.Usage.PromptTokens, 30, "execution PromptTokens")
	assertStringPtr(t, report.Usage.CostAmount, "4.00", "execution CostAmount")
	if !report.RequestsMade {
		t.Fatal("execution RequestsMade = false")
	}
	if !report.PostRun.RequestsMade {
		t.Fatal("post-run RequestsMade = false")
	}
	assertIntPtr(t, report.PostRun.Usage.PromptTokens, 10, "post-run PromptTokens")
	assertStringPtr(t, report.PostRun.Usage.CostAmount, "1.00", "post-run CostAmount")
	if report.PostRun.Provider != "nvidia" || report.PostRun.Model != "cheap-model" {
		t.Fatalf("post-run attribution = %s/%s, want chronicler mind", report.PostRun.Provider, report.PostRun.Model)
	}
}

func TestSproutRunReportPostRunReductionReachesReport(t *testing.T) {
	t.Setenv("TENDRIL_GENOME_MAX_TOKENS", "1")
	root := newOutcomeTestRepo(t)
	seedGenome(t, root, epigeneticGenomeHeader+"\n\n- existing oversized rule that forces reduction\n")
	stubRunChronicler(t, &usagePromptFake{
		results: []llm.Result{
			{Text: "- learning", Usage: completeUsage(10, 5, 15, "1.00", "USD", "openrouter")},
			{Text: "- reduced", Usage: completeUsage(4, 2, 6, "0.50", "USD", "openrouter")},
		},
		provider: "nvidia",
		model:    "cheap-model",
	})

	report := runReviewableFruit(t, root, usageReportRunner{
		result: sproutResult{Response: "done", WroteWorkspace: true, Usage: completeUsage(2, 1, 3, "0.10", "USD", "api"), RequestsMade: true},
	})
	assertIntPtr(t, report.PostRun.Usage.PromptTokens, 14, "post-run PromptTokens")
	assertStringPtr(t, report.PostRun.Usage.CostAmount, "1.50", "post-run CostAmount")
	assertIntPtr(t, report.Usage.PromptTokens, 2, "execution must stay uncombined")
}

func TestSproutRunReportPostRunNilUsageWithRequestsMade(t *testing.T) {
	root := newOutcomeTestRepo(t)
	stubRunChronicler(t, &usagePromptFake{
		results:  []llm.Result{{Text: "- learning"}},
		provider: "nvidia",
		model:    "cheap-model",
	})

	report := runReviewableFruit(t, root, usageReportRunner{
		result: sproutResult{Response: "done", WroteWorkspace: true, RequestsMade: true},
	})
	if !report.PostRun.RequestsMade {
		t.Fatal("post-run RequestsMade = false after a chronicler request with nil usage")
	}
	assertUsageAbsent(t, report.PostRun.Usage)
}

func TestSproutRunReportChroniclerErrorPreservesPostRunUsage(t *testing.T) {
	root := newOutcomeTestRepo(t)
	stubRunChronicler(t, &usagePromptFake{
		results:  []llm.Result{{Usage: completeUsage(8, 2, 10, "0.20", "USD", "api")}},
		errs:     []error{errors.New("chronicler provider failed")},
		provider: "nvidia",
		model:    "cheap-model",
	})

	report := runReviewableFruit(t, root, usageReportRunner{
		result: sproutResult{Response: "done", WroteWorkspace: true, Usage: completeUsage(30, 15, 45, "4.00", "USD", "api"), RequestsMade: true},
	})
	if !report.PostRun.RequestsMade {
		t.Fatal("post-run RequestsMade = false after a failed chronicler request")
	}
	assertIntPtr(t, report.PostRun.Usage.PromptTokens, 8, "post-run PromptTokens")
	assertIntPtr(t, report.Usage.PromptTokens, 30, "execution PromptTokens")
}

func TestSproutRunReportDifferingPostRunCostLeavesExecutionIntact(t *testing.T) {
	t.Setenv("TENDRIL_GENOME_MAX_TOKENS", "1")
	root := newOutcomeTestRepo(t)
	seedGenome(t, root, epigeneticGenomeHeader+"\n\n- existing oversized rule that forces reduction\n")
	stubRunChronicler(t, &usagePromptFake{
		results: []llm.Result{
			{Text: "- learning", Usage: completeUsage(10, 5, 15, "1.00", "USD", "openrouter")},
			{Text: "- reduced", Usage: completeUsage(4, 2, 6, "2.00", "credits", "nvidia")},
		},
		provider: "nvidia",
		model:    "cheap-model",
	})

	report := runReviewableFruit(t, root, usageReportRunner{
		result: sproutResult{Response: "done", WroteWorkspace: true, Usage: completeUsage(30, 15, 45, "4.00", "USD", "api"), RequestsMade: true},
	})
	assertCostAbsent(t, report.PostRun.Usage)
	assertStringPtr(t, report.Usage.CostAmount, "4.00", "execution CostAmount")
	assertStringPtr(t, report.Usage.CostUnit, "USD", "execution CostUnit")
}

func TestSproutRunReportNoChroniclerLeavesPostRunUnrequested(t *testing.T) {
	stubUsageReportRun(t, usageReportRunner{
		result: sproutResult{Response: "done", Usage: completeUsage(30, 15, 45, "4.00", "USD", "api"), RequestsMade: true},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := (&DockerOrchestrator{
		Substrate:        t.TempDir(),
		StepID:           "no-chronicler",
		DisableMergeBack: true,
	}).RunSprout(ctx, "task")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.PostRun.RequestsMade {
		t.Fatal("PostRun.RequestsMade = true when the chronicler was not invoked")
	}
	assertUsageAbsent(t, report.PostRun.Usage)
}

func TestRunSproutDetachedReturnDoesNotClaimTerminalUsage(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 300ms\n")

	runner := newHeldSproutRunner("finished after the Stem stopped waiting")
	stubRunSproutCollaborators(t, root, runner, []string{"pkg/thing.go"})
	stubCountingSession(t)

	bus := eventbus.New()
	recorder := recordSproutEvents(bus)
	orch := &DockerOrchestrator{
		Substrate: "bounded",
		StepID:    "detach-usage",
		EventBus:  bus,
	}
	report, err := orch.RunSprout(context.Background(), "detach probe")
	if err != nil {
		t.Fatalf("RunSprout returned %v", err)
	}
	if report.Outcome != SproutOutcomeDetached {
		t.Fatalf("Outcome = %q, want detached", report.Outcome)
	}
	if report.RequestsMade {
		t.Fatal("detached return claimed execution RequestsMade")
	}
	if report.PostRun.RequestsMade {
		t.Fatal("detached return claimed post-run RequestsMade")
	}
	assertUsageAbsent(t, report.Usage)
	assertUsageAbsent(t, report.PostRun.Usage)

	runner.release()
	recorder.awaitTerminal(t, "detach-usage")
}
