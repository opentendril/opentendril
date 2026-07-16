package receptors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

// newPassthroughTestHandler builds a PassthroughHandler over a real Core with
// a stubbed passthrough execution port, returning the mux, the bus (for audit
// assertions), a counter of executed runs, and the last spec the port saw.
func newPassthroughTestHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *eventbus.Bus, *atomic.Int64, *core.PassthroughSpec) {
	t.Helper()

	executed := &atomic.Int64{}
	lastSpec := &core.PassthroughSpec{}
	coreSvc := core.NewService(nil).WithPassthrough(core.PassthroughOperations{
		Run: func(ctx context.Context, spec core.PassthroughSpec) (core.PassthroughRunResult, error) {
			executed.Add(1)
			*lastSpec = spec
			return core.PassthroughRunResult{Status: "completed", ExitCode: 0, Stdout: "ran"}, nil
		},
	})

	bus := eventbus.New()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: bus}
	handler := NewPassthroughHandler(coreSvc).WithDelegation(gate)

	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, bus, executed, lastSpec
}

const passthroughRunBody = `{"substrate":"core","command":["gofmt","-l","."]}`

// TestPassthroughUnchangedWithoutDelegationMarker is the security-first
// regression: a request without the delegation marker follows the plain path
// — it executes, its egress allow-list is empty (deny-all), and no delegation
// audit event is produced.
func TestPassthroughUnchangedWithoutDelegationMarker(t *testing.T) {
	mux, bus, executed, lastSpec := newPassthroughTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(passthroughRunBody))
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

// TestDelegatedPassthroughDeniedAndAuditedWithoutGrant: a delegated
// invocation with no covering grant is refused before the execution port is
// reached, and the denial is audited (delegation-denied).
func TestDelegatedPassthroughDeniedAndAuditedWithoutGrant(t *testing.T) {
	mux, bus, executed, _ := newPassthroughTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(passthroughRunBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a denied delegated invocation still executed a passthrough run")
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("denied delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
	if event.Data["subject"] != "local-agent" || event.Data["operationClass"] != core.CapPassthroughRun {
		t.Fatalf("audit event data = %v, want the denied request's subject and operation-class", event.Data)
	}
}

// TestDelegatedPassthroughPermittedByMatchingGrant: an active grant covering
// {subject, passthrough.run, substrate} lets the invocation run, the exercise
// is audited, and — the egress-threading contract — the grant's allow-list
// (and only the grant's) reaches the execution port.
func TestDelegatedPassthroughPermittedByMatchingGrant(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapPassthroughRun},
		Substrates:       []string{"core"},
		Egress:           []string{"proxy.golang.org"},
	}}
	mux, bus, executed, lastSpec := newPassthroughTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(passthroughRunBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
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

// TestDelegatedPassthroughDeniedOnSubstrateMismatch verifies the grant's
// substrate scope is enforced on the passthrough route.
func TestDelegatedPassthroughDeniedOnSubstrateMismatch(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapPassthroughRun},
		Substrates:       []string{"another-substrate"},
	}}
	mux, _, executed, _ := newPassthroughTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(passthroughRunBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a substrate-mismatched delegated invocation still executed")
	}
}

// TestPassthroughCallerCannotSupplyEgress is the transport-level
// no-self-escalation check: a request body smuggling an "egress" key never
// widens the execution's allow-list — with or without a delegation marker.
func TestPassthroughCallerCannotSupplyEgress(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapPassthroughRun},
		Substrates:       []string{"core"},
		// The grant deliberately opens nothing.
	}}
	mux, _, _, lastSpec := newPassthroughTestHandler(t, grants)

	smuggled := `{"substrate":"core","command":["true"],"egress":["evil.example.com"],"Egress":["evil.example.com"]}`

	plain := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(smuggled))
	plainRecorder := httptest.NewRecorder()
	mux.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", plainRecorder.Code, plainRecorder.Body.String())
	}
	if len(lastSpec.Egress) != 0 {
		t.Fatalf("caller-supplied egress reached the port: %v", lastSpec.Egress)
	}

	delegated := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(smuggled))
	delegated.Header.Set(DelegationSubjectHeader, "local-agent")
	delegatedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(delegatedRecorder, delegated)
	if delegatedRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", delegatedRecorder.Code, delegatedRecorder.Body.String())
	}
	if len(lastSpec.Egress) != 0 {
		t.Fatalf("delegated caller widened egress beyond its grant: %v", lastSpec.Egress)
	}
}

// TestPassthroughDelegatedDeniedWithNilGate covers the fully unwired posture:
// a handler constructed without WithDelegation still denies delegated-marked
// traffic while non-delegated traffic is untouched.
func TestPassthroughDelegatedDeniedWithNilGate(t *testing.T) {
	coreSvc := core.NewService(nil).WithPassthrough(core.PassthroughOperations{
		Run: func(ctx context.Context, spec core.PassthroughSpec) (core.PassthroughRunResult, error) {
			return core.PassthroughRunResult{Status: "completed"}, nil
		},
	})
	handler := NewPassthroughHandler(coreSvc)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	plain := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(passthroughRunBody))
	plainRecorder := httptest.NewRecorder()
	mux.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("non-delegated status = %d, want 200: %s", plainRecorder.Code, plainRecorder.Body.String())
	}

	delegated := httptest.NewRequest(http.MethodPost, "/v1/passthrough/run", strings.NewReader(passthroughRunBody))
	delegated.Header.Set(DelegationSubjectHeader, "local-agent")
	delegatedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(delegatedRecorder, delegated)
	if delegatedRecorder.Code != http.StatusForbidden {
		t.Fatalf("delegated status with nil gate = %d, want 403", delegatedRecorder.Code)
	}
}
