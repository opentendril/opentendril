package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// ---------------------------------------------------------------------------
// Port resolution
// ---------------------------------------------------------------------------

func TestLocalClientDefaultPort(t *testing.T) {
	t.Setenv("PORT", "")
	c := newLocalStemClient()
	if c.port != "8080" {
		t.Fatalf("default port = %q, want 8080", c.port)
	}
	if c.baseURL() != "http://localhost:8080" {
		t.Fatalf("baseURL = %q, want http://localhost:8080", c.baseURL())
	}
}

func TestLocalClientPORTEnvControlsPort(t *testing.T) {
	t.Setenv("PORT", "19999")
	c := newLocalStemClient()
	if c.port != "19999" {
		t.Fatalf("port = %q, want 19999", c.port)
	}
	if c.baseURL() != "http://localhost:19999" {
		t.Fatalf("baseURL = %q, want http://localhost:19999", c.baseURL())
	}
}

// ---------------------------------------------------------------------------
// Bearer resolution
// ---------------------------------------------------------------------------

func TestLocalBearerBotanistKeyTakesPrecedence(t *testing.T) {
	tmp := t.TempDir()
	// Write a persisted key that must NOT win.
	tendrilDir := filepath.Join(tmp, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tendrilDir, "api-key"), []byte("persisted-key\n"), 0o600); err != nil {
		t.Fatalf("write api-key: %v", err)
	}

	// Change the working directory so "./.tendril" resolves to tmp.
	t.Chdir(tmp)

	t.Setenv(EnvBotanistKey, "botanist-wins")
	bearer := resolveLocalBearer()
	if bearer != "botanist-wins" {
		t.Fatalf("bearer = %q, want botanist-wins", bearer)
	}
}

func TestLocalBearerPersistedKeyWhenBotanistAbsent(t *testing.T) {
	tmp := t.TempDir()
	tendrilDir := filepath.Join(tmp, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tendrilDir, "api-key"), []byte("persisted-key\n"), 0o600); err != nil {
		t.Fatalf("write api-key: %v", err)
	}

	t.Chdir(tmp)

	// BOTANIST_KEY must be absent.
	t.Setenv(EnvBotanistKey, "")
	bearer := resolveLocalBearer()
	if bearer != "persisted-key" {
		t.Fatalf("bearer = %q, want persisted-key", bearer)
	}
}

func TestLocalBearerEmptyWhenNeitherSourcePresent(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	t.Setenv(EnvBotanistKey, "")
	bearer := resolveLocalBearer()
	if bearer != "" {
		t.Fatalf("bearer = %q, want empty (no fabricated credential)", bearer)
	}
}

func TestLocalClientSendsNoAuthHeaderWhenBearerEmpty(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "")

	// No persisted key in current dir.
	c := &localStemClient{port: u.Port(), bearer: ""}
	resp, err := c.do(context.Background(), http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want empty (no fabricated credential)", gotAuth)
	}
}

// ---------------------------------------------------------------------------
// DispatchSeed (POST /v1/seeds/grow)
// ---------------------------------------------------------------------------

func TestDispatchSeedUsesCanonicalRoute(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: "test-key"}
	_, err := c.DispatchSeed(context.Background(), map[string]any{
		"substrate": "myrepo", "goal": "fix things", "verify": []any{"go", "test"},
	})
	if err != nil {
		t.Fatalf("DispatchSeed: %v", err)
	}
	if gotPath != "/v1/seeds/grow" {
		t.Fatalf("path = %q, want /v1/seeds/grow", gotPath)
	}
}

func TestDispatchSeedBodyContainsDetachedTrue(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, _ = c.DispatchSeed(context.Background(), map[string]any{
		"substrate": "myrepo", "goal": "fix things", "verify": []any{"go", "test"},
	})
	if v, ok := gotBody["detached"]; !ok || v != true {
		t.Fatalf("body detached = %v (%T), want true", v, v)
	}
}

func TestDispatchSeedDecodesHandlePhytomerIDStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"handle":"seed-abc","phytomerId":"tendril-xyz","status":"running"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	result, err := c.DispatchSeed(context.Background(), map[string]any{
		"substrate": "myrepo", "goal": "do work",
	})
	if err != nil {
		t.Fatalf("DispatchSeed: %v", err)
	}
	if result.Handle != "seed-abc" {
		t.Fatalf("Handle = %q, want seed-abc", result.Handle)
	}
	if result.PhytomerID != "tendril-xyz" {
		t.Fatalf("PhytomerID = %q, want tendril-xyz", result.PhytomerID)
	}
	if result.Status != "running" {
		t.Fatalf("Status = %q, want running", result.Status)
	}
}

