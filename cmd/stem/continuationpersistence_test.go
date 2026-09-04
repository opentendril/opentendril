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

func TestSlice1DoesNotExposeContinuationSurface(t *testing.T) {
	for _, name := range core.CapabilityNames() {
		if name == "phytomer.continue" || strings.Contains(name, "continue") {
			t.Fatalf("canonical capability set includes %q", name)
		}
	}
	if core.IsDelegatedCapability("phytomer.continue") {
		t.Fatal("delegated set includes phytomer.continue")
	}
	cli := append([]string{}, sessionCLICapabilityNames()...)
	cli = append(cli, genomeCLICapabilityNames()...)
	cli = append(cli, plasmidCLICapabilityNames()...)
	cli = append(cli, meshCLICapabilityNames()...)
	cli = append(cli, sequenceCLICapabilityNames()...)
	cli = append(cli, sproutCLICapabilityNames()...)
	cli = append(cli, stomaCLICapabilityNames()...)
	cli = append(cli, seedCLICapabilityNames()...)
	cli = append(cli, gitCLICapabilityNames()...)
	cli = append(cli, genotypeCLICapabilityNames()...)
	for _, name := range cli {
		if name == "phytomer.continue" {
			t.Fatal("CLI projects phytomer.continue")
		}
	}
	mcpNames := receptors.NewMCPHandler().WithCore(core.NewService(nil)).CoreCapabilityNames()
	for _, name := range mcpNames {
		if name == "phytomer.continue" {
			t.Fatal("MCP projects phytomer.continue")
		}
	}
	rest := receptors.NewSessionsHandler(core.NewService(nil), nil, nil, nil)
	rest.Register(http.NewServeMux(), nil, nil)
	for _, name := range rest.Capabilities() {
		if name == "phytomer.continue" {
			t.Fatal("REST projects phytomer.continue")
		}
	}
}
