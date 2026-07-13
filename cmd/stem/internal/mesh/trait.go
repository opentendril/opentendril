package mesh

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// TraitKind identifies the epigenetic trait family carried by a signed envelope.
type TraitKind string

const (
	TraitKindPlasmid  TraitKind = "plasmid"
	TraitKindGenotype TraitKind = "genotype"
	TraitKindSequence TraitKind = "sequence"
)

// TraitPayload is the transport-agnostic trait body shared between mesh instances.
type TraitPayload struct {
	Kind    TraitKind `json:"kind"`
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Content string    `json:"content"`
}

// TraitOrigin captures the node that minted the signed trait envelope.
type TraitOrigin struct {
	NodeID               string `json:"nodeId"`
	PublicKeyFingerprint string `json:"publicKeyFingerprint"`
}

// TraitEnvelope packages a trait with its origin and signature.
//
// acceptPolicy (deny | manual | allowlist) governs how future trait ingress is
// classified; the actual inbound transport hook will be wired in a later
// slice.
type TraitEnvelope struct {
	Trait     TraitPayload `json:"trait"`
	Origin    TraitOrigin  `json:"origin"`
	SignedAt  int64        `json:"signedAt"`
	Signature string       `json:"signature"` // base64url(ed25519(canonicalJson(trait+origin+signedAt)))
}

// TraitStatus describes the lifecycle state of a trait inside the local inbox.
type TraitStatus string

const (
	TraitStatusPending  TraitStatus = "pending"
	TraitStatusAccepted TraitStatus = "accepted"
	TraitStatusRejected TraitStatus = "rejected"
	TraitStatusDropped  TraitStatus = "dropped"
)

// TraitDecisionAction classifies what the policy engine should do when a trait arrives.
type TraitDecisionAction string

const (
	TraitDecisionDrop      TraitDecisionAction = "drop"
	TraitDecisionPending   TraitDecisionAction = "pending"
	TraitDecisionAutoGraft TraitDecisionAction = "auto-graft"
)

// TraitDecision records the outcome of acceptPolicy evaluation.
type TraitDecision struct {
	Policy           string              `json:"policy"`
	Action           TraitDecisionAction `json:"action"`
	Status           TraitStatus         `json:"status"`
	Allowlist        []string            `json:"allowlist,omitempty"`
	Allowlisted      bool                `json:"allowlisted,omitempty"`
	SignatureMatched bool                `json:"signatureMatched,omitempty"`
}

// TraitRecord is the local inbox view of one trait. It is the JSON shape the
// mesh trait list route returns.
type TraitRecord struct {
	TraitID      string        `json:"traitId"`
	Trait        TraitEnvelope `json:"trait"`
	Status       TraitStatus   `json:"status"`
	AcceptPolicy string        `json:"acceptPolicy,omitempty"`
	ReceivedAt   int64         `json:"receivedAt,omitempty"`
}

type traitEnvelopeSigningBody struct {
	Trait    TraitPayload `json:"trait"`
	Origin   TraitOrigin  `json:"origin"`
	SignedAt int64        `json:"signedAt"`
}

// TraitEnvelopeSigningPayload returns the canonical JSON payload that the
// envelope signature covers.
func TraitEnvelopeSigningPayload(envelope TraitEnvelope) ([]byte, error) {
	return json.Marshal(traitEnvelopeSigningBody{
		Trait:    envelope.Trait,
		Origin:   envelope.Origin,
		SignedAt: envelope.SignedAt,
	})
}

