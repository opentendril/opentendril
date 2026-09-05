package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestContinuationPersistenceNilHistoryFailsHonestly(t *testing.T) {
	port := continuationPersistence(nil)
	ctx := context.Background()
	_, _, err := port.ResolveTarget(ctx, "tendril-1")
	if !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("resolve: %v", err)
	}
	_, err = port.Accept(ctx, core.ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", IdempotencyKey: "k1", Intent: "go",
	})
	if !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("accept: %v", err)
	}
	target := core.ContinuationTarget{PhytomerID: "tendril-1", Handle: "seed-1", Pollen: "claude", Substrate: "myrepo"}
	if _, err := port.ClaimPending(ctx, target); !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("claim: %v", err)
	}
	if err := port.MarkDelivered(ctx, target, []string{"continuation-1"}); !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("deliver: %v", err)
	}
	if _, err := port.HasUnresolved(ctx, target); !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("unresolved: %v", err)
	}
	if _, err := port.AcquireSettlementFence(ctx, target); !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("fence: %v", err)
	}
	if err := port.CompleteSuccessfulSettlement(ctx, core.SeedSettlement{Status: core.SeedStatusSatisfied}); !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("complete: %v", err)
	}
	if _, err := port.AccountTerminalFailure(ctx, core.SeedSettlement{Status: core.SeedStatusWithered}); !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("account: %v", err)
	}
	if err := port.ReconcileOrphaned(ctx); !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("reconcile: %v", err)
	}
}

func TestContinuationPersistenceAcceptsFromSeedOwnership(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "myrepo",
		Status: core.SeedStatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}

	manager, err := session.NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	svc := core.NewService(manager).WithContinuationPersistence(continuationPersistence(store))
	ctx := core.WithPollen(context.Background(), "claude")
	rec, err := svc.AcceptContinuation(ctx, core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "keep going", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if rec.Substrate != "myrepo" || rec.Sequence != 1 || rec.ContinuationID == "" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestAcceptContinuationFailsWhenSubstrateChangesAfterResolve(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "repo-a",
		Status: core.SeedStatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}

	manager, err := session.NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	port := continuationPersistence(store)
	svc := core.NewService(manager).WithContinuationPersistence(core.ContinuationPersistence{
		ResolveTarget: port.ResolveTarget,
		Accept: func(ctx context.Context, in core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			if in.Substrate != "repo-a" || in.Pollen != "claude" || in.Handle != "seed-1" {
				t.Errorf("expected resolved ownership not carried: %+v", in)
			}
			raw, err := sql.Open("sqlite", store.Path())
			if err != nil {
				t.Fatalf("raw open: %v", err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
				t.Fatalf("busy_timeout: %v", err)
			}
			if _, err := raw.Exec(`UPDATE seedruns SET substrate = ? WHERE phytomerId = ?`, "repo-b", "tendril-1"); err != nil {
				t.Fatalf("mutate substrate: %v", err)
			}
			return port.Accept(ctx, in)
		},
	})
	_, err = svc.AcceptContinuation(core.WithPollen(context.Background(), "claude"), core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "keep going", IdempotencyKey: "k1",
	})
	if !errors.Is(err, core.ErrContinuationTargetChanged) {
		t.Fatalf("TOCTOU accept: %v", err)
	}
	listed, err := store.ListContinuationsByPhytomer(context.Background(), "tendril-1")
	if err != nil || len(listed) != 0 {
		t.Fatalf("want no continuation after ownership change, got %+v err=%v", listed, err)
	}
}

