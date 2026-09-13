package receptors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// newSeedAsyncHandler builds a SeedHandler over a Core whose seed executor
// returns a fixed satisfied Fruit, wired to a real (temp) run store — the setup
// the async dispatch and collect routes need.
func newSeedAsyncHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *historydb.Store) {
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

	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			return core.SeedGrowResult{
				Status: core.SeedStatusSatisfied, Iterations: 1,
				PhytomerID: spec.PhytomerID,
				Branch:     "tendril/seed-x", Diff: "the diff", Logs: "the logs",
			}, nil
		},
	}).WithSeedPersistence(testSeedPersistence(store)).WithContinuationPersistence(testContinuationPersistence(store))

	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()}
	handler := NewSeedHandler(coreSvc).WithDelegation(gate).WithHistory(store)
	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, store
}

func testSeedPersistence(store *historydb.Store) core.SeedPersistence {
	return core.SeedPersistence{
		FindOpening: func(ctx context.Context, pollen, idempotencyKey string) (core.SeedOpening, bool, error) {
			if store == nil {
				return core.SeedOpening{}, false, core.ErrSeedHistoryUnavailable
			}
			run, found, err := store.GetSeedRunByPollenIdempotencyKey(ctx, pollen, idempotencyKey)
			if err != nil || !found {
				return core.SeedOpening{}, found, err
			}
			return core.SeedOpening{
				Handle:         run.Handle,
				PhytomerID:     run.PhytomerID,
				Pollen:         run.Pollen,
				Substrate:      run.Substrate,
				Goal:           run.Goal,
				IdempotencyKey: run.IdempotencyKey,
				RequestDigest:  run.RequestDigest,
				Status:         run.Status,
				StartedAt:      run.StartedAt,
			}, true, nil
		},
		RecordOpening: func(ctx context.Context, opening core.SeedOpening) error {
			if store == nil {
				return core.ErrSeedHistoryUnavailable
			}
			return store.RecordSeedOpening(ctx, historydb.SeedRun{
				Handle:         opening.Handle,
				Pollen:         opening.Pollen,
				PhytomerID:     opening.PhytomerID,
				Substrate:      opening.Substrate,
				Goal:           opening.Goal,
				IdempotencyKey: opening.IdempotencyKey,
				RequestDigest:  opening.RequestDigest,
				Status:         opening.Status,
				StartedAt:      opening.StartedAt,
			})
		},
		RecordSettlement: func(ctx context.Context, settled core.SeedSettlement) error {
			return testPersistSeedSettlement(ctx, store, settled)
		},
	}
}

func testPersistSeedSettlement(ctx context.Context, store *historydb.Store, settled core.SeedSettlement) error {
	if store == nil {
		return core.ErrSeedHistoryUnavailable
	}
	return store.RecordSeedRun(ctx, historydb.SeedRun{
		Handle:                  settled.Handle,
		Pollen:                  settled.Pollen,
		PhytomerID:              settled.PhytomerID,
		Substrate:               settled.Substrate,
		Goal:                    settled.Goal,
		Status:                  settled.Status,
		Iterations:              settled.Iterations,
		Branch:                  settled.Branch,
		Commit:                  settled.Commit,
		Diff:                    settled.Diff,
		Logs:                    settled.Logs,
		Error:                   settled.Error,
		PublicationDiagnostic:   testHistorySeedPublicationDiagnostic(settled.PublicationDiagnostic),
		VerificationDiagnostics: testHistorySeedVerificationDiagnostics(settled.VerificationDiagnostics),
		StartedAt:               settled.StartedAt,
		FinishedAt:              settled.FinishedAt,
	})
}

