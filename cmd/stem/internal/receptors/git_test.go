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
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// newGitTestHandler builds a GitHandler over a real Core with a stubbed git
// execution port, returning the mux, the bus (for audit assertions), a
// counter of executed commits, and the last spec the port saw.
func newGitTestHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *eventbus.Bus, *atomic.Int64, *core.GitCommitSpec) {
	t.Helper()

	executed := &atomic.Int64{}
	lastSpec := &core.GitCommitSpec{}
	coreSvc := core.NewService(nil).WithGit(core.GitOperations{
		Commit: func(ctx context.Context, spec core.GitCommitSpec) (core.GitCommitResult, error) {
			executed.Add(1)
			*lastSpec = spec
			return core.GitCommitResult{Status: "committed", CommitHash: "deadbeef"}, nil
		},
	})

	bus := eventbus.New()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: bus}
	handler := NewGitHandler(coreSvc).WithDelegation(gate)

	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, bus, executed, lastSpec
}

const gitCommitBody = `{"substrate":"core","message":"chore: record delegated growth"}`

// TestGitCommitUnchangedWithoutDelegationMarker is the security-first
// regression: a request without the delegation marker follows the plain path
// — it executes, the REST origin is stamped, and no delegation audit event is
// produced.
func TestGitCommitUnchangedWithoutDelegationMarker(t *testing.T) {
	mux, bus, executed, lastSpec := newGitTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/commit", strings.NewReader(gitCommitBody))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d commit(s), want 1", executed.Load())
	}
	if lastSpec.Origin != session.OriginREST {
		t.Fatalf("origin = %q, want the REST default", lastSpec.Origin)
	}
	if _, found := lastDelegationEvent(bus); found {
		t.Fatal("non-delegated request produced a delegation audit event")
	}
}

// TestDelegatedGitCommitDeniedAndAuditedWithoutGrant: a delegated invocation
// with no covering grant is refused before the execution port is reached, and
// the denial is audited (delegation-denied).
func TestDelegatedGitCommitDeniedAndAuditedWithoutGrant(t *testing.T) {
	mux, bus, executed, _ := newGitTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/commit", strings.NewReader(gitCommitBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a denied delegated invocation still executed a git commit")
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("denied delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
	if event.Data["subject"] != "local-agent" || event.Data["operationClass"] != core.CapGitCommit {
		t.Fatalf("audit event data = %v, want the denied request's subject and operation-class", event.Data)
	}
}

// TestDelegatedGitCommitPermittedByMatchingGrant: an active grant covering
// {subject, git.commit, substrate} lets the invocation run, and the exercise
// is audited.
func TestDelegatedGitCommitPermittedByMatchingGrant(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapGitCommit},
		Substrates:       []string{"core"},
	}}
	mux, bus, executed, lastSpec := newGitTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/commit", strings.NewReader(gitCommitBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d commit(s), want 1", executed.Load())
	}
	if lastSpec.Substrate != "core" || lastSpec.Message != "chore: record delegated growth" {
		t.Fatalf("spec = %+v, want the decoded request", lastSpec)
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("authorized delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationAuthorized {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationAuthorized)
	}
}

// TestDelegatedGitCommitDeniedOnSubstrateMismatch verifies the grant's
// substrate scope is enforced on the git commit route.
func TestDelegatedGitCommitDeniedOnSubstrateMismatch(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapGitCommit},
		Substrates:       []string{"another-substrate"},
	}}
	mux, _, executed, _ := newGitTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/commit", strings.NewReader(gitCommitBody))
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

