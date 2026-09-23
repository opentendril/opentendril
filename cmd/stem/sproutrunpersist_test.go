package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/roots/llm"
)

func persistIntPtr(v int) *int          { return &v }
func persistStringPtr(v string) *string { return &v }

func persistUsage(prompt, completion, total int, amount, unit, provenance string) llm.Usage {
	return llm.Usage{
		PromptTokens:     persistIntPtr(prompt),
		CompletionTokens: persistIntPtr(completion),
		TotalTokens:      persistIntPtr(total),
		CostAmount:       persistStringPtr(amount),
		CostUnit:         persistStringPtr(unit),
		CostProvenance:   persistStringPtr(provenance),
	}
}

func TestSproutRunUsageFromReportMapsSeparateComponents(t *testing.T) {
	report := conductor.SproutRunReport{
		Provider:     "openrouter",
		Model:        "anthropic/claude-sonnet-4.6",
		RequestsMade: true,
		Usage:        persistUsage(30, 15, 45, "4.00", "credits", "openrouter"),
		PostRun: conductor.PostRunUsage{
			RequestsMade: true,
			Provider:     "lab",
			Model:        "cheap-local",
			Usage:        persistUsage(12, 8, 20, "0.0001", "points", "lab"),
		},
	}

	usage := sproutRunUsageFromReport(report)
	if usage.Execution == nil || usage.PostRun == nil {
		t.Fatalf("mapped usage = %+v, want both components", usage)
	}
	if !usage.Execution.RequestsMade || usage.Execution.Provider != "openrouter" {
		t.Fatalf("execution = %+v", usage.Execution)
	}
	if !usage.PostRun.RequestsMade || usage.PostRun.Provider != "lab" {
		t.Fatalf("postRun = %+v", usage.PostRun)
	}
	if *usage.Execution.CostUnit == *usage.PostRun.CostUnit {
		t.Fatal("unlike cost units were collapsed")
	}

	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("mapped JSON: %v", err)
	}
	if _, ok := envelope["totalCost"]; ok {
		t.Fatalf("mapped JSON invented a combined totalCost: %s", raw)
	}
	if _, ok := envelope["totalTokens"]; ok {
		t.Fatalf("mapped JSON invented a combined totalTokens: %s", raw)
	}
}

func TestSproutRunUsageFromReportOmitsUnrequestedComponents(t *testing.T) {
	usage := sproutRunUsageFromReport(conductor.SproutRunReport{
		Outcome:  conductor.SproutOutcomeDetached,
		Provider: "openrouter",
		Model:    "some-model",
	})
	if usage.Execution != nil || usage.PostRun != nil {
		t.Fatalf("detached report fabricated usage: %+v", usage)
	}
}

func TestSproutRunUsageFromReportKeepsNilFieldsWhenRequestsMade(t *testing.T) {
	usage := sproutRunUsageFromReport(conductor.SproutRunReport{
		RequestsMade: true,
		Provider:     "openrouter",
		Model:        "some-model",
		PostRun: conductor.PostRunUsage{
			RequestsMade: true,
			Provider:     "nvidia",
			Model:        "cheap",
		},
	})
	if usage.Execution == nil || !usage.Execution.RequestsMade || usage.Execution.PromptTokens != nil {
		t.Fatalf("execution = %+v", usage.Execution)
	}
	if usage.PostRun == nil || !usage.PostRun.RequestsMade || usage.PostRun.CostAmount != nil {
		t.Fatalf("postRun = %+v", usage.PostRun)
	}
}

