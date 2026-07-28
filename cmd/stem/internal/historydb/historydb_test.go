package historydb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbDir := t.TempDir()
	keyPath := filepath.Join(dbDir, "rhizome.key")
	if err := os.WriteFile(keyPath, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	store, err := Open(context.Background(), filepath.Join(dbDir, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestLoggingEnabledToggle(t *testing.T) {
	t.Setenv(EnvDBLogging, "")
	if !LoggingEnabled() {
		t.Fatal("expected logging enabled by default")
	}

	for _, off := range []string{"false", "0", "off", "FALSE"} {
		t.Setenv(EnvDBLogging, off)
		if LoggingEnabled() {
			t.Fatalf("expected %q to disable logging", off)
		}
	}

	t.Setenv(EnvDBLogging, "false")
	store, err := OpenFromEnv(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("OpenFromEnv: %v", err)
	}
	if store != nil {
		t.Fatal("expected nil store when logging disabled")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sess := session.Phytomer{
		ID:           "tendril-test1",
		Origin:       session.OriginCLI,
		CreatedAt:    now,
		LastActiveAt: now,
		Preferences:  session.Preferences{Model: "claude-fable-5", Genotype: "go-dev"},
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	sess.Preferences.Provider = "anthropic"
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession upsert: %v", err)
	}

	loaded, err := store.LoadSessions(ctx)
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 session, got %d", len(loaded))
	}
	if loaded[0].Preferences.Provider != "anthropic" || loaded[0].Preferences.Model != "claude-fable-5" {
		t.Fatalf("preferences did not round-trip: %+v", loaded[0].Preferences)
	}

	one, ok, err := store.LoadSession(ctx, "tendril-test1")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !ok {
		t.Fatal("LoadSession reported missing for a saved session")
	}
	if one.Preferences.Provider != "anthropic" || one.Preferences.Genotype != "go-dev" {
		t.Fatalf("LoadSession preferences mismatch: %+v", one.Preferences)
	}

	if _, ok, err := store.LoadSession(ctx, "tendril-missing"); err != nil || ok {
		t.Fatalf("LoadSession missing id: ok=%v err=%v (want ok=false, err=nil)", ok, err)
	}
}

func TestMessagesRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	for _, content := range []string{"one", "two", "three"} {
		if err := store.AppendMessage(ctx, session.Message{
			SessionID: "tendril-test1",
			Role:      "user",
			Content:   content,
		}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	messages, err := store.LoadMessages(ctx, "tendril-test1", 2)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "two" || messages[1].Content != "three" {
		t.Fatalf("expected last two messages in order, got %+v", messages)
	}
}

func TestEventPersistenceViaBusSink(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	bus := eventbus.New()
	bus.AttachSink(store, 0, "historydb")

	bus.Publish(eventbus.Event{
		Type:      eventbus.EventSproutEmerged,
		Source:    "step-1",
		SessionID: "tendril-test1",
		Data:      map[string]interface{}{"branch": "shadow-1"},
	})
	bus.Shutdown() // drains the sink pump

	records, err := store.LoadEvents(ctx, "tendril-test1", 10)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 event, got %d", len(records))
	}
	if records[0].Type != string(eventbus.EventSproutEmerged) || records[0].Data["branch"] != "shadow-1" {
		t.Fatalf("event did not round-trip: %+v", records[0])
	}
}

func TestSproutRunUpsert(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	run := SproutRun{
		RunID:      "step-42",
		SessionID:  "tendril-test1",
		StepID:     "step-42",
		Origin:     "scheduler", // scheduled runs stay attributable
		Transcript: "fix the flaky test",
		Status:     "running",
		StartedAt:  time.Now().UTC(),
	}
	if err := store.RecordSproutRun(ctx, run); err != nil {
		t.Fatalf("RecordSproutRun start: %v", err)
	}

	run.Status = "matured"
	run.Output = "done"
	run.FinishedAt = time.Now().UTC()
	if err := store.RecordSproutRun(ctx, run); err != nil {
		t.Fatalf("RecordSproutRun finish: %v", err)
	}

	runs, err := store.LoadSproutRuns(ctx, "tendril-test1", 10)
	if err != nil {
		t.Fatalf("LoadSproutRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected upsert to keep 1 run, got %d", len(runs))
	}
	if runs[0].Status != "matured" || runs[0].Output != "done" || runs[0].FinishedAt.IsZero() {
		t.Fatalf("run did not upsert: %+v", runs[0])
	}
	if runs[0].Origin != "scheduler" {
		t.Fatalf("a scheduler-originated run must read back origin %q, got %q", "scheduler", runs[0].Origin)
	}
}

func TestEncryptionAtRest_CiphertextOnDisk(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Insert test data
	if err := store.AppendMessage(ctx, session.Message{
		SessionID: "s1", Role: "user", Content: "secret_msg",
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "r1", SessionID: "s1", Transcript: "secret_transcript", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed1", Diff: "secret_diff", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordSeedRun: %v", err)
	}

	if err := store.RecordEvent(ctx, eventbus.Event{
		SessionID: "s1", Type: "test_event", Data: map[string]any{"key": "val"},
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	// Open raw sql to inspect disk format
	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("Raw sql.Open: %v", err)
	}
	defer raw.Close()

	var content, sessionId string
	if err := raw.QueryRow("SELECT content, sessionId FROM messages WHERE sessionId='s1' LIMIT 1").Scan(&content, &sessionId); err != nil {
		t.Fatalf("Raw select message: %v", err)
	}
	if !strings.HasPrefix(content, heartwood.Prefix) {
		t.Errorf("expected ciphertext prefix for content, got: %q", content)
	}
	if sessionId != "s1" {
		t.Errorf("expected plaintext sessionId, got: %q", sessionId)
	}

	var transcript string
	if err := raw.QueryRow("SELECT transcript FROM sproutruns WHERE runId='r1' LIMIT 1").Scan(&transcript); err != nil {
		t.Fatalf("Raw select sprout: %v", err)
	}
	if !strings.HasPrefix(transcript, heartwood.Prefix) {
		t.Errorf("expected ciphertext prefix for transcript, got: %q", transcript)
	}

	var diff string
	if err := raw.QueryRow("SELECT diff FROM seedruns WHERE handle='seed1' LIMIT 1").Scan(&diff); err != nil {
		t.Fatalf("Raw select seed: %v", err)
	}
	if !strings.HasPrefix(diff, heartwood.Prefix) {
		t.Errorf("expected ciphertext prefix for diff, got: %q", diff)
	}

	var data string
	if err := raw.QueryRow("SELECT data FROM events WHERE type='test_event' LIMIT 1").Scan(&data); err != nil {
		t.Fatalf("Raw select event: %v", err)
	}
	if !strings.HasPrefix(data, heartwood.Prefix) {
		t.Errorf("expected ciphertext prefix for event data, got: %q", data)
	}
}

func TestEncryptionAtRest_LegacyPlaintextReadCompat(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Manually insert legacy plaintext
	_, err := store.db.Exec(`INSERT INTO messages (sessionId, role, content, model, createdAt) VALUES ('s2', 'user', 'legacy_text', 'mod', '2023-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("Raw insert legacy: %v", err)
	}

	msgs, err := store.LoadMessages(ctx, "s2", 10)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "legacy_text" {
		t.Fatalf("Expected legacy plaintext read compat, got: %+v", msgs)
	}
}

func TestEncryptionAtRest_OptOut(t *testing.T) {
	t.Setenv("TENDRIL_ENCRYPT_AT_REST", "off")
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.AppendMessage(ctx, session.Message{
		SessionID: "s3", Role: "user", Content: "opt_out_msg",
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("Raw sql.Open: %v", err)
	}
	defer raw.Close()

	var content string
	if err := raw.QueryRow("SELECT content FROM messages WHERE sessionId='s3' LIMIT 1").Scan(&content); err != nil {
		t.Fatalf("Raw select: %v", err)
	}
	if content != "opt_out_msg" {
		t.Fatalf("expected plaintext written when opted out, got: %q", content)
	}

	msgs, err := store.LoadMessages(ctx, "s3", 10)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "opt_out_msg" {
		t.Fatalf("LoadMessages failed after opt-out write")
	}
}

func TestTelemetryRedactThenEncrypt(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	err := store.RecordEvent(ctx, eventbus.Event{
		SessionID: "s4",
		Type:      "redact_test",
		Data:      map[string]any{"token": "sk-abc-1234567890"},
	})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	events, err := store.LoadEvents(ctx, "s4", 10)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(events))
	}

	token, ok := events[0].Data["token"].(string)
	if !ok || token != "[REDACTED]" {
		t.Fatalf("expected token to be [REDACTED], got: %v", token)
	}
}

func TestSchemaVersionStampedOnFreshDatabase(t *testing.T) {
	store := openTestStore(t)

	var version int
	err := store.db.QueryRow(`SELECT version FROM schemaMeta WHERE id = 1`).Scan(&version)
	if err != nil {
		t.Fatalf("QueryRow version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("expected version %d, got %d", currentSchemaVersion, version)
	}
}

func TestSchemaVersionBackstampsPreVersioningDatabase(t *testing.T) {
	dbDir := t.TempDir()
	path := filepath.Join(dbDir, "history.db")

	// Create database with original 5 tables and no schemaMeta
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	const oldSchema = `
CREATE TABLE IF NOT EXISTS sessions (
	sessionId TEXT PRIMARY KEY,
	origin TEXT NOT NULL,
	createdAt TEXT NOT NULL,
	lastActiveAt TEXT NOT NULL,
	preferences TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sessionId TEXT NOT NULL,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	createdAt TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS messagesBySession ON messages(sessionId, id);

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sessionId TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	data TEXT NOT NULL DEFAULT '{}',
	createdAt TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS eventsBySession ON events(sessionId, id);
CREATE INDEX IF NOT EXISTS eventsByType ON events(type, id);

CREATE TABLE IF NOT EXISTS sproutruns (
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
CREATE INDEX IF NOT EXISTS sproutrunsBySession ON sproutruns(sessionId, startedAt);

CREATE TABLE IF NOT EXISTS seedruns (
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
CREATE INDEX IF NOT EXISTS seedrunsByPollen ON seedruns(pollen, startedAt);`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("Exec oldSchema: %v", err)
	}
	db.Close()

	keyPath := filepath.Join(dbDir, "rhizome.key")
	if err := os.WriteFile(keyPath, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	// Open via Open/initSchema
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRow(`SELECT version FROM schemaMeta WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("QueryRow version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("expected backstamped version %d, got %d", currentSchemaVersion, version)
	}
}

func TestSchemaVersionRejectsNewerVersion(t *testing.T) {
	dbDir := t.TempDir()
	path := filepath.Join(dbDir, "history.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	const schemaWithNewerVersion = `
CREATE TABLE schemaMeta (id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL);
INSERT INTO schemaMeta (id, version) VALUES (1, 999);`
	if _, err := db.Exec(schemaWithNewerVersion); err != nil {
		t.Fatalf("Exec schema: %v", err)
	}
	db.Close()

	keyPath := filepath.Join(dbDir, "rhizome.key")
	if err := os.WriteFile(keyPath, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	store, err := Open(context.Background(), path)
	if err == nil {
		if store != nil {
			store.Close()
		}
		t.Fatalf("expected error opening database with newer schema version, got nil")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("expected error about newer version, got: %v", err)
	}
}

func TestPruneOlderThanDeletesOldRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour)
	newTime := time.Now().UTC()

	// 1. messages
	_ = store.AppendMessage(ctx, session.Message{SessionID: "s1", Role: "user", Content: "old", CreatedAt: oldTime})
	_ = store.AppendMessage(ctx, session.Message{SessionID: "s1", Role: "user", Content: "new", CreatedAt: newTime})

	// 2. events
	_ = store.RecordEvent(ctx, eventbus.Event{SessionID: "s1", Type: "old_event", Timestamp: oldTime})
	_ = store.RecordEvent(ctx, eventbus.Event{SessionID: "s1", Type: "new_event", Timestamp: newTime})

	// 3. sproutruns
	_ = store.RecordSproutRun(ctx, SproutRun{RunID: "old-sprout", SessionID: "s1", Status: "running", StartedAt: oldTime})
	_ = store.RecordSproutRun(ctx, SproutRun{RunID: "new-sprout", SessionID: "s1", Status: "running", StartedAt: newTime})

	// 4. seedruns
	_ = store.RecordSeedRun(ctx, SeedRun{Handle: "old-seed", Status: "running", StartedAt: oldTime})
	_ = store.RecordSeedRun(ctx, SeedRun{Handle: "new-seed", Status: "running", StartedAt: newTime})

	cutoff := time.Now().UTC().Add(-50 * 24 * time.Hour)
	n, err := store.PruneOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if n != 4 {
		t.Fatalf("expected 4 rows deleted, got %d", n)
	}

	msgs, _ := store.LoadMessages(ctx, "s1", 10)
	if len(msgs) != 1 || msgs[0].Content != "new" {
		t.Fatalf("expected 1 new message, got %+v", msgs)
	}

	events, _ := store.LoadEvents(ctx, "s1", 10)
	if len(events) != 1 || events[0].Type != string("new_event") {
		t.Fatalf("expected 1 new event, got %+v", events)
	}

	sprouts, _ := store.LoadSproutRuns(ctx, "s1", 10)
	if len(sprouts) != 1 || sprouts[0].RunID != "new-sprout" {
		t.Fatalf("expected 1 new sprout run, got %+v", sprouts)
	}

	_, okOld, _ := store.GetSeedRun(ctx, "old-seed")
	if okOld {
		t.Fatalf("expected old seed run to be deleted")
	}
	_, okNew, _ := store.GetSeedRun(ctx, "new-seed")
	if !okNew {
		t.Fatalf("expected new seed run to remain")
	}
}

func TestPruneOlderThanNeverTouchesSessions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour)
	sess := session.Phytomer{
		ID:           "s-old",
		Origin:       session.OriginCLI,
		CreatedAt:    oldTime,
		LastActiveAt: oldTime,
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	cutoff := time.Now().UTC().Add(-50 * 24 * time.Hour)
	n, err := store.PruneOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows deleted, got %d", n)
	}

	_, ok, _ := store.LoadSession(ctx, "s-old")
	if !ok {
		t.Fatalf("expected session to remain")
	}
}

func TestPruneOlderThanSkipsVacuumOnNoOp(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	cutoff := time.Now().UTC().Add(-100 * 24 * time.Hour)
	n, err := store.PruneOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows deleted, got %d", n)
	}
}

func TestHistoryRetentionDaysFromEnv(t *testing.T) {
	t.Setenv(EnvHistoryRetentionDays, "")
	if got := historyRetentionDaysFromEnv(); got != 0 {
		t.Errorf("unset expected 0, got %d", got)
	}

	t.Setenv(EnvHistoryRetentionDays, "30")
	if got := historyRetentionDaysFromEnv(); got != 30 {
		t.Errorf("valid '30' expected 30, got %d", got)
	}

	t.Setenv(EnvHistoryRetentionDays, "invalid")
	if got := historyRetentionDaysFromEnv(); got != 0 {
		t.Errorf("invalid string expected 0, got %d", got)
	}

	t.Setenv(EnvHistoryRetentionDays, "-5")
	if got := historyRetentionDaysFromEnv(); got != 0 {
		t.Errorf("non-positive expected 0, got %d", got)
	}
}
