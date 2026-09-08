package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// =============================================================================
// parseChatArgs — argument parsing
// =============================================================================

func TestParseChatArgsBareDoubleDashRequired(t *testing.T) {
	_, err := parseChatArgs([]string{"go", "test", "./..."})
	if err == nil {
		t.Fatal("expected error when -- is absent, got nil")
	}
	if !strings.Contains(err.Error(), "--") {
		t.Fatalf("error = %v, want mention of --", err)
	}
}

func TestParseChatArgsEmptyVerifierFails(t *testing.T) {
	_, err := parseChatArgs([]string{"--"})
	if err == nil {
		t.Fatal("expected error when verifier is empty, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("error = %v, want mention of non-empty", err)
	}
}

func TestParseChatArgsVerifierArgvPreservedExactly(t *testing.T) {
	args := []string{"--", "go", "test", "-count=1", "./cmd/..."}
	out, err := parseChatArgs(args)
	if err != nil {
		t.Fatalf("parseChatArgs: %v", err)
	}
	want := []string{"go", "test", "-count=1", "./cmd/..."}
	if len(out.verifyArgv) != len(want) {
		t.Fatalf("verifyArgv = %v, want %v", out.verifyArgv, want)
	}
	for i, w := range want {
		if out.verifyArgv[i] != w {
			t.Fatalf("verifyArgv[%d] = %q, want %q", i, out.verifyArgv[i], w)
		}
	}
}

func TestParseChatArgsSubstrate(t *testing.T) {
	out, err := parseChatArgs([]string{"--substrate", "myrepo", "--", "npm", "test"})
	if err != nil {
		t.Fatalf("parseChatArgs: %v", err)
	}
	if out.substrate != "myrepo" {
		t.Fatalf("substrate = %q, want myrepo", out.substrate)
	}
}

func TestParseChatArgsMaxIterations(t *testing.T) {
	out, err := parseChatArgs([]string{"--max-iterations", "5", "--", "go", "test", "./..."})
	if err != nil {
		t.Fatalf("parseChatArgs: %v", err)
	}
	if out.maxIterations != 5 {
		t.Fatalf("maxIterations = %d, want 5", out.maxIterations)
	}
}

func TestParseChatArgsTimeout(t *testing.T) {
	out, err := parseChatArgs([]string{"--timeout", "300", "--", "go", "test", "./..."})
	if err != nil {
		t.Fatalf("parseChatArgs: %v", err)
	}
	if out.timeout != 300 {
		t.Fatalf("timeout = %d, want 300", out.timeout)
	}
}

func TestParseChatArgsMalformedMaxIterationsFails(t *testing.T) {
	_, err := parseChatArgs([]string{"--max-iterations", "abc", "--", "go", "test", "./..."})
	if err == nil {
		t.Fatal("expected error for non-integer --max-iterations, got nil")
	}
}

func TestParseChatArgsMalformedTimeoutFails(t *testing.T) {
	_, err := parseChatArgs([]string{"--timeout", "not-a-number", "--", "go", "test", "./..."})
	if err == nil {
		t.Fatal("expected error for non-integer --timeout, got nil")
	}
}

func TestParseChatArgsUnknownFlagFails(t *testing.T) {
	_, err := parseChatArgs([]string{"--unknown", "val", "--", "go", "test", "./..."})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
	if !strings.Contains(err.Error(), "--unknown") {
		t.Fatalf("error = %v, want mention of --unknown", err)
	}
}

func TestParseChatArgsWsFails(t *testing.T) {
	_, err := parseChatArgs([]string{"--ws", "--", "go", "test", "./..."})
	if err == nil {
		t.Fatal("expected error for --ws flag, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ws") {
		t.Fatalf("error = %v, want ws mention", err)
	}
}

func TestParseChatArgsWsDoesNotDispatch(t *testing.T) {
	var dispatched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched = true
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
	}))
	defer server.Close()

	// --ws must fail before any network call.
	_, err := parseChatArgs([]string{"--ws", "--", "go", "test", "./..."})
	if err == nil {
		t.Fatal("expected error for --ws, got nil")
	}
	if dispatched {
		t.Fatal("--ws caused a network dispatch; it must fail locally")
	}
}

// =============================================================================
// Substrate resolution
// =============================================================================

