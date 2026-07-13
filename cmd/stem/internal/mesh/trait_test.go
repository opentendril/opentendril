package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestTraitEnvelopeSignatureRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	envelope := TraitEnvelope{
		Trait: TraitPayload{
			Kind:    TraitKindPlasmid,
			Name:    "go-rules",
			Version: "v1",
			Content: "content",
		},
		Origin: TraitOrigin{
			NodeID:               "node-123",
			PublicKeyFingerprint: PublicKeyFingerprint(publicKey),
		},
		SignedAt: 1234567890,
	}

	signature, err := SignTraitEnvelope(envelope, privateKey)
	if err != nil {
		t.Fatalf("sign trait envelope: %v", err)
	}
	envelope.Signature = signature

	ok, err := VerifyTraitEnvelopeSignature(envelope, publicKey)
	if err != nil {
		t.Fatalf("verify trait envelope: %v", err)
	}
	if !ok {
		t.Fatal("expected trait envelope signature to verify")
	}
}

func TestResolveTraitAcceptPolicy(t *testing.T) {
	envelope := TraitEnvelope{
		Origin: TraitOrigin{PublicKeyFingerprint: "fingerprint"},
	}

	tests := []struct {
		name           string
		policy         string
		signatureMatch bool
		wantAction     TraitDecisionAction
		wantStatus     TraitStatus
		wantAllowlist  []string
	}{
		{
			name:       "deny",
			policy:     "deny",
			wantAction: TraitDecisionDrop,
			wantStatus: TraitStatusDropped,
		},
		{
			name:       "manual",
			policy:     "manual",
			wantAction: TraitDecisionPending,
			wantStatus: TraitStatusPending,
		},
		{
			name:           "allowlist auto graft",
			policy:         "allowlist:fingerprint,other",
			signatureMatch: true,
			wantAction:     TraitDecisionAutoGraft,
			wantStatus:     TraitStatusAccepted,
			wantAllowlist:  []string{"fingerprint", "other"},
		},
		{
			name:          "allowlist mismatch stays pending",
			policy:        "allowlist:fingerprint",
			wantAction:    TraitDecisionPending,
			wantStatus:    TraitStatusPending,
			wantAllowlist: []string{"fingerprint"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision := ResolveTraitAcceptPolicy(tc.policy, envelope, tc.signatureMatch)
			if decision.Action != tc.wantAction {
				t.Fatalf("action = %q, want %q", decision.Action, tc.wantAction)
			}
			if decision.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", decision.Status, tc.wantStatus)
			}
			if len(decision.Allowlist) != len(tc.wantAllowlist) {
				t.Fatalf("allowlist = %v, want %v", decision.Allowlist, tc.wantAllowlist)
			}
			for i := range tc.wantAllowlist {
				if decision.Allowlist[i] != tc.wantAllowlist[i] {
					t.Fatalf("allowlist = %v, want %v", decision.Allowlist, tc.wantAllowlist)
				}
			}
		})
	}
}

func TestTraitInboxStoresAndMovesTraits(t *testing.T) {
	inbox := NewTraitInbox()
	envelope := TraitEnvelope{
		Trait: TraitPayload{
			Kind:    TraitKindGenotype,
			Name:    "frontend-dev",
			Version: "v1",
		},
		Origin: TraitOrigin{
			NodeID:               "node-123",
			PublicKeyFingerprint: "fingerprint",
		},
		SignedAt:  1234567890,
		Signature: "trait-123",
	}

	record, decision := inbox.Ingest(envelope, "manual", false)
	if decision.Status != TraitStatusPending {
		t.Fatalf("decision status = %q, want pending", decision.Status)
	}
	if record.Status != TraitStatusPending {
		t.Fatalf("record status = %q, want pending", record.Status)
	}
	if record.TraitID != "trait-123" {
		t.Fatalf("record id = %q, want trait-123", record.TraitID)
	}

	pending := inbox.ListPending()
	if len(pending) != 1 || pending[0].TraitID != "trait-123" {
		t.Fatalf("pending = %#v, want one record", pending)
	}

	if err := inbox.Accept("trait-123"); err != nil {
		t.Fatalf("accept trait: %v", err)
	}
	if pending := inbox.ListPending(); len(pending) != 0 {
		t.Fatalf("pending = %#v, want empty after accept", pending)
	}

	// Rejecting the same trait should keep it out of the pending queue.
	if err := inbox.Reject("trait-123"); err != nil {
		t.Fatalf("reject trait: %v", err)
	}
	if pending := inbox.ListPending(); len(pending) != 0 {
		t.Fatalf("pending = %#v, want empty after reject", pending)
	}
}
