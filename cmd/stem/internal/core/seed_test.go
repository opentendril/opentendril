package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// newSeedService wires a Service with a stubbed seed port and returns the
// captured spec of the last grow.
func newSeedService(t *testing.T) (*Service, *SeedSpec) {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	captured := &SeedSpec{}
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			*captured = spec
			return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1}, nil
		},
	})
	return svc, captured
}

func validSeedInput() SeedGrowInput {
	return SeedGrowInput{Substrate: "core", Goal: "make the tests pass", Verify: []string{"go", "test", "./..."}}
}

func TestSeedGrowValidatesInput(t *testing.T) {
	svc, _ := newSeedService(t)
	ctx := context.Background()

	if _, err := svc.SeedGrow(ctx, SeedGrowInput{Goal: "g", Verify: []string{"true"}}); err == nil {
		t.Fatal("missing substrate accepted")
	}
	if _, err := svc.SeedGrow(ctx, SeedGrowInput{Substrate: "core", Verify: []string{"true"}}); err == nil {
		t.Fatal("missing goal accepted")
	}
	if _, err := svc.SeedGrow(ctx, SeedGrowInput{Substrate: "core", Goal: "g"}); err == nil {
		t.Fatal("missing verify accepted")
	}
	if _, err := svc.SeedGrow(ctx, SeedGrowInput{Substrate: "core", Goal: "g", Verify: []string{"  "}}); err == nil {
		t.Fatal("blank verify token accepted")
	}
	in := validSeedInput()
	in.MaxIterations = -1
	if _, err := svc.SeedGrow(ctx, in); err == nil {
		t.Fatal("negative maxIterations accepted")
	}
	in = validSeedInput()
	in.TimeoutSeconds = -1
	if _, err := svc.SeedGrow(ctx, in); err == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestSeedGrowNotWired(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.SeedGrow(context.Background(), validSeedInput())
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired seed error = %v, want a not-wired report", err)
	}
}

// TestSeedGrowBoundsAreClamped proves a caller can only narrow, never widen, the
// Stem-owned bounds: defaults apply when unset, explicit values pass through,
// and values above the cap are clamped down.
func TestSeedGrowBoundsAreClamped(t *testing.T) {
	svc, captured := newSeedService(t)
	ctx := context.Background()

	if _, err := svc.SeedGrow(ctx, validSeedInput()); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if captured.MaxIterations != seedDefaultMaxIterations {
		t.Fatalf("default maxIterations = %d, want %d", captured.MaxIterations, seedDefaultMaxIterations)
	}
	if captured.Timeout != seedDefaultTimeout {
		t.Fatalf("default timeout = %v, want %v", captured.Timeout, seedDefaultTimeout)
	}

	in := validSeedInput()
	in.MaxIterations = 2
	in.TimeoutSeconds = 30
	if _, err := svc.SeedGrow(ctx, in); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if captured.MaxIterations != 2 || captured.Timeout != 30*time.Second {
		t.Fatalf("explicit bounds not honored: iterations=%d timeout=%v", captured.MaxIterations, captured.Timeout)
	}

	in = validSeedInput()
	in.MaxIterations = 9999
	in.TimeoutSeconds = 999999
	if _, err := svc.SeedGrow(ctx, in); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if captured.MaxIterations != seedMaximumMaxIterations {
		t.Fatalf("excess maxIterations = %d, want the %d cap", captured.MaxIterations, seedMaximumMaxIterations)
	}
	if captured.Timeout != seedMaximumTimeout {
		t.Fatalf("excess timeout = %v, want the %v cap", captured.Timeout, seedMaximumTimeout)
	}
}

