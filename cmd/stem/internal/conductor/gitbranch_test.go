package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func currentBranch(t *testing.T, repo string) string {
	t.Helper()
	out, err := runGitCommand(context.Background(), repo, "branch", "--show-current")
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	return strings.TrimSpace(out)
}

func branchOID(t *testing.T, repo, ref string) string {
	t.Helper()
	out, err := runGitCommand(context.Background(), repo, "rev-parse", "--verify", ref)
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(out)
}

func originRefsSnapshot(t *testing.T, repo string) string {
	t.Helper()
	out, err := runGitCommand(context.Background(), repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/origin")
	if err != nil {
		t.Fatalf("git for-each-ref origin refs: %v", err)
	}
	return out
}

func TestRunGitBranchValidatesExecution(t *testing.T) {
	ctx := context.Background()
	if _, err := RunGitBranch(ctx, GitBranchExecution{Branch: "feat/x"}); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if _, err := RunGitBranch(ctx, GitBranchExecution{Workspace: t.TempDir()}); err == nil {
		t.Fatal("missing branch accepted")
	}
}

// TestRunGitBranchRejectsUnsafeNames: a delegated caller supplies this name,
// so anything that could read as a flag or a malformed reference is refused.
func TestRunGitBranchRejectsUnsafeNames(t *testing.T) {
	repo := newBranchRepo(t, "feat/base", "trunk")
	for _, name := range []string{
		"--force", "-x", "/leading", "trailing/", "has..dots", "spaced name",
		"tilde~1", "caret^", "colon:ref", "star*", "quote\"", "semi;colon",
		"pipe|", "amp&", "sub$(x)", "back`tick`", "ends.lock",
	} {
		if _, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: name}); err == nil {
			t.Errorf("unsafe branch name %q was accepted", name)
		}
	}
	if got := currentBranch(t, repo); got != "feat/base" {
		t.Fatalf("workspace moved to %q during refused operations, want feat/base", got)
	}
}

// TestRunGitBranchCreatesAndSwitches covers the normal path and the
// idempotent repeat: creating, then asking again, which switches rather than
// failing or resetting.
func TestRunGitBranchCreatesAndSwitches(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/base", "trunk")

	result, err := RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "refs/heads/feat/new-leaf"})
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if result.Status != "created" || result.Branch != "feat/new-leaf" || result.PreviousBranch != "feat/base" {
		t.Fatalf("result = %+v, want feat/new-leaf created from feat/base", result)
	}
	if got := currentBranch(t, repo); got != "feat/new-leaf" {
		t.Fatalf("workspace on %q, want feat/new-leaf", got)
	}

	// Asking for the branch the workspace is already on is a no-op success.
	result, err = RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/new-leaf"})
	if err != nil {
		t.Fatalf("re-request current branch: %v", err)
	}
	if result.Status != "switched" {
		t.Fatalf("result = %+v, want switched for the branch already checked out", result)
	}

	// Go back, then ask for the existing branch again: it switches, and the
	// branch is NOT reset — its commit must survive.
	if _, err := runGitCommand(ctx, repo, "checkout", "feat/base"); err != nil {
		t.Fatalf("checkout base: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "feat/new-leaf"); err != nil {
		t.Fatalf("checkout leaf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "leaf.txt"), []byte("grown\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "work on the leaf"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "feat/base"); err != nil {
		t.Fatalf("checkout base: %v", err)
	}

	result, err = RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/new-leaf"})
	if err != nil {
		t.Fatalf("switch to existing branch: %v", err)
	}
	if result.Status != "switched" {
		t.Fatalf("result = %+v, want switched", result)
	}
	after, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if strings.TrimSpace(after) != strings.TrimSpace(head) {
		t.Fatal("switching to an existing branch moved it — an existing branch must never be reset, that discards commits")
	}
}

func TestRunGitBranchRecordsOwnershipOnlyForNewDelegatedBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/base", "trunk")
	base := branchOID(t, repo, "HEAD")

	result, err := RunGitBranch(ctx, GitBranchExecution{
		Workspace: repo, Repository: repo, Pollen: "pollen-one", Branch: "feat/new-owned",
	})
	if err != nil {
		t.Fatalf("create delegated branch: %v", err)
	}
	if result.Status != "created" {
		t.Fatalf("result = %+v, want created", result)
	}
	owned, ok := delegatedOwnedRef(repo, "feat/new-owned", "pollen-one")
	if !ok || owned.Repository != filepath.Clean(repo) || owned.Purpose != PurposeDelegatedWorkspace || owned.Base != base || owned.Pending || !owned.RetainEmpty {
		t.Fatalf("new branch ownership = %+v, want exact finalized repository/Pollen/base ownership", owned)
	}
}

