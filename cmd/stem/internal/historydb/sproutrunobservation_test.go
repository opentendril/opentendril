package historydb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFreshSchemaIncludesObservationColumns(t *testing.T) {
	store := openTestStore(t)
	for _, column := range []string{"provider", "observation"} {
		var name string
		if err := store.db.QueryRow(`SELECT name FROM pragma_table_info('sproutruns') WHERE name = ?`, column).Scan(&name); err != nil {
			t.Fatalf("fresh sproutruns missing %s column: %v", column, err)
		}
	}
	for _, column := range []string{"failureStage", "diagnosticCode", "terrariumProvider"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sproutruns') WHERE name = ?`, column).Scan(&count); err != nil {
			t.Fatalf("inspect sproutruns for %s: %v", column, err)
		}
		if count != 0 {
			t.Fatalf("fresh sproutruns unexpectedly includes %s column", column)
		}
	}
}

func TestSproutRunObservationRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	run := SproutRun{
		RunID:                    "run-observe",
		SessionID:                "s1",
		Provider:                 "openrouter",
		Model:                    "anthropic/claude-sonnet-4.6",
		Status:                   "withered",
		StartedAt:                time.Now().UTC(),
		FinishedAt:               time.Now().UTC(),
		Outcome:                  "failed",
		FailureCategory:          "provider-auth-rejected",
		FailureStage:             "provider-preflight",
		DiagnosticCode:           "provider-preflight-rejected",
		ProviderRequestAttempted: true,
		ToolInvocations:          0,
		ProviderDiagnostic: &ProviderDiagnostic{
			StatusCode: 401,
			Message:    "User not found",
			Provider:   "openrouter",
		},
	}
	if err := store.RecordSproutRun(ctx, run); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	loaded := loadRun(t, store, "run-observe")
	if loaded.Provider != "openrouter" {
		t.Fatalf("Provider = %q, want openrouter", loaded.Provider)
	}
	if loaded.FailureCategory != "provider-auth-rejected" {
		t.Fatalf("FailureCategory = %q", loaded.FailureCategory)
	}
	if loaded.Outcome != "failed" {
		t.Fatalf("Outcome = %q", loaded.Outcome)
	}
	if loaded.FailureStage != "provider-preflight" || loaded.DiagnosticCode != "provider-preflight-rejected" {
		t.Fatalf("failure provenance = %q/%q", loaded.FailureStage, loaded.DiagnosticCode)
	}
	if !loaded.ProviderRequestAttempted {
		t.Fatal("ProviderRequestAttempted = false")
	}
	if loaded.ToolInvocations != 0 {
		t.Fatalf("ToolInvocations = %d, want 0", loaded.ToolInvocations)
	}
	if loaded.ProviderDiagnostic == nil || loaded.ProviderDiagnostic.StatusCode != 401 || loaded.ProviderDiagnostic.Message != "User not found" {
		t.Fatalf("ProviderDiagnostic = %+v", loaded.ProviderDiagnostic)
	}

	raw, err := json.Marshal(loaded)
	if err != nil {
		t.Fatalf("marshal loaded run: %v", err)
	}
	if strings.Contains(string(raw), "sk-") || strings.Contains(strings.ToLower(string(raw)), "bearer ") {
		t.Fatalf("persisted JSON leaked a credential: %s", raw)
	}
}

func TestHistoricalSproutObservationWithoutProvenanceStaysEmpty(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID:     "run-historical",
		SessionID: "s1",
		Status:    "withered",
		Error:     "permission denied for /home/operator/private substrate",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}
	// Match an older structured envelope: it has failure category and the
	// existing observations, but predates lifecycle provenance fields.
	legacyObservation := `{"outcome":"failed","failureCategory":"execution-failed"}`
	if _, err := store.db.ExecContext(ctx, `UPDATE sproutruns SET observation = ? WHERE runId = ?`, legacyObservation, "run-historical"); err != nil {
		t.Fatalf("write historical observation envelope: %v", err)
	}

	loaded := loadRun(t, store, "run-historical")
	if loaded.FailureStage != "" || loaded.DiagnosticCode != "" {
		t.Fatalf("historical provenance = %q/%q, want both absent", loaded.FailureStage, loaded.DiagnosticCode)
	}
	if !strings.Contains(loaded.Error, "permission denied") {
		t.Fatalf("raw historical error = %q, want persisted legacy text", loaded.Error)
	}
}

func TestSproutRunTerrariumProviderRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for _, providerName := range []string{"docker", "gvisor", "firecracker", "host"} {
		runID := "run-" + providerName
		if err := store.RecordSproutRun(ctx, SproutRun{
			RunID:             runID,
			SessionID:         "s1",
			Status:            "matured",
			Outcome:           "complete",
			TerrariumProvider: providerName,
			StartedAt:         time.Now().UTC(),
			FinishedAt:        time.Now().UTC(),
		}); err != nil {
			t.Fatalf("RecordSproutRun(%s): %v", providerName, err)
		}
		loaded := loadRun(t, store, runID)
		if loaded.TerrariumProvider != providerName {
			t.Fatalf("TerrariumProvider = %q, want %q", loaded.TerrariumProvider, providerName)
		}
		var raw string
		if err := store.db.QueryRowContext(ctx, `SELECT observation FROM sproutruns WHERE runId = ?`, runID).Scan(&raw); err != nil {
			t.Fatalf("read observation: %v", err)
		}
		if !strings.Contains(raw, `"terrariumProvider":"`+providerName+`"`) {
			t.Fatalf("observation envelope = %s, want terrariumProvider %s", raw, providerName)
		}
	}

	var columnCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sproutruns') WHERE name = 'terrariumProvider'`).Scan(&columnCount); err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	if columnCount != 0 {
		t.Fatal("terrariumProvider was stored as a new column")
	}
}