func TestResolveDirectChatSubstrateZeroFails(t *testing.T) {
	// Empty config → no substrates.
	tmp := t.TempDir()
	t.Chdir(tmp)

	_, err := resolveDirectChatSubstrate("")
	if err == nil {
		t.Fatal("expected error when no substrates configured, got nil")
	}
	if !strings.Contains(err.Error(), "tendril setup substrate") {
		t.Fatalf("error = %v, want setup guidance", err)
	}
}

func TestResolveDirectChatSubstrateExactlyOneResolves(t *testing.T) {
	config := &conductor.SubstratesConfig{
		Substrates: map[string]conductor.SubstrateSpec{
			"only-repo": {},
		},
	}
	names := sortedSubstrateNames(config)
	if len(names) != 1 || names[0] != "only-repo" {
		t.Fatalf("sortedSubstrateNames = %v, want [only-repo]", names)
	}
}

func TestResolveDirectChatSubstrateMultipleFails(t *testing.T) {
	// Write a two-substrate config so LoadSubstratesConfig returns multiple.
	tmp := t.TempDir()
	t.Chdir(tmp)
	if err := os.WriteFile("substrates.yaml", []byte("substrates:\n  alpha: {}\n  beta: {}\n"), 0o644); err != nil {
		t.Fatalf("write substrates.yaml: %v", err)
	}
	_, err := resolveDirectChatSubstrate("")
	if err == nil {
		t.Fatal("expected error when multiple substrates configured, got nil")
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("error = %v, want both substrate names listed", err)
	}
}

func TestResolveDirectChatSubstrateExplicitDoesNotRequireImplicit(t *testing.T) {
	// Explicit --substrate value bypasses the config load entirely.
	tmp := t.TempDir()
	t.Chdir(tmp)
	// No substrates.yaml written.
	name, err := resolveDirectChatSubstrate("explicit-name")
	if err != nil {
		t.Fatalf("unexpected error with explicit substrate: %v", err)
	}
	if name != "explicit-name" {
		t.Fatalf("name = %q, want explicit-name", name)
	}
}

func TestSortedSubstrateNamesDeterministic(t *testing.T) {
	config := &conductor.SubstratesConfig{
		Substrates: map[string]conductor.SubstrateSpec{
			"zz": {}, "aa": {}, "mm": {},
		},
	}
	names := sortedSubstrateNames(config)
	want := []string{"aa", "mm", "zz"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], w)
		}
	}
}

// =============================================================================
// Initial task / Seed dispatch
// =============================================================================

// capturedSeedRequest records the request body + path seen by a test server.
type capturedSeedRequest struct {
	path string
	body map[string]any
}