func TestRunGitBranchSwitchDoesNotTransferExistingOwnership(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/base", "trunk")
	if _, err := runGitCommand(ctx, repo, "branch", "feat/pre-existing"); err != nil {
		t.Fatalf("create pre-existing branch: %v", err)
	}
	base := branchOID(t, repo, "refs/heads/feat/pre-existing")
	otherPollen := OwnedRef{
		Repository: repo, Branch: "feat/pre-existing", Purpose: PurposeDelegatedWorkspace,
		Pollen: "other-pollen", Base: base,
	}
	if err := RegisterOwnedRef(otherPollen); err != nil {
		t.Fatalf("register other Pollen ownership: %v", err)
	}

	result, err := RunGitBranch(ctx, GitBranchExecution{
		Workspace: repo, Repository: repo, Pollen: "pollen-one", Branch: "feat/pre-existing",
	})
	if err != nil {
		t.Fatalf("switch to existing branch: %v", err)
	}
	if result.Status != "switched" {
		t.Fatalf("result = %+v, want switched", result)
	}
	if _, ok := delegatedOwnedRef(repo, "feat/pre-existing", "pollen-one"); ok {
		t.Fatal("switching to an existing branch invented ownership for the current Pollen")
	}
	owned, ok := delegatedOwnedRef(repo, "feat/pre-existing", "other-pollen")
	if !ok || owned.Pollen != otherPollen.Pollen || owned.Base != otherPollen.Base {
		t.Fatalf("existing ownership = %+v, want the original other-Pollen ownership unchanged", owned)
	}
}

func TestRunGitBranchFinalizationFailureLeavesPendingStateAndRefusesContinuation(t *testing.T) {
	repository, workspacePath, _ := newDelegatedWorkspaceLifecycleFixture(t)
	branch := "feat/ownership-finalization-failure"
	blockedTempPath := ownedRefsPath() + ".tmp"
	originalRun := runGitCommitCommandFn
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		output, err := originalRun(ctx, dir, args...)
		if err == nil && dir == workspacePath && len(args) >= 3 && args[0] == "checkout" && args[1] == "-b" && args[2] == branch {
			if mkdirErr := os.Mkdir(blockedTempPath, 0o700); mkdirErr != nil {
				return "", mkdirErr
			}
		}
		return output, err
	}
	defer func() { runGitCommitCommandFn = originalRun }()

	_, err := RunGitBranch(context.Background(), GitBranchExecution{
		Workspace: workspacePath, Repository: repository, Pollen: "pollen", Branch: branch,
	})
	if err == nil || !strings.Contains(err.Error(), "pending reservation is preserved") {
		t.Fatalf("branch creation error = %v, want fail-closed finalization error with preserved reservation", err)
	}
	if current := currentBranch(t, workspacePath); current != branch {
		t.Fatalf("workspace branch = %q, want created branch retained for recovery", current)
	}
	owned, ok := ownedRefForBranch(repository, branch)
	if !ok || !owned.Pending || !owned.RetainEmpty || owned.Purpose != PurposeDelegatedWorkspace || owned.Pollen != "pollen" || owned.Base == "" {
		t.Fatalf("failed registration state = %+v, want exact pending, non-reclaimable ownership reservation", owned)
	}
	if _, err := ResolveDelegatedWorkspaceWithModeAndDefaultBranch(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, ExistingDelegatedWorkspaceOnly, "main"); err == nil {
		t.Fatal("workspace continued after ownership finalization failed")
	}
}