func TestPersistTerminalSproutRunSettlesUsageAndLeavesOpeningNonTerminalUntilThen(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opened := historydb.SproutRun{
		RunID:     "run-persist",
		SessionID: "s1",
		StepID:    "run-persist",
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	if err := store.RecordSproutRun(context.Background(), opened); err != nil {
		t.Fatalf("opening write: %v", err)
	}
	runs, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "running" || runs[0].Usage.Execution != nil {
		t.Fatalf("opening row = %+v err=%v", runs, err)
	}

	fruitCreated := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	persistTerminalSproutRun(context.Background(), store, opened, conductor.SproutRunReport{
		Output:       "done",
		Outcome:      conductor.SproutOutcomeComplete,
		Provider:     "openrouter",
		Model:        "anthropic/claude-sonnet-4.6",
		RequestsMade: true,
		Usage:        persistUsage(30, 15, 45, "0.0000052349000001", "credits", "openrouter"),
		PostRun: conductor.PostRunUsage{
			RequestsMade: true,
			Provider:     "lab",
			Model:        "cheap-local",
			Usage:        persistUsage(1, 1, 2, "0.01", "points", "lab"),
		},
		FruitRepository:       "github.com/example/repo",
		FruitWorkspace:        "/private/repo",
		FruitBranch:           "review/fruit",
		FruitCommit:           "deadbeef",
		FruitPublicationState: conductor.FruitPublicationPublished,
		FruitCreatedAt:        fruitCreated,
	}, nil)

	runs, err = store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load after terminal: %v %+v", err, runs)
	}
	got := runs[0]
	if got.Status != "matured" || got.Output != "done" || got.Model != "anthropic/claude-sonnet-4.6" || got.FinishedAt.IsZero() {
		t.Fatalf("terminal row = %+v", got)
	}
	if got.Usage.Execution == nil || got.Usage.Execution.CostAmount == nil || *got.Usage.Execution.CostAmount != "0.0000052349000001" {
		t.Fatalf("execution usage = %+v", got.Usage.Execution)
	}
	if got.Usage.PostRun == nil || got.Usage.PostRun.Provider != "lab" {
		t.Fatalf("post-run usage = %+v", got.Usage.PostRun)
	}
	if got.FruitRepository != "github.com/example/repo" || got.FruitWorkspace != "/private/repo" || got.FruitBranch != "review/fruit" || got.FruitCommit != "deadbeef" || got.FruitPublicationState != conductor.FruitPublicationPublished || !got.FruitCreatedAt.Equal(fruitCreated) {
		t.Fatalf("Fruit provenance = %+v", got)
	}

	if err := store.RecordSproutRun(context.Background(), historydb.SproutRun{
		RunID: "run-persist", Status: "matured", Output: "compat",
	}); err != nil {
		t.Fatalf("empty compatibility write: %v", err)
	}
	kept, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(kept) != 1 || kept[0].Usage.Execution == nil || kept[0].Usage.Execution.CostAmount == nil {
		t.Fatalf("compatibility write erased usage: %+v err=%v", kept, err)
	}
	if *kept[0].Usage.Execution.CostAmount != "0.0000052349000001" {
		t.Fatalf("exact cost did not survive compatibility write: %+v", kept[0].Usage.Execution)
	}
}

func TestInstallSproutTerminalHistoryIsNoopWithoutStore(t *testing.T) {
	orch := &conductor.DockerOrchestrator{}
	installSproutTerminalHistory(orch, nil, context.Background(), historydb.SproutRun{})
	if orch.OnTerminal != nil {
		t.Fatal("observer installed without a history store")
	}
}

func TestPersistTerminalSproutRunMapsProviderAuthFailure(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opened := historydb.SproutRun{
		RunID: "run-auth", SessionID: "s1", StepID: "run-auth",
		Status: "running", StartedAt: time.Now().UTC(),
	}
	if err := store.RecordSproutRun(context.Background(), opened); err != nil {
		t.Fatalf("opening write: %v", err)
	}

	authErr := &llm.RequestError{
		StatusCode: 401,
		Provider:   "openrouter",
		Model:      "anthropic/claude-sonnet-4.6",
		Body:       "User not found",
	}
	persistTerminalSproutRun(context.Background(), store, opened, conductor.SproutRunReport{
		Outcome:         conductor.SproutOutcomeFailed,
		Provider:        "openrouter",
		Model:           "anthropic/claude-sonnet-4.6",
		RequestsMade:    true,
		ToolInvocations: 0,
		FailureCategory: string(core.FailureCategoryProviderAuthRejected),
		ProviderDiagnostic: &core.ProviderDiagnostic{
			StatusCode: 401,
			Message:    "User not found",
			Provider:   "openrouter",
		},
	}, authErr)

	runs, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load: %v %+v", err, runs)
	}
	got := runs[0]
	if got.Status != "withered" {
		t.Fatalf("status = %q, want withered", got.Status)
	}
	if got.FailureCategory != string(core.FailureCategoryProviderAuthRejected) {
		t.Fatalf("failureCategory = %q, want %q", got.FailureCategory, core.FailureCategoryProviderAuthRejected)
	}
	if got.ProviderDiagnostic == nil || got.ProviderDiagnostic.StatusCode != 401 || got.ProviderDiagnostic.Message != "User not found" {
		t.Fatalf("providerDiagnostic = %+v", got.ProviderDiagnostic)
	}
	if !got.ProviderRequestAttempted {
		t.Fatal("providerRequestAttempted = false, want true")
	}
	if got.ToolInvocations != 0 {
		t.Fatalf("toolInvocations = %d, want 0", got.ToolInvocations)
	}
	if got.Provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter", got.Provider)
	}
	if strings.Contains(got.Error, "sk-") || strings.Contains(strings.ToLower(got.Error), "api key=") {
		t.Fatalf("persisted error leaked a credential: %q", got.Error)
	}
}

