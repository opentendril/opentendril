package core_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func newSproutService(t *testing.T, run func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error)) (*core.Service, *session.Manager) {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return core.NewService(manager).WithSprout(core.SproutOperations{Run: run}), manager
}

func TestSproutRunRequiresTranscriptAndSubstrate(t *testing.T) {
	svc, _ := newSproutService(t, func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
		return core.SproutRunReport{}, nil
	})
	for _, in := range []core.SproutRunInput{
		{},
		{Transcript: "fix the bug"},
		{Substrate: "/workspaces/core"},
	} {
		if _, err := svc.SproutRun(context.Background(), in); err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("input %+v: expected required-fields error, got %v", in, err)
		}
	}
}

func TestSproutRunUnwiredFailsLoudly(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := core.NewService(manager)
	if _, err := svc.SproutRun(context.Background(), core.SproutRunInput{Transcript: "t", Substrate: "s"}); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error, got %v", err)
	}
}

func TestSproutRunMintsStepIDAndBindsSession(t *testing.T) {
	var got core.SproutSpec
	svc, manager := newSproutService(t, func(_ context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
		got = spec
		return core.SproutRunReport{Output: "done", Outcome: "complete"}, nil
	})

	// A pre-existing session's preferences must shape the sprout.
	sess, err := manager.Initiate(context.Background(), session.OriginCLI, session.Preferences{
		Provider: "local",
		Model:    "llama3.2",
		Genotype: "verifier",
	})
	if err != nil {
		t.Fatalf("sprout session: %v", err)
	}

	result, err := svc.SproutRun(context.Background(), core.SproutRunInput{
		Transcript: "fix the flaky test",
		Substrate:  "/workspaces/core",
		SessionID:  sess.ID,
		Origin:     session.OriginCLI,
	})
	if err != nil {
		t.Fatalf("SproutRun: %v", err)
	}

	if got.StepID == "" || !strings.HasPrefix(got.StepID, "step-") {
		t.Fatalf("minted step id = %q", got.StepID)
	}
	if got.SessionID != sess.ID {
		t.Fatalf("spec session = %q, want %q", got.SessionID, sess.ID)
	}
	if got.Provider != "local" || got.Model != "llama3.2" || got.Genotype != "verifier" {
		t.Fatalf("session preferences not applied to spec: %+v", got)
	}
	if result.Status != "matured" || result.Output != "done" || result.SessionID != sess.ID {
		t.Fatalf("result = %+v", result)
	}
}

func TestSproutRunKeepsExplicitStepID(t *testing.T) {
	var got core.SproutSpec
	svc, _ := newSproutService(t, func(_ context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
		got = spec
		return core.SproutRunReport{}, nil
	})
	if _, err := svc.SproutRun(context.Background(), core.SproutRunInput{
		Transcript: "t",
		Substrate:  "s",
		StepID:     "step-custom",
	}); err != nil {
		t.Fatalf("SproutRun: %v", err)
	}
	if got.StepID != "step-custom" {
		t.Fatalf("step id = %q, want step-custom", got.StepID)
	}
}

func TestSproutRunEmptySessionSproutsFresh(t *testing.T) {
	var got core.SproutSpec
	svc, manager := newSproutService(t, func(_ context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
		got = spec
		return core.SproutRunReport{}, nil
	})
	if _, err := svc.SproutRun(context.Background(), core.SproutRunInput{
		Transcript: "t",
		Substrate:  "s",
		Origin:     session.OriginREST,
	}); err != nil {
		t.Fatalf("SproutRun: %v", err)
	}
	if got.SessionID == "" {
		t.Fatal("expected a fresh session to be sprouted for an unbound run")
	}
	if _, ok := manager.Get(got.SessionID); !ok {
		t.Fatalf("sprouted session %q not registered in the manager", got.SessionID)
	}
}

func TestSproutRunWitheredOnError(t *testing.T) {
	svc, _ := newSproutService(t, func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
		return core.SproutRunReport{}, fmt.Errorf("terrarium exploded")
	})
	result, err := svc.SproutRun(context.Background(), core.SproutRunInput{Transcript: "t", Substrate: "s"})
	if err == nil || !strings.Contains(err.Error(), "terrarium exploded") {
		t.Fatalf("expected execution error to propagate, got %v", err)
	}
	if result.Status != "withered" {
		t.Fatalf("status = %q, want withered", result.Status)
	}
	if result.StepID == "" {
		t.Fatal("failed runs must still report their step id")
	}
}

func TestSproutCapabilityInRegistry(t *testing.T) {
	svc, _ := newSproutService(t, func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
		return core.SproutRunReport{Output: "ok", Outcome: "complete"}, nil
	})

	declared := map[string]bool{}
	for _, capability := range svc.Capabilities() {
		declared[capability.Name] = true
	}
	if !declared[core.CapSproutRun] {
		t.Errorf("registry does not declare %s", core.CapSproutRun)
	}

	// Invoke path (the projection MCP/CLI use) enforces required fields and
	// returns the typed result.
	if _, err := svc.Invoke(context.Background(), core.CapSproutRun, map[string]any{"transcript": "t"}); err == nil {
		t.Fatal("Invoke(sprout.run) without substrate must fail")
	}
	result, err := svc.Invoke(context.Background(), core.CapSproutRun, map[string]any{"transcript": "t", "substrate": "s"})
	if err != nil {
		t.Fatalf("Invoke(sprout.run): %v", err)
	}
	if _, ok := result.(core.SproutRunResult); !ok {
		t.Fatalf("Invoke(sprout.run) = %T, want core.SproutRunResult", result)
	}
}

// TestSproutRunCarriesExecutionOutcome proves the Core relays the execution
// port's honest verdict — outcome and file evidence — instead of flattening
// every finished run into "matured".
func TestSproutRunCarriesExecutionOutcome(t *testing.T) {
	svc, _ := newSproutService(t, func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
		return core.SproutRunReport{Output: "report only", Outcome: "no-changes", FilesModified: []string{}}, nil
	})

	result, err := svc.SproutRun(context.Background(), core.SproutRunInput{
		Transcript: "investigate and report",
		Substrate:  "/workspaces/core",
	})
	if err != nil {
		t.Fatalf("SproutRun failed: %v", err)
	}
	if result.Status != "matured" {
		t.Fatalf("Status = %q, want matured (lifecycle verdict is unchanged)", result.Status)
	}
	if result.Outcome != "no-changes" {
		t.Fatalf("Outcome = %q, want no-changes", result.Outcome)
	}
	if result.Output != "report only" {
		t.Fatalf("Output = %q, want report only", result.Output)
	}
}