func TestRunGitBranchFromDefaultUsesExactOriginCommit(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/old", "main")
	if _, err := runGitCommand(ctx, repo, "branch", "main"); err != nil {
		t.Fatalf("create default branch: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "main"); err != nil {
		t.Fatalf("checkout default branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "default.txt"), []byte("refreshed default\n"), 0o644); err != nil {
		t.Fatalf("write refreshed default: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "default.txt"); err != nil {
		t.Fatalf("stage refreshed default: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "advance default"); err != nil {
		t.Fatalf("advance default: %v", err)
	}
	refreshedOID := branchOID(t, repo, "refs/heads/main")
	if _, err := runGitCommand(ctx, repo, "update-ref", "refs/remotes/origin/main", refreshedOID); err != nil {
		t.Fatalf("refresh origin/main: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "feat/old"); err != nil {
		t.Fatalf("return to old feature branch: %v", err)
	}
	oldOID := branchOID(t, repo, "refs/heads/feat/old")
	defaultOID := branchOID(t, repo, "refs/heads/main")
	remoteRefsBefore := originRefsSnapshot(t, repo)

	result, err := RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/next", ConfiguredBranch: "main", FromDefault: true})
	if err != nil {
		t.Fatalf("create feature branch from refreshed default: %v", err)
	}
	if result.Status != "created" || result.Branch != "feat/next" || result.PreviousBranch != "feat/old" {
		t.Fatalf("result = %+v, want feat/next created from the old feature workspace", result)
	}
	if got := currentBranch(t, repo); got != "feat/next" {
		t.Fatalf("workspace on %q, want feat/next", got)
	}
	if got := branchOID(t, repo, "refs/heads/feat/next"); got != refreshedOID {
		t.Fatalf("new feature branch OID = %s, want exact refreshed origin/main OID %s", got, refreshedOID)
	}
	if got := branchOID(t, repo, "refs/heads/feat/old"); got != oldOID {
		t.Fatalf("old feature branch moved from %s to %s", oldOID, got)
	}
	if got := branchOID(t, repo, "refs/heads/main"); got != defaultOID {
		t.Fatalf("default branch moved from %s to %s", defaultOID, got)
	}
	if got := originRefsSnapshot(t, repo); got != remoteRefsBefore {
		t.Fatalf("origin refs changed from %q to %q", remoteRefsBefore, got)
	}
}

func TestRunGitBranchFromDefaultRefusesDirtyWorkspace(t *testing.T) {
	repo := newBranchRepo(t, "feat/old", "main")
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	_, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: "feat/next", ConfiguredBranch: "main", FromDefault: true})
	if err == nil || !strings.Contains(err.Error(), "clean workspace") {
		t.Fatalf("error = %v, want a clean-workspace refusal", err)
	}
	if got := currentBranch(t, repo); got != "feat/old" {
		t.Fatalf("workspace moved to %q after refusal", got)
	}
	if _, err := runGitCommand(context.Background(), repo, "rev-parse", "--verify", "--quiet", "refs/heads/feat/next"); err == nil {
		t.Fatal("branch was created despite the dirty-workspace refusal")
	}
}

func TestRunGitBranchFromDefaultRefusesExistingTarget(t *testing.T) {
	repo := newBranchRepo(t, "feat/old", "main")
	if _, err := runGitCommand(context.Background(), repo, "branch", "feat/existing"); err != nil {
		t.Fatalf("create existing target: %v", err)
	}
	before := branchOID(t, repo, "refs/heads/feat/existing")
	_, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: "feat/existing", ConfiguredBranch: "main", FromDefault: true})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want an actionable existing-target refusal", err)
	}
	if got := currentBranch(t, repo); got != "feat/old" {
		t.Fatalf("workspace switched to %q after refusal", got)
	}
	if got := branchOID(t, repo, "refs/heads/feat/existing"); got != before {
		t.Fatalf("existing target moved from %s to %s", before, got)
	}
}

