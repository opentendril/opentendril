package receptors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// newDelegationTestHandler builds a SproutHandler over a real Core with a
// stubbed sprout execution port, returning the mux, the bus (for audit
// assertions), and a counter of executed sprout runs.
func newDelegationTestHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *eventbus.Bus, *atomic.Int64) {
	t.Helper()

	executed := &atomic.Int64{}
	coreSvc := core.NewService(nil).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			executed.Add(1)
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})

	bus := eventbus.New()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: bus}
	handler := NewSproutHandler(coreSvc, nil, bus).WithDelegation(gate)

	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, bus, executed
}

// waitForExecutions polls until the detached goroutine has run count sprouts.
func waitForExecutions(t *testing.T, executed *atomic.Int64, count int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for executed.Load() < count {
		if time.Now().After(deadline) {
			t.Fatalf("executed %d sprout run(s), want %d", executed.Load(), count)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func lastDelegationEvent(bus *eventbus.Bus) (eventbus.Event, bool) {
	for _, event := range bus.History(50) {
		if event.Type == eventbus.EventDelegationAuthorized || event.Type == eventbus.EventDelegationDenied {
			return event, true
		}
	}
	return eventbus.Event{}, false
}

const sproutRunBody = `{"transcript":"grow","substrate":"core"}`

// TestSproutRoutesUnchangedWithoutDelegationMarker is the security-first
// regression: with zero grants configured (and even with a gate wired), a
// request that does not carry the delegation marker follows today's path —
// the detached route answers 202 and the run executes.
func TestSproutRoutesUnchangedWithoutDelegationMarker(t *testing.T) {
	mux, bus, executed := newDelegationTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-1/sprout/run", strings.NewReader(sproutRunBody))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("detached route status = %d, want 202: %s", recorder.Code, recorder.Body.String())
	}
	waitForExecutions(t, executed, 1)

	syncRequest := httptest.NewRequest(http.MethodPost, "/v1/sprouts/run", strings.NewReader(sproutRunBody))
	syncRecorder := httptest.NewRecorder()
	mux.ServeHTTP(syncRecorder, syncRequest)

	if syncRecorder.Code != http.StatusOK {
		t.Fatalf("governed route status = %d, want 200: %s", syncRecorder.Code, syncRecorder.Body.String())
	}
	if _, found := lastDelegationEvent(bus); found {
		t.Fatal("non-delegated requests produced a delegation audit event")
	}
}

// TestSproutRoutesUnchangedWithNilGate covers the fully unwired posture: a
// handler constructed without WithDelegation behaves exactly as before for
// non-delegated traffic and still denies delegated-marked traffic.
func TestSproutRoutesUnchangedWithNilGate(t *testing.T) {
	coreSvc := core.NewService(nil).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})
	handler := NewSproutHandler(coreSvc, nil, eventbus.New())
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	plain := httptest.NewRequest(http.MethodPost, "/v1/sprouts/run", strings.NewReader(sproutRunBody))
	plainRecorder := httptest.NewRecorder()
	mux.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("non-delegated status = %d, want 200: %s", plainRecorder.Code, plainRecorder.Body.String())
	}

	delegated := httptest.NewRequest(http.MethodPost, "/v1/sprouts/run", strings.NewReader(sproutRunBody))
	delegated.Header.Set(DelegationSubjectHeader, "local-agent")
	delegatedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(delegatedRecorder, delegated)
	if delegatedRecorder.Code != http.StatusForbidden {
		t.Fatalf("delegated status with nil gate = %d, want 403", delegatedRecorder.Code)
	}
}

// TestDelegatedAsyncRunDeniedAndAuditedWithoutGrant closes the previously
// ungoverned detached path: a delegated invocation with no covering grant is
// refused before any goroutine detaches, and the denial is audited.
func TestDelegatedAsyncRunDeniedAndAuditedWithoutGrant(t *testing.T) {
	mux, bus, executed := newDelegationTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-1/sprout/run", strings.NewReader(sproutRunBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a denied delegated invocation still executed a sprout run")
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("denied delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
	if event.Data["subject"] != "local-agent" || event.Data["operationClass"] != core.CapSproutRun {
		t.Fatalf("audit event data = %v, want the denied request's subject and operation-class", event.Data)
	}
}

