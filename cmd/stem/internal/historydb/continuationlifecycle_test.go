package historydb

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestListPendingContinuationsSequenceAndExclusions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}

	first, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k1", Intent: "first",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k2", Intent: "second",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Sequence >= second.Sequence {
		t.Fatalf("sequence order = %d, %d", first.Sequence, second.Sequence)
	}

	pending, err := store.ListPendingContinuations(ctx, target)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 2 || pending[0].ContinuationID != first.ContinuationID || pending[1].ContinuationID != second.ContinuationID {
		t.Fatalf("pending = %+v", pending)
	}

	claimed, err := store.ClaimPendingContinuations(ctx, target)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	if err := store.MarkContinuationsDelivered(ctx, target, []string{first.ContinuationID}); err != nil {
		t.Fatalf("deliver first: %v", err)
	}
	if _, err := store.AccountSeedTerminalFailure(ctx, target, SeedRun{
		Status: seedStatusWithered, Error: "sprout failed", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("terminalize to fail remaining: %v", err)
	}

	mustRecordRunningSeed(t, store, "seed-2", "tendril-2", "claude", "myrepo")
	target2 := SeedTarget{PhytomerID: "tendril-2", Handle: "seed-2", Pollen: "claude", Substrate: "myrepo"}
	third, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-2", Pollen: "claude", Substrate: "myrepo", Handle: "seed-2",
		IdempotencyKey: "k3", Intent: "third",
	})
	if err != nil {
		t.Fatalf("third: %v", err)
	}

	pending, err = store.ListPendingContinuations(ctx, target)
	if err != nil {
		t.Fatalf("list after delivery/fail: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("delivered/failed rows returned pending: %+v", pending)
	}
	gotFirst, ok, err := store.GetContinuation(ctx, first.ContinuationID)
	if err != nil || !ok || gotFirst.DeliveryState != continuationDeliveryDelivered || gotFirst.DeliveredAt.IsZero() {
		t.Fatalf("delivered row = %+v ok=%v err=%v", gotFirst, ok, err)
	}
	gotSecond, ok, err := store.GetContinuation(ctx, second.ContinuationID)
	if err != nil || !ok || gotSecond.DeliveryState != continuationDeliveryFailed || gotSecond.FailedAt.IsZero() {
		t.Fatalf("failed row = %+v ok=%v err=%v", gotSecond, ok, err)
	}
	pending2, err := store.ListPendingContinuations(ctx, target2)
	if err != nil || len(pending2) != 1 || pending2[0].ContinuationID != third.ContinuationID {
		t.Fatalf("other phytomer pending = %+v err=%v", pending2, err)
	}
}

func TestContinuationDeliveryTransitions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}
	rec, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k1", Intent: "keep going",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	if err := store.MarkContinuationsDelivered(ctx, target, []string{rec.ContinuationID}); !errors.Is(err, ErrContinuationDeliveryState) {
		t.Fatalf("pending -> delivered without delivering: %v", err)
	}

	claimed, err := store.ClaimPendingContinuations(ctx, target)
	if err != nil || len(claimed) != 1 || claimed[0].DeliveryState != continuationDeliveryDelivering {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	secondClaim, err := store.ClaimPendingContinuations(ctx, target)
	if err != nil || len(secondClaim) != 0 {
		t.Fatalf("second claim replayed delivering row: %+v err=%v", secondClaim, err)
	}
	if err := store.MarkContinuationsDelivered(ctx, target, []string{rec.ContinuationID}); err != nil {
		t.Fatalf("delivering -> delivered: %v", err)
	}
	if err := store.MarkContinuationsDelivered(ctx, target, []string{rec.ContinuationID}); !errors.Is(err, ErrContinuationDeliveryState) {
		t.Fatalf("delivered -> delivered: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE continuations SET deliveryState = ? WHERE continuationId = ?`, continuationDeliveryPending, rec.ContinuationID); err != nil {
		t.Fatalf("force pending: %v", err)
	}
	if err := store.MarkContinuationsDelivered(ctx, target, []string{rec.ContinuationID}); !errors.Is(err, ErrContinuationDeliveryState) {
		t.Fatalf("forced delivered -> pending then deliver: %v", err)
	}

	failed, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k2", Intent: "another",
	})
	if err != nil {
		t.Fatalf("second accept: %v", err)
	}
	if _, err := store.AccountSeedTerminalFailure(ctx, target, SeedRun{Status: seedStatusWithered, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("fail unresolved: %v", err)
	}
	got, ok, err := store.GetContinuation(ctx, failed.ContinuationID)
	if err != nil || !ok || got.DeliveryState != continuationDeliveryFailed {
		t.Fatalf("failed continuation = %+v ok=%v err=%v", got, ok, err)
	}
	if err := store.MarkContinuationsDelivered(ctx, target, []string{failed.ContinuationID}); !errors.Is(err, ErrContinuationDeliveryState) {
		t.Fatalf("failed -> delivered: %v", err)
	}
}

