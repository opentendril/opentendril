package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/gateway"
	"github.com/opentendril/opentendril/cmd/stem/internal/scheduler"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// Issue finding 1: the Stem must never serve its API unauthenticated.
func TestWithAPIKeyAuthNeverFailsOpen(t *testing.T) {
	called := false
	handler := withAPIKeyAuth("", func(w http.ResponseWriter, r *http.Request) { called = true })

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))

	if called {
		t.Fatal("withAPIKeyAuth called next() with an empty configured key; must fail closed")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestWithAPIKeyAuthRequiresMatchingBearer(t *testing.T) {
	handler := withAPIKeyAuth("secret-key", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong key", "Bearer wrong", http.StatusUnauthorized},
		{"correct key", "Bearer secret-key", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// A credential issued under the superseded "otp_" prefix must be refused
// outright once the prefix changes.
//
// This is the one behaviour worth pinning about that rename. The prefix is the
// discriminator that routes a presented bearer to credential resolution, so an
// old value no longer looks credential-shaped and falls through to the
// Botanist-key comparison instead. It must fail there. The forbidden outcome is
// that it is accepted — either by matching the Botanist key or by being treated
// as an ordinary unauthenticated request that proceeds anyway.
func TestSupersededCredentialPrefixIsRefused(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	credentials, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	reached := false
	handler := withAPIKeyOrPollinatorAuth("botanist-key", credentials, nil, false, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	// The same secret carrying the superseded prefix: what a Pollinator issued
	// before the rename would still be presenting.
	superseded := "otp_" + strings.TrimPrefix(secret, "tendril_")

	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+superseded)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if reached {
		t.Fatal("a credential with the superseded prefix reached the handler")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d for a superseded-prefix credential", rec.Code, http.StatusUnauthorized)
	}

	// The current prefix still works, so the refusal above is about the prefix
	// rather than a broken fixture.
	reached = false
	req = httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec = httptest.NewRecorder()
	handler(rec, req)

	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("a current credential was refused: reached=%v status=%d", reached, rec.Code)
	}
}

// Issue finding 2: /ws must require the same bearer key, accepting it
// either via Authorization header (non-browser clients) or a `key` query
// parameter (the browser cannot set headers on a WebSocket upgrade).
func TestWithWebSocketAuth(t *testing.T) {
	bus := eventbus.New()
	handler := withWebSocketAuth("secret-key", gateway.HandleWebSocket(bus))
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	t.Run("rejects unauthenticated upgrade", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/ws")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
	})

	t.Run("rejects wrong query key", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/ws?key=wrong")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
	})

	t.Run("accepts matching query key", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/ws?key=secret-key")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		// The gorilla upgrader rejects a plain GET with 400 (not a WebSocket
		// handshake) once auth lets it through — the point under test is that
		// it's no longer 401.
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("status = %d, want anything but 401 once authenticated", resp.StatusCode)
		}
	})

	t.Run("accepts Authorization header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/ws", nil)
		req.Header.Set("Authorization", "Bearer secret-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("status = %d, want anything but 401 once authenticated", resp.StatusCode)
		}
	})
}

