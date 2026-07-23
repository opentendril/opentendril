package historydb

import (
	"context"
	"testing"
	"time"
)

func TestSeedRunRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, found, err := store.GetSeedRun(ctx, "seed-missing"); err != nil || found {
		t.Fatalf("GetSeedRun(missing) = found=%v err=%v, want false/nil", found, err)
	}

	started := time.Now().UTC()
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-1", Pollen: "claude", Substrate: "core", Goal: "make it pass",
		Status: "running", StartedAt: started,
	}); err != nil {
		t.Fatalf("record running: %v", err)
	}

	run, found, err := store.GetSeedRun(ctx, "seed-1")
	if err != nil || !found {
		t.Fatalf("get running: found=%v err=%v", found, err)
	}
	if run.Status != "running" || run.Pollen != "claude" || run.Substrate != "core" {
		t.Fatalf("running record = %+v", run)
	}

	// Settle: the same handle upserts the terminal Fruit.
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-1", Pollen: "claude", Substrate: "core", Status: "satisfied",
		Iterations: 2, Branch: "tendril/seed-1", Diff: "the diff", Logs: "the logs",
		StartedAt: started, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record settled: %v", err)
	}

	run, _, err = store.GetSeedRun(ctx, "seed-1")
	if err != nil {
		t.Fatalf("get settled: %v", err)
	}
	if run.Status != "satisfied" || run.Iterations != 2 || run.Branch != "tendril/seed-1" || run.Diff != "the diff" {
		t.Fatalf("settled record = %+v", run)
	}
	if run.FinishedAt.IsZero() {
		t.Fatal("settled record has no FinishedAt")
	}
}

func TestRecordSeedRunRequiresHandle(t *testing.T) {
	store := openTestStore(t)
	if err := store.RecordSeedRun(context.Background(), SeedRun{Status: "running"}); err == nil {
		t.Fatal("a seed run with an empty handle was accepted")
	}
}