func TestHistoricalSproutObservationWithoutTerrariumProviderStaysAbsent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID:     "run-historical-boundary",
		SessionID: "s1",
		Status:    "withered",
		Outcome:   "failed",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}
	legacy := `{"outcome":"failed","failureCategory":"execution-failed","providerRequestAttempted":true,"toolInvocations":2}`
	if _, err := store.db.ExecContext(ctx, `UPDATE sproutruns SET observation = ? WHERE runId = ?`, legacy, "run-historical-boundary"); err != nil {
		t.Fatalf("write historical observation: %v", err)
	}
	loaded := loadRun(t, store, "run-historical-boundary")
	if loaded.TerrariumProvider != "" {
		t.Fatalf("historical TerrariumProvider = %q, want absent", loaded.TerrariumProvider)
	}
	if loaded.Outcome != "failed" || loaded.ToolInvocations != 2 || !loaded.ProviderRequestAttempted {
		t.Fatalf("historical observation was rewritten: %+v", loaded)
	}
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "docker")
	reloaded := loadRun(t, store, "run-historical-boundary")
	if reloaded.TerrariumProvider != "" {
		t.Fatalf("configuration backfilled historical provider %q", reloaded.TerrariumProvider)
	}
}

func TestRecordSproutTerrariumProviderUpdatesRunningRow(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	opened := SproutRun{
		RunID: "run-live", SessionID: "s1", StepID: "run-live",
		Pollen: "claude", Substrate: "myrepo", Status: "running",
		Output: "keep-me", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if err := store.RecordSproutRun(ctx, opened); err != nil {
		t.Fatalf("opening write: %v", err)
	}
	if err := store.RecordSproutTerrariumProvider(ctx, opened.RunID, "gvisor"); err != nil {
		t.Fatalf("RecordSproutTerrariumProvider: %v", err)
	}
	running := loadRun(t, store, opened.RunID)
	if running.Status != "running" || !running.FinishedAt.IsZero() {
		t.Fatalf("running row was settled: %+v", running)
	}
	if running.TerrariumProvider != "gvisor" {
		t.Fatalf("running TerrariumProvider = %q, want gvisor", running.TerrariumProvider)
	}
	if running.Pollen != "claude" || running.Substrate != "myrepo" || running.Output != "keep-me" {
		t.Fatalf("running update rewrote the row: %+v", running)
	}
	if err := store.RecordSproutTerrariumProvider(ctx, opened.RunID, "docker"); err != nil {
		t.Fatalf("second provider write: %v", err)
	}
	if got := loadRun(t, store, opened.RunID).TerrariumProvider; got != "gvisor" {
		t.Fatalf("recorded provider changed to %q", got)
	}

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: opened.RunID, Status: "matured", Outcome: "complete",
		TerrariumProvider: "gvisor", StartedAt: opened.StartedAt, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("terminal write: %v", err)
	}
	settled := loadRun(t, store, opened.RunID)
	if settled.Status != "matured" || settled.TerrariumProvider != "gvisor" {
		t.Fatalf("terminal row = %+v, want matured gvisor", settled)
	}
}

func TestRecordSproutTerrariumProviderDoesNotInsertOrFabricate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.RecordSproutTerrariumProvider(ctx, "missing-run", "docker"); err == nil {
		t.Fatal("missing run accepted a provider write")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sproutruns`).Scan(&count); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if count != 0 {
		t.Fatalf("missing run inserted %d rows", count)
	}

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-empty", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("opening write: %v", err)
	}
	if err := store.RecordSproutTerrariumProvider(ctx, "run-empty", "  "); err != nil {
		t.Fatalf("empty provider: %v", err)
	}
	if got := loadRun(t, store, "run-empty").TerrariumProvider; got != "" {
		t.Fatalf("empty provider was stored as %q", got)
	}
	var raw string
	if err := store.db.QueryRow(`SELECT observation FROM sproutruns WHERE runId = 'run-empty'`).Scan(&raw); err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if strings.Contains(raw, "terrariumProvider") {
		t.Fatalf("empty provider wrote an observation fact: %s", raw)
	}
}

func TestSproutRunObservationCompatibilityWriteDoesNotErase(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID:                    "run-keep",
		Status:                   "withered",
		StartedAt:                time.Now().UTC(),
		FailureCategory:          "provider-auth-rejected",
		ProviderRequestAttempted: true,
		ProviderDiagnostic:       &ProviderDiagnostic{StatusCode: 401, Message: "User not found"},
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-keep", Status: "withered",
	}); err != nil {
		t.Fatalf("compat write: %v", err)
	}
	loaded := loadRun(t, store, "run-keep")
	if loaded.FailureCategory != "provider-auth-rejected" {
		t.Fatalf("compat write erased observation: %+v", loaded)
	}
}
