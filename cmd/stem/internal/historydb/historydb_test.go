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
	bus.AttachSink(store, 0)

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
