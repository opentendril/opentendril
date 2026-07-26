package session

import (
	"context"
	"testing"
)

func TestInitiateAssignsUniqueInitiateIDs(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		s, err := m.Initiate(context.Background(), OriginCLI, Preferences{})
		if err != nil {
			t.Fatalf("Initiate: %v", err)
		}
		if !ValidID(s.ID) {
			t.Fatalf("Initiate produced invalid ID %q", s.ID)
		}
		if seen[s.ID] {
			t.Fatalf("duplicate session ID %q", s.ID)
		}
		seen[s.ID] = true
	}
}

func TestGetOrInitiateAdoptsWellFormedIDs(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	s, err := m.GetOrInitiate(context.Background(), "tendril-abc123", OriginREST)
	if err != nil {
		t.Fatalf("GetOrInitiate: %v", err)
	}
	if s.ID != "tendril-abc123" {
		t.Fatalf("expected adopted ID, got %q", s.ID)
	}

	again, err := m.GetOrInitiate(context.Background(), "tendril-abc123", OriginMCP)
	if err != nil {
		t.Fatalf("GetOrInitiate second call: %v", err)
	}
	if again.Origin != OriginREST {
		t.Fatalf("expected original origin to be preserved, got %q", again.Origin)
	}

	if _, err := m.GetOrInitiate(context.Background(), "../etc/passwd", OriginREST); err == nil {
		t.Fatal("expected malformed ID to be rejected")
	}
}

func TestPreferencesMergeAndIsolationBetweenSessions(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	first, _ := m.Initiate(context.Background(), OriginCLI, Preferences{Model: "claude-fable-5"})
	second, _ := m.Initiate(context.Background(), OriginCLI, Preferences{})

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

func TestInitiateRetriesOnCollisionPreservesExistingSession(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Pre-populate a session with a known colliding ID.
	collidingID := "tendril-collision-fixture"
	sentinel := &sessionState{session: Phytomer{ID: collidingID, Origin: OriginCLI}}
	m.sessions[collidingID] = sentinel

	// Override the seam: first call returns the colliding ID, subsequent
	// calls produce a real unique ID so the retry succeeds.
	calls := 0
	orig := newSessionID
	t.Cleanup(func() { newSessionID = orig })
	newSessionID = func() string {
		calls++
		if calls == 1 {
			return collidingID
		}
		return NewID()
	}

	got, err := m.Initiate(context.Background(), OriginREST, Preferences{})
	if err != nil {
		t.Fatalf("Initiate should succeed after retry, got error: %v", err)
	}

	// (a) Returned ID must differ from the pre-existing session.
	if got.ID == collidingID {
		t.Fatalf("Initiate returned the colliding ID %q — existing session would have been overwritten", collidingID)
	}

	// (b) Pre-existing session's in-memory state must be untouched.
	if m.sessions[collidingID] != sentinel {
		t.Fatalf("pre-existing session state was overwritten by Initiate")
	}
}

func TestInitiateErrorsWhenRetriesExhausted(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Pre-populate a session with the fixed colliding ID.
	collidingID := "tendril-always-collides"
	m.sessions[collidingID] = &sessionState{session: Phytomer{ID: collidingID}}

	// Override the seam to always return the same colliding ID — it will
	// never resolve within the retry budget.
	orig := newSessionID
	t.Cleanup(func() { newSessionID = orig })
	newSessionID = func() string { return collidingID }

	_, err = m.Initiate(context.Background(), OriginREST, Preferences{})
	if err == nil {
		t.Fatal("Initiate should have returned an error after exhausting retries, got nil")
	}

	// The pre-existing session must still be intact (no silent overwrite).
	if _, ok := m.sessions[collidingID]; !ok {
		t.Fatal("pre-existing session was removed from the map during exhausted-retry path")
	}
}

func TestRecordMessageAndInMemoryHistory(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	s, _ := m.Initiate(context.Background(), OriginREST, Preferences{})
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
