package receptors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// newSeedTestHandler builds a SeedHandler over a real Core with a stubbed
// Seed-growth execution port, returning the mux, the bus (for audit
// assertions), a counter of executed grows, and the last spec the port saw.
func newSeedTestHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *eventbus.Bus, *atomic.Int64, *core.SeedSpec) {
	t.Helper()

	executed := &atomic.Int64{}
	lastSpec := &core.SeedSpec{}
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			executed.Add(1)
			*lastSpec = spec
			return core.SeedGrowResult{Status: core.SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	})

	bus := eventbus.New()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: bus}
	handler := NewSeedHandler(coreSvc).WithDelegation(gate)

	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, bus, executed, lastSpec
}

const seedGrowBody = `{"substrate":"core","goal":"make the tests pass","verify":["go","test","./..."]}`

// TestSeedUnchangedWithoutDelegationMarker is the security-first regression: a
// request without the delegation marker follows the plain path — it executes,
// its egress allow-list is empty (deny-all), and no delegation audit event is
// produced.
func TestSeedUnchangedWithoutDelegationMarker(t *testing.T) {
	mux, bus, executed, lastSpec := newSeedTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(seedGrowBody))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d grow(s), want 1", executed.Load())
	}
	if len(lastSpec.Egress) != 0 {
		t.Fatalf("non-delegated egress = %v, want empty (deny-all)", lastSpec.Egress)
	}
	if _, found := lastDelegationEvent(bus); found {
		t.Fatal("non-delegated request produced a delegation audit event")
	}
}

// TestDelegatedSeedDeniedAndAuditedWithoutGrant: a delegated invocation with no
// covering grant is refused before the execution port is reached, and the
// denial is audited.
func TestDelegatedSeedDeniedAndAuditedWithoutGrant(t *testing.T) {
	mux, bus, executed, _ := newSeedTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(seedGrowBody))
	request.Header.Set(PollenHeader, "local-pollinator")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a denied delegated invocation still grew a Seed")
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("denied delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
	if event.Data["pollen"] != "local-pollinator" || event.Data["operationClass"] != core.CapSeedGrow {
		t.Fatalf("audit event data = %v, want the denied request's pollen and operation-class", event.Data)
	}
}

// TestDelegatedSeedPermittedByMatchingGrant: an active grant covering
// {pollen, seed.grow, substrate} lets the invocation run, the exercise is
// audited, and the grant's allow-list (and only the grant's) reaches the port.
func TestDelegatedSeedPermittedByMatchingGrant(t *testing.T) {
	grants := []core.DelegationGrant{{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapSeedGrow},
		Substrates:       []string{"core"},
		Egress:           []string{"proxy.golang.org"},
	}}
	mux, bus, executed, lastSpec := newSeedTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(seedGrowBody))
	request.Header.Set(PollenHeader, "local-pollinator")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d grow(s), want 1", executed.Load())
	}
	if len(lastSpec.Egress) != 1 || lastSpec.Egress[0] != "proxy.golang.org" {
		t.Fatalf("spec egress = %v, want the matching grant's allow-list", lastSpec.Egress)
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("authorized delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationAuthorized {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationAuthorized)
	}
}

// TestDelegatedSeedDeniedOnSubstrateMismatch verifies the grant's substrate
// scope is enforced on the seed route.
func TestDelegatedSeedDeniedOnSubstrateMismatch(t *testing.T) {
	grants := []core.DelegationGrant{{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapSeedGrow},
		Substrates:       []string{"another-substrate"},
	}}
	mux, _, executed, _ := newSeedTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(seedGrowBody))
	request.Header.Set(PollenHeader, "local-pollinator")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a substrate-mismatched delegated invocation still ran")
	}
}

// TestSeedCallerCannotSupplyEgress is the transport-level no-self-escalation
// check: a request body smuggling an "egress" key never widens the execution's
// allow-list — with or without a delegation marker.
func TestSeedCallerCannotSupplyEgress(t *testing.T) {
	grants := []core.DelegationGrant{{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapSeedGrow},
		Substrates:       []string{"core"},
		// The grant deliberately opens nothing.
	}}
	mux, _, _, lastSpec := newSeedTestHandler(t, grants)

	smuggled := `{"substrate":"core","goal":"g","verify":["true"],"egress":["evil.example.com"],"Egress":["evil.example.com"]}`

	plain := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(smuggled))
	plainRecorder := httptest.NewRecorder()
	mux.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", plainRecorder.Code, plainRecorder.Body.String())
	}
	if len(lastSpec.Egress) != 0 {
		t.Fatalf("caller-supplied egress reached the port: %v", lastSpec.Egress)
	}

	delegated := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(smuggled))
	delegated.Header.Set(PollenHeader, "local-pollinator")
	delegatedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(delegatedRecorder, delegated)
	if delegatedRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", delegatedRecorder.Code, delegatedRecorder.Body.String())
	}
	if len(lastSpec.Egress) != 0 {
		t.Fatalf("delegated caller widened egress beyond its grant: %v", lastSpec.Egress)
	}
}

func TestSyncSeedResultReturnsPhytomerID(t *testing.T) {
	mux, _, _, lastSpec := newSeedTestHandler(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(seedGrowBody))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var result core.SeedGrowResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.PhytomerID == "" || !strings.HasPrefix(result.PhytomerID, session.IDPrefix) {
		t.Fatalf("phytomerId = %q", result.PhytomerID)
	}
	if lastSpec.PhytomerID != result.PhytomerID {
		t.Fatalf("adapter-invented phytomer? spec=%q result=%q", lastSpec.PhytomerID, result.PhytomerID)
	}
}

func TestDelegatedSeedStampsPollenOnContext(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	var saw string
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			saw = core.PollenFromContext(ctx)
			return core.SeedGrowResult{Status: core.SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	})
	grants := []core.DelegationGrant{{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapSeedGrow},
		Substrates:       []string{"core"},
	}}
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()}
	handler := NewSeedHandler(coreSvc).WithDelegation(gate)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	smuggled := `{"substrate":"core","goal":"g","verify":["true"],"pollen":"attacker","phytomerId":"tendril-forged"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(smuggled))
	request.Header.Set(PollenHeader, "local-pollinator")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if saw != "local-pollinator" {
		t.Fatalf("execution pollen = %q, want the authenticated subject", saw)
	}
	var result core.SeedGrowResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.PhytomerID == "tendril-forged" {
		t.Fatal("caller-supplied phytomer identity was accepted")
	}
}
