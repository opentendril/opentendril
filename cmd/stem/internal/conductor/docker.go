package conductor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	opentendril "github.com/opentendril/opentendril"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/mesh"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

const (
	terrariumProviderEnvKey = "TENDRIL_TERRARIUM_PROVIDER"
	// EnvAllowHostWorkspace opts into running on the active host workspace when
	// shadow-worktree isolation cannot be established. Default (unset) is fail-closed.
	EnvAllowHostWorkspace = "TENDRIL_ALLOW_HOST_WORKSPACE"

	// terrariumWatchdogFallback is the terrarium watchdog timeout used when the
	// caller's context carries no deadline. It is a backstop against a hung
	// container, not a statement about how long work should take — callers that
	// set their own deadline govern the run duration and the watchdog will never
	// fire before them.
	terrariumWatchdogFallback = 10 * time.Minute

	// sproutPostMortemBudget bounds what happens after the Sprout loop returns:
	// measuring what changed, classifying the outcome, and writing the record.
	// It is deliberately independent of the growth budget, because a budget
	// bounds the work and never the account of the work — a run whose growth
	// budget expired hands its post-mortem an already-spent context, and the
	// evidence dies with the clock that ended the run.
	//
	// It stays finite rather than unbounded: a git call issued after a stalled
	// run is exactly the shape that turned a waiting suite into an apparent
	// hang. Sized for git work (staging and committing a large change), not for
	// a status listing alone, because the record is written by the commit path.
	sproutPostMortemBudget = 60 * time.Second
)

// allowHostWorkspace reports whether the operator explicitly opted into the
// host-workspace fallback.
func allowHostWorkspace() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvAllowHostWorkspace)), "true")
}

// DockerOrchestrator implements the Orchestrator interface using the local Docker daemon.
type DockerOrchestrator struct {
	ImageName        string
	Substrate        string
	SubstrateURL     string
	SubstrateBranch  string
	StepID           string
	StatusPath       string
	IsCoordinator    bool
	Tier             llm.ModelTier
	Provider         string
	Model            string
	BaseURL          string
	Genotype         string
	Temperature      float64
	DisableMergeBack bool
	Investigation    bool
	EventBus         *eventbus.Bus
	// SessionID attributes the run's lifecycle events to the session (Phytomer)
	// it belongs to; empty for sessionless runs.
	SessionID string
	// AwaitsRunEnding declares that the caller will block until the run finishes,
	// rather than carrying it on asynchronously. When true, a spent growth
	// budget ends the run as timed-out instead of detaching.
	AwaitsRunEnding bool
	// OnTerminal is invoked exactly once when this RunSprout lifecycle reaches
	// a real ending. A detached return is not an ending; the goroutine that
	// finishes completeRun invokes this later. Adapters that persist history
	// install this; the conductor does not write the store.
	OnTerminal func(report SproutRunReport, err error)
}

func NewDockerOrchestrator() *DockerOrchestrator {
	return &DockerOrchestrator{}
}

type sproutRunner interface {
	Run(ctx context.Context, taskPrompt string) (sproutResult, error)
}

var (
	ensureSproutImageFn     = ensureSproutImage
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return startTerrariumSession(ctx, providerName, imageName, mountPath, readOnly, command, extraEnv, timeout, observers...)
	}
	newSproutFn = func(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string, sessionID string) (sproutRunner, error) {
		return newSprout(ctx, workspace, genotypeRoot, genotypeName, client, session, eventBus, stepID, sessionID)
	}
	stashHostWorkspaceFn           = stashHostWorkspace
	restoreHostStashFn             = restoreHostStash
	createShadowWorktreeFn         = createShadowWorktree
	removeShadowWorktreeFn         = removeShadowWorktree
	injectMycorrhizalCacheFn       = injectMycorrhizalCache
	copyMycorrhizalCacheFn         = copyMycorrhizalCache
	createRunWorkspaceFn           = CreateRunWorkspace
	terrariumNewProviderFn         = terrarium.NewProvider
	osGetuidFn                     = os.Getuid
	osGetgidFn                     = os.Getgid
	dockerIsRootlessFn             = DockerIsRootless
	collectStageableFilesFn        = collectStageableFiles
	collectGitDiffFn               = collectGitDiff
	commitTerrariumExecutionFn     = commitTerrariumExecution
	publishManagedAPIFruitFn       = publishManagedAPIFruit
	mergeTerrariumCommitFn         = mergeTerrariumCommit
	pushTerrariumCommitFn          = pushTerrariumCommit
	runContainerFitnessTestFn      = runContainerFitnessTest
	generateRepoMapFn              = GenerateRepoMap
	generateMemoryMapFn            = GenerateMemoryMap
	runSproutPreflightChecksFn     = runSproutPreflightChecks
	runVerifierCommandFn           = runVerifierCommand
	materializeSproutBuildInputsFn = materializeSproutBuildInputs
)

func (d *DockerOrchestrator) resolveLLMClient() *llm.Client {
	var spec llm.ProviderSpec
	if d != nil && d.IsCoordinator {
		spec = llm.ResolveCoordinatorProviderSpec()
	} else if d != nil && strings.TrimSpace(d.Provider) != "" {
		// A preference that names a provider and no model is still a choice of
		// provider. Requiring both before taking this path discarded it and
		// went to tier resolution, where — until the provider became a filter
		// there too — the run could end up somewhere else entirely.
		spec = llm.ResolveModelProviderSpec(d.Provider, d.Model)
	} else {
		// An unconfigured autonomous run starts on the cheapest model that can
		// still drive tools, and is escalated deliberately — by a tier on the
		// step, a session preference, or a pinned model — rather than by
		// default. The ceiling now means what it says, so defaulting it to
		// premium would put every unattended run on the most expensive model
		// its provider serves, which is not a decision to make on an operator's
		// behalf and not one they would see until a bill arrived.
		tier := llm.TierCheapest
		if d != nil && d.Tier != "" {
			tier = d.Tier
		}
		// An autonomous sprout must drive tools; the tier default must never
		// fall back to a model that cannot (see ResolveAgentTierProviderSpec,
		// which raises this ceiling before it ever gives up tool-capability).
		spec = llm.ResolveAgentTierProviderSpec(tier)
	}

	if d != nil && strings.TrimSpace(d.BaseURL) != "" {
		spec.BaseURL = strings.TrimSpace(d.BaseURL)
		if spec.Provider == "local" {
			spec.BaseURLs = llm.LocalInferenceBaseURLs(spec.BaseURL)
		} else {
			spec.BaseURLs = []string{spec.BaseURL}
		}
	}

	client := llm.NewClient(spec)
	if d != nil && d.Temperature > 0 {
		client.SetTemperature(d.Temperature)
	}
	return client
}

func isWritableManagedRun(path string, plan *substrateExecutionPlan, investigation bool) bool {
	if plan == nil || plan.readOnly || investigation {
		return false
	}

	// Persistence alone is not managed identity. In particular, checkout mode
	// path remains the operator's own checkout and keeps its existing shadow
	// worktree behaviour.
	switch strings.ToLower(strings.TrimSpace(plan.credential.Checkout.Mode)) {
	case "path", "ephemeral":
		return false
	}

	return isManagedCheckoutPath(path)
}

var (
	beforeResolveManagedRunStartCommitLock = func() {}
	readManagedRunStartCommitFn            = readManagedRunStartCommit
)

func resolveManagedRunStartCommit(ctx context.Context, sourcePath string) (string, error) {
	// The managed checkout may be refreshed by another named run. Resolve HEAD
	// under the same short Git metadata lock used by materialization and run
	// workspace allocation, but never carry the lock into Sprout execution.
	beforeResolveManagedRunStartCommitLock()
	unlockGit := lockRunWorkspaceGit(sourcePath)
	defer unlockGit()
	return readManagedRunStartCommitFn(ctx, sourcePath)
}

func readManagedRunStartCommit(ctx context.Context, sourcePath string) (string, error) {
	commit, err := runGitCommand(ctx, sourcePath, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve managed checkout start commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return "", fmt.Errorf("resolve managed checkout start commit returned no commit")
	}
	return commit, nil
}

type runWorkspaceFileSnapshot struct {
	content []byte
	mode    fs.FileMode
}

type runWorkspaceGeneratedTransition struct {
	before map[string]runWorkspaceFileSnapshot
	after  map[string]runWorkspaceFileSnapshot
}

// runWorkspaceGeneratedState records Tendril-owned files before and after
// each host-generated write. Cleanup restores only files that still contain
// the generated bytes; a Sprout change makes cleanup stop and preserve the
// workspace for review.
type runWorkspaceGeneratedState struct {
	root            string
	transitions     []runWorkspaceGeneratedTransition
	initialCaptured bool
}

func newRunWorkspaceGeneratedState(root string) (*runWorkspaceGeneratedState, error) {
	state := &runWorkspaceGeneratedState{root: root}
	before, err := state.snapshot()
	if err != nil {
		return nil, err
	}
	state.transitions = append(state.transitions, runWorkspaceGeneratedTransition{before: before})
	return state, nil
}

func (state *runWorkspaceGeneratedState) captureInitialAfter() error {
	if state == nil || len(state.transitions) == 0 {
		return nil
	}
	after, err := state.snapshot()
	if err != nil {
		return err
	}
	state.transitions[0].after = after
	state.initialCaptured = true
	return nil
}

func (state *runWorkspaceGeneratedState) captureTransition(work func() error) error {
	if state == nil {
		return work()
	}
	before, err := state.snapshot()
	if err != nil {
		return err
	}
	workErr := work()
	after, snapshotErr := state.snapshot()
	if snapshotErr != nil {
		return errors.Join(workErr, snapshotErr)
	}
	state.transitions = append(state.transitions, runWorkspaceGeneratedTransition{before: before, after: after})
	return workErr
}

func (state *runWorkspaceGeneratedState) cleanup() error {
	if state == nil {
		return nil
	}
	if !state.initialCaptured {
		if err := state.captureInitialAfter(); err != nil {
			return err
		}
	}

	for index := len(state.transitions) - 1; index >= 0; index-- {
		transition := state.transitions[index]
		current, err := state.snapshot()
		if err != nil {
			return err
		}
		for path, after := range transition.after {
			currentFile, exists := current[path]
			if !exists {
				return fmt.Errorf("Tendril-generated file %s was removed during the run; preserving the run workspace", path)
			}
			if !sameRunWorkspaceFileSnapshot(currentFile, after) {
				return fmt.Errorf("Tendril-generated file %s was changed during the run; preserving the run workspace", path)
			}

			before, existed := transition.before[path]
			absolute := filepath.Join(state.root, filepath.FromSlash(path))
			if existed {
				if err := os.WriteFile(absolute, before.content, before.mode.Perm()); err != nil {
					return fmt.Errorf("restore Tendril-generated file %s: %w", path, err)
				}
				if err := os.Chmod(absolute, before.mode.Perm()); err != nil {
					return fmt.Errorf("restore Tendril-generated file mode %s: %w", path, err)
				}
			} else if err := os.Remove(absolute); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove Tendril-generated file %s: %w", path, err)
			}
		}
	}

	return nil
}

func (state *runWorkspaceGeneratedState) snapshot() (map[string]runWorkspaceFileSnapshot, error) {
	return snapshotRunWorkspaceGeneratedFiles(state.root, nil)
}

func snapshotRunWorkspaceGeneratedFiles(root string, trackedRoots map[string]struct{}) (map[string]runWorkspaceFileSnapshot, error) {
	files := make(map[string]runWorkspaceFileSnapshot)
	roots := make([]string, 0, len(trackedRoots)+1)
	if trackedRoots == nil {
		roots = append(roots, filepath.Join(root, tendrilStateDirectory))
	}
	for trackedRoot := range trackedRoots {
		roots = append(roots, trackedRoot)
	}
	for _, scanRoot := range roots {
		err := filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(relative)] = runWorkspaceFileSnapshot{
				content: content,
				mode:    info.Mode(),
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("snapshot run workspace generated state: %w", err)
		}
	}
	return files, nil
}

