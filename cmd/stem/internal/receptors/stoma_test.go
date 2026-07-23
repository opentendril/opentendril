package receptors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// newStomaTestHandler builds a StomaHandler over a real Core with
// a stubbed stoma execution port, returning the mux, the bus (for audit
// assertions), a counter of executed runs, and the last spec the port saw.
func newStomaTestHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *eventbus.Bus, *atomic.Int64, *core.StomaSpec) {
	t.Helper()

	executed := &atomic.Int64{}
	lastSpec := &core.StomaSpec{}
	coreSvc := core.NewService(nil).WithStoma(core.StomaOperations{
		Run: func(ctx context.Context, spec core.StomaSpec) (core.StomaPassResult, error) {
			executed.Add(1)
			*lastSpec = spec
			return core.StomaPassResult{Status: "completed", ExitCode: 0, Stdout: "ran"}, nil
		},
	})

	bus := eventbus.New()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: bus}
	handler := NewStomaHandler(coreSvc).WithDelegation(gate)

	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, bus, executed, lastSpec
}

const stomaPassBody = `{"substrate":"core","command":["gofmt","-l","."]}`

// TestStomaUnchangedWithoutDelegationMarker is the security-first
// regression: a request without the delegation marker follows the plain path
// — it executes, its egress allow-list is empty (deny-all), and no delegation
// audit event is produced.
func TestStomaUnchangedWithoutDelegationMarker(t *testing.T) {
	mux, bus, executed, lastSpec := newStomaTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(stomaPassBody))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d run(s), want 1", executed.Load())
	}
	if len(lastSpec.Egress) != 0 {
		t.Fatalf("non-delegated egress = %v, want empty (deny-all)", lastSpec.Egress)
	}
	if _, found := lastDelegationEvent(bus); found {
		t.Fatal("non-delegated request produced a delegation audit event")
	}
}

// TestDelegatedStomaDeniedAndAuditedWithoutGrant: a delegated
// invocation with no covering grant is refused before the execution port is
// reached, and the denial is audited (delegation-denied).
func TestDelegatedStomaDeniedAndAuditedWithoutGrant(t *testing.T) {
	mux, bus, executed, _ := newStomaTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(stomaPassBody))
	request.Header.Set(PollenHeader, "local-pollinator")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a denied delegated invocation still executed a stoma pass")
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("denied delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
	if event.Data["pollen"] != "local-pollinator" || event.Data["operationClass"] != core.CapStomaPass {
		t.Fatalf("audit event data = %v, want the denied request's pollen and operation-class", event.Data)
	}
}

// TestDelegatedStomaPermittedByMatchingGrant: an active grant covering
// {pollen, stoma.pass, substrate} lets the invocation run, the exercise
// is audited, and — the egress-threading contract — the grant's allow-list
// (and only the grant's) reaches the execution port.
func TestDelegatedStomaPermittedByMatchingGrant(t *testing.T) {
	grants := []core.DelegationGrant{{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapStomaPass},
		Substrates:       []string{"core"},
		Egress:           []string{"proxy.golang.org"},
	}}
	mux, bus, executed, lastSpec := newStomaTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(stomaPassBody))
	request.Header.Set(PollenHeader, "local-pollinator")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d run(s), want 1", executed.Load())
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

// TestDelegatedStomaDeniedOnSubstrateMismatch verifies the grant's
// substrate scope is enforced on the stoma route.
func TestDelegatedStomaDeniedOnSubstrateMismatch(t *testing.T) {
	grants := []core.DelegationGrant{{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapStomaPass},
		Substrates:       []string{"another-substrate"},
	}}
	mux, _, executed, _ := newStomaTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(stomaPassBody))
	request.Header.Set(PollenHeader, "local-pollinator")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a substrate-mismatched delegated invocation still executed")
	}
}

// TestStomaCallerCannotSupplyEgress is the transport-level
// no-self-escalation check: a request body smuggling an "egress" key never
// widens the execution's allow-list — with or without a delegation marker.
func TestStomaCallerCannotSupplyEgress(t *testing.T) {
	grants := []core.DelegationGrant{{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapStomaPass},
		Substrates:       []string{"core"},
		// The grant deliberately opens nothing.
	}}
	mux, _, _, lastSpec := newStomaTestHandler(t, grants)

	smuggled := `{"substrate":"core","command":["true"],"egress":["evil.example.com"],"Egress":["evil.example.com"]}`

	plain := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(smuggled))
	plainRecorder := httptest.NewRecorder()
	mux.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", plainRecorder.Code, plainRecorder.Body.String())
	}
	if len(lastSpec.Egress) != 0 {
		t.Fatalf("caller-supplied egress reached the port: %v", lastSpec.Egress)
	}

	delegated := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(smuggled))
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

// TestStomaDelegatedDeniedWithNilGate covers the fully unwired posture:
// a handler constructed without WithDelegation still denies delegated-marked
// traffic while non-delegated traffic is untouched.
func TestStomaDelegatedDeniedWithNilGate(t *testing.T) {
	coreSvc := core.NewService(nil).WithStoma(core.StomaOperations{
		Run: func(ctx context.Context, spec core.StomaSpec) (core.StomaPassResult, error) {
			return core.StomaPassResult{Status: "completed"}, nil
		},
	})
	handler := NewStomaHandler(coreSvc)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	plain := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(stomaPassBody))
	plainRecorder := httptest.NewRecorder()
	mux.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("non-delegated status = %d, want 200: %s", plainRecorder.Code, plainRecorder.Body.String())
	}

	delegated := httptest.NewRequest(http.MethodPost, "/v1/stoma/pass", strings.NewReader(stomaPassBody))
	delegated.Header.Set(PollenHeader, "local-pollinator")
	delegatedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(delegatedRecorder, delegated)
	if delegatedRecorder.Code != http.StatusForbidden {
		t.Fatalf("delegated status with nil gate = %d, want 403", delegatedRecorder.Code)
	}
}
