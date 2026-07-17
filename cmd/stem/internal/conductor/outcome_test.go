package conductor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/terrarium"
)

func TestClassifySproutOutcome(t *testing.T) {
	timedOut := fmt.Errorf("tool call cut off: %w", ErrSproutTimedOut)
	cases := []struct {
		name       string
		runErr     error
		files      []string
		filesKnown bool
		want       string
	}{
		{"changed something", nil, []string{"main.go"}, true, SproutOutcomeComplete},
		{"changed nothing", nil, []string{}, true, SproutOutcomeNoChanges},
		{"changes unmeasurable", nil, nil, false, SproutOutcomeComplete},
		{"failed", errors.New("agent exploded"), nil, true, SproutOutcomeFailed},
		{"timed out", timedOut, nil, true, SproutOutcomeTimedOut},
		{"timed out beats file evidence", timedOut, []string{"main.go"}, true, SproutOutcomeTimedOut},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifySproutOutcome(testCase.runErr, testCase.files, testCase.filesKnown)
			if got != testCase.want {
				t.Fatalf("classifySproutOutcome() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// failingSproutRunner is an agent whose loop errors, standing in for both a
// broken run and a watchdog-killed run (via an error wrapping
// ErrSproutTimedOut, exactly what terrariumToolSession.Call produces).
type failingSproutRunner struct {
	err error
}

func (f *failingSproutRunner) Run(ctx context.Context, taskPrompt string) (agentResult, error) {
	return agentResult{}, f.err
}

// newOutcomeTestRepo builds a committed git repository on a non-default branch
// so RunSprout's branch protection does not rewrite it mid-test.
func newOutcomeTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"checkout", "-b", "outcome-test"},
	} {
		if _, err := runGitCommand(ctx, root, args...); err != nil {
			t.Fatalf("git %v failed: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatalf("write baseline file: %v", err)
	}
	if _, err := runGitCommand(ctx, root, "add", "README.md"); err != nil {
		t.Fatalf("git add baseline: %v", err)
	}
	if _, err := runGitCommand(ctx, root, "commit", "-m", "baseline"); err != nil {
		t.Fatalf("git commit baseline: %v", err)
	}
	return root
}

// stubRunSproutCollaborators fakes every collaborator around the agent run so
// the outcome pipeline can be driven deterministically. It returns a pointer
// to the execution status captured from the commit step.
func stubRunSproutCollaborators(t *testing.T, root string, runner sproutRunner, modifiedFiles []string) *sproutExecutionStatus {
	t.Helper()

	originalPreflight := runSproutPreflightChecksFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsureImage := ensureSproutImageFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalInjectCache := injectMycorrhizalCacheFn
	originalStartSession := startTerrariumSessionFn
	originalNewAgent := newAgentFn
	originalStash := stashHostWorkspaceFn
	originalCollect := collectStageableFilesFn
	originalDiff := collectGitDiffFn
	originalCommit := commitTerrariumExecutionFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsureImage
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		injectMycorrhizalCacheFn = originalInjectCache
		startTerrariumSessionFn = originalStartSession
		newAgentFn = originalNewAgent
		stashHostWorkspaceFn = originalStash
		collectStageableFilesFn = originalCollect
		collectGitDiffFn = originalDiff
		commitTerrariumExecutionFn = originalCommit
		mergeTerrariumCommitFn = originalMerge
	})

	runSproutPreflightChecksFn = func(ctx context.Context) error { return nil }
	generateRepoMapFn = func(ctx context.Context, workspace string) (string, error) { return "# Repo Map", nil }
	generateMemoryMapFn = func(ctx context.Context, workspace string) (string, error) { return "", nil }
	ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
	createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) {
		shadowPath := filepath.Join(root, "shadow-worktree")
		if err := os.MkdirAll(shadowPath, 0o755); err != nil {
			return "", err
		}
		return shadowPath, nil
	}
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) { _ = os.RemoveAll(shadowPath) }
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, command []string, extraEnv ...string) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	newAgentFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string) (sproutRunner, error) {
		return runner, nil
	}
	stashHostWorkspaceFn = func(ctx context.Context, repoRoot, runID string) (bool, error) { return false, nil }
	collectStageableFilesFn = func(ctx context.Context, mountPath string, excludedPaths ...string) ([]string, error) {
		// Preserve measured-empty: the real collector returns []string{}, never
		// nil, and the distinction is exactly what the outcome pipeline reports.
		copied := make([]string, len(modifiedFiles))
		copy(copied, modifiedFiles)
		return copied, nil
	}
	collectGitDiffFn = func(ctx context.Context, mountPath string) (string, error) { return "", nil }

	captured := &sproutExecutionStatus{}
	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential) (string, error) {
		*captured = executionStatus
		return "deadbeefcafe", nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error { return nil }

	return captured
}

