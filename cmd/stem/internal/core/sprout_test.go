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
		Provider:  "local",
		Model:     "llama3.2",
		Genotype:  "verifier",
		Substrate: "from-session",
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
	if got.Substrate != "/workspaces/core" {
		t.Fatalf("explicit substrate overwritten by session preference: %q", got.Substrate)
	}
	if result.Status != "matured" || result.Output != "done" || result.SessionID != sess.ID {
		t.Fatalf("result = %+v", result)
	}
}

func TestSproutRunUsesSessionSubstrateWhenInputOmitsIt(t *testing.T) {
	var got core.SproutSpec
	svc, manager := newSproutService(t, func(_ context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
		got = spec
		return core.SproutRunReport{Output: "done", Outcome: "complete"}, nil
	})

	sess, err := manager.Initiate(context.Background(), session.OriginREST, session.Preferences{
		Substrate: "opentendril",
	})
	if err != nil {
		t.Fatalf("sprout session: %v", err)
	}

	if _, err := svc.SproutRun(context.Background(), core.SproutRunInput{
		Transcript: "fix the flaky test",
		SessionID:  sess.ID,
		Origin:     session.OriginREST,
	}); err != nil {
		t.Fatalf("SproutRun: %v", err)
	}
	if got.Substrate != "opentendril" {
		t.Fatalf("spec substrate = %q, want session preference opentendril", got.Substrate)
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
		t.Fatal("expected a fresh session to be initiated for an unbound run")
	}
	if _, ok := manager.Get(context.Background(), got.SessionID); !ok {
		t.Fatalf("initiated session %q not registered in the manager", got.SessionID)
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
	if !declared[core.CapSproutGrow] {
		t.Errorf("registry does not declare %s", core.CapSproutGrow)
	}

	// Invoke path (the projection MCP/CLI use) enforces required fields and
	// returns the typed result.
	if _, err := svc.Invoke(context.Background(), core.CapSproutGrow, map[string]any{"transcript": "t"}); err == nil {
		t.Fatal("Invoke(sprout.grow) without substrate must fail")
	}
	result, err := svc.Invoke(context.Background(), core.CapSproutGrow, map[string]any{"transcript": "t", "substrate": "s"})
	if err != nil {
		t.Fatalf("Invoke(sprout.grow): %v", err)
	}
	if _, ok := result.(core.SproutRunResult); !ok {
		t.Fatalf("Invoke(sprout.grow) = %T, want core.SproutRunResult", result)
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

// The resolved provider and model reach the result on both endings. They are
// reported by the execution port rather than echoed from the request: a run
// that requested neither is exactly the run whose model nothing else could
// name, and that is the run the measurement is made of.
func TestSproutRunReportsTheResolvedProviderAndModel(t *testing.T) {
	t.Run("matured", func(t *testing.T) {
		svc, _ := newSproutService(t, func(_ context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			if spec.Model != "" || spec.Provider != "" {
				t.Fatalf("spec carried provider %q model %q, want neither", spec.Provider, spec.Model)
			}
			return core.SproutRunReport{
				Output: "ok", Outcome: "complete",
				Provider: "google", Model: "gemini-3.1-pro",
			}, nil
		})

		result, err := svc.SproutRun(context.Background(), core.SproutRunInput{Transcript: "t", Substrate: "s"})
		if err != nil {
			t.Fatalf("SproutRun failed: %v", err)
		}
		if result.Provider != "google" || result.Model != "gemini-3.1-pro" {
			t.Fatalf("result = %s/%s, want google/gemini-3.1-pro", result.Provider, result.Model)
		}
	})

	t.Run("withered", func(t *testing.T) {
		svc, _ := newSproutService(t, func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
			return core.SproutRunReport{Provider: "anthropic", Model: "claude-opus-4-8"}, fmt.Errorf("terrarium exploded")
		})

		result, err := svc.SproutRun(context.Background(), core.SproutRunInput{Transcript: "t", Substrate: "s"})
		if err == nil {
			t.Fatal("expected the execution error to propagate")
		}
		if result.Provider != "anthropic" || result.Model != "claude-opus-4-8" {
			t.Fatalf("result = %s/%s, want anthropic/claude-opus-4-8 on a failed run", result.Provider, result.Model)
		}
	})
}

func TestSproutRunClassifiesProviderAuthFromTypedDiagnostic(t *testing.T) {
	svc, _ := newSproutService(t, func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
		return core.SproutRunReport{
			Outcome:                  "failed",
			Provider:                 "openrouter",
			Model:                    "anthropic/claude-sonnet-4.6",
			ProviderRequestAttempted: true,
			ToolInvocations:          0,
			ProviderDiagnostic: &core.ProviderDiagnostic{
				StatusCode: 401,
				Message:    "User not found",
				Provider:   "openrouter",
			},
		}, fmt.Errorf("llm returned 401: User not found")
	})

	result, err := svc.SproutRun(context.Background(), core.SproutRunInput{Transcript: "t", Substrate: "s"})
	if err == nil {
		t.Fatal("expected the execution error to propagate")
	}
	if result.FailureCategory != string(core.FailureCategoryProviderAuthRejected) {
		t.Fatalf("FailureCategory = %q, want %q", result.FailureCategory, core.FailureCategoryProviderAuthRejected)
	}
	if result.ProviderDiagnostic == nil || result.ProviderDiagnostic.StatusCode != 401 || result.ProviderDiagnostic.Message != "User not found" {
		t.Fatalf("ProviderDiagnostic = %+v", result.ProviderDiagnostic)
	}
	if !result.ProviderRequestAttempted {
		t.Fatal("ProviderRequestAttempted = false, want true")
	}
	if result.ToolInvocations != 0 {
		t.Fatalf("ToolInvocations = %d, want 0", result.ToolInvocations)
	}
}
