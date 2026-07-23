package receptors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

// newSeedAsyncHandler builds a SeedHandler over a Core whose seed executor
// returns a fixed satisfied Fruit, wired to a real (temp) run store — the setup
// the async dispatch and collect routes need.
func newSeedAsyncHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *historydb.Store) {
	t.Helper()

	coreSvc := core.NewService(nil).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec) (core.SeedGrowResult, error) {
			return core.SeedGrowResult{
				Status: core.SeedStatusSatisfied, Iterations: 1,
				Branch: "tendril/seed-x", Diff: "the diff", Logs: "the logs",
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
		Handle string `json:"handle"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	if accepted.Handle == "" || accepted.Status != "running" {
		t.Fatalf("202 payload = %+v, want a handle and status running", accepted)
	}

	settled := waitForSeedRun(t, store, accepted.Handle)
	if settled.Status != core.SeedStatusSatisfied {
		t.Fatalf("settled status = %q, want satisfied", settled.Status)
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
