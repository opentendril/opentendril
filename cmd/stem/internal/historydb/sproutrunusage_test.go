package historydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func intPtr(v int) *int          { return &v }
func stringPtr(v string) *string { return &v }

func rawSproutRunUsage(t *testing.T, store *Store, runID string) string {
	t.Helper()
	var stored string
	if err := store.db.QueryRow(`SELECT usage FROM sproutruns WHERE runId = ?`, runID).Scan(&stored); err != nil {
		t.Fatalf("read stored usage for %s: %v", runID, err)
	}
	return stored
}

func TestSproutRunUsageRoundTripKeepsSeparateComponents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	run := SproutRun{
		RunID:     "run-components",
		SessionID: "s1",
		Status:    "matured",
		StartedAt: time.Now().UTC(),
		Usage: SproutRunUsage{
			Execution: &UsageComponent{
				RequestsMade:     true,
				PromptTokens:     intPtr(30),
				CompletionTokens: intPtr(15),
				TotalTokens:      intPtr(45),
				CostAmount:       stringPtr("4.00"),
				CostUnit:         stringPtr("USD"),
				CostProvenance:   stringPtr("openrouter"),
				Provider:         "openrouter",
				Model:            "anthropic/claude-sonnet-4.6",
			},
			PostRun: &UsageComponent{
				RequestsMade:     true,
				PromptTokens:     intPtr(12),
				CompletionTokens: intPtr(8),
				TotalTokens:      intPtr(20),
				CostAmount:       stringPtr("0.0001"),
				CostUnit:         stringPtr("credits"),
				CostProvenance:   stringPtr("nvidia"),
				Provider:         "nvidia",
				Model:            "meta/llama-3.1-8b-instruct",
			},
		},
	}
	if err := store.RecordSproutRun(ctx, run); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	loaded := loadRun(t, store, "run-components")
	if loaded.Usage.Execution == nil || loaded.Usage.PostRun == nil {
		t.Fatalf("loaded components = %+v, want both present", loaded.Usage)
	}
	if *loaded.Usage.Execution.CostAmount != "4.00" || *loaded.Usage.Execution.CostUnit != "USD" {
		t.Fatalf("execution cost = %+v", loaded.Usage.Execution)
	}
	if *loaded.Usage.PostRun.CostAmount != "0.0001" || *loaded.Usage.PostRun.CostUnit != "credits" {
		t.Fatalf("post-run cost = %+v", loaded.Usage.PostRun)
	}
	if loaded.Usage.Execution.Provider == loaded.Usage.PostRun.Provider {
		t.Fatal("components were collapsed onto one provider")
	}

	stored := rawSproutRunUsage(t, store, "run-components")
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored), &envelope); err != nil {
		t.Fatalf("stored usage is not JSON: %v %s", err, stored)
	}
	if _, ok := envelope["execution"]; !ok {
		t.Fatalf("stored usage JSON missing execution: %s", stored)
	}
	if _, ok := envelope["postRun"]; !ok {
		t.Fatalf("stored usage JSON missing postRun: %s", stored)
	}
	for _, banned := range []string{"totalCost", "totalTokens", "combined", "totalAmount"} {
		if _, ok := envelope[banned]; ok {
			t.Fatalf("stored usage JSON contains combined key %q: %s", banned, stored)
		}
	}
}

func TestSproutRunUsagePreservesExactCostAmount(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	const amount = "0.0000052349000001"

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-decimal", Status: "matured", StartedAt: time.Now().UTC(),
		Usage: SproutRunUsage{
			Execution: &UsageComponent{
				RequestsMade:   true,
				CostAmount:     stringPtr(amount),
				CostUnit:       stringPtr("USD"),
				CostProvenance: stringPtr("openrouter"),
			},
		},
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	loaded := loadRun(t, store, "run-decimal")
	if loaded.Usage.Execution == nil || loaded.Usage.Execution.CostAmount == nil {
		t.Fatal("cost amount did not reload")
	}
	if *loaded.Usage.Execution.CostAmount != amount {
		t.Fatalf("cost amount = %q, want exact %q", *loaded.Usage.Execution.CostAmount, amount)
	}
	stored := rawSproutRunUsage(t, store, "run-decimal")
	if !strings.Contains(stored, amount) {
		t.Fatalf("stored JSON lost the exact decimal literal: %s", stored)
	}
	if strings.Contains(stored, "5.2349e") || strings.Contains(stored, "5.2349000001e") {
		t.Fatalf("stored JSON used a float rendering: %s", stored)
	}
}

