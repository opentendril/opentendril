package receptors

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func newGitFetchAdapterFixtures(t *testing.T, grantOperation, grantSubstrate string, fetchErr error) (*http.ServeMux, *MCPHandler, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	svc := core.NewService(nil).WithGit(core.GitOperations{
		Fetch: func(_ context.Context, spec core.GitFetchSpec) (core.GitFetchResult, error) {
			calls.Add(1)
			if fetchErr != nil {
				return core.GitFetchResult{}, fetchErr
			}
			return core.GitFetchResult{Status: "fetched", Substrate: spec.Substrate, Remote: "origin", Changes: []core.GitFetchRefChange{}}, nil
		},
	})
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{{
		Pollen: "fetch-pollen", OperationClasses: []string{grantOperation}, Substrates: []string{grantSubstrate},
	}})}
	mux := http.NewServeMux()
	NewGitHandler(svc).WithDelegation(gate).Register(mux, nil)
	mcp := NewMCPHandler().WithCore(svc).WithDelegation(gate, "fetch-pollen")
	return mux, mcp, calls
}

func gitFetchRESTRequest(mux *http.ServeMux, body string, pollen string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/git/fetch", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if pollen != "" {
		request.Header.Set(PollenHeader, pollen)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestGitFetchRESTRequiresExactGrantAndRejectsCallerSelectedAuthority(t *testing.T) {
	body := `{"substrate":"demo","origin":"test"}`
	for _, test := range []struct {
		name      string
		operation string
		substrate string
		pollen    string
		body      string
		status    int
	}{
		{name: "exact grant", operation: core.CapGitFetch, substrate: "demo", pollen: "fetch-pollen", body: body, status: http.StatusOK},
		{name: "wrong operation does not confer", operation: core.CapGitApply, substrate: "demo", pollen: "fetch-pollen", body: body, status: http.StatusForbidden},
		{name: "wrong Substrate", operation: core.CapGitFetch, substrate: "other", pollen: "fetch-pollen", body: body, status: http.StatusForbidden},
		{name: "missing Pollen", operation: core.CapGitFetch, substrate: "demo", body: body, status: http.StatusForbidden},
		{name: "caller cannot select remote", operation: core.CapGitFetch, substrate: "demo", pollen: "fetch-pollen", body: `{"substrate":"demo","remote":"upstream"}`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			mux, _, calls := newGitFetchAdapterFixtures(t, test.operation, test.substrate, nil)
			response := gitFetchRESTRequest(mux, test.body, test.pollen)
			if response.Code != test.status {
				t.Fatalf("HTTP status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusOK {
				var result core.GitFetchResult
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if result.Substrate != "demo" || result.Remote != "origin" || calls.Load() != 1 {
					t.Fatalf("result=%#v Core calls=%d", result, calls.Load())
				}
			} else if calls.Load() != 0 {
				t.Fatalf("denied/malformed request reached Core fetch %d time(s)", calls.Load())
			}
		})
	}
}

func TestGitFetchMCPProjectsCoreAndKeepsTypedErrorsSafe(t *testing.T) {
	goodArgs := map[string]any{"substrate": "demo", "origin": "mcp"}
	t.Run("authorized", func(t *testing.T) {
		_, mcp, calls := newGitFetchAdapterFixtures(t, core.CapGitFetch, "demo", nil)
		text, isError := mcpCallTool(t, mcp, "gitFetch", goodArgs)
		if isError || calls.Load() != 1 || !strings.Contains(text, `"remote": "origin"`) {
			t.Fatalf("MCP result isError=%v calls=%d text=%s", isError, calls.Load(), text)
		}
	})
	t.Run("wrong Git grant does not confer", func(t *testing.T) {
		_, mcp, calls := newGitFetchAdapterFixtures(t, core.CapGitPush, "demo", nil)
		_, isError := mcpCallTool(t, mcp, "gitFetch", goodArgs)
		if !isError || calls.Load() != 0 {
			t.Fatalf("wrong grant result isError=%v calls=%d", isError, calls.Load())
		}
	})
	t.Run("typed failure is redacted", func(t *testing.T) {
		secret := core.GitFetchError{Category: core.GitFetchFailureRemoteIdentityMismatch}
		_, mcp, calls := newGitFetchAdapterFixtures(t, core.CapGitFetch, "demo", secret)
		text, isError := mcpCallTool(t, mcp, "gitFetch", goodArgs)
		if !isError || calls.Load() != 1 || !strings.Contains(text, core.GitFetchFailureRemoteIdentityMismatch) {
			t.Fatalf("safe failure isError=%v calls=%d text=%s", isError, calls.Load(), text)
		}
		for _, forbidden := range []string{"/home/", "https://", "token", "stderr"} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				t.Fatalf("failure disclosed %q: %s", forbidden, text)
			}
		}
	})
}

func TestGitFetchRESTMapsTypedFailureWithoutRawDiagnostics(t *testing.T) {
	fetchErr := core.GitFetchError{Category: core.GitFetchFailureRemoteIdentityMismatch}
	mux, _, calls := newGitFetchAdapterFixtures(t, core.CapGitFetch, "demo", fetchErr)
	response := gitFetchRESTRequest(mux, `{"substrate":"demo"}`, "fetch-pollen")
	if response.Code != http.StatusConflict || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
	}
	if strings.TrimSpace(response.Body.String()) != fetchErr.Error() {
		t.Fatalf("REST surfaced unsafe or inconsistent error: %q", response.Body.String())
	}
}