// recordSproutLifecycle subscribes to the sprout lifecycle events and returns
// a pointer to the slice they accumulate into. Handlers run synchronously on
// the publishing goroutine, so the slice is safe to read after RunSprout
// returns.
func recordSproutLifecycle(bus *eventbus.Bus) *[]eventbus.Event {
	events := &[]eventbus.Event{}
	for _, eventType := range []eventbus.EventType{
		eventbus.EventSproutEmerged,
		eventbus.EventSproutMatured,
		eventbus.EventSproutWithered,
	} {
		bus.Subscribe(eventType, func(event eventbus.Event) {
			*events = append(*events, event)
		})
	}
	return events
}

func filterEvents(events []eventbus.Event, eventType eventbus.EventType) []eventbus.Event {
	var matched []eventbus.Event
	for _, event := range events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

// TestRunSproutOutcomes drives RunSprout through each ending — changed
// something, changed nothing, failed, timed out — and asserts three surfaces
// agree: the returned report, the status written to the commit step, and the
// single lifecycle event published on the bus.
func TestRunSproutOutcomes(t *testing.T) {
	agentFailure := errors.New("agent exploded")
	agentTimeout := fmt.Errorf("tool call %q was cut off: %w", "runCommand", ErrSproutTimedOut)

	cases := []struct {
		name          string
		runner        sproutRunner
		modifiedFiles []string
		wantOutcome   string
		wantErrIs     error
		wantTerminal  eventbus.EventType
	}{
		{
			name:          "changed something",
			runner:        &stubSproutRunner{result: agentResult{Response: "did the work"}},
			modifiedFiles: []string{"pkg/thing.go"},
			wantOutcome:   SproutOutcomeComplete,
			wantTerminal:  eventbus.EventSproutMatured,
		},
		{
			name:          "changed nothing",
			runner:        &stubSproutRunner{result: agentResult{Response: "answered without acting"}},
			modifiedFiles: []string{},
			wantOutcome:   SproutOutcomeNoChanges,
			wantTerminal:  eventbus.EventSproutMatured,
		},
		{
			name:          "failed",
			runner:        &failingSproutRunner{err: agentFailure},
			modifiedFiles: []string{},
			wantOutcome:   SproutOutcomeFailed,
			wantErrIs:     agentFailure,
			wantTerminal:  eventbus.EventSproutWithered,
		},
		{
			name:          "timed out",
			runner:        &failingSproutRunner{err: agentTimeout},
			modifiedFiles: []string{},
			wantOutcome:   SproutOutcomeTimedOut,
			wantErrIs:     ErrSproutTimedOut,
			wantTerminal:  eventbus.EventSproutWithered,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := newOutcomeTestRepo(t)
			chdirToTempDir(t)
			captured := stubRunSproutCollaborators(t, root, testCase.runner, testCase.modifiedFiles)

			bus := eventbus.New()
			events := recordSproutLifecycle(bus)

			orch := &DockerOrchestrator{
				Substrate: root,
				StepID:    "step-outcome",
				SessionID: "session-outcome",
				EventBus:  bus,
			}
			report, err := orch.RunSprout(context.Background(), "task under test")

			if testCase.wantErrIs != nil {
				if !errors.Is(err, testCase.wantErrIs) {
					t.Fatalf("RunSprout error = %v, want errors.Is(%v)", err, testCase.wantErrIs)
				}
			} else if err != nil {
				t.Fatalf("RunSprout failed: %v", err)
			}

			if report.Outcome != testCase.wantOutcome {
				t.Fatalf("report.Outcome = %q, want %q", report.Outcome, testCase.wantOutcome)
			}
			if captured.Status != testCase.wantOutcome {
				t.Fatalf("tendril-status Status = %q, want %q", captured.Status, testCase.wantOutcome)
			}

			emerged := filterEvents(*events, eventbus.EventSproutEmerged)
			if len(emerged) != 1 {
				t.Fatalf("published %d sprout-emerged events, want exactly 1: %+v", len(emerged), emerged)
			}
			terminal := append(
				filterEvents(*events, eventbus.EventSproutMatured),
				filterEvents(*events, eventbus.EventSproutWithered)...,
			)
			if len(terminal) != 1 {
				t.Fatalf("published %d terminal lifecycle events, want exactly 1: %+v", len(terminal), terminal)
			}
			if terminal[0].Type != testCase.wantTerminal {
				t.Fatalf("terminal event type = %q, want %q", terminal[0].Type, testCase.wantTerminal)
			}
			if got := terminal[0].Data["outcome"]; got != testCase.wantOutcome {
				t.Fatalf("terminal event outcome = %v, want %q", got, testCase.wantOutcome)
			}
			if terminal[0].SessionID != "session-outcome" {
				t.Fatalf("terminal event SessionID = %q, want session-outcome", terminal[0].SessionID)
			}
			files, ok := terminal[0].Data["filesModified"].([]string)
			if !ok || files == nil {
				t.Fatalf("terminal event filesModified = %v, want a non-nil measured slice", terminal[0].Data["filesModified"])
			}
			if len(files) != len(testCase.modifiedFiles) {
				t.Fatalf("terminal event filesModified = %v, want %v", files, testCase.modifiedFiles)
			}
		})
	}
}

