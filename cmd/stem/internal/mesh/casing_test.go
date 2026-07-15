package mesh

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMeshWireKeysAreCamelCase pins the mesh wire contract to the repo-wide
// JSON casing rule (GUARDRAILS.md §2: all serialized JSON payload keys are
// camelCase). The mesh originally shipped kebab-case keys by following a
// since-corrected contradiction in AGENTS.md; this test makes
// any regression — or any new kebab-case key — a hard failure.
func TestMeshWireKeysAreCamelCase(t *testing.T) {
	payloads := map[string]any{
		"TokenClaims": TokenClaims{
			Issuer:        "opentendril-mesh",
			Subject:       "stem-graft",
			Audience:      []string{"mesh-graft"},
			IssuedAt:      1,
			ExpiresAt:     2,
			NotBefore:     1,
			ID:            "token-123",
			MeshScope:     "mesh-graft",
			WorkspacePath: "/tmp/workspace",
		},
		"TraitEnvelope": TraitEnvelope{
			Trait: TraitPayload{
				Kind:    TraitKindPlasmid,
				Name:    "test-plasmid",
				Version: "v1",
				Content: "content",
			},
			Origin: TraitOrigin{
				NodeID:               "node-123",
				PublicKeyFingerprint: "fingerprint",
			},
			SignedAt:  1234567890,
			Signature: "signature",
		},
		"TraitRecord": TraitRecord{
			TraitID: "trait-123",
			Trait: TraitEnvelope{
				Trait: TraitPayload{
					Kind:    TraitKindGenotype,
					Name:    "test-genotype",
					Version: "v1",
					Content: "content",
				},
				Origin: TraitOrigin{
					NodeID:               "node-123",
					PublicKeyFingerprint: "fingerprint",
				},
				SignedAt:  1234567890,
				Signature: "signature",
			},
			Status:       TraitStatusPending,
			AcceptPolicy: "manual",
			ReceivedAt:   1234567890,
		},
		"adminIssueTokenRequest": adminIssueTokenRequest{
			Issuer:        "issuer",
			Subject:       "subject",
			Audience:      "audience",
			MeshScope:     "scope",
			WorkspacePath: "/tmp/workspace",
			TokenID:       "token-123",
			TTL:           "1h",
		},
		"graftRequest": graftRequest{
			Type:          "graft-request",
			WorkspacePath: "/tmp/workspace",
			Branch:        "branch",
			CommitMessage: "message",
			CommitHash:    "hash",
			SequencePath:  "path",
			Patch:         "patch",
		},
		"graftMessage": graftMessage{
			Type:       "graft-result",
			Status:     "complete",
			Stream:     "stdout",
			Message:    "message",
			CommitHash: "hash",
			Error:      "error",
		},
	}

	for name, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}

		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}

		for key := range decoded {
			if strings.ContainsAny(key, "-_") {
				t.Errorf("%s serializes non-camelCase JSON key %q (GUARDRAILS.md §2 requires camelCase payload keys)", name, key)
			}
		}
	}
}

// TestTokenClaimsWireFormat locks the exact claim keys inside signed mesh
// tokens, since renaming them invalidates every previously issued token.
func TestTokenClaimsWireFormat(t *testing.T) {
	claims := TokenClaims{
		Issuer:        "iss",
		Subject:       "sub",
		Audience:      []string{"aud"},
		IssuedAt:      1,
		ExpiresAt:     2,
		NotBefore:     1,
		ID:            "jti",
		MeshScope:     "scope",
		WorkspacePath: "/tmp/workspace",
	}

	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}

	expected := []string{"iss", "sub", "aud", "iat", "exp", "nbf", "jti", "meshScope", "workspacePath"}
	if len(decoded) != len(expected) {
		t.Fatalf("claims serialized %d keys, want %d (%v)", len(decoded), len(expected), decoded)
	}
	for _, key := range expected {
		if _, ok := decoded[key]; !ok {
			t.Errorf("claims missing expected wire key %q", key)
		}
	}
}
