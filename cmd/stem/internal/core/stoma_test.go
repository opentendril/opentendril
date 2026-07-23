package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newStomaService wires a Service with a stubbed stoma port and
// returns the captured spec of the last run.
func newStomaService(t *testing.T) (*Service, *StomaSpec) {
	t.Helper()
	captured := &StomaSpec{}
	svc := NewService(nil).WithStoma(StomaOperations{
		Run: func(_ context.Context, spec StomaSpec) (StomaPassResult, error) {
			*captured = spec
			return StomaPassResult{Status: "completed", ExitCode: 0}, nil
		},
	})
	return svc, captured
}

func TestStomaPassValidatesInput(t *testing.T) {
	svc, _ := newStomaService(t)
	ctx := context.Background()

	if _, err := svc.StomaPass(ctx, StomaPassInput{Command: []string{"true"}}); err == nil {
		t.Fatal("missing substrate accepted")
	}
	if _, err := svc.StomaPass(ctx, StomaPassInput{Substrate: "core"}); err == nil {
		t.Fatal("missing command accepted")
	}
	if _, err := svc.StomaPass(ctx, StomaPassInput{Substrate: "core", Command: []string{"  "}}); err == nil {
		t.Fatal("blank command token accepted")
	}
	if _, err := svc.StomaPass(ctx, StomaPassInput{Substrate: "core", Command: []string{"true"}, TimeoutSeconds: -1}); err == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestStomaPassNotWired(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.StomaPass(context.Background(), StomaPassInput{Substrate: "core", Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired stoma error = %v, want a not-wired report", err)
	}
}

func TestStomaPassTimeoutBounds(t *testing.T) {
	svc, captured := newStomaService(t)
	ctx := context.Background()

	if _, err := svc.StomaPass(ctx, StomaPassInput{Substrate: "core", Command: []string{"true"}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Timeout != stomaDefaultTimeout {
		t.Fatalf("default timeout = %v, want %v", captured.Timeout, stomaDefaultTimeout)
	}

	if _, err := svc.StomaPass(ctx, StomaPassInput{Substrate: "core", Command: []string{"true"}, TimeoutSeconds: 10}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Timeout != 10*time.Second {
		t.Fatalf("explicit timeout = %v, want 10s", captured.Timeout)
	}

	if _, err := svc.StomaPass(ctx, StomaPassInput{Substrate: "core", Command: []string{"true"}, TimeoutSeconds: 7200}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Timeout != stomaMaximumTimeout {
		t.Fatalf("excess timeout = %v, want the %v cap", captured.Timeout, stomaMaximumTimeout)
	}
}

// TestStomaEgressNotDecodableFromInput is the no-self-escalation
// guarantee at the capability boundary: an input map that smuggles an
// "egress" key (as any transport caller would have to) never reaches the
// execution port — the allow-list is set only programmatically by the Stem's
// own call sites from an authorized delegation grant.
func TestStomaEgressNotDecodableFromInput(t *testing.T) {
	svc, captured := newStomaService(t)

	_, err := svc.Invoke(context.Background(), CapStomaPass, map[string]any{
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

// TestStomaEgressThreadedFromGrantMaterial covers the legitimate path:
// a Stem call site sets Egress programmatically and the spec carries it.
func TestStomaEgressThreadedFromGrantMaterial(t *testing.T) {
	svc, captured := newStomaService(t)

	_, err := svc.StomaPass(context.Background(), StomaPassInput{
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
