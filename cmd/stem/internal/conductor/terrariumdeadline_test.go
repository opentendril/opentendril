package conductor

import (
	"context"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// TestDeriveWatchdogTimeout proves the three structural properties of
// deriveWatchdogTimeout:
//
//  1. With a deadline: the result strictly exceeds the remaining time (so the
//     context expires before the watchdog fires) and the excess equals the
//     grace margin (so removing or inverting the margin changes the outcome).
//  2. Without a deadline: the result is exactly terrariumWatchdogFallback.
//
// Property 1 is time-independent: "remaining + margin > remaining" holds
// regardless of when the call is made.
func TestDeriveWatchdogTimeout(t *testing.T) {
	t.Run("context with deadline exceeds remaining and carries margin", func(t *testing.T) {
		remaining := 5 * time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		defer cancel()

		deadline, _ := ctx.Deadline()

		// Record remaining time on both sides of the call so the inequality
		// is time-independent: before ≥ remaining-at-call ≥ after.
		before := time.Until(deadline)

		got := deriveWatchdogTimeout(ctx)

		after := time.Until(deadline)

		// The returned value must strictly exceed the remaining time at the
		// moment of the call: the context expires first, the watchdog is a
		// backstop. Bounding both sides with before/after makes the check
		// independent of how long the call itself takes.
		if got <= before {
			t.Fatalf("deriveWatchdogTimeout = %s, want > remaining (%s) at call time", got, before)
		}

		// The margin must be approximately one minute. Using "after" as the
		// reference is conservative: "after ≤ remaining at call time ≤ before".
		margin := got - after
		if margin < time.Minute || margin > time.Minute+5*time.Second {
			t.Fatalf("deriveWatchdogTimeout margin = %s (got=%s, after=%s), want ~1m", margin, got, after)
		}
	})

	t.Run("context without deadline yields fallback exactly", func(t *testing.T) {
		got := deriveWatchdogTimeout(context.Background())
		if got != terrariumWatchdogFallback {
			t.Fatalf("deriveWatchdogTimeout without deadline = %s, want fallback %s", got, terrariumWatchdogFallback)
		}
	})
}

// TestRunSproutCallSitePassesDerivedWatchdog covers the RunSprout call site
// (docker.go). The assertions are structural, not value-equality:
//
//   - with a deadline: capturedTimeout strictly exceeds the context's remaining
//     time at the moment startTerrariumSessionFn is called, and it is not the
//     fallback constant (so a reverted call site still fails);
//   - without a deadline: capturedTimeout equals terrariumWatchdogFallback exactly.
//
// Both assertions are time-independent: no setup work can invalidate them.
func TestRunSproutCallSitePassesDerivedWatchdog(t *testing.T) {
	t.Run("context with deadline: watchdog exceeds remaining", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		chdirToTempDir(t)

		// capturedTimeout and capturedRemaining are set atomically inside the
		// seam so the inequality is measured at the moment of the call, not
		// before setup begins.
		var capturedTimeout time.Duration
		var capturedRemaining time.Duration

		stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "done"}, []string{"pkg/thing.go"})

		originalStartSession := startTerrariumSessionFn
		t.Cleanup(func() { startTerrariumSessionFn = originalStartSession })

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		deadline, _ := ctx.Deadline()

		startTerrariumSessionFn = func(innerCtx context.Context, providerName, imageName, mountPath string, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
			capturedRemaining = time.Until(deadline)
			capturedTimeout = timeout
			return &stubToolSession{}, nil
		}

		orch := &DockerOrchestrator{
			Substrate:        root,
			StepID:           "watchdog-runspr-deadline",
			DisableMergeBack: true,
		}
		orch.RunSprout(ctx, "watchdog probe") //nolint:errcheck

		if capturedTimeout == 0 {
			t.Fatal("startTerrariumSessionFn was never called; cannot verify timeout")
		}
		// The watchdog must exceed the context's remaining time so the context
		// expires first.
		if capturedTimeout <= capturedRemaining {
			t.Fatalf("RunSprout call site: capturedTimeout %s <= remaining %s; context would not expire first", capturedTimeout, capturedRemaining)
		}
		// A reverted call site passing terrariumWatchdogFallback directly also
		// satisfies the inequality when the context has 3+ minutes, so reject
		// the fallback value explicitly.
		if capturedTimeout == terrariumWatchdogFallback {
			t.Fatalf("RunSprout call site: capturedTimeout == terrariumWatchdogFallback (%s); derivation not applied", capturedTimeout)
		}
	})

	t.Run("context without deadline: watchdog is fallback", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		chdirToTempDir(t)

		var capturedTimeout time.Duration

		stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "done"}, []string{"pkg/thing.go"})

		originalStartSession := startTerrariumSessionFn
		t.Cleanup(func() { startTerrariumSessionFn = originalStartSession })
		startTerrariumSessionFn = func(_ context.Context, providerName, imageName, mountPath string, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
			capturedTimeout = timeout
			return &stubToolSession{}, nil
		}

		orch := &DockerOrchestrator{
			Substrate:        root,
			StepID:           "watchdog-runspr-nodeadline",
			DisableMergeBack: true,
		}
		orch.RunSprout(context.Background(), "watchdog probe") //nolint:errcheck

		if capturedTimeout == 0 {
			t.Fatal("startTerrariumSessionFn was never called; cannot verify timeout")
		}
		if capturedTimeout != terrariumWatchdogFallback {
			t.Fatalf("RunSprout call site without deadline: capturedTimeout = %s, want fallback %s", capturedTimeout, terrariumWatchdogFallback)
		}
	})
}

