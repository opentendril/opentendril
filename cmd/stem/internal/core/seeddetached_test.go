package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
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
	in := validSeedInput()
	in.IdempotencyKey = ""
	result, err := svc.SeedGrow(context.Background(), in)
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
		FindOpening: func(context.Context, string, string) (SeedOpening, bool, error) {
			return SeedOpening{}, false, nil
		},
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

func TestSeedGrowDetachedCanceledOpeningStillRecoversAndPrunes(t *testing.T) {
	ctx, cancel := context.WithCancel(WithPollen(context.Background(), "pollen-one"))
	defer cancel()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	var lookups atomic.Int32
	var preparedID string
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(context.Context, SeedSpec, *SeedContinuationLifecycle) (SeedGrowResult, error) {
			t.Error("Run started after canceled opening")
			return SeedGrowResult{}, nil
		},
	}).WithSeedPersistence(SeedPersistence{
		FindOpening: func(findCtx context.Context, _, _ string) (SeedOpening, bool, error) {
			if err := findCtx.Err(); err != nil {
				return SeedOpening{}, false, fmt.Errorf("recovery lookup used canceled context: %w", err)
			}
			lookups.Add(1)
			return SeedOpening{}, false, nil
		},
		RecordOpening: func(recordCtx context.Context, _ SeedOpening) error {
			prepared, err := manager.List(context.Background())
			if err != nil {
				t.Fatalf("list prepared Phytomer: %v", err)
			}
			if len(prepared) == 0 {
				t.Fatal("prepared Phytomer was not visible")
			}
			preparedID = prepared[0].ID
			cancel()
			return recordCtx.Err()
		},
	}).WithContinuationPersistence(wiredContinuationPersistence())
	if _, err := svc.SeedGrow(ctx, detachedSeedInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled opening error = %v, want context.Canceled", err)
	}
	if lookups.Load() != 2 {
		t.Fatalf("FindOpening called %d times, want initial and non-cancelable recovery lookups", lookups.Load())
	}
	sessions, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list Phytomers after canceled opening: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("canceled opening left Phytomer %q, expected cleanup", preparedID)
	}
}