func TestRunGitBranchFromDefaultRefusesUnknownDefault(t *testing.T) {
	repo := newBranchRepo(t, "feat/old", "")
	_, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: "feat/next", FromDefault: true})
	if err == nil || !strings.Contains(err.Error(), "requires a resolved repository default branch") {
		t.Fatalf("error = %v, want an actionable unknown-default refusal", err)
	}
	if got := currentBranch(t, repo); got != "feat/old" {
		t.Fatalf("workspace moved to %q after refusal", got)
	}
}

func TestRunGitBranchFromDefaultRefusesMissingOriginTrackingRef(t *testing.T) {
	repo := newBranchRepo(t, "feat/old", "main")
	if _, err := runGitCommand(context.Background(), repo, "update-ref", "-d", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("delete origin/main fixture: %v", err)
	}
	_, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: "feat/next", FromDefault: true})
	if err == nil || !strings.Contains(err.Error(), "does not resolve to a commit") || !strings.Contains(err.Error(), "git.fetch") {
		t.Fatalf("error = %v, want an actionable missing-origin-ref refusal", err)
	}
	if got := currentBranch(t, repo); got != "feat/old" {
		t.Fatalf("workspace moved to %q after refusal", got)
	}
}

// TestRunGitBranchRefusesDefaultBranch: creating a branch named as the
// repository's default branch is refused, same reasoning as the pull-request
// guard.
func TestRunGitBranchRefusesDefaultBranch(t *testing.T) {
	repo := newBranchRepo(t, "feat/base", "trunk")
	_, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: repo, Branch: "trunk"})
	if err == nil {
		t.Fatal("a branch named as the default branch was created")
	}
	if !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("error = %v, want a refusal naming the default branch", err)
	}

	// And under the floor, when the default branch cannot be determined.
	bare := newBranchRepo(t, "feat/base", "")
	if _, err := RunGitBranch(context.Background(), GitBranchExecution{Workspace: bare, Branch: "main"}); err == nil {
		t.Fatal("the protection floor did not refuse a branch named main when the default branch was undetermined")
	}
}

// TestRunGitBranchCarriesChangesToNewBranchButRefusesDirtySwitch pins the
// asymmetry: uncommitted work follows you onto a NEW branch (the normal
// started-editing-first recovery), but is never carried onto an EXISTING one.
func TestRunGitBranchCarriesChangesToNewBranchButRefusesDirtySwitch(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/base", "trunk")
	if _, err := runGitCommand(ctx, repo, "branch", "feat/already-there"); err != nil {
		t.Fatalf("pre-create branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Existing branch + dirty workspace: refused.
	_, err := RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/already-there"})
	if err == nil {
		t.Fatal("switching to an existing branch with uncommitted changes was allowed")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want an uncommitted-changes refusal", err)
	}
	if got := currentBranch(t, repo); got != "feat/base" {
		t.Fatalf("workspace moved to %q despite the refusal", got)
	}

	// New branch + dirty workspace: allowed, and the work comes along.
	result, err := RunGitBranch(ctx, GitBranchExecution{Workspace: repo, Branch: "feat/brand-new"})
	if err != nil {
		t.Fatalf("create branch with uncommitted changes: %v", err)
	}
	if result.Status != "created" {
		t.Fatalf("result = %+v, want created", result)
	}
	if _, err := os.Stat(filepath.Join(repo, "wip.txt")); err != nil {
		t.Fatalf("uncommitted work did not follow onto the new branch: %v", err)
	}
}