// TestSeedEgressNotDecodableFromInput is the no-self-escalation guarantee at the
// capability boundary: an input map that smuggles an "egress" key never reaches
// the execution port — the allow-list is set only programmatically by the Stem's
// own call sites from an authorized delegation grant.
func TestSeedEgressNotDecodableFromInput(t *testing.T) {
	svc, captured := newSeedService(t)

	_, err := svc.Invoke(context.Background(), CapSeedGrow, map[string]any{
		"substrate": "core",
		"goal":      "make the tests pass",
		"verify":    []any{"true"},
		"egress":    []any{"evil.example.com"},
		"Egress":    []any{"evil.example.com"},
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(captured.Egress) != 0 {
		t.Fatalf("caller input widened egress to %v; the allow-list must be grant-supplied only", captured.Egress)
	}
}

// TestSeedEgressThreadedFromGrantMaterial covers the legitimate path: a Stem
// call site sets Egress programmatically and the spec carries it.
func TestSeedEgressThreadedFromGrantMaterial(t *testing.T) {
	svc, captured := newSeedService(t)

	in := validSeedInput()
	in.Egress = []string{"proxy.golang.org"}
	if _, err := svc.SeedGrow(context.Background(), in); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if len(captured.Egress) != 1 || captured.Egress[0] != "proxy.golang.org" {
		t.Fatalf("spec egress = %v, want the grant-supplied allow-list", captured.Egress)
	}
}

// TestSeedGrowIsDelegated pins seed.grow into the delegated set: it must pass the
// grant gate before it can run on a Pollinator-facing surface.
func TestSeedGrowIsDelegated(t *testing.T) {
	if !IsDelegatedCapability(CapSeedGrow) {
		t.Fatal("seed.grow is not in the delegated set; it would run ungoverned on delegated surfaces")
	}
}

func TestSeedGrowEstablishesOneCanonicalPhytomer(t *testing.T) {
	svc, captured := newSeedService(t)

	result, err := svc.SeedGrow(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("grow: %v", err)
	}
	if result.PhytomerID == "" || !strings.HasPrefix(result.PhytomerID, session.IDPrefix) {
		t.Fatalf("phytomerId = %q, want a Stem-created phytomer identity", result.PhytomerID)
	}
	if captured.PhytomerID != result.PhytomerID {
		t.Fatalf("spec phytomer %q != result phytomer %q", captured.PhytomerID, result.PhytomerID)
	}
}

func TestTwoSeedsReceiveDistinctPhytomers(t *testing.T) {
	svc, _ := newSeedService(t)
	first, err := svc.SeedGrow(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("first grow: %v", err)
	}
	second, err := svc.SeedGrow(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("second grow: %v", err)
	}
	if first.PhytomerID == "" || second.PhytomerID == "" {
		t.Fatal("a Seed grew without a Phytomer")
	}
	if first.PhytomerID == second.PhytomerID {
		t.Fatalf("two Seeds shared phytomer %q", first.PhytomerID)
	}
}

func TestConcurrentSeedsReceiveDistinctPhytomers(t *testing.T) {
	svc, _ := newSeedService(t)
	const n = 8
	ids := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			result, err := svc.SeedGrow(context.Background(), validSeedInput())
			if err != nil {
				t.Errorf("grow %d: %v", i, err)
				return
			}
			ids[i] = result.PhytomerID
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, id := range ids {
		if id == "" {
			t.Fatalf("grow %d produced no phytomer", i)
		}
		if seen[id] {
			t.Fatalf("phytomer %q was issued more than once", id)
		}
		seen[id] = true
	}
}

func TestPrepareSeedThenGrowPreparedUsesTheSameEnvelope(t *testing.T) {
	svc, captured := newSeedService(t)
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if growth.PhytomerID() == "" {
		t.Fatal("prepare minted no phytomer")
	}

	result, err := svc.GrowPreparedSeed(context.Background(), growth)
	if err != nil {
		t.Fatalf("grow prepared: %v", err)
	}
	if result.PhytomerID != growth.PhytomerID() || captured.PhytomerID != growth.PhytomerID() {
		t.Fatalf("prepared phytomer %q was not executed (result=%q spec=%q)", growth.PhytomerID(), result.PhytomerID, captured.PhytomerID)
	}
}

func TestPreparedSeedCannotBeSubstitutedIntoADifferentGrowth(t *testing.T) {
	svc, captured := newSeedService(t)
	firstIn := validSeedInput()
	firstIn.Goal = "first seed"
	secondIn := validSeedInput()
	secondIn.Goal = "second seed"

	growthA, err := svc.PrepareSeed(context.Background(), firstIn)
	if err != nil {
		t.Fatalf("prepare A: %v", err)
	}
	growthB, err := svc.PrepareSeed(context.Background(), secondIn)
	if err != nil {
		t.Fatalf("prepare B: %v", err)
	}
	if growthA.PhytomerID() == growthB.PhytomerID() {
		t.Fatal("two prepared Seeds shared a Phytomer")
	}

	growthA.phytomerID = growthB.PhytomerID()
	growthA.spec.PhytomerID = growthB.PhytomerID()
	if _, err := svc.GrowPreparedSeed(context.Background(), growthA); err == nil {
		t.Fatal("substituted phytomer was executed")
	} else if !errors.Is(err, ErrSeedGrowthInvalid) {
		t.Fatalf("substitution error = %v, want ErrSeedGrowthInvalid", err)
	}
	if captured.PhytomerID == growthB.PhytomerID() && captured.Goal == "first seed" {
		t.Fatal("Seed A executed under Seed B's Phytomer")
	}
}

func TestDifferentSeedCannotReusePhytomerOnSameSubstrate(t *testing.T) {
	svc, _ := newSeedService(t)
	growthA, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare A: %v", err)
	}
	growthB, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare B: %v", err)
	}
	if growthA.Substrate() != growthB.Substrate() {
		t.Fatal("test requires identical substrates")
	}
	if growthA.PhytomerID() == growthB.PhytomerID() {
		t.Fatal("same-substrate Seeds shared a Phytomer")
	}

	growthB.phytomerID = growthA.PhytomerID()
	growthB.spec.PhytomerID = growthA.PhytomerID()
	if _, err := svc.GrowPreparedSeed(context.Background(), growthB); err == nil {
		t.Fatal("same-substrate phytomer reuse was accepted")
	} else if !errors.Is(err, ErrSeedGrowthInvalid) {
		t.Fatalf("reuse error = %v, want ErrSeedGrowthInvalid", err)
	}

	result, err := svc.SeedGrow(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("fresh grow: %v", err)
	}
	if result.PhytomerID == growthA.PhytomerID() || result.PhytomerID == growthB.PhytomerID() {
		t.Fatalf("SeedGrow reused a prepared Phytomer %q", result.PhytomerID)
	}
}

