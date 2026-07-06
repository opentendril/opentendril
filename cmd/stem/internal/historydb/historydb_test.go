package historydb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
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
	sess := session.Session{
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
}
