package session

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
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

	other, _ := m.Get(context.Background(), second.ID)
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

// memStore is a concurrency-safe in-memory Store used to prove eviction is
// transparent: idle entries leave m.sessions but survive on the durable side.
type memStore struct {
	mu       sync.Mutex
	sessions map[string]Phytomer
	messages map[string][]Message
}

func newMemStore() *memStore {
	return &memStore{
		sessions: make(map[string]Phytomer),
		messages: make(map[string][]Message),
	}
}

func (s *memStore) SaveSession(_ context.Context, p Phytomer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[p.ID] = p
	return nil
}

func (s *memStore) DeleteSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	delete(s.messages, sessionID)
	return nil
}

func (s *memStore) LoadSessions(_ context.Context) ([]Phytomer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Phytomer, 0, len(s.sessions))
	for _, p := range s.sessions {
		out = append(out, p)
	}
	return out, nil
}

func (s *memStore) LoadSession(_ context.Context, sessionID string) (Phytomer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.sessions[sessionID]
	return p, ok, nil
}

func (s *memStore) AppendMessage(_ context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[m.SessionID] = append(s.messages[m.SessionID], m)
	return nil
}

func (s *memStore) LoadMessages(_ context.Context, sessionID string, limit int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.messages[sessionID]
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return append([]Message(nil), msgs...), nil
}

func TestIdleEvictionRoundTripResumesPreferences(t *testing.T) {
	store := newMemStore()
	m, err := NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	created, err := m.Initiate(context.Background(), OriginREST, Preferences{
		Provider: "anthropic",
		Model:    "claude-fable-5",
		Genotype: "go-dev",
		Extras:   map[string]string{"tone": "terse"},
	})
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// Force the entry out of the in-memory cache without touching the store.
	if n := m.evictIdle(0); n != 1 {
		t.Fatalf("evictIdle removed %d entries, want 1", n)
	}
	if m.memoryLen() != 0 {
		t.Fatalf("memory still holds %d sessions after eviction", m.memoryLen())
	}
	// Store still has the durable record.
	if _, ok, err := store.LoadSession(context.Background(), created.ID); err != nil || !ok {
		t.Fatalf("store lost session after memory eviction: ok=%v err=%v", ok, err)
	}

	// Get must rehydrate the full Phytomer — not mint a blank stand-in.
	got, ok := m.Get(context.Background(), created.ID)
	if !ok {
		t.Fatal("Get after eviction: session not found")
	}
	if got.ID != created.ID || got.Origin != created.Origin {
		t.Fatalf("rehydrated identity mismatch: %+v", got)
	}
	if got.Preferences.Provider != "anthropic" ||
		got.Preferences.Model != "claude-fable-5" ||
		got.Preferences.Genotype != "go-dev" ||
		got.Preferences.Extras["tone"] != "terse" {
		t.Fatalf("preferences did not round-trip after eviction: %+v", got.Preferences)
	}
	if m.memoryLen() != 1 {
		t.Fatalf("Get should have rehydrated into memory, len=%d", m.memoryLen())
	}

	// GetOrInitiate on a second forced eviction must also resume, not blank-mint.
	if m.evictIdle(0) != 1 {
		t.Fatal("second eviction failed")
	}
	resumed, err := m.GetOrInitiate(context.Background(), created.ID, OriginCLI)
	if err != nil {
		t.Fatalf("GetOrInitiate after eviction: %v", err)
	}
	if resumed.Preferences.Model != "claude-fable-5" || resumed.Origin != OriginREST {
		t.Fatalf("GetOrInitiate minted a blank session instead of resuming: %+v", resumed)
	}
}

func TestListSourcesFromStoreAfterEviction(t *testing.T) {
	store := newMemStore()
	m, err := NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	a, err := m.Initiate(context.Background(), OriginREST, Preferences{Model: "a"})
	if err != nil {
		t.Fatalf("Initiate a: %v", err)
	}
	b, err := m.Initiate(context.Background(), OriginCLI, Preferences{Model: "b"})
	if err != nil {
		t.Fatalf("Initiate b: %v", err)
	}

	if m.evictIdle(0) != 2 {
		t.Fatal("expected both sessions evicted from memory")
	}
	if m.memoryLen() != 0 {
		t.Fatal("memory cache should be empty after full eviction")
	}

	listed, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("List returned %d sessions, want 2 (must source from store, not cache)", len(listed))
	}
	ids := map[string]bool{}
	for _, s := range listed {
		ids[s.ID] = true
	}
	if !ids[a.ID] || !ids[b.ID] {
		t.Fatalf("List missing sessions after eviction: %+v", listed)
	}
}

