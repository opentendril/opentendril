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
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

const gitApplyTestHead = "0123456789abcdef0123456789abcdef01234567"

func newGitApplyAdapterFixtures(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *MCPHandler, *atomic.Int64) {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	calls := &atomic.Int64{}
	svc := core.NewService(manager).WithGit(core.GitOperations{
		Apply: func(_ context.Context, spec core.GitApplySpec) (core.GitApplyResult, error) {
			calls.Add(1)
			return core.GitApplyResult{
				Status: "applied", Substrate: spec.Substrate, Branch: "feature/demo",
				Head: spec.ExpectedHead, ChangedPaths: []string{"a.txt", "z.txt"},
			}, nil
		},
	})
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants)}
	gitREST := NewGitHandler(svc).WithDelegation(gate)
	mux := http.NewServeMux()
	gitREST.Register(mux, nil)
	mcp := NewMCPHandler().WithCore(svc).WithDelegation(gate, "pollen-one")
	return mux, mcp, calls
}

func gitApplyTestGrant(operation string, substrate string) []core.DelegationGrant {
	return []core.DelegationGrant{{
		Pollen: "pollen-one", OperationClasses: []string{operation}, Substrates: []string{substrate},
	}}
}

func applyRESTRequest(mux *http.ServeMux, body []byte, pollen string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/git/apply", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if pollen != "" {
		req.Header.Set(PollenHeader, pollen)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	return response
}

func TestGitApplyRESTRequiresExactGrantAndTrustedPollen(t *testing.T) {
	body := []byte(`{"substrate":"demo","expectedHead":"` + gitApplyTestHead + `","patch":"patch-secret"}`)
	for _, test := range []struct {
		name   string
		grants []core.DelegationGrant
		pollen string
		status int
	}{
		{name: "correct grant", grants: gitApplyTestGrant(core.CapGitApply, "demo"), pollen: "pollen-one", status: http.StatusOK},
		{name: "missing Pollen", grants: gitApplyTestGrant(core.CapGitApply, "demo"), status: http.StatusForbidden},
		{name: "wrong Pollen", grants: gitApplyTestGrant(core.CapGitApply, "demo"), pollen: "other-pollen", status: http.StatusForbidden},
		{name: "wrong operation grant", grants: gitApplyTestGrant(core.CapGitCommit, "demo"), pollen: "pollen-one", status: http.StatusForbidden},
		{name: "unauthorized Substrate", grants: gitApplyTestGrant(core.CapGitApply, "other"), pollen: "pollen-one", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			mux, _, calls := newGitApplyAdapterFixtures(t, test.grants)
			response := applyRESTRequest(mux, body, test.pollen)
			if response.Code != test.status {
				t.Fatalf("REST status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusOK {
				if calls.Load() != 1 {
					t.Fatalf("Core GitApply calls = %d, want 1", calls.Load())
				}
				var result core.GitApplyResult
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatalf("decode REST result: %v", err)
				}
				if result.Status != "applied" || result.Substrate != "demo" || result.Head != gitApplyTestHead || !strings.Contains(response.Body.String(), `"changedPaths":["a.txt","z.txt"]`) {
					t.Fatalf("REST result = %+v, body %s", result, response.Body.String())
				}
				for _, forbidden := range []string{"patch-secret", "credential", "/tmp/", "raw Git"} {
					if strings.Contains(response.Body.String(), forbidden) {
						t.Errorf("REST result leaked %q: %s", forbidden, response.Body.String())
					}
				}
			} else if calls.Load() != 0 {
				t.Fatalf("denied REST request reached Core GitApply %d time(s)", calls.Load())
			}
		})
	}
}

func TestGitApplyRejectsInvalidRESTUTF8AndOversizeBeforeGit(t *testing.T) {
	mux, _, calls := newGitApplyAdapterFixtures(t, gitApplyTestGrant(core.CapGitApply, "demo"))
	invalid := []byte("{\"substrate\":\"demo\",\"expectedHead\":\"" + gitApplyTestHead + "\",\"patch\":\"")
	invalid = append(invalid, 0xff)
	invalid = append(invalid, []byte("\"}")...)
	if response := applyRESTRequest(mux, invalid, "pollen-one"); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 REST status = %d, want 400: %s", response.Code, response.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("invalid UTF-8 REST request reached Git")
	}

	over := core.MaxGitApplyPatchBytes + 1
	body, err := json.Marshal(core.GitApplyInput{Substrate: "demo", ExpectedHead: gitApplyTestHead, Patch: strings.Repeat("x", over)})
	if err != nil {
		t.Fatal(err)
	}
	response := applyRESTRequest(mux, body, "pollen-one")
	if response.Code < 400 {
		t.Fatalf("oversize patch REST status = %d, want error", response.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("oversize patch reached Git")
	}
	if strings.Contains(response.Body.String(), strings.Repeat("x", 64)) {
		t.Fatal("oversize patch content leaked in REST error")
	}
}

func TestGitApplyMCPUsesExactDelegatedCapability(t *testing.T) {
	goodArgs := map[string]any{
		"substrate": "demo", "expectedHead": gitApplyTestHead, "patch": "patch-secret",
	}
	t.Run("authorized", func(t *testing.T) {
		_, mcp, calls := newGitApplyAdapterFixtures(t, gitApplyTestGrant(core.CapGitApply, "demo"))
		text, isError := mcpCallTool(t, mcp, "gitApply", goodArgs)
		if isError || calls.Load() != 1 {
			t.Fatalf("MCP gitApply isError=%v calls=%d text=%s", isError, calls.Load(), text)
		}
		for _, forbidden := range []string{"patch-secret", "credential", "/tmp/", "raw Git"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("MCP result leaked %q: %s", forbidden, text)
			}
		}
	})
	for _, test := range []struct {
		name   string
		grants []core.DelegationGrant
		args   map[string]any
	}{
		{name: "wrong operation grant", grants: gitApplyTestGrant(core.CapGitCommit, "demo"), args: goodArgs},
		{name: "unauthorized substrate", grants: gitApplyTestGrant(core.CapGitApply, "other"), args: goodArgs},
		{name: "missing exact grant", grants: nil, args: goodArgs},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, mcp, calls := newGitApplyAdapterFixtures(t, test.grants)
			_, isError := mcpCallTool(t, mcp, "gitApply", test.args)
			if !isError || calls.Load() != 0 {
				t.Fatalf("denied MCP gitApply isError=%v calls=%d", isError, calls.Load())
			}
		})
	}
}
