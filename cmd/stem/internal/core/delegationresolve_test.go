package core_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestResolveDelegationRequestUsesExplicitSubstrate(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "owned-repo", Status: core.SeedStatusRunning,
	}))
	req, err := svc.ResolveDelegationRequest(core.WithPollen(context.Background(), "claude"), core.CapSeedGrow, map[string]any{
		"substrate": "caller-repo",
		"goal":      "fix tests",
	})
	if err != nil {
		t.Fatalf("resolve seed.grow: %v", err)
	}
	if req.Pollen != "claude" || req.Substrate != "caller-repo" || req.OperationClass != core.CapSeedGrow {
		t.Fatalf("request = %+v", req)
	}
	if req.Impact != core.DelegationImpactHigh {
		t.Fatalf("impact = %q", req.Impact)
	}
}

func TestResolveDelegationRequestContinuationIgnoresCallerSubstrate(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "owned-repo", Status: core.SeedStatusRunning,
	}))
	req, err := svc.ResolveDelegationRequest(core.WithPollen(context.Background(), "claude"), core.CapContinuePhytomer, map[string]any{
		"sessionId":      "tendril-1",
		"intent":         "keep going",
		"idempotencyKey": "k1",
		"substrate":      "other",
		"pollen":         "attacker",
		"handle":         "forged",
	})
	if err != nil {
		t.Fatalf("resolve continue: %v", err)
	}
	if req.Pollen != "claude" || req.Substrate != "owned-repo" || req.OperationClass != core.CapContinuePhytomer {
		t.Fatalf("request = %+v, want Stem-owned target", req)
	}
	if req.Impact != core.DelegationImpactHigh {
		t.Fatalf("impact = %q, want high", req.Impact)
	}
}

func TestResolveDelegationRequestContinuationFailsClosed(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "owned-repo", Status: core.SeedStatusRunning,
	}))
	ctx := core.WithPollen(context.Background(), "claude")

	if _, err := svc.ResolveDelegationRequest(ctx, core.CapContinuePhytomer, map[string]any{"sessionId": "missing"}); !errors.Is(err, core.ErrContinuationTargetNotFound) {
		t.Fatalf("unknown phytomer: %v", err)
	}
	if _, err := svc.ResolveDelegationRequest(core.WithPollen(context.Background(), "other"), core.CapContinuePhytomer, map[string]any{"sessionId": "tendril-1"}); !errors.Is(err, core.ErrContinuationPollenMismatch) {
		t.Fatalf("wrong pollen: %v", err)
	}
	if _, err := svc.ResolveDelegationRequest(ctx, core.CapContinuePhytomer, map[string]any{}); !errors.Is(err, core.ErrContinuationInvalid) {
		t.Fatalf("missing sessionId: %v", err)
	}

	terminal := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "owned-repo", Status: core.SeedStatusSatisfied,
	}))
	if _, err := terminal.ResolveDelegationRequest(ctx, core.CapContinuePhytomer, map[string]any{"sessionId": "tendril-1"}); !errors.Is(err, core.ErrContinuationNotEligible) {
		t.Fatalf("terminal: %v", err)
	}
}

func TestContinuationAuthorizationTargetMutationFailsAccept(t *testing.T) {
	current := core.ContinuationTarget{
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "repo-a", Status: core.SeedStatusRunning,
	}
	var mu sync.Mutex
	svc := newContinuationService(t, core.ContinuationPersistence{
		ResolveTarget: func(_ context.Context, phytomerID string) (core.ContinuationTarget, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			if phytomerID != current.PhytomerID {
				return core.ContinuationTarget{}, false, nil
			}
			return current, true, nil
		},
		Accept: func(_ context.Context, in core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			mu.Lock()
			defer mu.Unlock()
			if in.Substrate != current.Substrate || in.Handle != current.Handle || in.Pollen != current.Pollen {
				return core.ContinuationRecord{}, core.ErrContinuationTargetChanged
			}
			return core.ContinuationRecord{ContinuationID: "continuation-1", PhytomerID: in.PhytomerID, Sequence: 1}, nil
		},
	})
	ctx := core.WithPollen(context.Background(), "claude")
	req, err := svc.ResolveDelegationRequest(ctx, core.CapContinuePhytomer, map[string]any{"sessionId": "tendril-1"})
	if err != nil {
		t.Fatalf("authorize resolve: %v", err)
	}
	if req.Substrate != "repo-a" {
		t.Fatalf("authorized substrate = %q", req.Substrate)
	}
	ctx = core.WithAuthorizedDelegationRequest(ctx, req)
	mu.Lock()
	current.Substrate = "repo-b"
	mu.Unlock()
	_, err = svc.AcceptContinuation(ctx, core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "keep going", IdempotencyKey: "k1",
	})
	if !errors.Is(err, core.ErrContinuationTargetChanged) {
		t.Fatalf("mutated target accept: %v", err)
	}
}

func TestResolveDelegationRequestPollenComesFromContext(t *testing.T) {
	svc := newContinuationService(t, owningContinuationPort(core.ContinuationTarget{
		PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "owned-repo", Status: core.SeedStatusRunning,
	}))
	req, err := svc.ResolveDelegationRequest(core.WithPollen(context.Background(), "claude"), core.CapGitStatus, map[string]any{
		"substrate": "owned-repo",
		"pollen":    "attacker",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.Pollen != "claude" {
		t.Fatalf("pollen = %q, want context pollen", req.Pollen)
	}
	if strings.Contains(req.Pollen, "attacker") {
		t.Fatal("caller pollen reached the request")
	}
}
