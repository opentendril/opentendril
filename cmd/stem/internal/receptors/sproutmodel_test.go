package receptors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func newSproutAsyncHandler(t *testing.T, report core.SproutRunReport, runErr error) (*http.ServeMux, *historydb.Store, <-chan struct{}) {
	t.Helper()

	returned := make(chan struct{})
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	coreSvc := core.NewService(manager).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			defer close(returned)
			return report, runErr
		},
	})

	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mux := http.NewServeMux()
	NewSproutHandler(coreSvc, store, nil).Register(mux, nil)
	return mux, store, returned
}

func growSproutDetached(t *testing.T, mux *http.ServeMux) string {
	t.Helper()

	body := `{"transcript":"do the thing","substrate":"core"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/phytomers/new/sprout/grow", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		StepID string `json:"stepId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode 202 body: %v", err)
	}
	return accepted.StepID
}

func awaitInnerSproutRun(t *testing.T, returned <-chan struct{}) {
	t.Helper()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("inner SproutRun did not return")
	}
}

func loadSproutRun(t *testing.T, store *historydb.Store, runID string) historydb.SproutRun {
	t.Helper()
	runs, err := store.LoadSproutRuns(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("LoadSproutRuns: %v", err)
	}
	for _, run := range runs {
		if run.RunID == runID {
			return run
		}
	}
	t.Fatalf("run %q not found in %+v", runID, runs)
	return historydb.SproutRun{}
}

func assertReceptorLeftRunRunning(t *testing.T, store *historydb.Store, runID string) historydb.SproutRun {
	t.Helper()
	run := loadSproutRun(t, store, runID)
	if run.Status != "running" {
		t.Fatalf("status = %q, want running; the receptor must not be a terminal writer", run.Status)
	}
	if !run.FinishedAt.IsZero() {
		t.Fatalf("FinishedAt = %v, want zero on a still-running row", run.FinishedAt)
	}
	if run.Usage.Execution != nil || run.Usage.PostRun != nil {
		t.Fatalf("receptor wrote usage: %+v", run.Usage)
	}
	return run
}

func TestAsyncReceptorKeepsOpeningRowRunningAfterInnerComplete(t *testing.T) {
	mux, store, returned := newSproutAsyncHandler(t, core.SproutRunReport{
		Output: "grown", Outcome: "complete",
		Provider: "google", Model: "gemini-3.1-pro",
	}, nil)

	stepID := growSproutDetached(t, mux)
	opening := loadSproutRun(t, store, stepID)
	if opening.Status != "running" {
		t.Fatalf("opening status = %q, want running", opening.Status)
	}

	awaitInnerSproutRun(t, returned)
	assertReceptorLeftRunRunning(t, store, stepID)
}

func TestAsyncReceptorDoesNotSettleInnerDetachedResult(t *testing.T) {
	mux, store, returned := newSproutAsyncHandler(t, core.SproutRunReport{
		Outcome: "detached", Provider: "google", Model: "gemini-3.1-pro",
	}, nil)

	stepID := growSproutDetached(t, mux)
	awaitInnerSproutRun(t, returned)
	assertReceptorLeftRunRunning(t, store, stepID)
}

func TestAsyncReceptorDoesNotSettleInnerWitheredResult(t *testing.T) {
	mux, store, returned := newSproutAsyncHandler(t, core.SproutRunReport{
		Outcome: "failed", Provider: "anthropic", Model: "claude-opus-4-8",
	}, context.DeadlineExceeded)

	stepID := growSproutDetached(t, mux)
	awaitInnerSproutRun(t, returned)
	assertReceptorLeftRunRunning(t, store, stepID)
}