func TestNoStoreManagerNeverEvicts(t *testing.T) {
	m, err := NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	s, err := m.Initiate(context.Background(), OriginCLI, Preferences{Model: "keep-me"})
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// Age the entry far past any reasonable TTL; sweep must still be a no-op.
	m.mu.Lock()
	m.sessions[s.ID].session.LastActiveAt = time.Now().UTC().Add(-100 * 24 * time.Hour)
	m.mu.Unlock()

	if n := m.evictIdle(time.Hour); n != 0 {
		t.Fatalf("no-store Manager evicted %d sessions; sweep must be a pure no-op", n)
	}
	if m.memoryLen() != 1 {
		t.Fatal("no-store session disappeared without eviction path")
	}
	// StartIdleEviction must also refuse to schedule when store is nil.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.StartIdleEviction(ctx) // must return immediately, no panic
	if _, ok := m.Get(context.Background(), s.ID); !ok {
		t.Fatal("session lost under no-store Manager")
	}
}

func TestSessionTTLFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(EnvSessionTTL, "")
		if got := sessionTTLFromEnv(); got != defaultSessionTTL {
			t.Fatalf("got %v, want default %v", got, defaultSessionTTL)
		}
	})
	t.Run("valid duration respected", func(t *testing.T) {
		t.Setenv(EnvSessionTTL, "90m")
		if got := sessionTTLFromEnv(); got != 90*time.Minute {
			t.Fatalf("got %v, want 90m", got)
		}
	})
	t.Run("invalid falls back with warning", func(t *testing.T) {
		t.Setenv(EnvSessionTTL, "not-a-duration")
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		if got := sessionTTLFromEnv(); got != defaultSessionTTL {
			t.Fatalf("got %v, want default %v", got, defaultSessionTTL)
		}
		if !strings.Contains(buf.String(), EnvSessionTTL) {
			t.Fatalf("expected warning mentioning %s, log=%q", EnvSessionTTL, buf.String())
		}
	})
	t.Run("non-positive falls back with warning", func(t *testing.T) {
		t.Setenv(EnvSessionTTL, "0s")
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		if got := sessionTTLFromEnv(); got != defaultSessionTTL {
			t.Fatalf("got %v, want default %v", got, defaultSessionTTL)
		}
		if !strings.Contains(buf.String(), "non-positive") {
			t.Fatalf("expected non-positive warning, log=%q", buf.String())
		}
	})
}

func TestPruneStillDeletesMemoryAndStore(t *testing.T) {
	store := newMemStore()
	m, err := NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	s, err := m.Initiate(context.Background(), OriginREST, Preferences{Model: "doomed"})
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	if err := m.Prune(context.Background(), s.ID); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if m.memoryLen() != 0 {
		t.Fatal("Prune left session in memory")
	}
	if _, ok, err := store.LoadSession(context.Background(), s.ID); err != nil || ok {
		t.Fatalf("Prune left session in store: ok=%v err=%v", ok, err)
	}
	if _, ok := m.Get(context.Background(), s.ID); ok {
		t.Fatal("Get found pruned session")
	}
}

func TestEvictIdleRespectsTTL(t *testing.T) {
	store := newMemStore()
	m, err := NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	idle, err := m.Initiate(context.Background(), OriginREST, Preferences{})
	if err != nil {
		t.Fatalf("Initiate idle: %v", err)
	}
	active, err := m.Initiate(context.Background(), OriginCLI, Preferences{})
	if err != nil {
		t.Fatalf("Initiate active: %v", err)
	}

	m.mu.Lock()
	m.sessions[idle.ID].session.LastActiveAt = time.Now().UTC().Add(-2 * time.Hour)
	// active stays at "now"
	m.mu.Unlock()

	if n := m.evictIdle(time.Hour); n != 1 {
		t.Fatalf("evictIdle removed %d, want only the idle one", n)
	}
	if _, ok := m.sessions[idle.ID]; ok {
		t.Fatal("idle session still in memory")
	}
	if _, ok := m.sessions[active.ID]; !ok {
		t.Fatal("active session was incorrectly evicted")
	}
	// Idle session must still be durable.
	if _, ok, err := store.LoadSession(context.Background(), idle.ID); err != nil || !ok {
		t.Fatalf("eviction deleted from store: ok=%v err=%v", ok, err)
	}
}