func newSeedAcceptServer(t *testing.T) (*httptest.Server, *capturedSeedRequest, []string) {
	t.Helper()
	var mu sync.Mutex
	capture := &capturedSeedRequest{}
	var paths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		capture.path = r.URL.Path
		capture.body = body
		mu.Unlock()

		switch r.URL.Path {
		case "/v1/seeds/grow":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"test-handle","phytomerId":"tendril-test","status":"running"}`))
		default:
			http.NotFound(w, r)
		}
	}))

	return server, capture, paths
}

func TestFirstTaskCallsSeedsGrow(t *testing.T) {
	server, capture, _ := newSeedAcceptServer(t)
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "test-key")

	// Use a done channel to stop the watch immediately.
	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	_, err := sess.dispatchSeed(context.Background(), "fix the bug")
	if err != nil {
		t.Fatalf("dispatchSeed: %v", err)
	}
	if capture.path != "/v1/seeds/grow" {
		t.Fatalf("path = %q, want /v1/seeds/grow", capture.path)
	}
}

func TestFirstTaskBodyContainsDetachedTrue(t *testing.T) {
	server, capture, _ := newSeedAcceptServer(t)
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, _ = c.DispatchSeed(context.Background(), map[string]any{
		"substrate": "myrepo", "goal": "fix things", "verify": []any{"go", "test"},
	})
	if capture.body["detached"] != true {
		t.Fatalf("detached = %v, want true", capture.body["detached"])
	}
}

func TestFirstTaskBodyContainsOriginCLI(t *testing.T) {
	server, capture, _ := newSeedAcceptServer(t)
	defer server.Close()
	u, _ := url.Parse(server.URL)

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	_, _ = sess.dispatchSeed(context.Background(), "do work")
	if capture.body["origin"] != "cli" {
		t.Fatalf("origin = %v, want cli", capture.body["origin"])
	}
}

func TestFirstTaskGoalPreservedExactly(t *testing.T) {
	server, capture, _ := newSeedAcceptServer(t)
	defer server.Close()
	u, _ := url.Parse(server.URL)

	goal := "implement feature X with exact spacing"
	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	_, _ = sess.dispatchSeed(context.Background(), goal)
	if capture.body["goal"] != goal {
		t.Fatalf("goal = %v, want %q", capture.body["goal"], goal)
	}
}

func TestFirstTaskVerifyArgvPreservedExactly(t *testing.T) {
	server, capture, _ := newSeedAcceptServer(t)
	defer server.Close()
	u, _ := url.Parse(server.URL)

	verify := []string{"npm", "run", "test", "--", "--coverage"}
	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: verify,
	}
	_, _ = sess.dispatchSeed(context.Background(), "do work")
	// JSON arrays decode as []any from json.Unmarshal.
	got, ok := capture.body["verify"].([]any)
	if !ok {
		t.Fatalf("verify is %T, want []any", capture.body["verify"])
	}
	if len(got) != len(verify) {
		t.Fatalf("verify = %v, want %v", got, verify)
	}
	for i, w := range verify {
		if got[i] != w {
			t.Fatalf("verify[%d] = %v, want %q", i, got[i], w)
		}
	}
}

func TestFirstTaskReturnedHandleAndPhytomerBecomeActive(t *testing.T) {
	server, _, _ := newSeedAcceptServer(t)
	defer server.Close()
	u, _ := url.Parse(server.URL)

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	result, err := sess.dispatchSeed(context.Background(), "do work")
	if err != nil {
		t.Fatalf("dispatchSeed: %v", err)
	}
	if result.Handle != "test-handle" {
		t.Fatalf("Handle = %q, want test-handle", result.Handle)
	}
	if result.PhytomerID != "tendril-test" {
		t.Fatalf("PhytomerID = %q, want tendril-test", result.PhytomerID)
	}
}

func TestFirstTaskNeverCallsChatCompletions(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			called = true
			http.Error(w, "forbidden in direct lifecycle", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/v1/seeds/grow" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	_, _ = sess.dispatchSeed(context.Background(), "do work")
	if called {
		t.Fatal("/v1/chat/completions was called; it must never be called in direct lifecycle")
	}
}

func TestFirstTaskNeverCallsSessions(t *testing.T) {
	var sessionsCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			sessionsCalled = true
			http.Error(w, "forbidden in direct lifecycle", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/v1/seeds/grow" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	_, _ = sess.dispatchSeed(context.Background(), "do work")
	if sessionsCalled {
		t.Fatal("/v1/sessions was called; it must never be called in direct lifecycle")
	}
}

// =============================================================================
// Observation (watch)
// =============================================================================

func TestWatchTargetsReturnedPhytomerID(t *testing.T) {
	var gotPath string
	obs1, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h1", PhytomerID: "tendril-exact", Status: "satisfied", Iterations: 1,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: observation\ndata: %s\n\n", obs1)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_ = c.WatchPhytomer(context.Background(), "tendril-exact", func(core.PhytomerObservation) error { return nil })
	if gotPath != "/v1/phytomers/tendril-exact/watch" {
		t.Fatalf("path = %q, want /v1/phytomers/tendril-exact/watch", gotPath)
	}
}

func TestWatchRendersTerminalSatisfied(t *testing.T) {
	obs, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h1", PhytomerID: "tendril-1", Status: core.SeedStatusSatisfied, Iterations: 3,
		Branch: "staging/ai-fix", Commit: "deadbeef",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: observation\ndata: %s\n\n", obs)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	var received []core.PhytomerObservation
	_ = c.WatchPhytomer(context.Background(), "tendril-1", func(o core.PhytomerObservation) error {
		received = append(received, o)
		return nil
	})
	if len(received) == 0 {
		t.Fatal("no observations received")
	}
	last := received[len(received)-1]
	if last.Status != core.SeedStatusSatisfied {
		t.Fatalf("Status = %q, want satisfied", last.Status)
	}
	if last.Branch != "staging/ai-fix" {
		t.Fatalf("Branch = %q, want staging/ai-fix", last.Branch)
	}
	if last.Commit != "deadbeef" {
		t.Fatalf("Commit = %q, want deadbeef", last.Commit)
	}
}

func TestWatchBranchCommitAbsentWhenNotPresent(t *testing.T) {
	obs, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h1", PhytomerID: "tendril-1", Status: core.SeedStatusSatisfied, Iterations: 1,
		// Branch and Commit deliberately absent.
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: observation\ndata: %s\n\n", obs)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	var received []core.PhytomerObservation
	_ = c.WatchPhytomer(context.Background(), "tendril-1", func(o core.PhytomerObservation) error {
		received = append(received, o)
		return nil
	})
	if len(received) == 0 {
		t.Fatal("no observations")
	}
	last := received[len(received)-1]
	if last.Branch != "" {
		t.Fatalf("Branch = %q, want empty", last.Branch)
	}
	if last.Commit != "" {
		t.Fatalf("Commit = %q, want empty", last.Commit)
	}
}

func TestWatchNonSuccessTerminalDistinguished(t *testing.T) {
	for _, status := range []string{
		core.SeedStatusExhausted,
		core.SeedStatusWithered,
		core.SeedStatusFruitPublicationFailed,
	} {
		status := status
		t.Run(status, func(t *testing.T) {
			obs, _ := json.Marshal(core.PhytomerObservation{
				Handle: "h1", PhytomerID: "tendril-1", Status: status, Iterations: 2,
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "event: observation\ndata: %s\n\n", obs)
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)

			c := &localStemClient{port: u.Port(), bearer: ""}
			var received []core.PhytomerObservation
			_ = c.WatchPhytomer(context.Background(), "tendril-1", func(o core.PhytomerObservation) error {
				received = append(received, o)
				return nil
			})
			if len(received) == 0 {
				t.Fatal("no observations")
			}
			last := received[len(received)-1]
			if !core.SeedStatusIsTerminal(last.Status) {
				t.Fatalf("Status %q is not terminal", last.Status)
			}
			if last.Status == core.SeedStatusSatisfied {
				t.Fatalf("non-success status = satisfied, want %s", status)
			}
		})
	}
}

// =============================================================================
// Continuation
// =============================================================================

func TestContinuationCallsExactPhytomer(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continuationId":"c1","sessionId":"tendril-active","sequence":1,"deliveryState":"pending","idempotencyKey":"k1"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	_, err := c.ContinuePhytomer(context.Background(), "tendril-active", "do next step", "k1")
	if err != nil {
		t.Fatalf("ContinuePhytomer: %v", err)
	}
	if gotPath != "/v1/phytomers/tendril-active/continue" {
		t.Fatalf("path = %q, want /v1/phytomers/tendril-active/continue", gotPath)
	}
}

func TestContinuationNotNewSeed(t *testing.T) {
	var growCalled, continueCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/seeds/grow":
			growCalled = true
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"tendril-1","status":"running"}`))
		case "/v1/phytomers/tendril-1/continue":
			continueCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"continuationId":"c1","sessionId":"tendril-1","sequence":1,"deliveryState":"pending","idempotencyKey":"k1"}`))
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
		handle:     "h1",
		phytomerID: "tendril-1",
	}
	sess.storeState(chatStateActive)
	handleActiveInput(context.Background(), sess, "please also add tests")

	if !continueCalled {
		t.Fatal("/v1/phytomers/{id}/continue was not called for second input")
	}
	if growCalled {
		t.Fatal("/v1/seeds/grow was called for second input; must not")
	}
}

func TestContinuationIdempotencyKeyNonEmpty(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continuationId":"c1","sessionId":"tendril-1","sequence":1,"deliveryState":"pending","idempotencyKey":"k1"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
		handle:     "h1",
		phytomerID: "tendril-1",
	}
	sess.storeState(chatStateActive)
	handleActiveInput(context.Background(), sess, "do next iteration")
	if key, _ := gotBody["idempotencyKey"].(string); key == "" {
		t.Fatal("idempotencyKey in request body is empty; must be non-empty")
	}
}

func TestContinuationRendersAcceptanceNoKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continuationId":"cont-abc","sessionId":"tendril-1","sequence":2,"deliveryState":"pending","idempotencyKey":"secret-key-xyz"}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	// Capture stdout.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
		handle:     "h1",
		phytomerID: "tendril-1",
	}
	sess.storeState(chatStateActive)
	handleActiveInput(context.Background(), sess, "add a test")

	w.Close()
	os.Stdout = origStdout
	out, _ := io.ReadAll(r)
	outStr := string(out)

	if !strings.Contains(outStr, "cont-abc") {
		t.Fatalf("output = %q, want continuationId mention", outStr)
	}
	if !strings.Contains(outStr, "2") { // sequence
		t.Fatalf("output = %q, want sequence=2 mention", outStr)
	}
	if !strings.Contains(outStr, "pending") {
		t.Fatalf("output = %q, want deliveryState mention", outStr)
	}
	if strings.Contains(outStr, "secret-key-xyz") {
		t.Fatalf("output contains idempotency key %q; must not print it", "secret-key-xyz")
	}
}

func TestContinuationTerminalRaceDoesNotDispatchNewSeed(t *testing.T) {
	var growCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/seeds/grow":
			growCalled = true
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h2","phytomerId":"tendril-2","status":"running"}`))
		case "/v1/phytomers/tendril-1/continue":
			// Simulate terminal-race rejection (409 Conflict).
			http.Error(w, "phytomer is not continuation-eligible", http.StatusConflict)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	sess := &directChatSession{
		client:     &localStemClient{port: u.Port(), bearer: ""},
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
		handle:     "h1",
		phytomerID: "tendril-1",
	}
	sess.storeState(chatStateActive)
	handleActiveInput(context.Background(), sess, "add more tests")
	if growCalled {
		t.Fatal("/v1/seeds/grow was called after terminal-race rejection; it must not")
	}
}