func testContinuationPersistence(store *historydb.Store) core.ContinuationPersistence {
	unavailable := func() error {
		if store == nil {
			return core.ErrContinuationHistoryUnavailable
		}
		return nil
	}
	return core.ContinuationPersistence{
		ResolveTarget: func(ctx context.Context, phytomerID string) (core.ContinuationTarget, bool, error) {
			if err := unavailable(); err != nil {
				return core.ContinuationTarget{}, false, err
			}
			seed, found, err := store.ResolveContinuationTarget(ctx, phytomerID)
			if err != nil || !found {
				return core.ContinuationTarget{}, found, mapTestContinuationErr(err)
			}
			return core.ContinuationTarget{
				PhytomerID: seed.PhytomerID,
				Handle:     seed.Handle,
				Pollen:     seed.Pollen,
				Substrate:  seed.Substrate,
				Status:     seed.Status,
			}, true, nil
		},
		Accept: func(ctx context.Context, in core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			if err := unavailable(); err != nil {
				return core.ContinuationRecord{}, err
			}
			rec, err := store.AcceptContinuation(ctx, historydb.ContinuationAcceptance{
				PhytomerID:     in.PhytomerID,
				Pollen:         in.Pollen,
				Substrate:      in.Substrate,
				Handle:         in.Handle,
				IdempotencyKey: in.IdempotencyKey,
				Intent:         in.Intent,
				IntentDigest:   in.IntentDigest,
			})
			if err != nil {
				return core.ContinuationRecord{}, mapTestContinuationErr(err)
			}
			return core.ContinuationRecord{
				ContinuationID: rec.ContinuationID,
				PhytomerID:     rec.PhytomerID,
				Pollen:         rec.Pollen,
				Substrate:      rec.Substrate,
				IdempotencyKey: rec.IdempotencyKey,
				IntentDigest:   rec.IntentDigest,
				Intent:         rec.Intent,
				Sequence:       rec.Sequence,
				DeliveryState:  rec.DeliveryState,
				AcceptedAt:     rec.AcceptedAt,
			}, nil
		},
		ClaimPending: func(context.Context, core.ContinuationTarget) ([]core.ContinuationRecord, error) {
			if err := unavailable(); err != nil {
				return nil, err
			}
			return nil, nil
		},
		MarkDelivered: func(context.Context, core.ContinuationTarget, []string) error {
			return unavailable()
		},
		HasUnresolved: func(context.Context, core.ContinuationTarget) (bool, error) {
			return false, unavailable()
		},
		AcquireSettlementFence: func(context.Context, core.ContinuationTarget) (bool, error) {
			return true, unavailable()
		},
		CompleteSuccessfulSettlement: func(ctx context.Context, settled core.SeedSettlement) error {
			if err := unavailable(); err != nil {
				return err
			}
			return testPersistSeedSettlement(ctx, store, settled)
		},
		AccountTerminalFailure: func(ctx context.Context, settled core.SeedSettlement) (core.TerminalFailureAccount, error) {
			if err := unavailable(); err != nil {
				return core.TerminalFailureAccount{}, err
			}
			return core.TerminalFailureAccount{}, testPersistSeedSettlement(ctx, store, settled)
		},
		ReconcileOrphaned: func(ctx context.Context) error {
			if err := unavailable(); err != nil {
				return err
			}
			return store.ReconcileOrphanedSeedWork(ctx)
		},
	}
}

func testHistorySeedVerificationDiagnostics(src []core.SeedVerificationDiagnostic) []historydb.SeedVerificationDiagnostic {
	if len(src) == 0 {
		return nil
	}
	out := make([]historydb.SeedVerificationDiagnostic, len(src))
	for i, diagnostic := range src {
		out[i] = historydb.SeedVerificationDiagnostic{
			Iteration: diagnostic.Iteration,
			Outcome:   diagnostic.Outcome,
			TimedOut:  diagnostic.TimedOut,
			Message:   diagnostic.Message,
		}
		if diagnostic.ExitCode != nil {
			code := *diagnostic.ExitCode
			out[i].ExitCode = &code
		}
	}
	return out
}

func testHistorySeedPublicationDiagnostic(diagnostic *core.SeedPublicationDiagnostic) *historydb.SeedPublicationDiagnostic {
	if diagnostic == nil {
		return nil
	}
	return &historydb.SeedPublicationDiagnostic{
		FailureCategory: diagnostic.FailureCategory,
		ExecutionStatus: diagnostic.ExecutionStatus,
		Phase:           diagnostic.Phase,
		Outcome:         diagnostic.Outcome,
		RetrySafe:       diagnostic.RetrySafe,
		Message:         diagnostic.Message,
		RequestID:       diagnostic.RequestID,
	}
}

func mapTestContinuationErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, historydb.ErrContinuationNotFound):
		return core.ErrContinuationTargetNotFound
	case errors.Is(err, historydb.ErrContinuationPollenMismatch):
		return core.ErrContinuationPollenMismatch
	case errors.Is(err, historydb.ErrContinuationNotEligible):
		return core.ErrContinuationNotEligible
	case errors.Is(err, historydb.ErrContinuationTargetChanged):
		return core.ErrContinuationTargetChanged
	case errors.Is(err, historydb.ErrContinuationIdempotencyConflict):
		return core.ErrContinuationIdempotencyConflict
	case errors.Is(err, historydb.ErrContinuationInvalid):
		return core.ErrContinuationInvalid
	default:
		return err
	}
}

func seedGrantFor(pollen string) core.DelegationGrant {
	return core.DelegationGrant{
		Pollen:           pollen,
		OperationClasses: []string{core.CapSeedGrow},
		Substrates:       []string{"core"},
	}
}

func dispatchSeedAsync(t *testing.T, mux *http.ServeMux, pollen string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow/async", strings.NewReader(seedGrowBody))
	if pollen != "" {
		req.Header.Set(PollenHeader, pollen)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func waitForSeedRun(t *testing.T, store *historydb.Store, handle string) historydb.SeedRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, found, err := store.GetSeedRun(context.Background(), handle)
		if err != nil {
			t.Fatalf("GetSeedRun: %v", err)
		}
		if found && run.Status != "running" {
			return run
		}
		time.Sleep(10 * time.Millisecond) // poll: wait until the async SeedRun leaves running
	}
	t.Fatalf("seed run %s did not settle in time", handle)
	return historydb.SeedRun{}
}

