package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// TestAccessTokenIsAcceptedOnDataRoutes: the REST transport gate admits a valid
// access token, exactly as it admits a resolving credential.
func TestAccessTokenIsAcceptedOnDataRoutes(t *testing.T) {
	signer, err := core.LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, err := signer.MintAccessToken("claude", 5*time.Minute, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	reached := false
	handler := withAPIKeyOrPollinatorAuth("botanist-key", nil, signer, false, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("a valid access token was refused: reached=%v status=%d", reached, rec.Code)
	}
}

// TestExpiredOrForgedTokenIsRefused: a token-shaped bearer that does not verify
// is terminal — it is refused, never retried as the Botanist key.
func TestExpiredOrForgedTokenIsRefused(t *testing.T) {
	signer, err := core.LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	// A token from a different key: shaped like a token, fails verification.
	other, err := core.LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("other signer: %v", err)
	}
	forged, _ := other.MintAccessToken("claude", 5*time.Minute, core.AccessTokenScope{})

	reached := false
	handler := withAPIKeyOrPollinatorAuth("botanist-key", nil, signer, false, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+forged)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if reached {
		t.Fatal("a forged token reached the handler")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged token: status = %d, want 401", rec.Code)
	}
}

// TestTokenWithNilVerifierIsDenied: with no verifier configured, a token-shaped
// bearer proves nothing and is denied (deny-closed) — it must not fall through
// to the Botanist-key comparison.
func TestTokenWithNilVerifierIsDenied(t *testing.T) {
	signer, err := core.LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, _ := signer.MintAccessToken("claude", 5*time.Minute, core.AccessTokenScope{})

	reached := false
	handler := withAPIKeyOrPollinatorAuth("botanist-key", nil, nil, false, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if reached || rec.Code != http.StatusUnauthorized {
		t.Fatalf("token with nil verifier: reached=%v status=%d, want 401", reached, rec.Code)
	}
}
