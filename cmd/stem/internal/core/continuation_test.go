package core_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestContinuationInputHasNoCallerSubstrate(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(core.ContinuationInput{}),
		reflect.TypeOf(core.ContinuationAcceptance{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.ToLower(field.Name)
			if strings.Contains(name, "substrate") {
				t.Errorf("%s field %s is a caller-controlled Substrate path", typ.Name(), field.Name)
			}
			tag := strings.ToLower(field.Tag.Get("json"))
			if strings.Contains(tag, "substrate") {
				t.Errorf("%s json tag %q is a caller-controlled Substrate path", typ.Name(), field.Tag.Get("json"))
			}
		}
	}
}

func TestContinuationIsNotAGovernedCapability(t *testing.T) {
	for _, name := range core.CapabilityNames() {
		if name == "phytomer.continue" || strings.Contains(name, "continue") {
			t.Fatalf("governed registry includes %q", name)
		}
	}
	if core.IsDelegatedCapability("phytomer.continue") {
		t.Fatal("phytomer.continue was added to the delegated set")
	}
	svc := newTestCore(t)
	if _, err := svc.Invoke(context.Background(), "phytomer.continue", map[string]any{
		"sessionId": "tendril-1", "intent": "keep going", "idempotencyKey": "k1",
	}); err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("Invoke(phytomer.continue) = %v, want unknown capability", err)
	}
}

func TestContinuationIntentDigestDoesNotContainPlaintext(t *testing.T) {
	intent := "SECRET-CONTINUATION-INTENT"
	digest := core.ContinuationIntentDigest(intent)
	if digest == "" || strings.Contains(digest, intent) || strings.Contains(strings.ToLower(digest), "secret") {
		t.Fatalf("digest leaked plaintext: %q", digest)
	}
	if got := core.ContinuationIntentDigest(intent); got != digest {
		t.Fatalf("digest is not stable: %q vs %q", got, digest)
	}
	if core.ContinuationIntentDigest(intent+"!") == digest {
		t.Fatal("different intent produced the same digest")
	}
}

func TestResolveContinuationTargetUnknownFails(t *testing.T) {
	svc := newContinuationService(t, core.ContinuationPersistence{
		ResolveTarget: func(context.Context, string) (core.ContinuationTarget, bool, error) {
			return core.ContinuationTarget{}, false, nil
		},
		Accept: ignoreAccept,
	})
	_, err := svc.ResolveContinuationTarget(core.WithPollen(context.Background(), "claude"), "tendril-missing")
	if !errors.Is(err, core.ErrContinuationTargetNotFound) {
		t.Fatalf("unknown phytomer: %v", err)
	}
}

func TestResolveContinuationTargetNoSeedOwnershipFails(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	sess, err := manager.Initiate(context.Background(), session.OriginCLI, session.Preferences{Substrate: "from-preferences"})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	svc := core.NewService(manager).WithContinuationPersistence(core.ContinuationPersistence{
		ResolveTarget: func(context.Context, string) (core.ContinuationTarget, bool, error) {
			return core.ContinuationTarget{}, false, nil
		},
		Accept: ignoreAccept,
	})
	target, err := svc.ResolveContinuationTarget(core.WithPollen(context.Background(), "claude"), sess.ID)
	if !errors.Is(err, core.ErrContinuationTargetNotFound) {
		t.Fatalf("session without seed ownership: target=%+v err=%v", target, err)
	}
	if target.Substrate == "from-preferences" {
		t.Fatal("resolved Substrate from session preferences")
	}
}

func TestResolveContinuationTargetUsesSeedOwnership(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo", Status: "running",
	}))
	target, err := svc.ResolveContinuationTarget(core.WithPollen(context.Background(), "claude"), "tendril-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Pollen != "claude" || target.Substrate != "myrepo" || target.Handle != "seed-1" {
		t.Fatalf("target = %+v", target)
	}
	req := target.ToDelegationRequest("seed.grow")
	if req.Pollen != "claude" || req.Substrate != "myrepo" || req.OperationClass != "seed.grow" {
		t.Fatalf("delegation request = %+v", req)
	}
}

func TestResolveContinuationTargetWrongPollenFails(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Status: "running",
	}))
	_, err := svc.ResolveContinuationTarget(core.WithPollen(context.Background(), "other"), "tendril-1")
	if !errors.Is(err, core.ErrContinuationPollenMismatch) {
		t.Fatalf("wrong pollen: %v", err)
	}
}

