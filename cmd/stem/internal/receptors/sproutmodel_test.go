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

// newSproutModelHandler builds a sprout REST surface over a Core whose
// execution port reports a resolved model the request never asked for — which
// is the ordinary case for an autonomous run and the one the record used to
// lose.
func newSproutModelHandler(t *testing.T, report core.SproutRunReport, runErr error) (*http.ServeMux, *historydb.Store) {
	t.Helper()

	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	coreSvc := core.NewService(manager).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			if strings.TrimSpace(spec.Model) != "" {
				t.Errorf("spec.Model = %q, want empty: this test covers the run that pinned nothing", spec.Model)
			}
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
	return mux, store
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

func waitForSproutRun(t *testing.T, store *historydb.Store, runID string, wantStatus string) historydb.SproutRun {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	var last historydb.SproutRun
	for time.Now().Before(deadline) {
		runs, err := store.LoadSproutRuns(context.Background(), "", 50)
		if err != nil {
			t.Fatalf("LoadSproutRuns: %v", err)
		}
		for _, run := range runs {
			if run.RunID != runID {
				continue
			}
			last = run
			if run.Status == wantStatus {
				return run
			}
		}
		// poll: the detached goroutine records the run, so re-read the store until the deadline above.
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %q never reached status %q; last seen %+v", runID, wantStatus, last)
	return historydb.SproutRun{}
}

// The detached REST path is the one an unattended run takes, and it recorded no
// model on any of its writes. The stored record is the only account such a run
// leaves, so a null model there is a run whose cost and behaviour cannot be
// attributed to anything.
func TestDetachedSproutRunRecordsTheResolvedModel(t *testing.T) {
	mux, store := newSproutModelHandler(t, core.SproutRunReport{
		Output: "grown", Outcome: "complete",
		Provider: "google", Model: "gemini-2.5-pro",
	}, nil)

	stepID := growSproutDetached(t, mux)
	run := waitForSproutRun(t, store, stepID, "matured")

	if run.Model != "gemini-2.5-pro" {
		t.Fatalf("stored model = %q, want gemini-2.5-pro", run.Model)
	}
	if run.Output != "grown" {
		t.Fatalf("stored output = %q, want grown", run.Output)
	}
}

// A run that failed still ran against a model, and a failure is the record most
// likely to be read afterwards.
func TestDetachedSproutRunRecordsTheModelOnAWitheredRun(t *testing.T) {
	mux, store := newSproutModelHandler(t, core.SproutRunReport{
		Outcome: "failed", Provider: "anthropic", Model: "claude-opus-4-8",
	}, context.DeadlineExceeded)

	stepID := growSproutDetached(t, mux)
	run := waitForSproutRun(t, store, stepID, "withered")

	if run.Model != "claude-opus-4-8" {
		t.Fatalf("stored model = %q, want claude-opus-4-8", run.Model)
	}
	if run.Error == "" {
		t.Fatalf("stored run carries no error: %+v", run)
	}
}