func sameRunWorkspaceFileSnapshot(first, second runWorkspaceFileSnapshot) bool {
	return first.mode.Perm() == second.mode.Perm() && bytes.Equal(first.content, second.content)
}

type runWorkspaceCacheState struct {
	workspaceRoot string
	cacheRoot     string
	initial       map[string]runWorkspaceFileSnapshot
}

func newRunWorkspaceCacheState(workspaceRoot, cacheRoot string) (runWorkspaceCacheState, error) {
	initial, err := snapshotRunWorkspaceGeneratedFiles(workspaceRoot, map[string]struct{}{filepath.Clean(cacheRoot): {}})
	if err != nil {
		return runWorkspaceCacheState{}, err
	}
	return runWorkspaceCacheState{
		workspaceRoot: workspaceRoot,
		cacheRoot:     filepath.Clean(cacheRoot),
		initial:       initial,
	}, nil
}

func (state runWorkspaceCacheState) cleanup() error {
	current, err := snapshotRunWorkspaceGeneratedFiles(state.workspaceRoot, map[string]struct{}{state.cacheRoot: {}})
	if err != nil {
		return err
	}
	if !sameRunWorkspaceFileSnapshots(state.initial, current) {
		return fmt.Errorf("Mycorrhizal cache %s was changed during the run; preserving the run workspace", state.cacheRoot)
	}
	if err := os.RemoveAll(state.cacheRoot); err != nil {
		return fmt.Errorf("remove copied Mycorrhizal cache %s: %w", state.cacheRoot, err)
	}
	return nil
}

func sameRunWorkspaceFileSnapshots(first, second map[string]runWorkspaceFileSnapshot) bool {
	if len(first) != len(second) {
		return false
	}
	for path, snapshot := range first {
		other, ok := second[path]
		if !ok || !sameRunWorkspaceFileSnapshot(snapshot, other) {
			return false
		}
	}
	return true
}

