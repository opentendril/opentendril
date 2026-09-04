package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestSeedGrowInputHasNoDetachedContinuationSurface(t *testing.T) {
	typ := reflect.TypeOf(SeedGrowInput{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name + field.Tag.Get("json"))
		if strings.Contains(name, "async") || strings.Contains(name, "detach") || strings.Contains(name, "continue") {
			t.Fatalf("SeedGrowInput field %s introduces a detached/continuation surface", field.Name)
		}
	}
}

func TestSynchronousSeedGrowDoesNotReceiveContinuationLifecycle(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	var got *SeedContinuationLifecycle
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, lifecycle *SeedContinuationLifecycle) (SeedGrowResult, error) {
			got = lifecycle
			return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithContinuationPersistence(wiredContinuationPersistence())
	if _, err := svc.SeedGrow(context.Background(), validSeedInput()); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if got != nil {
		t.Fatalf("synchronous SeedGrow received continuation lifecycle %+v", got.Target())
	}
}

func TestOpenedSeedReceivesContinuationLifecycleWithExactTarget(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	var got *SeedContinuationLifecycle
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, lifecycle *SeedContinuationLifecycle) (SeedGrowResult, error) {
			got = lifecycle
			return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error { return nil },
	}).WithContinuationPersistence(wiredContinuationPersistence())

	ctx := WithPollen(context.Background(), "claude")
	growth, err := svc.PrepareSeed(ctx, validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(ctx, growth, "seed-opened-1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := svc.GrowPreparedSeed(ctx, growth); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if got == nil {
		t.Fatal("opened Seed did not receive continuation lifecycle")
	}
	target := got.Target()
	if target.Handle != "seed-opened-1" || target.PhytomerID != growth.PhytomerID() || target.Pollen != "claude" || target.Substrate != "core" {
		t.Fatalf("lifecycle target = %+v", target)
	}
}

func TestOpenedSettlementPersistenceErrorsArePropagated(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	persistErr := errors.New("durable settlement failed")
	port := wiredContinuationPersistence()
	port.CompleteSuccessfulSettlement = func(context.Context, SeedSettlement) error {
		return persistErr
	}
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error { return nil },
		RecordSettlement: func(context.Context, SeedSettlement) error {
			t.Fatal("best-effort RecordSettlement used for continuation-aware opened settlement")
			return nil
		},
	}).WithContinuationPersistence(port)

	ctx := WithPollen(context.Background(), "claude")
	growth, err := svc.PrepareSeed(ctx, validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(ctx, growth, "seed-persist-err"); err != nil {
		t.Fatalf("open: %v", err)
	}
	result, err := svc.GrowPreparedSeed(ctx, growth)
	if !errors.Is(err, persistErr) {
		t.Fatalf("grow err = %v, want persist error", err)
	}
	if result.Status == SeedStatusSatisfied {
		t.Fatal("failed settlement persistence still reported satisfied")
	}
}

func TestCancelledExecutionContextStillGetsBoundedFinalization(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	var persistCancelled bool
	var persistHadDeadline bool
	var persistDeadline time.Time
	var persistCalled bool
	port := wiredContinuationPersistence()
	port.AccountTerminalFailure = func(ctx context.Context, settled SeedSettlement) (TerminalFailureAccount, error) {
		persistCalled = true
		persistCancelled = ctx.Err() != nil
		persistDeadline, persistHadDeadline = ctx.Deadline()
		return TerminalFailureAccount{}, nil
	}
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			if ctx.Err() == nil {
				t.Error("execution context was not cancelled")
			}
			return SeedGrowResult{Status: SeedStatusWithered, Iterations: 1, PhytomerID: spec.PhytomerID}, ctx.Err()
		},
	}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error { return nil },
	}).WithContinuationPersistence(port)

	ctx := WithPollen(context.Background(), "claude")
	growth, err := svc.PrepareSeed(ctx, validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(ctx, growth, "seed-cancelled"); err != nil {
		t.Fatalf("open: %v", err)
	}
	cancelled, cancel := context.WithCancel(WithPollen(context.Background(), "claude"))
	cancel()
	_, err = svc.GrowPreparedSeed(cancelled, growth)
	if !persistCalled {
		t.Fatalf("cancelled execution did not run durable finalization; grow err=%v", err)
	}
	if persistCancelled {
		t.Fatal("finalization context was already cancelled during persist")
	}
	if !persistHadDeadline || time.Until(persistDeadline) > 20*time.Second {
		t.Fatalf("finalization context was not narrowly bounded: deadline=%v has=%v", persistDeadline, persistHadDeadline)
	}
}

