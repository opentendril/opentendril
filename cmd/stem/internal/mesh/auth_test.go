package mesh

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndLoadKeyPairRoundTrip(t *testing.T) {
	workspace := t.TempDir()

	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	if err := WriteKeyPair(workspace, pair); err != nil {
		t.Fatalf("WriteKeyPair failed: %v", err)
	}

	privatePath, publicPath := WorkspaceKeyPaths(workspace)
	if _, err := os.Stat(privatePath); err != nil {
		t.Fatalf("private key not written: %v", err)
	}
	if _, err := os.Stat(publicPath); err != nil {
		t.Fatalf("public key not written: %v", err)
	}

	loaded, err := LoadKeyPair(workspace)
	if err != nil {
		t.Fatalf("LoadKeyPair failed: %v", err)
	}

	if !bytes.Equal(loaded.PublicKey, pair.PublicKey) {
		t.Fatalf("public key mismatch after round trip")
	}
	if !bytes.Equal(loaded.PrivateKey, pair.PrivateKey) {
		t.Fatalf("private key mismatch after round trip")
	}
}

func TestIssueAndVerifyTokenRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	now := time.Date(2026, time.June, 30, 12, 0, 0, 0, time.UTC)
	token, err := IssueToken(pair.PrivateKey, TokenOptions{
		Issuer:        defaultIssuer,
		Subject:       "mesh-graft",
		Audience:      []string{defaultAudience},
		MeshScope:     defaultMeshScope,
		WorkspacePath: workspace,
		TokenID:       "mesh-token-123",
		ExpiresIn:     30 * time.Minute,
		Now:           now,
	})
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}

	claims, err := VerifyToken(token, pair.PublicKey, TokenValidationOptions{
		Now:               now.Add(5 * time.Minute),
		ExpectedIssuer:    defaultIssuer,
		ExpectedAudience:  defaultAudience,
		ExpectedScope:     defaultMeshScope,
		ExpectedWorkspace: workspace,
	})
	if err != nil {
		t.Fatalf("VerifyToken failed: %v", err)
	}

	if claims.ID != "mesh-token-123" {
		t.Fatalf("token ID = %q, want mesh-token-123", claims.ID)
	}
	if claims.Subject != "mesh-graft" {
		t.Fatalf("subject = %q, want mesh-graft", claims.Subject)
	}
	if claims.WorkspacePath != workspace {
		t.Fatalf("workspace path = %q, want %q", claims.WorkspacePath, workspace)
	}
	if claims.MeshScope != defaultMeshScope {
		t.Fatalf("scope = %q, want %q", claims.MeshScope, defaultMeshScope)
	}
}

func TestVerifyTokenRejectsTampering(t *testing.T) {
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	token, err := IssueToken(pair.PrivateKey, TokenOptions{
		Now:       time.Unix(1000, 0).UTC(),
		ExpiresIn: time.Hour,
	})
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
	originalPayload := parts[1]
	if len(originalPayload) == 0 {
		t.Fatalf("expected payload to be non-empty")
	}
	if originalPayload[len(originalPayload)-1] == 'A' {
		parts[1] = originalPayload[:len(originalPayload)-1] + "B"
	} else {
		parts[1] = originalPayload[:len(originalPayload)-1] + "A"
	}
	if parts[1] == originalPayload {
		t.Fatalf("expected tampered payload")
	}

	tampered := strings.Join(parts, ".")
	if _, err := VerifyToken(tampered, pair.PublicKey, TokenValidationOptions{Now: time.Unix(1001, 0).UTC()}); err == nil {
		t.Fatalf("expected tampered token to fail verification")
	}
}

func TestVerifyTokenRejectsExpiredTokens(t *testing.T) {
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	token, err := IssueToken(pair.PrivateKey, TokenOptions{
		Now:       time.Unix(1000, 0).UTC(),
		ExpiresIn: time.Minute,
	})
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}

	if _, err := VerifyToken(token, pair.PublicKey, TokenValidationOptions{Now: time.Unix(1000+120, 0).UTC()}); err == nil {
		t.Fatalf("expected expired token to fail verification")
	}
}

func TestWriteKeyPairCreatesWorkspaceSecurityDir(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "repo")
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	if err := WriteKeyPair(workspace, pair); err != nil {
		t.Fatalf("WriteKeyPair failed: %v", err)
	}

	if _, err := os.Stat(WorkspaceSecurityDir(workspace)); err != nil {
		t.Fatalf("workspace security dir missing: %v", err)
	}
}