// SignTraitEnvelope signs a trait envelope and returns its base64url signature.
func SignTraitEnvelope(envelope TraitEnvelope, privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid ed25519 private key length: %d", len(privateKey))
	}

	payload, err := TraitEnvelopeSigningPayload(envelope)
	if err != nil {
		return "", fmt.Errorf("encode trait envelope: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload)), nil
}

// VerifyTraitEnvelopeSignature validates a trait envelope signature against a
// public key. It returns false for malformed envelopes and signatures.
func VerifyTraitEnvelopeSignature(envelope TraitEnvelope, publicKey ed25519.PublicKey) (bool, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return false, fmt.Errorf("invalid ed25519 public key length: %d", len(publicKey))
	}

	payload, err := TraitEnvelopeSigningPayload(envelope)
	if err != nil {
		return false, fmt.Errorf("encode trait envelope: %w", err)
	}

	signature, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(envelope.Signature))
	if err != nil {
		return false, err
	}
	if len(signature) != ed25519.SignatureSize {
		return false, nil
	}

	return ed25519.Verify(publicKey, payload, signature), nil
}

// ResolveTraitAcceptPolicy classifies an arriving trait based on the current
// acceptPolicy config string.
//
// deny: drop immediately.
// manual: park in the pending inbox.
// allowlist:<fingerprints>: auto-graft when the origin fingerprint matches and
// the signature has already been verified; otherwise the trait stays pending.
func ResolveTraitAcceptPolicy(acceptPolicy string, envelope TraitEnvelope, signatureMatched bool) TraitDecision {
	policy := strings.ToLower(strings.TrimSpace(acceptPolicy))
	if policy == "" {
		policy = "manual"
	}

	switch {
	case policy == "deny":
		return TraitDecision{
			Policy:           policy,
			Action:           TraitDecisionDrop,
			Status:           TraitStatusDropped,
			SignatureMatched: signatureMatched,
		}
	case policy == "manual":
		return TraitDecision{
			Policy:           policy,
			Action:           TraitDecisionPending,
			Status:           TraitStatusPending,
			SignatureMatched: signatureMatched,
		}
	case strings.HasPrefix(policy, "allowlist"):
		allowlist := parseTraitAllowlist(policy)
		fingerprint := strings.ToLower(strings.TrimSpace(envelope.Origin.PublicKeyFingerprint))
		allowlisted := fingerprint != "" && containsString(allowlist, fingerprint)
		if allowlisted && signatureMatched {
			return TraitDecision{
				Policy:           policy,
				Action:           TraitDecisionAutoGraft,
				Status:           TraitStatusAccepted,
				Allowlist:        allowlist,
				Allowlisted:      true,
				SignatureMatched: true,
			}
		}
		return TraitDecision{
			Policy:           policy,
			Action:           TraitDecisionPending,
			Status:           TraitStatusPending,
			Allowlist:        allowlist,
			Allowlisted:      allowlisted,
			SignatureMatched: signatureMatched,
		}
	default:
		return TraitDecision{
			Policy:           policy,
			Action:           TraitDecisionPending,
			Status:           TraitStatusPending,
			SignatureMatched: signatureMatched,
		}
	}
}

func parseTraitAllowlist(policy string) []string {
	raw := strings.TrimSpace(policy)
	if raw == "" {
		return nil
	}

	raw = strings.TrimPrefix(raw, "allowlist")
	raw = strings.TrimSpace(strings.TrimLeft(raw, ":= "))
	if raw == "" {
		return nil
	}

	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})

	if len(parts) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(parts))
	allowlist := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.ToLower(strings.TrimSpace(part))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		allowlist = append(allowlist, value)
	}
	sort.Strings(allowlist)
	if len(allowlist) == 0 {
		return nil
	}
	return allowlist
}

func traitRecordID(envelope TraitEnvelope) string {
	if signature := strings.TrimSpace(envelope.Signature); signature != "" {
		return signature
	}

	parts := []string{
		strings.TrimSpace(string(envelope.Trait.Kind)),
		strings.TrimSpace(envelope.Trait.Name),
		strings.TrimSpace(envelope.Trait.Version),
		strings.TrimSpace(envelope.Origin.NodeID),
		fmt.Sprintf("%d", envelope.SignedAt),
	}
	return strings.Join(parts, ":")
}