// TestSeedAsyncDispatchAndCollect: a granted Pollinator dispatches a Seed, gets
// a handle, and later collects the reviewable Fruit by that handle.
func TestSeedAsyncDispatchAndCollect(t *testing.T) {
	mux, store := newSeedAsyncHandler(t, []core.DelegationGrant{seedGrantFor("local-pollinator")})

	rec := dispatchSeedAsync(t, mux, "local-pollinator")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		Handle     string `json:"handle"`
		PhytomerID string `json:"phytomerId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	if accepted.Handle == "" || accepted.Status != "running" || accepted.PhytomerID == "" {
		t.Fatalf("202 payload = %+v, want handle, phytomerId, and status running", accepted)
	}
	if !strings.HasPrefix(accepted.PhytomerID, session.IDPrefix) {
		t.Fatalf("phytomerId %q was not Stem-created", accepted.PhytomerID)
	}

	opening, found, err := store.GetSeedRun(context.Background(), accepted.Handle)
	if err != nil || !found {
		t.Fatalf("opening row: found=%v err=%v", found, err)
	}
	if opening.PhytomerID != accepted.PhytomerID || opening.Pollen != "local-pollinator" {
		t.Fatalf("opening ownership = %+v", opening)
	}
	if _, found, err := store.GetSeedRunByPhytomer(context.Background(), accepted.PhytomerID); err != nil || !found {
		t.Fatalf("phytomer lookup before sprout: found=%v err=%v", found, err)
	}

	settled := waitForSeedRun(t, store, accepted.Handle)
	if settled.Status != core.SeedStatusSatisfied {
		t.Fatalf("settled status = %q, want satisfied", settled.Status)
	}
	if settled.PhytomerID != accepted.PhytomerID {
		t.Fatalf("collected phytomer %q != dispatch phytomer %q", settled.PhytomerID, accepted.PhytomerID)
	}

	collect := httptest.NewRequest(http.MethodGet, "/v1/seeds/runs/"+accepted.Handle, nil)
	collect.Header.Set(PollenHeader, "local-pollinator")
	crec := httptest.NewRecorder()
	mux.ServeHTTP(crec, collect)
	if crec.Code != http.StatusOK {
		t.Fatalf("collect status = %d, want 200: %s", crec.Code, crec.Body.String())
	}
	var fruit historydb.SeedRun
	if err := json.Unmarshal(crec.Body.Bytes(), &fruit); err != nil {
		t.Fatalf("decode collect: %v", err)
	}
	if fruit.Status != core.SeedStatusSatisfied || fruit.Branch != "tendril/seed-x" || fruit.Diff != "the diff" {
		t.Fatalf("collected Fruit = %+v", fruit)
	}
	if fruit.PhytomerID != accepted.PhytomerID {
		t.Fatalf("collect phytomer %q != dispatch phytomer %q", fruit.PhytomerID, accepted.PhytomerID)
	}
}

func TestRESTDetachedSeedIdempotencyAndConflict(t *testing.T) {
	mux, store := newSeedAsyncHandler(t, []core.DelegationGrant{seedGrantFor("local-pollinator")})
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set(PollenHeader, "local-pollinator")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	missing := post("/v1/seeds/grow", `{"substrate":"core","goal":"make the tests pass","verify":["true"],"detached":true}`)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("detached REST without key = %d, want 400: %s", missing.Code, missing.Body.String())
	}

	body := `{"substrate":"core","goal":"make the tests pass","verify":["true"],"detached":true,"idempotencyKey":"rest-retry-key"}`
	first := post("/v1/seeds/grow", body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first REST open = %d, want 202: %s", first.Code, first.Body.String())
	}
	var accepted core.SeedGrowResult
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode first open: %v", err)
	}
	second := post("/v1/seeds/grow", body)
	if second.Code != http.StatusAccepted {
		t.Fatalf("REST replay = %d, want 202: %s", second.Code, second.Body.String())
	}
	var replay core.SeedGrowResult
	if err := json.Unmarshal(second.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replay.Handle != accepted.Handle || replay.PhytomerID != accepted.PhytomerID {
		t.Fatalf("REST replay identity = %+v, first = %+v", replay, accepted)
	}
	run, found, err := store.GetSeedRunByPollenIdempotencyKey(context.Background(), "local-pollinator", "rest-retry-key")
	if err != nil || !found || run.Handle != accepted.Handle || run.PhytomerID != accepted.PhytomerID {
		t.Fatalf("durable REST opening = found %v run %+v err %v", found, run, err)
	}

	conflict := post("/v1/seeds/grow", `{"substrate":"core","goal":"different goal","verify":["true"],"detached":true,"idempotencyKey":"rest-retry-key"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("semantic conflict = %d, want 409: %s", conflict.Code, conflict.Body.String())
	}

	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("revoked session manager: %v", err)
	}
	revokedCore := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(context.Context, core.SeedSpec, *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			t.Fatal("revoked Pollinator reached Seed execution")
			return core.SeedGrowResult{}, nil
		},
	}).WithSeedPersistence(testSeedPersistence(store)).WithContinuationPersistence(testContinuationPersistence(store))
	revoked := NewSeedHandler(revokedCore).WithDelegation(&DelegationGate{Authorizer: core.NewDelegationAuthorizer(nil), Bus: eventbus.New()})
	revokedMux := http.NewServeMux()
	revoked.Register(revokedMux, nil)
	revokedRequest := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(body))
	revokedRequest.Header.Set(PollenHeader, "local-pollinator")
	revokedResponse := httptest.NewRecorder()
	revokedMux.ServeHTTP(revokedResponse, revokedRequest)
	if revokedResponse.Code != http.StatusForbidden {
		t.Fatalf("revoked retry status = %d, want 403: %s", revokedResponse.Code, revokedResponse.Body.String())
	}
}

func TestRESTAsyncCompatibilityRouteUsesDetachedIdempotency(t *testing.T) {
	mux, _ := newSeedAsyncHandler(t, []core.DelegationGrant{seedGrantFor("local-pollinator")})
	body := `{"substrate":"core","goal":"make the tests pass","verify":["true"],"idempotencyKey":"async-compat-key"}`
	dispatch := func() core.SeedDispatch {
		req := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow/async", strings.NewReader(body))
		req.Header.Set(PollenHeader, "local-pollinator")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("async compatibility status = %d: %s", rec.Code, rec.Body.String())
		}
		var result core.SeedDispatch
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode async compatibility response: %v", err)
		}
		return result
	}
	first := dispatch()
	second := dispatch()
	if first.Handle != second.Handle || first.PhytomerID != second.PhytomerID {
		t.Fatalf("compatibility route replay changed identity: first=%+v second=%+v", first, second)
	}
}