func TestDifferentSubstrateCannotReusePreparedPhytomer(t *testing.T) {
	svc, _ := newSeedService(t)
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	growth.spec.Substrate = "other-substrate"
	if _, err := svc.GrowPreparedSeed(context.Background(), growth); err == nil {
		t.Fatal("different-substrate reuse of a prepared Phytomer was accepted")
	} else if !errors.Is(err, ErrSeedGrowthInvalid) {
		t.Fatalf("reuse error = %v, want ErrSeedGrowthInvalid", err)
	}

	other := validSeedInput()
	other.Substrate = "other-substrate"
	result, err := svc.SeedGrow(context.Background(), other)
	if err != nil {
		t.Fatalf("other grow: %v", err)
	}
	if result.PhytomerID == growth.PhytomerID() {
		t.Fatal("a different-Substrate Seed reused the prepared Phytomer")
	}
}

func TestZeroSeedGrowthIsRefused(t *testing.T) {
	svc, _ := newSeedService(t)
	if _, err := svc.GrowPreparedSeed(context.Background(), SeedGrowth{}); err == nil {
		t.Fatal("a zero SeedGrowth was executed")
	}
}

func TestSeedGrowInputHasNoPhytomerResumeField(t *testing.T) {
	if _, ok := reflect.TypeOf(SeedGrowInput{}).FieldByName("PhytomerID"); ok {
		t.Fatal("SeedGrowInput still exposes a Phytomer resume field")
	}
}

