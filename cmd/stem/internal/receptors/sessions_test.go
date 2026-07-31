package receptors

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
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