func TestDetachedSeedIdempotencySurvivesCoreReconstruction(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open HistoryDB: %v", err)
	}
	defer store.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var firstRuns atomic.Int32
	firstManager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("first session manager: %v", err)
	}
	firstCore := core.NewService(firstManager).WithSeed(core.SeedOperations{
		Run: func(_ context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			firstRuns.Add(1)
			close(started)
			<-release
			close(finished)
			return core.SeedGrowResult{Status: core.SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSeedPersistence(testSeedPersistence(store)).WithContinuationPersistence(testContinuationPersistence(store))

	ctx := core.WithPollen(context.Background(), "restart-pollen")
	input := core.SeedGrowInput{
		Substrate: "core", Goal: "recover the accepted Seed", Verify: []string{"true"},
		Detached: true, IdempotencyKey: "restart-retry-key",
	}
	first, err := firstCore.SeedGrow(ctx, input)
	if err != nil {
		t.Fatalf("first detached open: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first background execution did not start")
	}

	// Reconstruct Core and its SessionManager while retaining only durable
	// HistoryDB state. A replay must be resolved from that durable opening.
	var secondRuns atomic.Int32
	secondStarted := make(chan struct{}, 1)
	secondManager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("second session manager: %v", err)
	}
	secondCore := core.NewService(secondManager).WithSeed(core.SeedOperations{
		Run: func(_ context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			secondRuns.Add(1)
			secondStarted <- struct{}{}
			return core.SeedGrowResult{Status: core.SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSeedPersistence(testSeedPersistence(store)).WithContinuationPersistence(testContinuationPersistence(store))
	second, err := secondCore.SeedGrow(ctx, input)
	if err != nil {
		t.Fatalf("reconstructed Core replay: %v", err)
	}
	if second.Handle != first.Handle || second.PhytomerID != first.PhytomerID {
		t.Fatalf("restart replay changed identity: first=%+v second=%+v", first, second)
	}
	changed := input
	changed.Goal = "different semantic request"
	if _, err := secondCore.SeedGrow(ctx, changed); !errors.Is(err, core.ErrSeedIdempotencyConflict) {
		t.Fatalf("changed request after restart = %v, want idempotency conflict", err)
	}
	select {
	case <-secondStarted:
		t.Fatal("reconstructed Core started a background execution during replay")
	case <-time.After(50 * time.Millisecond):
	}
	if secondRuns.Load() != 0 || firstRuns.Load() != 1 {
		t.Fatalf("background runs = first %d second %d, want 1 and 0", firstRuns.Load(), secondRuns.Load())
	}
	run, found, err := store.GetSeedRunByPollenIdempotencyKey(ctx, "restart-pollen", "restart-retry-key")
	if err != nil || !found || run.Handle != first.Handle || run.PhytomerID != first.PhytomerID || run.Status != core.SeedStatusRunning {
		t.Fatalf("durable retry row = found %v run %+v err %v", found, run, err)
	}
	close(release)
	released = true
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("original detached execution did not finish")
	}
}

func TestSeedAsyncCollectionPreservesFruitPublicationFailureDiagnostic(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	diagnostic := &core.SeedPublicationDiagnostic{
		FailureCategory: core.SeedFailureCategoryFruitPublication,
		ExecutionStatus: core.SeedStatusSatisfied,
		Phase:           "commit-mutation",
		Outcome:         "reconciliation-unavailable",
		Message:         "read-only GitHub reconciliation could not establish the target state",
		RequestID:       "req-safe-123",
	}
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(_ context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			return core.SeedGrowResult{
				Status:                core.SeedStatusSatisfied,
				Iterations:            2,
				PhytomerID:            spec.PhytomerID,
				Branch:                "tendril/untrusted",
				Commit:                "untrusted-oid",
				Diff:                  "completed diff",
				Logs:                  "completed logs",
				PublicationDiagnostic: diagnostic,
			}, fmt.Errorf("upstream-secret-content")
		},
	}).WithSeedPersistence(testSeedPersistence(store)).WithContinuationPersistence(testContinuationPersistence(store))
	gates := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{seedGrantFor("publication-pollinator")}), Bus: eventbus.New()}
	handler := NewSeedHandler(coreSvc).WithDelegation(gates).WithHistory(store)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	rec := dispatchSeedAsync(t, mux, "publication-pollinator")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d: %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}
	settled := waitForSeedRun(t, store, accepted.Handle)
	if settled.Status != core.SeedStatusFruitPublicationFailed || settled.Branch != "" || settled.Commit != "" {
		t.Fatalf("settled Fruit = %+v", settled)
	}
	if settled.Iterations != 2 || settled.Diff != "completed diff" || settled.Logs != "completed logs" {
		t.Fatalf("settled execution evidence = %+v", settled)
	}
	if settled.PublicationDiagnostic == nil || settled.PublicationDiagnostic.RequestID != diagnostic.RequestID {
		t.Fatalf("settled diagnostic = %+v", settled.PublicationDiagnostic)
	}

	collect := httptest.NewRequest(http.MethodGet, "/v1/seeds/runs/"+accepted.Handle, nil)
	collect.Header.Set(PollenHeader, "publication-pollinator")
	collected := httptest.NewRecorder()
	mux.ServeHTTP(collected, collect)
	if collected.Code != http.StatusOK {
		t.Fatalf("collect status = %d: %s", collected.Code, collected.Body.String())
	}
	var got historydb.SeedRun
	if err := json.Unmarshal(collected.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode collection: %v", err)
	}
	if got.Status != core.SeedStatusFruitPublicationFailed || got.PublicationDiagnostic == nil || got.PublicationDiagnostic.Outcome != diagnostic.Outcome {
		t.Fatalf("collected publication failure = %+v", got)
	}
	if strings.Contains(collected.Body.String(), "upstream-secret-content") {
		t.Fatalf("raw execution error leaked from collection: %s", collected.Body.String())
	}
}

