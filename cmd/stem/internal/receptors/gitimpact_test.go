package receptors

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestGitDelegationImpactWiring(t *testing.T) {
	// A grant covering multiple classes with a High confirm-above threshold.
	grants := []core.DelegationGrant{{
		Pollen:             "test-pollen",
		OperationClasses:   []string{core.CapGitStatus, core.CapGitCommit, core.CapGitPush},
		Substrates:         []string{"core"},
		ConfirmAboveImpact: core.DelegationImpactHigh,
	}}

	t.Run("status is low impact (authorized)", func(t *testing.T) {
		mux, _ := newGitStatusTestHandler(t, grants)
		req := httptest.NewRequest(http.MethodPost, "/v1/git/status", strings.NewReader(`{"substrate": "core"}`))
		req.Header.Set(PollenHeader, "test-pollen")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("want 200 OK, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("commit is medium impact (authorized)", func(t *testing.T) {
		mux, _, _, _ := newGitTestHandler(t, grants)
		req := httptest.NewRequest(http.MethodPost, "/v1/git/commit", strings.NewReader(`{"substrate": "core", "message": "test"}`))
		req.Header.Set(PollenHeader, "test-pollen")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("want 200 OK, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("push is high impact (denied by threshold)", func(t *testing.T) {
		mux, _, _ := newGitPushTestHandler(t, grants)
		req := httptest.NewRequest(http.MethodPost, "/v1/git/push", strings.NewReader(`{"substrate": "core", "branch": "main"}`))
		req.Header.Set(PollenHeader, "test-pollen")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "requires human confirmation") {
			t.Errorf("want 403 with confirmation required, got %d: %s", w.Code, w.Body.String())
		}
	})
}
