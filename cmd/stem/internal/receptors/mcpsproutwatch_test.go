package receptors

import (
	"context"
	"encoding/json"
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

type sproutWatchEnv struct {
	started      chan struct{}
	releaseFirst chan struct{}
	delivered    chan struct{}
	releaseDone  chan struct{}
	finished     chan struct{}
	core         *core.Service
	store        *historydb.Store
	gate         *DelegationGate
	handler      *MCPHandler
}

func liveWatchContinuationPersistence(store *historydb.Store) core.ContinuationPersistence {
	port := testContinuationPersistence(store)
	port.ClaimPending = func(ctx context.Context, target core.ContinuationTarget) ([]core.ContinuationRecord, error) {
		recs, err := store.ClaimPendingContinuations(ctx, historydb.SeedTarget{
			PhytomerID: target.PhytomerID,
			Handle:     target.Handle,
			Pollen:     target.Pollen,
			Substrate:  target.Substrate,
		})
		if err != nil {
			return nil, mapTestContinuationErr(err)
		}
		out := make([]core.ContinuationRecord, 0, len(recs))
		for _, rec := range recs {
			out = append(out, core.ContinuationRecord{
				ContinuationID: rec.ContinuationID,
				PhytomerID:     rec.PhytomerID,
				Pollen:         rec.Pollen,
				Substrate:      rec.Substrate,
				Intent:         rec.Intent,
				Sequence:       rec.Sequence,
				DeliveryState:  rec.DeliveryState,
			})
		}
		return out, nil
	}
	port.MarkDelivered = func(ctx context.Context, target core.ContinuationTarget, ids []string) error {
		return mapTestContinuationErr(store.MarkContinuationsDelivered(ctx, historydb.SeedTarget{
			PhytomerID: target.PhytomerID,
			Handle:     target.Handle,
			Pollen:     target.Pollen,
			Substrate:  target.Substrate,
		}, ids))
	}
	port.HasUnresolved = func(ctx context.Context, target core.ContinuationTarget) (bool, error) {
		ok, err := store.HasUnresolvedContinuations(ctx, historydb.SeedTarget{
			PhytomerID: target.PhytomerID,
			Handle:     target.Handle,
			Pollen:     target.Pollen,
			Substrate:  target.Substrate,
		})
		return ok, mapTestContinuationErr(err)
	}
	return port
}

func watchGrant(pollen, substrate string, classes ...string) core.DelegationGrant {
	return core.DelegationGrant{
		Pollen:           pollen,
		OperationClasses: append([]string(nil), classes...),
		Substrates:       []string{substrate},
	}
}

func newSproutWatchEnv(t *testing.T, pollen string, grants []core.DelegationGrant) *sproutWatchEnv {
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

	env := &sproutWatchEnv{
		started:      make(chan struct{}),
		releaseFirst: make(chan struct{}),
		delivered:    make(chan struct{}),
		releaseDone:  make(chan struct{}),
		finished:     make(chan struct{}),
		store:        store,
		gate:         &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()},
	}
	startedOnce := &atomic.Bool{}
	env.core = core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, life *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			defer close(env.finished)
			if startedOnce.CompareAndSwap(false, true) {
				close(env.started)
			}
			select {
			case <-env.releaseFirst:
			case <-ctx.Done():
				return core.SeedGrowResult{}, ctx.Err()
			}
			if life != nil {
				if _, err := life.DeliverPending(ctx, spec.Goal); err != nil {
					return core.SeedGrowResult{}, err
				}
				if err := life.ConfirmDelivery(ctx); err != nil {
					return core.SeedGrowResult{}, err
				}
			}
			close(env.delivered)
			select {
			case <-env.releaseDone:
			case <-ctx.Done():
				return core.SeedGrowResult{}, ctx.Err()
			}
			return core.SeedGrowResult{
				Status:     core.SeedStatusSatisfied,
				Iterations: 1,
				PhytomerID: spec.PhytomerID,
				Branch:     "tendril/seed-fruit",
				Commit:     "abc123def456",
			}, nil
		},
	}).WithSeedPersistence(testSeedPersistence(store)).
		WithContinuationPersistence(liveWatchContinuationPersistence(store)).
		WithPhytomerObservationSource(testPhytomerObservationSource(store))
	env.handler = NewMCPHandler().
		WithCore(env.core).
		WithDelegation(env.gate, pollen).
		WithWatch(NewWatchAuthority(env.gate, store))
	return env
}