func TestPersistTerminalSproutRunClassifiesAuthWhenReportOmitsCategory(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opened := historydb.SproutRun{
		RunID: "run-auth-classify", SessionID: "s1", StepID: "run-auth-classify",
		Status: "running", StartedAt: time.Now().UTC(),
	}
	persistTerminalSproutRun(context.Background(), store, opened, conductor.SproutRunReport{
		Outcome:      conductor.SproutOutcomeFailed,
		RequestsMade: true,
		ProviderDiagnostic: &core.ProviderDiagnostic{
			StatusCode: 401,
			Message:    "User not found",
			Provider:   "openrouter",
		},
	}, &llm.RequestError{StatusCode: 401, Body: "User not found", Provider: "openrouter"})

	runs, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load: %v %+v", err, runs)
	}
	if runs[0].FailureCategory != string(core.FailureCategoryProviderAuthRejected) {
		t.Fatalf("adapter must call Core classification, got %q", runs[0].FailureCategory)
	}
}

func TestPersistTerminalSproutRunUsesCoreLifecycleForNoEngagement(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opened := historydb.SproutRun{
		RunID: "run-no-engagement", SessionID: "s1", StepID: "run-no-engagement",
		Status: "running", StartedAt: time.Now().UTC(),
	}
	if err := store.RecordSproutRun(context.Background(), opened); err != nil {
		t.Fatalf("opening write: %v", err)
	}

	persistTerminalSproutRun(context.Background(), store, opened, conductor.SproutRunReport{
		Outcome:  conductor.SproutOutcomeNoEngagement,
		Provider: "openrouter",
		Model:    "anthropic/claude-sonnet-4.6",
	}, nil)

	runs, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load: %v %+v", err, runs)
	}
	got := runs[0]
	if got.Status != "withered" {
		t.Fatalf("status = %q, want withered", got.Status)
	}
	if got.Outcome != conductor.SproutOutcomeNoEngagement {
		t.Fatalf("outcome = %q, want no-engagement", got.Outcome)
	}
	if got.FailureCategory != string(core.FailureCategoryNoEngagement) {
		t.Fatalf("failureCategory = %q, want %q", got.FailureCategory, core.FailureCategoryNoEngagement)
	}
	if got.Error != "" {
		t.Fatalf("error = %q, want empty for nil execution error", got.Error)
	}
	if got.Provider != "openrouter" || got.Model != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("provider/model = %s/%s", got.Provider, got.Model)
	}
}

