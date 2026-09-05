package receptors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestWriteCoreErr_ConductorWorkspaceAbsentMapsTo409(t *testing.T) {
	// The resolver never returns the bare sentinel — it always wraps it with the
	// Substrate name. Asserting against the wrapped form is what pins the
	// unwrapping: a mapping written as `err == ErrWorkspaceAbsent` passes for the
	// sentinel alone and returns 500 for every value production actually produces.
	err := fmt.Errorf("%w: managed checkout for substrate %q is missing", conductor.ErrWorkspaceAbsent, "demo")
	w := httptest.NewRecorder()

	writeCoreErr(w, err)

	if w.Code != http.StatusConflict {
		t.Errorf("writeCoreErr(wrapped ErrWorkspaceAbsent) got status %d, want %d", w.Code, http.StatusConflict)
	}

	body := w.Body.String()
	if body == "" {
		t.Errorf("expected non-empty response body")
	}
}

func TestWriteCoreErr_OtherCoreErrorsMapCorrectly(t *testing.T) {
	err := core.ErrNotFound
	w := httptest.NewRecorder()

	writeCoreErr(w, err)

	if w.Code != http.StatusNotFound {
		t.Errorf("writeCoreErr(ErrNotFound) got status %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestWriteCoreErr_ContinuationFailures(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{core.ErrContinuationInvalid, http.StatusBadRequest},
		{core.ErrContinuationTargetNotFound, http.StatusNotFound},
		{core.ErrContinuationPollenMismatch, http.StatusForbidden},
		{core.ErrContinuationNotEligible, http.StatusConflict},
		{core.ErrContinuationIdempotencyConflict, http.StatusConflict},
		{core.ErrContinuationTargetChanged, http.StatusConflict},
		{core.ErrContinuationHistoryUnavailable, http.StatusServiceUnavailable},
		{core.ErrContinuationNotWired, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeCoreErr(w, tc.err)
		if w.Code != tc.want {
			t.Errorf("writeCoreErr(%v) = %d, want %d", tc.err, w.Code, tc.want)
		}
	}
}

func TestUpdatePreferencesPersistsAndReturnsSubstrate(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	sess, err := manager.Initiate(context.Background(), session.OriginREST, session.Preferences{Model: "claude-sonnet"})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}

	handler := NewSessionsHandler(core.NewService(manager), manager, nil, nil)
	req := httptest.NewRequest(http.MethodPatch, "/v1/phytomers/"+sess.ID, strings.NewReader(`{"preferences":{"substrate":"opentendril"}}`))
	req.SetPathValue("sessionId", sess.ID)
	rec := httptest.NewRecorder()
	handler.updatePreferences(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body %s", rec.Code, rec.Body.String())
	}

	var got session.Phytomer
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode PATCH body: %v", err)
	}
	if got.Preferences.Substrate != "opentendril" {
		t.Fatalf("PATCH returned preferences = %+v, want substrate opentendril", got.Preferences)
	}
	if got.Preferences.Model != "claude-sonnet" {
		t.Fatalf("PATCH dropped existing model: %+v", got.Preferences)
	}

	stored, ok := manager.Get(context.Background(), sess.ID)
	if !ok {
		t.Fatal("session missing after PATCH")
	}
	if stored.Preferences.Substrate != "opentendril" {
		t.Fatalf("persisted preferences = %+v, want substrate opentendril", stored.Preferences)
	}
}

func TestCreateSessionAcceptsPreferencesSubstrate(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	handler := NewSessionsHandler(core.NewService(manager), manager, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/phytomers", strings.NewReader(`{"origin":"ws","preferences":{"substrate":"opentendril"}}`))
	rec := httptest.NewRecorder()
	handler.create(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body %s", rec.Code, rec.Body.String())
	}

	var got session.Phytomer
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if got.Preferences.Substrate != "opentendril" {
		t.Fatalf("create returned preferences = %+v, want substrate opentendril", got.Preferences)
	}
}
