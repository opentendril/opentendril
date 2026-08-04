package conductor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// TestPatienceScratchLoading pins the schema half of the scratch interval, on
// the same terms as the two budgets beside it: a well-formed value parses to
// exactly what was written, an absent one is zero without error, and a value
// that cannot be honoured fails the load by name rather than defaulting quietly.
// The three patience fields differ in what they govern, never in how strictly
// they are read.
func TestPatienceScratchLoading(t *testing.T) {
	t.Run("configured value parses and reaches the plan", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      scratch: 45s\n")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}

		spec, ok := config.Substrates["core"]
		if !ok {
			t.Fatalf("substrate core missing from %#v", config.Substrates)
		}
		if spec.Patience.Scratch != "45s" {
			t.Fatalf("patience.scratch = %q, want %q", spec.Patience.Scratch, "45s")
		}

		interval, err := spec.Patience.ScratchInterval()
		if err != nil {
			t.Fatalf("ScratchInterval failed: %v", err)
		}
		// Forty-five seconds as a literal the test owns, never a value read
		// back from the parser it is checking.
		if interval != 45*time.Second {
			t.Fatalf("ScratchInterval = %s, want %s", interval, 45*time.Second)
		}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "core"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan failed: %v", err)
		}
		if plan.scratchInterval != 45*time.Second {
			t.Fatalf("plan.scratchInterval = %s, want %s; the plan is the carrier and dropped it", plan.scratchInterval, 45*time.Second)
		}
	})

	t.Run("absent scratch interval is zero without error", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      growth: 20m\n")

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}

		interval, err := config.Substrates["core"].Patience.ScratchInterval()
		if err != nil {
			t.Fatalf("ScratchInterval failed: %v", err)
		}
		if interval != 0 {
			t.Fatalf("ScratchInterval = %s, want 0 when unconfigured", interval)
		}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "core"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan failed: %v", err)
		}
		if plan.scratchInterval != 0 {
			t.Fatalf("plan.scratchInterval = %s, want 0 when unconfigured", plan.scratchInterval)
		}
	})

	t.Run("malformed duration fails the load naming the field", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      scratch: occasionally\n")

		config, err := LoadSubstratesConfig("")
		if err == nil {
			t.Fatalf("LoadSubstratesConfig returned no error; config = %#v", config)
		}
		if config != nil {
			t.Fatalf("expected nil config alongside the error, got %#v", config)
		}
		for _, want := range []string{"patience.scratch", "occasionally", `"core"`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("load error %q does not mention %q", err.Error(), want)
			}
		}
	})

	t.Run("zero duration fails the load rather than probing in a tight loop", func(t *testing.T) {
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "core", cwd, "    patience:\n      scratch: 0s\n")

		config, err := LoadSubstratesConfig("")
		if err == nil {
			t.Fatalf("LoadSubstratesConfig returned no error for a zero interval; config = %#v", config)
		}
		if !strings.Contains(err.Error(), "patience.scratch") {
			t.Fatalf("load error %q does not mention patience.scratch", err.Error())
		}
	})
}

// TestWatchDormancyAttachesOnlyWhenConfigured states what the configuration
// actually switches. Unset, nothing is attached and nothing is probed, so the
// default is byte-for-byte today's behaviour; set, the watcher is on the bus for
// the run and off it again afterwards.
func TestWatchDormancyAttachesOnlyWhenConfigured(t *testing.T) {
	t.Run("unconfigured attaches nothing", func(t *testing.T) {
		bus := eventbus.New()
		stop := watchDormancy(context.Background(), bus, 0, t.TempDir())
		defer stop()

		if got := bus.HandlerCount(eventbus.EventStreamToken); got != 0 {
			t.Fatalf("stream-token handlers with no scratch interval = %d, want 0", got)
		}
	})

	t.Run("no bus attaches nothing", func(t *testing.T) {
		// Nothing to publish a report to and nothing to read events from; the
		// call must be inert rather than starting a loop that observes nobody.
		stop := watchDormancy(context.Background(), nil, time.Second, t.TempDir())
		stop()
	})

	t.Run("configured attaches for the run and detaches after it", func(t *testing.T) {
		bus := eventbus.New()
		stop := watchDormancy(context.Background(), bus, time.Hour, t.TempDir())

		if got := bus.HandlerCount(eventbus.EventStreamToken); got != 1 {
			t.Fatalf("stream-token handlers while watching = %d, want 1", got)
		}
		if got := bus.HandlerCount(eventbus.EventSproutDetached); got != 1 {
			t.Fatalf("sprout-detached handlers while watching = %d, want 1; a detached run is the one that most needs watching", got)
		}

		stop()

		if got := bus.HandlerCount(eventbus.EventStreamToken); got != 0 {
			t.Fatalf("stream-token handlers after the run = %d, want 0; the watcher outlived its run", got)
		}
	})
}

