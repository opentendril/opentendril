package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newPassthroughService wires a Service with a stubbed passthrough port and
// returns the captured spec of the last run.
func newPassthroughService(t *testing.T) (*Service, *PassthroughSpec) {
	t.Helper()
	captured := &PassthroughSpec{}
	svc := NewService(nil).WithPassthrough(PassthroughOperations{
		Run: func(_ context.Context, spec PassthroughSpec) (PassthroughRunResult, error) {
			*captured = spec
			return PassthroughRunResult{Status: "completed", ExitCode: 0}, nil
		},
	})
	return svc, captured
}

func TestPassthroughRunValidatesInput(t *testing.T) {
	svc, _ := newPassthroughService(t)
	ctx := context.Background()

	if _, err := svc.PassthroughRun(ctx, PassthroughRunInput{Command: []string{"true"}}); err == nil {
		t.Fatal("missing substrate accepted")
	}
	if _, err := svc.PassthroughRun(ctx, PassthroughRunInput{Substrate: "core"}); err == nil {
		t.Fatal("missing command accepted")
	}
	if _, err := svc.PassthroughRun(ctx, PassthroughRunInput{Substrate: "core", Command: []string{"  "}}); err == nil {
		t.Fatal("blank command token accepted")
	}
	if _, err := svc.PassthroughRun(ctx, PassthroughRunInput{Substrate: "core", Command: []string{"true"}, TimeoutSeconds: -1}); err == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestPassthroughRunNotWired(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.PassthroughRun(context.Background(), PassthroughRunInput{Substrate: "core", Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired passthrough error = %v, want a not-wired report", err)
	}
}

func TestPassthroughRunTimeoutBounds(t *testing.T) {
	svc, captured := newPassthroughService(t)
	ctx := context.Background()

	if _, err := svc.PassthroughRun(ctx, PassthroughRunInput{Substrate: "core", Command: []string{"true"}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Timeout != passthroughDefaultTimeout {
		t.Fatalf("default timeout = %v, want %v", captured.Timeout, passthroughDefaultTimeout)
	}

	if _, err := svc.PassthroughRun(ctx, PassthroughRunInput{Substrate: "core", Command: []string{"true"}, TimeoutSeconds: 10}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Timeout != 10*time.Second {
		t.Fatalf("explicit timeout = %v, want 10s", captured.Timeout)
	}

	if _, err := svc.PassthroughRun(ctx, PassthroughRunInput{Substrate: "core", Command: []string{"true"}, TimeoutSeconds: 7200}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Timeout != passthroughMaximumTimeout {
		t.Fatalf("excess timeout = %v, want the %v cap", captured.Timeout, passthroughMaximumTimeout)
	}
}

// TestPassthroughEgressNotDecodableFromInput is the no-self-escalation
// guarantee at the capability boundary: an input map that smuggles an
// "egress" key (as any transport caller would have to) never reaches the
// execution port — the allow-list is set only programmatically by the Stem's
// own call sites from an authorized delegation grant.
func TestPassthroughEgressNotDecodableFromInput(t *testing.T) {
	svc, captured := newPassthroughService(t)

	_, err := svc.Invoke(context.Background(), CapPassthroughRun, map[string]any{
		"substrate": "core",
		"command":   []any{"true"},
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

// TestPassthroughEgressThreadedFromGrantMaterial covers the legitimate path:
// a Stem call site sets Egress programmatically and the spec carries it.
func TestPassthroughEgressThreadedFromGrantMaterial(t *testing.T) {
	svc, captured := newPassthroughService(t)

	_, err := svc.PassthroughRun(context.Background(), PassthroughRunInput{
		Substrate: "core",
		Command:   []string{"true"},
		Egress:    []string{"proxy.golang.org"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(captured.Egress) != 1 || captured.Egress[0] != "proxy.golang.org" {
		t.Fatalf("spec egress = %v, want the grant-supplied allow-list", captured.Egress)
	}
}