func (env *sproutWatchEnv) finish() {
	select {
	case <-env.releaseFirst:
	default:
		close(env.releaseFirst)
	}
	select {
	case <-env.releaseDone:
	default:
		close(env.releaseDone)
	}
}

func TestMCPToolsListIncludesSproutWatchOnce(t *testing.T) {
	handler := NewMCPHandler().WithCore(core.NewService(nil))
	resp := handler.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}
	count := 0
	for _, tool := range parsed.Result.Tools {
		if tool.Name == MCPViewSproutWatch {
			count++
		}
		if tool.Name == "sprout.watch" || tool.Name == "seedWatch" || tool.Name == "phytomerWatch" {
			t.Fatalf("tools/list published forbidden view name %q", tool.Name)
		}
	}
	if count != 1 {
		t.Fatalf("sproutWatch listed %d times, want 1", count)
	}
	for _, name := range handler.CoreCapabilityNames() {
		if name == core.CapSproutWatch {
			t.Fatal("sprout.watch appeared in CoreCapabilityNames()")
		}
	}
}

func TestMCPSproutWatchRequiresSessionID(t *testing.T) {
	handler := NewMCPHandler().WithCore(core.NewService(nil))
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": MCPViewSproutWatch, "arguments": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var response struct {
		Error *mcpError `json:"error"`
	}
	if err := json.Unmarshal(handler.ProcessMCPMessage(payload), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("missing sessionId = %+v, want Invalid arguments", response.Error)
	}
}

func TestMCPSproutWatchOwnerGrantReceivesSafeObservation(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapSeedGrow, core.CapContinuePhytomer, core.CapSproutWatch),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)
	text, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if isError {
		t.Fatalf("owner watch denied: %s", text)
	}
	assertSafeWatchJSON(t, text, seed.PhytomerID, seed.Handle)
}

func TestMCPSproutWatchDeniedWrongPollen(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapSeedGrow, core.CapContinuePhytomer, core.CapSproutWatch),
		watchGrant("claude", "core", core.CapSproutWatch),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)
	foreign := NewMCPHandler().WithCore(env.core).WithDelegation(env.gate, "claude").WithWatch(NewWatchAuthority(env.gate, env.store))
	text, isError := mcpCallTool(t, foreign, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if !isError || !strings.Contains(text, "delegation denied") {
		t.Fatalf("wrong pollen: isError=%v text=%q", isError, text)
	}
	if strings.Contains(text, seed.Handle) || strings.Contains(text, `"continuations"`) {
		t.Fatalf("wrong pollen leaked observation: %s", text)
	}
}

func TestMCPSproutWatchDeniedWithoutGrant(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapSeedGrow, core.CapContinuePhytomer),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)
	text, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if !isError || !strings.Contains(text, "delegation denied") {
		t.Fatalf("no watch grant: isError=%v text=%q", isError, text)
	}
}

func TestMCPSproutWatchDeniedWithOnlySeedGrow(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapSeedGrow),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)
	text, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if !isError || !strings.Contains(text, "delegation denied") {
		t.Fatalf("seed.grow only: isError=%v text=%q", isError, text)
	}
}

func TestMCPSproutWatchDeniedWithOnlyPhytomerContinue(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapContinuePhytomer, core.CapSeedGrow),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)
	text, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if !isError || !strings.Contains(text, "delegation denied") {
		t.Fatalf("continue only: isError=%v text=%q", isError, text)
	}
}

func TestMCPSproutWatchDeniedWrongSubstrateGrant(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapSeedGrow, core.CapContinuePhytomer),
		watchGrant("codex", "other", core.CapSproutWatch),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)
	text, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if !isError || !strings.Contains(text, "delegation denied") {
		t.Fatalf("wrong substrate grant: isError=%v text=%q", isError, text)
	}
}

func TestMCPSproutWatchOperatorNoPollen(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapSeedGrow, core.CapContinuePhytomer, core.CapSproutWatch),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)
	operator := NewMCPHandler().WithCore(env.core).WithWatch(NewWatchAuthority(env.gate, env.store))
	text, isError := mcpCallTool(t, operator, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if isError {
		t.Fatalf("operator watch denied: %s", text)
	}
	assertSafeWatchJSON(t, text, seed.PhytomerID, seed.Handle)
}

