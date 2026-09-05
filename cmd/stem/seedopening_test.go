package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestDetachedSeedGrowCollisionDoesNotMutateExistingRow(t *testing.T) {
	ctx := context.Background()
	store, err := historydb.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	started := time.Now().UTC().Add(-time.Hour)
	finished := started.Add(5 * time.Minute)
	existing := historydb.SeedRun{
		Handle:     "seed-collision",
		Pollen:     "claude",
		PhytomerID: "tendril-existing",
		Substrate:  "core",
		Goal:       "original goal",
		Status:     core.SeedStatusSatisfied,
		Iterations: 4,
		Branch:     "tendril/existing",
		Commit:     "cafebabe",
		Diff:       "existing diff",
		Logs:       "existing logs",
		StartedAt:  started,
		FinishedAt: finished,
	}
	if err := store.RecordSeedOpening(ctx, existing); err != nil {
		t.Fatalf("seed existing row: %v", err)
	}

	manager, err := session.NewManager(ctx, store)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	var runs atomic.Int64
	svc := core.NewService(manager).
		WithSeed(core.SeedOperations{
			Run: func(context.Context, core.SeedSpec, *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
				runs.Add(1)
				t.Error("executor started after handle collision")
				return core.SeedGrowResult{}, nil
			},
		}).
		WithSeedPersistence(seedPersistence(store)).
		WithContinuationPersistence(continuationPersistence(store)).
		WithSeedHandleMint(func() (string, error) { return "seed-collision", nil })

	result, err := svc.SeedGrow(core.WithPollen(ctx, "claude"), core.SeedGrowInput{
		Substrate: "core",
		Goal:      "make the tests pass",
		Verify:    []string{"true"},
		Detached:  true,
	})
	if err == nil {
		t.Fatalf("colliding detached grow returned %+v", result)
	}
	if !errors.Is(err, historydb.ErrSeedHandleExists) {
		t.Fatalf("collision error = %v, want ErrSeedHandleExists", err)
	}
	if result.Status == core.SeedStatusRunning || result.Handle != "" {
		t.Fatalf("running dispatch leaked: %+v", result)
	}
	if runs.Load() != 0 {
		t.Fatalf("executor started %d time(s)", runs.Load())
	}

	got, found, err := store.GetSeedRun(ctx, "seed-collision")
	if err != nil || !found {
		t.Fatalf("existing row missing: found=%v err=%v", found, err)
	}
	if got.Pollen != existing.Pollen || got.PhytomerID != existing.PhytomerID || got.Substrate != existing.Substrate {
		t.Fatalf("ownership mutated: %+v", got)
	}
	if got.Status != existing.Status || got.Iterations != existing.Iterations || got.Branch != existing.Branch || got.Commit != existing.Commit || got.Diff != existing.Diff || got.Logs != existing.Logs {
		t.Fatalf("Fruit/result mutated: %+v", got)
	}
}