func TestSeedGrowDetachedCleanupFailureStillReturnsDurableWinner(t *testing.T) {
	ctx := WithPollen(context.Background(), "pollen-one")
	store := &seedPhytomerDeleteFailStore{sessions: make(map[string]session.Phytomer)}
	manager, err := session.NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	input := detachedSeedInput()
	spec, err := resolveSeedSpec(input)
	if err != nil {
		t.Fatalf("resolve Seed input: %v", err)
	}
	winner := SeedOpening{
		Handle: "seed-canonical", PhytomerID: "tendril-canonical", Pollen: "pollen-one",
		Substrate: spec.Substrate, Goal: spec.Goal, IdempotencyKey: input.IdempotencyKey,
		RequestDigest: seedRequestDigest(spec), Status: SeedStatusRunning,
	}
	var accepted bool
	reported := make(chan SeedLifecycleReport, 1)
	var runs atomic.Int32
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(context.Context, SeedSpec, *SeedContinuationLifecycle) (SeedGrowResult, error) {
			runs.Add(1)
			return SeedGrowResult{}, nil
		},
	}).WithSeedPersistence(SeedPersistence{
		FindOpening: func(context.Context, string, string) (SeedOpening, bool, error) {
			return winner, accepted, nil
		},
		RecordOpening: func(context.Context, SeedOpening) error {
			accepted = true
			return errors.New("detached Seed key already has a winner")
		},
	}).WithContinuationPersistence(wiredContinuationPersistence()).WithSeedLifecycleReporter(func(report SeedLifecycleReport) {
		reported <- report
	})
	result, err := svc.SeedGrow(ctx, input)
	if err != nil {
		t.Fatalf("recovered durable winner after cleanup failure: %v", err)
	}
	if result.Handle != winner.Handle || result.PhytomerID != winner.PhytomerID || result.Status != winner.Status {
		t.Fatalf("replay did not return canonical durable winner: %+v, want %+v", result, winner)
	}
	select {
	case report := <-reported:
		if report.Kind != SeedLifecyclePreparationCleanupFailed || report.PhytomerID == winner.PhytomerID {
			t.Fatalf("unexpected cleanup report: %+v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("losing Phytomer cleanup failure was not reported")
	}
	if runs.Load() != 0 {
		t.Fatalf("replay after cleanup failure started %d background run(s)", runs.Load())
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
		"substrate":      "core",
		"goal":           "make the tests pass",
		"verify":         []any{"true"},
		"detached":       true,
		"idempotencyKey": "caller-key",
		"handle":         "seed-forged",
		"phytomerId":     "tendril-forged",
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

func TestDetachedAccountingFailureQuarantinesPhytomer(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	reported := make(chan SeedLifecycleReport, 1)
	var accepted int
	var live ContinuationTarget
	port := wiredContinuationPersistence()
	port.ResolveTarget = func(_ context.Context, phytomerID string) (ContinuationTarget, bool, error) {
		if phytomerID != live.PhytomerID {
			return ContinuationTarget{}, false, nil
		}
		return live, true, nil
	}
	port.Accept = func(context.Context, ContinuationAcceptance) (ContinuationRecord, error) {
		accepted++
		return ContinuationRecord{}, errors.New("accept must not run after accounting failure")
	}
	port.CompleteSuccessfulSettlement = func(context.Context, SeedSettlement) error {
		return errors.New("settlement persist failed")
	}

	svc := newDetachedSeedService(t, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		close(started)
		<-release
		return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
	})
	svc.WithContinuationPersistence(port).WithSeedLifecycleReporter(func(report SeedLifecycleReport) {
		reported <- report
	})

	ctx := WithPollen(context.Background(), "claude")
	result, err := svc.SeedGrow(ctx, detachedSeedInput())
	if err != nil {
		t.Fatalf("detached grow: %v", err)
	}
	live = ContinuationTarget{
		PhytomerID: result.PhytomerID,
		Handle:     result.Handle,
		Pollen:     "claude",
		Substrate:  "core",
		Status:     SeedStatusRunning,
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	close(release)
	var report SeedLifecycleReport
	select {
	case report = <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("accounting failure was not reported")
	}
	if report.Kind != SeedLifecycleAccountingIncomplete || report.PhytomerID != result.PhytomerID || report.Handle != result.Handle {
		t.Fatalf("report = %+v", report)
	}
	if _, err := svc.ResolveContinuationTarget(ctx, result.PhytomerID); !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("resolve after accounting failure: %v", err)
	}
	if _, err := svc.ContinuePhytomer(ctx, ContinuationInput{
		PhytomerID: result.PhytomerID, Intent: "keep going", IdempotencyKey: "k1",
	}); !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("continue after accounting failure: %v", err)
	}
	if accepted != 0 {
		t.Fatalf("continuation accepted %d time(s)", accepted)
	}
}

func TestDetachedFailedRunAccountingFailureQuarantines(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	reported := make(chan SeedLifecycleReport, 1)
	var live ContinuationTarget
	port := wiredContinuationPersistence()
	port.ResolveTarget = func(_ context.Context, phytomerID string) (ContinuationTarget, bool, error) {
		if phytomerID != live.PhytomerID {
			return ContinuationTarget{}, false, nil
		}
		return live, true, nil
	}
	port.AccountTerminalFailure = func(context.Context, SeedSettlement) (TerminalFailureAccount, error) {
		return TerminalFailureAccount{}, errors.New("account persist failed")
	}
	svc := newDetachedSeedService(t, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		close(started)
		<-release
		return SeedGrowResult{Status: SeedStatusWithered, Iterations: 1, PhytomerID: spec.PhytomerID}, errors.New("sprout failed")
	})
	svc.WithContinuationPersistence(port).WithSeedLifecycleReporter(func(report SeedLifecycleReport) {
		reported <- report
	})
	ctx := WithPollen(context.Background(), "claude")
	result, err := svc.SeedGrow(ctx, detachedSeedInput())
	if err != nil {
		t.Fatalf("detached grow: %v", err)
	}
	live = ContinuationTarget{
		PhytomerID: result.PhytomerID, Handle: result.Handle, Pollen: "claude", Substrate: "core", Status: SeedStatusRunning,
	}
	<-started
	close(release)
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("failed-run accounting failure was not reported")
	}
	if _, err := svc.ResolveContinuationTarget(ctx, result.PhytomerID); !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("resolve: %v", err)
	}
}