func (d *DockerOrchestrator) RunSprout(ctx context.Context, taskPrompt string) (report SproutRunReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx := context.WithoutCancel(ctx)

	stepID := strings.TrimSpace(d.StepID)
	if stepID == "" {
		stepID = newSproutExecutionID("step")
		d.StepID = stepID
	}

	// Every ENDING below leaves through this one publisher, so every surface —
	// command line, sequence, MCP, daemon — sees exactly one terminal lifecycle
	// event per run, with the honest outcome and the evidence for it. A detach
	// is not an ending: it hands the same publisher to the goroutine that
	// outlives this call, which fires it when the work does end.
	var changes changeEvidence
	detached := false
	publishTerminal := func(report *SproutRunReport, changes changeEvidence, err error) {
		if report.Outcome == "" {
			report.Outcome = classifySproutOutcome(err, changes, report.Output, d.Investigation)
		}
		applyObservation(report, err)
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		publishSproutTerminal(d.EventBus, stepID, d.SessionID, *report, reason)
		if d.OnTerminal != nil {
			d.OnTerminal(*report, err)
		}
	}
	defer func() {
		if detached {
			return
		}
		publishTerminal(&report, changes, err)
	}()

	// Teardown a detached run must NOT take with it: the terrarium it is still
	// growing in, the worktree it is still writing to, and the host workspace
	// state it still owns. Collected here rather than deferred one by one so
	// the goroutine that outlives a detached call runs exactly the same
	// sequence, in the same order, once the work has actually ended.
	var teardown []func()
	var teardownErr error
	runTeardown := func() {
		for index := len(teardown) - 1; index >= 0; index-- {
			teardown[index]()
		}
	}
	defer func() {
		if detached {
			return
		}
		runTeardown()
		err = errors.Join(err, teardownErr)
	}()

	// The mind is resolved before anything is built, so every report this
	// function produces names what carried the run — including the reports for
	// runs that failed before they ever reached the model, so the record of a
	// run can be checked against what the provider billed for.
	//
	// Resolving is not the same as refusing on it. The refusal waits until the
	// substrate has been resolved to a real workspace, because a run can be
	// misconfigured in two ways at once and the operator needs the more
	// specific answer: an installation with no provider key AND a missing
	// checkout was told only that no provider was configured, which sends
	// somebody to fix the wrong thing. Substrate resolution reads; it spends
	// nothing that a failure here would waste.
	mind := d.resolveLLMClient()
	report.Provider = mind.Provider()
	report.Model = mind.Model()
	// refuseUnresolvedMind stops a run that has no model to call. It is invoked
	// on each substrate path once that path has a workspace and before that
	// path mutates anything, so the terrarium, the shadow worktree, the host
	// stash and the isolation branch all stay on the far side of it.
	refuseUnresolvedMind := func() error { return mind.ResolutionError() }

	substratesConfig, err := LoadSubstratesConfig("")
	if err != nil {
		return report, err
	}

	plan, err := resolveSubstrateExecutionPlan(d, substratesConfig)
	if err != nil {
		return report, err
	}

	if err := runSproutPreflightChecksFn(ctx); err != nil {
		return report, err
	}

	// The caller's own context, kept before the growth budget narrows ctx. The
	// growth budget bounds how long the STEM WAITS, so the work hangs off the
	// caller's context instead and outlives the wait — while a caller that
	// cancels still reaches the work, which is the difference between a run
	// nobody is waiting for and a run nobody wants.
	callerCtx := ctx

	// The configured patience bounds the WAIT by bounding the CONTEXT this
	// function blocks on. context.WithTimeoutCause never extends an existing
	// parent deadline, so a caller that already set a tighter budget still
	// governs — and the cause records whose clock expired, because only the
	// growth budget's own expiry may detach: an inherited deadline ends the
	// work too, and reporting that as "still growing" would be a lie about a
	// run that has stopped. cleanupCtx above is taken from the unbounded
	// parent, so teardown still runs once the budget is spent.
	if plan.growthBudget > 0 {
		var cancelGrowth context.CancelFunc
		ctx, cancelGrowth = context.WithTimeoutCause(ctx, plan.growthBudget, errGrowthBudgetSpent)
		defer cancelGrowth()
	}

	if plan.readOnly {
		fmt.Fprintln(os.Stderr, "⚠️ Substrate is configured as READONLY. Discarding terrarium modifications.")
	}

	sourcePath := plan.hostPath
	mountPath := sourcePath
	statusPath := strings.TrimSpace(d.StatusPath)
	gitRepo := false
	var hostStashed bool
	var hostRestorePath string
	var cleanup func()
	var managedRun bool
	var managedWorkspace RunWorkspace
	var managedWorkspaceAllocated bool
	var generatedState *runWorkspaceGeneratedState
	var managedCacheStates []runWorkspaceCacheState
	cleanupManagedWorkspace := func() {
		if !managedWorkspaceAllocated {
			return
		}
		if generatedState != nil {
			if generatedErr := generatedState.cleanup(); generatedErr != nil {
				teardownErr = errors.Join(teardownErr, generatedErr)
				return
			}
		}
		for _, cacheState := range managedCacheStates {
			if cacheErr := cacheState.cleanup(); cacheErr != nil {
				teardownErr = errors.Join(teardownErr, cacheErr)
				return
			}
		}
		if workspaceErr := managedWorkspace.Cleanup(cleanupCtx, plan.credential); workspaceErr != nil {
			teardownErr = errors.Join(teardownErr, workspaceErr)
		}
	}

	teardown = append(teardown, func() {
		if !hostStashed || strings.TrimSpace(hostRestorePath) == "" {
			return
		}
		if restoreErr := restoreHostStashFn(cleanupCtx, hostRestorePath); restoreErr != nil {
			teardownErr = errors.Join(teardownErr, restoreErr)
		}
	})
	extraEnv := make([]string, 0, 2)
	if plan.readOnly || d.Investigation {
		extraEnv = append(extraEnv, "TENDRIL_READONLY=true")
	}
	if plan.credential.ExposeToken && strings.TrimSpace(plan.credential.TokenValue) != "" {
		extraEnv = append(extraEnv, gitHubTokenEnv+"="+plan.credential.TokenValue, gitHubPATLegacyEnv+"="+plan.credential.TokenValue)
	}
	allocateManagedWorkspace := func() error {
		startCommit, err := resolveManagedRunStartCommit(ctx, sourcePath)
		if err != nil {
			return err
		}
		managedWorkspace, err = createRunWorkspaceFn(ctx, sourcePath, stepID, startCommit)
		if err != nil {
			return err
		}
		managedWorkspaceAllocated = true
		mountPath = managedWorkspace.Path
		cleanup = cleanupManagedWorkspace
		generatedState, err = newRunWorkspaceGeneratedState(mountPath)
		if err != nil {
			return err
		}
		cachePaths, copyErr := copyMycorrhizalCacheFn(ctx, sourcePath, mountPath)
		for _, cachePath := range cachePaths {
			cacheState, stateErr := newRunWorkspaceCacheState(mountPath, cachePath)
			if stateErr != nil {
				return stateErr
			}
			managedCacheStates = append(managedCacheStates, cacheState)
		}
		if copyErr != nil {
			return copyErr
		}
		return nil
	}

	if plan.remoteClone {
		clonedPath, persistent, err := cloneNamedForeignSubstrate(plan.name, plan.cloneURL, plan.cloneBranch, plan.credential)
		if err != nil {
			return report, err
		}

		sourcePath = clonedPath
		mountPath = clonedPath
		statusPath = ""
		gitRepo = isGitRepo(sourcePath)
		// Ephemeral checkouts are removed after the run; managed/path checkouts
		// persist (they are Tendril-owned or user-chosen and refreshed on reuse).
		if !persistent {
			cleanup = func() {
				_ = os.RemoveAll(clonedPath)
			}
		}
		if !gitRepo {
			if cleanup != nil {
				cleanup()
			}
			return report, fmt.Errorf("cloned substrate %s is not a git repository", clonedPath)
		}

		fmt.Fprintf(os.Stderr, "🍄 Cross-pollinated foreign Substrate: %s\n", plan.cloneURL)

		// The checkout resolved, so an unresolvable mind is now the run's most
		// specific failure and is reported as one. An absent managed checkout
		// has already returned above, which is the whole point of the ordering.
		if err := refuseUnresolvedMind(); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return report, err
		}
		managedRun = isWritableManagedRun(sourcePath, plan, d.Investigation)
		if managedRun {
			if err := allocateManagedWorkspace(); err != nil {
				if cleanup != nil {
					cleanup()
				}
				return report, err
			}
		}
	} else {
		sourcePath = repoRoot(sourcePath)
		gitRepo = isGitRepo(sourcePath)
		managedRun = gitRepo && isWritableManagedRun(sourcePath, plan, d.Investigation)

		// Before the isolation branch, the host stash and the shadow worktree,
		// all of which are below this line.
		if err := refuseUnresolvedMind(); err != nil {
			return report, err
		}

		if statusPath != "" && !filepath.IsAbs(statusPath) {
			statusPath = filepath.Join(sourcePath, statusPath)
		}

		if gitRepo && statusPath != "" {
			if existing, err := loadSproutStatus(statusPath); err != nil {
				return report, err
			} else if existing != nil && strings.TrimSpace(existing.StepID) == stepID {
				// A timed-out status deliberately falls through to a fresh run:
				// the previous attempt was cut off, not judged, so retrying is
				// the honest resumption. Failed stays a halt — that run was
				// judged and recorded.
				switch strings.ToLower(strings.TrimSpace(existing.Status)) {
				case SproutOutcomeComplete, SproutOutcomeNoChanges:
					message := fmt.Sprintf("Step %s already completed. Skipping.", stepID)
					fmt.Fprintln(os.Stderr, message)
					report.Output = message
					report.Outcome = SproutOutcomeSkipped
					return report, nil
				case SproutOutcomeFailed:
					errText := strings.TrimSpace(existing.Error)
					if errText == "" {
						errText = "previous execution failed"
					}
					fmt.Fprintf(os.Stderr, "⚠️ Resumption halted for %s: %s\n", stepID, errText)
					return report, fmt.Errorf("step %s previously failed: %s", stepID, errText)
				}
			}
		}

		if gitRepo && !managedRun && !plan.readOnly && !d.DisableMergeBack {
			branchOutput, err := runGitCommand(ctx, sourcePath, "rev-parse", "--abbrev-ref", "HEAD")
			if err == nil {
				currentBranch := strings.TrimSpace(branchOutput)
				// The protected branch is RESOLVED, never assumed. This check
				// previously compared against two hard-coded names, so a
				// repository whose default branch was anything else silently
				// received no protection at all. No credential is required:
				// the resolver falls back to the local record of the remote's
				// head, and to the protected-name floor if even that is
				// missing — so protection can widen here, never narrow.
				defaultBranch := ResolveDefaultBranchLocal(ctx, sourcePath, "")
				if defaultBranch.IsProtected(currentBranch) {
					newBranch := fmt.Sprintf("sprout/task-%s", stepID)
					fmt.Fprintf(os.Stderr, "🛡️  Branch Protection: Auto-branching from %s to %s\n", currentBranch, newBranch)
					baseCommit := ""
					if out, revErr := runGitCommand(ctx, sourcePath, "rev-parse", "HEAD"); revErr == nil {
						baseCommit = strings.TrimSpace(out)
					}
					if _, err := runGitCommand(ctx, sourcePath, "checkout", "-b", newBranch); err != nil {
						return report, fmt.Errorf("branch protection failed: could not create isolation branch %s: %w", newBranch, err)
					}
					// Registered at creation, so this branch has a moment at
					// which it can be declared finished. Without that, a run
					// that produces nothing leaves a branch behind forever —
					// and one that was never pushed can never be cleaned up
					// afterwards, because no remote can vouch for it.
					if registerErr := RegisterOwnedRef(OwnedRef{
						Repository: sourcePath,
						Branch:     newBranch,
						Purpose:    PurposeSproutIsolation,
						Base:       baseCommit,
					}); registerErr != nil {
						fmt.Fprintf(os.Stderr, "⚠️ Could not record ownership of %s: %v\n", newBranch, registerErr)
					}
					// The branch gets the reclamation its worktree already has:
					// a run that produced no commits takes its protective
					// branch with it when it leaves, returning the workspace
					// to the branch it started on. A run that produced commits
					// keeps both — that branch is the work.
					returnTo := currentBranch
					teardown = append(teardown, func() {
						if ReclaimUnusedIsolationBranch(cleanupCtx, sourcePath, newBranch, returnTo, plan.credential) {
							fmt.Fprintf(os.Stderr, "🧹 Reclaimed empty isolation branch %s; back on %s\n", newBranch, returnTo)
						}
					})
				}
			}
			hostStashed, err = stashHostWorkspaceFn(ctx, sourcePath, stepID)
			if err != nil {
				return report, err
			}
			if hostStashed {
				hostRestorePath = sourcePath
			}
		} else if statusPath != "" {
			fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Tendril state externalization is disabled.\n", sourcePath)
		}

		if gitRepo {
			if managedRun {
				if err := allocateManagedWorkspace(); err != nil {
					if cleanup != nil {
						cleanup()
					}
					return report, err
				}
			} else if isManagedCheckoutPath(sourcePath) {
				// Read-only managed execution keeps the existing direct mount. A
				// writable managed execution is handled above by a run workspace.
				mountPath = sourcePath
			} else {
				shadowPath, err := createShadowWorktreeFn(sourcePath, plan.cloneBranch)
				if err == nil {
					mountPath = shadowPath
					injectMycorrhizalCacheFn(sourcePath, shadowPath)
					cleanup = func() {
						removeShadowWorktreeFn(sourcePath, shadowPath)
					}
				} else if allowHostWorkspace() {
					fmt.Fprintf(os.Stderr, "⚠️  Failed to create shadow worktree: %v. Using active workspace (%s).\n", err, EnvAllowHostWorkspace)
					if d.EventBus != nil {
						d.EventBus.Publish(eventbus.Event{
							Type:      eventbus.EventHostExecutionActivated,
							Source:    stepID,
							SessionID: d.SessionID,
							Data: map[string]interface{}{
								"workspace": sourcePath,
								"stepId":    stepID,
							},
						})
					}
				} else {
					return report, fmt.Errorf("isolation could not be established (create shadow worktree: %w); set %s=true to run in the active workspace", err, EnvAllowHostWorkspace)
				}
			}
		} else {
			fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Shadow Git terrariuming disabled.\n", sourcePath)
		}
	}

	if d.Genotype != "" {
		if err := stagePlasmidsForGenotype(sourcePath, mountPath, d.Genotype); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return report, err
		}
	}

	repoMapMarkdown, err := generateRepoMapFn(ctx, mountPath)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return report, fmt.Errorf("generate repo map: %w", err)
	}

	repoMapPath := filepath.Join(mountPath, tendrilStateDirectory, "genome", repositoryMapFile)
	if err := os.MkdirAll(filepath.Dir(repoMapPath), 0o755); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return report, fmt.Errorf("create repo map directory: %w", err)
	}
	if err := os.WriteFile(repoMapPath, []byte(repoMapMarkdown), 0o644); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return report, fmt.Errorf("write repo map plasmid: %w", err)
	}

	memoryMapMarkdown, memErr := generateMemoryMapFn(ctx, mountPath)
	if memErr == nil && memoryMapMarkdown != "" {
		memoryMapPath := filepath.Join(mountPath, ".tendril", "genome", memoryMapFile)
		_ = os.WriteFile(memoryMapPath, []byte(memoryMapMarkdown), 0o644)
	}
	if generatedState != nil {
		if err := generatedState.captureInitialAfter(); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return report, err
		}
	}

	imageName := d.resolveImageName(mountPath)
	if err := ensureSproutImageFn(ctx, imageName); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return report, err
	}

	// Use the substrate-configured provider if set, otherwise fall back to env/default.
	providerName := resolveTerrariumProviderName(ctx, d)
	if plan.provider != "" {
		providerName = plan.provider
	}

	// Discover provider authentication rejection on the host, via Roots,
	// before emergence is declared and before a Terrarium exists. A later
	// mid-run 401 still uses the existing classification path.
	if err := applyProviderAuthPreflight(ctx, mind, &report); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return report, err
	}

	publishSproutEmerged(d.EventBus, stepID, d.SessionID, d.Substrate)

	obs := terrarium.ActivationObserver(func(name string) {
		if d.EventBus != nil {
			d.EventBus.Publish(eventbus.Event{
				Type:      eventbus.EventHostExecutionActivated,
				Source:    stepID,
				SessionID: d.SessionID,
				Data: map[string]interface{}{
					"provider": name,
					"stepId":   stepID,
				},
			})
		}
	})

	// The terrarium belongs to the WORK, not to the wait, so it is created on
	// the work's own context: the container process lives for as long as that
	// context does, and starting it on the growth budget would kill the
	// container the moment the Stem stopped waiting — a kill wearing the word
	// detach. The reaper, when configured, is the wall clock behind the work,
	// and the terrarium watchdog is derived from it for the same reason it
	// always was: the context expires first, the watchdog is the backstop.
	workCtx, releaseWork := newSproutWorkContext(callerCtx, plan.reapBudget)
	defer func() {
		if detached {
			return
		}
		releaseWork(nil)
	}()

	if err := assertTerrariumBindMountSource(mountPath); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return report, err
	}

	session, err := startTerrariumSessionFn(workCtx, providerName, imageName, mountPath, d.Investigation, plan.command, extraEnv, deriveWatchdogTimeout(workCtx), obs)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return report, err
	}
	if cleanup != nil {
		teardown = append(teardown, cleanup)
	}
	teardown = append(teardown, func() { _ = session.Close() })

	sprout, err := newSproutFn(workCtx, mountPath, sourcePath, d.Genotype, mind, session, d.EventBus, stepID, d.SessionID)
	if err != nil {
		return report, err
	}

	// The dormancy watcher runs on the WORK's context and is stopped by the
	// teardown sequence rather than by this function returning. A detached call
	// returns while the Sprout is still growing, and a watcher torn down at that
	// moment would stop observing precisely the run nobody else is waiting on —
	// the one we hold the least evidence about.
	var dormancyInspector terrariumInspector
	if insp, ok := session.(terrariumInspector); ok {
		dormancyInspector = insp
	}
	var dormancySnapshot sproutSnapshot
	if snap, ok := sprout.(sproutSnapshot); ok {
		dormancySnapshot = snap
	}
	teardown = append(teardown, watchDormancy(workCtx, d.EventBus, plan.scratchInterval, mountPath, dormancyInspector, dormancySnapshot))

	// completeRun measures, classifies and records what the run did. It is a
	// closure rather than the tail of this function because a detached call
	// reaches it from the goroutine below, long after RunSprout returned — so
	// it owns every value it touches and shares nothing with the caller's
	// report, which by then belongs to somebody else.
	completeRun := func(sproutResult sproutResult, runErr error) (report SproutRunReport, changes changeEvidence, err error) {
		// Everything here runs on its own clock. The work's context may be
		// spent — that is the ordinary way a run ends — and handing the
		// post-mortem an already-expired context makes the clock that ended the
		// run also destroy the account of it. cleanupCtx carries no deadline
		// (it is taken from the unbounded parent above every budget), so the
		// post-mortem bound below is its own, and finite.
		postMortemCtx, cancelPostMortem := context.WithTimeout(cleanupCtx, sproutPostMortemBudget)
		defer cancelPostMortem()

		// Recorded once, before any of the exits below. A run that failed is
		// the one whose record gets asked whether the carrying protocol was to
		// blame, and the paths that end early — a non-git or readonly
		// substrate, an investigation, a run that errored — are the ones most
		// likely to be reading a report assembled by an exit that forgot.
		report.Protocol = sproutResult.Protocol

		// Usage is the Sprout execution component, carried whether the run
		// failed or succeeded. RequestsMade is the occurrence fact, so an
		// all-nil Usage still records that provider requests happened.
		report.Usage = sproutResult.Usage
		report.RequestsMade = sproutResult.RequestsMade
		report.ToolInvocations = sproutResult.ToolInvocations

		// This report is built fresh, not inherited from the caller's — a
		// detached run reaches here long after RunSprout returned — so the
		// resolved mind is restated on it. Without this, every detached run
		// records a null model, which is precisely the population of runs
		// nobody watched and the account matters most for.
		report.Provider = mind.Provider()
		report.Model = mind.Model()

		// Recorded before the exits below for the same reason as the protocol:
		// what the model asked the terrarium to do is known the moment the turn
		// ends, and every path out of here is asked whether the run did any
		// work.
		changes.modelWrote = sproutResult.WroteWorkspace

		if err := session.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Sprout session shutdown issue: %v\n", err)
		}

		// Non-git and readonly substrates cannot measure what changed, so their
		// successful runs report SproutOutcomeComplete with FilesModified unknown
		// (nil) rather than claiming a no-changes verdict nothing measured.
		if !gitRepo || plan.readOnly || d.Investigation {
			if runErr != nil {
				return report, changes, runErr
			}
			if d.Investigation && statusPath != "" {
				executionStatus := sproutExecutionStatus{
					StepID:          stepID,
					Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
					Status:          classifySproutOutcome(runErr, changes, sproutResult.Response, d.Investigation),
					FilesUnmeasured: "run was investigation-only and therefore took no diff",
				}
				if writeErr := writeSproutStatus(statusPath, executionStatus); writeErr != nil {
					fmt.Fprintf(os.Stderr, "⚠️ Failed to write investigation status to %s: %v\n", statusPath, writeErr)
				}
			}
			report.Output = sproutResult.Response
			return report, changes, nil
		}

		var statusRelPath string
		if statusPath != "" {
			var err error
			statusRelPath, err = workspaceRelativePath(sourcePath, statusPath)
			if err != nil {
				return report, changes, err
			}
		}

		// A measurement that fails is not a run that failed. Losing the file list
		// costs the evidence behind the verdict; returning here would additionally
		// lose the verdict, the status file, and the run's own error — replaced by
		// the measurement's. Record why the evidence is missing and carry on.
		measuredFiles, diffErr := collectStageableFilesFn(postMortemCtx, mountPath, statusRelPath)
		if diffErr != nil {
			report.FilesUnmeasured = diffErr.Error()
			fmt.Fprintf(os.Stderr, "⚠️ Could not measure the files this run changed: %v\n", diffErr)
		} else {
			changes.measured = true
			changes.measuredFiles = measuredFiles
		}

		// The diff of the mount is not the run's change set. OpenTendril wrote
		// into this workspace before the run and writes into it again after,
		// and a previous run's leavings are still here on a checkout that
		// persists — so a model that only read files finds a diff waiting for
		// it. What the run is answerable for is what the model did.
		modifiedFiles := changes.attributedFiles()
		report.FilesModified = modifiedFiles

		gitDiff, diffErr := collectGitDiffFn(postMortemCtx, mountPath)
		if diffErr != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to collect git diff for epigenetic chronicler: %v\n", diffErr)
		}

		executionStatus := sproutExecutionStatus{
			StepID:          stepID,
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			FilesModified:   modifiedFiles,
			FilesUnmeasured: report.FilesUnmeasured,
			Status:          classifySproutOutcome(runErr, changes, sproutResult.Response, d.Investigation),
		}
		if runErr != nil {
			executionStatus.Error = runErr.Error()
		}
		report.Outcome = executionStatus.Status

		if statusPath != "" {
			writeStatus := func() error {
				return writeSproutStatus(statusPath, executionStatus)
			}
			writeErr := writeStatus()
			if writeErr != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to write status to %s: %v\n", statusPath, writeErr)
			}
		}

		isReviewableFruit := executionStatus.Status == SproutOutcomeComplete &&
			len(modifiedFiles) > 0 &&
			runErr == nil &&
			!plan.readOnly && !d.Investigation &&
			changes.measured && report.FilesUnmeasured == ""

		if isReviewableFruit {
			var commitHash string
			var commitErr error
			apiCommit := managedRun && plan.remoteClone && plan.credential.CommitMode == CommitModeAPI
			var apiCommitOID string

			if apiCommit {
				apiCommitOID, commitErr = publishManagedAPIFruitFn(postMortemCtx, mountPath, executionStatus, taskPrompt, plan, managedWorkspace)
				commitHash = apiCommitOID
			} else {
				commitHash, commitErr = commitTerrariumExecutionFn(postMortemCtx, mountPath, sourcePath, "", executionStatus, taskPrompt, plan.credential)
			}
			if commitErr != nil {
				report.Outcome = ""
				if runErr != nil {
					return report, changes, errors.Join(runErr, commitErr)
				}
				return report, changes, commitErr
			}

			// Record Fruit identity immediately after the commit is created,
			// before any push or merge. This means failure to publish does
			// not erase the identity of work that is already committed.
			if managedRun {
				report.FruitBranch = managedWorkspace.Branch
				report.FruitCommit = strings.TrimSpace(commitHash)
			}

			if d.DisableMergeBack || (managedRun && !plan.remoteClone) {
				report.Output = sproutResult.Response
				return report, changes, runErr
			}

			if plan.remoteClone {
				// For a managed remote run, publication targets the run-specific
				// Fruit branch (managedWorkspace.Branch), never the configured
				// source branch. The source branch is the STARTING POINT, not
				// the target. allowDefaultBranchCommit must not redirect managed
				// Fruit onto the default branch — the isolation is structural,
				// not dependent on the protected-branch detection.
				var pushErr error
				if apiCommit {
					pushErr = managedWorkspace.ReconcilePublishedFruit(postMortemCtx, apiCommitOID)
				} else if managedRun {
					pushErr = pushTerrariumCommitFn(postMortemCtx, mountPath, managedWorkspace.Branch, plan.credential, false, stepID)
				} else {
					pushErr = pushTerrariumCommitFn(postMortemCtx, mountPath, plan.cloneBranch, plan.credential, plan.allowDefaultBranchCommit, stepID)
				}
				if pushErr != nil {
					report.Outcome = ""
					if runErr != nil {
						return report, changes, errors.Join(runErr, pushErr)
					}
					return report, changes, pushErr
				}
			} else {
				mergeErr := mergeTerrariumCommitFn(postMortemCtx, sourcePath, commitHash)
				if mergeErr != nil {
					report.Outcome = ""
					if runErr != nil {
						return report, changes, errors.Join(runErr, mergeErr)
					}
					return report, changes, mergeErr
				}
			}

			if runErr != nil {
				return report, changes, runErr
			}

			if gitDiff != "" {
				// On the post-mortem's clock, not the wait's: transcribing what a
				// run learned is part of the account of the run, and a detached
				// call has long since let go of the context it waited on.
				chroniclerPath := sourcePath
				if managedRun {
					chroniclerPath = mountPath
				}
				chronicler := newRunChroniclerFn(chroniclerPath, llm.TierCheapest)
				var postRun PostRunUsage
				transcribe := func() error {
					var transcribeErr error
					postRun, transcribeErr = chronicler.TranscribeLearnings(postMortemCtx, sproutResult.Transcript, gitDiff, session.Logs())
					return transcribeErr
				}
				var transcribeErr error
				if generatedState != nil && managedRun {
					transcribeErr = generatedState.captureTransition(transcribe)
				} else {
					transcribeErr = transcribe()
				}
				report.PostRun = postRun
				if transcribeErr != nil {
					fmt.Fprintf(os.Stderr, "⚠️ Epigenetic chronicler skipped: %v\n", transcribeErr)
				}
			}
		} else {
			if runErr != nil {
				return report, changes, runErr
			}
		}

		report.Output = sproutResult.Response
		return report, changes, nil
	}

	// The Sprout turn runs on the work's clock, in its own goroutine, so this
	// function can stop waiting without stopping it. That separation is the
	// whole of the change: patience.growth bounds attention, and attention
	// running out has never been evidence that work has stopped.
	turns := make(chan sproutTurn, 1)
	go func() {
		result, runErr := sprout.Run(workCtx, taskPrompt)
		turns <- sproutTurn{result: result, err: runErr}
	}()

	var turn sproutTurn
	select {
	case turn = <-turns:
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errGrowthBudgetSpent) && !d.AwaitsRunEnding {
			// The Stem stops waiting; the Sprout keeps growing. Nothing here
			// closes the session, removes the worktree or restores the host
			// stash — the run still owns all three — and no terminal event is
			// published, because the run has not ended. The goroutine below
			// finishes the job when the work does.
			//
			// An awaiting caller (one-shot CLI/MCP) cannot carry the work
			// after this return: it will shut down its bus and history store.
			// That path falls through and ends the work as timed-out.
			detached = true
			publishSproutDetached(d.EventBus, stepID, d.SessionID, plan.growthBudget)
			fmt.Fprintf(os.Stderr, "🌿 Growth budget %s spent; detaching from %s. The Sprout keeps growing.\n", plan.growthBudget, stepID)
			go func() {
				finished := <-turns
				// Named before the work's context is released: the cause is
				// what says which clock ended the run, and releasing overwrites
				// it with a bare cancellation.
				runErr := attributeSproutEnding(workCtx, finished.err)
				detachedReport, detachedChanges, detachedErr := completeRun(finished.result, runErr)
				releaseWork(nil)
				runTeardown()
				publishTerminal(&detachedReport, detachedChanges, errors.Join(detachedErr, teardownErr))
			}()
			report.Outcome = SproutOutcomeDetached
			return report, nil
		}
		// Not the growth budget: the caller's own deadline expired or the
		// caller cancelled, and nothing else will carry the work on. End it
		// here and account for it here — detaching would report a run as still
		// growing when nothing is growing it.
		releaseWork(context.Cause(ctx))
		turn = <-turns
	}

	report, changes, err = completeRun(turn.result, attributeSproutEnding(workCtx, turn.err))
	return report, err
}

