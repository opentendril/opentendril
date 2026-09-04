package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Continuation delivery states. Slice 1 persists a single accepted/pending
// state so later delivery transitions remain forward-compatible. Continuation
// is a Stem-internal lifecycle contract, not a governed Pollinator command.
const (
	ContinuationDeliveryPending = "pending"
)

// Continuation sentinels. Adapters map these; they are not HTTP/MCP types.
var (
	ErrContinuationNotWired = errors.New("phytomer continuation is not wired")
	// ErrContinuationHistoryUnavailable is returned when durable continuation
	// cannot be recorded because no persistence port is wired or history is
	// disabled. Accepted continued intent must not degrade to memory-only state.
	ErrContinuationHistoryUnavailable  = errors.New("phytomer continuation history is not available")
	ErrContinuationTargetNotFound      = errors.New("phytomer continuation target not found")
	ErrContinuationPollenMismatch      = errors.New("phytomer continuation pollen does not match seed ownership")
	ErrContinuationNotEligible         = errors.New("phytomer is not continuation-eligible")
	ErrContinuationTargetChanged       = errors.New("phytomer continuation target ownership changed")
	ErrContinuationIdempotencyConflict = errors.New("phytomer continuation idempotency key was reused with different intent")
	ErrContinuationInvalid             = errors.New("phytomer continuation request is invalid")
)

// ContinuationInput is the transport-free acceptance request. The caller names
// a Phytomer, the continued intent, and a retry identity. Authoritative
// Pollen comes from trusted Core context. Authoritative Substrate is resolved
// from durable Seed ownership — this type has no Substrate field.
type ContinuationInput struct {
	PhytomerID     string
	Intent         string
	IdempotencyKey string
}

// ContinuationAcceptance is the persist-port request Core composes after
// resolving Stem-owned ownership. Pollen, Substrate, and Handle are the
// expected Stem-resolved identity used for atomic revalidation — not
// caller-nominated authority. The stored Substrate is the live Seed row
// after that equality check succeeds.
type ContinuationAcceptance struct {
	PhytomerID     string
	Pollen         string
	Substrate      string
	Handle         string
	IdempotencyKey string
	Intent         string
	IntentDigest   string
}

// ContinuationRecord is one immutable accepted continuation.
type ContinuationRecord struct {
	ContinuationID string
	PhytomerID     string
	Pollen         string
	Substrate      string
	IdempotencyKey string
	IntentDigest   string
	Intent         string
	Sequence       int
	DeliveryState  string
	AcceptedAt     time.Time
	DeliveredAt    time.Time
	FailedAt       time.Time
}

// ContinuationTarget is the Stem-owned ownership resolution of one Phytomer.
// It is suitable for constructing a later DelegationRequest: Pollen and
// Substrate come from durable Seed state, never from caller input.
type ContinuationTarget struct {
	PhytomerID string
	Handle     string
	Pollen     string
	Substrate  string
	Status     string
}

// ToDelegationRequest returns a transport-free delegated invocation targeting
// this Phytomer's Stem-owned Pollen and Substrate. The caller names the
// operation-class; this helper never invents a governed capability.
func (t ContinuationTarget) ToDelegationRequest(operationClass string) DelegationRequest {
	return DelegationRequest{
		Pollen:         strings.TrimSpace(t.Pollen),
		OperationClass: strings.TrimSpace(operationClass),
		Substrate:      strings.TrimSpace(t.Substrate),
	}
}

// ContinuationPersistence is the injected durable continuation port. Core
// owns semantic validation and composition; the port records and queries.
// Core never imports historydb.
type ContinuationPersistence struct {
	ResolveTarget func(ctx context.Context, phytomerID string) (ContinuationTarget, bool, error)
	Accept        func(ctx context.Context, in ContinuationAcceptance) (ContinuationRecord, error)
}

// WithContinuationPersistence wires the durable continuation port.
func (s *Service) WithContinuationPersistence(p ContinuationPersistence) *Service {
	s.continuation = p
	return s
}