func TestDetachedFailedRunWithCommittedAccountingIsNotQuarantine(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	accounted := make(chan struct{})
	reported := make(chan SeedLifecycleReport, 1)
	var live ContinuationTarget
	port := wiredContinuationPersistence()
	port.ResolveTarget = func(_ context.Context, phytomerID string) (ContinuationTarget, bool, error) {
		if phytomerID != live.PhytomerID {
			return ContinuationTarget{}, false, nil
		}
		return live, true, nil
	}
	port.AccountTerminalFailure = func(_ context.Context, settled SeedSettlement) (TerminalFailureAccount, error) {
		live.Status = settled.Status
		close(accounted)
		return TerminalFailureAccount{}, nil
	}
	svc := newDetachedSeedService(t, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		close(started)
		<-release
		return SeedGrowResult{Status: SeedStatusWithered, Iterations: 1, PhytomerID: spec.PhytomerID}, errors.New("sprout failed")
	})
	svc.WithContinuationPersistence(port).WithSeedLifecycleReporter(func(report SeedLifecycleReport) {
		reported <- report
	})
	ctx := WithPollen(context.Background(), "claude")
	result, err := svc.SeedGrow(ctx, detachedSeedInput())
	if err != nil {
		t.Fatalf("detached grow: %v", err)
	}
	live = ContinuationTarget{
		PhytomerID: result.PhytomerID, Handle: result.Handle, Pollen: "claude", Substrate: "core", Status: SeedStatusRunning,
	}
	<-started
	close(release)
	select {
	case <-accounted:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal accounting did not commit")
	}
	select {
	case report := <-reported:
		t.Fatalf("committed accounting reported as failure: %+v", report)
	default:
	}
	if _, err := svc.ResolveContinuationTarget(ctx, result.PhytomerID); !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("terminal seed still continuation-eligible: %v", err)
	}
}

func TestDetachedSuccessfulSettlementDoesNotReportAccountingFailure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	settled := make(chan struct{})
	reported := make(chan SeedLifecycleReport, 1)
	port := wiredContinuationPersistence()
	port.CompleteSuccessfulSettlement = func(context.Context, SeedSettlement) error {
		close(settled)
		return nil
	}
	svc := newDetachedSeedService(t, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		close(started)
		<-release
		return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
	})
	svc.WithContinuationPersistence(port).WithSeedLifecycleReporter(func(report SeedLifecycleReport) {
		reported <- report
	})
	if _, err := svc.SeedGrow(context.Background(), detachedSeedInput()); err != nil {
		t.Fatalf("detached grow: %v", err)
	}
	<-started
	close(release)
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("successful settlement did not complete")
	}
	select {
	case report := <-reported:
		t.Fatalf("successful settlement reported: %+v", report)
	default:
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
	dispatch, err := svc.OpenPreparedSeed(context.Background(), growth)
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
	var mu sync.Mutex
	openings := make(map[string]SeedOpening)
	openingKey := func(pollen, key string) string { return pollen + "\x00" + key }
	return NewService(manager).WithSeed(SeedOperations{Run: run}).WithSeedPersistence(SeedPersistence{
		FindOpening: func(_ context.Context, pollen, key string) (SeedOpening, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			opening, found := openings[openingKey(pollen, key)]
			return opening, found, nil
		},
		RecordOpening: func(_ context.Context, opening SeedOpening) error {
			mu.Lock()
			defer mu.Unlock()
			key := openingKey(opening.Pollen, opening.IdempotencyKey)
			if _, found := openings[key]; found {
				return errors.New("duplicate seed idempotency key")
			}
			openings[key] = opening
			return nil
		},
		RecordSettlement: func(context.Context, SeedSettlement) error { return nil },
	}).WithContinuationPersistence(wiredContinuationPersistence())
}

func detachedSeedInput() SeedGrowInput {
	in := validSeedInput()
	in.Detached = true
	in.IdempotencyKey = "detached-test-key"
	return in
}

type seedOpeningKey struct {
	pollen string
	key    string
}

type seedOpeningMemory struct {
	mu                 sync.Mutex
	byKey              map[seedOpeningKey]SeedOpening
	byHandle           map[string]struct{}
	openingCount       atomic.Int32
	initialLookupCount atomic.Int32
	initialLookupGate  chan struct{}
}

func newSeedOpeningMemory() *seedOpeningMemory {
	return &seedOpeningMemory{byKey: make(map[seedOpeningKey]SeedOpening), byHandle: make(map[string]struct{})}
}

