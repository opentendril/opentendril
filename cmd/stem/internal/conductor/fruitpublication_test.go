package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// TestFruitPublicationProviderFailureDoesNotPushRemote verifies that a provider/LLM
// failure occurring after isolation setup does not push a remote Fruit branch.
func TestFruitPublicationProviderFailureDoesNotPushRemote(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	providerErr := errors.New("provider API rate limit exceeded")
	stubRunSproutCollaborators(t, root, &failingSproutRunner{err: providerErr}, nil)

	var commitCalled, pushCalled, mergeCalled bool
	origCommit := commitTerrariumExecutionFn
	origPush := pushTerrariumCommitFn
	origMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		commitTerrariumExecutionFn = origCommit
		pushTerrariumCommitFn = origPush
		mergeTerrariumCommitFn = origMerge
	})

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		commitCalled = true
		return "deadbeef", nil
	}
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
		pushCalled = true
		return nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error {
		mergeCalled = true
		return nil
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	orch := &DockerOrchestrator{
		Substrate: root,
		StepID:    "step-provider-fail",
		EventBus:  bus,
	}

	report, err := orch.RunSprout(context.Background(), "task under test")
	if !errors.Is(err, providerErr) {
		t.Fatalf("RunSprout error = %v, want %v", err, providerErr)
	}

	if report.Outcome != SproutOutcomeFailed {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeFailed)
	}

	if commitCalled {
		t.Errorf("commitTerrariumExecutionFn was called for provider failure; want not called")
	}
	if pushCalled {
		t.Errorf("pushTerrariumCommitFn was called for provider failure; want not called")
	}
	if mergeCalled {
		t.Errorf("mergeTerrariumCommitFn was called for provider failure; want not called")
	}

	terminal := append(
		filterEvents(*events, eventbus.EventSproutMatured),
		filterEvents(*events, eventbus.EventSproutWithered)...,
	)
	if len(terminal) != 1 {
		t.Fatalf("published %d terminal events, want 1", len(terminal))
	}
	if terminal[0].Type != eventbus.EventSproutWithered {
		t.Fatalf("terminal event type = %q, want %q", terminal[0].Type, eventbus.EventSproutWithered)
	}
	if got := terminal[0].Data["outcome"]; got != SproutOutcomeFailed {
		t.Fatalf("terminal event outcome = %v, want %q", got, SproutOutcomeFailed)
	}
}

// TestFruitPublicationNoChangesDoesNotPushRemote verifies that a matured run with
// no workspace file changes does not push a remote Fruit branch.
func TestFruitPublicationNoChangesDoesNotPushRemote(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	runner := &stubSproutRunner{result: sproutResult{Response: "investigated and reported", WroteWorkspace: false}}
	stubRunSproutCollaborators(t, root, runner, []string{})

	var commitCalled, pushCalled, mergeCalled bool
	origCommit := commitTerrariumExecutionFn
	origPush := pushTerrariumCommitFn
	origMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		commitTerrariumExecutionFn = origCommit
		pushTerrariumCommitFn = origPush
		mergeTerrariumCommitFn = origMerge
	})

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		commitCalled = true
		return "deadbeef", nil
	}
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
		pushCalled = true
		return nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error {
		mergeCalled = true
		return nil
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	orch := &DockerOrchestrator{
		Substrate: root,
		StepID:    "step-no-changes",
		EventBus:  bus,
	}

	report, err := orch.RunSprout(context.Background(), "no changes task")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Outcome != SproutOutcomeNoChanges {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeNoChanges)
	}

	if commitCalled {
		t.Errorf("commitTerrariumExecutionFn was called for no-changes run; want not called")
	}
	if pushCalled {
		t.Errorf("pushTerrariumCommitFn was called for no-changes run; want not called")
	}
	if mergeCalled {
		t.Errorf("mergeTerrariumCommitFn was called for no-changes run; want not called")
	}

	terminal := filterEvents(*events, eventbus.EventSproutMatured)
	if len(terminal) != 1 {
		t.Fatalf("published %d matured events, want 1", len(terminal))
	}
	if got := terminal[0].Data["outcome"]; got != SproutOutcomeNoChanges {
		t.Fatalf("matured event outcome = %v, want %q", got, SproutOutcomeNoChanges)
	}
}