// sproutTurn is one completed Sprout loop, carried off the goroutine that ran
// it. It exists so the turn can outlive the wait for it.
type sproutTurn struct {
	result sproutResult
	err    error
}

func (d *DockerOrchestrator) resolveImageName(workspace string) string {
	if trimmed := strings.TrimSpace(d.ImageName); trimmed != "" {
		return trimmed
	}

	// A go.mod at the workspace root is the definitive marker of a Go module —
	// the same role package.json plays for node — so it must win before the
	// extension heuristics below. Otherwise a Go-primary repo that carries a
	// TypeScript subtree (e.g. a ui/ front-end) resolves to the toolchain-less
	// typescript image and every `go build`/`go test` step fails.
	if _, err := os.Stat(filepath.Join(workspace, "go.mod")); err == nil {
		return "opentendril-go:latest"
	}

	if _, err := os.Stat(filepath.Join(workspace, "package.json")); err == nil {
		return "opentendril-node:latest"
	}

	if workspaceHasExtension(workspace, ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx") {
		return "opentendril-typescript:latest"
	}

	if workspaceHasExtension(workspace, ".py") {
		return "opentendril-python:latest"
	}

	return "opentendril-go:latest"
}

func ensureSproutImage(ctx context.Context, imageName string) error {
	// An image already present needs no build inputs, so it must not need them
	// to be resolvable either. Asking first also keeps the common case free of
	// the materialisation below.
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", imageName).Run(); err == nil {
		return nil
	}

	contextPath, dockerfilePath := sproutBuildLayout(imageName)
	if contextPath == "" || dockerfilePath == "" {
		return nil
	}

	root, cleanup, err := materializeSproutBuildInputsFn()
	if err != nil {
		return err
	}
	defer cleanup()

	buildContext := filepath.Join(root, filepath.FromSlash(contextPath))
	dockerfile := filepath.Join(root, filepath.FromSlash(dockerfilePath))

	fmt.Fprintf(os.Stderr, "🧱 Building %s from %s\n", imageName, dockerfilePath)
	args := []string{"build", "-f", dockerfile, "-t", imageName, buildContext}
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build %s failed: %w (output: %s)", imageName, err, strings.TrimSpace(string(output)))
	}

	return nil
}

// sproutBuildLayout names where an image's build context and Dockerfile sit
// within the embedded build inputs, as slash-separated paths relative to their
// root. "." is the root itself. Both are empty for an image the Stem does not
// build.
//
// It is deliberately a pure function of the name: nothing about which image
// maps to which Dockerfile depends on the filesystem, and keeping it that way
// is what lets the presence check above run without touching the disk.
func sproutBuildLayout(imageName string) (string, string) {
	switch imageName {
	case "opentendril-go:latest":
		return ".", "sprouts/go/Dockerfile"
	case macrophageFuzzImage:
		return ".", "toolchains/go-fuzz/Dockerfile"
	case verifierImage:
		return ".", "toolchains/go-verifier/Dockerfile"
	case "opentendril-typescript:latest":
		return ".", "sprouts/typescript/Dockerfile"
	case "opentendril-node:latest":
		return ".", "sprouts/node/Dockerfile"
	case "opentendril-python:latest":
		// The Python image copies its inputs relative to its own directory
		// rather than the root, so its context is that directory.
		return "sprouts/python", "sprouts/python/Dockerfile"
	default:
		return "", ""
	}
}

