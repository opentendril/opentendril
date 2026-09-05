package receptors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

type blockingSeedEnv struct {
	started chan struct{}
	release chan struct{}
	runs    *atomic.Int64
	egress  atomic.Value
	core    *core.Service
	store   *historydb.Store
	gate    *DelegationGate
}

func newBlockingSeedEnv(t *testing.T, grants []core.DelegationGrant) *blockingSeedEnv {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	env := &blockingSeedEnv{
		started: make(chan struct{}),
		release: make(chan struct{}),
		runs:    &atomic.Int64{},
		store:   store,
		gate:    &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()},
	}
	startedOnce := &atomic.Bool{}
	env.core = core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			env.runs.Add(1)
			env.egress.Store(append([]string(nil), spec.Egress...))
			if startedOnce.CompareAndSwap(false, true) {
				close(env.started)
			}
			select {
			case <-env.release:
				return core.SeedGrowResult{Status: core.SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
			case <-ctx.Done():
				return core.SeedGrowResult{}, ctx.Err()
			}
		},
	}).WithSeedPersistence(testSeedPersistence(store)).WithContinuationPersistence(testContinuationPersistence(store))
	return env
}

func (env *blockingSeedEnv) startRunningSeed(t *testing.T, pollen, substrate string) core.SeedGrowResult {
	t.Helper()
	ctx := context.Background()
	if pollen != "" {
		ctx = core.WithPollen(ctx, pollen)
	}
	result, err := env.core.SeedGrow(ctx, core.SeedGrowInput{
		Substrate: substrate,
		Goal:      "make the tests pass",
		Verify:    []string{"true"},
		Detached:  true,
		Origin:    session.OriginMCP,
	})
	if err != nil {
		t.Fatalf("detached seed: %v", err)
	}
	select {
	case <-env.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background Seed Run did not start")
	}
	return result
}

func (env *blockingSeedEnv) finish(t *testing.T) {
	t.Helper()
	select {
	case <-env.release:
	default:
		close(env.release)
	}
}

func continueGrant(pollen, substrate string) core.DelegationGrant {
	return core.DelegationGrant{
		Pollen:           pollen,
		OperationClasses: []string{core.CapContinuePhytomer, core.CapSeedGrow},
		Substrates:       []string{substrate},
		Egress:           []string{"api.example.test"},
	}
}

func TestMCPPhytomerContinueAuthorizedByMatchingGrant(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{continueGrant("codex", "core")})
	defer env.finish(t)
	seed := env.startRunningSeed(t, "codex", "core")
	handler := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "codex")

	text, isError := mcpCallTool(t, handler, "phytomerContinue", map[string]any{
		"sessionId":      seed.PhytomerID,
		"intent":         "keep going",
		"idempotencyKey": "k1",
		"substrate":      "other",
	})
	if isError {
		t.Fatalf("authorized continue denied: %s", text)
	}
	if strings.Contains(text, "keep going") {
		t.Fatalf("tool result echoed plaintext intent: %s", text)
	}
	if !strings.Contains(text, `"deliveryState"`) || !strings.Contains(text, seed.PhytomerID) {
		t.Fatalf("acceptance missing identity/state: %s", text)
	}
}

func TestMCPPhytomerContinueDeniedWithoutGrant(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{seedGrantFor("codex")})
	defer env.finish(t)
	seed := env.startRunningSeed(t, "codex", "core")
	handler := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "codex")

	text, isError := mcpCallTool(t, handler, "phytomerContinue", map[string]any{
		"sessionId":      seed.PhytomerID,
		"intent":         "keep going",
		"idempotencyKey": "k1",
	})
	if !isError || !strings.Contains(text, "delegation denied") {
		t.Fatalf("missing grant: isError=%v text=%q", isError, text)
	}
}

func TestMCPPhytomerContinueDeniedOnOtherSubstrateGrant(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{continueGrant("codex", "other")})
	defer env.finish(t)
	seed := env.startRunningSeed(t, "codex", "core")
	handler := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "codex")

	text, isError := mcpCallTool(t, handler, "phytomerContinue", map[string]any{
		"sessionId":      seed.PhytomerID,
		"intent":         "keep going",
		"idempotencyKey": "k1",
		"substrate":      "other",
	})
	if !isError || !strings.Contains(text, "delegation denied") {
		t.Fatalf("grant on other substrate: isError=%v text=%q", isError, text)
	}
}

func TestMCPPhytomerContinueWrongPollenFailsClosed(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{
		continueGrant("codex", "core"),
		continueGrant("claude", "core"),
	})
	defer env.finish(t)
	seed := env.startRunningSeed(t, "codex", "core")
	handler := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "claude")

	text, isError := mcpCallTool(t, handler, "phytomerContinue", map[string]any{
		"sessionId":      seed.PhytomerID,
		"intent":         "keep going",
		"idempotencyKey": "k1",
	})
	if !isError {
		t.Fatalf("wrong pollen succeeded: %s", text)
	}
	if strings.Contains(text, "delegation denied") && !strings.Contains(text, "pollen") {
		// Grant might match, but ownership must still fail closed.
	}
	if strings.Contains(strings.ToLower(text), "keep going") {
		t.Fatalf("wrong-pollen error leaked intent: %s", text)
	}
}

