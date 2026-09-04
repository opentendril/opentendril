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

// Continuation delivery states. Continuation is a Stem-internal lifecycle
// contract, not a governed Pollinator command.
const (
	ContinuationDeliveryPending    = "pending"
	ContinuationDeliveryDelivering = "delivering"
	ContinuationDeliveryDelivered  = "delivered"
	ContinuationDeliveryFailed     = "failed"

	continuedIntentHeading = "Continued intent accepted for this Phytomer, in acceptance order:"
	continuedIntentBounds  = "This continued intent does not change verification, isolation, egress, or iteration bounds."
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
	ErrContinuationDeliveryState       = errors.New("phytomer continuation delivery state transition is not allowed")
	ErrSeedSettlementNotFenced         = errors.New("seed settlement fence was not acquired")
	ErrSeedSettlementInvalid           = errors.New("seed settlement is not valid for the exact target")
	// ErrContinuationUndeliverable is the safe terminal failure when accepted
	// continued intent cannot be delivered within original Seed bounds. It
	// never includes raw continued intent.
	ErrContinuationUndeliverable = errors.New("accepted continued intent could not be delivered within the Seed bounds")
	// ErrSeedInterruptedByRestart is the safe diagnostic stamped onto orphaned
	// active Seed work after Stem restart.
	ErrSeedInterruptedByRestart = errors.New("seed growth was interrupted by Stem restart")
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

// TerminalFailureAccount reports how many unresolved continuations a
// terminal Seed transaction failed.
type TerminalFailureAccount struct {
	UnresolvedFailed int
}

// ContinuationPersistence is the injected durable continuation port. Core
// owns semantic validation and composition; the port records and queries.
// Core never imports historydb.
type ContinuationPersistence struct {
	ResolveTarget                func(ctx context.Context, phytomerID string) (ContinuationTarget, bool, error)
	Accept                       func(ctx context.Context, in ContinuationAcceptance) (ContinuationRecord, error)
	ClaimPending                 func(ctx context.Context, target ContinuationTarget) ([]ContinuationRecord, error)
	MarkDelivered                func(ctx context.Context, target ContinuationTarget, ids []string) error
	HasUnresolved                func(ctx context.Context, target ContinuationTarget) (bool, error)
	AcquireSettlementFence       func(ctx context.Context, target ContinuationTarget) (bool, error)
	CompleteSuccessfulSettlement func(ctx context.Context, settled SeedSettlement) error
	AccountTerminalFailure       func(ctx context.Context, settled SeedSettlement) (TerminalFailureAccount, error)
	ReconcileOrphaned            func(ctx context.Context) error
}

// SeedContinuationLifecycle is the Core-owned continuation contract for one
// opened Seed growth. A nil value means continuation is not in play
// (synchronous/unopened growth).
type SeedContinuationLifecycle struct {
	persist ContinuationPersistence
	target  ContinuationTarget
	claimed []ContinuationRecord
}

// ComposeContinuedIntentPrompt appends a delimited continued-intent section
// to an existing iteration prompt. It does not replace the original goal or
// alter verify argv.
func ComposeContinuedIntentPrompt(basePrompt string, intents []string) string {
	selected := make([]string, 0, len(intents))
	for _, intent := range intents {
		if trimmed := strings.TrimSpace(intent); trimmed != "" {
			selected = append(selected, trimmed)
		}
	}
	if len(selected) == 0 {
		return basePrompt
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(basePrompt, "\r\n"))
	b.WriteString("\n\n")
	b.WriteString(continuedIntentHeading)
	b.WriteByte('\n')
	for i, intent := range selected {
		fmt.Fprintf(&b, "%d. %s\n", i+1, intent)
	}
	b.WriteByte('\n')
	b.WriteString(continuedIntentBounds)
	return b.String()
}

// openedContinuationLifecycleWired reports whether the per-run continuation
// operations required for durably opened Seed growth are present.
// ReconcileOrphaned is startup-only and is not part of this contract.
func (s *Service) openedContinuationLifecycleWired() error {
	if s == nil {
		return ErrContinuationNotWired
	}
	p := s.continuation
	if p.ClaimPending == nil || p.MarkDelivered == nil || p.HasUnresolved == nil ||
		p.AcquireSettlementFence == nil || p.CompleteSuccessfulSettlement == nil || p.AccountTerminalFailure == nil {
		return ErrContinuationNotWired
	}
	return nil
}

func (s *Service) newOpenedSeedContinuationLifecycle(target ContinuationTarget) *SeedContinuationLifecycle {
	if err := s.openedContinuationLifecycleWired(); err != nil {
		return nil
	}
	target.PhytomerID = strings.TrimSpace(target.PhytomerID)
	target.Handle = strings.TrimSpace(target.Handle)
	target.Pollen = strings.TrimSpace(target.Pollen)
	target.Substrate = strings.TrimSpace(target.Substrate)
	if target.PhytomerID == "" || target.Handle == "" || target.Substrate == "" {
		return nil
	}
	return &SeedContinuationLifecycle{persist: s.continuation, target: target}
}

// Target returns the exact Stem-owned identity this lifecycle is bound to.
func (l *SeedContinuationLifecycle) Target() ContinuationTarget {
	if l == nil {
		return ContinuationTarget{}
	}
	return l.target
}

// DeliverPending claims pending continuations for the next Sprout and appends
// them to the existing iteration prompt in durable sequence order.
func (l *SeedContinuationLifecycle) DeliverPending(ctx context.Context, basePrompt string) (string, error) {
	if l == nil || l.persist.ClaimPending == nil {
		return basePrompt, nil
	}
	recs, err := l.persist.ClaimPending(ctx, l.target)
	if err != nil {
		return "", err
	}
	l.claimed = recs
	intents := make([]string, 0, len(recs))
	for _, rec := range recs {
		intents = append(intents, rec.Intent)
	}
	return ComposeContinuedIntentPrompt(basePrompt, intents), nil
}

// ConfirmDelivery marks claimed continuations delivered at the Sprout
// cognitive boundary. It is a no-op when nothing was claimed.
func (l *SeedContinuationLifecycle) ConfirmDelivery(ctx context.Context) error {
	if l == nil || l.persist.MarkDelivered == nil || len(l.claimed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(l.claimed))
	for _, rec := range l.claimed {
		ids = append(ids, rec.ContinuationID)
	}
	if err := l.persist.MarkDelivered(ctx, l.target, ids); err != nil {
		return err
	}
	l.claimed = nil
	return nil
}

// AcquireSettlementFence asks persistence to fence successful settlement.
func (l *SeedContinuationLifecycle) AcquireSettlementFence(ctx context.Context) (bool, error) {
	if l == nil || l.persist.AcquireSettlementFence == nil {
		return false, ErrContinuationNotWired
	}
	return l.persist.AcquireSettlementFence(ctx, l.target)
}

// HasUnresolved reports pending or delivering continuation for this target.
func (l *SeedContinuationLifecycle) HasUnresolved(ctx context.Context) (bool, error) {
	if l == nil || l.persist.HasUnresolved == nil {
		return false, ErrContinuationNotWired
	}
	return l.persist.HasUnresolved(ctx, l.target)
}

// CompleteSuccessfulSettlement persists Fruit after a successful fence.
func (l *SeedContinuationLifecycle) CompleteSuccessfulSettlement(ctx context.Context, settled SeedSettlement) error {
	if l == nil || l.persist.CompleteSuccessfulSettlement == nil {
		return ErrContinuationNotWired
	}
	settled = bindSettlementTarget(settled, l.target)
	return l.persist.CompleteSuccessfulSettlement(ctx, settled)
}

// AccountTerminalFailure atomically terminalizes the Seed and fails unresolved
// continuation. Persistence overwrites a silent success-incompatible outcome
// when accepted intent was not delivered.
func (l *SeedContinuationLifecycle) AccountTerminalFailure(ctx context.Context, settled SeedSettlement) (TerminalFailureAccount, error) {
	if l == nil || l.persist.AccountTerminalFailure == nil {
		return TerminalFailureAccount{}, ErrContinuationNotWired
	}
	settled = bindSettlementTarget(settled, l.target)
	return l.persist.AccountTerminalFailure(ctx, settled)
}

func bindSettlementTarget(settled SeedSettlement, target ContinuationTarget) SeedSettlement {
	settled.Handle = target.Handle
	settled.PhytomerID = target.PhytomerID
	settled.Pollen = target.Pollen
	settled.Substrate = target.Substrate
	return settled
}

// ReconcileOrphanedSeedWork terminalizes active durable Seed/continuation
// state left by a previous process. Persistence disabled is not a memory
// fallback; callers must not serve when durable history exists and this fails.
func (s *Service) ReconcileOrphanedSeedWork(ctx context.Context) error {
	if s == nil || s.continuation.ReconcileOrphaned == nil {
		return ErrContinuationNotWired
	}
	return s.continuation.ReconcileOrphaned(ctx)
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
