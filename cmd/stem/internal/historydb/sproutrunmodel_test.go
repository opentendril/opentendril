package historydb

import (
	"context"
	"testing"
	"time"
)

// A run's model is not known when the run opens: the provider is resolved
// inside the run. So the opening record carries no model, and the finishing one
// has to be able to add it. The upsert used to update status, output, error and
// finishedAt only, which meant a model supplied at the end was discarded and
// every stored run read as having no model at all.
func TestRecordSproutRunSettlesTheModelOnTheFinishingCall(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-1", SessionID: "s1", StepID: "run-1",
		Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun(running): %v", err)
	}

	stored := loadRun(t, store, "run-1")
	if stored.Model != "" {
		t.Fatalf("opening record model = %q, want empty (nothing has resolved yet)", stored.Model)
	}

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-1", SessionID: "s1", StepID: "run-1", Model: "gemini-2.5-pro",
		Status: "matured", Output: "done", FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun(matured): %v", err)
	}

	stored = loadRun(t, store, "run-1")
	if stored.Model != "gemini-2.5-pro" {
		t.Fatalf("stored model = %q, want gemini-2.5-pro", stored.Model)
	}
	if stored.Status != "matured" || stored.Output != "done" {
		t.Fatalf("stored run = %+v, want a matured run carrying its output", stored)
	}
}

// A later write that does not know the model must not erase one an earlier
// write recorded. Taking excluded.model unconditionally would make the order of
// two truthful callers decide whether the record keeps its answer.
func TestRecordSproutRunKeepsAModelALaterCallOmits(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-2", Model: "claude-haiku-4-5", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun(running): %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-2", Status: "withered", Error: "boom", FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordSproutRun(withered): %v", err)
	}

	stored := loadRun(t, store, "run-2")
	if stored.Model != "claude-haiku-4-5" {
		t.Fatalf("stored model = %q, want the earlier record's claude-haiku-4-5", stored.Model)
	}
	if stored.Status != "withered" {
		t.Fatalf("stored status = %q, want withered", stored.Status)
	}
}

func loadRun(t *testing.T, store *Store, runID string) SproutRun {
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
	t.Fatalf("no stored run %q in %+v", runID, runs)
	return SproutRun{}
}
