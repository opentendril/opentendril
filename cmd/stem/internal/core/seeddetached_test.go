package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestSeedGrowDetachedReturnsBeforeExecutorTerminal(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int64
	svc := newDetachedSeedService(t, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		runs.Add(1)
		close(started)
		select {
		case <-release:
			return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		case <-ctx.Done():
			t.Errorf("detached growth saw request cancellation: %v", ctx.Err())
			return SeedGrowResult{}, ctx.Err()
		}
	})

	ctx, cancel := context.WithCancel(WithPollen(context.Background(), "claude"))
	result, err := svc.SeedGrow(ctx, detachedSeedInput())
	if err != nil {
		t.Fatalf("detached grow: %v", err)
	}
	if result.Status != SeedStatusRunning || result.Handle == "" || result.PhytomerID == "" {
		t.Fatalf("detached result = %+v", result)
	}
	if !strings.HasPrefix(result.Handle, "seed-") || result.Handle == "seed-forged" {
		t.Fatalf("handle = %q, want Core-minted seed- prefix", result.Handle)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background Run did not start")
	}
	cancel()
	select {
	case <-time.After(50 * time.Millisecond):
	case <-release:
	}
	close(release)
	if runs.Load() != 1 {
		t.Fatalf("Run started %d times, want 1", runs.Load())
	}
}

func TestSeedGrowSynchronousRemainsTerminal(t *testing.T) {
	svc, captured := newSeedService(t)
	result, err := svc.SeedGrow(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("sync grow: %v", err)
	}
	if result.Status != SeedStatusSatisfied || result.Handle != "" {
		t.Fatalf("sync result = %+v", result)
	}
	if captured.PhytomerID == "" {
		t.Fatal("sync grow did not bind a Phytomer")
	}
}

func TestSeedGrowDetachedOpeningFailureStartsNoExecutor(t *testing.T) {
	var runs atomic.Int64
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(context.Context, SeedSpec, *SeedContinuationLifecycle) (SeedGrowResult, error) {
			runs.Add(1)
			t.Error("Run started after opening failure")
			return SeedGrowResult{}, nil
		},
	}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error {
			return errors.New("disk full")
		},
	}).WithContinuationPersistence(wiredContinuationPersistence())

	_, err = svc.SeedGrow(context.Background(), detachedSeedInput())
	if err == nil {
		t.Fatal("opening failure returned a running dispatch")
	}
	if runs.Load() != 0 {
		t.Fatalf("Run started %d times after opening failure", runs.Load())
	}
}

func TestSeedGrowDetachedUnwiredContinuationStartsNoExecutor(t *testing.T) {
	var runs atomic.Int64
	svc, _ := newSeedService(t)
	svc.WithSeed(SeedOperations{
		Run: func(context.Context, SeedSpec, *SeedContinuationLifecycle) (SeedGrowResult, error) {
			runs.Add(1)
			t.Error("Run started without continuation lifecycle")
			return SeedGrowResult{}, nil
		},
	})
	_, err := svc.SeedGrow(context.Background(), detachedSeedInput())
	if !errors.Is(err, ErrContinuationNotWired) {
		t.Fatalf("unwired continuation: %v", err)
	}
	if runs.Load() != 0 {
		t.Fatalf("Run started %d times", runs.Load())
	}
}

func TestSeedGrowDetachedCallerCannotSupplyHandle(t *testing.T) {
	svc := newDetachedSeedService(t, func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
	})
	result, err := svc.Invoke(WithPollen(context.Background(), "claude"), CapSeedGrow, map[string]any{
		"substrate":  "core",
		"goal":       "make the tests pass",
		"verify":     []any{"true"},
		"detached":   true,
		"handle":     "seed-forged",
		"phytomerId": "tendril-forged",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	got, ok := result.(SeedGrowResult)
	if !ok {
		t.Fatalf("result type %T", result)
	}
	if got.Handle == "seed-forged" || got.PhytomerID == "tendril-forged" {
		t.Fatalf("caller identity accepted: %+v", got)
	}
	if got.Status != SeedStatusRunning || !strings.HasPrefix(got.Handle, "seed-") {
		t.Fatalf("detached result = %+v", got)
	}
}

func TestOpenPreparedSeedMintsHandleWhenEmpty(t *testing.T) {
	svc := newDetachedSeedService(t, func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
	})
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	dispatch, err := svc.OpenPreparedSeed(context.Background(), growth, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.HasPrefix(dispatch.Handle, "seed-") || dispatch.Status != SeedStatusRunning {
		t.Fatalf("dispatch = %+v", dispatch)
	}
}

func newDetachedSeedService(t *testing.T, run func(context.Context, SeedSpec, *SeedContinuationLifecycle) (SeedGrowResult, error)) *Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	var openings []SeedOpening
	return NewService(manager).WithSeed(SeedOperations{Run: run}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(_ context.Context, opening SeedOpening) error {
			for _, existing := range openings {
				if existing.Handle == opening.Handle {
					return errors.New("duplicate seed handle")
				}
			}
			openings = append(openings, opening)
			return nil
		},
		RecordSettlement: func(context.Context, SeedSettlement) error { return nil },
	}).WithContinuationPersistence(wiredContinuationPersistence())
}

func detachedSeedInput() SeedGrowInput {
	in := validSeedInput()
	in.Detached = true
	return in
}
