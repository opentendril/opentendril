package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestContinuationInputHasNoCallerSubstrate(t *testing.T) {
	typ := reflect.TypeOf(core.ContinuationInput{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		if strings.Contains(name, "substrate") {
			t.Errorf("ContinuationInput field %s is a caller-controlled Substrate path", field.Name)
		}
		tag := strings.ToLower(field.Tag.Get("json"))
		if strings.Contains(tag, "substrate") {
			t.Errorf("ContinuationInput json tag %q is a caller-controlled Substrate path", field.Tag.Get("json"))
		}
	}
}

func TestContinuePhytomerIsGovernedDelegatedCapability(t *testing.T) {
	found := false
	for _, name := range core.CapabilityNames() {
		if name == core.CapContinuePhytomer {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("CapabilityNames() missing phytomer.continue")
	}
	if !core.IsDelegatedCapability(core.CapContinuePhytomer) {
		t.Fatal("phytomer.continue is not delegated")
	}
	if got := core.CapabilityImpact(core.CapContinuePhytomer); got != core.DelegationImpactHigh {
		t.Fatalf("CapabilityImpact(phytomer.continue) = %q, want high", got)
	}
	svc := newTestCore(t)
	_, err := svc.Invoke(context.Background(), core.CapContinuePhytomer, map[string]any{
		"sessionId": "tendril-1", "intent": "keep going", "idempotencyKey": "k1",
	})
	if err == nil {
		t.Fatal("unwired Invoke(phytomer.continue) succeeded")
	}
	if errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unwired continue mapped to session-not-found: %v", err)
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
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo", Status: core.SeedStatusRunning,
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
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Status: core.SeedStatusRunning,
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

func TestResolveContinuationTargetUnrecognizedStatusFails(t *testing.T) {
	for _, status := range []string{"unknown", "matured", "settling"} {
		svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
			PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", Status: status,
		}))
		_, err := svc.ResolveContinuationTarget(core.WithPollen(context.Background(), "claude"), "tendril-1")
		if !errors.Is(err, core.ErrContinuationNotEligible) {
			t.Fatalf("status %q: %v", status, err)
		}
	}
}

func TestResolveContinuationTargetBlankSubstrateFails(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "  ", Status: core.SeedStatusRunning,
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
			PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo", Status: core.SeedStatusRunning,
		}).ResolveTarget,
		Accept: func(_ context.Context, in core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			got = in
			return core.ContinuationRecord{
				ContinuationID: "continuation-1",
				PhytomerID:     in.PhytomerID,
				Pollen:         in.Pollen,
				Substrate:      in.Substrate,
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
	if got.Substrate != "myrepo" || got.Handle != "seed-1" {
		t.Fatalf("expected ownership not carried: %+v", got)
	}
	if got.IntentDigest != core.ContinuationIntentDigest("keep going") {
		t.Fatalf("digest = %q", got.IntentDigest)
	}
	if rec.Sequence != 1 || rec.DeliveryState != core.ContinuationDeliveryPending {
		t.Fatalf("record = %+v", rec)
	}
}

func TestContinuationResultOmitsPlaintextIntent(t *testing.T) {
	typ := reflect.TypeOf(core.ContinuationResult{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name + field.Tag.Get("json"))
		if strings.Contains(name, "intent") && !strings.Contains(name, "idempotency") {
			t.Errorf("ContinuationResult field %s discloses intent", field.Name)
		}
		if strings.Contains(name, "pollen") || strings.Contains(name, "substrate") || strings.Contains(name, "handle") {
			t.Errorf("ContinuationResult field %s discloses Stem-internal ownership", field.Name)
		}
	}
}

func TestContinuePhytomerReturnsSafeAcceptance(t *testing.T) {
	svc := newContinuationService(t, core.ContinuationPersistence{
		ResolveTarget: owningContinuationPort(core.ContinuationTarget{
			PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo", Status: core.SeedStatusRunning,
		}).ResolveTarget,
		Accept: func(_ context.Context, in core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			return core.ContinuationRecord{
				ContinuationID: "continuation-1",
				PhytomerID:     in.PhytomerID,
				Pollen:         in.Pollen,
				Substrate:      in.Substrate,
				IdempotencyKey: in.IdempotencyKey,
				IntentDigest:   in.IntentDigest,
				Intent:         in.Intent,
				Sequence:       1,
				DeliveryState:  core.ContinuationDeliveryPending,
			}, nil
		},
	})
	result, err := svc.ContinuePhytomer(core.WithPollen(context.Background(), "claude"), core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "SECRET-INTENT", IdempotencyKey: "retry-1",
	})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if result.ContinuationID != "continuation-1" || result.PhytomerID != "tendril-1" || result.Sequence != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.DeliveryState != core.ContinuationDeliveryPending || result.IdempotencyKey != "retry-1" {
		t.Fatalf("result = %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "SECRET-INTENT") {
		t.Fatalf("result JSON echoed plaintext intent: %s", encoded)
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
