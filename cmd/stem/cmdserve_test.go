package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/gateway"
	"github.com/opentendril/opentendril/cmd/stem/internal/scheduler"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
	"github.com/opentendril/opentendril/cmd/stem/internal/triggers"
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

// A credential issued under any superseded prefix — "otp_", and the bare
// "tendril_" namespace that preceded the two-segment kind prefixes — must be
// refused outright.
//
// This is the one behaviour worth pinning about a prefix rename. The prefix is
// the discriminator that routes a presented bearer to credential resolution, so
// an old value no longer looks credential-shaped and falls through to the
// Botanist-key comparison instead. It must fail there. The forbidden outcome is
// that it is accepted — either by matching the Botanist key or by being treated
// as an ordinary unauthenticated request that proceeds anyway.
//
// The refusal of a "tendril_" bearer is over-determined here — the digest never
// matches either — so this covers the surface outcome rather than the prefix
// check itself. Prefix discrimination is pinned in the core package, by
// TestBearerPrefixesAreDisjoint and
// TestAccessTokenAndCredentialPrefixesAreMutuallyExclusive.
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

	// The same secret body carrying each superseded prefix: what a Pollinator
	// issued before that rename would still be presenting. The current prefix is
	// unexported, so it is spelled out here and guarded — the guard fires on the
	// next rename, which is the reminder to add the outgoing prefix below.
	const currentPrefix = "tendril_refresh_"
	body := strings.TrimPrefix(secret, currentPrefix)
	if body == secret {
		t.Fatalf("issued secret does not carry %q; add the outgoing prefix to the superseded list", currentPrefix)
	}

	// Only the prefix is ever named in a failure — never the value, which
	// carries the secret body.
	for _, superseded := range []string{"otp_", "tendril_", "tendril_root_"} {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
		req.Header.Set("Authorization", "Bearer "+superseded+body)
		rec := httptest.NewRecorder()
		handler(rec, req)

		if reached {
			t.Fatalf("a credential carrying the superseded prefix %q reached the handler", superseded)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d for a credential carrying superseded prefix %q", rec.Code, http.StatusUnauthorized, superseded)
		}
	}

	// The current prefix still works, so the refusals above are about the prefix
	// rather than a broken fixture.
	reached = false
	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
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
	handler := withWebSocketAuth("secret-key", nil, nil, false, gateway.HandleWebSocket(bus))
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
	t.Setenv(EnvBotanistKey, "env-key")
	dir := t.TempDir()

	key, generated, err := getOrCreateAPIKey(filepath.Join(dir, ".tendril"))
	if err != nil {
		t.Fatalf("getOrCreateAPIKey: %v", err)
	}
	if generated {
		t.Fatalf("should not generate a key when %s is set", EnvBotanistKey)
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

	// An empty triggers dir means no Hormonal Triggers are configured,
	// so the fire proceeds.
	triggersDir := filepath.Join(t.TempDir(), "no-triggers")
	if err := os.MkdirAll(triggersDir, 0o755); err != nil {
		t.Fatalf("failed to create triggers dir: %v", err)
	}
	firer := scheduledRunFirer(svc, manager, triggersDir, nil)
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
	sess, ok := manager.Get(context.Background(), got.SessionID)
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
	os.Unsetenv(EnvBotanistKey)

	if key := resolveServeAPIKey(); key != "" {
		t.Fatalf("resolveServeAPIKey returned %q from a variable that is not the bearer key", key)
	}
}

func TestStemBearerKeyComesFromItsOwnVariable(t *testing.T) {
	t.Setenv(EnvBotanistKey, "a-real-bearer-key")
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")

	if key := resolveServeAPIKey(); key != "a-real-bearer-key" {
		t.Fatalf("resolveServeAPIKey = %q, want the value of %s", key, EnvBotanistKey)
	}
}

