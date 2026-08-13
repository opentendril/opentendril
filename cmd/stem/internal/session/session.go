// Package session implements the unified session manager for OpenTendril.
//
// A Phytomer is the canonical name for one logical interaction thread — what
// the external surfaces present as a "session" — bound to a unique session ID.
// Every interface surface — the interactive CLI chat, the MCP stdio/HTTP
// server, the OpenAPI REST endpoints, and the WebSocket gateway — resolves its
// traffic through the same Manager, so concurrent conversations coexist without
// trampling each other's state and each Phytomer carries its own preferences
// (LLM provider/model overrides, Genotype, Epigenetic Genome).
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// IDPrefix marks every Phytomer session identifier.
	IDPrefix = "tendril-"

	// memoryHistoryCap bounds the in-memory per-session message buffer used
	// when no persistent store is attached (headless / DB logging disabled).
	memoryHistoryCap = 200

	// maxSessionIDCollisionRetries is the upper bound on how many times
	// Initiate will regenerate a candidate ID before giving up. A real
	// collision after even one retry against 96 bits of entropy would
	// indicate a broken RNG, so failing closed is correct.
	maxSessionIDCollisionRetries = 5

	// EnvSessionTTL configures how long a session may sit idle in the
	// in-memory cache before the idle-eviction sweep drops it. Only applies
	// when a Store is attached; no-store Managers never evict. Parsed with
	// time.ParseDuration (e.g. "24h", "90m"). Defaults to 24h; an invalid
	// value falls back to the default with a logged warning.
	EnvSessionTTL = "TENDRIL_SESSION_TTL"

	// defaultSessionTTL is used when TENDRIL_SESSION_TTL is unset or invalid.
	defaultSessionTTL = 24 * time.Hour

	// idleEvictionInterval is how often the daemon sweeps for idle entries.
	// Cheap enough not to matter under lock contention; long enough that a
	// 24h-default TTL is not checked more often than useful.
	idleEvictionInterval = 5 * time.Minute
)

// Known interaction origins. Origins outside this set are preserved verbatim.
const (
	OriginCLI  = "cli"
	OriginMCP  = "mcp"
	OriginREST = "rest"
	OriginWS   = "ws"
)

var validIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Preferences hold per-Phytomer overrides that shape how sprouts execute for
// this session only. Zero values mean "inherit the global default".
type Preferences struct {
	Provider         string            `json:"provider,omitempty"`
	Model            string            `json:"model,omitempty"`
	Genotype         string            `json:"genotype,omitempty"`
	EpigeneticGenome string            `json:"epigeneticGenome,omitempty"`
	Extras           map[string]string `json:"extras,omitempty"`
}

// Merge layers overrides on top of the receiver, returning the result.
func (p Preferences) Merge(overrides Preferences) Preferences {
	merged := p
	if strings.TrimSpace(overrides.Provider) != "" {
		merged.Provider = overrides.Provider
	}
	if strings.TrimSpace(overrides.Model) != "" {
		merged.Model = overrides.Model
	}
	if strings.TrimSpace(overrides.Genotype) != "" {
		merged.Genotype = overrides.Genotype
	}
	if strings.TrimSpace(overrides.EpigeneticGenome) != "" {
		merged.EpigeneticGenome = overrides.EpigeneticGenome
	}
	if len(overrides.Extras) > 0 {
		if merged.Extras == nil {
			merged.Extras = make(map[string]string, len(overrides.Extras))
		} else {
			// Size from the existing map only; adding both lengths can overflow
			// an int before the map grows naturally for the override keys.
			copied := make(map[string]string, len(merged.Extras))
			for key, value := range merged.Extras {
				copied[key] = value
			}
			merged.Extras = copied
		}
		for key, value := range overrides.Extras {
			merged.Extras[key] = value
		}
	}
	return merged
}

// Phytomer is one stateful interaction thread (surfaced externally as a session).
type Phytomer struct {
	ID           string      `json:"sessionId"`
	Origin       string      `json:"origin"`
	CreatedAt    time.Time   `json:"createdAt"`
	LastActiveAt time.Time   `json:"lastActiveAt"`
	Preferences  Preferences `json:"preferences"`
}

