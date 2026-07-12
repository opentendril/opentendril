package session

import (
	"context"
	"testing"
)

func TestSproutAssignsUniqueSproutIDs(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		s, err := m.Sprout(context.Background(), OriginCLI, Preferences{})
		if err != nil {
			t.Fatalf("Sprout: %v", err)
		}
		if !ValidID(s.ID) {
			t.Fatalf("Sprout produced invalid ID %q", s.ID)
		}
		if seen[s.ID] {
			t.Fatalf("duplicate session ID %q", s.ID)
		}
		seen[s.ID] = true
	}
}

func TestGetOrSproutAdoptsWellFormedIDs(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	s, err := m.GetOrSprout(context.Background(), "tendril-abc123", OriginREST)
	if err != nil {
		t.Fatalf("GetOrSprout: %v", err)
	}
	if s.ID != "tendril-abc123" {
		t.Fatalf("expected adopted ID, got %q", s.ID)
	}

	again, err := m.GetOrSprout(context.Background(), "tendril-abc123", OriginMCP)
	if err != nil {
		t.Fatalf("GetOrSprout second call: %v", err)
	}
	if again.Origin != OriginREST {
		t.Fatalf("expected original origin to be preserved, got %q", again.Origin)
	}

	if _, err := m.GetOrSprout(context.Background(), "../etc/passwd", OriginREST); err == nil {
		t.Fatal("expected malformed ID to be rejected")
	}
}

func TestPreferencesMergeAndIsolationBetweenSessions(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	first, _ := m.Sprout(context.Background(), OriginCLI, Preferences{Model: "claude-fable-5"})
	second, _ := m.Sprout(context.Background(), OriginCLI, Preferences{})

	updated, err := m.UpdatePreferences(context.Background(), first.ID, Preferences{Genotype: "go-dev"})
	if err != nil {
		t.Fatalf("UpdatePreferences: %v", err)
	}
	if updated.Preferences.Model != "claude-fable-5" || updated.Preferences.Genotype != "go-dev" {
		t.Fatalf("merge lost fields: %+v", updated.Preferences)
	}

	other, _ := m.Get(second.ID)
	if other.Preferences.Model != "" || other.Preferences.Genotype != "" {
		t.Fatalf("preferences leaked across sessions: %+v", other.Preferences)
	}
}

func TestRecordMessageAndInMemoryHistory(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	s, _ := m.Sprout(context.Background(), OriginREST, Preferences{})
	for _, content := range []string{"hello", "world"} {
		if err := m.RecordMessage(context.Background(), Message{
			SessionID: s.ID,
			Role:      "user",
			Content:   content,
		}); err != nil {
			t.Fatalf("RecordMessage: %v", err)
		}
	}

	history, err := m.History(context.Background(), s.ID, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 2 || history[0].Content != "hello" || history[1].Content != "world" {
		t.Fatalf("unexpected history: %+v", history)
	}

	if err := m.RecordMessage(context.Background(), Message{SessionID: "tendril-missing", Role: "user", Content: "x"}); err == nil {
		t.Fatal("expected error recording to unknown session")
	}
}