func TestPersistTerminalSproutRunKeepsUsageOnWitheredError(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opened := historydb.SproutRun{
		RunID: "run-withered", SessionID: "s1", StepID: "run-withered",
		Status: "running", StartedAt: time.Now().UTC(),
	}
	if err := store.RecordSproutRun(context.Background(), opened); err != nil {
		t.Fatalf("opening write: %v", err)
	}

	persistTerminalSproutRun(context.Background(), store, opened, conductor.SproutRunReport{
		Outcome:      conductor.SproutOutcomeFailed,
		Provider:     "openrouter",
		Model:        "anthropic/claude-sonnet-4.6",
		RequestsMade: true,
		Usage:        persistUsage(9, 3, 12, "0.04", "credits", "openrouter"),
	}, context.DeadlineExceeded)

	runs, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load: %v %+v", err, runs)
	}
	got := runs[0]
	if got.Status != "withered" {
		t.Fatalf("status = %q, want withered", got.Status)
	}
	if got.Error == "" {
		t.Fatal("withered row carries no error")
	}
	if got.Usage.Execution == nil || got.Usage.Execution.CostAmount == nil || *got.Usage.Execution.CostAmount != "0.04" {
		t.Fatalf("withered usage was dropped: %+v", got.Usage)
	}
	if got.Usage.Execution.CostUnit == nil || *got.Usage.Execution.CostUnit != "credits" {
		t.Fatalf("withered cost unit = %v, want credits", got.Usage.Execution.CostUnit)
	}
}

func TestDaemonDetachedHistorySettlesAfterTerminalObserver(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opened := historydb.SproutRun{
		RunID: "run-detach", SessionID: "s1", StepID: "run-detach",
		Status: "running", StartedAt: time.Now().UTC(),
	}
	if err := store.RecordSproutRun(context.Background(), opened); err != nil {
		t.Fatalf("opening write: %v", err)
	}

	orch := &conductor.DockerOrchestrator{}
	installSproutTerminalHistory(orch, store, context.Background(), opened)

	// Immediate detach is not a terminal write. The observer must not have
	// fired merely because RunSprout returned detached.
	runs, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "running" || !runs[0].FinishedAt.IsZero() {
		t.Fatalf("row at detach = %+v err=%v, want running with no FinishedAt", runs, err)
	}
	if runs[0].Usage.Execution != nil || runs[0].Usage.PostRun != nil {
		t.Fatalf("detach fabricated usage: %+v", runs[0].Usage)
	}

	orch.OnTerminal(conductor.SproutRunReport{
		Output:       "done later",
		Outcome:      conductor.SproutOutcomeComplete,
		Provider:     "openrouter",
		Model:        "anthropic/claude-sonnet-4.6",
		RequestsMade: true,
		Usage:        persistUsage(30, 15, 45, "4.00", "credits", "openrouter"),
		PostRun: conductor.PostRunUsage{
			RequestsMade: true,
			Provider:     "lab",
			Model:        "cheap-local",
			Usage:        persistUsage(2, 1, 3, "0.01", "points", "lab"),
		},
	}, nil)

	settled, err := store.LoadSproutRuns(context.Background(), "s1", 10)
	if err != nil || len(settled) != 1 {
		t.Fatalf("load after terminal: %v %+v", err, settled)
	}
	got := settled[0]
	if got.Status != "matured" || got.Output != "done later" || got.FinishedAt.IsZero() {
		t.Fatalf("settled row = %+v", got)
	}
	if got.Usage.Execution == nil || got.Usage.Execution.CostAmount == nil || *got.Usage.Execution.CostAmount != "4.00" {
		t.Fatalf("execution usage = %+v", got.Usage.Execution)
	}
	if got.Usage.PostRun == nil || got.Usage.PostRun.Provider != "lab" || got.Usage.PostRun.CostAmount == nil || *got.Usage.PostRun.CostAmount != "0.01" {
		t.Fatalf("post-run usage = %+v", got.Usage.PostRun)
	}
}