func TestMCPPhytomerContinueUnknownAndTerminalFailClosed(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{continueGrant("codex", "core")})
	defer env.finish(t)
	handler := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "codex")

	text, isError := mcpCallTool(t, handler, "phytomerContinue", map[string]any{
		"sessionId":      "tendril-missing",
		"intent":         "keep going",
		"idempotencyKey": "k1",
	})
	if !isError {
		t.Fatalf("unknown phytomer succeeded: %s", text)
	}

	seed := env.startRunningSeed(t, "codex", "core")
	if err := env.store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle:     seed.Handle,
		Pollen:     "codex",
		PhytomerID: seed.PhytomerID,
		Substrate:  "core",
		Goal:       "make the tests pass",
		Status:     core.SeedStatusSatisfied,
		StartedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	text, isError = mcpCallTool(t, handler, "phytomerContinue", map[string]any{
		"sessionId":      seed.PhytomerID,
		"intent":         "keep going",
		"idempotencyKey": "k-terminal",
	})
	if !isError {
		t.Fatalf("terminal phytomer succeeded: %s", text)
	}
}

func TestMCPSeedGrowDetachedReturnsBeforeTerminal(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{continueGrant("codex", "core")})
	defer env.finish(t)
	handler := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "codex")

	type callResult struct {
		text    string
		isError bool
	}
	done := make(chan callResult, 1)
	go func() {
		text, isError := mcpCallTool(t, handler, "seedGrow", map[string]any{
			"substrate": "core",
			"goal":      "make the tests pass",
			"verify":    []string{"true"},
			"detached":  true,
			"egress":    []string{"attacker.example"},
		})
		done <- callResult{text: text, isError: isError}
	}()

	select {
	case <-env.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background Seed Run did not start")
	}
	var got callResult
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("MCP seedGrow detached did not return while Run blocked")
	}
	if got.isError {
		t.Fatalf("detached seedGrow error: %s", got.text)
	}
	if !strings.Contains(got.text, `"status": "running"`) && !strings.Contains(got.text, `"status":"running"`) {
		t.Fatalf("detached seedGrow did not return running: %s", got.text)
	}
	if !strings.Contains(got.text, `"handle"`) || !strings.Contains(got.text, `"phytomerId"`) {
		t.Fatalf("detached seedGrow missing identity: %s", got.text)
	}

	opening, found, err := env.store.GetSeedRunByPhytomer(context.Background(), jsonString(t, got.text, "phytomerId"))
	if err != nil || !found {
		t.Fatalf("durable opening missing: found=%v err=%v", found, err)
	}
	if opening.Status != core.SeedStatusRunning {
		t.Fatalf("opening status = %q", opening.Status)
	}
}

func TestMCPDetachedSeedInjectsGrantEgress(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{continueGrant("codex", "core")})
	defer env.finish(t)
	handler := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "codex")

	done := make(chan string, 1)
	go func() {
		text, isError := mcpCallTool(t, handler, "seedGrow", map[string]any{
			"substrate": "core",
			"goal":      "make the tests pass",
			"verify":    []string{"true"},
			"detached":  true,
			"egress":    []string{"attacker.example"},
		})
		if isError {
			done <- "error:" + text
			return
		}
		done <- text
	}()
	select {
	case <-env.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not start")
	}
	select {
	case text := <-done:
		if strings.HasPrefix(text, "error:") {
			t.Fatalf("detached seedGrow: %s", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("seedGrow did not return")
	}
	got, _ := env.egress.Load().([]string)
	if len(got) != 1 || got[0] != "api.example.test" {
		t.Fatalf("egress = %v, want grant allow-list", got)
	}
}

func jsonString(t *testing.T, raw, key string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode %s from %q: %v", key, raw, err)
	}
	value, _ := payload[key].(string)
	if value == "" {
		t.Fatalf("missing %s in %q", key, raw)
	}
	return value
}

