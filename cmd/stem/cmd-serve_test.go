package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/gateway"
)

// Issue #171 finding 1: the Stem must never serve its API unauthenticated.
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

// Issue #171 finding 2: /ws must require the same bearer key, accepting it
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
	t.Setenv("OPENTENDRIL_API_KEY", "env-key")
	dir := t.TempDir()

	key, generated, err := getOrCreateAPIKey(filepath.Join(dir, ".tendril"))
	if err != nil {
		t.Fatalf("getOrCreateAPIKey: %v", err)
	}
	if generated {
		t.Fatal("should not generate a key when OPENTENDRIL_API_KEY is set")
	}
	if key != "env-key" {
		t.Fatalf("key = %q, want env-key", key)
	}
}