// TestRunSproutResumptionHonorsOutcomeVocabulary covers the tendril-status.json
// readers of the new vocabulary: no-changes resumes as already-complete (skip),
// while timed-out re-runs — the previous attempt was cut off, not judged.
func TestRunSproutResumptionHonorsOutcomeVocabulary(t *testing.T) {
	t.Run("no-changes skips", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		chdirToTempDir(t)
		statusPath := filepath.Join(root, "tendril-status.json")
		if err := writeSproutStatus(statusPath, sproutExecutionStatus{
			StepID: "step-resume", Status: SproutOutcomeNoChanges, FilesModified: []string{},
		}); err != nil {
			t.Fatalf("write status: %v", err)
		}
		if _, err := runGitCommand(context.Background(), root, "add", "tendril-status.json"); err != nil {
			t.Fatalf("git add status: %v", err)
		}
		if _, err := runGitCommand(context.Background(), root, "commit", "-m", "status"); err != nil {
			t.Fatalf("git commit status: %v", err)
		}

		originalPreflight := runSproutPreflightChecksFn
		t.Cleanup(func() { runSproutPreflightChecksFn = originalPreflight })
		runSproutPreflightChecksFn = func(ctx context.Context) error { return nil }

		report, err := (&DockerOrchestrator{
			Substrate:  root,
			StepID:     "step-resume",
			StatusPath: statusPath,
		}).RunSprout(context.Background(), "resume me")
		if err != nil {
			t.Fatalf("RunSprout failed: %v", err)
		}
		if report.Outcome != SproutOutcomeSkipped {
			t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeSkipped)
		}
	})

	t.Run("timed-out re-runs", func(t *testing.T) {
		root := newOutcomeTestRepo(t)
		chdirToTempDir(t)
		statusPath := filepath.Join(root, "tendril-status.json")
		if err := writeSproutStatus(statusPath, sproutExecutionStatus{
			StepID: "step-resume", Status: SproutOutcomeTimedOut, Error: "cut off",
		}); err != nil {
			t.Fatalf("write status: %v", err)
		}
		if _, err := runGitCommand(context.Background(), root, "add", "tendril-status.json"); err != nil {
			t.Fatalf("git add status: %v", err)
		}
		if _, err := runGitCommand(context.Background(), root, "commit", "-m", "status"); err != nil {
			t.Fatalf("git commit status: %v", err)
		}

		captured := stubRunSproutCollaborators(t, root,
			&stubSproutRunner{result: agentResult{Response: "second attempt"}}, []string{"pkg/thing.go"})

		report, err := (&DockerOrchestrator{
			Substrate:  root,
			StepID:     "step-resume",
			StatusPath: statusPath,
		}).RunSprout(context.Background(), "resume me")
		if err != nil {
			t.Fatalf("RunSprout failed: %v", err)
		}
		if report.Outcome != SproutOutcomeComplete {
			t.Fatalf("report.Outcome = %q, want %q (timed-out must retry, not skip)", report.Outcome, SproutOutcomeComplete)
		}
		if captured.Status != SproutOutcomeComplete {
			t.Fatalf("re-run did not reach the commit step; captured status %q", captured.Status)
		}
	})
}

// timedOutTerrarium reports every Run as cut off by the watchdog.
type timedOutTerrarium struct{}