func (m *seedOpeningMemory) persistence() SeedPersistence {
	return SeedPersistence{
		FindOpening: func(_ context.Context, pollen, key string) (SeedOpening, bool, error) {
			m.mu.Lock()
			opening, found := m.byKey[seedOpeningKey{pollen: pollen, key: key}]
			m.mu.Unlock()
			if m.initialLookupGate != nil {
				count := m.initialLookupCount.Add(1)
				if count <= 2 {
					if count == 2 {
						close(m.initialLookupGate)
					}
					<-m.initialLookupGate
				}
			}
			return opening, found, nil
		},
		RecordOpening: func(_ context.Context, opening SeedOpening) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			key := seedOpeningKey{pollen: opening.Pollen, key: opening.IdempotencyKey}
			if _, found := m.byKey[key]; found {
				return errors.New("unique detached Seed retry identity")
			}
			if _, found := m.byHandle[opening.Handle]; found {
				return errors.New("duplicate detached Seed handle")
			}
			m.byKey[key] = opening
			m.byHandle[opening.Handle] = struct{}{}
			m.openingCount.Add(1)
			return nil
		},
		RecordSettlement: func(_ context.Context, settled SeedSettlement) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			for key, opening := range m.byKey {
				if opening.Handle == settled.Handle {
					opening.Status = settled.Status
					m.byKey[key] = opening
				}
			}
			return nil
		},
	}
}

func newSeedServiceWithOpeningMemory(t *testing.T, memory *seedOpeningMemory, run func(context.Context, SeedSpec, *SeedContinuationLifecycle) (SeedGrowResult, error)) *Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	return NewService(manager).WithSeed(SeedOperations{Run: run}).WithSeedPersistence(memory.persistence()).WithContinuationPersistence(wiredContinuationPersistence())
}

func TestSeedGrowDetachedRequiresIdempotencyKeyBeforePreparing(t *testing.T) {
	memory := newSeedOpeningMemory()
	var prepared, ran atomic.Int32
	svc := newSeedServiceWithOpeningMemory(t, memory, func(context.Context, SeedSpec, *SeedContinuationLifecycle) (SeedGrowResult, error) {
		ran.Add(1)
		return SeedGrowResult{}, nil
	})
	svc.newPreparedSeedToken = func() (string, error) {
		prepared.Add(1)
		return "prepared", nil
	}
	in := detachedSeedInput()
	in.IdempotencyKey = "  \t"
	if _, err := svc.SeedGrow(context.Background(), in); !errors.Is(err, ErrSeedIdempotencyKeyRequired) {
		t.Fatalf("detached grow without key = %v, want ErrSeedIdempotencyKeyRequired", err)
	}
	if prepared.Load() != 0 || memory.openingCount.Load() != 0 || ran.Load() != 0 {
		t.Fatalf("missing key began work: prepared=%d openings=%d runs=%d", prepared.Load(), memory.openingCount.Load(), ran.Load())
	}
}

func TestSeedGrowDetachedReplayReturnsExistingIdentityWithoutWork(t *testing.T) {
	memory := newSeedOpeningMemory()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var runs, prepared atomic.Int32
	svc := newSeedServiceWithOpeningMemory(t, memory, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		if runs.Add(1) == 1 {
			started <- struct{}{}
		}
		<-release
		return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
	})
	svc.newPreparedSeedToken = func() (string, error) {
		return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil
	}
	defer close(release)

	ctx := WithPollen(context.Background(), "pollen-one")
	firstInput := detachedSeedInput()
	firstInput.IdempotencyKey = "  detached-test-key  "
	first, err := svc.SeedGrow(ctx, firstInput)
	if err != nil {
		t.Fatalf("first detached grow: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first background Seed did not start")
	}
	memory.mu.Lock()
	storedKey := seedOpeningKey{pollen: "pollen-one", key: "detached-test-key"}
	opening := memory.byKey[storedKey]
	opening.Status = SeedStatusSatisfied
	memory.byKey[storedKey] = opening
	memory.mu.Unlock()

	replayInput := detachedSeedInput()
	replayInput.Origin = "mcp"
	replayInput.Egress = []string{"changed.example"}
	second, err := svc.SeedGrow(ctx, replayInput)
	if err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	if second.Handle != first.Handle || second.PhytomerID != first.PhytomerID || second.Status != SeedStatusSatisfied {
		t.Fatalf("replay = %+v, first = %+v", second, first)
	}
	if memory.openingCount.Load() != 1 || prepared.Load() != 1 {
		t.Fatalf("replay created work: openings=%d prepared=%d", memory.openingCount.Load(), prepared.Load())
	}
	select {
	case <-started:
		t.Fatal("replay started a second background Seed")
	case <-time.After(50 * time.Millisecond):
	}
	if runs.Load() != 1 {
		t.Fatalf("background runs = %d, want 1", runs.Load())
	}
}

