package receptors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// mintFixture stands up a signer and one issued root credential in a temp dir.
func mintFixture(t *testing.T) (*core.StemSigner, PollinatorCredentials, string) {
	t.Helper()
	dir := t.TempDir()
	signer, err := core.LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "laptop")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	credentials, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return signer, credentials, secret
}

func mint(t *testing.T, h *PollinatorTokenHandler, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/pollinator/token", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.HandleMint(rec, req)
	return rec
}

// TestMintFromValidRootReturnsAVerifiableToken is the happy path: a root
// credential is exchanged for a token that verifies to its Pollen.
func TestMintFromValidRootReturnsAVerifiableToken(t *testing.T) {
	signer, credentials, secret := mintFixture(t)
	h := NewPollinatorTokenHandler(signer, credentials)

	rec := mint(t, h, secret, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
	var resp struct {
		Token  string `json:"token"`
		Pollen string `json:"pollen"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !core.LooksLikeAccessToken(resp.Token) {
		t.Fatalf("minted value %q is not an access token", resp.Token)
	}
	if resp.Pollen != "claude" {
		t.Fatalf("pollen = %q, want claude", resp.Pollen)
	}
	claims, ok := signer.VerifyAccessToken(resp.Token)
	if !ok || claims.Pollen != "claude" {
		t.Fatalf("minted token did not verify to claude (ok=%v, pollen=%q)", ok, claims.Pollen)
	}
}

// TestMintHonoursShorterTTL: a custom under-cap ttl is signed into the token.
func TestMintHonoursShorterTTL(t *testing.T) {
	signer, credentials, secret := mintFixture(t)
	h := NewPollinatorTokenHandler(signer, credentials)

	before := time.Now().UTC()
	rec := mint(t, h, secret, `{"ttlSeconds":60}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	claims, ok := signer.VerifyAccessToken(resp.Token)
	if !ok {
		t.Fatal("minted token did not verify")
	}
	// Expiry should land near issued+60s, not the 15-minute default.
	want := before.Add(60 * time.Second)
	if claims.ExpiresAt.Before(want.Add(-2*time.Second)) || claims.ExpiresAt.After(want.Add(5*time.Second)) {
		t.Fatalf("expiresAt = %s, want ~%s (± a few seconds)", claims.ExpiresAt, want)
	}
}

// TestMintRejectsNegativeTTL: a negative ttl is a client error, not the default.
func TestMintRejectsNegativeTTL(t *testing.T) {
	signer, credentials, secret := mintFixture(t)
	h := NewPollinatorTokenHandler(signer, credentials)

	rec := mint(t, h, secret, `{"ttlSeconds":-300}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative ttl: status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestMintRejectsNonCredentialBearers: a token cannot mint another token, and an
// absent/plain bearer cannot mint at all.
func TestMintRejectsNonCredentialBearers(t *testing.T) {
	signer, credentials, secret := mintFixture(t)
	h := NewPollinatorTokenHandler(signer, credentials)

	// A previously minted access token must not be usable to mint again.
	first := mint(t, h, secret, "")
	var body struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &body)
	if rec := mint(t, h, body.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("minting with an access token: status = %d, want 401", rec.Code)
	}

	// No bearer at all.
	if rec := mint(t, h, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("minting with no bearer: status = %d, want 401", rec.Code)
	}

	// A plain (non-credential-shaped) bearer.
	if rec := mint(t, h, "some-botanist-key", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("minting with a plain bearer: status = %d, want 401", rec.Code)
	}
}

// TestMintRejectsRevokedRoot: revoking the root ends minting.
func TestMintRejectsRevokedRoot(t *testing.T) {
	dir := t.TempDir()
	signer, err := core.LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := core.RevokePollinatorCredentials(dir, "claude"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	credentials, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	h := NewPollinatorTokenHandler(signer, credentials)

	if rec := mint(t, h, secret, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("minting with a revoked root: status = %d, want 401", rec.Code)
	}
}

// TestMintRejectsTTLOverCap: a lifetime request above the cap is a bad request,
// not a silent clamp.
func TestMintRejectsTTLOverCap(t *testing.T) {
	signer, credentials, secret := mintFixture(t)
	h := NewPollinatorTokenHandler(signer, credentials)

	over := int(core.MaxAccessTokenTTL.Seconds()) + 60
	rec := mint(t, h, secret, `{"ttlSeconds":`+strconv.Itoa(over)+`}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-cap ttl: status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exceeds the maximum") {
		t.Fatalf("over-cap body = %q, want a stable maximum-ttl message", rec.Body.String())
	}
}

// TestMintRejectsNonPost: the mint route is POST-only.
func TestMintRejectsNonPost(t *testing.T) {
	signer, credentials, secret := mintFixture(t)
	h := NewPollinatorTokenHandler(signer, credentials)

	req := httptest.NewRequest(http.MethodGet, "/v1/pollinator/token", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	h.HandleMint(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status = %d, want 405", rec.Code)
	}
}

// TestPollenForResolvesAndDeniesTokens exercises the delegation-path derivation:
// a valid token resolves to its Pollen; an expired or forged one denies without
// falling back to the header claim.
func TestPollenForResolvesAndDeniesTokens(t *testing.T) {
	signer, _, _ := mintFixture(t)
	gate := &DelegationGate{Signer: signer}

	good, err := signer.MintAccessToken("claude", 0, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+good)
	// A header claim that must be ignored in favour of the proven token identity.
	req.Header.Set(PollenHeader, "someone-else")
	pollen, ok := gate.PollenFor(req)
	if !ok || pollen != "claude" {
		t.Fatalf("valid token: pollen=%q ok=%v, want claude/true", pollen, ok)
	}

	// A forged token (other key) must deny — ok=false — never the header claim.
	other, _ := core.LoadOrCreateStemSigner(t.TempDir())
	forged, _ := other.MintAccessToken("claude", 0, core.AccessTokenScope{})
	req = httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+forged)
	req.Header.Set(PollenHeader, "someone-else")
	if pollen, ok := gate.PollenFor(req); ok || pollen != "" {
		t.Fatalf("forged token: pollen=%q ok=%v, want \"\"/false (deny-closed, no header fallback)", pollen, ok)
	}
}