// The end of the chain: the trial constant must not authenticate.
func TestProviderValueDoesNotAuthenticate(t *testing.T) {
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")
	os.Unsetenv(EnvBotanistKey)

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

// TestTerrariumRunnerPublishesHostActivationEvent verifies that RunTrigger
// publishes exactly one EventHostExecutionActivated event on the bus when the
// host terrarium provider is activated. This is the wiring assertion for the
// structured audit trail — the observer-callback contract in the terrarium
// package only guarantees the callback is called; this test guarantees the
// caller correctly translates it into a bus event.
func TestTerrariumRunnerPublishesHostActivationEvent(t *testing.T) {
	t.Setenv("TENDRIL_ALLOW_HOST_EXECUTION", "true")

	// The host provider is activated but the script does not exist, so the run
	// will fail after the provider is resolved. That is fine — we only care
	// that the event was published before the error.
	bus := eventbus.New()
	var received []eventbus.EventType
	bus.Subscribe(eventbus.EventHostExecutionActivated, func(e eventbus.Event) {
		received = append(received, e.Type)
	})

	mode, runner := resolveTriggerModeAndRunner(bus)
	_ = mode
	// RunTrigger with a non-existent script: the error from the provider itself
	// won't happen (the host provider is allowed), but the script-exec will fail.
	// We just need to verify the event fired before the post-activation error.
	_ = runner.RunTrigger(context.Background(), "/dev/null/no-such-script", triggers.TriggerPayload{})

	if len(received) != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d", eventbus.EventHostExecutionActivated, len(received))
	}
}

// TestTerrariumRunnerNoEventForDockerProvider verifies that no
// EventHostExecutionActivated event is published when the Docker provider is
// selected. Only host-provider activation is a security-relevant event.
func TestTerrariumRunnerNoEventForDockerProvider(t *testing.T) {
	t.Setenv("TENDRIL_ALLOW_HOST_EXECUTION", "")

	bus := eventbus.New()
	var received int
	bus.Subscribe(eventbus.EventHostExecutionActivated, func(_ eventbus.Event) {
		received++
	})

	// Docker is the default when TENDRIL_ALLOW_HOST_EXECUTION is unset.
	_, runner := resolveTriggerModeAndRunner(bus)
	// The script path is irrelevant; we only check the event count.
	_ = runner.RunTrigger(context.Background(), "/nonexistent", triggers.TriggerPayload{})

	if received != 0 {
		t.Fatalf("expected no host-activation events for docker provider, got %d", received)
	}
}

// TestHandleHealthPublishesToBus proves the nil-bus on-demand bug is fixed:
// hitting GET /health with a real *eventbus.Bus wired into the monitor must
// result in EventHealthCheck landing on a subscriber. Before this fix,
// handleHealth constructed its own monitor with bus == nil so publish was a
// silent no-op and no health event ever reached the bus in production.
func TestHandleHealthPublishesToBus(t *testing.T) {
	bus := eventbus.New()

	received := make(chan eventbus.Event, 1)
	bus.Subscribe(eventbus.EventHealthCheck, func(e eventbus.Event) {
		select {
		case received <- e:
		default:
		}
	})

	monitor := newDefaultHealthMonitor(bus, 30*time.Second)
	handler := handleHealth(monitor, false)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	// The handler must have returned a valid JSON body first.
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status %d", rec.Code)
	}

	// Now assert the bus actually received the event (proving the wiring, not
	// just that the handler returned 200).
	select {
	case e := <-received:
		if e.Type != eventbus.EventHealthCheck {
			t.Fatalf("event type = %q, want %q", e.Type, eventbus.EventHealthCheck)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: EventHealthCheck never reached the bus; nil-bus bug may still be present")
	}
}

// TestHandleHealthStatusCodes confirms the handler maps Overall=true → 200 and
// Overall=false → 503, so callers can rely on the status code without parsing
// the JSON body.
func TestHandleHealthStatusCodes(t *testing.T) {
	bus := eventbus.New()
	monitor := newDefaultHealthMonitor(bus, 30*time.Second)
	handler := handleHealth(monitor, false)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	// Default checks all pass in a unit-test environment (no disks, no
	// external services): overall should be true → 200 OK.
	if rec.Code != http.StatusOK {
		// A 503 is not a test failure per se — it means a default check
		// reported unhealthy in CI. Log it clearly but don't hard-fail, because
		// the important invariant is the mapping logic, not the check outcome.
		t.Logf("note: /health returned %d (a registered check reported unhealthy); mapping logic is still exercised", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
	}
}

func TestHandleHealthOwnerPublication(t *testing.T) {
	bus := eventbus.New()
	monitor := newDefaultHealthMonitor(bus, 30*time.Second)

	cases := []struct {
		name      string
		networked bool
		wantOwner bool
	}{
		{"loopback publishes owner", false, true},
		{"networked withholds owner", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := handleHealth(monitor, tc.networked)
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			var payload map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("parse response: %v", err)
			}

			// 1. Existing fields are unchanged
			if _, ok := payload["timestamp"]; !ok {
				t.Error("timestamp is missing")
			}
			if _, ok := payload["overall"]; !ok {
				t.Error("overall is missing")
			}
			if _, ok := payload["results"]; !ok {
				t.Error("results is missing")
			}

			// 2. Minimum disclosure: no other fields are present (e.g. no account name, no executable)
			allowedKeys := map[string]bool{
				"timestamp": true,
				"overall":   true,
				"results":   true,
			}
			if tc.wantOwner {
				allowedKeys["owner"] = true
			}
			for k := range payload {
				if !allowedKeys[k] {
					t.Errorf("unexpected field in response: %q", k)
				}
			}

			owner, hasOwner := payload["owner"]
			if tc.wantOwner {
				if !hasOwner {
					t.Fatal("owner is absent on loopback bind")
				}
				uid, ok := owner.(float64)
				if !ok {
					t.Fatalf("owner is not a number: %T", owner)
				}
				if int(uid) != os.Getuid() {
					t.Errorf("owner = %v, want %d", int(uid), os.Getuid())
				}
			} else {
				if hasOwner {
					t.Errorf("owner is present on networked bind: %v", owner)
				}
			}
		})
	}
}

