package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newSeedService wires a Service with a stubbed seed port and returns the
// captured spec of the last grow.
func newSeedService(t *testing.T) (*Service, *SeedSpec) {
	t.Helper()
	captured := &SeedSpec{}
	svc := NewService(nil).WithSeed(SeedOperations{
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
