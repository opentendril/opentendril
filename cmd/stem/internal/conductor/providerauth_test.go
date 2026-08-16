package conductor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

func TestRunSproutProviderAuthPreflightRejectsBeforeEmerge(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	terrariumStarts := 0
	captured := stubRunSproutCollaborators(t, root, &stubSproutRunner{result: sproutResult{Response: "should not run"}}, []string{"pkg/thing.go"})
	originalStart := startTerrariumSessionFn
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		terrariumStarts++
		return originalStart(ctx, providerName, imageName, mountPath, readOnly, command, extraEnv, timeout, observers...)
	}

	secretBody := `{"error":{"message":"User not found Bearer sk-super-secret-value"}}`
	probeProviderAuthFn = func(context.Context, *llm.Client) error {
		return llm.NewRequestError(401, secretBody, llm.ProviderSpec{
			Provider: "openrouter",
			Model:    "anthropic/claude-sonnet-4.6",
		})
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)
	orch := &DockerOrchestrator{
		Substrate: root,
		StepID:    "step-auth-preflight",
		SessionID: "session-auth-preflight",
		EventBus:  bus,
	}

	report, err := orch.RunSprout(context.Background(), "task under test")
	if err == nil {
		t.Fatal("RunSprout error = nil, want provider auth rejection")
	}
	var reqErr *llm.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != 401 {
		t.Fatalf("RunSprout error = %v, want typed HTTP 401", err)
	}
	if terrariumStarts != 0 {
		t.Fatalf("Terrarium starts = %d, want 0", terrariumStarts)
	}
	if emerged := filterEvents(*events, eventbus.EventSproutEmerged); len(emerged) != 0 {
		t.Fatalf("published %d sprout-emerged events, want 0: %+v", len(emerged), emerged)
	}
	if report.FailureCategory != string(core.FailureCategoryProviderAuthRejected) {
		t.Fatalf("FailureCategory = %q, want %q", report.FailureCategory, core.FailureCategoryProviderAuthRejected)
	}
	if !report.RequestsMade {
		t.Fatal("RequestsMade = false, want true for the preflight attempt")
	}
	if report.ToolInvocations != 0 {
		t.Fatalf("ToolInvocations = %d, want 0", report.ToolInvocations)
	}
	if captured.Status != "" {
		t.Fatalf("commit status = %q, want empty (no Terrarium execution)", captured.Status)
	}

	withered := filterEvents(*events, eventbus.EventSproutWithered)
	if len(withered) != 1 {
		t.Fatalf("published %d sprout-withered events, want 1: %+v", len(withered), withered)
	}
	if got := withered[0].Data["failureCategory"]; got != string(core.FailureCategoryProviderAuthRejected) {
		t.Fatalf("terminal failureCategory = %v, want %q", got, core.FailureCategoryProviderAuthRejected)
	}
	if got := withered[0].Data["providerRequestAttempted"]; got != true {
		t.Fatalf("terminal providerRequestAttempted = %v, want true", got)
	}
	if got := withered[0].Data["toolInvocations"]; got != 0 {
		t.Fatalf("terminal toolInvocations = %v, want 0", got)
	}
}

func TestRunSproutProviderAuthPreflightSuccessStillEmerges(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	terrariumStarts := 0
	stubRunSproutCollaborators(t, root, &stubSproutRunner{result: sproutResult{Response: "did the work", WroteWorkspace: true}}, []string{"pkg/thing.go"})
	originalStart := startTerrariumSessionFn
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		terrariumStarts++
		return originalStart(ctx, providerName, imageName, mountPath, readOnly, command, extraEnv, timeout, observers...)
	}

	probed := 0
	probeProviderAuthFn = func(context.Context, *llm.Client) error {
		probed++
		return nil
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)
	orch := &DockerOrchestrator{
		Substrate: root,
		StepID:    "step-auth-ok",
		SessionID: "session-auth-ok",
		EventBus:  bus,
	}

	report, err := orch.RunSprout(context.Background(), "task under test")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}
	if probed != 1 {
		t.Fatalf("provider auth preflight calls = %d, want 1", probed)
	}
	if terrariumStarts != 1 {
		t.Fatalf("Terrarium starts = %d, want 1", terrariumStarts)
	}
	if emerged := filterEvents(*events, eventbus.EventSproutEmerged); len(emerged) != 1 {
		t.Fatalf("published %d sprout-emerged events, want 1", len(emerged))
	}
	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}
}