func TestContinuationLifecycleRefusesStaleAndCrossPollenTargets(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	if _, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k1", Intent: "keep going",
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}

	cases := []SeedTarget{
		{PhytomerID: "tendril-1", Handle: "seed-other", Pollen: "claude", Substrate: "myrepo"},
		{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "other", Substrate: "myrepo"},
		{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "other-repo"},
		{PhytomerID: "tendril-missing", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"},
	}
	for _, target := range cases {
		if _, err := store.ListPendingContinuations(ctx, target); err == nil {
			t.Fatalf("list pending accepted stale target %+v", target)
		}
		if _, err := store.ClaimPendingContinuations(ctx, target); err == nil {
			t.Fatalf("claim accepted stale target %+v", target)
		}
		if _, err := store.AcquireSeedSettlementFence(ctx, target); err == nil {
			t.Fatalf("fence accepted stale target %+v", target)
		}
		if _, err := store.HasUnresolvedContinuations(ctx, target); err == nil {
			t.Fatalf("unresolved accepted stale target %+v", target)
		}
	}
}

func TestLocalEmptyPollenSeedTargetLifecycle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-local", "tendril-local", "", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-local", Handle: "seed-local", Pollen: "", Substrate: "myrepo"}

	claimed, err := store.ClaimPendingContinuations(ctx, target)
	if err != nil {
		t.Fatalf("claim empty-pollen target: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed = %+v", claimed)
	}
	fenced, err := store.AcquireSeedSettlementFence(ctx, target)
	if err != nil || !fenced {
		t.Fatalf("fence empty-pollen target: fenced=%v err=%v", fenced, err)
	}
	if err := store.CompleteSeedSettlement(ctx, target, SeedRun{
		Status: seedStatusSatisfied, Iterations: 1, Diff: "diff",
	}); err != nil {
		t.Fatalf("complete empty-pollen settlement: %v", err)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, "tendril-local")
	if err != nil || !ok || seed.Status != seedStatusSatisfied || seed.Pollen != "" {
		t.Fatalf("settled local seed = %+v ok=%v err=%v", seed, ok, err)
	}
}

func TestEmptyPollenContinuationClaimDeliveryAndFence(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-local", "tendril-local", "", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-local", Handle: "seed-local", Pollen: "", Substrate: "myrepo"}
	rec, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-local", Pollen: "", Substrate: "myrepo", Handle: "seed-local",
		IdempotencyKey: "k-local", Intent: "keep going locally",
	})
	if err != nil {
		t.Fatalf("accept empty pollen: %v", err)
	}
	if rec.Pollen != "" || rec.DeliveryState != continuationDeliveryPending {
		t.Fatalf("record = %+v", rec)
	}

	claimed, err := store.ClaimPendingContinuations(ctx, target)
	if err != nil || len(claimed) != 1 || claimed[0].ContinuationID != rec.ContinuationID {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	if err := store.MarkContinuationsDelivered(ctx, target, []string{rec.ContinuationID}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	fenced, err := store.AcquireSeedSettlementFence(ctx, target)
	if err != nil || !fenced {
		t.Fatalf("fence after empty-pollen delivery: fenced=%v err=%v", fenced, err)
	}
	if err := store.CompleteSeedSettlement(ctx, target, SeedRun{Status: seedStatusSatisfied, Iterations: 1}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestSeedTargetPollenMismatchEmptyVersusDelegated(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-codex", "tendril-codex", "codex", "myrepo")
	mustRecordRunningSeed(t, store, "seed-local", "tendril-local", "", "myrepo")

	delegatedEmpty := SeedTarget{PhytomerID: "tendril-codex", Handle: "seed-codex", Pollen: "", Substrate: "myrepo"}
	if _, err := store.ClaimPendingContinuations(ctx, delegatedEmpty); !errors.Is(err, ErrContinuationPollenMismatch) {
		t.Fatalf("delegated seed vs empty pollen: %v", err)
	}
	if _, err := store.AcquireSeedSettlementFence(ctx, delegatedEmpty); !errors.Is(err, ErrContinuationPollenMismatch) {
		t.Fatalf("fence delegated vs empty: %v", err)
	}

	localDelegated := SeedTarget{PhytomerID: "tendril-local", Handle: "seed-local", Pollen: "codex", Substrate: "myrepo"}
	if _, err := store.ClaimPendingContinuations(ctx, localDelegated); !errors.Is(err, ErrContinuationPollenMismatch) {
		t.Fatalf("local seed vs delegated pollen: %v", err)
	}
	if _, err := store.AcquireSeedSettlementFence(ctx, localDelegated); !errors.Is(err, ErrContinuationPollenMismatch) {
		t.Fatalf("fence local vs delegated: %v", err)
	}
}

func TestSettlementFenceNoPendingAcquiresSettling(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}

	fenced, err := store.AcquireSeedSettlementFence(ctx, target)
	if err != nil || !fenced {
		t.Fatalf("fence: fenced=%v err=%v", fenced, err)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, "tendril-1")
	if err != nil || !ok || seed.Status != seedStatusSettling {
		t.Fatalf("seed after fence = %+v ok=%v err=%v", seed, ok, err)
	}
	_, err = store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "late", Intent: "too late",
	})
	if !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("accept after fence: %v", err)
	}
}

