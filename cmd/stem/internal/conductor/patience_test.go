package conductor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// configuredPatienceBudget is the value the call-site tests below configure. It
// is a literal the tests own, never a value read back from the code under test,
// so an implementation that resolved the wrong duration cannot agree with it.
const configuredPatienceBudget = 2 * time.Minute

// writePatienceSubstrate writes a substrates.yaml in the working directory
// naming one substrate that points at workspace, with the given patience block
// spliced in verbatim (empty for no patience at all).
func writePatienceSubstrate(t *testing.T, cwd, name, workspace, patienceBlock string) {
	t.Helper()

	writeSubstratesYAML(t, filepath.Join(cwd, "substrates.yaml"), fmt.Sprintf(`
substrates:
  %s:
    path: %s
%s`, name, workspace, patienceBlock))
}

// TestPatienceGrowthLoading pins the schema half: a well-formed value parses to
// exactly the duration written, an absent one is zero without error, and a value
// that cannot be honoured fails the load by name rather than defaulting quietly.
func TestPatienceGrowthLoading(t *testing.T) {
	t.Run("configured value parses to the written duration", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      growth: 20m\n")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}
		if config == nil {
			t.Fatal("expected config, got nil")
		}

		spec, ok := config.Substrates["core"]
		if !ok {
			t.Fatalf("substrate core missing from %#v", config.Substrates)
		}
		if spec.Patience.Growth != "20m" {
			t.Fatalf("patience.growth = %q, want %q", spec.Patience.Growth, "20m")
		}

		budget, err := spec.Patience.GrowthBudget()
		if err != nil {
			t.Fatalf("GrowthBudget failed: %v", err)
		}
		// 20 minutes as a literal, not as anything the parser produced.
		if budget != 20*time.Minute {
			t.Fatalf("GrowthBudget = %s, want %s", budget, 20*time.Minute)
		}
	})

	t.Run("absent patience is zero without error", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}

		spec, ok := config.Substrates["core"]
		if !ok {
			t.Fatalf("substrate core missing from %#v", config.Substrates)
		}
		if spec.Patience.Growth != "" {
			t.Fatalf("patience.growth = %q, want empty", spec.Patience.Growth)
		}

		budget, err := spec.Patience.GrowthBudget()
		if err != nil {
			t.Fatalf("GrowthBudget failed: %v", err)
		}
		if budget != 0 {
			t.Fatalf("GrowthBudget = %s, want 0", budget)
		}
	})

	t.Run("malformed duration fails the load naming the field", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      growth: soonish\n")

		config, err := LoadSubstratesConfig("")
		if err == nil {
			t.Fatalf("LoadSubstratesConfig returned no error; config = %#v", config)
		}
		if config != nil {
			t.Fatalf("expected nil config alongside the error, got %#v", config)
		}
		// Naming the field and the offending value is the whole point: a bare
		// "invalid configuration" leaves the operator hunting.
		for _, want := range []string{"patience.growth", "soonish", `"core"`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("load error %q does not mention %q", err.Error(), want)
			}
		}
	})

	t.Run("zero duration fails the load rather than bounding the run to nothing", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      growth: 0s\n")

		config, err := LoadSubstratesConfig("")
		if err == nil {
			t.Fatalf("LoadSubstratesConfig returned no error for a zero patience; config = %#v", config)
		}
		if !strings.Contains(err.Error(), "patience.growth") {
			t.Fatalf("load error %q does not mention patience.growth", err.Error())
		}
	})
}

// TestResolveSubstrateExecutionPlanCarriesPatience proves the plan is the
// carrier: the resolver hands the configured duration onward, and hands zero
// onward when nothing is configured.
func TestResolveSubstrateExecutionPlanCarriesPatience(t *testing.T) {
	t.Run("configured", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      growth: 20m\n")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "core"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan failed: %v", err)
		}
		if plan.growthBudget != 20*time.Minute {
			t.Fatalf("plan.growthBudget = %s, want %s", plan.growthBudget, 20*time.Minute)
		}
	})

	t.Run("unconfigured", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "core"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan failed: %v", err)
		}
		if plan.growthBudget != 0 {
			t.Fatalf("plan.growthBudget = %s, want 0 when unconfigured", plan.growthBudget)
		}
	})
}

// patienceCapture records what the terrarium session seam saw at the instant it
// was called. The context's remaining time is sampled INSIDE the seam, from the
// context the call site actually passed: a sample taken before the call would
// have decayed by however long the setup took.
type patienceCapture struct {
	called      bool
	timeout     time.Duration
	hasDeadline bool
	remaining   time.Duration
}

// captureTerrariumSessionPatience replaces the session seam with one that
// records the deadline state of the context reaching it.
func captureTerrariumSessionPatience(t *testing.T) *patienceCapture {
	t.Helper()

	capture := &patienceCapture{}
	original := startTerrariumSessionFn
	t.Cleanup(func() { startTerrariumSessionFn = original })

	startTerrariumSessionFn = func(innerCtx context.Context, providerName, imageName, mountPath string, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		capture.called = true
		capture.timeout = timeout
		if deadline, ok := innerCtx.Deadline(); ok {
			capture.hasDeadline = true
			capture.remaining = time.Until(deadline)
		}
		return &stubToolSession{}, nil
	}

	return capture
}