// TestFruitPublicationNoEngagementDoesNotPushRemote verifies that a run producing
// no response and modifying no files does not push a remote Fruit branch.
func TestFruitPublicationNoEngagementDoesNotPushRemote(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	runner := &stubSproutRunner{result: sproutResult{Response: "", WroteWorkspace: false}}
	stubRunSproutCollaborators(t, root, runner, []string{})

	var commitCalled, pushCalled, mergeCalled bool
	origCommit := commitTerrariumExecutionFn
	origPush := pushTerrariumCommitFn
	origMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		commitTerrariumExecutionFn = origCommit
		pushTerrariumCommitFn = origPush
		mergeTerrariumCommitFn = origMerge
	})

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		commitCalled = true
		return "deadbeef", nil
	}
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
		pushCalled = true
		return nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error {
		mergeCalled = true
		return nil
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	orch := &DockerOrchestrator{
		Substrate: root,
		StepID:    "step-no-engagement",
		EventBus:  bus,
	}

	report, err := orch.RunSprout(context.Background(), "no engagement task")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Outcome != SproutOutcomeNoEngagement {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeNoEngagement)
	}

	if commitCalled {
		t.Errorf("commitTerrariumExecutionFn was called for no-engagement run; want not called")
	}
	if pushCalled {
		t.Errorf("pushTerrariumCommitFn was called for no-engagement run; want not called")
	}
	if mergeCalled {
		t.Errorf("mergeTerrariumCommitFn was called for no-engagement run; want not called")
	}

	terminal := filterEvents(*events, eventbus.EventSproutWithered)
	if len(terminal) != 1 {
		t.Fatalf("published %d withered events, want 1", len(terminal))
	}
	if got := terminal[0].Data["outcome"]; got != SproutOutcomeNoEngagement {
		t.Fatalf("withered event outcome = %v, want %q", got, SproutOutcomeNoEngagement)
	}
}

// TestFruitPublicationMaturedPushesReviewableFruit verifies that a matured run with
// a modified file commits and merges/pushes exactly one reviewable Fruit ref.
func TestFruitPublicationMaturedPushesReviewableFruit(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	runner := &stubSproutRunner{result: sproutResult{Response: "added feature", WroteWorkspace: true}}
	stubRunSproutCollaborators(t, root, runner, []string{"pkg/feature.go"})

	var capturedStatusPath string
	var commitCount, mergeCount int
	origCommit := commitTerrariumExecutionFn
	origMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		commitTerrariumExecutionFn = origCommit
		mergeTerrariumCommitFn = origMerge
	})

	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		capturedStatusPath = statusPath
		commitCount++
		return "deadbeef1234", nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error {
		mergeCount++
		return nil
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	statusPath := filepath.Join(root, "tendril-status.json")

	orch := &DockerOrchestrator{
		Substrate:  root,
		StepID:     "step-matured-one-file",
		EventBus:   bus,
		StatusPath: statusPath,
	}

	report, err := orch.RunSprout(context.Background(), "matured task")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}

	if capturedStatusPath != "" {
		t.Fatalf("commitTerrariumExecutionFn was passed statusPath %q, want empty (status should not be committed as fruit)", capturedStatusPath)
	}

	if commitCount != 1 {
		t.Fatalf("commitTerrariumExecutionFn called %d times, want 1", commitCount)
	}
	if mergeCount != 1 {
		t.Fatalf("mergeTerrariumCommitFn called %d times, want 1", mergeCount)
	}

	terminal := filterEvents(*events, eventbus.EventSproutMatured)
	if len(terminal) != 1 {
		t.Fatalf("published %d matured events, want 1", len(terminal))
	}
	if got := terminal[0].Data["outcome"]; got != SproutOutcomeComplete {
		t.Fatalf("matured event outcome = %v, want %q", got, SproutOutcomeComplete)
	}

	statusBytes, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("expected status to be written locally: %v", err)
	}
	if !strings.Contains(string(statusBytes), "step-matured-one-file") {
		t.Fatalf("local status missing expected step id")
	}
}