// TestGitCommitDelegatedDeniedWithNilGate covers the fully unwired posture: a
// handler constructed without WithDelegation still denies delegated-marked
// traffic while non-delegated traffic is untouched.
func TestGitCommitDelegatedDeniedWithNilGate(t *testing.T) {
	coreSvc := core.NewService(nil).WithGit(core.GitOperations{
		Commit: func(ctx context.Context, spec core.GitCommitSpec) (core.GitCommitResult, error) {
			return core.GitCommitResult{Status: "committed", CommitHash: "deadbeef"}, nil
		},
	})
	handler := NewGitHandler(coreSvc)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	plain := httptest.NewRequest(http.MethodPost, "/v1/git/commit", strings.NewReader(gitCommitBody))
	plainRecorder := httptest.NewRecorder()
	mux.ServeHTTP(plainRecorder, plain)
	if plainRecorder.Code != http.StatusOK {
		t.Fatalf("non-delegated status = %d, want 200: %s", plainRecorder.Code, plainRecorder.Body.String())
	}

	delegated := httptest.NewRequest(http.MethodPost, "/v1/git/commit", strings.NewReader(gitCommitBody))
	delegated.Header.Set(DelegationSubjectHeader, "local-agent")
	delegatedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(delegatedRecorder, delegated)
	if delegatedRecorder.Code != http.StatusForbidden {
		t.Fatalf("delegated status with nil gate = %d, want 403", delegatedRecorder.Code)
	}
}

// newGitPushTestHandler builds a GitHandler over a real Core with a stubbed
// push port, returning the mux, the bus, and a counter of executed pushes.
func newGitPushTestHandler(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *eventbus.Bus, *atomic.Int64) {
	t.Helper()

	executed := &atomic.Int64{}
	coreSvc := core.NewService(nil).WithGit(core.GitOperations{
		Push: func(ctx context.Context, spec core.GitPushSpec) (core.GitPushResult, error) {
			executed.Add(1)
			return core.GitPushResult{Status: "pushed", Branch: "main"}, nil
		},
	})

	bus := eventbus.New()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: bus}
	handler := NewGitHandler(coreSvc).WithDelegation(gate)

	mux := http.NewServeMux()
	handler.Register(mux, nil)
	return mux, bus, executed
}

const gitPushBody = `{"substrate":"core","branch":"main"}`

// TestDelegatedGitPushDeniedAndAuditedWithoutGrant: a delegated push with no
// covering grant is refused before the execution port is reached, and the
// denial is audited under the git.push operation-class.
func TestDelegatedGitPushDeniedAndAuditedWithoutGrant(t *testing.T) {
	mux, bus, executed := newGitPushTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/push", strings.NewReader(gitPushBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a denied delegated invocation still executed a git push")
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("denied delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
	if event.Data["subject"] != "local-agent" || event.Data["operationClass"] != core.CapGitPush {
		t.Fatalf("audit event data = %v, want the denied push's subject and operation-class", event.Data)
	}
}

// TestDelegatedGitPushPermittedByMatchingGrant: a grant covering git.push on
// the substrate lets the push run and audits the exercise. A grant that only
// covers git.commit must NOT authorize a push.
func TestDelegatedGitPushPermittedByMatchingGrant(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapGitPush},
		Substrates:       []string{"core"},
	}}
	mux, bus, executed := newGitPushTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/push", strings.NewReader(gitPushBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d push(es), want 1", executed.Load())
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("authorized delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationAuthorized {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationAuthorized)
	}
}

// TestDelegatedGitPushDeniedByCommitOnlyGrant proves the operation-classes are
// distinct: a subject granted git.commit cannot push.
func TestDelegatedGitPushDeniedByCommitOnlyGrant(t *testing.T) {
	grants := []core.DelegationGrant{{
		Subject:          "local-agent",
		OperationClasses: []string{core.CapGitCommit},
		Substrates:       []string{"core"},
	}}
	mux, _, executed := newGitPushTestHandler(t, grants)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/push", strings.NewReader(gitPushBody))
	request.Header.Set(DelegationSubjectHeader, "local-agent")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (commit grant must not authorize push): %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 0 {
		t.Fatal("a commit-only grant authorized a push")
	}
}

// TestGitPushUnchangedWithoutDelegationMarker is the security-first regression
// for push: a non-delegated request runs the plain bearer-authenticated path.
func TestGitPushUnchangedWithoutDelegationMarker(t *testing.T) {
	mux, bus, executed := newGitPushTestHandler(t, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/git/push", strings.NewReader(gitPushBody))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d push(es), want 1", executed.Load())
	}
	if _, found := lastDelegationEvent(bus); found {
		t.Fatal("non-delegated push produced a delegation audit event")
	}
}