func TestContinuationAcceptedShowsProgressDelivered(t *testing.T) {
	// Simulate an observation stream that shows a continuation going to delivered.
	contObs, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h1", PhytomerID: "tendril-1", Status: "running", Iterations: 1,
		Continuations: []core.ContinuationObservation{
			{ContinuationID: "cont-1", Sequence: 1, DeliveryState: core.ContinuationDeliveryDelivered},
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: observation\ndata: %s\n\n", contObs)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)

	c := &localStemClient{port: u.Port(), bearer: ""}
	var received []core.PhytomerObservation
	_ = c.WatchPhytomer(context.Background(), "tendril-1", func(o core.PhytomerObservation) error {
		received = append(received, o)
		return nil
	})
	if len(received) == 0 {
		t.Fatal("no observations")
	}
	if len(received[0].Continuations) == 0 {
		t.Fatal("no continuations in observation")
	}
	if received[0].Continuations[0].DeliveryState != core.ContinuationDeliveryDelivered {
		t.Fatalf("DeliveryState = %q, want delivered", received[0].Continuations[0].DeliveryState)
	}
}

// =============================================================================
// Port / coexistence
// =============================================================================

func TestAlternatePortUsedByDirectChat(t *testing.T) {
	var gotRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequest = true
		if r.URL.Path == "/v1/seeds/grow" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"p1","status":"running"}`))
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	// Override PORT so the client points to our test server.
	t.Setenv("PORT", u.Port())

	c := newLocalStemClient()
	if c.port != u.Port() {
		t.Fatalf("port = %q, want %s", c.port, u.Port())
	}
	_, err := c.DispatchSeed(context.Background(), map[string]any{
		"substrate": "r", "goal": "g",
	})
	if err != nil {
		t.Fatalf("DispatchSeed: %v", err)
	}
	if !gotRequest {
		t.Fatal("no request reached the alternate port server")
	}
}

// =============================================================================
// Legacy-path negatives
// =============================================================================

// TestChatLoopNeverCallsLegacyEndpoints runs through a single goal/watch cycle
// and verifies that /v1/chat/completions, /v1/sessions, and /ws are never hit.
func TestChatLoopNeverCallsLegacyEndpoints(t *testing.T) {
	var mu sync.Mutex
	var hitPaths []string

	seedObs, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h1", PhytomerID: "tendril-1",
		Status: core.SeedStatusSatisfied, Iterations: 1,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitPaths = append(hitPaths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/v1/seeds/grow":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"tendril-1","status":"running"}`))
		case "/v1/phytomers/tendril-1/watch":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "event: observation\ndata: %s\n\n", seedObs)
		default:
			http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "test-key")

	// Feed one goal via a pipe.
	pr, pw, _ := os.Pipe()
	fmt.Fprintln(pw, "fix the login bug")
	pw.Close()

	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	runChatLoop(context.Background(), sess, pr)
	pr.Close()

	mu.Lock()
	defer mu.Unlock()
	for _, p := range hitPaths {
		if p == "/v1/chat/completions" {
			t.Fatal("/v1/chat/completions was hit in direct chat lifecycle")
		}
		if p == "/v1/sessions" {
			t.Fatal("/v1/sessions was hit in direct chat lifecycle")
		}
		if p == "/ws" {
			t.Fatal("/ws was hit in direct chat lifecycle")
		}
	}
}