func TestOpenedSeedTerminalFailureAccountsUndeliveredContinuation(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	port := wiredContinuationPersistence()
	port.AccountTerminalFailure = func(_ context.Context, settled SeedSettlement) (TerminalFailureAccount, error) {
		if settled.Status != SeedStatusExhausted {
			t.Errorf("accounted status = %q, want exhausted", settled.Status)
		}
		if strings.Contains(settled.Error, "SECRET-INTENT") {
			t.Errorf("settlement error leaked continued intent: %q", settled.Error)
		}
		return TerminalFailureAccount{UnresolvedFailed: 1}, nil
	}
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			return SeedGrowResult{Status: SeedStatusExhausted, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error { return nil },
	}).WithContinuationPersistence(port)

	ctx := WithPollen(context.Background(), "claude")
	growth, err := svc.PrepareSeed(ctx, validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(ctx, growth, "seed-undelivered"); err != nil {
		t.Fatalf("open: %v", err)
	}
	result, err := svc.GrowPreparedSeed(ctx, growth)
	if !errors.Is(err, ErrContinuationUndeliverable) {
		t.Fatalf("grow err = %v, want undeliverable", err)
	}
	if result.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", result.Status)
	}
	if strings.Contains(err.Error(), "SECRET-INTENT") {
		t.Fatalf("error leaked continued intent: %v", err)
	}
}

func TestSeedStatusSettlingIsNotTerminalAndNotContinuationEligible(t *testing.T) {
	if SeedStatusIsTerminal(SeedStatusSettling) {
		t.Fatal("settling must not be terminal")
	}
	if err := continuationEligibilityError(SeedStatusSettling, "myrepo"); !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("settling eligibility: %v", err)
	}
}

func TestComposeContinuedIntentPromptPreservesBaseAndOrdersIntents(t *testing.T) {
	base := "original goal\n\nDeterministic verification configured by the Stem:\n[\"true\"]"
	got := ComposeContinuedIntentPrompt(base, []string{" first ", "second"})
	if !strings.HasPrefix(got, base) {
		t.Fatalf("replaced original goal:\n%s", got)
	}
	if strings.Index(got, "first") > strings.Index(got, "second") {
		t.Fatalf("intent order lost:\n%s", got)
	}
	if !strings.Contains(got, continuedIntentHeading) || !strings.Contains(got, continuedIntentBounds) {
		t.Fatalf("missing delimited continuation section:\n%s", got)
	}
	if ComposeContinuedIntentPrompt(base, nil) != base {
		t.Fatal("empty intents mutated the prompt")
	}
}

func TestReconcileOrphanedSeedWorkNotWired(t *testing.T) {
	svc := NewService(nil)
	if err := svc.ReconcileOrphanedSeedWork(context.Background()); !errors.Is(err, ErrContinuationNotWired) {
		t.Fatalf("unwired reconcile: %v", err)
	}
}

func wiredContinuationPersistence() ContinuationPersistence {
	return ContinuationPersistence{
		ResolveTarget: func(context.Context, string) (ContinuationTarget, bool, error) {
			return ContinuationTarget{}, false, nil
		},
		Accept: func(context.Context, ContinuationAcceptance) (ContinuationRecord, error) {
			return ContinuationRecord{}, ErrContinuationNotWired
		},
		ClaimPending: func(context.Context, ContinuationTarget) ([]ContinuationRecord, error) {
			return nil, nil
		},
		MarkDelivered:          func(context.Context, ContinuationTarget, []string) error { return nil },
		HasUnresolved:          func(context.Context, ContinuationTarget) (bool, error) { return false, nil },
		AcquireSettlementFence: func(context.Context, ContinuationTarget) (bool, error) { return true, nil },
		CompleteSuccessfulSettlement: func(context.Context, SeedSettlement) error {
			return nil
		},
		AccountTerminalFailure: func(context.Context, SeedSettlement) (TerminalFailureAccount, error) {
			return TerminalFailureAccount{}, nil
		},
		ReconcileOrphaned: func(context.Context) error { return nil },
	}
}
