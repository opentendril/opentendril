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

// configuredPatienceBudgetText is the value the call-site tests below configure,
// written exactly as an operator would put it in the file. It is a literal the
// tests own, never a value read back from the code under test, so an
// implementation that resolved the wrong duration — or the wrong unit — cannot
// agree with it.
const configuredPatienceBudgetText = "300ms"

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

// assertGrowthBudgetStaysOffTheTerrarium replaces the assertion this file used
// to make. It asserted that the configured growth budget reached the terrarium
// — that the container's context carried the budget's deadline and the watchdog
// was derived from it. That was correct when the budget bounded the work. It is
// now the defect: the budget bounds how long the STEM WAITS, and a container
// whose clock is the wait's clock dies the moment the Stem stops waiting, which
// makes detaching a kill with a nicer name.
//
// The old assertion's positive half — that the configured value is applied
// exactly, with the unit written — has not been dropped. It moved to the
// mechanism the budget now drives, and is asserted on the sprout-detached event
// in the call-site tests below.
func assertGrowthBudgetStaysOffTheTerrarium(t *testing.T, label string, capture *patienceCapture) {
	t.Helper()

	if !capture.called {
		t.Fatalf("%s: startTerrariumSessionFn was never called; nothing was measured", label)
	}
	if capture.hasDeadline {
		t.Fatalf("%s: the context that owns the container carries a deadline (%s remaining) though only a growth budget is configured; the budget would stop the terrarium the moment the Stem stopped waiting", label, capture.remaining)
	}
	if capture.timeout != terrariumWatchdogFallback {
		t.Fatalf("%s: watchdog = %s, want the fallback %s; a watchdog derived from the growth budget fires shortly after a detach and ends the run the detach was meant to preserve", label, capture.timeout, terrariumWatchdogFallback)
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
// context deliberately carries NO deadline, so any bound observed can only have
// come from the configured patience — and the point of each assertion is WHICH
// clock the configured value became.
func TestRunSproutAppliesConfiguredPatience(t *testing.T) {
	t.Run("configured patience bounds the wait, not the terrarium", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: "+configuredPatienceBudgetText+"\n")

		runner := newHeldSproutRunner("done")
		stubRunSproutCollaborators(t, root, runner, []string{"pkg/thing.go"})
		capture := captureTerrariumSessionPatience(t)

		bus := eventbus.New()
		recorder := recordSproutEvents(bus)

		orch := &DockerOrchestrator{
			Substrate:        "bounded",
			StepID:           "patience-runsprout-configured",
			EventBus:         bus,
			DisableMergeBack: true,
		}
		report, err := orch.RunSprout(context.Background(), "patience probe")

		// The configured budget bounded the wait: the run is still going and
		// the Stem has stopped waiting for it. Without the bound this call
		// would not have returned at all.
		if err != nil {
			t.Fatalf("RunSprout returned %v, want a clean detach", err)
		}
		if report.Outcome != SproutOutcomeDetached {
			t.Fatalf("report.Outcome = %q, want %q; the configured patience did not bound the wait", report.Outcome, SproutOutcomeDetached)
		}
		// Exactly the value written, unit included.
		assertDetachedEvent(t, "RunSprout", recorder, configuredPatienceBudgetText)
		assertGrowthBudgetStaysOffTheTerrarium(t, "RunSprout", capture)

		runner.release()
		recorder.awaitTerminal(t, "RunSprout")
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

	t.Run("configured patience bounds the wait, not the terrarium", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "bounded", root, "    patience:\n      growth: "+configuredPatienceBudgetText+"\n")

		stubSequenceCollaborators(t, root)
		runner := newHeldSproutRunner("done")
		stubSequenceRunner(t, runner)
		capture := captureTerrariumSessionPatience(t)

		bus := eventbus.New()
		recorder := recordSproutEvents(bus)

		orch := &DockerOrchestrator{
			Substrate:        "bounded",
			StepID:           "patience-seqpath-configured",
			EventBus:         bus,
			DisableMergeBack: true,
		}
		result, err := runSequenceSproutAtPath(context.Background(), orch, "patience probe", root, root)

		if err != nil {
			t.Fatalf("runSequenceSproutAtPath returned %v, want a clean detach", err)
		}
		if result.Outcome != SproutOutcomeDetached {
			t.Fatalf("result.Outcome = %q, want %q; the configured patience did not bound the wait", result.Outcome, SproutOutcomeDetached)
		}
		assertDetachedEvent(t, "runSequenceSproutAtPath", recorder, configuredPatienceBudgetText)
		assertGrowthBudgetStaysOffTheTerrarium(t, "runSequenceSproutAtPath", capture)

		runner.release()
		recorder.awaitTerminal(t, "runSequenceSproutAtPath")
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