func TestSettlementFenceRefusesWhenAcceptanceWinsFirst(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}
	if _, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k1", Intent: "keep going",
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	fenced, err := store.AcquireSeedSettlementFence(ctx, target)
	if err != nil || fenced {
		t.Fatalf("fence after accept: fenced=%v err=%v", fenced, err)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, "tendril-1")
	if err != nil || !ok || seed.Status != seedStatusRunning {
		t.Fatalf("seed status after refused fence = %+v ok=%v err=%v", seed, ok, err)
	}
}

func TestSettlementFenceBeforeAcceptRejectsLaterAccept(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}
	fenced, err := store.AcquireSeedSettlementFence(ctx, target)
	if err != nil || !fenced {
		t.Fatalf("fence: fenced=%v err=%v", fenced, err)
	}
	_, err = store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k1", Intent: "keep going",
	})
	if !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("later accept: %v", err)
	}
}

func TestConcurrentAcceptVersusSettlementFence(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var acceptErr, fenceErr error
	var fenced bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, acceptErr = store.AcceptContinuation(ctx, ContinuationAcceptance{
			PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
			IdempotencyKey: "race", Intent: "keep going",
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		fenced, fenceErr = store.AcquireSeedSettlementFence(ctx, target)
	}()
	close(start)
	wg.Wait()
	if fenceErr != nil {
		t.Fatalf("fence: %v", fenceErr)
	}

	listed, err := store.ListContinuationsByPhytomer(ctx, "tendril-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, "tendril-1")
	if err != nil || !ok {
		t.Fatalf("seed: %+v ok=%v err=%v", seed, ok, err)
	}

	if fenced {
		if acceptErr == nil {
			t.Fatal("fence won but accept also succeeded")
		}
		if !errors.Is(acceptErr, ErrContinuationNotEligible) {
			t.Fatalf("accept after fence-win: %v", acceptErr)
		}
		if seed.Status != seedStatusSettling {
			t.Fatalf("fence-win status = %q", seed.Status)
		}
		if len(listed) != 0 {
			t.Fatalf("fence-win left continuations: %+v", listed)
		}
	} else {
		if acceptErr != nil {
			t.Fatalf("accept-win: %v", acceptErr)
		}
		if seed.Status != seedStatusRunning {
			t.Fatalf("accept-win status = %q", seed.Status)
		}
		if len(listed) != 1 || listed[0].DeliveryState != continuationDeliveryPending {
			t.Fatalf("accept-win continuations = %+v", listed)
		}
	}
}

func TestCompleteSeedSettlementRequiresSettlingAndNoUnresolved(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}

	err := store.CompleteSeedSettlement(ctx, target, SeedRun{Status: seedStatusSatisfied, Iterations: 1})
	if !errors.Is(err, ErrSeedSettlementNotFenced) {
		t.Fatalf("complete while running: %v", err)
	}

	if _, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "k1", Intent: "keep going",
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE seedruns SET status = ? WHERE handle = ?`, seedStatusSettling, "seed-1"); err != nil {
		t.Fatalf("force settling: %v", err)
	}
	err = store.CompleteSeedSettlement(ctx, target, SeedRun{Status: seedStatusSatisfied, Iterations: 1, Diff: "diff"})
	if !errors.Is(err, ErrSeedSettlementNotFenced) {
		t.Fatalf("complete with unresolved: %v", err)
	}

	if _, err := store.AccountSeedTerminalFailure(ctx, target, SeedRun{Status: seedStatusWithered}); err != nil {
		t.Fatalf("clear unresolved: %v", err)
	}
	mustRecordRunningSeed(t, store, "seed-2", "tendril-2", "claude", "myrepo")
	target2 := SeedTarget{PhytomerID: "tendril-2", Handle: "seed-2", Pollen: "claude", Substrate: "myrepo"}
	fenced, err := store.AcquireSeedSettlementFence(ctx, target2)
	if err != nil || !fenced {
		t.Fatalf("fence: fenced=%v err=%v", fenced, err)
	}
	if err := store.CompleteSeedSettlement(ctx, target2, SeedRun{
		Status: seedStatusSatisfied, Iterations: 2, Branch: "tendril/seed", Commit: "abc", Diff: "the diff", Logs: "logs",
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, "tendril-2")
	if err != nil || !ok || seed.Status != seedStatusSatisfied || seed.Commit != "abc" || seed.FinishedAt.IsZero() {
		t.Fatalf("settled = %+v ok=%v err=%v", seed, ok, err)
	}
}

func TestAccountSeedTerminalFailureFailsUnresolvedAtomically(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	target := SeedTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}
	pending, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "pending", Intent: "pending intent",
	})
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	claimed, err := store.ClaimPendingContinuations(ctx, target)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	delivered, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "delivered", Intent: "already delivered",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, err := store.ClaimPendingContinuations(ctx, target); err != nil {
		t.Fatalf("claim delivered candidate: %v", err)
	}
	if err := store.MarkContinuationsDelivered(ctx, target, []string{delivered.ContinuationID}); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	account, err := store.AccountSeedTerminalFailure(ctx, target, SeedRun{
		Status: seedStatusExhausted, Iterations: 3, Logs: "verify failed", Error: "nominal exhausted",
	})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UnresolvedFailed != 1 {
		t.Fatalf("unresolved failed = %d, want 1", account.UnresolvedFailed)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, "tendril-1")
	if err != nil || !ok || seed.Status != seedStatusWithered || seed.Error != continuationUndeliverableError {
		t.Fatalf("seed after undelivered accounting = %+v ok=%v err=%v", seed, ok, err)
	}
	gotPending, ok, err := store.GetContinuation(ctx, pending.ContinuationID)
	if err != nil || !ok || gotPending.DeliveryState != continuationDeliveryFailed || gotPending.FailedAt.IsZero() {
		t.Fatalf("unresolved after account = %+v ok=%v err=%v", gotPending, ok, err)
	}
	gotDelivered, ok, err := store.GetContinuation(ctx, delivered.ContinuationID)
	if err != nil || !ok || gotDelivered.DeliveryState != continuationDeliveryDelivered {
		t.Fatalf("delivered after account = %+v ok=%v err=%v", gotDelivered, ok, err)
	}
	_, err = store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Handle: "seed-1",
		IdempotencyKey: "after", Intent: "too late",
	})
	if !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("accept after terminalize: %v", err)
	}
}

func TestReconcileOrphanedSeedWorkIsAtomicAndIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	mustRecordRunningSeed(t, store, "seed-running-pending", "tendril-running-pending", "claude", "myrepo")
	if _, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-running-pending", Pollen: "claude", Substrate: "myrepo", Handle: "seed-running-pending",
		IdempotencyKey: "p1", Intent: "pending on running",
	}); err != nil {
		t.Fatalf("running pending: %v", err)
	}

	mustRecordRunningSeed(t, store, "seed-settling-pending", "tendril-settling-pending", "claude", "myrepo")
	if _, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-settling-pending", Pollen: "claude", Substrate: "myrepo", Handle: "seed-settling-pending",
		IdempotencyKey: "p2", Intent: "pending on settling",
	}); err != nil {
		t.Fatalf("settling pending: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE seedruns SET status = ? WHERE handle = ?`, seedStatusSettling, "seed-settling-pending"); err != nil {
		t.Fatalf("force settling: %v", err)
	}

	mustRecordRunningSeed(t, store, "seed-running-delivered", "tendril-running-delivered", "claude", "myrepo")
	delivered, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-running-delivered", Pollen: "claude", Substrate: "myrepo", Handle: "seed-running-delivered",
		IdempotencyKey: "d1", Intent: "already delivered",
	})
	if err != nil {
		t.Fatalf("delivered accept: %v", err)
	}
	deliveredTarget := SeedTarget{PhytomerID: "tendril-running-delivered", Handle: "seed-running-delivered", Pollen: "claude", Substrate: "myrepo"}
	if _, err := store.ClaimPendingContinuations(ctx, deliveredTarget); err != nil {
		t.Fatalf("claim delivered: %v", err)
	}
	if err := store.MarkContinuationsDelivered(ctx, deliveredTarget, []string{delivered.ContinuationID}); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	mustRecordRunningSeed(t, store, "seed-running-delivering", "tendril-running-delivering", "claude", "myrepo")
	delivering, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-running-delivering", Pollen: "claude", Substrate: "myrepo", Handle: "seed-running-delivering",
		IdempotencyKey: "g1", Intent: "in flight",
	})
	if err != nil {
		t.Fatalf("delivering accept: %v", err)
	}
	if _, err := store.ClaimPendingContinuations(ctx, SeedTarget{
		PhytomerID: "tendril-running-delivering", Handle: "seed-running-delivering", Pollen: "claude", Substrate: "myrepo",
	}); err != nil {
		t.Fatalf("claim delivering: %v", err)
	}

	if err := store.ReconcileOrphanedSeedWork(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	assertReconciledOrphan := func(phytomerID, continuationID, wantContinuationState string) {
		t.Helper()
		seed, ok, err := store.GetSeedRunByPhytomer(ctx, phytomerID)
		if err != nil || !ok || seed.Status != seedStatusWithered || seed.FinishedAt.IsZero() || seed.Error != seedRestartInterruptedError {
			t.Fatalf("%s seed = %+v ok=%v err=%v", phytomerID, seed, ok, err)
		}
		rec, ok, err := store.GetContinuation(ctx, continuationID)
		if err != nil || !ok || rec.DeliveryState != wantContinuationState {
			t.Fatalf("%s continuation = %+v ok=%v err=%v", continuationID, rec, ok, err)
		}
		if wantContinuationState == continuationDeliveryFailed && rec.FailedAt.IsZero() {
			t.Fatalf("%s missing failedAt", continuationID)
		}
	}
	pendingRunning, err := store.ListContinuationsByPhytomer(ctx, "tendril-running-pending")
	if err != nil || len(pendingRunning) != 1 {
		t.Fatalf("running-pending list: %+v err=%v", pendingRunning, err)
	}
	assertReconciledOrphan("tendril-running-pending", pendingRunning[0].ContinuationID, continuationDeliveryFailed)
	settlingPending, err := store.ListContinuationsByPhytomer(ctx, "tendril-settling-pending")
	if err != nil || len(settlingPending) != 1 {
		t.Fatalf("settling-pending list: %+v err=%v", settlingPending, err)
	}
	assertReconciledOrphan("tendril-settling-pending", settlingPending[0].ContinuationID, continuationDeliveryFailed)
	assertReconciledOrphan("tendril-running-delivered", delivered.ContinuationID, continuationDeliveryDelivered)
	assertReconciledOrphan("tendril-running-delivering", delivering.ContinuationID, continuationDeliveryFailed)

	if err := store.ReconcileOrphanedSeedWork(ctx); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, "tendril-running-delivered")
	if err != nil || !ok || seed.Status != seedStatusWithered {
		t.Fatalf("idempotent seed = %+v ok=%v err=%v", seed, ok, err)
	}
	gotDelivered, ok, err := store.GetContinuation(ctx, delivered.ContinuationID)
	if err != nil || !ok || gotDelivered.DeliveryState != continuationDeliveryDelivered {
		t.Fatalf("idempotent delivered = %+v ok=%v err=%v", gotDelivered, ok, err)
	}
}