// materializeSproutBuildInputs writes the embedded build inputs into a
// temporary directory and returns its root, laid out at the paths the
// Dockerfiles expect. The caller must invoke cleanup.
//
// The directory is what the build context used to be taken from a repository
// checkout for. Writing it fresh per build keeps the inputs the binary's own,
// rather than whatever happens to be on disk near it.
func materializeSproutBuildInputs() (string, func(), error) {
	root, err := os.MkdirTemp("", "opentendril-sprout-build-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create sprout build inputs directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }

	walkErr := fs.WalkDir(opentendril.SproutBuildInputs, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		payload, readErr := opentendril.SproutBuildInputs.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
			return mkdirErr
		}
		// 0o644 throughout: every Dockerfile that needs a file executable sets
		// that itself, and an embedded file carries no mode of its own.
		return os.WriteFile(target, payload, 0o644)
	})
	if walkErr != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write sprout build inputs: %w", walkErr)
	}

	return root, cleanup, nil
}

func workspaceHasExtension(workspace string, extensions ...string) bool {
	extensionSet := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		extensionSet[strings.ToLower(extension)] = struct{}{}
	}

	ignoredDirs := map[string]struct{}{
		".git":         {},
		".tendril":     {},
		"tendrils":     {},
		"sprouts":      {},
		"static":       {},
		"scripts":      {},
		"node_modules": {},
		"vendor":       {},
		".venv":        {},
		"venv":         {},
		"dist":         {},
		"build":        {},
		"__pycache__":  {},
	}

	found := false
	_ = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if found {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			if _, ok := ignoredDirs[entry.Name()]; ok && path != workspace {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := extensionSet[strings.ToLower(filepath.Ext(entry.Name()))]; ok {
			found = true
			return filepath.SkipDir
		}
		return nil
	})

	return found
}

type terrariumToolSession struct {
	terrarium terrarium.Terrarium
}

// terrariumInspector is the narrow surface watchDormancy uses to observe a
// live Terrarium without touching the tool protocol. It is a separate interface
// — not a widened toolSession — so the capture path is never confused with the
// model's conversation channel.
type terrariumInspector interface {
	// Logs returns the container's accumulated stderr. It is a snapshot, not a
	// stream, so it is safe to call while the model turn is in flight.
	Logs() string
	// ProcessListing returns a best-effort snapshot of the container's running
	// processes, taken at the Terrarium level rather than through the tool
	// protocol. The listing may be unavailable if the container has already
	// stopped; callers must treat a non-nil error as a first-class answer.
	ProcessListing(ctx context.Context) (string, error)
}

// sproutSnapshot is the narrow surface watchDormancy uses to read the last
// request and response from the model conversation without being able to write
// to it. It is a separate interface from sproutRunner so the capture path
// cannot accidentally drive the model turn.
type sproutSnapshot interface {
	// LastExchange returns the last request sent to the model and the response
	// received, both as raw strings. Either may be empty if the run has not yet
	// completed a full exchange.
	LastExchange() (request, response string)
}

// deriveWatchdogTimeout computes the terrarium watchdog duration from the
// caller's context. When the context has a deadline, the watchdog is set to
// the remaining time plus a grace margin so the context deadline governs the
// run and the watchdog fires only if the context is not cancelled cleanly.
// When there is no deadline, the fallback constant applies — it is a backstop
// against a hung container, not a policy on how long work may take.
// errGrowthBudgetSpent is the cause recorded when the configured growth budget
// — and only that budget — ends the Stem's wait. Detaching is correct solely
// when something else will still carry the work: a deadline the caller already
// held, or a cancelled parent, ends the work as well, and matching on
// context.DeadlineExceeded alone cannot tell those apart. The cause can.
var errGrowthBudgetSpent = errors.New("growth budget spent; the Stem stopped waiting")

// errReapBudgetSpent is the cause recorded when the orphan reaper's backstop
// clock ends a run. It is what turns a bare cancellation into a named ending,
// so a terrarium stopped for want of anyone waiting is never filed as a run
// that broke.
var errReapBudgetSpent = errors.New("reap budget spent; nothing was waiting on the run")

// newSproutWorkContext derives the context the Sprout turn and its terrarium
// run on. It deliberately does not inherit the growth budget: that budget
// bounds how long the Stem waits, and the work outlives the wait. The reaper,
// when configured, is the wall clock behind the work — the backstop that ends a
// terrarium nothing is waiting on any more.
//
// The returned release must be called exactly once, by whichever goroutine ends
// up owning the run. A non-nil cause names the clock that ended it.
func newSproutWorkContext(parent context.Context, reapBudget time.Duration) (context.Context, context.CancelCauseFunc) {
	if parent == nil {
		parent = context.Background()
	}

	workCtx, cancelWork := context.WithCancelCause(parent)
	if reapBudget <= 0 {
		return workCtx, cancelWork
	}

	reapedCtx, cancelReap := context.WithTimeoutCause(workCtx, reapBudget, errReapBudgetSpent)
	return reapedCtx, func(cause error) {
		cancelReap()
		cancelWork(cause)
	}
}

// attributeSproutEnding names the clock that ended a Sprout turn. The turn
// reports the cancellation it observed, which for a context cancelled on its
// behalf is a bare context.Canceled — true, uninformative, and enough to file
// an expired deadline as an ordinary failure. The context's cause records which
// clock cancelled the work, so the ending is named after that instead.
func attributeSproutEnding(workCtx context.Context, runErr error) error {
	if runErr == nil || workCtx == nil {
		return runErr
	}

	cause := context.Cause(workCtx)
	switch {
	case cause == nil:
		return runErr
	case errors.Is(cause, errReapBudgetSpent):
		return fmt.Errorf("%w: %w", ErrSproutReaped, runErr)
	case errors.Is(cause, context.DeadlineExceeded) && !errors.Is(runErr, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", context.DeadlineExceeded, runErr)
	case errors.Is(cause, errGrowthBudgetSpent):
		return fmt.Errorf("%w: %w", context.DeadlineExceeded, runErr)
	}

	return runErr
}

func deriveWatchdogTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			return remaining + time.Minute
		}
	}
	return terrariumWatchdogFallback
}

// assertTerrariumBindMountSource is the last host-side check before docker
// run -v. A bare name becomes an empty Docker named volume; an unpopulated
// managed checkout becomes an empty /app. Log the source so an operator can
// see which host path the Terrarium actually received.
func assertTerrariumBindMountSource(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("terrarium bind-mount source is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return fmt.Errorf("terrarium bind-mount source %q is not an absolute path; Docker treats a bare name as an empty named volume at /app", trimmed)
	}
	info, err := os.Stat(trimmed)
	if err != nil {
		return fmt.Errorf("terrarium bind-mount source %q: %w", trimmed, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("terrarium bind-mount source %q is not a directory", trimmed)
	}
	if isManagedCheckoutPath(trimmed) && !checkoutHasGitMetadata(trimmed) {
		return fmt.Errorf("managed checkout %q has no .git; refusing to mount an empty /app", trimmed)
	}
	log.Printf("[Terrarium] bind-mount source %s -> /app", trimmed)
	return nil
}

func startTerrariumSession(ctx context.Context, providerName, imageName string, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
	provider, err := terrariumNewProviderFn(ctx, providerName, observers...)
	if err != nil {
		return nil, err
	}

	runAsUser := fmt.Sprintf("%d:%d", osGetuidFn(), osGetgidFn())
	if dockerIsRootlessFn() {
		runAsUser = "0:0"
	}

	instance, err := provider.Create(ctx, terrarium.TerrariumSpec{
		Image:          imageName,
		WorkingDir:     "/app",
		NetworkMode:    terrarium.NetworkModeNone,
		RunAsUser:      runAsUser,
		CPUQuota:       "1.0",
		MemoryLimitMB:  2048,
		ReadOnlyRootFS: false,
		PidsLimit:      512,
		Timeout:        timeout,
		Mounts: []terrarium.MountSpec{
			{
				Source:   mountPath,
				Target:   "/app",
				ReadOnly: readOnly,
			},
		},
		Command:     command,
		Environment: buildTerrariumEnvironment(extraEnv...),
	})
	if err != nil {
		return nil, err
	}

	return &terrariumToolSession{terrarium: instance}, nil
}

// CheckGVisorReadinessFn is a test seam over terrarium.CheckGVisorReadiness.
var CheckGVisorReadinessFn = terrarium.CheckGVisorReadiness

func resolveTerrariumProviderName(ctx context.Context, d *DockerOrchestrator) string {
	if providerName := strings.TrimSpace(os.Getenv(terrariumProviderEnvKey)); providerName != "" {
		return providerName
	}

	if d != nil {
		switch strings.ToLower(strings.TrimSpace(d.Substrate)) {
		case terrarium.ProviderDocker, terrarium.ProviderGVisor, terrarium.ProviderFirecracker, terrarium.ProviderHost:
			return strings.ToLower(strings.TrimSpace(d.Substrate))
		}
	}

	// No explicit preference from the env var or Substrate: prefer gVisor's
	// added syscall-filtering isolation over plain Docker when the host's
	// Docker daemon has the runsc runtime registered, falling back to Docker
	// when it doesn't. An explicit choice above is always honored verbatim
	// and never overridden by this check.
	if CheckGVisorReadinessFn(ctx) == nil {
		return terrarium.ProviderGVisor
	}
	return terrarium.ProviderDocker
}

// TerrariumProviderStatus returns the resolved terrarium provider name, whether it was
// explicitly selected via the environment, and whether the preferred gVisor runtime
// (runsc) is available on the host. This accessor is exported for hardiness reporting.
func TerrariumProviderStatus(ctx context.Context) (resolved string, explicit bool, runscPresent bool) {
	explicit = strings.TrimSpace(os.Getenv(terrariumProviderEnvKey)) != ""
	runscPresent = CheckGVisorReadinessFn(ctx) == nil
	resolved = resolveTerrariumProviderName(ctx, nil)
	return resolved, explicit, runscPresent
}

func (s *terrariumToolSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
	response, err := s.Call(ctx, ToolCall{Tool: "listAvailableTools", Arguments: map[string]any{}})
	if err != nil {
		return nil, err
	}
	if strings.ToLower(strings.TrimSpace(response.Status)) != "success" {
		if strings.TrimSpace(response.Error) != "" {
			return nil, fmt.Errorf("listAvailableTools failed: %s", response.Error)
		}
		return nil, fmt.Errorf("listAvailableTools failed: %v", response.Output)
	}

	return decodeToolDefinitions(response.Output)
}

func (s *terrariumToolSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.terrarium == nil {
		return ToolResponse{}, fmt.Errorf("terrarium session is not active")
	}

	payload, err := json.Marshal(call)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("encode tool call: %w", err)
	}

	result, err := s.terrarium.Run(ctx, terrarium.CommandSpec{Stdin: payload})
	if result.TimedOut {
		return ToolResponse{}, fmt.Errorf("tool call %q was cut off: %w", call.Tool, ErrSproutTimedOut)
	}
	// The terrarium reports a watchdog kill as a timed-out result, not an
	// error. Convert it into the typed sentinel here so the run's outcome can
	// say "cut off" instead of dressing the kill as a decode failure — the
	// exact confusion that once sent a diagnosis chasing a healthy model.
	if err != nil {
		return ToolResponse{}, err
	}

	var response ToolResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &response); err != nil {
		return ToolResponse{}, fmt.Errorf("decode tool response: %w (payload: %s)", err, strings.TrimSpace(result.Stdout))
	}

	return response, nil
}

