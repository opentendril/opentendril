package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

func TestPrepareSeedSproutPersistsUniqueOpeningRows(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := core.WithPollen(context.Background(), "claude")
	spec := core.SeedSpec{
		Substrate:  "myrepo",
		Goal:       "make it pass",
		PhytomerID: "tendril-seed-ops",
		Origin:     "rest",
	}
	var ids []string
	for i := 1; i <= 3; i++ {
		orch := conductor.NewDockerOrchestrator()
		if err := prepareSeedSprout(ctx, store, spec, orch, i); err != nil {
			t.Fatalf("prepare iteration %d: %v", i, err)
		}
		if orch.SessionID != spec.PhytomerID {
			t.Fatalf("sessionID = %q, want %q", orch.SessionID, spec.PhytomerID)
		}
		if orch.StepID == "" {
			t.Fatal("missing unique step id")
		}
		ids = append(ids, orch.StepID)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("iteration ids collided: %v", ids)
		}
		seen[id] = true
	}

	runs, err := store.LoadSproutRuns(context.Background(), spec.PhytomerID, 10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("persisted %d sprout rows, want 3", len(runs))
	}
	for _, run := range runs {
		if run.Pollen != "claude" || run.Substrate != "myrepo" || run.SessionID != spec.PhytomerID {
			t.Fatalf("opening row = %+v", run)
		}
		if run.Status != "running" {
			t.Fatalf("opening status = %q, want running", run.Status)
		}
	}
}

func TestPrepareSeedSproutDoesNotInventAFakeSproutWhenHistoryIsNil(t *testing.T) {
	orch := conductor.NewDockerOrchestrator()
	err := prepareSeedSprout(context.Background(), nil, core.SeedSpec{
		Substrate: "myrepo", PhytomerID: "tendril-seed-ops", Goal: "g",
	}, orch, 1)
	if err != nil {
		t.Fatalf("nil history prepare: %v", err)
	}
	if orch.SessionID != "tendril-seed-ops" {
		t.Fatalf("sessionID = %q", orch.SessionID)
	}
}

func TestPrepareSeedSproutRequiresPhytomer(t *testing.T) {
	err := prepareSeedSprout(context.Background(), nil, core.SeedSpec{Substrate: "myrepo"}, conductor.NewDockerOrchestrator(), 1)
	if err == nil {
		t.Fatal("missing phytomer was accepted")
	}
}