func TestSeedGrowDetachedSemanticConflictFields(t *testing.T) {
	cases := []struct {
		name   string
		change func(*SeedGrowInput)
	}{
		{name: "substrate", change: func(in *SeedGrowInput) { in.Substrate = "other" }},
		{name: "goal", change: func(in *SeedGrowInput) { in.Goal = "different goal" }},
		{name: "verify argv", change: func(in *SeedGrowInput) { in.Verify = []string{"go", "test", "./cmd/stem"} }},
		{name: "verify argv token bytes", change: func(in *SeedGrowInput) { in.Verify[2] = " ./..." }},
		{name: "effective iterations", change: func(in *SeedGrowInput) { in.MaxIterations = 4 }},
		{name: "effective timeout", change: func(in *SeedGrowInput) { in.TimeoutSeconds = 901 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			memory := newSeedOpeningMemory()
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var prepared atomic.Int32
			svc := newSeedServiceWithOpeningMemory(t, memory, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
				started <- struct{}{}
				<-release
				return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
			})
			svc.newPreparedSeedToken = func() (string, error) { return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil }
			defer close(release)

			ctx := WithPollen(context.Background(), "pollen-one")
			if _, err := svc.SeedGrow(ctx, detachedSeedInput()); err != nil {
				t.Fatalf("open: %v", err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("background Seed did not start")
			}
			changed := detachedSeedInput()
			tc.change(&changed)
			if _, err := svc.SeedGrow(ctx, changed); !errors.Is(err, ErrSeedIdempotencyConflict) {
				t.Fatalf("changed semantic request = %v, want ErrSeedIdempotencyConflict", err)
			}
			if memory.openingCount.Load() != 1 || prepared.Load() != 1 {
				t.Fatalf("conflict created work: openings=%d prepared=%d", memory.openingCount.Load(), prepared.Load())
			}
		})
	}
}

func TestSeedGrowDetachedEffectiveDefaultsAndCapsShareDigest(t *testing.T) {
	cases := []struct {
		name          string
		first, second SeedGrowInput
	}{
		{name: "defaults", first: detachedSeedInput(), second: func() SeedGrowInput {
			in := detachedSeedInput()
			in.MaxIterations = seedDefaultMaxIterations
			in.TimeoutSeconds = int(seedDefaultTimeout / time.Second)
			return in
		}()},
		{name: "trimmed substrate and goal", first: func() SeedGrowInput {
			in := detachedSeedInput()
			in.Substrate = " core "
			in.Goal = " make the tests pass "
			return in
		}(), second: detachedSeedInput()},
		{name: "caps", first: func() SeedGrowInput {
			in := detachedSeedInput()
			in.MaxIterations = 100
			in.TimeoutSeconds = int(^uint(0) >> 1)
			return in
		}(), second: func() SeedGrowInput {
			in := detachedSeedInput()
			in.MaxIterations = seedMaximumMaxIterations
			in.TimeoutSeconds = int(seedMaximumTimeout / time.Second)
			return in
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			memory := newSeedOpeningMemory()
			var runs, prepared atomic.Int32
			svc := newSeedServiceWithOpeningMemory(t, memory, func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
				runs.Add(1)
				return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
			})
			svc.newPreparedSeedToken = func() (string, error) { return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil }
			ctx := WithPollen(context.Background(), "pollen-one")
			first, err := svc.SeedGrow(ctx, tc.first)
			if err != nil {
				t.Fatalf("first open: %v", err)
			}
			second, err := svc.SeedGrow(ctx, tc.second)
			if err != nil {
				t.Fatalf("equivalent replay: %v", err)
			}
			if first.Handle != second.Handle || first.PhytomerID != second.PhytomerID {
				t.Fatalf("replay identity changed: first=%+v second=%+v", first, second)
			}
			if memory.openingCount.Load() != 1 || prepared.Load() != 1 {
				t.Fatalf("equivalent replay created work: openings=%d prepared=%d", memory.openingCount.Load(), prepared.Load())
			}
		})
	}
}

func TestSeedGrowDetachedSameKeyIsIndependentAcrossPollens(t *testing.T) {
	memory := newSeedOpeningMemory()
	var prepared atomic.Int32
	svc := newSeedServiceWithOpeningMemory(t, memory, func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
	})
	svc.newPreparedSeedToken = func() (string, error) { return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil }
	input := detachedSeedInput()
	first, err := svc.SeedGrow(WithPollen(context.Background(), "pollen-one"), input)
	if err != nil {
		t.Fatalf("first Pollen: %v", err)
	}
	second, err := svc.SeedGrow(WithPollen(context.Background(), "pollen-two"), input)
	if err != nil {
		t.Fatalf("second Pollen: %v", err)
	}
	if first.Handle == second.Handle || first.PhytomerID == second.PhytomerID || memory.openingCount.Load() != 2 {
		t.Fatalf("same key was not independent by Pollen: first=%+v second=%+v openings=%d", first, second, memory.openingCount.Load())
	}
}