// TestDelegatedAsyncRunPermittedByMatchingGrant exercises the grant-match
// path end to end on the detached route: an active grant covering
// {subject, sprout.run, substrate} lets the invocation detach, and the
// exercise is audited.
func TestDelegatedAsyncRunPermittedByMatchingGrant(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapSproutRun},
		Substrates:       []string{"core"},
	}}
	mux, bus, executed := newDelegationTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-1/sprout/run", strings.NewReader(sproutRunBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", recorder.Code, recorder.Body.String())
	}
	waitForExecutions(t, executed, 1)

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("authorized delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationAuthorized {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationAuthorized)
	}
}

// TestDelegatedGovernedRunDeniedOnSubstrateMismatch verifies the governed
// synchronous route enforces the grant's substrate scope.
func TestDelegatedGovernedRunDeniedOnSubstrateMismatch(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapSproutRun},
		Substrates:       []string{"another-substrate"},
	}}
	mux, _, executed := newDelegationTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/sprouts/run", strings.NewReader(sproutRunBody))
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

// TestDelegationGateMiddlewareDefaultDeny verifies the blanket middleware:
// non-delegated requests pass through untouched; delegated-marked requests to
// routes with no delegable operation-class are denied and audited — even when
// a grant exists (grants name operation-classes, and these routes expose
// none).
func TestDelegationGateMiddlewareDefaultDeny(t *testing.T) {
	bus := eventbus.New()
	gate := &DelegationGate{
		Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{{
			Subject:          "local-agent",
			OperationClasses: []string{core.CapSproutRun},
			Substrates:       []string{"core"},
		}}),
		Bus: bus,
	}

	nextCalled := 0
	wrapped := gate.Middleware(func(w http.ResponseWriter, r *http.Request) { nextCalled++ })

	plain := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	plainRecorder := httptest.NewRecorder()
	wrapped(plainRecorder, plain)
	if nextCalled != 1 || plainRecorder.Code != http.StatusOK {
		t.Fatalf("non-delegated request blocked: nextCalled=%d status=%d", nextCalled, plainRecorder.Code)
	}

	delegated := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	delegated.Header.Set(DelegationSubjectHeader, "local-agent")
	delegatedRecorder := httptest.NewRecorder()
	wrapped(delegatedRecorder, delegated)
	if nextCalled != 1 {
		t.Fatal("delegated-marked request reached a route with no delegable operation-class")
	}
	if delegatedRecorder.Code != http.StatusForbidden {
		t.Fatalf("delegated status = %d, want 403", delegatedRecorder.Code)
	}
	if event, found := lastDelegationEvent(bus); !found || event.Type != eventbus.EventDelegationDenied {
		t.Fatal("default-denied delegated request left no denial audit event")
	}

	// A nil gate keeps the same posture without panicking.
	var nilGate *DelegationGate
	nilWrapped := nilGate.Middleware(func(w http.ResponseWriter, r *http.Request) { nextCalled++ })
	nilRecorder := httptest.NewRecorder()
	nilWrapped(nilRecorder, delegated)
	if nextCalled != 1 || nilRecorder.Code != http.StatusForbidden {
		t.Fatalf("nil gate: nextCalled=%d status=%d, want 1 and 403", nextCalled, nilRecorder.Code)
	}
}

// TestAuthMiddlewareDeniesDelegatedRequests verifies the bearer middleware's
// delegation posture: its config routes expose no delegable operation-class,
// so a delegated-marked request is refused rather than silently executed.
func TestAuthMiddlewareDeniesDelegatedRequests(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "")

	nextCalled := 0
	wrapped := AuthMiddleware(func(w http.ResponseWriter, r *http.Request) { nextCalled++ })

	plain := httptest.NewRequest(http.MethodGet, "/v1/config/triggers", nil)
	plainRecorder := httptest.NewRecorder()
	wrapped(plainRecorder, plain)
	if nextCalled != 1 || plainRecorder.Code != http.StatusOK {
		t.Fatalf("non-delegated request blocked: nextCalled=%d status=%d", nextCalled, plainRecorder.Code)
	}

	delegated := httptest.NewRequest(http.MethodGet, "/v1/config/triggers", nil)
	delegated.Header.Set(DelegationSubjectHeader, "local-agent")
	delegatedRecorder := httptest.NewRecorder()
	wrapped(delegatedRecorder, delegated)
	if nextCalled != 1 {
		t.Fatal("delegated-marked request passed the bearer middleware")
	}
	if delegatedRecorder.Code != http.StatusForbidden {
		t.Fatalf("delegated status = %d, want 403", delegatedRecorder.Code)
	}
}