func TestCallerCannotSupplySeedPhytomerIdentity(t *testing.T) {
	svc, captured := newSeedService(t)
	_, err := svc.Invoke(context.Background(), CapSeedGrow, map[string]any{
		"substrate":  "core",
		"goal":       "make the tests pass",
		"verify":     []any{"true"},
		"phytomerId": "tendril-forged",
		"PhytomerID": "tendril-forged",
		"sessionId":  "tendril-forged",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if captured.PhytomerID == "tendril-forged" {
		t.Fatal("caller-supplied phytomer identity reached the execution port")
	}
	if captured.PhytomerID == "" {
		t.Fatal("Stem did not create a phytomer")
	}
}

func TestSeedPollenIsNotACallerField(t *testing.T) {
	svc, _ := newSeedService(t)
	ctx := WithPollen(context.Background(), "granted-pollen")
	_, err := svc.Invoke(ctx, CapSeedGrow, map[string]any{
		"substrate": "core",
		"goal":      "make the tests pass",
		"verify":    []any{"true"},
		"pollen":    "attacker",
		"Pollen":    "attacker",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got := PollenFromContext(ctx); got != "granted-pollen" {
		t.Fatalf("context pollen = %q, want granted-pollen", got)
	}
}

func TestPrepareSeedIsNotAGovernedCapability(t *testing.T) {
	for _, name := range CapabilityNames() {
		if name == "seed.prepare" || name == "seed.watch" || name == "seed.growPrepared" {
			t.Fatalf("governed registry includes %q", name)
		}
	}
	if IsDelegatedCapability("seed.watch") || IsDelegatedCapability("seed.prepare") {
		t.Fatal("a Seed observation/prepare command was added to the delegated set")
	}
}

func TestOpenPreparedSeedComposesOwnershipFromTheEnvelope(t *testing.T) {
	svc, _ := newSeedService(t)
	var opening SeedOpening
	svc.WithSeedPersistence(SeedPersistence{
		RecordOpening: func(_ context.Context, got SeedOpening) error {
			opening = got
			return nil
		},
	}).WithContinuationPersistence(wiredContinuationPersistence())
	ctx := WithPollen(context.Background(), "claude")
	growth, err := svc.PrepareSeed(ctx, validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	dispatch, err := svc.OpenPreparedSeed(ctx, growth, "seed-handle-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if dispatch.Handle != "seed-handle-1" || dispatch.PhytomerID != growth.PhytomerID() || dispatch.Status != "running" {
		t.Fatalf("dispatch = %+v", dispatch)
	}
	if opening.Handle != "seed-handle-1" || opening.PhytomerID != growth.PhytomerID() || opening.Pollen != "claude" || opening.Substrate != "core" {
		t.Fatalf("opening composed from outside the envelope: %+v", opening)
	}

	foreign := validSeedInput()
	foreign.Substrate = "other"
	other, err := svc.PrepareSeed(ctx, foreign)
	if err != nil {
		t.Fatalf("prepare other: %v", err)
	}
	other.phytomerID = growth.PhytomerID()
	other.spec.PhytomerID = growth.PhytomerID()
	if _, err := svc.OpenPreparedSeed(ctx, other, "seed-stolen"); err == nil {
		t.Fatal("open accepted a substituted phytomer")
	}
}

func TestOpenPreparedSeedRefusesSecondHandle(t *testing.T) {
	svc, _ := newSeedService(t)
	var openings atomic.Int32
	svc.WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error {
			openings.Add(1)
			return nil
		},
	}).WithContinuationPersistence(wiredContinuationPersistence())
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(context.Background(), growth, "seed-a"); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(context.Background(), growth, "seed-b"); err == nil {
		t.Fatal("second open of the same growth was accepted")
	} else if !errors.Is(err, ErrSeedGrowthInvalid) {
		t.Fatalf("second open error = %v, want ErrSeedGrowthInvalid", err)
	}
	if openings.Load() != 1 {
		t.Fatalf("durable openings = %d, want 1", openings.Load())
	}
}

func TestOpenPreparedSeedRefusesConcurrentSecondHandle(t *testing.T) {
	svc, _ := newSeedService(t)
	persistStarted := make(chan struct{})
	persistRelease := make(chan struct{})
	var openings atomic.Int32
	svc.WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error {
			openings.Add(1)
			persistStarted <- struct{}{}
			<-persistRelease
			return nil
		},
	}).WithContinuationPersistence(wiredContinuationPersistence())
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	firstErr := make(chan error, 1)
	go func() {
		_, err := svc.OpenPreparedSeed(context.Background(), growth, "seed-a")
		firstErr <- err
	}()
	select {
	case <-persistStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first open never reached durable persist")
	}

	secondErr := make(chan error, 1)
	go func() {
		_, err := svc.OpenPreparedSeed(context.Background(), growth, "seed-b")
		secondErr <- err
	}()
	select {
	case err := <-secondErr:
		if err == nil {
			t.Fatal("concurrent second open succeeded")
		}
		if !errors.Is(err, ErrSeedGrowthInvalid) {
			t.Fatalf("concurrent second open error = %v, want ErrSeedGrowthInvalid", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent second open did not fail closed")
	}
	close(persistRelease)
	if err := <-firstErr; err != nil {
		t.Fatalf("first open: %v", err)
	}
	if openings.Load() != 1 {
		t.Fatalf("durable openings = %d, want 1", openings.Load())
	}
}

func TestPrepareSeedFailsClosedWhenTokenMintFails(t *testing.T) {
	svc, captured := newSeedService(t)
	svc.newPreparedSeedToken = func() (string, error) {
		return "", fmt.Errorf("crypto/rand failed")
	}
	if _, err := svc.PrepareSeed(context.Background(), validSeedInput()); err == nil {
		t.Fatal("prepare succeeded when token mint failed")
	}
	if captured.PhytomerID != "" {
		t.Fatal("execution ran after a failed token mint")
	}
	if len(svc.preparedSeeds) != 0 {
		t.Fatalf("failed mint left prepared growth in the map: %d", len(svc.preparedSeeds))
	}
	sessions, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("token mint failure left orphan phytomer: %+v", sessions)
	}
}

func TestGrowPreparedSeedCannotRaceOpeningPersistence(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	var ran atomic.Int32
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			ran.Add(1)
			return SeedGrowResult{Status: SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	})
	persistStarted := make(chan struct{})
	persistRelease := make(chan struct{})
	svc.WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error {
			persistStarted <- struct{}{}
			<-persistRelease
			return nil
		},
	}).WithContinuationPersistence(wiredContinuationPersistence())

	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	openErr := make(chan error, 1)
	go func() {
		_, err := svc.OpenPreparedSeed(context.Background(), growth, "seed-handle")
		openErr <- err
	}()
	select {
	case <-persistStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("OpenPreparedSeed never reached RecordOpening")
	}

	growWhileOpeningErr := make(chan error, 1)
	go func() {
		_, err := svc.GrowPreparedSeed(context.Background(), growth)
		growWhileOpeningErr <- err
	}()
	select {
	case err := <-growWhileOpeningErr:
		if err == nil {
			t.Fatal("GrowPreparedSeed executed while durable opening was incomplete")
		}
		if !errors.Is(err, ErrSeedGrowthInvalid) {
			t.Fatalf("grow-while-opening error = %v, want ErrSeedGrowthInvalid", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GrowPreparedSeed did not fail closed while opening was incomplete")
	}
	if ran.Load() != 0 {
		t.Fatal("SeedOperations.Run was invoked before OpenPreparedSeed completed")
	}

	close(persistRelease)
	if err := <-openErr; err != nil {
		t.Fatalf("OpenPreparedSeed: %v", err)
	}

	result, err := svc.GrowPreparedSeed(context.Background(), growth)
	if err != nil {
		t.Fatalf("grow after open: %v", err)
	}
	if result.Status != SeedStatusSatisfied || ran.Load() != 1 {
		t.Fatalf("after open: status=%q runs=%d, want satisfied/1", result.Status, ran.Load())
	}

	if _, err := svc.GrowPreparedSeed(context.Background(), growth); err == nil {
		t.Fatal("replay of a claimed growth was accepted")
	} else if !errors.Is(err, ErrSeedGrowthInvalid) {
		t.Fatalf("replay error = %v, want ErrSeedGrowthInvalid", err)
	}
	if ran.Load() != 1 {
		t.Fatalf("replay invoked Run: runs=%d", ran.Load())
	}
}

func TestGrowPreparedSeedRemovesClaimedGrowth(t *testing.T) {
	svc, _ := newSeedService(t)
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.GrowPreparedSeed(context.Background(), growth); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if _, err := svc.GrowPreparedSeed(context.Background(), growth); err == nil {
		t.Fatal("replay of a claimed growth was accepted")
	} else if !errors.Is(err, ErrSeedGrowthInvalid) {
		t.Fatalf("replay error = %v, want ErrSeedGrowthInvalid", err)
	}
}

func TestGrowPreparedSeedPreservesExecutionEvidenceOnFruitPublicationFailure(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	diagnostic := &SeedPublicationDiagnostic{
		FailureCategory: SeedFailureCategoryFruitPublication,
		ExecutionStatus: SeedStatusSatisfied,
		Phase:           "commit-mutation",
		Outcome:         "reconciliation-unavailable",
		RetrySafe:       false,
		Message:         "read-only GitHub reconciliation could not establish the target state",
		RequestID:       "req-safe-123",
	}
	var settled SeedSettlement
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			return SeedGrowResult{
				Status:                SeedStatusSatisfied,
				Iterations:            3,
				PhytomerID:            spec.PhytomerID,
				Branch:                "tendril/seed-fruit",
				Commit:                "fruit-oid",
				Diff:                  "the completed diff",
				Logs:                  "the completed logs",
				PublicationDiagnostic: diagnostic,
			}, errors.New("upstream-secret-content")
		},
	}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error { return nil },
		RecordSettlement: func(context.Context, SeedSettlement) error {
			t.Fatal("best-effort RecordSettlement used for opened Seed settlement")
			return nil
		},
	}).WithContinuationPersistence(captureOpenedSettlement(&settled))

	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(context.Background(), growth, "seed-publication-failure"); err != nil {
		t.Fatalf("open: %v", err)
	}
	result, err := svc.GrowPreparedSeed(context.Background(), growth)
	if err == nil {
		t.Fatal("publication failure was reported as success")
	}
	if result.Status != SeedStatusFruitPublicationFailed || result.Branch != "" || result.Commit != "" {
		t.Fatalf("returned publication failure result = %+v", result)
	}
	if result.Iterations != 3 || result.Diff != "the completed diff" || result.Logs != "the completed logs" {
		t.Fatalf("returned execution evidence = %+v", result)
	}
	if result.PublicationDiagnostic == nil || result.PublicationDiagnostic.Outcome != diagnostic.Outcome {
		t.Fatalf("returned diagnostic = %+v", result.PublicationDiagnostic)
	}
	if settled.Status != SeedStatusFruitPublicationFailed || settled.Branch != "" || settled.Commit != "" {
		t.Fatalf("settled publication failure = %+v", settled)
	}
	if settled.Iterations != 3 || settled.Diff != "the completed diff" || settled.Logs != "the completed logs" {
		t.Fatalf("settled execution evidence = %+v", settled)
	}
	if settled.PublicationDiagnostic == nil || settled.PublicationDiagnostic.RequestID != diagnostic.RequestID {
		t.Fatalf("settled diagnostic = %+v", settled.PublicationDiagnostic)
	}
	if settled.Error != diagnostic.Message || strings.Contains(settled.Error, "upstream-secret-content") {
		t.Fatalf("settled error = %q, want safe publication message", settled.Error)
	}
}