// TestRunSequenceSproutAtPathCallSitePassesDerivedWatchdog covers the
// runSequenceSproutAtPath call site (sequence.go) — the load-bearing one
// reached from the parallel sprouting path. The same structural assertions
// apply: time-independent inequalities, not proximity to a decaying value.
func TestRunSequenceSproutAtPathCallSitePassesDerivedWatchdog(t *testing.T) {
	t.Run("context with deadline: watchdog exceeds remaining", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		chdirToTempDir(t)

		var capturedTimeout time.Duration
		var capturedRemaining time.Duration

		originalEnsure := ensureSproutImageFn
		originalCreateShadow := createShadowWorktreeFn
		originalRemoveShadow := removeShadowWorktreeFn
		originalInjectCache := injectMycorrhizalCacheFn
		originalStartSession := startTerrariumSessionFn
		originalNewSprout := newSproutFn
		originalStash := stashHostWorkspaceFn
		t.Cleanup(func() {
			ensureSproutImageFn = originalEnsure
			createShadowWorktreeFn = originalCreateShadow
			removeShadowWorktreeFn = originalRemoveShadow
			injectMycorrhizalCacheFn = originalInjectCache
			startTerrariumSessionFn = originalStartSession
			newSproutFn = originalNewSprout
			stashHostWorkspaceFn = originalStash
		})

		ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
		createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) { return root, nil }
		removeShadowWorktreeFn = func(sourcePath, shadowPath string) {}
		injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
		stashHostWorkspaceFn = func(ctx context.Context, repoRoot, runID string) (bool, error) { return false, nil }
		newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
			return &mockSproutRunner{response: "done"}, nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		deadline, _ := ctx.Deadline()

		startTerrariumSessionFn = func(innerCtx context.Context, providerName, imageName, mountPath string, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
			capturedRemaining = time.Until(deadline)
			capturedTimeout = timeout
			return &stubToolSession{}, nil
		}

		orch := &DockerOrchestrator{
			Substrate:        root,
			StepID:           "watchdog-seqpath-deadline",
			DisableMergeBack: true,
		}
		runSequenceSproutAtPath(ctx, orch, "watchdog probe", root, root) //nolint:errcheck

		if capturedTimeout == 0 {
			t.Fatal("startTerrariumSessionFn was never called; cannot verify timeout")
		}
		if capturedTimeout <= capturedRemaining {
			t.Fatalf("runSequenceSproutAtPath call site: capturedTimeout %s <= remaining %s; context would not expire first", capturedTimeout, capturedRemaining)
		}
		if capturedTimeout == terrariumWatchdogFallback {
			t.Fatalf("runSequenceSproutAtPath call site: capturedTimeout == terrariumWatchdogFallback (%s); derivation not applied", capturedTimeout)
		}
	})

	t.Run("context without deadline: watchdog is fallback", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		chdirToTempDir(t)

		var capturedTimeout time.Duration

		originalEnsure := ensureSproutImageFn
		originalCreateShadow := createShadowWorktreeFn
		originalRemoveShadow := removeShadowWorktreeFn
		originalInjectCache := injectMycorrhizalCacheFn
		originalStartSession := startTerrariumSessionFn
		originalNewSprout := newSproutFn
		originalStash := stashHostWorkspaceFn
		t.Cleanup(func() {
			ensureSproutImageFn = originalEnsure
			createShadowWorktreeFn = originalCreateShadow
			removeShadowWorktreeFn = originalRemoveShadow
			injectMycorrhizalCacheFn = originalInjectCache
			startTerrariumSessionFn = originalStartSession
			newSproutFn = originalNewSprout
			stashHostWorkspaceFn = originalStash
		})

		ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
		createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) { return root, nil }
		removeShadowWorktreeFn = func(sourcePath, shadowPath string) {}
		injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
		stashHostWorkspaceFn = func(ctx context.Context, repoRoot, runID string) (bool, error) { return false, nil }
		startTerrariumSessionFn = func(_ context.Context, providerName, imageName, mountPath string, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
			capturedTimeout = timeout
			return &stubToolSession{}, nil
		}
		newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
			return &mockSproutRunner{response: "done"}, nil
		}

		orch := &DockerOrchestrator{
			Substrate:        root,
			StepID:           "watchdog-seqpath-nodeadline",
			DisableMergeBack: true,
		}
		runSequenceSproutAtPath(context.Background(), orch, "watchdog probe", root, root) //nolint:errcheck

		if capturedTimeout == 0 {
			t.Fatal("startTerrariumSessionFn was never called; cannot verify timeout")
		}
		if capturedTimeout != terrariumWatchdogFallback {
			t.Fatalf("runSequenceSproutAtPath call site without deadline: capturedTimeout = %s, want fallback %s", capturedTimeout, terrariumWatchdogFallback)
		}
	})
}
