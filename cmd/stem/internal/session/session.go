// Package session implements the unified SessionManager for the OS of OT.
//
// A Session is a "Tendril": one logical interaction thread bound to a unique
// session ID. Every interface surface — the interactive CLI chat, the MCP
// stdio/HTTP server, the OpenAPI REST endpoints, and the WebSocket gateway —
// resolves its traffic through the same Manager, so concurrent conversations
// coexist without trampling each other's state and each Tendril carries its
// own preferences (LLM provider/model overrides, Genotype, Epigenetic Genome).
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// IDPrefix marks every Tendril session identifier.
	IDPrefix = "tendril-"

	// memoryHistoryCap bounds the in-memory per-session message buffer used
	// when no persistent store is attached (headless / DB logging disabled).
	memoryHistoryCap = 200
)

// Known interaction origins. Origins outside this set are preserved verbatim.
const (
	OriginCLI  = "cli"
	OriginMCP  = "mcp"
	OriginREST = "rest"
	OriginWS   = "ws"
)

var validIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Preferences hold per-Tendril overrides that shape how sprouts execute for
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
			copied := make(map[string]string, len(merged.Extras)+len(overrides.Extras))
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

// Session is one Tendril: a stateful interaction thread.
type Session struct {
	ID           string      `json:"sessionId"`
	Origin       string      `json:"origin"`
	CreatedAt    time.Time   `json:"createdAt"`
	LastActiveAt time.Time   `json:"lastActiveAt"`
	Preferences  Preferences `json:"preferences"`
}

// Message is one unified chat-log entry bound to a Tendril.
type Message struct {
	SessionID string    `json:"sessionId"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store persists Tendril sessions and their unified chat logs. The SQLite
// history database implements this interface; a nil Store keeps the Manager
// fully in-memory for high-performance headless runs.
type Store interface {
	SaveSession(ctx context.Context, s Session) error
	DeleteSession(ctx context.Context, sessionID string) error
	LoadSessions(ctx context.Context) ([]Session, error)
	AppendMessage(ctx context.Context, m Message) error
	LoadMessages(ctx context.Context, sessionID string, limit int) ([]Message, error)
}

type sessionState struct {
	session  Session
	messages []Message
}

// Manager is the single source of truth for live Tendril sessions across the
// CLI, MCP, REST, and WebSocket surfaces.
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

// NewID mints a unique Tendril session identifier.
func NewID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s%d", IDPrefix, time.Now().UTC().UnixNano())
	}
	return IDPrefix + hex.EncodeToString(buf)
}

// ValidID reports whether an externally supplied session ID is acceptable.
func ValidID(id string) bool {
	return validIDPattern.MatchString(id)
}

// Sprout creates a new Tendril session.
func (m *Manager) Sprout(ctx context.Context, origin string, prefs Preferences) (Session, error) {
	if m == nil {
		return Session{}, fmt.Errorf("session manager is nil")
	}

	origin = normalizeOrigin(origin)
	now := time.Now().UTC()
	s := Session{
		ID:           NewID(),
		Origin:       origin,
		CreatedAt:    now,
		LastActiveAt: now,
		Preferences:  prefs,
	}

	m.mu.Lock()
	m.sessions[s.ID] = &sessionState{session: s}
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.SaveSession(ctx, s); err != nil {
			return s, fmt.Errorf("persist session %s: %w", s.ID, err)
		}
	}
	return s, nil
}

// Get returns a snapshot of the session with the given ID.
func (m *Manager) Get(id string) (Session, bool) {
	if m == nil {
		return Session{}, false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.sessions[id]
	if !ok {
		return Session{}, false
	}
	return state.session, true
}

// GetOrSprout resolves an existing session or creates one. An empty ID always
// sprouts a fresh Tendril; a well-formed unknown ID is adopted so clients can
// mint IDs offline (e.g. the CLI when the server rotates underneath it).
func (m *Manager) GetOrSprout(ctx context.Context, id, origin string) (Session, error) {
	if m == nil {
		return Session{}, fmt.Errorf("session manager is nil")
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return m.Sprout(ctx, origin, Preferences{})
	}
	if !ValidID(id) {
		return Session{}, fmt.Errorf("invalid session id %q", id)
	}

	if s, ok := m.Get(id); ok {
		return s, nil
	}

	origin = normalizeOrigin(origin)
	now := time.Now().UTC()
	s := Session{
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

// List returns all live sessions, most recently active first.
func (m *Manager) List() []Session {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	sessions := make([]Session, 0, len(m.sessions))
	for _, state := range m.sessions {
		sessions = append(sessions, state.session)
	}
	m.mu.RUnlock()

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActiveAt.After(sessions[j].LastActiveAt)
	})
	return sessions
}

// UpdatePreferences merges preference overrides into a session.
func (m *Manager) UpdatePreferences(ctx context.Context, id string, overrides Preferences) (Session, error) {
	if m == nil {
		return Session{}, fmt.Errorf("session manager is nil")
	}

	m.mu.Lock()
	state, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return Session{}, fmt.Errorf("session %s not found", id)
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