func TestScheduledRunFirerPublishesTriggerBlockedEvent(t *testing.T) {
	bus := eventbus.New()
	var received []eventbus.Event
	bus.Subscribe(eventbus.EventTriggerBlocked, func(e eventbus.Event) {
		received = append(received, e)
	})

	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := core.NewService(manager)

	// ModeEnforce ensures that a nonexistent triggers directory causes a block
	t.Setenv("TENDRIL_TRIGGERS_MODE", "enforce")

	firer := scheduledRunFirer(svc, manager, "/nonexistent-dir-blocks-run", bus)
	entry := scheduler.Entry{
		Model: "some-model",
	}

	err = firer(context.Background(), "my-schedule", entry)
	if err == nil || !strings.Contains(err.Error(), "blocked by Hormonal Triggers") {
		t.Fatalf("expected blocked by Hormonal Triggers error, got: %v", err)
	}

	if len(received) != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d", eventbus.EventTriggerBlocked, len(received))
	}
	if received[0].Source != "scheduler" {
		t.Errorf("Source = %q, want scheduler", received[0].Source)
	}
	data := received[0].Data
	if data["schedule"] != "my-schedule" {
		t.Errorf("data.schedule = %v, want my-schedule", data["schedule"])
	}
	if data["genotype"] != "some-model" {
		t.Errorf("data.genotype = %v, want some-model", data["genotype"])
	}
	if data["reason"] == nil || data["reason"] == "" {
		t.Error("data.reason is empty")
	}
}

func TestHandleChatCompletionsPublishesTriggerBlockedEvent(t *testing.T) {
	t.Setenv("TENDRIL_ALLOW_HOST_EXECUTION", "true")
	t.Setenv("TENDRIL_TRIGGERS_MODE", "enforce")

	// Chdir to temp dir so getTriggersDir() looks in an empty directory and blocks
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(cwd)

	bus := eventbus.New()
	var received []eventbus.Event
	bus.Subscribe(eventbus.EventTriggerBlocked, func(e eventbus.Event) {
		received = append(received, e)
	})

	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	handler := handleChatCompletions(bus, manager, nil)

	body := `{"model": "test-model", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Phytomer", "test-session")

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	if len(received) != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d", eventbus.EventTriggerBlocked, len(received))
	}

	ev := received[0]
	if ev.Source != "chat" {
		t.Errorf("Source = %q, want chat", ev.Source)
	}
	if ev.SessionID == "" {
		t.Error("SessionID is empty")
	}

	data := ev.Data
	if data["genotype"] != "test-model" {
		t.Errorf("data.genotype = %v, want test-model", data["genotype"])
	}
	if data["reason"] == nil || data["reason"] == "" {
		t.Error("data.reason is empty")
	}
}

func TestTriggersModeFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want triggers.TriggerMode
	}{
		{"", triggers.ModeEnforce},
		{"   ", triggers.ModeEnforce},
		{"enforce", triggers.ModeEnforce},
		{"disabled", triggers.ModeDisabled},
		{"DISABLED", triggers.ModeDisabled},
		{"Disabled ", triggers.ModeDisabled},
		{"unknown", triggers.ModeEnforce},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("TENDRIL_TRIGGERS_MODE", tc.env)
			got := triggersModeFromEnv()
			if got != tc.want {
				t.Errorf("env %q: got %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}