func TestGetOrCreateAPIKeyPersistsAndReuses(t *testing.T) {
	dir := t.TempDir()
	tendrilDir := filepath.Join(dir, ".tendril")

	key1, generated1, err := getOrCreateAPIKey(tendrilDir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !generated1 {
		t.Fatal("expected first call to generate a new key")
	}
	if key1 == "" {
		t.Fatal("generated key is empty")
	}

	key2, generated2, err := getOrCreateAPIKey(tendrilDir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if generated2 {
		t.Fatal("expected second call to reuse the persisted key, not regenerate")
	}
	if key2 != key1 {
		t.Fatalf("key changed across calls: %q != %q", key1, key2)
	}

	info, err := os.Stat(apiKeyFilePath(tendrilDir))
	if err != nil {
		t.Fatalf("stat persisted key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestGetOrCreateAPIKeyPrefersEnv(t *testing.T) {
	t.Setenv(EnvStemAPIKey, "env-key")
	dir := t.TempDir()

	key, generated, err := getOrCreateAPIKey(filepath.Join(dir, ".tendril"))
	if err != nil {
		t.Fatalf("getOrCreateAPIKey: %v", err)
	}
	if generated {
		t.Fatalf("should not generate a key when %s is set", EnvStemAPIKey)
	}
	if key != "env-key" {
		t.Fatalf("key = %q, want env-key", key)
	}
}

// Issue slice 3: a scheduler-originated sprout run must be attributable
// in history. The firer stamps origin "scheduler" into the governed sprout.grow
// input; the Core carries it onto the resolved SproutSpec, which is exactly
// the field the execution port records as historydb.SproutRun.Origin
// (cmdsprout.go). Asserting on the spec therefore pins the whole flow this
// side of the terrarium.
func TestScheduledRunFirerStampsSchedulerOrigin(t *testing.T) {
	ctx := context.Background()
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	var got core.SproutSpec
	svc := core.NewService(manager).WithSprout(core.SproutOperations{
		Run: func(_ context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			got = spec
			return core.SproutRunReport{Output: "matured", Outcome: "complete"}, nil
		},
	})

	// A nonexistent triggers dir means no Hormonal Triggers are configured,
	// so the fire proceeds.
	firer := scheduledRunFirer(svc, manager, filepath.Join(t.TempDir(), "no-triggers"))
	entry := scheduler.Entry{
		Cron: "0 3 * * *",
		Sprout: &scheduler.SproutSpec{
			Transcript: "nightly upkeep",
			Substrate:  "/workspaces/core",
		},
	}
	if err := firer(ctx, "nightly", entry); err != nil {
		t.Fatalf("scheduled fire: %v", err)
	}

	if got.Origin != "scheduler" {
		t.Fatalf("scheduled sprout run origin = %q, want %q", got.Origin, "scheduler")
	}
	// The dedicated session initiated for the run carries the same origin, so
	// the session row and the run row agree on which surface grew it.
	if got.SessionID == "" {
		t.Fatal("scheduled sprout run must be bound to a session")
	}
	sess, ok := manager.Get(got.SessionID)
	if !ok {
		t.Fatalf("session %q not found", got.SessionID)
	}
	if sess.Origin != "scheduler" {
		t.Fatalf("scheduled run session origin = %q, want %q", sess.Origin, "scheduler")
	}
}

// The Stem's bearer key must be its own secret, never a provider's. A provider
// value may be shared and reaches every Terrarium; a bearer key grants unscoped
// access.
func TestOtherProviderKeysAreNotTheStemBearerKey(t *testing.T) {
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")
	os.Unsetenv(EnvStemAPIKey)
	os.Unsetenv("ADMIN_TOKEN")

	if key := resolveServeAPIKey(); key != "" {
		t.Fatalf("resolveServeAPIKey returned %q from a variable that is not the bearer key", key)
	}
}

func TestStemBearerKeyComesFromItsOwnVariable(t *testing.T) {
	t.Setenv(EnvStemAPIKey, "a-real-bearer-key")
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")

	if key := resolveServeAPIKey(); key != "a-real-bearer-key" {
		t.Fatalf("resolveServeAPIKey = %q, want the value of %s", key, EnvStemAPIKey)
	}
}

// The end of the chain: the trial constant must not authenticate.
func TestProviderValueDoesNotAuthenticate(t *testing.T) {
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")
	os.Unsetenv(EnvStemAPIKey)
	os.Unsetenv("ADMIN_TOKEN")

	dir := t.TempDir()
	apiKey, _, err := getOrCreateAPIKey(filepath.Join(dir, ".tendril"))
	if err != nil {
		t.Fatalf("getOrCreateAPIKey: %v", err)
	}
	if apiKey == "a-shared-provider-value" {
		t.Fatal("a provider value became the Stem's bearer key")
	}

	reached := false
	handler := withAPIKeyAuth(apiKey, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer a-shared-provider-value")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if reached || rec.Code != http.StatusUnauthorized {
		t.Fatalf("a provider value authenticated: reached=%v status=%d", reached, rec.Code)
	}
}
