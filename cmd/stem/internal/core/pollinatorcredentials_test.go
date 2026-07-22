package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIssuedCredentialResolvesToItsPollen is the core property: the credential
// carries the identity, so resolving it needs nothing from the caller.
func TestIssuedCredentialResolvesToItsPollen(t *testing.T) {
	dir := t.TempDir()
	secret, credential, err := IssuePollinatorCredential(dir, "claude", "laptop")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if credential.Pollen != "claude" || !credential.Active() {
		t.Fatalf("credential = %+v, want an active credential for claude", credential)
	}

	stored, err := LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := ResolvePollenFromCredential(stored, secret); got != "claude" {
		t.Fatalf("resolved %q, want claude", got)
	}
}

// TestSecretIsNeverPersisted: a leaked store must not be a leaked credential.
func TestSecretIsNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, PollinatorCredentialsFilename))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the store contains the secret — a leaked file would be a leaked credential")
	}
	if !strings.Contains(string(raw), "digest") {
		t.Fatal("the store does not appear to hold a digest")
	}

	info, err := os.Stat(filepath.Join(dir, PollinatorCredentialsFilename))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store permissions = %o, want 600", perm)
	}
}

// TestRevokedCredentialStopsResolving is the property revocation exists for.
func TestRevokedCredentialStopsResolving(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	revoked, err := RevokePollinatorCredentials(dir, "claude")
	if err != nil || revoked != 1 {
		t.Fatalf("revoke = %d, %v; want 1, nil", revoked, err)
	}

	stored, err := LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := ResolvePollenFromCredential(stored, secret); got != "" {
		t.Fatalf("a revoked credential still resolved to %q", got)
	}
	// The record survives revocation: an operator can still see what existed.
	if len(stored) != 1 || stored[0].Active() {
		t.Fatalf("stored = %+v, want the credential retained and marked revoked", stored)
	}
}

// TestUnknownAndMalformedCredentialsResolveToNothing: every failure is the
// same deny, with no signal about which one occurred.
func TestUnknownAndMalformedCredentialsResolveToNothing(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := IssuePollinatorCredential(dir, "claude", ""); err != nil {
		t.Fatalf("issue: %v", err)
	}
	stored, err := LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for _, presented := range []string{
		"", "   ", "not-a-credential", "tendril_", "tendril_wrong",
		"Bearer tendril_wrong", "TENDRIL_uppercase",
		// The superseded prefix must not resolve either.
		"otp_wrong",
	} {
		if got := ResolvePollenFromCredential(stored, presented); got != "" {
			t.Errorf("presented %q resolved to %q, want nothing", presented, got)
		}
	}
}

// TestCredentialsAreIndependentPerPollen: revoking one Pollinator must not
// disturb another, which is the point of per-Pollinator credentials.
func TestCredentialsAreIndependentPerPollen(t *testing.T) {
	dir := t.TempDir()
	claudeSecret, _, err := IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue claude: %v", err)
	}
	codexSecret, _, err := IssuePollinatorCredential(dir, "codex", "")
	if err != nil {
		t.Fatalf("issue codex: %v", err)
	}

	if _, err := RevokePollinatorCredentials(dir, "claude"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	stored, err := LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := ResolvePollenFromCredential(stored, claudeSecret); got != "" {
		t.Fatalf("revoked claude still resolves to %q", got)
	}
	if got := ResolvePollenFromCredential(stored, codexSecret); got != "codex" {
		t.Fatalf("codex resolved to %q after revoking claude — revocation is not independent", got)
	}
}

// TestMalformedStoreIsAnError: it must never degrade into "no credentials",
// which would silently return every caller to the weaker declared-Pollen path.
func TestMalformedStoreIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PollinatorCredentialsFilename), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadPollinatorCredentials(dir); err == nil {
		t.Fatal("a malformed store loaded as empty — that silently weakens the tier")
	}
}

// TestMissingStoreIsTheSecureDefault: none issued means none can authenticate.
func TestMissingStoreIsTheSecureDefault(t *testing.T) {
	credentials, err := LoadPollinatorCredentials(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(credentials) != 0 {
		t.Fatalf("loaded %d credential(s) from an empty directory", len(credentials))
	}
	if got := ResolvePollenFromCredential(credentials, "tendril_anything"); got != "" {
		t.Fatalf("resolved %q with no credentials issued", got)
	}
}