func TestRunSproutProviderAuthPreflightDiagnosticOmitsSecrets(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)
	stubRunSproutCollaborators(t, root, &stubSproutRunner{result: sproutResult{Response: "should not run"}}, nil)

	secret := "sk-super-secret-value-that-must-not-leak"
	probeProviderAuthFn = func(context.Context, *llm.Client) error {
		return llm.NewRequestError(401, `denied Bearer `+secret, llm.ProviderSpec{Provider: "openrouter"})
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)
	report, err := (&DockerOrchestrator{
		Substrate: root,
		StepID:    "step-auth-secret",
		EventBus:  bus,
	}).RunSprout(context.Background(), "task under test")
	if err == nil {
		t.Fatal("RunSprout error = nil, want provider auth rejection")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(strings.ToLower(err.Error()), "bearer sk-") {
		t.Fatalf("error leaked a secret: %q", err.Error())
	}
	if report.ProviderDiagnostic == nil {
		t.Fatal("ProviderDiagnostic = nil")
	}
	if strings.Contains(report.ProviderDiagnostic.Message, secret) {
		t.Fatalf("ProviderDiagnostic leaked a secret: %+v", report.ProviderDiagnostic)
	}
	if !strings.Contains(report.ProviderDiagnostic.Message, "[REDACTED]") {
		t.Fatalf("ProviderDiagnostic.Message = %q, want a redacted marker", report.ProviderDiagnostic.Message)
	}

	withered := filterEvents(*events, eventbus.EventSproutWithered)
	if len(withered) != 1 {
		t.Fatalf("published %d sprout-withered events, want 1", len(withered))
	}
	diagnostic, _ := withered[0].Data["providerDiagnostic"].(map[string]interface{})
	if diagnostic == nil {
		t.Fatalf("terminal providerDiagnostic = %v", withered[0].Data["providerDiagnostic"])
	}
	message, _ := diagnostic["message"].(string)
	if strings.Contains(message, secret) {
		t.Fatalf("terminal diagnostic leaked a secret: %q", message)
	}
}

func TestApplyProviderAuthPreflightIgnoresNonAuthProbeErrors(t *testing.T) {
	report := SproutRunReport{}
	probeProviderAuthFn = func(context.Context, *llm.Client) error {
		return llm.NewRequestError(500, "upstream unavailable", llm.ProviderSpec{Provider: "openrouter"})
	}
	t.Cleanup(func() {
		probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	})

	if err := applyProviderAuthPreflight(context.Background(), &llm.Client{}, &report); err != nil {
		t.Fatalf("applyProviderAuthPreflight() = %v, want nil so a non-auth probe error is not a new gate", err)
	}
	if report.RequestsMade {
		t.Fatal("RequestsMade = true for a non-auth probe error that does not stop the run")
	}
}

func TestProbeProviderAuthenticationSkipsLocal(t *testing.T) {
	client := llm.NewClient(llm.ProviderSpec{
		Provider: "local",
		BaseURL:  "http://127.0.0.1:1/v1",
		Model:    "test-only-model",
	})
	// A local mind must not issue a chat request merely to learn there is
	// no credential to reject. Reachability stays the existing preflight.
	if err := probeProviderAuthentication(context.Background(), client); err != nil {
		t.Fatalf("probeProviderAuthentication() = %v, want nil for local", err)
	}
}