// assertBoundedByConfiguredPatience checks the properties that only hold when
// the call site wrapped the context with the configured budget:
//
//   - the context reaching the session carries a deadline at all (the caller
//     passed one without a deadline, so it can only have come from the wrap);
//   - that deadline is inside the configured budget and not trivially small,
//     so a wrap with the wrong duration or the wrong unit still fails;
//   - the derived watchdog exceeds the remaining time and is not the fallback,
//     so the bound genuinely reached the terrarium.
func assertBoundedByConfiguredPatience(t *testing.T, label string, capture *patienceCapture) {
	t.Helper()

	if !capture.called {
		t.Fatalf("%s: startTerrariumSessionFn was never called; nothing was measured", label)
	}
	if !capture.hasDeadline {
		t.Fatalf("%s: context reaching the session carries no deadline; the configured patience was not applied", label)
	}
	if capture.remaining <= 0 || capture.remaining > configuredPatienceBudget {
		t.Fatalf("%s: remaining %s outside (0, %s]; the applied bound is not the configured one", label, capture.remaining, configuredPatienceBudget)
	}
	if capture.remaining < time.Minute {
		t.Fatalf("%s: remaining %s is far below the configured %s; a different duration was applied", label, capture.remaining, configuredPatienceBudget)
	}
	if capture.timeout <= capture.remaining {
		t.Fatalf("%s: watchdog %s <= remaining %s; the context would not expire first", label, capture.timeout, capture.remaining)
	}
	if capture.timeout == terrariumWatchdogFallback {
		t.Fatalf("%s: watchdog == terrariumWatchdogFallback (%s); the bound never reached the terrarium", label, capture.timeout)
	}
}

// assertUnbounded checks the opposite: with no patience configured nothing is
// wrapped, so today's behaviour is preserved exactly.
func assertUnbounded(t *testing.T, label string, capture *patienceCapture) {
	t.Helper()

	if !capture.called {
		t.Fatalf("%s: startTerrariumSessionFn was never called; nothing was measured", label)
	}
	if capture.hasDeadline {
		t.Fatalf("%s: context reaching the session carries a deadline (%s remaining) though no patience is configured", label, capture.remaining)
	}
	if capture.timeout != terrariumWatchdogFallback {
		t.Fatalf("%s: watchdog = %s, want fallback %s", label, capture.timeout, terrariumWatchdogFallback)
	}
}

// TestRunSproutAppliesConfiguredPatience covers the RunSprout path. The caller's
// context deliberately carries NO deadline, so any deadline observed at the
// session seam can only have come from the configured patience.
func TestRunSproutAppliesConfiguredPatience(t *testing.T) {
	t.Run("configured patience bounds the run", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 2m\n")

		stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "done"}, []string{"pkg/thing.go"})
		capture := captureTerrariumSessionPatience(t)

		orch := &DockerOrchestrator{
			Substrate:        "bounded",
			StepID:           "patience-runsprout-configured",
			DisableMergeBack: true,
		}
		orch.RunSprout(context.Background(), "patience probe") //nolint:errcheck

		assertBoundedByConfiguredPatience(t, "RunSprout", capture)
	})

	t.Run("unset patience leaves the run unbounded", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "bounded", root, "")

		stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "done"}, []string{"pkg/thing.go"})
		capture := captureTerrariumSessionPatience(t)

		orch := &DockerOrchestrator{
			Substrate:        "bounded",
			StepID:           "patience-runsprout-unset",
			DisableMergeBack: true,
		}
		orch.RunSprout(context.Background(), "patience probe") //nolint:errcheck

		assertUnbounded(t, "RunSprout", capture)
	})
}

// TestRunSequenceSproutAtPathAppliesConfiguredPatience covers the sequence path.
// Covering only RunSprout would leave this call site free to ignore the setting.
func TestRunSequenceSproutAtPathAppliesConfiguredPatience(t *testing.T) {
	stubSequenceCollaborators := func(t *testing.T, root string) {
		t.Helper()

		originalEnsure := ensureSproutImageFn
		originalCreateShadow := createShadowWorktreeFn
		originalRemoveShadow := removeShadowWorktreeFn
		originalInjectCache := injectMycorrhizalCacheFn
		originalNewSprout := newSproutFn
		originalStash := stashHostWorkspaceFn
		t.Cleanup(func() {
			ensureSproutImageFn = originalEnsure
			createShadowWorktreeFn = originalCreateShadow
			removeShadowWorktreeFn = originalRemoveShadow
			injectMycorrhizalCacheFn = originalInjectCache
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
	}

	t.Run("configured patience bounds the run", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: 2m\n")

		stubSequenceCollaborators(t, root)
		capture := captureTerrariumSessionPatience(t)

		orch := &DockerOrchestrator{
			Substrate:        "bounded",
			StepID:           "patience-seqpath-configured",
			DisableMergeBack: true,
		}
		runSequenceSproutAtPath(context.Background(), orch, "patience probe", root, root) //nolint:errcheck

		assertBoundedByConfiguredPatience(t, "runSequenceSproutAtPath", capture)
	})

	t.Run("unset patience leaves the run unbounded", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "bounded", root, "")

		stubSequenceCollaborators(t, root)
		capture := captureTerrariumSessionPatience(t)

		orch := &DockerOrchestrator{
			Substrate:        "bounded",
			StepID:           "patience-seqpath-unset",
			DisableMergeBack: true,
		}
		runSequenceSproutAtPath(context.Background(), orch, "patience probe", root, root) //nolint:errcheck

		assertUnbounded(t, "runSequenceSproutAtPath", capture)
	})
}