func (f *timedOutTerrarium) ID() string                            { return "timed-out-terrarium" }
func (f *timedOutTerrarium) Provider() terrarium.TerrariumProvider { return nil }
func (f *timedOutTerrarium) CopyIn(ctx context.Context, payloads []terrarium.FilePayload) error {
	return nil
}
func (f *timedOutTerrarium) Run(ctx context.Context, spec terrarium.CommandSpec) (terrarium.CommandResult, error) {
	return terrarium.CommandResult{ExitCode: -1, TimedOut: true}, nil
}
func (f *timedOutTerrarium) CopyOut(ctx context.Context, paths []string) ([]terrarium.Artifact, error) {
	return nil, nil
}
func (f *timedOutTerrarium) SnapshotLogs(ctx context.Context) (terrarium.TerrariumLogs, error) {
	return terrarium.TerrariumLogs{}, nil
}
func (f *timedOutTerrarium) Stop(ctx context.Context) error { return nil }

// TestTerrariumToolSessionSurfacesTimeout proves the sprout tool session
// converts a timed-out CommandResult into the typed sentinel instead of a
// decode error — the link that lets RunSprout name the timeout.
func TestTerrariumToolSessionSurfacesTimeout(t *testing.T) {
	session := &terrariumToolSession{terrarium: &timedOutTerrarium{}}
	_, err := session.Call(context.Background(), ToolCall{Tool: "runCommand"})
	if !errors.Is(err, ErrSproutTimedOut) {
		t.Fatalf("Call error = %v, want errors.Is(ErrSproutTimedOut)", err)
	}
}

// TestRunSequenceSproutPublishesLifecycleOnce proves the sequence path emits
// the same single emerged/terminal pair the direct sprout path does, with the
// outcome the execution actually produced.
func TestRunSequenceSproutPublishesLifecycleOnce(t *testing.T) {
	agentTimeout := fmt.Errorf("tool call cut off: %w", ErrSproutTimedOut)
	cases := []struct {
		name         string
		result       sproutExecutionResult
		err          error
		wantOutcome  string
		wantTerminal eventbus.EventType
	}{
		{
			name:         "changed something",
			result:       sproutExecutionResult{Response: "done", Outcome: SproutOutcomeComplete, FilesModified: []string{"a.go"}},
			wantOutcome:  SproutOutcomeComplete,
			wantTerminal: eventbus.EventSproutMatured,
		},
		{
			name:         "changed nothing",
			result:       sproutExecutionResult{Response: "report only", Outcome: SproutOutcomeNoChanges, FilesModified: []string{}},
			wantOutcome:  SproutOutcomeNoChanges,
			wantTerminal: eventbus.EventSproutMatured,
		},
		{
			name:         "failed",
			err:          errors.New("agent exploded"),
			wantOutcome:  SproutOutcomeFailed,
			wantTerminal: eventbus.EventSproutWithered,
		},
		{
			name:         "timed out",
			err:          agentTimeout,
			wantOutcome:  SproutOutcomeTimedOut,
			wantTerminal: eventbus.EventSproutWithered,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			workspace := t.TempDir()
			chdirToTempDir(t)

			originalAtPath := runSequenceSproutAtPathFn
			t.Cleanup(func() { runSequenceSproutAtPathFn = originalAtPath })
			runSequenceSproutAtPathFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt, sourcePath, mountPath string) (sproutExecutionResult, error) {
				return testCase.result, testCase.err
			}

			bus := eventbus.New()
			events := recordSproutLifecycle(bus)

			_, err := runSequenceSprout(context.Background(), &DockerOrchestrator{
				Substrate: workspace,
				StepID:    "step-sequence",
				EventBus:  bus,
			}, "sequence task")
			if (err != nil) != (testCase.err != nil) {
				t.Fatalf("runSequenceSprout error = %v, want error presence %v", err, testCase.err != nil)
			}

			emerged := filterEvents(*events, eventbus.EventSproutEmerged)
			if len(emerged) != 1 {
				t.Fatalf("published %d sprout-emerged events, want exactly 1", len(emerged))
			}
			terminal := append(
				filterEvents(*events, eventbus.EventSproutMatured),
				filterEvents(*events, eventbus.EventSproutWithered)...,
			)
			if len(terminal) != 1 {
				t.Fatalf("published %d terminal lifecycle events, want exactly 1: %+v", len(terminal), terminal)
			}
			if terminal[0].Type != testCase.wantTerminal {
				t.Fatalf("terminal event type = %q, want %q", terminal[0].Type, testCase.wantTerminal)
			}
			if got := terminal[0].Data["outcome"]; got != testCase.wantOutcome {
				t.Fatalf("terminal event outcome = %v, want %q", got, testCase.wantOutcome)
			}
		})
	}
}
