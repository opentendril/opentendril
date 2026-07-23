package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// TestIsLoopbackBindHost classifies bind hosts for the off-host hardening
// signal: loopback keeps root credentials on data routes; everything else
// engages the access-token requirement.
func TestIsLoopbackBindHost(t *testing.T) {
	cases := []struct {
		host      string
		loopback  bool
		networked bool
	}{
		{"", true, false},
		{"127.0.0.1", true, false},
		{"::1", true, false},
		{"[::1]", true, false},
		{"localhost", true, false},
		{"LOCALHOST", true, false},
		{"0.0.0.0", false, true},
		{"::", false, true},
		{"192.168.1.10", false, true},
		{"example.com", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.host+"_", func(t *testing.T) {
			if got := isLoopbackBindHost(tc.host); got != tc.loopback {
				t.Fatalf("isLoopbackBindHost(%q) = %v, want %v", tc.host, got, tc.loopback)
			}
			if got := isNetworkedBindHost(tc.host); got != tc.networked {
				t.Fatalf("isNetworkedBindHost(%q) = %v, want %v", tc.host, got, tc.networked)
			}
		})
	}
}

// TestServeListenHostDefaultsToLoopback: with no HOST env, the bind host is
// 127.0.0.1 so a bare start never exposes the Stem beyond the local machine.
func TestServeListenHostDefaultsToLoopback(t *testing.T) {
	t.Setenv("HOST", "")
	if got := serveListenHost(); got != "127.0.0.1" {
		t.Fatalf("serveListenHost() = %q, want 127.0.0.1", got)
	}
	t.Setenv("HOST", "0.0.0.0")
	if got := serveListenHost(); got != "0.0.0.0" {
		t.Fatalf("serveListenHost() with HOST=0.0.0.0 = %q", got)
	}
}

// TestOffHostBindRefusesRootCredentialOnDataRoutes: networked=true refuses a
// durable Pollinator credential on the data-route gate and points the caller
// at mint. A valid access token and the Botanist api-key still succeed.
func TestOffHostBindRefusesRootCredentialOnDataRoutes(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	credentials, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	signer, err := core.LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, err := signer.MintAccessToken("claude", 5*time.Minute, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// Networked gate: root refused, token and api-key accepted.
	networkedHandler := withAPIKeyOrPollinatorAuth("botanist-key", credentials, signer, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	networkedHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("networked root credential: status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mint an access token") {
		t.Fatalf("networked root body = %q, want a mint-direction message", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	networkedHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("networked access token: status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer botanist-key")
	rec = httptest.NewRecorder()
	networkedHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("networked api-key: status = %d, want 200", rec.Code)
	}
}

// TestLoopbackBindStillAcceptsRootCredential: networked=false keeps the prior
// root-on-data-routes behaviour so personal local setups are unchanged.
func TestLoopbackBindStillAcceptsRootCredential(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	credentials, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	handler := withAPIKeyOrPollinatorAuth("botanist-key", credentials, nil, false, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback root credential: status = %d, want 200", rec.Code)
	}
}