func TestDispatchSeedNon2xxFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "seed dispatch forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.DispatchSeed(context.Background(), map[string]any{
		"substrate": "myrepo", "goal": "do work",
	})
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v, want 403 mention", err)
	}
}

func TestDispatchSeedMalformedJSONFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`not valid json`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.DispatchSeed(context.Background(), map[string]any{"substrate": "r", "goal": "g"})
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}

// TestDispatchSeedHTTPRejectionIsTyped verifies that a non-2xx response from
// the Stem daemon is returned as a *stemHTTPError carrying the status code,
// not as a transport/unreachable error.
func TestDispatchSeedHTTPRejectionIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota exceeded", http.StatusTooManyRequests)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.DispatchSeed(context.Background(), map[string]any{"substrate": "r", "goal": "g"})
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	var httpErr *stemHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *stemHTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d, want %d", httpErr.StatusCode, http.StatusTooManyRequests)
	}
	if !strings.Contains(httpErr.Body, "quota exceeded") {
		t.Fatalf("Body = %q, want quota exceeded mention", httpErr.Body)
	}
}

// TestDispatchSeedTransportFailureIsNotTypedHTTP verifies that a network-level
// failure (unreachable daemon) is NOT returned as *stemHTTPError so callers
// can distinguish transport from HTTP rejection without substring matching.
func TestDispatchSeedTransportFailureIsNotTypedHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &localStemClient{port: "65533", bearer: ""}
	_, err := c.DispatchSeed(ctx, map[string]any{"substrate": "r", "goal": "g"})
	if err == nil {
		t.Fatal("expected error connecting to port 65533, got nil")
	}
	var httpErr *stemHTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("transport failure returned *stemHTTPError; must not: %v", err)
	}
}

func TestDispatchSeedSendsBearer(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: "botanist-secret"}
	_, err := c.DispatchSeed(context.Background(), map[string]any{"substrate": "r", "goal": "g"})
	if err != nil {
		t.Fatalf("DispatchSeed: %v", err)
	}
	if gotAuth != "Bearer botanist-secret" {
		t.Fatalf("Authorization = %q, want Bearer botanist-secret", gotAuth)
	}
}

// ---------------------------------------------------------------------------
// CollectSeed (GET /v1/seeds/runs/{handle})
// ---------------------------------------------------------------------------

func TestCollectSeedDecodesExistingFruitFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status":"satisfied","iterations":2,"phytomerId":"tendril-abc",
			"branch":"staging/ai-feature","commit":"deadbeef",
			"diff":"--- a/foo.go\n+++ b/foo.go","logs":"build output"
		}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	result, err := c.CollectSeed(context.Background(), "some-handle")
	if err != nil {
		t.Fatalf("CollectSeed: %v", err)
	}
	if result.Status != "satisfied" {
		t.Fatalf("Status = %q, want satisfied", result.Status)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
	if result.PhytomerID != "tendril-abc" {
		t.Fatalf("PhytomerID = %q, want tendril-abc", result.PhytomerID)
	}
	if result.Branch != "staging/ai-feature" {
		t.Fatalf("Branch = %q", result.Branch)
	}
	if result.Commit != "deadbeef" {
		t.Fatalf("Commit = %q", result.Commit)
	}
	if !strings.Contains(result.Diff, "foo.go") {
		t.Fatalf("Diff = %q, expected foo.go", result.Diff)
	}
	if result.Logs != "build output" {
		t.Fatalf("Logs = %q", result.Logs)
	}
}

func TestCollectSeedNotFoundFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.CollectSeed(context.Background(), "missing-handle")
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}

func TestCollectSeedNon2xxFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.CollectSeed(context.Background(), "h")
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want 500 mention", err)
	}
}

func TestCollectSeedMalformedJSONFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{bad json`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.CollectSeed(context.Background(), "h")
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}

// TestCollectSeedNotFoundIsNotTypedHTTP verifies that a 404 response is
// returned as a plain descriptive error (not *stemHTTPError), matching the
// CLI's "no seed run for handle" output path.
func TestCollectSeedNotFoundIsNotTypedHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.CollectSeed(context.Background(), "missing-handle")
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	var httpErr *stemHTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("404 should NOT be *stemHTTPError (it becomes a plain no-handle error); got %v", err)
	}
	if !strings.Contains(err.Error(), "no seed run for handle") {
		t.Fatalf("error = %q, want 'no seed run for handle' mention", err.Error())
	}
}

// TestCollectSeedHTTPRejectionIsTyped verifies that a non-2xx, non-404
// response is returned as *stemHTTPError with the exact status code.
func TestCollectSeedHTTPRejectionIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.CollectSeed(context.Background(), "h")
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	var httpErr *stemHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *stemHTTPError for 500, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("StatusCode = %d, want 500", httpErr.StatusCode)
	}
}

// TestCollectSeedTransportFailureIsNotTypedHTTP verifies that an unreachable
// daemon does not surface as *stemHTTPError.
func TestCollectSeedTransportFailureIsNotTypedHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &localStemClient{port: "65533", bearer: ""}
	_, err := c.CollectSeed(ctx, "some-handle")
	if err == nil {
		t.Fatal("expected error connecting to port 65533, got nil")
	}
	var httpErr *stemHTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("transport failure returned *stemHTTPError; must not: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ContinuePhytomer (POST /v1/phytomers/{id}/continue)
// ---------------------------------------------------------------------------

func TestContinuePhytomerUsesExactPhytomerID(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continuationId":"c1","sessionId":"tendril-exact","sequence":1,"deliveryState":"pending","idempotencyKey":"k1"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.ContinuePhytomer(context.Background(), "tendril-exact", "do it", "k1")
	if err != nil {
		t.Fatalf("ContinuePhytomer: %v", err)
	}
	if gotPath != "/v1/phytomers/tendril-exact/continue" {
		t.Fatalf("path = %q, want /v1/phytomers/tendril-exact/continue", gotPath)
	}
}

func TestContinuePhytomerPreservesExactIdempotencyKey(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continuationId":"c1","sessionId":"s1","sequence":1,"deliveryState":"pending","idempotencyKey":"exact-key-xyz"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.ContinuePhytomer(context.Background(), "s1", "some intent", "exact-key-xyz")
	if err != nil {
		t.Fatalf("ContinuePhytomer: %v", err)
	}
	if gotBody["idempotencyKey"] != "exact-key-xyz" {
		t.Fatalf("idempotencyKey = %v, want exact-key-xyz", gotBody["idempotencyKey"])
	}
	if gotBody["intent"] != "some intent" {
		t.Fatalf("intent = %v, want 'some intent'", gotBody["intent"])
	}
}

func TestContinuePhytomerNon2xxFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "phytomer not continuation-eligible", http.StatusConflict)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.ContinuePhytomer(context.Background(), "s1", "intent", "k1")
	if err == nil {
		t.Fatal("expected error on 409, got nil")
	}
	if !strings.Contains(err.Error(), "409") && !strings.Contains(err.Error(), "not continuation-eligible") {
		t.Fatalf("error = %v, want 409 or descriptive", err)
	}
}

func TestContinuePhytomerDaemonUnreachableWrapsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &localStemClient{port: "65533", bearer: ""}
	_, err := c.ContinuePhytomer(ctx, "s1", "intent", "k1")
	if err == nil {
		t.Fatal("expected error connecting to port 65533, got nil")
	}
	if !strings.Contains(err.Error(), "unreachable") && !strings.Contains(err.Error(), "connection refused") && !strings.Contains(err.Error(), "connect") {
		t.Fatalf("error = %v, want network error", err)
	}
}

func TestContinuePhytomerMalformedJSONFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.ContinuePhytomer(context.Background(), "s1", "intent", "k1")
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}

// TestContinuePhytomerHTTPRejectionIsTyped verifies that a non-2xx response
// from the Stem daemon is returned as *stemHTTPError so the caller can
// distinguish it from a transport failure without substring matching.
func TestContinuePhytomerHTTPRejectionIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "phytomer is locked", http.StatusConflict)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.ContinuePhytomer(context.Background(), "s1", "intent", "k1")
	if err == nil {
		t.Fatal("expected error on 409, got nil")
	}
	var httpErr *stemHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *stemHTTPError for 409, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("StatusCode = %d, want 409", httpErr.StatusCode)
	}
	if !strings.Contains(httpErr.Body, "phytomer is locked") {
		t.Fatalf("Body = %q, want phytomer is locked mention", httpErr.Body)
	}
}

// TestContinuePhytomerTransportFailureIsNotTypedHTTP verifies that an
// unreachable daemon is NOT returned as *stemHTTPError.
func TestContinuePhytomerTransportFailureIsNotTypedHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &localStemClient{port: "65533", bearer: ""}
	_, err := c.ContinuePhytomer(ctx, "s1", "intent", "k1")
	if err == nil {
		t.Fatal("expected error connecting to port 65533, got nil")
	}
	var httpErr *stemHTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("transport failure returned *stemHTTPError; must not: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WatchPhytomer / SSE decoding
// ---------------------------------------------------------------------------

func makeSSEBody(events ...struct{ name, data string }) string {
	var sb strings.Builder
	for _, ev := range events {
		if ev.name != "" {
			sb.WriteString("event: ")
			sb.WriteString(ev.name)
			sb.WriteString("\n")
		}
		sb.WriteString("data: ")
		sb.WriteString(ev.data)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func TestWatchPhytomerParsesObservationEvents(t *testing.T) {
	obs1, _ := json.Marshal(core.PhytomerObservation{
		Handle: "seed-h1", PhytomerID: "tendril-1", Status: "running", Iterations: 1,
	})
	obs2, _ := json.Marshal(core.PhytomerObservation{
		Handle: "seed-h1", PhytomerID: "tendril-1", Status: "satisfied", Iterations: 2,
		Branch: "staging/ai-slice", Commit: "abc123",
	})

	body := makeSSEBody(
		struct{ name, data string }{"observation", string(obs1)},
		struct{ name, data string }{"observation", string(obs2)},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	var received []core.PhytomerObservation
	err := c.WatchPhytomer(context.Background(), "tendril-1", func(obs core.PhytomerObservation) error {
		received = append(received, obs)
		return nil
	})
	if err != nil {
		t.Fatalf("WatchPhytomer: %v", err)
	}
	if len(received) != 2 {
		t.Fatalf("received %d observations, want 2", len(received))
	}
	if received[0].Status != "running" {
		t.Fatalf("obs[0].Status = %q, want running", received[0].Status)
	}
	if received[1].Status != "satisfied" {
		t.Fatalf("obs[1].Status = %q, want satisfied", received[1].Status)
	}
	if received[1].Branch != "staging/ai-slice" {
		t.Fatalf("obs[1].Branch = %q, want staging/ai-slice", received[1].Branch)
	}
	if received[1].Commit != "abc123" {
		t.Fatalf("obs[1].Commit = %q, want abc123", received[1].Commit)
	}
}

func TestWatchPhytomerServerErrorEventFails(t *testing.T) {
	body := makeSSEBody(
		struct{ name, data string }{"error", `{"error":"observation closed"}`},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	err := c.WatchPhytomer(context.Background(), "tendril-1", func(core.PhytomerObservation) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error on SSE error event, got nil")
	}
	if !strings.Contains(err.Error(), "server") && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("error = %v, want server-closed description", err)
	}
}

func TestWatchPhytomerMalformedSSEJSONFails(t *testing.T) {
	body := "event: observation\ndata: {not valid json}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	err := c.WatchPhytomer(context.Background(), "tendril-1", func(core.PhytomerObservation) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error on malformed SSE JSON, got nil")
	}
}

func TestWatchPhytomerNon2xxFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	err := c.WatchPhytomer(context.Background(), "tendril-1", func(core.PhytomerObservation) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want 401 mention", err)
	}
}

func TestWatchPhytomerUnknownEventTypesAreSkipped(t *testing.T) {
	obs, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h1", PhytomerID: "tendril-1", Status: "running",
	})
	body := makeSSEBody(
		struct{ name, data string }{"heartbeat", `{}`},
		struct{ name, data string }{"observation", string(obs)},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	var received []core.PhytomerObservation
	err := c.WatchPhytomer(context.Background(), "tendril-1", func(obs core.PhytomerObservation) error {
		received = append(received, obs)
		return nil
	})
	if err != nil {
		t.Fatalf("WatchPhytomer: %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("received %d observations, want 1 (heartbeat must be skipped)", len(received))
	}
}

func TestWatchPhytomerCallbackErrorStopsStream(t *testing.T) {
	obs, _ := json.Marshal(core.PhytomerObservation{Status: "running"})
	body := makeSSEBody(
		struct{ name, data string }{"observation", string(obs)},
		struct{ name, data string }{"observation", string(obs)}, // second event: never delivered
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	count := 0
	c := &localStemClient{port: u.Port(), bearer: ""}
	err := c.WatchPhytomer(context.Background(), "tendril-1", func(core.PhytomerObservation) error {
		count++
		return fmt.Errorf("stop after first")
	})
	if err == nil {
		t.Fatal("expected callback error to propagate, got nil")
	}
	if count != 1 {
		t.Fatalf("callback called %d times, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// decodeSSEObservations unit tests
// ---------------------------------------------------------------------------

func TestDecodeSSEObservationsEmptyBodyIsOK(t *testing.T) {
	err := decodeSSEObservations(strings.NewReader(""), func(core.PhytomerObservation) error {
		t.Fatal("callback called on empty body")
		return nil
	})
	if err != nil {
		t.Fatalf("decodeSSEObservations empty: %v", err)
	}
}

func TestDecodeSSEObservationsMultipleEvents(t *testing.T) {
	obs1, _ := json.Marshal(core.PhytomerObservation{Status: "running", Iterations: 1})
	obs2, _ := json.Marshal(core.PhytomerObservation{Status: "satisfied", Iterations: 3})
	body := fmt.Sprintf("event: observation\ndata: %s\n\nevent: observation\ndata: %s\n\n", obs1, obs2)

	var got []core.PhytomerObservation
	err := decodeSSEObservations(strings.NewReader(body), func(obs core.PhytomerObservation) error {
		got = append(got, obs)
		return nil
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Status != "running" || got[1].Status != "satisfied" {
		t.Fatalf("got %+v", got)
	}
}

func TestDecodeSSEObservationsErrorEventFails(t *testing.T) {
	body := "event: error\ndata: {\"error\":\"ownership conflict\"}\n\n"
	err := decodeSSEObservations(strings.NewReader(body), func(core.PhytomerObservation) error {
		t.Fatal("callback called on error event")
		return nil
	})
	if err == nil {
		t.Fatal("expected error event to produce an error")
	}
}

// ---------------------------------------------------------------------------
// Integration: submitPhytomerContinue still uses correct behavior
// ---------------------------------------------------------------------------

func TestSubmitPhytomerContinueStillPreservesIdempotencyKey(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continuationId":"c1","sessionId":"tendril-1","sequence":1,"deliveryState":"pending","idempotencyKey":"retry-specific"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "bot-key")

	result, err := submitPhytomerContinue(context.Background(), map[string]any{
		"sessionId":      "tendril-1",
		"intent":         "do next iteration",
		"idempotencyKey": "retry-specific",
	})
	if err != nil {
		t.Fatalf("submitPhytomerContinue: %v", err)
	}
	if gotBody["idempotencyKey"] != "retry-specific" {
		t.Fatalf("idempotencyKey = %v, want retry-specific", gotBody["idempotencyKey"])
	}
	if result.ContinuationID != "c1" {
		t.Fatalf("ContinuationID = %q, want c1", result.ContinuationID)
	}
}

// ---------------------------------------------------------------------------
// Integration: submitSeedAsync uses canonical /v1/seeds/grow endpoint
// ---------------------------------------------------------------------------

func TestSubmitSeedAsyncUsesCanonicalGrowEndpoint(t *testing.T) {
	var (
		gotPath string
		gotBody map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	result, err := c.DispatchSeed(context.Background(), map[string]any{
		"substrate": "myrepo",
		"goal":      "fix the bug",
		"verify":    []any{"go", "test", "./..."},
	})
	if err != nil {
		t.Fatalf("DispatchSeed: %v", err)
	}

	// Must use canonical /v1/seeds/grow not /v1/seeds/grow/async.
	if gotPath != "/v1/seeds/grow" {
		t.Fatalf("path = %q, must be /v1/seeds/grow (not /v1/seeds/grow/async)", gotPath)
	}
	if gotBody["detached"] != true {
		t.Fatalf("detached = %v, want true", gotBody["detached"])
	}
	if result.Handle != "h1" {
		t.Fatalf("Handle = %q, want h1", result.Handle)
	}
}

// ---------------------------------------------------------------------------
// Streaming SSE with a buffered server (pipe-based)
// ---------------------------------------------------------------------------

func TestWatchPhytomerReadsLiveStreamFromPipe(t *testing.T) {
	// Use a pipe to simulate a live SSE stream that closes after a few events.
	pr, pw := io.Pipe()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			fmt.Fprintln(w, scanner.Text())
			flusher.Flush()
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	obs, _ := json.Marshal(core.PhytomerObservation{Status: "satisfied", Iterations: 5})
	go func() {
		// Write one observation event then close the pipe.
		fmt.Fprintf(pw, "event: observation\ndata: %s\n\n", obs)
		pw.Close()
	}()

	c := &localStemClient{port: u.Port(), bearer: ""}
	var received []core.PhytomerObservation
	err := c.WatchPhytomer(context.Background(), "tendril-1", func(o core.PhytomerObservation) error {
		received = append(received, o)
		return nil
	})
	if err != nil {
		t.Fatalf("WatchPhytomer live: %v", err)
	}
	if len(received) != 1 || received[0].Status != "satisfied" {
		t.Fatalf("received = %+v", received)
	}
}