func TestSeedGrowDetachedConcurrentSameKeyHasOneOpeningAndRun(t *testing.T) {
	memory := newSeedOpeningMemory()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var prepared, runs atomic.Int32
	svc := newSeedServiceWithOpeningMemory(t, memory, func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		runs.Add(1)
		started <- struct{}{}
		<-release
		return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
	})
	svc.newPreparedSeedToken = func() (string, error) { return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil }
	defer close(release)
	ctx := WithPollen(context.Background(), "pollen-one")
	results := make(chan SeedGrowResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := svc.SeedGrow(ctx, detachedSeedInput())
			results <- result
			errs <- err
		}()
	}
	first, second := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatalf("first concurrent open: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("second concurrent open: %v", err)
	}
	if first.Handle != second.Handle || first.PhytomerID != second.PhytomerID {
		t.Fatalf("concurrent opens chose different identities: %+v and %+v", first, second)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background Seed did not start")
	}
	select {
	case <-started:
		t.Fatal("second background Seed started")
	case <-time.After(50 * time.Millisecond):
	}
	if memory.openingCount.Load() != 1 || prepared.Load() != 1 || runs.Load() != 1 {
		t.Fatalf("concurrent opens duplicated work: openings=%d prepared=%d runs=%d", memory.openingCount.Load(), prepared.Load(), runs.Load())
	}
}

func TestSeedGrowDetachedPersistenceUniquenessRaceReturnsWinner(t *testing.T) {
	memory := newSeedOpeningMemory()
	memory.initialLookupGate = make(chan struct{})
	started := make(chan struct{}, 2)
	var prepared, runs atomic.Int32
	run := func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		runs.Add(1)
		started <- struct{}{}
		return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
	}
	svc1 := newSeedServiceWithOpeningMemory(t, memory, run)
	svc2 := newSeedServiceWithOpeningMemory(t, memory, run)
	for _, svc := range []*Service{svc1, svc2} {
		svc.newPreparedSeedToken = func() (string, error) { return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil }
	}
	ctx := WithPollen(context.Background(), "pollen-one")
	results := make(chan SeedGrowResult, 2)
	errs := make(chan error, 2)
	for _, svc := range []*Service{svc1, svc2} {
		go func(svc *Service) {
			result, err := svc.SeedGrow(ctx, detachedSeedInput())
			results <- result
			errs <- err
		}(svc)
	}
	first, second := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatalf("first race result: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("second race result: %v", err)
	}
	if first.Handle != second.Handle || first.PhytomerID != second.PhytomerID {
		t.Fatalf("uniqueness race did not recover winner: %+v and %+v", first, second)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("winning background Seed did not start")
	}
	select {
	case <-started:
		t.Fatal("losing opening launched background execution")
	case <-time.After(50 * time.Millisecond):
	}
	if memory.openingCount.Load() != 1 || runs.Load() != 1 {
		t.Fatalf("uniqueness race accepted duplicate work: openings=%d runs=%d", memory.openingCount.Load(), runs.Load())
	}
}

