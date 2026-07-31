package receptors

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestWriteCoreErr_ConductorWorkspaceAbsentMapsTo409(t *testing.T) {
	err := conductor.ErrWorkspaceAbsent
	w := httptest.NewRecorder()

	writeCoreErr(w, err)

	if w.Code != http.StatusConflict {
		t.Errorf("writeCoreErr(ErrWorkspaceAbsent) got status %d, want %d", w.Code, http.StatusConflict)
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