// TestFruitPublicationHistoryAndEventPublishedForNonReviewableOutcomes verifies that
// history/event terminal outcomes are still published and status records are written
// for non-reviewable outcomes even when no Fruit commit/push occurs.
func TestFruitPublicationHistoryAndEventPublishedForNonReviewableOutcomes(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	statusFile := filepath.Join(root, "tendril-status.json")

	runner := &stubSproutRunner{result: sproutResult{Response: "investigation report", WroteWorkspace: false}}
	stubRunSproutCollaborators(t, root, runner, []string{})

	var commitCalled bool
	origCommit := commitTerrariumExecutionFn
	t.Cleanup(func() { commitTerrariumExecutionFn = origCommit })
	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		commitCalled = true
		return "deadbeef", nil
	}

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	orch := &DockerOrchestrator{
		Substrate:  root,
		StepID:     "step-history-event",
		StatusPath: statusFile,
		EventBus:   bus,
	}

	report, err := orch.RunSprout(context.Background(), "history test task")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Outcome != SproutOutcomeNoChanges {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeNoChanges)
	}

	if commitCalled {
		t.Errorf("commitTerrariumExecutionFn was called for non-reviewable outcome; want not called")
	}

	// Verify terminal event was published on EventBus
	terminal := filterEvents(*events, eventbus.EventSproutMatured)
	if len(terminal) != 1 {
		t.Fatalf("published %d matured events, want 1", len(terminal))
	}
	if got := terminal[0].Data["outcome"]; got != SproutOutcomeNoChanges {
		t.Fatalf("event outcome = %v, want %q", got, SproutOutcomeNoChanges)
	}

	// Verify status file was written locally on disk
	onDisk := readStatusFile(t, statusFile)
	if onDisk.Status != SproutOutcomeNoChanges {
		t.Fatalf("status file status = %q, want %q", onDisk.Status, SproutOutcomeNoChanges)
	}
	if onDisk.StepID != "step-history-event" {
		t.Fatalf("status file stepID = %q, want step-history-event", onDisk.StepID)
	}
}

// TestFruitPublicationStatusDoesNotDirtyGitWorktree verifies that a non-reviewable run
// with StatusPath set inside a Git repo leaves the repo clean and reclaims the isolation branch.
func TestFruitPublicationStatusDoesNotDirtyGitWorktree(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	statusFile := filepath.Join(root, "tendril-status.json")

	runner := &stubSproutRunner{result: sproutResult{Response: "investigation report", WroteWorkspace: false}}
	stubRunSproutCollaborators(t, root, runner, []string{})

	origCommit := commitTerrariumExecutionFn
	t.Cleanup(func() { commitTerrariumExecutionFn = origCommit })
	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential, seedIntegrationCheckpoint bool) (string, error) {
		t.Fatalf("commitTerrariumExecutionFn should not be called")
		return "", nil
	}

	bus := eventbus.New()

	orch := &DockerOrchestrator{
		Substrate:  root,
		StepID:     "step-status-dirty",
		StatusPath: statusFile,
		EventBus:   bus,
	}

	report, err := orch.RunSprout(context.Background(), "dirty test task")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}
	if report.Outcome != SproutOutcomeNoChanges {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeNoChanges)
	}

	// Verify git status --porcelain is empty on the root
	out, err := runGitCommand(context.Background(), root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status failed: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("git status --porcelain is not empty, worktree is dirty:\n%s", out)
	}

	// Verify current branch is back to main
	branch, err := runGitCommand(context.Background(), root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse failed: %v", err)
	}
	if strings.TrimSpace(branch) != "outcome-test" {
		t.Fatalf("expected to return to 'outcome-test' branch, got %q", strings.TrimSpace(branch))
	}

	// Verify the isolation branch is deleted
	branches, _ := runGitCommand(context.Background(), root, "branch", "--list", "sprout/task-step-status-dirty")
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("isolation branch was not deleted")
	}
}