func TestPersistDispatchSproutRunNotifiesOnlyAfterCommit(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	notified := false
	ctx := core.WithSproutDispatchHook(context.Background(), func(dispatch core.SproutDispatch) {
		runs, loadErr := store.LoadSproutRuns(context.Background(), "tendril-dispatch", 10)
		if loadErr != nil || len(runs) != 1 || runs[0].Status != "running" || runs[0].Pollen != "claude" {
			t.Fatalf("hook fired before a durable running row: %+v err=%v", runs, loadErr)
		}
		if dispatch.SessionID != "tendril-dispatch" || dispatch.StepID != "step-dispatch" {
			t.Fatalf("dispatch = %+v", dispatch)
		}
		notified = true
	})

	if err := persistDispatchSproutRun(ctx, store, historydb.SproutRun{
		RunID:     "step-dispatch",
		SessionID: "tendril-dispatch",
		StepID:    "step-dispatch",
		Pollen:    "claude",
		Substrate: "myrepo",
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("persistDispatchSproutRun: %v", err)
	}
	if !notified {
		t.Fatal("dispatch hook was not invoked after commit")
	}
}

func TestPersistDispatchSproutRunRequiresPhytomer(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	notified := false
	ctx := core.WithSproutDispatchHook(context.Background(), func(core.SproutDispatch) {
		notified = true
	})
	err = persistDispatchSproutRun(ctx, store, historydb.SproutRun{
		RunID: "step-no-session", Status: "running",
	})
	if err == nil || !strings.Contains(err.Error(), "sessionId") {
		t.Fatalf("expected sessionId error, got %v", err)
	}
	if notified {
		t.Fatal("ready signal fired without a phytomer")
	}
}

func TestPersistDispatchSproutRunNilHistoryDoesNotSignal(t *testing.T) {
	notified := false
	ctx := core.WithSproutDispatchHook(context.Background(), func(core.SproutDispatch) {
		notified = true
	})
	if err := persistDispatchSproutRun(ctx, nil, historydb.SproutRun{
		RunID: "step-1", SessionID: "tendril-1", Status: "running",
	}); err != nil {
		t.Fatalf("nil history: %v", err)
	}
	if notified {
		t.Fatal("ready signal fired with no history store")
	}
}

func TestOneShotSproutOrchestratorAwaitsRunEnding(t *testing.T) {
	oneShot := newSproutRunOrchestrator(core.SproutSpec{StepID: "one-shot"}, sproutSubstrateWiring{}, nil, nil)
	if !oneShot.AwaitsRunEnding {
		t.Fatal("one-shot sproutOperations wiring must set AwaitsRunEnding")
	}

	daemon := newSproutRunOrchestrator(core.SproutSpec{StepID: "daemon"}, sproutSubstrateWiring{}, eventbus.New(), eventbus.New())
	if daemon.AwaitsRunEnding {
		t.Fatal("daemon-backed sproutOperations wiring must remain detachable")
	}
}

func openOneShotPersistenceHistory(t *testing.T) *historydb.Store {
	t.Helper()
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func assertOneShotOpeningRow(t *testing.T, store *historydb.Store, sessionID, stepID string) {
	t.Helper()
	runs, err := store.LoadSproutRuns(context.Background(), sessionID, 10)
	if err != nil || len(runs) != 1 || runs[0].StepID != stepID || runs[0].Status != "running" {
		t.Fatalf("opening row = %+v err=%v", runs, err)
	}
}

func oneShotPersistenceSpec(sessionID, stepID string) core.SproutSpec {
	return core.SproutSpec{
		StepID:     stepID,
		SessionID:  sessionID,
		Origin:     "cli",
		Transcript: "safe fixture transcript",
	}
}

func TestOneShotSproutOperationsDrainsEventsAndSettlesRunBeforeReturn(t *testing.T) {
	store := openOneShotPersistenceHistory(t)
	const sessionID = "s-one-shot-success"
	const stepID = "step-one-shot-success"
	semanticReport := conductor.SproutRunReport{
		Output:  "semantic work completed",
		Outcome: conductor.SproutOutcomeComplete,
	}
	originalRun := runSproutTerrarium
	runSproutTerrarium = func(_ context.Context, orch *conductor.DockerOrchestrator, _ string) (conductor.SproutRunReport, error) {
		assertOneShotOpeningRow(t, store, sessionID, stepID)
		orch.EventBus.Publish(eventbus.Event{
			Type:      eventbus.EventTaskContextAssembled,
			SessionID: sessionID,
			Source:    stepID,
			Data: map[string]interface{}{
				"items": []interface{}{map[string]interface{}{"kind": "file", "source": "README.md", "status": "included"}},
			},
		})
		if orch.OnTerminal == nil {
			t.Fatal("one-shot lifecycle did not install terminal persistence")
		}
		orch.OnTerminal(semanticReport, nil)
		return semanticReport, nil
	}
	t.Cleanup(func() { runSproutTerrarium = originalRun })

	result, err := sproutOperations(store, nil).Run(context.Background(), oneShotPersistenceSpec(sessionID, stepID))
	if err != nil {
		t.Fatalf("one-shot run: %v", err)
	}
	if result.Output != semanticReport.Output || result.Outcome != semanticReport.Outcome {
		t.Fatalf("result = %+v, want %+v", result, semanticReport)
	}

	events, err := store.LoadEvents(context.Background(), sessionID, 10)
	if err != nil {
		t.Fatalf("load events after one-shot return: %v", err)
	}
	foundTaskContext := false
	for _, event := range events {
		if event.Type == string(eventbus.EventTaskContextAssembled) && event.Source == stepID {
			foundTaskContext = true
		}
	}
	if !foundTaskContext {
		t.Fatalf("task-context-assembled was not persisted before return: %+v", events)
	}

	runs, err := store.LoadSproutRuns(context.Background(), sessionID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load settled row: %+v err=%v", runs, err)
	}
	if runs[0].Status != "matured" || runs[0].Output != semanticReport.Output || runs[0].FinishedAt.IsZero() {
		t.Fatalf("opening row was not settled before return: %+v", runs[0])
	}
}

func TestOneShotSproutOperationsReturnsTerminalPersistenceFailure(t *testing.T) {
	store := openOneShotPersistenceHistory(t)
	const sessionID = "s-one-shot-terminal-failure"
	const stepID = "step-one-shot-terminal-failure"
	semanticReport := conductor.SproutRunReport{Output: "semantic work completed", Outcome: conductor.SproutOutcomeComplete}
	originalRun := runSproutTerrarium
	runSproutTerrarium = func(_ context.Context, orch *conductor.DockerOrchestrator, _ string) (conductor.SproutRunReport, error) {
		assertOneShotOpeningRow(t, store, sessionID, stepID)
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("close HistoryDB during run: %v", closeErr)
		}
		if orch.OnTerminal == nil {
			t.Fatal("one-shot lifecycle did not install terminal persistence")
		}
		orch.OnTerminal(semanticReport, nil)
		return semanticReport, nil
	}
	t.Cleanup(func() { runSproutTerrarium = originalRun })

	result, err := sproutOperations(store, nil).Run(context.Background(), oneShotPersistenceSpec(sessionID, stepID))
	if result.Output != semanticReport.Output || result.Outcome != semanticReport.Outcome {
		t.Fatalf("semantic result = %+v, want %+v", result, semanticReport)
	}
	if err == nil || !strings.Contains(err.Error(), "terminal sprout run") {
		t.Fatalf("terminal persistence failure = %v, want propagated terminal write error", err)
	}
}

func TestOneShotSproutOperationsReturnsEventPersistenceFailure(t *testing.T) {
	store := openOneShotPersistenceHistory(t)
	const sessionID = "s-one-shot-event-failure"
	const stepID = "step-one-shot-event-failure"
	semanticReport := conductor.SproutRunReport{Output: "semantic work completed", Outcome: conductor.SproutOutcomeComplete}
	originalRun := runSproutTerrarium
	runSproutTerrarium = func(_ context.Context, orch *conductor.DockerOrchestrator, _ string) (conductor.SproutRunReport, error) {
		assertOneShotOpeningRow(t, store, sessionID, stepID)
		handlerEntered := make(chan struct{})
		releaseHandler := make(chan struct{})
		orch.EventBus.Subscribe(eventbus.EventTaskContextAssembled, func(eventbus.Event) {
			close(handlerEntered)
			<-releaseHandler
		})
		published := make(chan struct{})
		go func() {
			orch.EventBus.Publish(eventbus.Event{
				Type: eventbus.EventTaskContextAssembled, SessionID: sessionID, Source: stepID,
			})
			close(published)
		}()
		<-handlerEntered

		// Settle the terminal row while HistoryDB is still available. The
		// pending Publish cannot reach its asynchronous sink until the handler
		// is released below, making this a deterministic event-only failure.
		if orch.OnTerminal == nil {
			t.Fatal("one-shot lifecycle did not install terminal persistence")
		}
		orch.OnTerminal(semanticReport, nil)
		runs, loadErr := store.LoadSproutRuns(context.Background(), sessionID, 10)
		if loadErr != nil || len(runs) != 1 || runs[0].Status != "matured" {
			t.Fatalf("terminal row should persist before event-only failure: %+v err=%v", runs, loadErr)
		}
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("close HistoryDB before asynchronous event sink: %v", closeErr)
		}
		close(releaseHandler)
		<-published
		return semanticReport, nil
	}
	t.Cleanup(func() { runSproutTerrarium = originalRun })

	result, err := sproutOperations(store, nil).Run(context.Background(), oneShotPersistenceSpec(sessionID, stepID))
	if result.Output != semanticReport.Output || result.Outcome != semanticReport.Outcome {
		t.Fatalf("semantic result = %+v, want %+v", result, semanticReport)
	}
	if err == nil || !strings.Contains(err.Error(), "task-context-assembled") {
		t.Fatalf("event persistence failure = %v, want propagated event write error", err)
	}
	if strings.Contains(err.Error(), "terminal sprout run") {
		t.Fatalf("terminal persistence unexpectedly failed in event-only test: %v", err)
	}
}