func TestGrowPreparedSeedPersistsVerificationDiagnostics(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	code := 2
	var settled SeedSettlement
	svc := NewService(manager).WithSeed(SeedOperations{
		Run: func(_ context.Context, spec SeedSpec, _ *SeedContinuationLifecycle) (SeedGrowResult, error) {
			return SeedGrowResult{
				Status:     SeedStatusExhausted,
				Iterations: 1,
				PhytomerID: spec.PhytomerID,
				VerificationDiagnostics: []SeedVerificationDiagnostic{{
					Iteration: 1,
					Outcome:   SeedVerificationOutcomeInfrastructureFailed,
					ExitCode:  &code,
					TimedOut:  false,
					Message:   "verify infrastructure could not execute",
				}},
			}, nil
		},
	}).WithSeedPersistence(SeedPersistence{
		RecordOpening: func(context.Context, SeedOpening) error { return nil },
		RecordSettlement: func(context.Context, SeedSettlement) error {
			t.Fatal("best-effort RecordSettlement used for opened Seed settlement")
			return nil
		},
	}).WithContinuationPersistence(captureOpenedSettlement(&settled))
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := svc.OpenPreparedSeed(context.Background(), growth, "seed-verify-diag"); err != nil {
		t.Fatalf("open: %v", err)
	}
	result, err := svc.GrowPreparedSeed(context.Background(), growth)
	if err != nil {
		t.Fatalf("grow: %v", err)
	}
	if len(result.VerificationDiagnostics) != 1 || result.VerificationDiagnostics[0].Outcome != SeedVerificationOutcomeInfrastructureFailed {
		t.Fatalf("result diagnostics = %+v", result.VerificationDiagnostics)
	}
	if len(settled.VerificationDiagnostics) != 1 || settled.VerificationDiagnostics[0].ExitCode == nil || *settled.VerificationDiagnostics[0].ExitCode != 2 {
		t.Fatalf("settled diagnostics = %+v", settled.VerificationDiagnostics)
	}
}