// =============================================================================
// continuationKeySource — determinism and non-emptiness
// =============================================================================

func TestContinuationKeyNonEmpty(t *testing.T) {
	key, err := newContinuationKey()
	if err != nil {
		t.Fatalf("newContinuationKey: %v", err)
	}
	if key == "" {
		t.Fatal("newContinuationKey returned empty key")
	}
}

func TestContinuationKeyDistinctOnEachCall(t *testing.T) {
	k1, _ := newContinuationKey()
	k2, _ := newContinuationKey()
	if k1 == k2 {
		t.Fatalf("consecutive keys are equal: %q; must be distinct", k1)
	}
}

// =============================================================================
// Helpers
// =============================================================================

// makeSeedAndWatchServers builds an httptest.Server that handles both
// /v1/seeds/grow (202) and /v1/phytomers/{id}/watch (SSE stream).
func makeSeedAndWatchServers(t *testing.T, observations []core.PhytomerObservation) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/seeds/grow":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h1","phytomerId":"tendril-1","status":"running"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/phytomers/") && strings.HasSuffix(r.URL.Path, "/watch"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			bw := bufio.NewWriter(w)
			for _, obs := range observations {
				raw, _ := json.Marshal(obs)
				fmt.Fprintf(bw, "event: observation\ndata: %s\n\n", raw)
			}
			_ = bw.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestChatLoopGoalThenWatchSettles(t *testing.T) {
	obs := core.PhytomerObservation{
		Handle: "h1", PhytomerID: "tendril-1",
		Status: core.SeedStatusSatisfied, Iterations: 2,
		Branch: "staging/ai-slice", Commit: "abc123",
	}
	server := makeSeedAndWatchServers(t, []core.PhytomerObservation{obs})
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "test-key")

	pr, pw, _ := os.Pipe()
	fmt.Fprintln(pw, "implement the feature")
	pw.Close()

	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}
	runChatLoop(context.Background(), sess, pr)
	pr.Close()

	// After settling, state must return to idle.
	if sess.loadState() != chatStateIdle {
		t.Fatalf("state = %v after terminal settlement, want chatStateIdle", sess.loadState())
	}
}

