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

// credentialFixture issues a credential in a temporary control-plane directory
// and returns the secret plus the loaded set.
func credentialFixture(t *testing.T, pollen string) (secret string, credentials PollinatorCredentials) {
	t.Helper()
	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, pollen, "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	loaded, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return secret, PollinatorCredentials(loaded)
}

// newCredentialGate builds a git surface whose gate resolves the given
// credentials and grants.
func newCredentialGate(t *testing.T, credentials PollinatorCredentials, grants []core.DelegationGrant) (*http.ServeMux, *atomic.Int64) {
	t.Helper()
	executed := &atomic.Int64{}
	coreSvc := core.NewService(nil).WithGit(core.GitOperations{
		Status: func(ctx context.Context, spec core.GitStatusSpec) (core.GitStatusResult, error) {
			executed.Add(1)
			return core.GitStatusResult{Branch: "feat/x", CommitAllowed: true}, nil
		},
	})
	gate := &DelegationGate{
		Pollinators: credentials,
		Authorizer:  core.NewDelegationAuthorizer(grants),
		Bus:         eventbus.New(),
	}
	mux := http.NewServeMux()
	NewGitHandler(coreSvc).WithDelegation(gate).Register(mux, nil)
	return mux, executed
}

const credentialBody = `{"substrate":"demo"}`

func statusRequest(secret, headerClaim string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/git/status", strings.NewReader(credentialBody))
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	if headerClaim != "" {
		request.Header.Set(PollenHeader, headerClaim)
	}
	return request
}

// TestCredentialDerivesThePollen: a presented credential authenticates as the
// Pollen it was issued for, with no header involved at all.
func TestCredentialDerivesThePollen(t *testing.T) {
	secret, credentials := credentialFixture(t, "claude")
	mux, executed := newCredentialGate(t, credentials, []core.DelegationGrant{{
		Pollen: "claude", OperationClasses: []string{core.CapGitStatus}, Substrates: []string{"demo"},
	}})

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, statusRequest(secret, ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d, want 1", executed.Load())
	}
}

// TestCredentialOverridesTheHeaderClaim is the property that closes the gap:
// holding a credential must not let a caller act as a different Pollen. The
// grant here covers ONLY the credential's identity, so if the header were
// honoured the request would be denied — and if the credential is honoured it
// succeeds. Either way the header cannot promote the caller.
func TestCredentialOverridesTheHeaderClaim(t *testing.T) {
	secret, credentials := credentialFixture(t, "claude")
	mux, executed := newCredentialGate(t, credentials, []core.DelegationGrant{{
		Pollen: "claude", OperationClasses: []string{core.CapGitStatus}, Substrates: []string{"demo"},
	}})

	// The caller claims a different, more privileged-sounding identity.
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, statusRequest(secret, "botanist"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d — the header claim displaced the credential's Pollen: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d, want 1", executed.Load())
	}
}

// TestClaimedIdentityCannotBorrowAnothersGrant: the inverse direction. The
// grant belongs to "privileged"; the credential says "claude". Honouring the
// header would authorise the request, so it must be denied.
func TestClaimedIdentityCannotBorrowAnothersGrant(t *testing.T) {
	secret, credentials := credentialFixture(t, "claude")
	mux, executed := newCredentialGate(t, credentials, []core.DelegationGrant{{
		Pollen: "privileged", OperationClasses: []string{core.CapGitStatus}, Substrates: []string{"demo"},
	}})

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, statusRequest(secret, "privileged"))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a claimed Pollen borrowed another's grant", recorder.Code)
	}
	if executed.Load() != 0 {
		t.Fatal("the operation ran despite the credential naming a different Pollen")
	}
}

// TestUnresolvableCredentialDeniesRatherThanFallingThrough is the fail-open
// this design had to avoid: a revoked or unknown credential must NOT degrade
// into a plain, ungoverned request.
func TestUnresolvableCredentialDeniesRatherThanFallingThrough(t *testing.T) {
	_, credentials := credentialFixture(t, "claude")
	mux, executed := newCredentialGate(t, credentials, nil)

	for _, presented := range []string{"tendril_revoked-or-unknown", "tendril_"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, statusRequest(presented, ""))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("presented %q gave status %d, want 403 — an unresolvable credential fell through to the plain path", presented, recorder.Code)
		}
	}
	if executed.Load() != 0 {
		t.Fatal("an unresolvable credential still reached the execution port")
	}
}

// TestRevokedCredentialIsRefusedAtTheSurface closes the loop end to end.
func TestRevokedCredentialIsRefusedAtTheSurface(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := core.RevokePollinatorCredentials(dir, "claude"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	loaded, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	mux, executed := newCredentialGate(t, PollinatorCredentials(loaded), []core.DelegationGrant{{
		Pollen: "claude", OperationClasses: []string{core.CapGitStatus}, Substrates: []string{"demo"},
	}})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, statusRequest(secret, ""))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a revoked credential", recorder.Code)
	}
	if executed.Load() != 0 {
		t.Fatal("a revoked credential still executed the operation")
	}
}

// TestNoCredentialKeepsTheDeclaredPollenPath: Tier 1 behaviour survives for a
// Botanist's own key plus a header claim, which is what a same-principal
// terminal still uses.
func TestNoCredentialKeepsTheDeclaredPollenPath(t *testing.T) {
	_, credentials := credentialFixture(t, "claude")
	mux, executed := newCredentialGate(t, credentials, []core.DelegationGrant{{
		Pollen: "claude", OperationClasses: []string{core.CapGitStatus}, Substrates: []string{"demo"},
	}})

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, statusRequest("", "claude"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the declared-Pollen path regressed: %s", recorder.Code, recorder.Body.String())
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d, want 1", executed.Load())
	}
}
