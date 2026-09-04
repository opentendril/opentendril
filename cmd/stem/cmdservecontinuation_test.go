package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestReconcileServeSeedWorkDisabledHistoryIsNoop(t *testing.T) {
	if err := reconcileServeSeedWork(context.Background(), core.NewService(nil), nil); err != nil {
		t.Fatalf("disabled history: %v", err)
	}
}

func TestReconcileServeSeedWorkBeforeMuxTerminalizesOrphans(t *testing.T) {
	ctx := context.Background()
	store, err := historydb.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mustServeSeed := func(handle, phytomer, status string) {
		t.Helper()
		if err := store.RecordSeedRun(ctx, historydb.SeedRun{
			Handle: handle, Pollen: "claude", PhytomerID: phytomer, Substrate: "myrepo",
			Status: status, StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("record %s: %v", handle, err)
		}
	}
	mustAccept := func(phytomer, handle, key, intent string) historydb.Continuation {
		t.Helper()
		rec, err := store.AcceptContinuation(ctx, historydb.ContinuationAcceptance{
			PhytomerID: phytomer, Pollen: "claude", Substrate: "myrepo", Handle: handle,
			IdempotencyKey: key, Intent: intent,
		})
		if err != nil {
			t.Fatalf("accept %s: %v", key, err)
		}
		return rec
	}

	mustServeSeed("seed-running-pending", "tendril-running-pending", core.SeedStatusRunning)
	runningPending := mustAccept("tendril-running-pending", "seed-running-pending", "p1", "pending on running")

	mustServeSeed("seed-settling-pending", "tendril-settling-pending", core.SeedStatusRunning)
	settlingPending := mustAccept("tendril-settling-pending", "seed-settling-pending", "p2", "pending on settling")
	mustServeSeed("seed-settling-pending", "tendril-settling-pending", core.SeedStatusSettling)

	mustServeSeed("seed-running-delivered", "tendril-running-delivered", core.SeedStatusRunning)
	delivered := mustAccept("tendril-running-delivered", "seed-running-delivered", "d1", "already delivered")
	deliveredTarget := historydb.SeedTarget{PhytomerID: "tendril-running-delivered", Handle: "seed-running-delivered", Pollen: "claude", Substrate: "myrepo"}
	if _, err := store.ClaimPendingContinuations(ctx, deliveredTarget); err != nil {
		t.Fatalf("claim delivered: %v", err)
	}
	if err := store.MarkContinuationsDelivered(ctx, deliveredTarget, []string{delivered.ContinuationID}); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	mustServeSeed("seed-running-delivering", "tendril-running-delivering", core.SeedStatusRunning)
	delivering := mustAccept("tendril-running-delivering", "seed-running-delivering", "g1", "in flight")
	if _, err := store.ClaimPendingContinuations(ctx, historydb.SeedTarget{
		PhytomerID: "tendril-running-delivering", Handle: "seed-running-delivering", Pollen: "claude", Substrate: "myrepo",
	}); err != nil {
		t.Fatalf("claim delivering: %v", err)
	}

	handlesBefore, err := store.ListContinuationsByPhytomer(ctx, "tendril-running-pending")
	if err != nil || len(handlesBefore) != 1 {
		t.Fatalf("pre-reconcile pending: %+v err=%v", handlesBefore, err)
	}

	manager, err := session.NewManager(ctx, store)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	svc := buildServeCore(manager, t.TempDir(), store, eventbus.New())
	if err := reconcileServeSeedWork(ctx, svc, store); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	assertOrphan := func(phytomer, continuationID, wantState string) {
		t.Helper()
		seed, ok, err := store.GetSeedRunByPhytomer(ctx, phytomer)
		if err != nil || !ok || seed.Status != core.SeedStatusWithered || seed.FinishedAt.IsZero() {
			t.Fatalf("%s seed = %+v ok=%v err=%v", phytomer, seed, ok, err)
		}
		if seed.Error != core.ErrSeedInterruptedByRestart.Error() {
			t.Fatalf("%s error = %q", phytomer, seed.Error)
		}
		rec, ok, err := store.GetContinuation(ctx, continuationID)
		if err != nil || !ok || rec.DeliveryState != wantState {
			t.Fatalf("%s continuation = %+v ok=%v err=%v", continuationID, rec, ok, err)
		}
	}
	assertOrphan("tendril-running-pending", runningPending.ContinuationID, core.ContinuationDeliveryFailed)
	assertOrphan("tendril-settling-pending", settlingPending.ContinuationID, core.ContinuationDeliveryFailed)
	assertOrphan("tendril-running-delivered", delivered.ContinuationID, core.ContinuationDeliveryDelivered)
	assertOrphan("tendril-running-delivering", delivering.ContinuationID, core.ContinuationDeliveryFailed)

	for _, phytomer := range []string{"tendril-running-pending", "tendril-settling-pending", "tendril-running-delivered", "tendril-running-delivering"} {
		seed, ok, err := store.GetSeedRunByPhytomer(ctx, phytomer)
		if err != nil || !ok {
			t.Fatalf("post-reconcile %s: ok=%v err=%v", phytomer, ok, err)
		}
		if seed.Handle == "" || seed.PhytomerID != phytomer {
			t.Fatalf("reconcile created a replacement identity: %+v", seed)
		}
	}

	if err := reconcileServeSeedWork(ctx, svc, store); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	got, ok, err := store.GetContinuation(ctx, delivered.ContinuationID)
	if err != nil || !ok || got.DeliveryState != core.ContinuationDeliveryDelivered {
		t.Fatalf("idempotent delivered = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestReconcileServeSeedWorkFailsClosedWhenDurableHistoryCannotReconcile(t *testing.T) {
	err := reconcileServeSeedWork(context.Background(), nil, &historydb.Store{})
	if !errors.Is(err, core.ErrContinuationNotWired) {
		t.Fatalf("missing core with durable history: %v", err)
	}
}