// =============================================================================
// Integration: real event loop — concurrent continuation, observation loss,
// terminal settlement.
// =============================================================================

// TestEventLoopContinuationWhileWatchOpen asserts that a second developer
// input line reaches /continue while the watch stream is still open (i.e.,
// the loop does not block stdin behind the watch goroutine).
//
// The httptest Stem:
//  1. accepts /v1/seeds/grow and returns phytomerId "tendril-loop".
//  2. opens /watch, signals a channel, then blocks until a second channel is
//     released — keeping the watch connection alive.
//  3. only then emits a terminal observation and lets the watch close.
//  4. accepts /continue and records the path + phytomerId.
//
// The test feeds two stdin lines through a pipe:
//
//	"first coding goal"
//	"continued intent"
//
// and asserts that /continue is received before the watch emits terminal
// state, that no second /v1/seeds/grow occurs, and that the phytomerId
// matches the one returned from grow.
func TestEventLoopContinuationWhileWatchOpen(t *testing.T) {
	// watchEstablished is closed when the watch handler is entered.
	watchEstablished := make(chan struct{})
	// releaseWatch is closed to allow the watch handler to emit terminal and return.
	releaseWatch := make(chan struct{})
	// continueReceived records the path of each /continue call.
	var mu sync.Mutex
	var continuePaths []string
	var growPaths []string

	terminalObs, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h-loop", PhytomerID: "tendril-loop",
		Status: core.SeedStatusSatisfied, Iterations: 1,
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/seeds/grow":
			mu.Lock()
			growPaths = append(growPaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h-loop","phytomerId":"tendril-loop","status":"running"}`))

		case strings.HasPrefix(r.URL.Path, "/v1/phytomers/") && strings.HasSuffix(r.URL.Path, "/watch"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			// Signal that the watch is established; the event loop may now
			// deliver the second stdin line.
			close(watchEstablished)
			// Block until the test allows emission of the terminal observation.
			<-releaseWatch
			flusher, _ := w.(http.Flusher)
			fmt.Fprintf(w, "event: observation\ndata: %s\n\n", terminalObs)
			if flusher != nil {
				flusher.Flush()
			}

		case strings.HasPrefix(r.URL.Path, "/v1/phytomers/") && strings.HasSuffix(r.URL.Path, "/continue"):
			mu.Lock()
			continuePaths = append(continuePaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"continuationId":"c-loop","sessionId":"tendril-loop","sequence":1,"deliveryState":"pending","idempotencyKey":"k1"}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "test-key")

	pr, pw, _ := os.Pipe()

	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runChatLoop(context.Background(), sess, pr)
	}()

	// Send the first goal.
	fmt.Fprintln(pw, "first coding goal")

	// Wait for the watch stream to be established before sending continuation.
	select {
	case <-watchEstablished:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watch to be established")
	}

	// Send continued intent while the watch is still open.
	fmt.Fprintln(pw, "continued intent")

	// Give the continuation enough time to land, then release the watch.
	// We poll until /continue is recorded to avoid a fixed sleep.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(continuePaths)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Release the watch to emit terminal state.
	close(releaseWatch)

	// Close stdin and wait for the loop to finish.
	pw.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runChatLoop to return")
	}
	pr.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(continuePaths) == 0 {
		t.Fatal("/continue was never called while watch was open; continuation is not concurrent")
	}
	wantContinuePath := "/v1/phytomers/tendril-loop/continue"
	if continuePaths[0] != wantContinuePath {
		t.Fatalf("/continue path = %q, want %q", continuePaths[0], wantContinuePath)
	}
	if len(growPaths) > 1 {
		t.Fatalf("/v1/seeds/grow was called %d times; must be exactly 1", len(growPaths))
	}
}