func TestSproutRunUsagePreservesNilVersusZeroTokens(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-zero", Status: "matured", StartedAt: time.Now().UTC(),
		Usage: SproutRunUsage{
			Execution: &UsageComponent{
				RequestsMade:     true,
				PromptTokens:     intPtr(0),
				CompletionTokens: nil,
				TotalTokens:      intPtr(0),
			},
		},
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	loaded := loadRun(t, store, "run-zero")
	exec := loaded.Usage.Execution
	if exec == nil {
		t.Fatal("execution component missing")
	}
	if exec.PromptTokens == nil || *exec.PromptTokens != 0 {
		t.Fatalf("PromptTokens = %v, want measured 0", exec.PromptTokens)
	}
	if exec.CompletionTokens != nil {
		t.Fatalf("CompletionTokens = %v, want absent", exec.CompletionTokens)
	}
	if exec.TotalTokens == nil || *exec.TotalTokens != 0 {
		t.Fatalf("TotalTokens = %v, want measured 0", exec.TotalTokens)
	}

	stored := rawSproutRunUsage(t, store, "run-zero")
	if !strings.Contains(stored, `"promptTokens":0`) {
		t.Fatalf("measured zero was omitted from JSON: %s", stored)
	}
	if strings.Contains(stored, `"completionTokens"`) {
		t.Fatalf("absent token field was persisted: %s", stored)
	}
}

func TestSproutRunUsageRequestsMadeWithNilFieldsPersistsComponent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-nil-usage", Status: "matured", StartedAt: time.Now().UTC(),
		Usage: SproutRunUsage{
			PostRun: &UsageComponent{
				RequestsMade: true,
				Provider:     "nvidia",
				Model:        "meta/llama-3.1-8b-instruct",
			},
		},
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	loaded := loadRun(t, store, "run-nil-usage")
	if loaded.Usage.Execution != nil {
		t.Fatalf("unrequested execution was stored: %+v", loaded.Usage.Execution)
	}
	if loaded.Usage.PostRun == nil {
		t.Fatal("post-run component missing")
	}
	if !loaded.Usage.PostRun.RequestsMade {
		t.Fatal("RequestsMade was not persisted")
	}
	if loaded.Usage.PostRun.PromptTokens != nil || loaded.Usage.PostRun.CostAmount != nil {
		t.Fatalf("nil usage fields were invented: %+v", loaded.Usage.PostRun)
	}
	if loaded.Usage.PostRun.Provider != "nvidia" || loaded.Usage.PostRun.Model != "meta/llama-3.1-8b-instruct" {
		t.Fatalf("post-run attribution = %+v", loaded.Usage.PostRun)
	}
}

func TestSproutRunUsageOmitsUnrequestedComponents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-empty", Status: "matured", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	loaded := loadRun(t, store, "run-empty")
	if loaded.Usage.Execution != nil || loaded.Usage.PostRun != nil {
		t.Fatalf("unrequested usage was stored: %+v", loaded.Usage)
	}
	if stored := rawSproutRunUsage(t, store, "run-empty"); stored != "" {
		t.Fatalf("empty usage wrote JSON: %q", stored)
	}
}

func TestSproutRunUsageOpeningWriteSettlesOnTerminal(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-settle", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun(running): %v", err)
	}
	if stored := rawSproutRunUsage(t, store, "run-settle"); stored != "" {
		t.Fatalf("opening write stored usage %q", stored)
	}

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-settle", Status: "matured", Output: "done", FinishedAt: time.Now().UTC(),
		Usage: SproutRunUsage{
			Execution: &UsageComponent{RequestsMade: true, PromptTokens: intPtr(7)},
		},
	}); err != nil {
		t.Fatalf("RecordSproutRun(matured): %v", err)
	}

	loaded := loadRun(t, store, "run-settle")
	if loaded.Usage.Execution == nil || loaded.Usage.Execution.PromptTokens == nil || *loaded.Usage.Execution.PromptTokens != 7 {
		t.Fatalf("terminal usage did not settle: %+v", loaded.Usage)
	}
}

func TestSproutRunUsageEmptyWriteDoesNotEraseStoredValue(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-keep", Status: "matured", StartedAt: time.Now().UTC(),
		Usage: SproutRunUsage{
			Execution: &UsageComponent{RequestsMade: true, PromptTokens: intPtr(11)},
		},
	}); err != nil {
		t.Fatalf("RecordSproutRun(usage): %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-keep", Status: "matured", Output: "compat", FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun(empty): %v", err)
	}

	loaded := loadRun(t, store, "run-keep")
	if loaded.Usage.Execution == nil || loaded.Usage.Execution.PromptTokens == nil || *loaded.Usage.Execution.PromptTokens != 11 {
		t.Fatalf("empty write erased usage: %+v", loaded.Usage)
	}
	if loaded.Output != "compat" {
		t.Fatalf("compatibility write did not update output: %+v", loaded)
	}
}