// TestWatchDormancyProbesThroughTheInjectedPort proves the scratch test reaches
// the real workspace measurement without the watcher importing it. The seam is
// the same one the post-mortem uses, and the mount path must arrive intact — a
// probe pointed at the wrong directory would report a diff that never grows and
// silently disable the suppressor.
func TestWatchDormancyProbesThroughTheInjectedPort(t *testing.T) {
	original := collectStageableFilesFn
	t.Cleanup(func() { collectStageableFilesFn = original })

	mountPath := t.TempDir()
	probed := make(chan string, 1)
	collectStageableFilesFn = func(ctx context.Context, path string, excludedPaths ...string) ([]string, error) {
		select {
		case probed <- path:
		default:
		}
		return []string{"pkg/thing.go"}, nil
	}

	bus := eventbus.New()
	// A short interval so the production loop's own ticker fires promptly. The
	// assertion below blocks on the probe rather than on the clock, so the
	// interval only bounds how long the wait can be, never whether it is long
	// enough for the thing to have happened.
	stop := watchDormancy(context.Background(), bus, 5*time.Millisecond, mountPath)
	defer stop()

	bus.Publish(eventbus.Event{
		Type:      eventbus.EventSproutEmerged,
		Source:    "step-probe",
		SessionID: "session-probe",
	})

	select {
	case path := <-probed:
		if path != mountPath {
			t.Fatalf("probe measured %q, want the run's mount path %q", path, mountPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the workspace was never probed; the scratch test is not wired to the measurement")
	}
}

// dormancyWatchProbe is a Sprout stand-in that records what the bus looked like
// while the run was in progress. Asserting after RunSprout returns cannot tell a
// watcher that ran from one that was attached and detached without ever
// observing anything, because by then it is detached either way.
type dormancyWatchProbe struct {
	response          string
	bus               *eventbus.Bus
	handlersDuringRun int
}

// Run takes the observation from inside the Sprout turn. Taken anywhere else it
// would measure the wrong instant: the watcher is attached after the Sprout is
// built and detached by the teardown sequence, so before and after the turn the
// count is zero whether or not anything was ever watching.
func (p *dormancyWatchProbe) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	p.handlersDuringRun = p.bus.HandlerCount(eventbus.EventStreamToken)
	return sproutResult{Response: p.response}, nil
}

// TestRunSproutWatchesForDormancyWhenConfigured is the end-to-end half: the
// configured interval reaches the watcher through RunSprout, and the watcher is
// live for the duration of the Sprout turn rather than attached after it.
func TestRunSproutWatchesForDormancyWhenConfigured(t *testing.T) {
	assertWatched := func(t *testing.T, patienceBlock string, wantHandlers int) {
		t.Helper()

		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "watched", root, patienceBlock)

		bus := eventbus.New()
		probe := &dormancyWatchProbe{response: "done", bus: bus}
		stubRunSproutCollaborators(t, root, probe, []string{"pkg/thing.go"})

		orch := &DockerOrchestrator{
			Substrate:        "watched",
			StepID:           "dormancy-runsprout",
			DisableMergeBack: true,
			EventBus:         bus,
		}
		orch.RunSprout(context.Background(), "dormancy probe") //nolint:errcheck

		if probe.handlersDuringRun != wantHandlers {
			t.Fatalf("stream-token handlers during the run = %d, want %d", probe.handlersDuringRun, wantHandlers)
		}
		if got := bus.HandlerCount(eventbus.EventStreamToken); got != 0 {
			t.Fatalf("stream-token handlers after the run = %d, want 0; the watcher was not torn down", got)
		}
	}

	t.Run("configured", func(t *testing.T) {
		assertWatched(t, "    patience:\n      scratch: 30s\n", 1)
	})

	// The default has to be provably inert, not merely believed to be: every
	// run in this repository that does not configure patience must behave
	// exactly as it did before this existed.
	t.Run("unconfigured", func(t *testing.T) {
		assertWatched(t, "", 0)
	})
}

// TestRunSequenceSproutAtPathWatchesForDormancyWhenConfigured covers the second
// call site. It is the one the parallel path actually reaches, so covering only
// RunSprout would leave the observable behaviour unchanged while looking
// addressed — the same trap the earlier slices on this path each fell into.
func TestRunSequenceSproutAtPathWatchesForDormancyWhenConfigured(t *testing.T) {
	assertWatched := func(t *testing.T, patienceBlock string, wantHandlers int) {
		t.Helper()

		root := newOutcomeTestRepo(t)
		cwd := chdirToTempDir(t)
		writePatienceSubstrate(t, cwd, "watched", root, patienceBlock)

		bus := eventbus.New()
		probe := &dormancyWatchProbe{response: "done", bus: bus}

		originalEnsure := ensureSproutImageFn
		originalCreateShadow := createShadowWorktreeFn
		originalRemoveShadow := removeShadowWorktreeFn
		originalInjectCache := injectMycorrhizalCacheFn
		originalNewSprout := newSproutFn
		originalStash := stashHostWorkspaceFn
		originalStartSession := startTerrariumSessionFn
		t.Cleanup(func() {
			ensureSproutImageFn = originalEnsure
			createShadowWorktreeFn = originalCreateShadow
			removeShadowWorktreeFn = originalRemoveShadow
			injectMycorrhizalCacheFn = originalInjectCache
			newSproutFn = originalNewSprout
			stashHostWorkspaceFn = originalStash
			startTerrariumSessionFn = originalStartSession
		})

		ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
		createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) { return root, nil }
		removeShadowWorktreeFn = func(sourcePath, shadowPath string) {}
		injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
		stashHostWorkspaceFn = func(ctx context.Context, repoRoot, runID string) (bool, error) { return false, nil }
		newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
			return probe, nil
		}

		orch := &DockerOrchestrator{
			Substrate:        "watched",
			StepID:           "dormancy-seqpath",
			DisableMergeBack: true,
			EventBus:         bus,
		}
		runSequenceSproutAtPath(context.Background(), orch, "dormancy probe", root, root) //nolint:errcheck

		if probe.handlersDuringRun != wantHandlers {
			t.Fatalf("stream-token handlers during the run = %d, want %d", probe.handlersDuringRun, wantHandlers)
		}
		if got := bus.HandlerCount(eventbus.EventStreamToken); got != 0 {
			t.Fatalf("stream-token handlers after the run = %d, want 0; the watcher was not torn down", got)
		}
	}

	t.Run("configured", func(t *testing.T) {
		assertWatched(t, "    patience:\n      scratch: 30s\n", 1)
	})

	t.Run("unconfigured", func(t *testing.T) {
		assertWatched(t, "", 0)
	})
}