// TestEventLoopObservationLostByError asserts that when the watch stream
// closes with an error event before a terminal observation, the session
// identity (handle and phytomerId) is preserved and no replacement Seed is
// dispatched on the next developer input line.
func TestEventLoopObservationLostByError(t *testing.T) {
	var mu sync.Mutex
	var growPaths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/seeds/grow":
			mu.Lock()
			growPaths = append(growPaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h-loss","phytomerId":"tendril-loss","status":"running"}`))

		case strings.HasPrefix(r.URL.Path, "/v1/phytomers/") && strings.HasSuffix(r.URL.Path, "/watch"):
			// Emit a non-terminal observation then an error event (transport failure).
			nontermObs, _ := json.Marshal(core.PhytomerObservation{
				Handle: "h-loss", PhytomerID: "tendril-loss",
				Status: "running", Iterations: 0,
			})
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "event: observation\ndata: %s\n\n", nontermObs)
			// Send an error event to simulate a transport-level failure.
			fmt.Fprintf(w, "event: error\ndata: internal server error\n\n")

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "test-key")

	pr, pw, _ := os.Pipe()
	// Send the initial goal and then another line after the watch closes.
	fmt.Fprintln(pw, "first goal")

	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}

	// Run with a cancellable context so we can stop after the observation loss.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runChatLoop(ctx, sess, pr)
	}()

	// Give the loop time to dispatch the Seed, start the watch, receive the
	// error event, and enter chatStateObservationLost.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// The session state becomes ObservationLost after the watch event is processed.
		if sess.loadState() == chatStateObservationLost {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sess.loadState() != chatStateObservationLost {
		t.Fatalf("state = %v after error-event watch close, want chatStateObservationLost", sess.loadState())
	}

	// Now send another line — it must NOT dispatch a new Seed.
	fmt.Fprintln(pw, "another line after loss")
	// Brief pause to allow the event loop to process the line.
	time.Sleep(100 * time.Millisecond)

	// Cancel and drain.
	cancel()
	pw.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runChatLoop to return after cancel")
	}
	pr.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(growPaths) > 1 {
		t.Fatalf("/v1/seeds/grow called %d times; want exactly 1 (no replacement Seed after observation loss)", len(growPaths))
	}
	// Handle and phytomerId must be preserved.
	if sess.handle == "" {
		t.Fatal("handle is empty after observation loss; must be preserved")
	}
	if sess.phytomerID == "" {
		t.Fatal("phytomerId is empty after observation loss; must be preserved")
	}
	if sess.phytomerID != "tendril-loss" {
		t.Fatalf("phytomerId = %q, want tendril-loss", sess.phytomerID)
	}
}