func TestSeedGrowDetachedUniquenessRaceWithChangedSemanticsConflicts(t *testing.T) {
	memory := newSeedOpeningMemory()
	memory.initialLookupGate = make(chan struct{})
	started := make(chan struct{}, 2)
	var prepared, runs atomic.Int32
	run := func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		runs.Add(1)
		started <- struct{}{}
		return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
	}
	svc1 := newSeedServiceWithOpeningMemory(t, memory, run)
	svc2 := newSeedServiceWithOpeningMemory(t, memory, run)
	for _, svc := range []*Service{svc1, svc2} {
		svc.newPreparedSeedToken = func() (string, error) { return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil }
	}
	ctx := WithPollen(context.Background(), "pollen-one")
	input1 := detachedSeedInput()
	input2 := detachedSeedInput()
	input2.Goal = "different goal"
	type outcome struct {
		result SeedGrowResult
		err    error
	}
	results := make(chan outcome, 2)
	for i, svc := range []*Service{svc1, svc2} {
		input := input1
		if i == 1 {
			input = input2
		}
		go func(svc *Service, input SeedGrowInput) {
			result, err := svc.SeedGrow(ctx, input)
			results <- outcome{result: result, err: err}
		}(svc, input)
	}
	first, second := <-results, <-results
	var accepted int
	var conflicts int
	for _, got := range []outcome{first, second} {
		switch {
		case got.err == nil && got.result.Handle != "" && got.result.PhytomerID != "":
			accepted++
		case errors.Is(got.err, ErrSeedIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("race outcome = %+v, want one accepted winner and one semantic conflict", got)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("accepted=%d conflicts=%d, want one each", accepted, conflicts)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("winning background Seed did not start")
	}
	select {
	case <-started:
		t.Fatal("losing semantic request launched background execution")
	case <-time.After(50 * time.Millisecond):
	}
	if memory.openingCount.Load() != 1 || runs.Load() != 1 {
		t.Fatalf("changed-semantic race duplicated work: openings=%d runs=%d", memory.openingCount.Load(), runs.Load())
	}
}

func TestSeedGrowDetachedAmbiguousCommittedOpeningLaunchesExactlyOnce(t *testing.T) {
	memory := newSeedOpeningMemory()
	persistence := memory.persistence()
	recordOpening := persistence.RecordOpening
	persistence.RecordOpening = func(ctx context.Context, opening SeedOpening) error {
		if err := recordOpening(ctx, opening); err != nil {
			return err
		}
		return errors.New("opening committed but acknowledgement was lost")
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRun()
	finished := make(chan struct{}, 1)
	var prepared, runs atomic.Int32
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(ctx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			runs.Add(1)
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return SeedGrowResult{}, ctx.Err()
			}
			finished <- struct{}{}
			return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSeedPersistence(persistence).WithContinuationPersistence(wiredContinuationPersistence())
	svc.newPreparedSeedToken = func() (string, error) {
		return fmt.Sprintf("prepared-%d", prepared.Add(1)), nil
	}
	ctx := WithPollen(context.Background(), "pollen-one")
	first, err := svc.SeedGrow(ctx, detachedSeedInput())
	if err != nil {
		t.Fatalf("commit-then-error opening recovery: %v", err)
	}
	if first.Handle == "" || first.PhytomerID == "" || first.Status != SeedStatusRunning {
		t.Fatalf("recovered opening = %+v", first)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("committed opening was not launched after error recovery")
	}
	second, err := svc.SeedGrow(ctx, detachedSeedInput())
	if err != nil {
		t.Fatalf("retry committed opening: %v", err)
	}
	if second.Handle != first.Handle || second.PhytomerID != first.PhytomerID || second.Status != first.Status {
		t.Fatalf("retry identity differs: first=%+v second=%+v", first, second)
	}
	if memory.openingCount.Load() != 1 || prepared.Load() != 1 || runs.Load() != 1 {
		t.Fatalf("ambiguous commit duplicated work: openings=%d prepared=%d runs=%d", memory.openingCount.Load(), prepared.Load(), runs.Load())
	}
	select {
	case <-started:
		t.Fatal("retry launched a second background Seed")
	case <-time.After(50 * time.Millisecond):
	}
	releaseRun()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("background Seed did not finish after release")
	}
}

func TestSeedGrowDetachedDurableUniquenessRacePrunesLosingPhytomer(t *testing.T) {
	t.Setenv(historydb.EnvEncryptAtRest, "off")
	ctx := context.Background()
	dbDir := t.TempDir()
	path := filepath.Join(dbDir, "history.db")
	store, err := historydb.Open(ctx, path)
	if err != nil {
		t.Fatalf("open HistoryDB: %v", err)
	}
	defer store.Close()

	// Both Core instances read absence before either can prepare and insert.
	// The real HistoryDB unique index chooses one accepted opening; the losing
	// Core must remove its newly persisted Phytomer before returning the winner.
	findGate := make(chan struct{})
	var lookups atomic.Int32
	findOpening := func(findCtx context.Context, pollen, key string) (SeedOpening, bool, error) {
		run, found, err := store.GetSeedRunByPollenIdempotencyKey(findCtx, pollen, key)
		if err != nil {
			return SeedOpening{}, false, err
		}
		lookupNumber := lookups.Add(1)
		if lookupNumber <= 2 {
			if lookupNumber == 2 {
				close(findGate)
			}
			select {
			case <-findGate:
			case <-findCtx.Done():
				return SeedOpening{}, false, findCtx.Err()
			}
		}
		if !found {
			return SeedOpening{}, false, nil
		}
		return SeedOpening{
			Handle: run.Handle, PhytomerID: run.PhytomerID, Pollen: run.Pollen,
			Substrate: run.Substrate, Goal: run.Goal, IdempotencyKey: run.IdempotencyKey,
			RequestDigest: run.RequestDigest, Status: run.Status, StartedAt: run.StartedAt,
		}, true, nil
	}
	persistence := SeedPersistence{
		FindOpening: findOpening,
		RecordOpening: func(recordCtx context.Context, opening SeedOpening) error {
			return store.RecordSeedOpening(recordCtx, historydb.SeedRun{
				Handle: opening.Handle, Pollen: opening.Pollen, PhytomerID: opening.PhytomerID,
				Substrate: opening.Substrate, Goal: opening.Goal, IdempotencyKey: opening.IdempotencyKey,
				RequestDigest: opening.RequestDigest, Status: opening.Status, StartedAt: opening.StartedAt,
			})
		},
		RecordSettlement: func(recordCtx context.Context, settled SeedSettlement) error {
			return store.RecordSeedRun(recordCtx, historydb.SeedRun{
				Handle: settled.Handle, Pollen: settled.Pollen, PhytomerID: settled.PhytomerID,
				Substrate: settled.Substrate, Goal: settled.Goal, Status: settled.Status,
				Iterations: settled.Iterations, Branch: settled.Branch, Commit: settled.Commit,
				Diff: settled.Diff, Logs: settled.Logs, Error: settled.Error,
				StartedAt: settled.StartedAt, FinishedAt: settled.FinishedAt,
			})
		},
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRun()
	settled := make(chan struct{}, 1)
	var runs atomic.Int32
	run := func(runCtx context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
		runs.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-runCtx.Done():
			return SeedGrowResult{}, runCtx.Err()
		}
		return SeedGrowResult{Status: SeedStatusSatisfied, PhytomerID: spec.PhytomerID}, nil
	}
	continuation := wiredContinuationPersistence()
	continuation.CompleteSuccessfulSettlement = func(context.Context, SeedSettlement) error {
		settled <- struct{}{}
		return nil
	}
	makeService := func() *Service {
		manager, err := session.NewManager(ctx, store)
		if err != nil {
			t.Fatalf("session manager: %v", err)
		}
		return NewService(manager).WithSeed(SeedOperations{Run: run}).WithSeedPersistence(persistence).WithContinuationPersistence(continuation)
	}
	firstService, secondService := makeService(), makeService()
	requestCtx := WithPollen(ctx, "pollen-one")
	results := make(chan SeedGrowResult, 2)
	errs := make(chan error, 2)
	for _, svc := range []*Service{firstService, secondService} {
		go func(svc *Service) {
			result, err := svc.SeedGrow(requestCtx, detachedSeedInput())
			results <- result
			errs <- err
		}(svc)
	}
	first, second := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatalf("first durable race result: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("second durable race result: %v", err)
	}
	if first.Handle != second.Handle || first.PhytomerID != second.PhytomerID {
		t.Fatalf("durable uniqueness race did not recover winner: %+v and %+v", first, second)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("winning background Seed did not start")
	}
	select {
	case <-started:
		t.Fatal("losing opening launched a second background Seed")
	case <-time.After(50 * time.Millisecond):
	}
	sessions, err := store.LoadSessions(ctx)
	if err != nil {
		t.Fatalf("load persisted Phytomers: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != first.PhytomerID {
		t.Fatalf("losing Phytomer was not pruned: %+v, want only %q", sessions, first.PhytomerID)
	}
	if runs.Load() != 1 {
		t.Fatalf("background execution count = %d, want 1", runs.Load())
	}
	releaseRun()
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("winning background Seed did not complete settlement after release")
	}
}

type seedPhytomerDeleteFailStore struct {
	sessions map[string]session.Phytomer
}

func (s *seedPhytomerDeleteFailStore) SaveSession(_ context.Context, phytomer session.Phytomer) error {
	s.sessions[phytomer.ID] = phytomer
	return nil
}

func (*seedPhytomerDeleteFailStore) DeleteSession(context.Context, string) error {
	return errors.New("session storage is temporarily unavailable")
}

func (s *seedPhytomerDeleteFailStore) LoadSessions(context.Context) ([]session.Phytomer, error) {
	rows := make([]session.Phytomer, 0, len(s.sessions))
	for _, phytomer := range s.sessions {
		rows = append(rows, phytomer)
	}
	return rows, nil
}

func (s *seedPhytomerDeleteFailStore) LoadSession(_ context.Context, id string) (session.Phytomer, bool, error) {
	phytomer, found := s.sessions[id]
	return phytomer, found, nil
}

func (*seedPhytomerDeleteFailStore) AppendMessage(context.Context, session.Message) error { return nil }

func (*seedPhytomerDeleteFailStore) LoadMessages(context.Context, string, int) ([]session.Message, error) {
	return nil, nil
}