// TestSeedAsyncDeniedWithoutGrant: a delegated dispatch with no covering grant
// is refused before any handle is minted.
func TestSeedAsyncDeniedWithoutGrant(t *testing.T) {
	mux, _ := newSeedAsyncHandler(t, nil)
	rec := dispatchSeedAsync(t, mux, "local-pollinator")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestSeedCollectScopedToDispatchingSubject: a handle dispatched by one subject
// cannot be collected by another, even one that holds its own grant.
func TestSeedCollectScopedToDispatchingSubject(t *testing.T) {
	mux, store := newSeedAsyncHandler(t, []core.DelegationGrant{
		seedGrantFor("pollen-a"),
		seedGrantFor("pollen-b"),
	})

	rec := dispatchSeedAsync(t, mux, "pollen-a")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, want 202", rec.Code)
	}
	var accepted struct {
		Handle string `json:"handle"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &accepted)
	waitForSeedRun(t, store, accepted.Handle)

	collect := httptest.NewRequest(http.MethodGet, "/v1/seeds/runs/"+accepted.Handle, nil)
	collect.Header.Set(PollenHeader, "pollen-b")
	crec := httptest.NewRecorder()
	mux.ServeHTTP(crec, collect)
	if crec.Code != http.StatusForbidden {
		t.Fatalf("cross-subject collect status = %d, want 403: %s", crec.Code, crec.Body.String())
	}
}

func TestSeedAsyncPersistFailureDoesNotAccept(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			t.Fatal("execution must not start when ownership cannot be persisted")
			return core.SeedGrowResult{}, nil
		},
	}).WithSeedPersistence(core.SeedPersistence{
		RecordOpening: func(context.Context, core.SeedOpening) error {
			return fmt.Errorf("disk full")
		},
	}).WithContinuationPersistence(testContinuationPersistence(nil))
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{seedGrantFor("local-pollinator")}), Bus: eventbus.New()}
	handler := NewSeedHandler(coreSvc).WithDelegation(gate)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	rec := dispatchSeedAsync(t, mux, "local-pollinator")
	if rec.Code == http.StatusAccepted {
		t.Fatalf("persist failure returned 202: %s", rec.Body.String())
	}
}

func TestSeedAsyncCallerCannotSupplyOwnership(t *testing.T) {
	mux, store := newSeedAsyncHandler(t, []core.DelegationGrant{seedGrantFor("local-pollinator")})
	body := `{"substrate":"core","goal":"make the tests pass","verify":["go","test","./..."],"idempotencyKey":"ownership-test-key","pollen":"attacker","phytomerId":"tendril-forged"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow/async", strings.NewReader(body))
	req.Header.Set(PollenHeader, "local-pollinator")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		Handle     string `json:"handle"`
		PhytomerID string `json:"phytomerId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if accepted.PhytomerID == "tendril-forged" {
		t.Fatal("caller-supplied phytomer identity was accepted")
	}
	run := waitForSeedRun(t, store, accepted.Handle)
	if run.Pollen != "local-pollinator" {
		t.Fatalf("recorded pollen = %q, want authenticated subject", run.Pollen)
	}
	if run.PhytomerID != accepted.PhytomerID {
		t.Fatalf("recorded phytomer %q != dispatch %q", run.PhytomerID, accepted.PhytomerID)
	}
}

func TestSeedAsyncWithoutHistoryDoesNotAccept(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			t.Fatal("execution must not start when ownership cannot be persisted")
			return core.SeedGrowResult{}, nil
		},
	})
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{seedGrantFor("local-pollinator")}), Bus: eventbus.New()}
	handler := NewSeedHandler(coreSvc).WithDelegation(gate)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	rec := dispatchSeedAsync(t, mux, "local-pollinator")
	if rec.Code == http.StatusAccepted {
		t.Fatalf("missing history still returned 202: %s", rec.Body.String())
	}
}

func TestRESTCannotManufactureSeedLifecycleRelation(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	var openings []core.SeedOpening
	executed := make(chan core.SeedSpec, 1)
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			executed <- spec
			return core.SeedGrowResult{Status: core.SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSeedPersistence(core.SeedPersistence{
		FindOpening: func(context.Context, string, string) (core.SeedOpening, bool, error) {
			return core.SeedOpening{}, false, nil
		},
		RecordOpening: func(_ context.Context, opening core.SeedOpening) error {
			openings = append(openings, opening)
			return nil
		},
		RecordSettlement: func(context.Context, core.SeedSettlement) error { return nil },
	}).WithContinuationPersistence(testContinuationPersistence(nil))
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{seedGrantFor("local-pollinator")}), Bus: eventbus.New()}
	handler := NewSeedHandler(coreSvc).WithDelegation(gate)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	body := `{"substrate":"core","goal":"make the tests pass","verify":["true"],"idempotencyKey":"relation-test-key","pollen":"attacker","phytomerId":"tendril-forged","handle":"seed-forged"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow/async", strings.NewReader(body))
	req.Header.Set(PollenHeader, "local-pollinator")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var accepted core.SeedDispatch
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if accepted.PhytomerID == "tendril-forged" || accepted.Handle == "seed-forged" {
		t.Fatalf("REST manufactured lifecycle identity: %+v", accepted)
	}
	if len(openings) != 1 {
		t.Fatalf("want one Stem-composed opening, got %d", len(openings))
	}
	if openings[0].Pollen != "local-pollinator" || openings[0].PhytomerID != accepted.PhytomerID || openings[0].Substrate != "core" {
		t.Fatalf("REST chose ownership: %+v", openings[0])
	}
	select {
	case spec := <-executed:
		if spec.PhytomerID != accepted.PhytomerID {
			t.Fatalf("async execution did not use the prepared Phytomer: spec=%+v accepted=%+v", spec, accepted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async execution did not run the prepared growth")
	}
}

func TestSeedCollectUnknownHandle(t *testing.T) {
	mux, _ := newSeedAsyncHandler(t, []core.DelegationGrant{seedGrantFor("local-pollinator")})
	collect := httptest.NewRequest(http.MethodGet, "/v1/seeds/runs/seed-nope", nil)
	collect.Header.Set(PollenHeader, "local-pollinator")
	crec := httptest.NewRecorder()
	mux.ServeHTTP(crec, collect)
	if crec.Code != http.StatusNotFound {
		t.Fatalf("unknown handle status = %d, want 404", crec.Code)
	}
}
