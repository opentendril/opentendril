package historydb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSproutRunRecordsItsDispatcher pins the field the read surface scopes by:
// a run stores who dispatched it and what it targeted, and gives both back.
func TestSproutRunRecordsItsDispatcher(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-1", SessionID: "sess-1", Pollen: "claude", Substrate: "myrepo",
		Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	runs, err := store.LoadSproutRuns(ctx, "sess-1", 10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("loaded %d run(s), want 1", len(runs))
	}
	if runs[0].Pollen != "claude" {
		t.Fatalf("Pollen = %q, want %q", runs[0].Pollen, "claude")
	}
	if runs[0].Substrate != "myrepo" {
		t.Fatalf("Substrate = %q, want %q", runs[0].Substrate, "myrepo")
	}
}

// TestRecordedRunNeverChangesHands is the immutability that makes ownership
// worth checking. A later write naming a different subject must not reassign a
// run that already has one — otherwise anything able to write a run record
// could hand somebody else's run to itself.
func TestRecordedRunNeverChangesHands(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-1", SessionID: "sess-1", Pollen: "claude", Substrate: "myrepo",
		Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-1", SessionID: "sess-1", Pollen: "codex", Substrate: "otherrepo",
		Status: "matured", FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record finish: %v", err)
	}

	runs, err := store.LoadSproutRuns(ctx, "sess-1", 10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if runs[0].Pollen != "claude" {
		t.Fatalf("Pollen = %q after a second write claimed it; ownership was reassigned", runs[0].Pollen)
	}
	if runs[0].Substrate != "myrepo" {
		t.Fatalf("Substrate = %q after a second write changed it", runs[0].Substrate)
	}
	// The rest of the record must still settle, or "never changes hands" would
	// have been bought by dropping the finishing write altogether.
	if runs[0].Status != "matured" {
		t.Fatalf("Status = %q, want matured", runs[0].Status)
	}
}

// TestUnownedRunAcquiresItsDispatcher covers the other direction: a row that
// reached the store with no subject on it is not frozen as ownerless.
func TestUnownedRunAcquiresItsDispatcher(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-1", SessionID: "sess-1", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-1", SessionID: "sess-1", Pollen: "claude", Substrate: "myrepo",
		Status: "matured", FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record finish: %v", err)
	}

	runs, err := store.LoadSproutRuns(ctx, "sess-1", 10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if runs[0].Pollen != "claude" {
		t.Fatalf("Pollen = %q, want claude", runs[0].Pollen)
	}
}

// TestSproutRunOwnersReportsEveryDispatcher is what the read surface asks
// before it releases a phytomer: every distinct subject that put a run there.
func TestSproutRunOwnersReportsEveryDispatcher(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	for _, run := range []SproutRun{
		{RunID: "a", SessionID: "sess-1", Pollen: "claude", Substrate: "myrepo"},
		{RunID: "b", SessionID: "sess-1", Pollen: "claude", Substrate: "myrepo"},
		{RunID: "c", SessionID: "sess-1", Pollen: "codex", Substrate: "myrepo"},
		{RunID: "d", SessionID: "sess-2", Pollen: "claude", Substrate: "elsewhere"},
	} {
		run.Status = "matured"
		run.StartedAt = time.Now().UTC()
		if err := store.RecordSproutRun(ctx, run); err != nil {
			t.Fatalf("record %s: %v", run.RunID, err)
		}
	}

	owners, err := store.SproutRunOwners(ctx, "sess-1")
	if err != nil {
		t.Fatalf("owners: %v", err)
	}
	// Two runs by claude collapse to one pairing; codex is reported separately;
	// the run in another phytomer is not reported at all.
	want := []SproutRunOwner{{Pollen: "claude", Substrate: "myrepo"}, {Pollen: "codex", Substrate: "myrepo"}}
	if len(owners) != len(want) {
		t.Fatalf("owners = %+v, want %+v", owners, want)
	}
	for i, owner := range owners {
		if owner != want[i] {
			t.Fatalf("owners[%d] = %+v, want %+v", i, owner, want[i])
		}
	}

	empty, err := store.SproutRunOwners(ctx, "sess-nothing")
	if err != nil {
		t.Fatalf("owners of an empty phytomer: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("owners of an empty phytomer = %+v, want none", empty)
	}
}

// TestOpeningAnEarlierGenerationAddsOwnership is the migration. A history.db
// written by the previous generation has no ownership columns at all; opening
// it with this binary must add them, keep the rows already there, and leave the
// pre-existing runs unowned rather than guessing at a subject for them.
func TestOpeningAnEarlierGenerationAddsOwnership(t *testing.T) {
	dbDir := t.TempDir()
	keyPath := filepath.Join(dbDir, "rhizome.key")
	if err := os.WriteFile(keyPath, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	path := filepath.Join(dbDir, "history.db")

	// The first-generation shape, written directly so the fixture cannot drift
	// with the current schema literal.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	const legacySchema = `
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
INSERT INTO schemaMeta (id, version) VALUES (1, 1);
INSERT INTO sproutruns (runId, sessionId, status, startedAt)
VALUES ('legacy-run', 'sess-legacy', 'matured', '2026-01-01T00:00:00Z');`
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("write legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
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

	runs, err := store.LoadSproutRuns(context.Background(), "sess-legacy", 10)
	if err != nil {
		t.Fatalf("load migrated runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "legacy-run" {
		t.Fatalf("migration lost the rows already there: %+v", runs)
	}
	if runs[0].Pollen != "" {
		t.Fatalf("a run written before ownership existed came back owned by %q", runs[0].Pollen)
	}

	// A run recorded after the migration uses the new columns for real.
	if err := store.RecordSproutRun(context.Background(), SproutRun{
		RunID: "new-run", SessionID: "sess-legacy", Pollen: "claude", Substrate: "myrepo",
		Status: "matured", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record after migration: %v", err)
	}
	owners, err := store.SproutRunOwners(context.Background(), "sess-legacy")
	if err != nil {
		t.Fatalf("owners after migration: %v", err)
	}
	if len(owners) != 2 {
		t.Fatalf("owners after migration = %+v, want the unowned legacy run and the new one", owners)
	}
}

// TestReopeningIsNotAMigration guards the idempotence the forward steps rely
// on: a database already at the current generation must open again unchanged,
// not fail on a column it already has.
func TestReopeningIsNotAMigration(t *testing.T) {
	dbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dbDir, "rhizome.key"), []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	path := filepath.Join(dbDir, "history.db")

	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.RecordSproutRun(context.Background(), SproutRun{
		RunID: "run-1", SessionID: "sess-1", Pollen: "claude", Substrate: "myrepo",
		Status: "matured", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	runs, err := second.LoadSproutRuns(context.Background(), "sess-1", 10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != 1 || runs[0].Pollen != "claude" {
		t.Fatalf("reopening changed the record: %+v", runs)
	}
}