// TestEventLoopObservationLostByCleanEOF asserts that when the watch stream
// closes with a clean EOF (no error event) before a terminal observation, the
// session transitions to chatStateObservationLost and no replacement Seed
// is dispatched.
func TestEventLoopObservationLostByCleanEOF(t *testing.T) {
	var mu sync.Mutex
	var growPaths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/seeds/grow":
			mu.Lock()
			growPaths = append(growPaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h-eof","phytomerId":"tendril-eof","status":"running"}`))

		case strings.HasPrefix(r.URL.Path, "/v1/phytomers/") && strings.HasSuffix(r.URL.Path, "/watch"):
			// Emit one non-terminal observation then close cleanly.
			nontermObs, _ := json.Marshal(core.PhytomerObservation{
				Handle: "h-eof", PhytomerID: "tendril-eof",
				Status: "running", Iterations: 0,
			})
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "event: observation\ndata: %s\n\n", nontermObs)
			// Handler returns → clean EOF.

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "test-key")

	pr, pw, _ := os.Pipe()
	fmt.Fprintln(pw, "first goal")

	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runChatLoop(ctx, sess, pr)
	}()

	// Wait for observation loss state.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess.loadState() == chatStateObservationLost {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sess.loadState() != chatStateObservationLost {
		t.Fatalf("state = %v after clean EOF watch close, want chatStateObservationLost", sess.loadState())
	}

	// Send another line — must not dispatch a replacement Seed.
	fmt.Fprintln(pw, "follow-up after eof")
	time.Sleep(100 * time.Millisecond)

	cancel()
	pw.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runChatLoop to return after cancel")
	}
	pr.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(growPaths) > 1 {
		t.Fatalf("/v1/seeds/grow called %d times; want exactly 1 (no replacement Seed after clean EOF)", len(growPaths))
	}
	if sess.phytomerID != "tendril-eof" {
		t.Fatalf("phytomerId = %q after clean EOF, want tendril-eof (must be preserved)", sess.phytomerID)
	}
}

// TestEventLoopTerminalSettlementReturnsToIdle proves that a genuine terminal
// observation is the only normal transition back to chatStateIdle and that a
// subsequent input line is permitted to start a fresh Seed.
func TestEventLoopTerminalSettlementReturnsToIdle(t *testing.T) {
	var mu sync.Mutex
	var growPaths []string

	termObs, _ := json.Marshal(core.PhytomerObservation{
		Handle: "h-term", PhytomerID: "tendril-term",
		Status: core.SeedStatusSatisfied, Iterations: 1,
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/seeds/grow":
			mu.Lock()
			growPaths = append(growPaths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"handle":"h-term","phytomerId":"tendril-term","status":"running"}`))

		case strings.HasPrefix(r.URL.Path, "/v1/phytomers/") && strings.HasSuffix(r.URL.Path, "/watch"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "event: observation\ndata: %s\n\n", termObs)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "test-key")

	pr, pw, _ := os.Pipe()
	// First goal, then a second goal after settlement.
	fmt.Fprintln(pw, "first goal")

	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  "myrepo",
		verifyArgv: []string{"go", "test", "./..."},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runChatLoop(ctx, sess, pr)
	}()

	// Wait for settlement back to Idle.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess.loadState() == chatStateIdle && len(func() []string { mu.Lock(); defer mu.Unlock(); return growPaths }()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sess.loadState() != chatStateIdle {
		t.Fatalf("state = %v after terminal observation, want chatStateIdle", sess.loadState())
	}

	// Sending a fresh goal after settlement must be accepted (starts a new Seed).
	fmt.Fprintln(pw, "second goal after settlement")
	deadline2 := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline2) {
		mu.Lock()
		n := len(growPaths)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	pw.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runChatLoop to return after second goal")
	}
	pr.Close()

	mu.Lock()
	defer mu.Unlock()

	if len(growPaths) < 2 {
		t.Fatalf("/v1/seeds/grow called %d times; want 2 (fresh Seed only allowed after terminal settlement)", len(growPaths))
	}
}