func TestBuildServeCoreWiresContinuationPersistence(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "myrepo",
		Status: core.SeedStatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}
	manager, err := session.NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	svc := buildServeCore(manager, t.TempDir(), store, eventbus.New())
	gotCaps := make([]string, 0, len(svc.Capabilities()))
	for _, capability := range svc.Capabilities() {
		gotCaps = append(gotCaps, capability.Name)
	}
	sort.Strings(gotCaps)
	wantCaps := core.CapabilityNames()
	if len(gotCaps) != len(wantCaps) {
		t.Fatalf("serve Core capability count = %d, want %d", len(gotCaps), len(wantCaps))
	}
	for i := range wantCaps {
		if gotCaps[i] != wantCaps[i] {
			t.Fatalf("serve Core capability %d = %q, want %q", i, gotCaps[i], wantCaps[i])
		}
	}
	ctx := core.WithPollen(context.Background(), "claude")
	rec, err := svc.AcceptContinuation(ctx, core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "keep going", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("serve core accept: %v", err)
	}
	if rec.Substrate != "myrepo" || rec.Pollen != "claude" {
		t.Fatalf("record = %+v", rec)
	}

	disabled := buildServeCore(manager, t.TempDir(), nil, eventbus.New())
	_, err = disabled.AcceptContinuation(ctx, core.ContinuationInput{
		PhytomerID: "tendril-1", Intent: "keep going", IdempotencyKey: "k2",
	})
	if !errors.Is(err, core.ErrContinuationHistoryUnavailable) {
		t.Fatalf("disabled history: %v", err)
	}
}