func (s *terrariumToolSession) Close() error {
	if s == nil || s.terrarium == nil {
		return nil
	}
	return s.terrarium.Stop(context.Background())
}

func (s *terrariumToolSession) Logs() string {
	if s == nil || s.terrarium == nil {
		return ""
	}

	logs, err := s.terrarium.SnapshotLogs(context.Background())
	if err != nil {
		return ""
	}
	return logs.Stderr
}

// ProcessListing returns the container's running processes by running ps(1)
// through the Terrarium's Run channel — not through the model tool protocol.
// This is intentional: the model turn may be blocked inside Call at exactly
// the moment a dormancy capture fires, and injecting anything into that channel
// would put an unsolicited tool result into the model's conversation.
//
// If the listing cannot be taken (container already stopped, ps not present,
// execution timed out) the error is returned as-is; callers record it as
// "could not be taken" rather than treating it as an empty listing.
func (s *terrariumToolSession) ProcessListing(ctx context.Context) (string, error) {
	if s == nil || s.terrarium == nil {
		return "", fmt.Errorf("terrarium is not active")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	const listingTimeout = 10 * time.Second
	listCtx, cancel := context.WithTimeout(ctx, listingTimeout)
	defer cancel()

	result, err := s.terrarium.Run(listCtx, terrarium.CommandSpec{
		Command: []string{"ps", "aux"},
		Timeout: listingTimeout,
	})
	if err != nil {
		return "", fmt.Errorf("ps aux: %w", err)
	}
	if result.TimedOut {
		return "", fmt.Errorf("ps aux timed out after %s", listingTimeout)
	}
	return result.Stdout, nil
}

func buildTerrariumEnvironment(extraEnv ...string) map[string]string {
	values := make(map[string]string)

	for _, key := range []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GOOGLE_API_KEY",
		"GROK_API_KEY",
		"OPENROUTER_API_KEY",
		"NVIDIA_API_KEY",
		"DEFAULT_LLM_PROVIDER",
		"LOCAL_INFERENCE_URL",
		"LOCAL_MODEL_NAME",
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values[key] = value
		}
	}

	for _, entry := range extraEnv {
		key, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		values[key] = value
	}

	return values
}

func decodeToolDefinitions(output any) ([]ToolDefinition, error) {
	if output == nil {
		return nil, nil
	}

	raw, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("marshal tool inventory: %w", err)
	}

	var wrapped struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Tools) > 0 {
		return wrapped.Tools, nil
	}

	var tools []ToolDefinition
	if err := json.Unmarshal(raw, &tools); err == nil && len(tools) > 0 {
		return tools, nil
	}

	var single ToolDefinition
	if err := json.Unmarshal(raw, &single); err == nil && strings.TrimSpace(single.Name) != "" {
		return []ToolDefinition{single}, nil
	}

	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
		return nil, fmt.Errorf("unrecognized tool inventory payload: %s", trimmed)
	}

	return nil, nil
}

func getEnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func runSproutPreflightChecks(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.CommandContext(ctx, "docker", "info")
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = output
		return fmt.Errorf("❌ Docker daemon is not responding. OpenTendril requires Docker to run secure Sprouts.")
	}

	env := buildTerrariumEnvironment()
	if !strings.EqualFold(strings.TrimSpace(env["DEFAULT_LLM_PROVIDER"]), "local") {
		return nil
	}

	inferenceURL := strings.TrimSpace(env["LOCAL_INFERENCE_URL"])
	if inferenceURL == "" {
		inferenceURL = "http://localhost:11434/v1"
	}

	return checkLocalInferenceReachable(ctx, inferenceURL)
}

func checkLocalInferenceReachable(ctx context.Context, inferenceURL string) error {
	checkURL := hostInferenceHealthURL(inferenceURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if isConnectionRefused(err) {
			return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
		}
		return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return nil
	}

	return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
}

func hostInferenceHealthURL(inferenceURL string) string {
	trimmed := strings.TrimSpace(inferenceURL)
	trimmed = strings.ReplaceAll(trimmed, "host.docker.internal", "localhost")

	if strings.HasSuffix(trimmed, "/v1") {
		return strings.TrimSuffix(trimmed, "/v1") + "/api/tags"
	}
	if strings.HasSuffix(trimmed, "/v1/") {
		return strings.TrimSuffix(trimmed, "/v1/") + "/api/tags"
	}

	if strings.Contains(trimmed, "/api/") {
		return trimmed
	}

	return strings.TrimRight(trimmed, "/") + "/api/tags"
}

func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if sysErr, ok := opErr.Err.(syscall.Errno); ok && sysErr == syscall.ECONNREFUSED {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "connection refused")
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

// createShadowWorktree creates a new git worktree in a temporary directory.
func createShadowWorktree(sourcePath, substrateBranch string) (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	runID := hex.EncodeToString(bytes)

	shadowPath := filepath.Join(os.TempDir(), fmt.Sprintf("opentendril-terrarium-%s", runID))

	branch := strings.TrimSpace(substrateBranch)
	var cmd *exec.Cmd
	if branch == "" {
		// Create the worktree pointing to HEAD (or a detached HEAD)
		// We use --detach to avoid checking out the current branch which might be locked
		cmd = exec.Command("git", "worktree", "add", "--detach", shadowPath, "HEAD")
	} else if localBranchExists(sourcePath, branch) {
		cmd = exec.Command("git", "worktree", "add", shadowPath, branch)
	} else {
		cmd = exec.Command("git", "worktree", "add", "-b", branch, shadowPath, "HEAD")
	}
	cmd.Dir = sourcePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add failed: %w, output: %s", err, string(output))
	}

	return shadowPath, nil
}

func localBranchExists(sourcePath, branch string) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false
	}

	ref := "refs/heads/" + strings.TrimPrefix(branch, "refs/heads/")
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Dir = sourcePath
	return cmd.Run() == nil
}

// injectMycorrhizalCache hard-links dependency directories from the host to the shadow terrarium.
func injectMycorrhizalCache(sourcePath, shadowPath string) {
	for _, dir := range mycorrhizalCacheDirs {
		srcDir := filepath.Join(sourcePath, dir)
		if info, err := os.Stat(srcDir); err == nil && info.IsDir() {
			dstDir := filepath.Join(shadowPath, dir)
			// Use cp -rl to recursively hard-link the directory
			cmd := exec.Command("cp", "-rl", srcDir, dstDir)
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to inject mycorrhizal cache %s: %v\n", dir, err)
			} else {
				fmt.Fprintf(os.Stderr, "🍄 Injected Mycorrhizal Cache: %s\n", dir)
			}
		}
	}
}

var mycorrhizalCacheDirs = []string{"node_modules", ".venv", "venv", "vendor"}

// copyMycorrhizalCache copies dependency directories into a managed run
// workspace. Managed workspaces must not use hardlinks: a Sprout mutating a
// hardlinked cache would mutate the persistent managed checkout as well.
func copyMycorrhizalCache(ctx context.Context, sourcePath, runPath string) ([]string, error) {
	copied := make([]string, 0, len(mycorrhizalCacheDirs))
	for _, dir := range mycorrhizalCacheDirs {
		srcDir := filepath.Join(sourcePath, dir)
		info, err := os.Stat(srcDir)
		if err != nil || !info.IsDir() {
			continue
		}
		dstDir := filepath.Join(runPath, dir)
		if err := os.Mkdir(dstDir, 0o755); err != nil {
			if os.IsExist(err) {
				// A linked worktree may already contain a tracked dependency
				// tree. It belongs to Git, so do not place a host cache beneath
				// it or register it for disposable-run cleanup.
				continue
			}
			return copied, fmt.Errorf("claim Mycorrhizal cache %s: %w", dir, err)
		}
		sourceContents := filepath.Clean(srcDir) + string(filepath.Separator) + "."
		cmd := exec.CommandContext(ctx, "cp", "-r", "--", sourceContents, dstDir)
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to copy mycorrhizal cache %s: %v\n", dir, err)
			if _, statErr := os.Lstat(dstDir); statErr == nil {
				copied = append(copied, dstDir)
			}
			return copied, fmt.Errorf("copy Mycorrhizal cache %s: %w", dir, err)
		}
		copied = append(copied, dstDir)
		fmt.Fprintf(os.Stderr, "🍄 Copied Mycorrhizal Cache: %s\n", dir)
	}
	return copied, nil
}

// removeShadowWorktree securely removes the temporary git worktree.
func removeShadowWorktree(sourcePath, shadowPath string) {
	// First tell git to remove the worktree references
	cmd := exec.Command("git", "worktree", "remove", "--force", shadowPath)
	cmd.Dir = sourcePath
	_ = cmd.Run()

	// Ensure the directory is actually gone
	_ = os.RemoveAll(shadowPath)
}

func collectGitDiff(ctx context.Context, mountPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", mountPath, "diff", "--no-color", "--binary")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w (output: %s)", err, string(output))
	}
	return strings.TrimSpace(string(output)), nil
}

