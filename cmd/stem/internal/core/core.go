// Package core is the single, transport-free service that owns the shared
// command capabilities of the OpenTendril Stem. The CLI, MCP, and REST/OpenAPI
// surfaces are thin *adapters* that translate their transport to and from this
// package and MUST NOT contain business logic (see AGENTS.md, "Adapters
// translate only"). The litmus test for a capability living here: it must be
// invokable with zero HTTP, CLI, or MCP types in scope.
//
// This began as the first slice of Interface Parity. It governs
// the session-lifecycle capability family, the genome family (// slice 1), the plasmid family, the substrate-grafting
// family, and the sequence family —
// all three surfaces project each identically through this one Core. The remaining
// Stem capabilities (sprout/run) are NOT yet part of the governed registry — they
// are tracked in and the parity tests deliberately assert only the
// governed set so the three surfaces cannot silently diverge on it. plasmid.sign
// and mesh key-management commands stay deliberately ungoverned (see plasmid.go
// and mesh.go).
// slice 1), and the sprout/run family — all three
// surfaces project each identically through this one Core. The remaining
// Stem capabilities (sequence, plasmid, substrate grafting) are NOT yet part
// of the governed registry — they are tracked in and the parity
// tests deliberately assert only the governed set so the three surfaces
// cannot silently diverge on it.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// ErrNotFound is the transport-neutral sentinel returned when a capability
// targets a session that does not exist. Adapters map it to their own
// not-found signal (HTTP 404, an MCP error, a CLI message).
var ErrNotFound = errors.New("session not found")

// Core is the capability service every interface adapter routes through. Every
// method is expressible with plain domain types only — no net/http, no MCP,
// no CLI types appear in any signature.
type Core interface {
	CreateSession(ctx context.Context, in CreateSessionInput) (session.Phytomer, error)
	ListSessions(ctx context.Context) ([]session.Phytomer, error)
	GetSession(ctx context.Context, in GetSessionInput) (session.Phytomer, error)
	UpdateSessionPreferences(ctx context.Context, in UpdateSessionInput) (session.Phytomer, error)
	DeleteSession(ctx context.Context, in DeleteSessionInput) error
	SessionHistory(ctx context.Context, in SessionHistoryInput) ([]session.Message, error)

	// Genome family. Reading is pure filesystem work;
	// reduce/evolve run through the injected GenomeOperations execution port.
	GenomeView(ctx context.Context) ([]GenomeSeed, error)
	GenomeReduce(ctx context.Context) (string, error)
	GenomeEvolve(ctx context.Context) (string, error)

	// Plasmid family. Listing is pure filesystem work;
	// injection runs through the injected PlasmidOperations execution port.
	PlasmidList(ctx context.Context) ([]string, error)
	PlasmidInject(ctx context.Context, in PlasmidInjectInput) (PlasmidInjection, error)

	// Substrate-grafting family. Both operations run
	// through the injected MeshOperations execution port.
	MeshGraft(ctx context.Context, in MeshGraftInput) (MeshDelegation, error)
	MeshPromote(ctx context.Context, in MeshPromoteInput) (MeshPromotion, error)
	// Mesh trait governance family. Listing and moderation run
	// through the injected MeshOperations execution port.
	MeshTraitList(ctx context.Context, in MeshTraitListInput) (MeshTraitListOutput, error)
	MeshTraitAccept(ctx context.Context, in MeshTraitAcceptInput) (MeshTraitAcceptOutput, error)
	MeshTraitReject(ctx context.Context, in MeshTraitRejectInput) (MeshTraitRejectOutput, error)

	// Sequence family. Both operations run through the
	// injected SequenceOperations execution port.
	SequenceList(ctx context.Context) ([]string, error)
	SequenceRun(ctx context.Context, in SequenceRunInput) (SequenceRunResult, error)
	// Sprout/run family. Runs through the injected
	// SproutOperations execution port.
	SproutRun(ctx context.Context, in SproutRunInput) (SproutRunResult, error)
	// Passthrough family: one bounded command in a sealed Terrarium, the
	// minimal delegable operation-class. Runs through the injected
	// PassthroughOperations execution port.
	PassthroughRun(ctx context.Context, in PassthroughRunInput) (PassthroughRunResult, error)
	// Git family: commit a substrate's workspace under its configured commit
	// identity, the lowest rung of the delegated-execution ladder. Runs
	// through the injected GitOperations execution port.
	GitCommit(ctx context.Context, in GitCommitInput) (GitCommitResult, error)

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
// surface initiated it; empty defers to the session manager's default.
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

// MeshTraitListInput asks for the current pending foreign traits.
type MeshTraitListInput struct{}

// MeshTraitListOutput returns the pending foreign traits.
type MeshTraitListOutput struct {
	Traits []any `json:"traits"`
}

// MeshTraitAcceptInput identifies a pending trait to accept.
type MeshTraitAcceptInput struct {
	TraitID string `json:"traitId"`
}

// MeshTraitAcceptOutput reports the accepted trait identifier.
type MeshTraitAcceptOutput struct {
	TraitID string `json:"traitId"`
	Status  string `json:"status"`
}

// MeshTraitRejectInput identifies a pending trait to reject.
type MeshTraitRejectInput struct {
	TraitID string `json:"traitId"`
}

// MeshTraitRejectOutput reports the rejected trait identifier.
type MeshTraitRejectOutput struct {
	TraitID string `json:"traitId"`
	Status  string `json:"status"`
}

// --- service implementation -------------------------------------------------

// only place session-command business logic lives. The genome, plasmid, mesh,
// sequence, and sprout fields are the injected execution ports for their capability families
// (see genome.go, plasmid.go, mesh.go, sequence.go, and sprout.go).
type Service struct {
	sessions    *session.Manager
	genome      GenomeOperations
	plasmid     PlasmidOperations
	mesh        MeshOperations
	sequence    SequenceOperations
	sprout      SproutOperations
	passthrough PassthroughOperations
	git         GitOperations
}

// NewService builds a Core over the shared SessionManager.
func NewService(sessions *session.Manager) *Service {
	return &Service{sessions: sessions}
}

var _ Core = (*Service)(nil)

func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (session.Phytomer, error) {
	return s.sessions.Initiate(ctx, in.Origin, in.Preferences)
}

func (s *Service) ListSessions(_ context.Context) ([]session.Phytomer, error) {
	return s.sessions.List(), nil
}

func (s *Service) GetSession(_ context.Context, in GetSessionInput) (session.Phytomer, error) {
	sess, ok := s.sessions.Get(in.SessionID)
	if !ok {
		return session.Phytomer{}, ErrNotFound
	}
	return sess, nil
}

func (s *Service) UpdateSessionPreferences(ctx context.Context, in UpdateSessionInput) (session.Phytomer, error) {
	sess, err := s.sessions.UpdatePreferences(ctx, in.SessionID, in.Preferences)
	if err != nil {
		return session.Phytomer{}, mapManagerErr(err)
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