func TestMCPSeedGrowContinueWatchSamePhytomer(t *testing.T) {
	env := newSproutWatchEnv(t, "codex", []core.DelegationGrant{
		watchGrant("codex", "core", core.CapSeedGrow, core.CapContinuePhytomer, core.CapSproutWatch),
	})
	defer env.finish()
	seed := startDetachedMCPSeed(t, env)

	active, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if isError {
		t.Fatalf("active watch: %s", active)
	}
	assertSafeWatchJSON(t, active, seed.PhytomerID, seed.Handle)
	if strings.Contains(active, `"continuations"`) {
		t.Fatalf("fabricated continuations before accept: %s", active)
	}

	accepted, isError := mcpCallTool(t, env.handler, "phytomerContinue", map[string]any{
		"sessionId":      seed.PhytomerID,
		"intent":         "SECRET_CONTINUED_INTENT keep going",
		"idempotencyKey": "k-watch-1",
	})
	if isError {
		t.Fatalf("continue denied: %s", accepted)
	}
	if strings.Contains(accepted, "SECRET_CONTINUED_INTENT") {
		t.Fatalf("continue echoed intent: %s", accepted)
	}
	continuationID := jsonString(t, accepted, "continuationId")

	pending, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if isError {
		t.Fatalf("pending watch: %s", pending)
	}
	assertSafeWatchJSON(t, pending, seed.PhytomerID, seed.Handle)
	if !strings.Contains(pending, continuationID) || !strings.Contains(pending, `"deliveryState": "pending"`) && !strings.Contains(pending, `"deliveryState":"pending"`) {
		t.Fatalf("pending continuation missing: %s", pending)
	}

	close(env.releaseFirst)
	select {
	case <-env.delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation was not delivered at the cognitive boundary")
	}

	delivered, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if isError {
		t.Fatalf("delivered watch: %s", delivered)
	}
	if !strings.Contains(delivered, continuationID) || !strings.Contains(delivered, `"deliveryState": "delivered"`) && !strings.Contains(delivered, `"deliveryState":"delivered"`) {
		t.Fatalf("delivered continuation missing: %s", delivered)
	}

	close(env.releaseDone)
	select {
	case <-env.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("seed did not finish")
	}

	terminal, isError := mcpCallTool(t, env.handler, MCPViewSproutWatch, map[string]any{"sessionId": seed.PhytomerID})
	if isError {
		t.Fatalf("terminal watch: %s", terminal)
	}
	if !strings.Contains(terminal, `"status": "satisfied"`) && !strings.Contains(terminal, `"status":"satisfied"`) {
		t.Fatalf("terminal status missing: %s", terminal)
	}
	if !strings.Contains(terminal, "abc123def456") {
		t.Fatalf("terminal fruit missing: %s", terminal)
	}
	if !strings.Contains(terminal, continuationID) {
		t.Fatalf("terminal continuation missing: %s", terminal)
	}
	assertSafeWatchJSON(t, terminal, seed.PhytomerID, seed.Handle)
}

func startDetachedMCPSeed(t *testing.T, env *sproutWatchEnv) core.SeedGrowResult {
	t.Helper()
	type callResult struct {
		text    string
		isError bool
	}
	done := make(chan callResult, 1)
	go func() {
		text, isError := mcpCallTool(t, env.handler, "seedGrow", map[string]any{
			"substrate": "core",
			"goal":      "make the tests pass",
			"verify":    []string{"true"},
			"detached":  true,
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
		t.Fatalf("detached seedGrow: %s", got.text)
	}
	return core.SeedGrowResult{
		Handle:     jsonString(t, got.text, "handle"),
		PhytomerID: jsonString(t, got.text, "phytomerId"),
		Status:     jsonString(t, got.text, "status"),
	}
}

func assertSafeWatchJSON(t *testing.T, raw, phytomerID, handle string) {
	t.Helper()
	if !strings.Contains(raw, phytomerID) || !strings.Contains(raw, handle) {
		t.Fatalf("watch missing identity: %s", raw)
	}
	for _, banned := range []string{
		"SECRET_CONTINUED_INTENT",
		"idempotencyKey",
		"intentDigest",
		`"intent"`,
		"PRIVATE_PROMPT_CONTENT",
		"chain-of-thought",
		"Authorization: Bearer",
	} {
		if strings.Contains(raw, banned) {
			t.Fatalf("unsafe material %q in watch JSON: %s", banned, raw)
		}
	}
}