func TestPhytomerContinueIsProjectedOnEverySurface(t *testing.T) {
	found := false
	for _, name := range core.CapabilityNames() {
		if name == core.CapContinuePhytomer {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("canonical capability set missing phytomer.continue")
	}
	if !core.IsDelegatedCapability(core.CapContinuePhytomer) {
		t.Fatal("delegated set missing phytomer.continue")
	}
	cli := append([]string{}, sessionCLICapabilityNames()...)
	cliFound := false
	for _, name := range cli {
		if name == core.CapContinuePhytomer {
			cliFound = true
		}
	}
	if !cliFound {
		t.Fatal("CLI does not project phytomer.continue")
	}
	mcpNames := receptors.NewMCPHandler().WithCore(core.NewService(nil)).CoreCapabilityNames()
	mcpFound := false
	for _, name := range mcpNames {
		if name == core.CapContinuePhytomer {
			mcpFound = true
		}
	}
	if !mcpFound {
		t.Fatal("MCP does not project phytomer.continue")
	}
	rest := receptors.NewSessionsHandler(core.NewService(nil), nil, nil, nil)
	rest.Register(http.NewServeMux(), nil, nil)
	restFound := false
	for _, name := range rest.Capabilities() {
		if name == core.CapContinuePhytomer {
			restFound = true
		}
	}
	if !restFound {
		t.Fatal("REST does not project phytomer.continue")
	}
}

func TestProductionAdapterEmptyPollenOpenedSeedLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := historydb.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := session.NewManager(ctx, store)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}

	intent := "keep going locally"
	svc := core.NewService(manager).
		WithSeed(core.SeedOperations{
			Run: func(ctx context.Context, spec core.SeedSpec, lifecycle *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
				if lifecycle == nil {
					t.Fatal("opened local seed missing continuation lifecycle")
				}
				if lifecycle.Target().Pollen != "" {
					t.Fatalf("local lifecycle pollen = %q", lifecycle.Target().Pollen)
				}
				prompt, err := lifecycle.DeliverPending(ctx, spec.Goal)
				if err != nil {
					return core.SeedGrowResult{}, err
				}
				if !strings.Contains(prompt, intent) {
					t.Fatalf("local continuation missing from prompt: %q", prompt)
				}
				if err := lifecycle.ConfirmDelivery(ctx); err != nil {
					return core.SeedGrowResult{}, err
				}
				fenced, err := lifecycle.AcquireSettlementFence(ctx)
				if err != nil || !fenced {
					t.Fatalf("local fence: fenced=%v err=%v", fenced, err)
				}
				return core.SeedGrowResult{Status: core.SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
			},
		}).
		WithSeedPersistence(seedPersistence(store)).
		WithContinuationPersistence(continuationPersistence(store))

	growth, err := svc.PrepareSeed(ctx, core.SeedGrowInput{Substrate: "myrepo", Goal: "make it pass", Verify: []string{"true"}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(ctx, growth); err != nil {
		t.Fatalf("open: %v", err)
	}
	accepted, err := svc.AcceptContinuation(ctx, core.ContinuationInput{
		PhytomerID: growth.PhytomerID(), Intent: intent, IdempotencyKey: "local-1",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.Pollen != "" {
		t.Fatalf("accepted pollen = %q", accepted.Pollen)
	}

	result, err := svc.GrowPreparedSeed(ctx, growth)
	if err != nil {
		t.Fatalf("grow: %v", err)
	}
	if result.Status != core.SeedStatusSatisfied {
		t.Fatalf("status = %q", result.Status)
	}
	seed, ok, err := store.GetSeedRunByPhytomer(ctx, growth.PhytomerID())
	if err != nil || !ok || seed.Pollen != "" || seed.Status != core.SeedStatusSatisfied {
		t.Fatalf("settled = %+v ok=%v err=%v", seed, ok, err)
	}
	got, ok, err := store.GetContinuation(ctx, accepted.ContinuationID)
	if err != nil || !ok || got.DeliveryState != core.ContinuationDeliveryDelivered || got.Pollen != "" {
		t.Fatalf("continuation = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestProductionPreProviderFailureFailsContinuationNotDelivered(t *testing.T) {
	ctx := context.Background()
	store, err := historydb.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := session.NewManager(ctx, store)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}

	intent := "SECRET-LOCAL-INTENT"
	svc := core.NewService(manager).
		WithSeed(core.SeedOperations{
			Run: func(ctx context.Context, spec core.SeedSpec, lifecycle *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
				if _, err := lifecycle.DeliverPending(ctx, spec.Goal); err != nil {
					return core.SeedGrowResult{}, err
				}
				return core.SeedGrowResult{
					Status:     core.SeedStatusWithered,
					Iterations: 1,
					PhytomerID: spec.PhytomerID,
				}, errors.New("dial refused before provider request")
			},
		}).
		WithSeedPersistence(seedPersistence(store)).
		WithContinuationPersistence(continuationPersistence(store))

	growth, err := svc.PrepareSeed(ctx, core.SeedGrowInput{Substrate: "myrepo", Goal: "make it pass", Verify: []string{"true"}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(ctx, growth); err != nil {
		t.Fatalf("open: %v", err)
	}
	accepted, err := svc.AcceptContinuation(ctx, core.ContinuationInput{
		PhytomerID: growth.PhytomerID(), Intent: intent, IdempotencyKey: "pre-1",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	result, err := svc.GrowPreparedSeed(ctx, growth)
	if !errors.Is(err, core.ErrContinuationUndeliverable) {
		t.Fatalf("grow err = %v, want undeliverable", err)
	}
	if strings.Contains(err.Error(), intent) {
		t.Fatalf("error leaked continued intent: %v", err)
	}
	if result.Status == core.SeedStatusSatisfied {
		t.Fatal("pre-provider failure reported satisfied")
	}
	seed, ok, getErr := store.GetSeedRunByPhytomer(ctx, growth.PhytomerID())
	if getErr != nil || !ok || seed.Status == core.SeedStatusSatisfied {
		t.Fatalf("seed = %+v ok=%v err=%v", seed, ok, getErr)
	}
	got, ok, getErr := store.GetContinuation(ctx, accepted.ContinuationID)
	if getErr != nil || !ok || got.DeliveryState != core.ContinuationDeliveryFailed {
		t.Fatalf("continuation = %+v ok=%v err=%v", got, ok, getErr)
	}
	if strings.Contains(seed.Error, intent) {
		t.Fatalf("seed error leaked continued intent: %q", seed.Error)
	}
}