func TestOneShotSproutOperationsPropagatesPersistenceFailuresAfterSemanticSuccess(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const sessionID = "s-one-shot-persistence-failure"
	const stepID = "step-one-shot-persistence-failure"
	semanticReport := conductor.SproutRunReport{
		Output:  "semantic work completed",
		Outcome: conductor.SproutOutcomeComplete,
	}
	originalRun := runSproutTerrarium
	runSproutTerrarium = func(_ context.Context, orch *conductor.DockerOrchestrator, _ string) (conductor.SproutRunReport, error) {
		if orch.EventBus == nil {
			t.Fatal("one-shot lifecycle did not receive its EventBus")
		}

		// Prove the opening row was committed before simulating the mid-run
		// HistoryDB failure. This is the ownership checkpoint sproutOperations
		// is required to persist before invoking the Terrarium.
		runs, loadErr := store.LoadSproutRuns(context.Background(), sessionID, 10)
		if loadErr != nil || len(runs) != 1 || runs[0].StepID != stepID || runs[0].Status != "running" {
			t.Fatalf("opening row before simulated run = %+v err=%v", runs, loadErr)
		}

		// Hold Publish in its synchronous handler after the event has reached
		// the one-shot bus but before the asynchronous sink receives it. Closing
		// the already-open HistoryDB here makes the sink failure deterministic.
		handlerEntered := make(chan struct{})
		releaseHandler := make(chan struct{})
		orch.EventBus.Subscribe(eventbus.EventTaskContextAssembled, func(eventbus.Event) {
			close(handlerEntered)
			<-releaseHandler
		})
		published := make(chan struct{})
		go func() {
			orch.EventBus.Publish(eventbus.Event{
				Type:      eventbus.EventTaskContextAssembled,
				SessionID: sessionID,
				Source:    stepID,
				Data:      map[string]interface{}{"items": []interface{}{map[string]interface{}{"kind": "task", "source": "approved-context"}}},
			})
			close(published)
		}()
		<-handlerEntered
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("close HistoryDB during run: %v", closeErr)
		}
		close(releaseHandler)
		<-published

		if orch.OnTerminal == nil {
			t.Fatal("one-shot lifecycle did not install terminal persistence")
		}
		orch.OnTerminal(semanticReport, nil)
		return semanticReport, nil
	}
	t.Cleanup(func() { runSproutTerrarium = originalRun })

	operations := sproutOperations(store, nil)
	result, runErr := operations.Run(context.Background(), core.SproutSpec{
		StepID:     stepID,
		SessionID:  sessionID,
		Origin:     "cli",
		Transcript: "report what you observe",
	})
	if result.Output != semanticReport.Output || result.Outcome != semanticReport.Outcome {
		t.Fatalf("semantic result = %+v, want successful report %+v", result, semanticReport)
	}
	if runErr == nil {
		t.Fatal("one-shot operation returned semantic success although required HistoryDB event and terminal persistence failed")
	}
}
