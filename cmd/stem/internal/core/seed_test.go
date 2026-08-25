package core

import (
	"context"
	"strings"
	"sync"
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
		Run: func(_ context.Context, spec SeedSpec) (SeedGrowResult, error) {
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

func TestPrepareSeedThenGrowReusesTheSamePhytomer(t *testing.T) {
	svc, captured := newSeedService(t)
	growth, err := svc.PrepareSeed(context.Background(), validSeedInput())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if growth.PhytomerID == "" {
		t.Fatal("prepare minted no phytomer")
	}

	in := validSeedInput()
	in.PhytomerID = growth.PhytomerID
	result, err := svc.SeedGrow(context.Background(), in)
	if err != nil {
		t.Fatalf("grow prepared: %v", err)
	}
	if result.PhytomerID != growth.PhytomerID || captured.PhytomerID != growth.PhytomerID {
		t.Fatalf("prepared phytomer %q was not reused (result=%q spec=%q)", growth.PhytomerID, result.PhytomerID, captured.PhytomerID)
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
		if name == "seed.prepare" || name == "seed.watch" {
			t.Fatalf("governed registry includes %q", name)
		}
	}
	if IsDelegatedCapability("seed.watch") || IsDelegatedCapability("seed.prepare") {
		t.Fatal("a Seed observation/prepare command was added to the delegated set")
	}
}
