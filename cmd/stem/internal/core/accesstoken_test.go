package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMintedTokenVerifiesToItsClaims is the core round-trip: a minted token
// verifies with the Stem's public key and carries back the Pollen and scope it
// was minted with.
func TestMintedTokenVerifiesToItsClaims(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	scope := AccessTokenScope{OperationClasses: []string{"git.push"}, Substrates: []string{"core"}}
	token, err := signer.MintAccessToken("claude", 5*time.Minute, scope)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !LooksLikeAccessToken(token) {
		t.Fatalf("token %q is not recognised as an access token", token)
	}

	claims, ok := signer.VerifyAccessToken(token)
	if !ok {
		t.Fatal("a freshly minted token did not verify")
	}
	if claims.Pollen != "claude" {
		t.Fatalf("pollen = %q, want claude", claims.Pollen)
	}
	if len(claims.Scope.OperationClasses) != 1 || claims.Scope.OperationClasses[0] != "git.push" {
		t.Fatalf("operation-classes = %v, want [git.push]", claims.Scope.OperationClasses)
	}
	if len(claims.Scope.Substrates) != 1 || claims.Scope.Substrates[0] != "core" {
		t.Fatalf("substrates = %v, want [core]", claims.Scope.Substrates)
	}
}

// TestVerifyIsStateless: a token verifies with the public key alone — no signer,
// no credential store. This is the property a remote executor relies on.
func TestVerifyIsStateless(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, err := signer.MintAccessToken("claude", 0, AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	public := signer.Public()

	if _, ok := VerifyAccessToken(public, token); !ok {
		t.Fatal("token did not verify against the public key alone")
	}
}

// TestForgedSignatureIsDenied: a token signed by any other key does not verify.
func TestForgedSignatureIsDenied(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	// A token minted by a different signer must not verify against ours.
	other, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("other signer: %v", err)
	}
	forged, err := other.MintAccessToken("claude", 5*time.Minute, AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint forged: %v", err)
	}
	if _, ok := signer.VerifyAccessToken(forged); ok {
		t.Fatal("a token from another key verified — signature is not being checked")
	}

	// A tampered payload with the original signature must not verify either.
	valid, _ := signer.MintAccessToken("claude", 5*time.Minute, AccessTokenScope{})
	body := strings.TrimPrefix(valid, AccessTokenPrefix)
	_, sig, _ := strings.Cut(body, ".")
	tampered := AccessTokenPrefix +
		base64.RawURLEncoding.EncodeToString([]byte(`{"pollen":"root","exp":"2999-01-01T00:00:00Z"}`)) +
		"." + sig
	if _, ok := signer.VerifyAccessToken(tampered); ok {
		t.Fatal("a tampered payload verified — the signature does not cover the payload")
	}
}

// TestExpiredTokenIsDenied: an elapsed lifetime denies, with no way to tell it
// apart from any other failure.
func TestExpiredTokenIsDenied(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	// Sign claims that already expired, using the real key, to isolate the
	// expiry check from the signature check.
	past := time.Now().UTC().Add(-time.Minute)
	payload, _ := json.Marshal(AccessTokenClaims{Pollen: "claude", IssuedAt: past.Add(-time.Minute), ExpiresAt: past})
	token := AccessTokenPrefix +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(ed25519.Sign(signer.private, payload))

	if _, ok := signer.VerifyAccessToken(token); ok {
		t.Fatal("an expired token verified")
	}
}

// TestTTLOverCapIsRejected: a request for more than the maximum is refused, not
// silently clamped.
func TestTTLOverCapIsRejected(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	if _, err := signer.MintAccessToken("claude", MaxAccessTokenTTL+time.Second, AccessTokenScope{}); err == nil {
		t.Fatal("a ttl above the cap was accepted")
	}
}

// TestDefaultTTLApplied: a zero ttl takes the default lifetime rather than
// minting an already-expired or immortal token.
func TestDefaultTTLApplied(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, err := signer.MintAccessToken("claude", 0, AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims, ok := signer.VerifyAccessToken(token)
	if !ok {
		t.Fatal("default-ttl token did not verify")
	}
	life := claims.ExpiresAt.Sub(claims.IssuedAt)
	if life <= 0 || life > MaxAccessTokenTTL {
		t.Fatalf("default lifetime = %s, want (0, %s]", life, MaxAccessTokenTTL)
	}
}

// TestMintFromCredentialAuthenticatesTheRoot: only a resolving root credential
// mints a token; a revoked one is refused.
func TestMintFromCredentialAuthenticatesTheRoot(t *testing.T) {
	dir := t.TempDir()
	signer, err := LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	secret, _, err := IssuePollinatorCredential(dir, "claude", "laptop")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	credentials, err := LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	token, err := signer.MintFromCredential(credentials, secret, 0, AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint from credential: %v", err)
	}
	claims, ok := signer.VerifyAccessToken(token)
	if !ok || claims.Pollen != "claude" {
		t.Fatalf("token from a valid root did not resolve to claude (ok=%v, pollen=%q)", ok, claims.Pollen)
	}

	if _, err := RevokePollinatorCredentials(dir, "claude"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	revoked, err := LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := signer.MintFromCredential(revoked, secret, 0, AccessTokenScope{}); err == nil {
		t.Fatal("a revoked root credential minted a token")
	}
}

// TestSigningKeyIsPersistedAndReused: the key survives a reload and is stored
// 0600 — a restarted Stem verifies tokens it minted before the restart.
func TestSigningKeyIsPersistedAndReused(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("first signer: %v", err)
	}
	token, err := first.MintAccessToken("claude", 5*time.Minute, AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, StemSigningKeyFilename))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key permissions = %o, want 600", perm)
	}

	second, err := LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("reload signer: %v", err)
	}
	if _, ok := second.VerifyAccessToken(token); !ok {
		t.Fatal("a reloaded signer could not verify a token minted before reload — the key was not reused")
	}
}

// TestAccessTokenAndCredentialPrefixesAreMutuallyExclusive: a bearer is at most
// one of the two, so surfaces route it unambiguously.
func TestAccessTokenAndCredentialPrefixesAreMutuallyExclusive(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, _ := signer.MintAccessToken("claude", 5*time.Minute, AccessTokenScope{})
	if LooksLikePollinatorCredential(token) {
		t.Fatal("an access token is mis-recognised as a Pollinator credential")
	}

	rootSecret, _, err := IssuePollinatorCredential(t.TempDir(), "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if LooksLikeAccessToken(rootSecret) {
		t.Fatal("a Pollinator credential is mis-recognised as an access token")
	}
}

// TestEmptyPollenIsRejected: a token must name an identity.
func TestEmptyPollenIsRejected(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	if _, err := signer.MintAccessToken("  ", 0, AccessTokenScope{}); err == nil {
		t.Fatal("a token with no Pollen was minted")
	}
}

// TestVerifyRejectsWrongSizedKey guards the length check that precedes
// ed25519.Verify, which panics on a wrong-sized key rather than returning false.
func TestVerifyRejectsWrongSizedKey(t *testing.T) {
	signer, err := LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, _ := signer.MintAccessToken("claude", 5*time.Minute, AccessTokenScope{})
	short := make(ed25519.PublicKey, 8)
	_, _ = rand.Read(short)
	if _, ok := VerifyAccessToken(short, token); ok {
		t.Fatal("verification succeeded against a malformed public key")
	}
}