// ContinuationIntentDigest is the stable equality proof for continued intent.
// It is a SHA-256 hex digest of the canonical (trimmed) intent bytes and does
// not contain the plaintext.
func ContinuationIntentDigest(intent string) string {
	sum := sha256.Sum256([]byte(intent))
	return hex.EncodeToString(sum[:])
}

// ResolveContinuationTarget loads Stem-owned Seed ownership for a Phytomer
// and fails closed unless the authenticated Pollen matches and the Seed is
// continuation-eligible. It does not consult session preference Substrate.
func (s *Service) ResolveContinuationTarget(ctx context.Context, phytomerID string) (ContinuationTarget, error) {
	if s == nil || s.continuation.ResolveTarget == nil {
		return ContinuationTarget{}, ErrContinuationNotWired
	}
	phytomerID = strings.TrimSpace(phytomerID)
	if phytomerID == "" {
		return ContinuationTarget{}, fmt.Errorf("%w: phytomer id is required", ErrContinuationInvalid)
	}
	target, found, err := s.continuation.ResolveTarget(ctx, phytomerID)
	if err != nil {
		return ContinuationTarget{}, err
	}
	if !found {
		return ContinuationTarget{}, ErrContinuationTargetNotFound
	}
	return s.authorizeContinuationTarget(ctx, target)
}

func (s *Service) authorizeContinuationTarget(ctx context.Context, target ContinuationTarget) (ContinuationTarget, error) {
	target.PhytomerID = strings.TrimSpace(target.PhytomerID)
	target.Handle = strings.TrimSpace(target.Handle)
	target.Pollen = strings.TrimSpace(target.Pollen)
	target.Substrate = strings.TrimSpace(target.Substrate)
	target.Status = strings.TrimSpace(target.Status)
	if target.PhytomerID == "" {
		return ContinuationTarget{}, ErrContinuationTargetNotFound
	}
	pollen := PollenFromContext(ctx)
	if target.Pollen != pollen {
		return ContinuationTarget{}, ErrContinuationPollenMismatch
	}
	if err := continuationEligibilityError(target.Status, target.Substrate); err != nil {
		return ContinuationTarget{}, err
	}
	return target, nil
}

func continuationEligibilityError(status, substrate string) error {
	if strings.TrimSpace(substrate) == "" {
		return ErrContinuationNotEligible
	}
	if strings.TrimSpace(status) != SeedStatusRunning {
		return ErrContinuationNotEligible
	}
	return nil
}

// AcceptContinuation durably records continued intent for an eligible
// Seed-owned Phytomer. It resolves Stem-owned ownership first, then passes
// that expected Pollen/Substrate/Handle into persistence so the atomic
// insert can refuse a TOCTOU ownership change. Idempotency is keyed by
// Phytomer + Pollen + idempotency key; equality uses the intent digest.
func (s *Service) AcceptContinuation(ctx context.Context, in ContinuationInput) (ContinuationRecord, error) {
	if s == nil || s.continuation.Accept == nil || s.continuation.ResolveTarget == nil {
		return ContinuationRecord{}, ErrContinuationNotWired
	}
	phytomerID := strings.TrimSpace(in.PhytomerID)
	intent := strings.TrimSpace(in.Intent)
	key := strings.TrimSpace(in.IdempotencyKey)
	if phytomerID == "" || intent == "" || key == "" {
		return ContinuationRecord{}, fmt.Errorf("%w: phytomer id, intent, and idempotency key are required", ErrContinuationInvalid)
	}
	target, err := s.ResolveContinuationTarget(ctx, phytomerID)
	if err != nil {
		return ContinuationRecord{}, err
	}
	rec, err := s.continuation.Accept(ctx, ContinuationAcceptance{
		PhytomerID:     phytomerID,
		Pollen:         target.Pollen,
		Substrate:      target.Substrate,
		Handle:         target.Handle,
		IdempotencyKey: key,
		Intent:         intent,
		IntentDigest:   ContinuationIntentDigest(intent),
	})
	if err != nil {
		return ContinuationRecord{}, err
	}
	return rec, nil
}