// Message is one unified chat-log entry bound to a Phytomer.
type Message struct {
	SessionID string    `json:"sessionId"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store persists Phytomer sessions and their unified chat logs. The SQLite
// history database implements this interface; a nil Store keeps the Manager
// fully in-memory for high-performance headless runs.
type Store interface {
	SaveSession(ctx context.Context, s Phytomer) error
	DeleteSession(ctx context.Context, sessionID string) error
	LoadSessions(ctx context.Context) ([]Phytomer, error)
	// LoadSession loads one persisted session by ID. The second return value is
	// false if no such session exists.
	LoadSession(ctx context.Context, sessionID string) (Phytomer, bool, error)
	AppendMessage(ctx context.Context, m Message) error
	LoadMessages(ctx context.Context, sessionID string, limit int) ([]Message, error)
}

type sessionState struct {
	session  Phytomer
	messages []Message
}

// Manager coordinates live Phytomer sessions across the CLI, MCP, REST, and
// WebSocket surfaces. With a Store attached, m.sessions is a bounded idle-TTL
// cache over the durable record (lazy-rehydrated on Get/GetOrInitiate); without
// a store it remains the sole in-memory authority and never evicts.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*sessionState
	store    Store
}

// NewManager creates a Manager, resuming previously persisted sessions when a
// store is attached so the future UI never loses state across restarts.
func NewManager(ctx context.Context, store Store) (*Manager, error) {
	m := &Manager{
		sessions: make(map[string]*sessionState),
		store:    store,
	}

	if store != nil {
		persisted, err := store.LoadSessions(ctx)
		if err != nil {
			return nil, fmt.Errorf("resume persisted sessions: %w", err)
		}
		for _, s := range persisted {
			m.sessions[s.ID] = &sessionState{session: s}
		}
	}

	return m, nil
}

// NewID mints a unique Phytomer session identifier.
func NewID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s%d", IDPrefix, time.Now().UTC().UnixNano())
	}
	return IDPrefix + hex.EncodeToString(buf)
}

// newSessionID is a package-var seam so tests can force a deterministic
// ID collision without depending on crypto/rand's astronomically low
// real-world collision odds.
var newSessionID = NewID

// ValidID reports whether an externally supplied session ID is acceptable.
func ValidID(id string) bool {
	return validIDPattern.MatchString(id)
}

// Initiate creates a new Phytomer session.
func (m *Manager) Initiate(ctx context.Context, origin string, prefs Preferences) (Phytomer, error) {
	if m == nil {
		return Phytomer{}, fmt.Errorf("session manager is nil")
	}

	origin = normalizeOrigin(origin)
	now := time.Now().UTC()

	m.mu.Lock()
	id := newSessionID()
	for attempt := 0; ; attempt++ {
		if _, exists := m.sessions[id]; !exists {
			break
		}
		if attempt >= maxSessionIDCollisionRetries {
			m.mu.Unlock()
			return Phytomer{}, fmt.Errorf("session id generation collided %d times consecutively", attempt+1)
		}
		id = newSessionID()
	}
	s := Phytomer{
		ID:           id,
		Origin:       origin,
		CreatedAt:    now,
		LastActiveAt: now,
		Preferences:  prefs,
	}
	m.sessions[id] = &sessionState{session: s}
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.SaveSession(ctx, s); err != nil {
			return s, fmt.Errorf("persist session %s: %w", s.ID, err)
		}
	}
	return s, nil
}

// Get returns a snapshot of the session with the given ID. When a store is
// attached and the ID is absent from the in-memory cache, Get falls back to
// LoadSession and rehydrates the cache on a hit so idle eviction is transparent.
func (m *Manager) Get(ctx context.Context, id string) (Phytomer, bool) {
	if m == nil {
		return Phytomer{}, false
	}

	m.mu.RLock()
	state, ok := m.sessions[id]
	if ok {
		s := state.session
		m.mu.RUnlock()
		return s, true
	}
	m.mu.RUnlock()

	return m.rehydrateFromStore(ctx, id)
}

// rehydrateFromStore loads one session from the durable store into m.sessions.
// Callers must already have observed a cache miss under their own locking.
func (m *Manager) rehydrateFromStore(ctx context.Context, id string) (Phytomer, bool) {
	if m.store == nil {
		return Phytomer{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	loaded, found, err := m.store.LoadSession(ctx, id)
	if err != nil {
		log.Printf("⚠️ load session %s from store: %v", id, err)
		return Phytomer{}, false
	}
	if !found {
		return Phytomer{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Another goroutine may have won the rehydrate race; prefer the live entry.
	if existing, ok := m.sessions[id]; ok {
		return existing.session, true
	}
	m.sessions[id] = &sessionState{session: loaded}
	return loaded, true
}

// GetOrInitiate resolves an existing session or creates one. An empty ID always
// initiates a fresh Phytomer; a well-formed unknown ID is adopted so clients can
// mint IDs offline (e.g. the CLI when the server rotates underneath it). When a
// store is attached, a cache miss first consults the durable record so an
// idle-evicted session resumes instead of being silently replaced by a blank one.
func (m *Manager) GetOrInitiate(ctx context.Context, id, origin string) (Phytomer, error) {
	if m == nil {
		return Phytomer{}, fmt.Errorf("session manager is nil")
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return m.Initiate(ctx, origin, Preferences{})
	}
	if !ValidID(id) {
		return Phytomer{}, fmt.Errorf("invalid session id %q", id)
	}

	if s, ok := m.Get(ctx, id); ok {
		return s, nil
	}

	origin = normalizeOrigin(origin)
	now := time.Now().UTC()
	s := Phytomer{
		ID:           id,
		Origin:       origin,
		CreatedAt:    now,
		LastActiveAt: now,
	}

	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		s = existing.session
		m.mu.Unlock()
		return s, nil
	}
	m.sessions[id] = &sessionState{session: s}
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.SaveSession(ctx, s); err != nil {
			return s, fmt.Errorf("persist session %s: %w", s.ID, err)
		}
	}
	return s, nil
}

// List returns all sessions, most recently active first. When a store is
// attached it sources from the durable record (the complete set once the
// in-memory map is a bounded cache); without a store it iterates m.sessions.
func (m *Manager) List(ctx context.Context) ([]Phytomer, error) {
	if m == nil {
		return nil, nil
	}

	if m.store != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		sessions, err := m.store.LoadSessions(ctx)
		if err != nil {
			return nil, fmt.Errorf("list persisted sessions: %w", err)
		}
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].LastActiveAt.After(sessions[j].LastActiveAt)
		})
		return sessions, nil
	}

	m.mu.RLock()
	sessions := make([]Phytomer, 0, len(m.sessions))
	for _, state := range m.sessions {
		sessions = append(sessions, state.session)
	}
	m.mu.RUnlock()

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActiveAt.After(sessions[j].LastActiveAt)
	})
	return sessions, nil
}

// StartIdleEviction launches a background sweep that drops idle entries from
// the in-memory cache. It is a no-op when no store is attached — eviction
// without a durable reload path would be pure data loss. Only the long-lived
// daemon (cmdserve) should call this; short-lived CLI/MCP entry points exit
// long before a default 24h TTL would fire. The sweep never calls
// store.DeleteSession — that remains Prune's job.
func (m *Manager) StartIdleEviction(ctx context.Context) {
	if m == nil || m.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ttl := sessionTTLFromEnv()

	go func() {
		ticker := time.NewTicker(idleEvictionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.evictIdle(ttl)
			}
		}
	}()
}

// evictIdle removes in-memory sessions whose LastActiveAt is at least ttl old.
// It never touches the store. A non-positive ttl evicts every cached entry
// (useful for tests that force a cache miss). Returns the number removed.
// No-op when store is nil so ephemeral no-store Managers never lose state.
func (m *Manager) evictIdle(ttl time.Duration) int {
	if m == nil || m.store == nil {
		return 0
	}

	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()

	evicted := 0
	for id, state := range m.sessions {
		if ttl <= 0 || !state.session.LastActiveAt.After(now.Add(-ttl)) {
			delete(m.sessions, id)
			evicted++
		}
	}
	return evicted
}

// sessionTTLFromEnv reads TENDRIL_SESSION_TTL. Empty/invalid values fall back
// to defaultSessionTTL with a warning — a hygiene knob must not gate startup.
func sessionTTLFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvSessionTTL))
	if raw == "" {
		return defaultSessionTTL
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("⚠️ invalid %s=%q (want a Go duration like \"24h\"); using default %s: %v",
			EnvSessionTTL, raw, defaultSessionTTL, err)
		return defaultSessionTTL
	}
	if parsed <= 0 {
		log.Printf("⚠️ non-positive %s=%q; using default %s", EnvSessionTTL, raw, defaultSessionTTL)
		return defaultSessionTTL
	}
	return parsed
}

// memoryLen reports how many sessions currently sit in the in-memory cache.
// Intended for tests asserting eviction; not part of the public surface.
func (m *Manager) memoryLen() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// UpdatePreferences merges preference overrides into a session.
func (m *Manager) UpdatePreferences(ctx context.Context, id string, overrides Preferences) (Phytomer, error) {
	if m == nil {
		return Phytomer{}, fmt.Errorf("session manager is nil")
	}

	m.mu.Lock()
	state, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return Phytomer{}, fmt.Errorf("session %s not found", id)
	}
	state.session.Preferences = state.session.Preferences.Merge(overrides)
	state.session.LastActiveAt = time.Now().UTC()
	updated := state.session
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.SaveSession(ctx, updated); err != nil {
			return updated, fmt.Errorf("persist session %s: %w", id, err)
		}
	}
	return updated, nil
}

// Touch refreshes a session's activity timestamp.
func (m *Manager) Touch(ctx context.Context, id string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	state, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	state.session.LastActiveAt = time.Now().UTC()
	touched := state.session
	m.mu.Unlock()

	if m.store != nil {
		_ = m.store.SaveSession(ctx, touched)
	}
}

// RecordMessage appends a message to a session's unified chat log, buffering
// in memory and persisting when a store is attached.
func (m *Manager) RecordMessage(ctx context.Context, msg Message) error {
	if m == nil {
		return fmt.Errorf("session manager is nil")
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}

	m.mu.Lock()
	state, ok := m.sessions[msg.SessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", msg.SessionID)
	}
	state.session.LastActiveAt = msg.CreatedAt
	state.messages = append(state.messages, msg)
	if len(state.messages) > memoryHistoryCap {
		copy(state.messages, state.messages[len(state.messages)-memoryHistoryCap:])
		state.messages = state.messages[:memoryHistoryCap]
	}
	touched := state.session
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.AppendMessage(ctx, msg); err != nil {
			return fmt.Errorf("persist message for session %s: %w", msg.SessionID, err)
		}
		if err := m.store.SaveSession(ctx, touched); err != nil {
			return fmt.Errorf("persist session %s: %w", msg.SessionID, err)
		}
	}
	return nil
}

// History returns a session's most recent messages in chronological order,
// preferring the persistent store and falling back to the memory buffer.
func (m *Manager) History(ctx context.Context, id string, limit int) ([]Message, error) {
	if m == nil {
		return nil, fmt.Errorf("session manager is nil")
	}
	if limit <= 0 {
		limit = 50
	}

	if m.store != nil {
		return m.store.LoadMessages(ctx, id, limit)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	messages := state.messages
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return append([]Message(nil), messages...), nil
}

// Prune removes a session and its persisted state.
func (m *Manager) Prune(ctx context.Context, id string) error {
	if m == nil {
		return fmt.Errorf("session manager is nil")
	}

	m.mu.Lock()
	_, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("session %s not found", id)
	}
	if m.store != nil {
		if err := m.store.DeleteSession(ctx, id); err != nil {
			return fmt.Errorf("delete persisted session %s: %w", id, err)
		}
	}
	return nil
}

func normalizeOrigin(origin string) string {
	origin = strings.ToLower(strings.TrimSpace(origin))
	if origin == "" {
		return OriginREST
	}
	return origin
}