func TestSproutRunUsageMalformedJSONIsAnError(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(`INSERT INTO sproutruns (runId, status, startedAt, usage) VALUES ('run-bad', 'matured', '2026-01-01T00:00:00Z', '{not-json')`); err != nil {
		t.Fatalf("insert malformed usage: %v", err)
	}
	_, err := store.LoadSproutRuns(context.Background(), "", 10)
	if err == nil {
		t.Fatal("LoadSproutRuns succeeded on malformed usage JSON")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Fatalf("error = %v, want it to name usage", err)
	}
}

func TestSproutRunUsageBlankPreV3ValueMeansNoUsage(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(`INSERT INTO sproutruns (runId, status, startedAt, usage) VALUES ('run-blank', 'matured', '2026-01-01T00:00:00Z', '')`); err != nil {
		t.Fatalf("insert blank usage: %v", err)
	}
	loaded := loadRun(t, store, "run-blank")
	if loaded.Usage.Execution != nil || loaded.Usage.PostRun != nil {
		t.Fatalf("blank usage decoded as %+v", loaded.Usage)
	}
}

func TestPreColumnSproutRunsGainsUsageViaEnsureColumn(t *testing.T) {
	dbDir := t.TempDir()
	path := filepath.Join(dbDir, "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	const oldSchema = `
CREATE TABLE sproutruns (
	runId TEXT PRIMARY KEY,
	sessionId TEXT NOT NULL DEFAULT '',
	stepId TEXT NOT NULL DEFAULT '',
	origin TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	genotype TEXT NOT NULL DEFAULT '',
	transcript TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	output TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	startedAt TEXT NOT NULL,
	finishedAt TEXT NOT NULL DEFAULT ''
);
CREATE TABLE schemaMeta (id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL);
INSERT INTO schemaMeta (id, version) VALUES (1, 2);
INSERT INTO sproutruns (runId, sessionId, status, startedAt)
VALUES ('legacy-run', 'sess-legacy', 'matured', '2026-01-01T00:00:00Z');`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("write pre-column schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var version int
	if err := store.db.QueryRow(`SELECT version FROM schemaMeta WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 3 {
		t.Fatalf("schema version = %d, want 3", version)
	}

	var usageColumn string
	if err := store.db.QueryRow(`SELECT name FROM pragma_table_info('sproutruns') WHERE name = 'usage'`).Scan(&usageColumn); err != nil {
		t.Fatalf("usage column missing after migrate: %v", err)
	}

	loaded := loadRun(t, store, "legacy-run")
	if loaded.Usage.Execution != nil || loaded.Usage.PostRun != nil {
		t.Fatalf("pre-column row invented usage: %+v", loaded.Usage)
	}

	if err := store.RecordSproutRun(context.Background(), SproutRun{
		RunID: "legacy-run", Status: "matured",
		Usage: SproutRunUsage{Execution: &UsageComponent{RequestsMade: true, PromptTokens: intPtr(3)}},
	}); err != nil {
		t.Fatalf("RecordSproutRun after migrate: %v", err)
	}
	settled := loadRun(t, store, "legacy-run")
	if settled.Usage.Execution == nil || settled.Usage.Execution.PromptTokens == nil || *settled.Usage.Execution.PromptTokens != 3 {
		t.Fatalf("usage did not settle on migrated table: %+v", settled.Usage)
	}
}

func TestFreshSchemaIncludesUsageColumn(t *testing.T) {
	store := openTestStore(t)
	var name string
	if err := store.db.QueryRow(`SELECT name FROM pragma_table_info('sproutruns') WHERE name = 'usage'`).Scan(&name); err != nil {
		t.Fatalf("fresh sproutruns missing usage column: %v", err)
	}
}

func TestEncodeSproutRunUsageOmitsCombinedTotals(t *testing.T) {
	encoded, err := encodeSproutRunUsage(SproutRunUsage{
		Execution: &UsageComponent{RequestsMade: true, CostAmount: stringPtr("1.00"), CostUnit: stringPtr("USD")},
		PostRun:   &UsageComponent{RequestsMade: true, CostAmount: stringPtr("2.00"), CostUnit: stringPtr("credits")},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if _, ok := envelope["execution"]; !ok {
		t.Fatal("execution missing")
	}
	if _, ok := envelope["postRun"]; !ok {
		t.Fatal("postRun missing")
	}
	if _, ok := envelope["totalCost"]; ok {
		t.Fatal("combined totalCost key present")
	}
	if _, ok := envelope["totalTokens"]; ok {
		t.Fatal("combined totalTokens key present")
	}
}