// TraitInbox keeps track of pending, accepted, and rejected traits in memory.
// It is intentionally simple for this slice; a later slice can swap in
// persistence without changing the Core surface.
type TraitInbox struct {
	mu       sync.Mutex
	pending  map[string]TraitRecord
	accepted map[string]TraitRecord
	rejected map[string]TraitRecord
}

// NewTraitInbox constructs an empty trait inbox.
func NewTraitInbox() *TraitInbox {
	return &TraitInbox{
		pending:  make(map[string]TraitRecord),
		accepted: make(map[string]TraitRecord),
		rejected: make(map[string]TraitRecord),
	}
}

// Ingest evaluates the acceptPolicy and stores the trait in the appropriate
// in-memory bucket.
func (i *TraitInbox) Ingest(envelope TraitEnvelope, acceptPolicy string, signatureMatched bool) (TraitRecord, TraitDecision) {
	decision := ResolveTraitAcceptPolicy(acceptPolicy, envelope, signatureMatched)
	record := TraitRecord{
		TraitID:      traitRecordID(envelope),
		Trait:        envelope,
		Status:       decision.Status,
		AcceptPolicy: decision.Policy,
		ReceivedAt:   time.Now().UTC().Unix(),
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.ensure()

	switch decision.Action {
	case TraitDecisionDrop:
		i.pendingDelete(record.TraitID)
		i.acceptedDelete(record.TraitID)
		i.rejectedDelete(record.TraitID)
		return record, decision
	case TraitDecisionAutoGraft:
		i.pendingDelete(record.TraitID)
		i.rejectedDelete(record.TraitID)
		i.accepted[record.TraitID] = record
	case TraitDecisionPending:
		i.acceptedDelete(record.TraitID)
		i.rejectedDelete(record.TraitID)
		i.pending[record.TraitID] = record
	}

	return record, decision
}

// ListPending returns a stable snapshot of the pending inbox.
func (i *TraitInbox) ListPending() []TraitRecord {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ensure()

	if len(i.pending) == 0 {
		return []TraitRecord{}
	}

	ids := make([]string, 0, len(i.pending))
	for id := range i.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	records := make([]TraitRecord, 0, len(ids))
	for _, id := range ids {
		records = append(records, i.pending[id])
	}
	return records
}

// Accept moves the trait into the accepted bucket when it exists, and is a
// no-op otherwise. That keeps the slice permissive while the later persistence
// slice lands.
func (i *TraitInbox) Accept(traitID string) error {
	i.move(strings.TrimSpace(traitID), TraitStatusAccepted)
	return nil
}

// Reject moves the trait into the rejected bucket when it exists, and is a
// no-op otherwise.
func (i *TraitInbox) Reject(traitID string) error {
	i.move(strings.TrimSpace(traitID), TraitStatusRejected)
	return nil
}

func (i *TraitInbox) ensure() {
	if i.pending == nil {
		i.pending = make(map[string]TraitRecord)
	}
	if i.accepted == nil {
		i.accepted = make(map[string]TraitRecord)
	}
	if i.rejected == nil {
		i.rejected = make(map[string]TraitRecord)
	}
}

func (i *TraitInbox) move(traitID string, status TraitStatus) {
	if traitID == "" {
		return
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.ensure()

	record, ok := i.pending[traitID]
	if ok {
		delete(i.pending, traitID)
	} else if record, ok = i.accepted[traitID]; ok {
		delete(i.accepted, traitID)
	} else if record, ok = i.rejected[traitID]; ok {
		delete(i.rejected, traitID)
	} else {
		return
	}

	record.Status = status
	switch status {
	case TraitStatusAccepted:
		i.accepted[traitID] = record
	case TraitStatusRejected:
		i.rejected[traitID] = record
	default:
		i.pending[traitID] = record
	}
}

func (i *TraitInbox) pendingDelete(traitID string) {
	delete(i.pending, traitID)
}

func (i *TraitInbox) acceptedDelete(traitID string) {
	delete(i.accepted, traitID)
}

func (i *TraitInbox) rejectedDelete(traitID string) {
	delete(i.rejected, traitID)
}
