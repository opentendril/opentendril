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

	_ "modernc.org/sqlite"
)

func TestSeedRunRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, found, err := store.GetSeedRun(ctx, "seed-missing"); err != nil || found {
		t.Fatalf("GetSeedRun(missing) = found=%v err=%v, want false/nil", found, err)
	}

	started := time.Now().UTC()
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-seed-1", Substrate: "core", Goal: "make it pass",
		Status: "running", StartedAt: started,
	}); err != nil {
		t.Fatalf("record running: %v", err)
	}

	run, found, err := store.GetSeedRun(ctx, "seed-1")
	if err != nil || !found {
		t.Fatalf("get running: found=%v err=%v", found, err)
	}
	if run.Status != "running" || run.Pollen != "claude" || run.Substrate != "core" || run.PhytomerID != "tendril-seed-1" {
		t.Fatalf("running record = %+v", run)
	}

	byPhytomer, found, err := store.GetSeedRunByPhytomer(ctx, "tendril-seed-1")
	if err != nil || !found || byPhytomer.Handle != "seed-1" {
		t.Fatalf("GetSeedRunByPhytomer = found=%v err=%v run=%+v", found, err, byPhytomer)
	}

	// Settle: the same handle upserts the terminal Fruit.
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-seed-1", Substrate: "core", Status: "satisfied",
		Iterations: 2, Branch: "tendril/seed-1", Commit: "abc123", Diff: "the diff", Logs: "the logs",
		StartedAt: started, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record settled: %v", err)
	}

	run, _, err = store.GetSeedRun(ctx, "seed-1")
	if err != nil {
		t.Fatalf("get settled: %v", err)
	}
	if run.Status != "satisfied" || run.Iterations != 2 || run.Branch != "tendril/seed-1" || run.Diff != "the diff" || run.Commit != "abc123" {
		t.Fatalf("settled record = %+v", run)
	}
	if run.PhytomerID != "tendril-seed-1" {
		t.Fatalf("settled phytomer = %q, want the identity recorded at dispatch", run.PhytomerID)
	}
	if run.FinishedAt.IsZero() {
		t.Fatal("settled record has no FinishedAt")
	}
}

func TestSeedRunPhytomerIsImmutableOnSettle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-imm", Pollen: "claude", PhytomerID: "tendril-original", Substrate: "core",
		Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-imm", Pollen: "claude", PhytomerID: "tendril-forged", Substrate: "core",
		Status: "satisfied", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	run, _, err := store.GetSeedRun(ctx, "seed-imm")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if run.PhytomerID != "tendril-original" {
		t.Fatalf("phytomer mutated on settle: %q", run.PhytomerID)
	}
}

func TestSeedRunFruitPublicationDiagnosticRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	started := time.Now().UTC()
	diagnostic := &SeedPublicationDiagnostic{
		FailureCategory: "fruit-publication",
		ExecutionStatus: "satisfied",
		Phase:           "reconciliation",
		Outcome:         "reconciliation-unavailable",
		RetrySafe:       false,
		Message:         "read-only GitHub reconciliation could not establish the target state",
		RequestID:       "req-safe-123",
	}
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-publication-failure", Pollen: "claude", PhytomerID: "tendril-publication-failure",
		Substrate: "core", Status: "fruit-publication-failed", Iterations: 2,
		Diff: "completed diff", Logs: "completed logs", Error: diagnostic.Message,
		PublicationDiagnostic: diagnostic, StartedAt: started, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record publication failure: %v", err)
	}

	run, found, err := store.GetSeedRun(ctx, "seed-publication-failure")
	if err != nil || !found {
		t.Fatalf("get publication failure: found=%v err=%v", found, err)
	}
	if run.Status != "fruit-publication-failed" || run.Branch != "" || run.Commit != "" || run.Iterations != 2 {
		t.Fatalf("publication failure record = %+v", run)
	}
	if run.Diff != "completed diff" || run.Logs != "completed logs" {
		t.Fatalf("completed execution evidence = %+v", run)
	}
	if run.PublicationDiagnostic == nil || run.PublicationDiagnostic.RequestID != diagnostic.RequestID || run.PublicationDiagnostic.Message != diagnostic.Message {
		t.Fatalf("publication diagnostic = %+v", run.PublicationDiagnostic)
	}
	if len(run.VerificationDiagnostics) != 0 {
		t.Fatalf("invented verification diagnostics on a publication-only record: %+v", run.VerificationDiagnostics)
	}

	var rawObservation string
	if err := store.db.QueryRow(`SELECT observation FROM seedruns WHERE handle = ?`, "seed-publication-failure").Scan(&rawObservation); err != nil {
		t.Fatalf("read stored observation: %v", err)
	}
	if rawObservation == "" || strings.Contains(rawObservation, "Authorization") || strings.Contains(rawObservation, "PRIVATE_PROMPT_CONTENT") {
		t.Fatalf("unsafe publication observation = %q", rawObservation)
	}
}

func TestSeedRunVerificationDiagnosticsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	started := time.Now().UTC()
	code := 1
	diagnostics := []SeedVerificationDiagnostic{{
		Iteration: 1,
		Outcome:   "predicate-failed",
		ExitCode:  &code,
		TimedOut:  false,
		Message:   "verify command exited 1",
	}, {
		Iteration: 2,
		Outcome:   "infrastructure-failed",
		TimedOut:  true,
		Message:   "verify command timed out",
	}}
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-verify-diag", Pollen: "claude", PhytomerID: "tendril-verify-diag",
		Substrate: "core", Status: "exhausted", Iterations: 2,
		VerificationDiagnostics: diagnostics, StartedAt: started, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	run, found, err := store.GetSeedRun(ctx, "seed-verify-diag")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if len(run.VerificationDiagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", run.VerificationDiagnostics)
	}
	if run.VerificationDiagnostics[0].ExitCode == nil || *run.VerificationDiagnostics[0].ExitCode != 1 {
		t.Fatalf("exit code = %+v", run.VerificationDiagnostics[0])
	}
	if !run.VerificationDiagnostics[1].TimedOut || run.VerificationDiagnostics[1].Outcome != "infrastructure-failed" {
		t.Fatalf("timeout diagnostic = %+v", run.VerificationDiagnostics[1])
	}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "/home/") || strings.Contains(string(raw), "Bearer ") {
		t.Fatalf("unsafe material in seed run JSON: %s", raw)
	}
}

func TestRecordSeedRunRequiresHandle(t *testing.T) {
	store := openTestStore(t)
	if err := store.RecordSeedRun(context.Background(), SeedRun{Status: "running"}); err == nil {
		t.Fatal("a seed run with an empty handle was accepted")
	}
}

func TestOpeningEarlierSeedRunsLeavesPhytomerUnknown(t *testing.T) {
	dbDir := t.TempDir()
	path := filepath.Join(dbDir, "history.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	const legacySchema = `
CREATE TABLE seedruns (
	handle TEXT PRIMARY KEY,
	pollen TEXT NOT NULL DEFAULT '',
	substrate TEXT NOT NULL DEFAULT '',
	goal TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	iterations INTEGER NOT NULL DEFAULT 0,
	branch TEXT NOT NULL DEFAULT '',
	diff TEXT NOT NULL DEFAULT '',
	logs TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	startedAt TEXT NOT NULL,
	finishedAt TEXT NOT NULL DEFAULT ''
);
CREATE TABLE schemaMeta (id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL);
INSERT INTO schemaMeta (id, version) VALUES (1, 4);
INSERT INTO seedruns (handle, pollen, substrate, goal, status, startedAt)
VALUES ('legacy-seed', 'claude', 'core', 'old goal', 'satisfied', '2026-01-01T00:00:00Z');`
	const legacyVersion = 4
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("write legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var version int
	if err := store.db.QueryRow(`SELECT version FROM schemaMeta WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
	if version <= legacyVersion {
		t.Fatalf("schema version = %d after a shape change; it must advance past %d", version, legacyVersion)
	}

	run, found, err := store.GetSeedRun(context.Background(), "legacy-seed")
	if err != nil || !found {
		t.Fatalf("get legacy: found=%v err=%v", found, err)
	}
	if run.PhytomerID != "" || run.Commit != "" {
		t.Fatalf("legacy seed was assigned fabricated identity: %+v", run)
	}
	if run.Pollen != "claude" || run.Substrate != "core" || run.Status != "satisfied" {
		t.Fatalf("migration lost the legacy row: %+v", run)
	}
	if len(run.VerificationDiagnostics) != 0 {
		t.Fatalf("legacy seed was assigned invented verification facts: %+v", run.VerificationDiagnostics)
	}

	if _, found, err := store.GetSeedRunByPhytomer(context.Background(), "tendril-anything"); err != nil || found {
		t.Fatalf("legacy row became watchable under a fabricated phytomer: found=%v err=%v", found, err)
	}

	if err := store.RecordSeedRun(context.Background(), SeedRun{
		Handle: "new-seed", Pollen: "claude", PhytomerID: "tendril-new", Substrate: "core",
		Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record new: %v", err)
	}
	fresh, found, err := store.GetSeedRunByPhytomer(context.Background(), "tendril-new")
	if err != nil || !found || fresh.Handle != "new-seed" {
		t.Fatalf("new seed lookup: found=%v err=%v run=%+v", found, err, fresh)
	}
}
