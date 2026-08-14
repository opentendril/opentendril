package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