type sproutExecutionStatus struct {
	StepID string `json:"stepId"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// FilesUnmeasured explains a null filesModified that a reader would
	// otherwise have to interpret. Absent when the measurement succeeded.
	FilesUnmeasured string   `json:"filesUnmeasured,omitempty"`
	Timestamp       string   `json:"timestamp"`
	FilesModified   []string `json:"filesModified"`
}

func newSproutExecutionID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
}

func stashHostWorkspace(ctx context.Context, root, runID string) (bool, error) {
	statusOutput, err := runGitCommand(ctx, root, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("host pre-flight status check failed: %w", err)
	}
	if strings.TrimSpace(statusOutput) == "" {
		return false, nil
	}

	stashName := fmt.Sprintf("opentendril-host-pre-flight-stash-%s", runID)
	if _, err := runGitCommand(ctx, root, "stash", "save", "-u", stashName); err != nil {
		return false, fmt.Errorf("git stash save failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "🧺 Stashed host workspace as %s\n", stashName)
	return true, nil
}

func restoreHostStash(ctx context.Context, root string) error {
	if _, err := runGitCommand(ctx, root, "stash", "pop"); err != nil {
		return recoverFailedStashPop(ctx, root, err)
	}

	return nil
}

// recoverFailedStashPop salvages the one stash-pop failure a sprout inflicts on
// itself. The epigenetic chronicler regenerates an untracked state file on the
// host during the run (e.g. .tendril/genome/epigenetics.md) that the pre-flight
// stash also captured, so `git stash pop` cannot lay the stashed copy back down
// and fails with "could not restore untracked files from stash" — withering an
// otherwise successful run on self-inflicted state.
//
// Git still does everything that matters before it fails: it applies the
// stash's tracked changes and restores every non-colliding untracked file, then
// leaves only the redundant stash and the regenerated copy in place (verified).
// So when the failure is that untracked-restore conflict and there is no genuine
// tracked merge conflict, drop the stash and let the run finish. A real merge
// conflict is never papered over — it is returned so the run withers honestly.
func recoverFailedStashPop(ctx context.Context, root string, popErr error) error {
	if !strings.Contains(popErr.Error(), "could not restore untracked files") {
		return fmt.Errorf("git stash pop failed: %w", popErr)
	}
	status, statusErr := runGitCommandRawOutput(ctx, root, "status", "--porcelain")
	if statusErr != nil {
		return fmt.Errorf("git stash pop failed: %w (status check also failed: %v)", popErr, statusErr)
	}
	if porcelainHasUnmergedPaths(status) {
		return fmt.Errorf("git stash pop failed with merge conflicts: %w", popErr)
	}
	if _, err := runGitCommand(ctx, root, "stash", "drop"); err != nil {
		return fmt.Errorf("git stash pop recovered the workspace but dropping the redundant stash failed: %w", err)
	}
	fmt.Fprintln(os.Stderr, "🧺 Restored host workspace; kept OpenTendril's regenerated state and dropped the redundant stash.")
	return nil
}

// porcelainHasUnmergedPaths reports whether a `git status --porcelain` listing
// contains an unmerged (conflicted) path, whose two-letter code is one of the
// conflict states. Used to tell a self-inflicted untracked conflict from a
// genuine tracked merge conflict that must not be silently discarded.
func porcelainHasUnmergedPaths(status string) bool {
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[:2] {
		case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
			return true
		}
	}
	return false
}

func loadSproutStatus(path string) (*sproutExecutionStatus, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tendril status %s: %w", path, err)
	}

	var status sproutExecutionStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return nil, fmt.Errorf("decode tendril status %s: %w", path, err)
	}

	return &status, nil
}

func writeSproutStatus(path string, status sproutExecutionStatus) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create tendril status directory: %w", err)
	}

	ensureNotGitVisible(path)

	payload, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tendril status: %w", err)
	}
	payload = append(payload, '\n')

	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write tendril status %s: %w", path, err)
	}

	return nil
}

func workspaceRelativePath(rootPath, targetPath string) (string, error) {
	rootPath = strings.TrimSpace(rootPath)
	targetPath = strings.TrimSpace(targetPath)
	if rootPath == "" || targetPath == "" {
		return "", nil
	}

	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(rootPath, targetPath)
	}

	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace relative path for %s: %w", targetPath, err)
	}

	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s escapes workspace %s", targetPath, rootPath)
	}

	return filepath.ToSlash(rel), nil
}

func collectStageableFiles(ctx context.Context, mountPath string, excludedPaths ...string) ([]string, error) {
	// -uall lists untracked files individually. Without it git collapses an
	// untracked directory to a single entry — `?? .tendril/` — and a filter
	// that reasons about files then has nothing to match, so everything under
	// the directory is staged wholesale. That is how OpenTendril's own index
	// key reached a commit: the filter was asked about a directory it did not
	// recognise rather than the files inside it.
	// -z separates entries with NUL bytes and never quotes paths. The
	// line-oriented format cannot be sliced reliably here: an entry for an
	// unstaged modification starts with a space (" M path"), and the trimmed
	// command output plus per-line trimming shifted every fixed offset one
	// byte into the path — staging then failed on names like
	// "ubstrates.yaml.example".
	output, err := runGitCommandRawOutput(ctx, mountPath, "status", "--porcelain", "-uall", "-z")
	if err != nil {
		return nil, err
	}

	excluded := make(map[string]struct{}, len(excludedPaths))
	for _, path := range excludedPaths {
		normalized := filepath.ToSlash(strings.TrimSpace(path))
		if normalized == "" {
			continue
		}
		excluded[normalized] = struct{}{}
	}

	stageable := make(map[string]struct{})
	include := func(path string) {
		normalized := filepath.ToSlash(path)
		if normalized == "" {
			return
		}
		if _, ok := excluded[normalized]; ok {
			return
		}
		if shouldIgnoreStagePath(normalized) {
			return
		}
		stageable[normalized] = struct{}{}
	}

	entries := strings.Split(output, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		status := entry[:2]
		include(entry[3:])
		// A rename or copy carries the original path as its own following
		// NUL-separated field, without a status prefix. It names a deletion
		// that must be staged too.
		if status[0] == 'R' || status[0] == 'C' {
			i++
			if i < len(entries) {
				include(entries[i])
			}
		}
	}

	if len(stageable) == 0 {
		return []string{}, nil
	}

	files := make([]string, 0, len(stageable))
	for path := range stageable {
		files = append(files, path)
	}
	sort.Strings(files)

	return files, nil
}

func shouldIgnoreStagePath(path string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" {
		return true
	}

	// OpenTendril's own working state, written into the substrate while it
	// indexes it. The change set comes from `git status --porcelain` in the
	// mount, which cannot tell the tool's writes from the Sprout's, so without
	// this they were committed as the Sprout's work and merged back — including
	// the index encryption key, which a push would then publish. This
	// repository ignores .tendril, which is why the path only ever misbehaved
	// against other people's repositories.
	if isGeneratedRuntimeArtifact(normalized) {
		return true
	}

	lowerPath := strings.ToLower(normalized)
	if strings.HasSuffix(lowerPath, ".log") {
		return true
	}

	ignoredSegments := map[string]struct{}{
		".cache":      {},
		"build":       {},
		"dist":        {},
		"tmp":         {},
		"__pycache__": {},
	}

	for _, segment := range strings.Split(normalized, "/") {
		if _, ok := ignoredSegments[strings.ToLower(segment)]; ok {
			return true
		}
	}

	return false
}

func publishManagedAPIFruit(ctx context.Context, mountPath string, executionStatus sproutExecutionStatus, taskPrompt string, plan *substrateExecutionPlan, managedWorkspace RunWorkspace) (string, error) {
	originURL, err := runGitCommand(ctx, managedWorkspace.Repository, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("api fruit publication: resolve origin remote: %w", err)
	}
	originURL = strings.TrimSpace(originURL)
	owner, repo, err := parseOwnerRepo(originURL)
	if err != nil {
		return "", fmt.Errorf("api fruit publication: %w", err)
	}

	token, err := githubAppInstallationToken(ctx, plan.credential.App, originURL)
	if err != nil {
		return "", fmt.Errorf("api fruit publication: github app auth: %w", err)
	}

	err = githubCreateRef(ctx, owner, repo, managedWorkspace.Branch, managedWorkspace.BaseCommit, token)
	if err != nil {
		return "", fmt.Errorf("api fruit publication: create remote branch %s: %w", managedWorkspace.Branch, err)
	}

	additions, deletions, err := apiCommitFileChangesFromWorkspace(ctx, mountPath, executionStatus.FilesModified)
	if err != nil {
		return "", fmt.Errorf("api fruit publication: enumerate changes: %w", err)
	}
	if len(additions) == 0 && len(deletions) == 0 {
		return "", fmt.Errorf("api fruit publication: nothing to commit")
	}

	commitMessage := buildSproutCommitMessage(executionStatus.StepID, taskPrompt, executionStatus.Status, executionStatus.Error)
	headline, body := splitCommitMessage(commitMessage)

	input := createCommitOnBranchInput{
		Branch: apiCommitBranch{
			RepositoryNameWithOwner: owner + "/" + repo,
			BranchName:              managedWorkspace.Branch,
		},
		Message:         apiCommitMessage{Headline: headline, Body: body},
		ExpectedHeadOid: managedWorkspace.BaseCommit,
		FileChanges: apiCommitFileChanges{
			Additions: additions,
			Deletions: deletions,
		},
	}

	var response createCommitOnBranchResponse
	if err := githubGraphQLPost(ctx, token, createCommitOnBranchMutation, map[string]any{"input": input}, &response); err != nil {
		return "", fmt.Errorf("api fruit publication: %w", err)
	}
	oid := strings.TrimSpace(response.CreateCommitOnBranch.Commit.Oid)
	if oid == "" {
		return "", fmt.Errorf("api fruit publication: github returned no commit oid")
	}

	return oid, nil
}

func commitTerrariumExecution(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential) (string, error) {
	if credential.CommitMode == CommitModeAPI {
		return "", fmt.Errorf("sprout git commit refused: the substrate is configured for api commit mode (commit: api), which commits directly to the remote branch, but a Sprout requires a local commit to merge back from its shadow worktree. Remove commit: api from the substrate to use the Sprout local commit path")
	}

	if strings.TrimSpace(credential.Identity.Name) == "" && strings.TrimSpace(credential.Identity.Email) == "" {
		if _, err := runGitCommand(ctx, mountPath, "var", "GIT_COMMITTER_IDENT"); err != nil {
			return "", fmt.Errorf("sprout git commit refused: the substrate has no configured commit identity (set identity name and email in substrates.yaml) and git cannot resolve an ambient identity — an unattributable Sprout commit is never created")
		}
	}

	stagePaths := append([]string{}, executionStatus.FilesModified...)

	if strings.TrimSpace(statusPath) != "" {
		statusRelPath, err := workspaceRelativePath(sourcePath, statusPath)
		if err != nil {
			return "", err
		}

		statusTerrariumPath := filepath.Join(mountPath, filepath.FromSlash(statusRelPath))
		if err := writeSproutStatus(statusTerrariumPath, executionStatus); err != nil {
			return "", err
		}

		stagePaths = append(stagePaths, statusRelPath)
	}

	stageSet := make(map[string]struct{}, len(stagePaths))
	uniqueStagePaths := make([]string, 0, len(stagePaths))
	for _, path := range stagePaths {
		normalized := filepath.ToSlash(strings.TrimSpace(path))
		if normalized == "" {
			continue
		}
		if _, ok := stageSet[normalized]; ok {
			continue
		}
		stageSet[normalized] = struct{}{}
		uniqueStagePaths = append(uniqueStagePaths, normalized)
	}

	if len(uniqueStagePaths) > 0 {
		addArgs := append([]string{"add", "-A", "--"}, uniqueStagePaths...)
		if _, err := runGitCommand(ctx, mountPath, addArgs...); err != nil {
			return "", err
		}
	}

	commitMessage := buildSproutCommitMessage(executionStatus.StepID, taskPrompt, executionStatus.Status, executionStatus.Error)
	// Signing and identity config (`-c ...`) must precede the `commit` subcommand.
	configArgs := append(signingGitConfigArgs(credential.Sign), identityGitConfigArgs(credential.Identity)...)
	commitArgs := append(append([]string{}, configArgs...), "commit", "-m", commitMessage)
	if len(uniqueStagePaths) == 0 {
		commitArgs = append(append([]string{}, configArgs...), "commit", "--allow-empty", "-m", commitMessage)
	}

	if _, err := runGitCommand(ctx, mountPath, commitArgs...); err != nil {
		return "", err
	}

	commitHash, err := runGitCommand(ctx, mountPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}

	return commitHash, nil
}

func mergeTerrariumCommit(ctx context.Context, sourcePath, commitHash string) error {
	// The kernel guard runs before the merge, not after: once a fast-forward
	// lands, the orchestrator's own files have already changed underneath it.
	if err := checkTerrariumCommitPaths(ctx, sourcePath, commitHash); err != nil {
		return err
	}

	if _, err := runGitCommand(ctx, sourcePath, "merge", "--ff-only", commitHash); err != nil {
		return err
	}

	return nil
}

// checkTerrariumCommitPaths refuses a Terrarium commit that would change a
// protected path.
//
// The paths are those the merge would actually bring in — the difference
// between where the checkout stands now and the commit offered — so a Sprout is
// judged on what it changed rather than on what the repository contains.
func checkTerrariumCommitPaths(ctx context.Context, sourcePath, commitHash string) error {
	output, err := runGitCommand(ctx, sourcePath, "diff", "--name-only", "HEAD", commitHash)
	if err != nil {
		// Failing to determine what a commit touches is not permission to merge
		// it: the guard cannot be satisfied by breaking the question.
		return fmt.Errorf("protected-path check could not read the incoming changes: %w", err)
	}

	changed := []string{}
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			changed = append(changed, trimmed)
		}
	}

	return checkProtectedPaths(sourcePath, changed)
}

func runContainerFitnessTest(ctx context.Context, imageName, shadowPath, fitnessTest string) error {
	if strings.TrimSpace(fitnessTest) == "" {
		return nil
	}
	if strings.TrimSpace(imageName) == "" {
		return fmt.Errorf("fitness test image name is empty")
	}
	if strings.TrimSpace(shadowPath) == "" {
		return fmt.Errorf("fitness test shadow path is empty")
	}

	args := []string{
		"run",
		"--rm",
		"-v", fmt.Sprintf("%s:/app", shadowPath),
		"-w", "/app",
		imageName,
		"sh",
		"-c",
		fitnessTest,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker fitness test failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// buildSproutCommitMessage names what is being committed. SproutOutcomeDetached
// is deliberately absent: a detached run has produced no result to commit and
// never reaches this path, because the commit happens after the work ends and a
// detached run has not ended. A reaped one has — cut short at the backstop —
// so its partial work is committed and marked incomplete, like any other run a
// clock ended.
func buildSproutCommitMessage(stepID, taskPrompt, status, failureError string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case SproutOutcomeFailed, SproutOutcomeTimedOut, SproutOutcomeReaped:
		return fmt.Sprintf("tendril(%s) [INCOMPLETE]: %s", strings.TrimSpace(stepID), summarizeSproutFailureError(failureError))
	}

	return fmt.Sprintf("tendril(%s): %s", strings.TrimSpace(stepID), summarizeSproutPrompt(taskPrompt))
}

func summarizeSproutPrompt(taskPrompt string) string {
	summary := strings.Join(strings.Fields(strings.TrimSpace(taskPrompt)), " ")
	if summary == "" {
		return "tendril task"
	}

	const maxRunes = 72
	runes := []rune(summary)
	if len(runes) <= maxRunes {
		return summary
	}

	summary = strings.TrimRight(string(runes[:maxRunes]), " ,.;:-")
	if summary == "" {
		summary = string(runes[:maxRunes])
	}

	return summary + "..."
}

func summarizeSproutFailureError(failureError string) string {
	summary := strings.Join(strings.Fields(strings.TrimSpace(failureError)), " ")
	if summary == "" {
		return "execution failed"
	}

	const maxRunes = 120
	runes := []rune(summary)
	if len(runes) <= maxRunes {
		return summary
	}

	summary = strings.TrimRight(string(runes[:maxRunes]), " ,.;:-")
	if summary == "" {
		summary = string(runes[:maxRunes])
	}

	return summary + "..."
}

// cloneForeignSubstrate clones a remote repository into a temporary terrarium.
func cloneForeignSubstrate(url, branch string) (string, error) {
	path, _, err := cloneNamedForeignSubstrate("", url, branch, ResolvedCredential{})
	return path, err
}

// cloneNamedForeignSubstrate materializes a foreign substrate and returns its
// path plus whether that path is persistent (managed/path checkout) — the caller
// removes only non-persistent (ephemeral) checkouts.
func cloneNamedForeignSubstrate(name, url, branch string, cred ResolvedCredential) (string, bool, error) {
	checkout, err := resolveCheckoutPlan(name, cred.Checkout)
	if err != nil {
		return "", false, err
	}
	dest := ""

	// Resolve git auth (mints a fresh GitHub App token when needed). The token
	// travels only in the process environment via an inline credential helper —
	// never in the clone URL, the command line, or the persisted .git/config, so
	// it can't leak into the mounted terrarium.
	gitEnv, err := materializeGitAuth(context.Background(), cred, url)
	if err != nil {
		return "", false, err
	}

	if !checkout.persistent {
		if dest, err = ephemeralCheckoutPath(name); err != nil {
			return "", false, err
		}
	} else if dest == "" {
		dest = checkout.dir
	}

	if checkout.persistent && checkout.tendrilOwned {
		// Managed checkout materialization mutates shared Git state. Reuse the
		// RunWorkspace metadata lock for only this short section; CreateRunWorkspace
		// acquires the same lock later, so release it before workspace allocation.
		dest = checkout.dir
		unlockGit := lockRunWorkspaceGit(dest)
		defer unlockGit()
		if err := materializeManagedCheckoutFn(name, dest, url, branch, cred, gitEnv); err != nil {
			return "", false, err
		}
		return dest, true, nil
	}

	// Path checkouts remain operator-owned and ephemeral checkouts remain
	// throwaway. Neither participates in the managed-base lock.
	if checkout.persistent {
		dest, err = ResolveSubstrateWorkspace(name, &SubstrateSpec{Checkout: cred.Checkout})
		if err != nil && !errors.Is(err, ErrWorkspaceAbsent) {
			return "", false, err
		}
	}
	existing := checkout.persistent && checkoutHasGitMetadata(dest)
	if err := materializeCheckout(dest, url, branch, gitEnv, checkout.tendrilOwned, existing); err != nil {
		return "", false, err
	}

	return dest, checkout.persistent, nil
}

var (
	refreshExistingCheckoutFn    = refreshExistingCheckout
	cloneCheckoutFn              = cloneCheckout
	materializeManagedCheckoutFn = materializeManagedCheckout
)

func materializeManagedCheckout(name, dest, url, branch string, cred ResolvedCredential, gitEnv []string) error {
	// Reuse only a checkout that is itself a git repository. git-rev-parse
	// walks parents, so an empty managed placeholder under Stem home (or
	// any parent repo) would otherwise be "refreshed" as that parent and
	// the Terrarium would bind-mount the still-empty directory as /app.
	resolved, err := ResolveSubstrateWorkspace(name, &SubstrateSpec{Checkout: cred.Checkout})
	if err != nil && !errors.Is(err, ErrWorkspaceAbsent) {
		return err
	}
	return materializeCheckout(dest, url, branch, gitEnv, true, strings.TrimSpace(resolved) != "")
}

func materializeCheckout(dest, url, branch string, gitEnv []string, tendrilOwned, existing bool) error {
	if existing {
		return refreshExistingCheckoutFn(dest, branch, gitEnv, tendrilOwned)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("prepare checkout dir: %w", err)
	}
	return cloneCheckoutFn(dest, url, branch, gitEnv)
}

func cloneCheckout(dest, url, branch string, gitEnv []string) error {
	args := []string{"-c", "protocol.ext.allow=never", "clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", url, dest)

	cmd := exec.Command("git", args...)
	if len(gitEnv) > 0 {
		cmd.Env = append(os.Environ(), gitEnv...)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}
	return nil
}

func pushTerrariumCommit(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
	targetBranch := strings.TrimSpace(branch)
	if targetBranch == "" {
		currentBranch, err := runGitCommand(ctx, mountPath, "branch", "--show-current")
		if err != nil {
			return err
		}
		targetBranch = strings.TrimSpace(currentBranch)
	}
	if targetBranch == "" {
		return fmt.Errorf("unable to determine branch for push")
	}
	targetBranch = strings.TrimPrefix(targetBranch, "refs/heads/")

	defaultBranch := ResolveDefaultBranchLocal(ctx, mountPath, "")
	if !allowDefaultBranchCommit && defaultBranch.IsProtected(targetBranch) {
		newBranch := fmt.Sprintf("sprout/task-%s", stepID)
		fmt.Fprintf(os.Stderr, "🛡️  Branch Protection: Auto-branching push from %s to %s\n", targetBranch, newBranch)

		targetBranch = newBranch
	}

	commitMessage, err := runGitCommand(ctx, mountPath, "log", "-1", "--pretty=%B", "HEAD")
	if err != nil {
		return err
	}

	if delegated, err := delegateGitPushIfConfigured(ctx, mountPath, targetBranch, commitMessage); delegated {
		return err
	}

	// Re-resolve auth for the push against the (tokenless) origin URL. For a
	// GitHub App this mints a fresh installation token; the credential travels
	// only in the process environment, never persisted to .git/config.
	originURL, _ := runGitCommand(ctx, mountPath, "remote", "get-url", "origin")
	pushEnv, authErr := materializeGitAuth(ctx, cred, strings.TrimSpace(originURL))
	if authErr != nil {
		return authErr
	}
	if err := requireGitHubPushAuth(pushEnv, strings.TrimSpace(originURL), cred); err != nil {
		return err
	}
	if _, err := runGitCommandWithEnv(ctx, mountPath, pushEnv, "push", "origin", "--", "HEAD:refs/heads/"+targetBranch); err != nil {
		return err
	}

	return nil
}

func stagePlasmidsForGenotype(sourcePath, targetPath, genotypeName string) error {
	genotype, err := loadGenotypeContext(sourcePath, genotypeName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to read genotype %s for plasmid staging: %v\n", genotypeName, err)
		return nil
	}
	if genotype == nil {
		fmt.Fprintf(os.Stderr, "⚠️ Genotype %s not found for plasmid staging\n", genotypeName)
		return nil
	}
	if len(genotype.Plasmids) == 0 {
		return nil
	}

	var sigVerifyFailed bool
	allowedPlasmids := make(map[string]struct{}, len(genotype.Plasmids))
	for _, allowed := range genotype.Plasmids {
		allowed = strings.TrimSpace(allowed)
		if allowed != "" {
			allowedPlasmids[strings.ToLower(allowed)] = struct{}{}
		}
	}

	for _, name := range genotype.Plasmids {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := allowedPlasmids[strings.ToLower(name)]; !ok {
			fmt.Fprintf(os.Stderr, "⚠️ Skipping staging of non-allowlisted plasmid %s\n", name)
			continue
		}

		denied := false
		for _, deny := range genotype.DenyPlasmids {
			if strings.EqualFold(name, deny) {
				denied = true
				break
			}
		}
		if denied {
			fmt.Fprintf(os.Stderr, "⚠️ Skipping staging of denied plasmid %s\n", name)
			continue
		}

		sourceFile, err := FindPlasmidSource(sourcePath, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to locate plasmid %s: %v\n", name, err)
			continue
		}

		if genotype.RequirePlasmidSignatures {
			publicKey, err := mesh.LoadPublicKey(sourcePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to load node public key for plasmid %s: %v\n", name, err)
				sigVerifyFailed = true
				continue
			}
			if err := VerifyPlasmidSignature(sourceFile, publicKey); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to verify plasmid signature for %s: %v\n", name, err)
				sigVerifyFailed = true
				continue
			}
		}

		destDir := filepath.Join(targetPath, ".tendril", "genome")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to create terrarium genome directory: %v\n", err)
			continue
		}

		destFile := filepath.Join(destDir, filepath.Base(sourceFile))
		if err := CopyMarkdownFile(sourceFile, destFile); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to stage terrarium plasmid %s: %v\n", name, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "🧬 Staged terrarium plasmid: %s -> %s\n", name, destFile)
	}

	if sigVerifyFailed {
		return fmt.Errorf("one or more required plasmid signature checks failed")
	}
	return nil
}

// DockerIsRootless asks the daemon whether it is running rootless. A rootless
// daemon cannot grant root on the host, so group membership stops being an
// escalation path, and container root (0:0) maps to an unprivileged host user.
func DockerIsRootless() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.SecurityOptions}}").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "rootless")
}

// ensureNotGitVisible ensures that if the targetPath is inside a Git repository,
// it is added to .git/info/exclude so it does not dirty the worktree.
func ensureNotGitVisible(targetPath string) {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return
	}
	dir := filepath.Dir(absTarget)
	for {
		gitDir := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			excludePath := filepath.Join(gitDir, "info", "exclude")
			rel, err := filepath.Rel(dir, absTarget)
			if err == nil {
				// Normalize for exclude pattern
				rel = filepath.ToSlash(rel)
				content, _ := os.ReadFile(excludePath)
				lines := strings.Split(string(content), "\n")
				found := false
				for _, line := range lines {
					if strings.TrimSpace(line) == rel {
						found = true
						break
					}
				}
				if !found {
					os.MkdirAll(filepath.Dir(excludePath), 0755)
					f, _ := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
					if f != nil {
						f.WriteString("\n" + rel + "\n")
						f.Close()
					}
				}
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}
