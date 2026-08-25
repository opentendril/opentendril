package receptors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec) (core.SeedGrowResult, error) {
			return core.SeedGrowResult{
				Status: core.SeedStatusSatisfied, Iterations: 1,
				PhytomerID: spec.PhytomerID,
				Branch:     "tendril/seed-x", Diff: "the diff", Logs: "the logs",
			}, nil
		},
	})

	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()}
	handler := NewSeedHandler(coreSvc).WithDelegation(gate).WithHistory(store)
	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, store
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
		time.Sleep(10 * time.Millisecond)
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
	mux, _ := newSeedAsyncHandler(t, []core.DelegationGrant{seedGrantFor("local-pollinator")})
	original := recordSeedRunFn
	t.Cleanup(func() { recordSeedRunFn = original })
	recordSeedRunFn = func(context.Context, *historydb.Store, historydb.SeedRun) error {
		return fmt.Errorf("disk full")
	}

	rec := dispatchSeedAsync(t, mux, "local-pollinator")
	if rec.Code == http.StatusAccepted {
		t.Fatalf("persist failure returned 202: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestSeedAsyncCallerCannotSupplyOwnership(t *testing.T) {
	mux, store := newSeedAsyncHandler(t, []core.DelegationGrant{seedGrantFor("local-pollinator")})
	body := `{"substrate":"core","goal":"make the tests pass","verify":["go","test","./..."],"pollen":"attacker","phytomerId":"tendril-forged"}`
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
		Run: func(ctx context.Context, spec core.SeedSpec) (core.SeedGrowResult, error) {
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