func TestResolveContinuationTargetTerminalSeedFails(t *testing.T) {
	for _, status := range []string{
		core.SeedStatusSatisfied, core.SeedStatusExhausted, core.SeedStatusWithered, core.SeedStatusFruitPublicationFailed,
	} {
		svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
			PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Status: status,
		}))
		_, err := svc.ResolveContinuationTarget(core.WithPollen(context.Background(), "claude"), "tendril-1")
		if !errors.Is(err, core.ErrContinuationNotEligible) {
			t.Fatalf("terminal %q: %v", status, err)
		}
	}
}

func TestResolveContinuationTargetBlankSubstrateFails(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "  ", Status: "running",
	}))
	_, err := svc.ResolveContinuationTarget(core.WithPollen(context.Background(), "claude"), "tendril-1")
	if !errors.Is(err, core.ErrContinuationNotEligible) {
		t.Fatalf("blank substrate: %v", err)
	}
}

func TestAcceptContinuationNotWired(t *testing.T) {
	svc := core.NewService(nil)
	_, err := svc.AcceptContinuation(context.Background(), core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "go", IdempotencyKey: "k1",
	})
	if !errors.Is(err, core.ErrContinuationNotWired) {
		t.Fatalf("unwired accept: %v", err)
	}
}

func TestAcceptContinuationUnavailablePort(t *testing.T) {
	svc := newContinuationService(t, core.ContinuationPersistence{
		ResolveTarget: func(context.Context, string) (core.ContinuationTarget, bool, error) {
			return core.ContinuationTarget{}, false, core.ErrContinuationHistoryUnavailable
		},
		Accept: func(context.Context, core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			return core.ContinuationRecord{}, core.ErrContinuationHistoryUnavailable
		},
	})
	_, err := svc.AcceptContinuation(core.WithPollen(context.Background(), "claude"), core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "go", IdempotencyKey: "k1",
	})
	if !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
}

func TestAcceptContinuationComposesStemOwnedOwnership(t *testing.T) {
	var got core.ContinuationAcceptance
	svc := newContinuationService(t, core.ContinuationPersistence{
		ResolveTarget: owningContinuationPort(core.ContinuationTarget{
			PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Status: "running",
		}).ResolveTarget,
		Accept: func(_ context.Context, in core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			got = in
			return core.ContinuationRecord{
				ContinuationID: "continuation-1",
				PhytomerID:     in.PhytomerID,
				Pollen:         in.Pollen,
				Substrate:      "myrepo",
				IdempotencyKey: in.IdempotencyKey,
				IntentDigest:   in.IntentDigest,
				Intent:         in.Intent,
				Sequence:       1,
				DeliveryState:  core.ContinuationDeliveryPending,
			}, nil
		},
	})
	rec, err := svc.AcceptContinuation(core.WithPollen(context.Background(), "claude"), core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "keep going", IdempotencyKey: "retry-1",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got.Pollen != "claude" || got.PhytomerID != "tendril-1" || got.Intent != "keep going" {
		t.Fatalf("acceptance = %+v", got)
	}
	if got.IntentDigest != core.ContinuationIntentDigest("keep going") {
		t.Fatalf("digest = %q", got.IntentDigest)
	}
	if rec.Sequence != 1 || rec.DeliveryState != core.ContinuationDeliveryPending {
		t.Fatalf("record = %+v", rec)
	}
}

func TestAcceptContinuationRefusesWrongPollenAndTerminal(t *testing.T) {
	accepted := 0
	svc := newContinuationService(t, core.ContinuationPersistence{
		ResolveTarget: owningContinuationPort(core.ContinuationTarget{
			PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Status: core.SeedStatusSatisfied,
		}).ResolveTarget,
		Accept: func(context.Context, core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			accepted++
			return core.ContinuationRecord{}, nil
		},
	})
	_, err := svc.AcceptContinuation(core.WithPollen(context.Background(), "claude"), core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "go", IdempotencyKey: "k1",
	})
	if !errors.Is(err, core.ErrContinuationNotEligible) {
		t.Fatalf("terminal accept: %v", err)
	}
	if accepted != 0 {
		t.Fatal("persist ran for a terminal Seed")
	}
}

func newContinuationService(t *testing.T, port core.ContinuationPersistence) *core.Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	return core.NewService(manager).WithContinuationPersistence(port)
}

func owningContinuationPort(target core.ContinuationTarget) core.ContinuationPersistence {
	return core.ContinuationPersistence{
		ResolveTarget: func(_ context.Context, phytomerID string) (core.ContinuationTarget, bool, error) {
			if phytomerID != target.PhytomerID {
				return core.ContinuationTarget{}, false, nil
			}
			return target, true, nil
		},
		Accept: ignoreAccept,
	}
}

func ignoreAccept(context.Context, core.ContinuationAcceptance) (core.ContinuationRecord, error) {
	return core.ContinuationRecord{}, errors.New("accept should not run")
}
