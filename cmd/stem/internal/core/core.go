// Package core is the single, transport-free service that owns the shared
// command capabilities of the OpenTendril Stem. The CLI, MCP, and REST/OpenAPI
// surfaces are thin *adapters* that translate their transport to and from this
// package and MUST NOT contain business logic (see AGENTS.md, "Adapters
// translate only"). The litmus test for a capability living here: it must be
// invokable with zero HTTP, CLI, or MCP types in scope.
//
// This began as the first slice of Interface Parity (issue #159). It governs
// the session-lifecycle capability family, the genome family (issue #181
// slice 1), and the plasmid family (issue #181 slice 2) — all three surfaces
// project each identically through this one Core. The remaining Stem
// capabilities (sprout/run, sequence, substrate grafting) are NOT yet part of
// the governed registry — they are tracked in issue #181, and the parity
// tests deliberately assert only the governed set so the three surfaces
// cannot silently diverge on it. plasmid.sign stays deliberately ungoverned
// (see plasmid.go).
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/session"
)

// ErrNotFound is the transport-neutral sentinel returned when a capability
// targets a session that does not exist. Adapters map it to their own
// not-found signal (HTTP 404, an MCP error, a CLI message).
var ErrNotFound = errors.New("session not found")

// Core is the capability service every interface adapter routes through. Every
// method is expressible with plain domain types only — no net/http, no MCP,
// no CLI types appear in any signature.
type Core interface {
	CreateSession(ctx context.Context, in CreateSessionInput) (session.Session, error)
	ListSessions(ctx context.Context) ([]session.Session, error)
	GetSession(ctx context.Context, in GetSessionInput) (session.Session, error)
	UpdateSessionPreferences(ctx context.Context, in UpdateSessionInput) (session.Session, error)
	DeleteSession(ctx context.Context, in DeleteSessionInput) error
	SessionHistory(ctx context.Context, in SessionHistoryInput) ([]session.Message, error)

	// Genome family (issue #181, slice 1). Reading is pure filesystem work;
	// reduce/evolve run through the injected GenomeOps execution port.
	GenomeView(ctx context.Context) ([]GenomeSeed, error)
	GenomeReduce(ctx context.Context) (string, error)
	GenomeEvolve(ctx context.Context) (string, error)

	// Plasmid family (issue #181, slice 2). Listing is pure filesystem work;
	// injection runs through the injected PlasmidOps execution port.
	PlasmidList(ctx context.Context) ([]string, error)
	PlasmidInject(ctx context.Context, in PlasmidInjectInput) (PlasmidInjection, error)

	// Capabilities returns the declarative registry that every surface
	// projects. Adding an entry here is the single act that makes a capability
	// appear (and be required) on all three surfaces.
	Capabilities() []Capability
	// Invoke runs a capability by name with a decoded input map. This is the
	// uniform projection path used by the MCP and CLI adapters.
	Invoke(ctx context.Context, name string, input map[string]any) (any, error)
}

// --- capability input types (plain domain structs) --------------------------

// CreateSessionInput asks for a new Tendril session. Origin records which
// surface sprouted it; empty defers to the session manager's default.
type CreateSessionInput struct {
	Origin      string              `json:"origin,omitempty"`
	Preferences session.Preferences `json:"preferences,omitempty"`
}

// GetSessionInput identifies a session to fetch.
type GetSessionInput struct {
	SessionID string `json:"sessionId"`
}

// DeleteSessionInput identifies a session to prune.
type DeleteSessionInput struct {
	SessionID string `json:"sessionId"`
}

// UpdateSessionInput layers preference overrides onto an existing session.
type UpdateSessionInput struct {
	SessionID   string              `json:"sessionId"`
	Preferences session.Preferences `json:"preferences"`
}

// SessionHistoryInput asks for a session's recent unified chat log.
type SessionHistoryInput struct {
	SessionID string `json:"sessionId"`
	Limit     int    `json:"limit,omitempty"`
}

// --- service implementation -------------------------------------------------

// Service is the concrete Core, backed by the unified SessionManager. It is the
// only place session-command business logic lives. The genome and plasmid
// fields are the injected execution ports for their capability families (see
// genome.go / WithGenome and plasmid.go / WithPlasmid).
type Service struct {
	sessions *session.Manager
	genome   GenomeOps
	plasmid  PlasmidOps
}

// NewService builds a Core over the shared SessionManager.
func NewService(sessions *session.Manager) *Service {
	return &Service{sessions: sessions}
}

var _ Core = (*Service)(nil)

func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (session.Session, error) {
	return s.sessions.Sprout(ctx, in.Origin, in.Preferences)
}

func (s *Service) ListSessions(_ context.Context) ([]session.Session, error) {
	return s.sessions.List(), nil
}

func (s *Service) GetSession(_ context.Context, in GetSessionInput) (session.Session, error) {
	sess, ok := s.sessions.Get(in.SessionID)
	if !ok {
		return session.Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *Service) UpdateSessionPreferences(ctx context.Context, in UpdateSessionInput) (session.Session, error) {
	sess, err := s.sessions.UpdatePreferences(ctx, in.SessionID, in.Preferences)
	if err != nil {
		return session.Session{}, mapManagerErr(err)
	}
	return sess, nil
}

func (s *Service) DeleteSession(ctx context.Context, in DeleteSessionInput) error {
	if err := s.sessions.Prune(ctx, in.SessionID); err != nil {
		return mapManagerErr(err)
	}
	return nil
}

func (s *Service) SessionHistory(ctx context.Context, in SessionHistoryInput) ([]session.Message, error) {
	// Preserve the surfaces' historic behavior: a missing session is a
	// not-found, distinct from an empty log.
	if _, ok := s.sessions.Get(in.SessionID); !ok {
		return nil, ErrNotFound
	}
	return s.sessions.History(ctx, in.SessionID, in.Limit)
}

// Invoke dispatches by capability name via the declarative registry.
func (s *Service) Invoke(ctx context.Context, name string, input map[string]any) (any, error) {
	for _, capability := range s.Capabilities() {
		if capability.Name == name {
			return capability.Invoke(ctx, input)
		}
	}
	return nil, fmt.Errorf("unknown capability %q", name)
}

// mapManagerErr normalizes the SessionManager's ad-hoc "not found" errors onto
// the transport-neutral sentinel so adapters do not string-match.
func mapManagerErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "not found") {
		return ErrNotFound
	}
	return err
}

// decodeInput copies a generic input map into a typed capability input via a
// JSON round-trip, so the MCP/CLI adapters can hand over untyped argument maps.
func decodeInput(input map[string]any, target any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