func TestRESTPhytomerContinueGrantAndOwnership(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{
		continueGrant("codex", "core"),
		continueGrant("claude", "core"),
	})
	defer env.finish(t)
	seed := env.startRunningSeed(t, "codex", "core")

	handler := NewSessionsHandler(env.core, nil, env.store, nil).WithDelegation(env.gate)
	mux := http.NewServeMux()
	handler.Register(mux, nil, nil)

	postContinue := func(pollen, sessionID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/phytomers/"+sessionID+"/continue", strings.NewReader(body))
		req.SetPathValue("sessionId", sessionID)
		if pollen != "" {
			req.Header.Set(PollenHeader, pollen)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	ok := postContinue("codex", seed.PhytomerID, `{"intent":"keep going","idempotencyKey":"k1","sessionId":"forged","substrate":"other"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("matching grant status = %d: %s", ok.Code, ok.Body.String())
	}
	if strings.Contains(ok.Body.String(), "keep going") {
		t.Fatalf("REST echoed intent: %s", ok.Body.String())
	}

	noGrantEnv := newBlockingSeedEnv(t, []core.DelegationGrant{seedGrantFor("codex")})
	defer noGrantEnv.finish(t)
	noGrantSeed := noGrantEnv.startRunningSeed(t, "codex", "core")
	noGrantHandler := NewSessionsHandler(noGrantEnv.core, nil, noGrantEnv.store, nil).WithDelegation(noGrantEnv.gate)
	noGrantMux := http.NewServeMux()
	noGrantHandler.Register(noGrantMux, nil, nil)
	denied := httptest.NewRequest(http.MethodPost, "/v1/phytomers/"+noGrantSeed.PhytomerID+"/continue", strings.NewReader(`{"intent":"keep going","idempotencyKey":"k2"}`))
	denied.SetPathValue("sessionId", noGrantSeed.PhytomerID)
	denied.Header.Set(PollenHeader, "codex")
	deniedRec := httptest.NewRecorder()
	noGrantMux.ServeHTTP(deniedRec, denied)
	if deniedRec.Code != http.StatusForbidden {
		t.Fatalf("no grant status = %d: %s", deniedRec.Code, deniedRec.Body.String())
	}

	wrongPollen := postContinue("claude", seed.PhytomerID, `{"intent":"steal","idempotencyKey":"k3"}`)
	if wrongPollen.Code != http.StatusForbidden {
		t.Fatalf("wrong pollen status = %d, want 403: %s", wrongPollen.Code, wrongPollen.Body.String())
	}

	unknown := postContinue("codex", "tendril-missing", `{"intent":"keep going","idempotencyKey":"k4"}`)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown phytomer status = %d: %s", unknown.Code, unknown.Body.String())
	}

	legacy := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+seed.PhytomerID+"/continue", strings.NewReader(`{"intent":"keep going","idempotencyKey":"k1"}`))
	legacy.SetPathValue("sessionId", seed.PhytomerID)
	legacy.Header.Set(PollenHeader, "codex")
	legacyRec := httptest.NewRecorder()
	mux.ServeHTTP(legacyRec, legacy)
	if legacyRec.Code != http.StatusOK {
		t.Fatalf("legacy alias status = %d: %s", legacyRec.Code, legacyRec.Body.String())
	}
}

func TestRESTDetachedSeedGrowAndAsyncCompatibility(t *testing.T) {
	env := newBlockingSeedEnv(t, []core.DelegationGrant{continueGrant("codex", "core")})
	defer env.finish(t)
	handler := NewSeedHandler(env.core).WithDelegation(env.gate).WithHistory(env.store)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(`{"substrate":"core","goal":"make the tests pass","verify":["true"],"detached":true,"egress":["attacker.example"]}`))
	req.Header.Set(PollenHeader, "codex")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("detached grow status = %d: %s", rec.Code, rec.Body.String())
	}
	var detached core.SeedGrowResult
	if err := json.Unmarshal(rec.Body.Bytes(), &detached); err != nil {
		t.Fatalf("decode detached: %v", err)
	}
	if detached.Status != core.SeedStatusRunning || detached.Handle == "" || detached.PhytomerID == "" {
		t.Fatalf("detached result = %+v", detached)
	}
	select {
	case <-env.started:
	case <-time.After(2 * time.Second):
		t.Fatal("detached grow did not start Run")
	}

	env2 := newBlockingSeedEnv(t, []core.DelegationGrant{continueGrant("codex", "core")})
	defer env2.finish(t)
	handler2 := NewSeedHandler(env2.core).WithDelegation(env2.gate).WithHistory(env2.store)
	mux2 := http.NewServeMux()
	handler2.Register(mux2, nil)
	async := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow/async", strings.NewReader(`{"substrate":"core","goal":"make the tests pass","verify":["true"]}`))
	async.Header.Set(PollenHeader, "codex")
	asyncRec := httptest.NewRecorder()
	mux2.ServeHTTP(asyncRec, async)
	if asyncRec.Code != http.StatusAccepted {
		t.Fatalf("async status = %d: %s", asyncRec.Code, asyncRec.Body.String())
	}
	var compat core.SeedGrowResult
	if err := json.Unmarshal(asyncRec.Body.Bytes(), &compat); err != nil {
		t.Fatalf("decode async: %v", err)
	}
	if compat.Status != core.SeedStatusRunning || !strings.HasPrefix(compat.Handle, "seed-") {
		t.Fatalf("async result = %+v", compat)
	}
	select {
	case <-env2.started:
	case <-time.After(2 * time.Second):
		t.Fatal("async compatibility did not start Run")
	}
}
