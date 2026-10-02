package conductor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Reclamation: giving an owned reference the moment at which it is finished.
//
// The rules are deliberately narrower than git.prune's, because reclamation is
// automatic and prune is not: anything the system does unattended must be a
// certainty. Exactly two conditions reclaim a branch, and both mean there is
// nothing to lose:
//
//  1. The branch has produced no commits beyond the base it was cut from. It
//     is pure litter — a reference that was created in case work happened, and
//     no work happened. This needs no network call and cannot destroy
//     anything, because there is nothing on it.
//  2. The branch's tip belongs to a pull request that merged — the same
//     evidence git.prune requires, and the only signal that survives a squash
//     merge.
//
// Everything else is kept. A branch carrying unpublished commits is somebody's
// work, however old it looks and whoever created it.

// ReclaimOutcome reports what happened to one owned reference.
type ReclaimOutcome struct {
	Branch string
	// Reclaimed reports whether the branch was deleted.
	Reclaimed bool
	// Reason explains the decision either way.
	Reason string
}

// branchHasNoWork reports whether a branch has produced no commits beyond the
// base it was cut from. A branch with no recorded base is treated as having
// work, because "we do not know" must never authorize a deletion.
func branchHasNoWork(ctx context.Context, repository string, ref OwnedRef) bool {
	base := strings.TrimSpace(ref.Base)
	if base == "" {
		return false
	}
	out, err := runGitCommitCommandFn(ctx, repository, "rev-list", "--count", base+".."+ref.Branch)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "0"
}

// ReclaimOwnedRefIfNoWork removes an owned branch only when it has produced no
// commits beyond its recorded base. This narrow lifecycle primitive is for
// teardown paths whose responsibility ends with the workspace; committed Fruit
// remains available for review regardless of forge evidence or credentials.
func ReclaimOwnedRefIfNoWork(ctx context.Context, repository string, ref OwnedRef) ReclaimOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := ReclaimOutcome{Branch: ref.Branch}

	if current, err := runGitCommitCommandFn(ctx, repository, "branch", "--show-current"); err == nil {
		if strings.TrimSpace(current) == ref.Branch {
			outcome.Reason = "checked out here"
			return outcome
		}
	}
	if out, err := runGitCommitCommandFn(ctx, repository, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+ref.Branch); err == nil {
		if strings.TrimSpace(out) != "" {
			outcome.Reason = "checked out in another workspace"
			return outcome
		}
	}
	if !branchHasNoWork(ctx, repository, ref) {
		outcome.Reason = "carries committed Fruit"
		return outcome
	}

	if _, err := runGitCommitCommandFn(ctx, repository, "branch", "-D", ref.Branch); err != nil {
		outcome.Reason = fmt.Sprintf("reclamation failed: %v", err)
		return outcome
	}
	outcome.Reclaimed = true
	outcome.Reason = "no commits beyond its base — nothing to lose"
	_ = ForgetOwnedRef(repository, ref.Branch)
	return outcome
}

// ReclaimIntegratedIsolationBranch removes an owned branch after its exact
// checkpoint commit has been successfully integrated into a Seed branch.
func ReclaimIntegratedIsolationBranch(ctx context.Context, workspace RunWorkspace, seedBranch, checkpointCommit string) ReclaimOutcome {
	if ctx == nil {
		ctx = context.Background()
	}

	repository := strings.TrimSpace(workspace.Repository)
	branch := strings.TrimSpace(workspace.Branch)
	baseCommit := strings.TrimSpace(workspace.BaseCommit)
	runID := strings.TrimSpace(workspace.RunID)
	seedBranch = strings.TrimSpace(seedBranch)
	checkpointCommit = strings.TrimSpace(checkpointCommit)
	outcome := ReclaimOutcome{Branch: branch}

	if repository == "" || branch == "" || baseCommit == "" || runID == "" || seedBranch == "" || checkpointCommit == "" {
		outcome.Reason = "reclamation requires repository, branch, base commit, run ID, Seed branch, and checkpoint commit"
		return outcome
	}
	if !strings.HasPrefix(seedBranch, "tendril/seed-") || strings.TrimPrefix(seedBranch, "tendril/seed-") == "" {
		outcome.Reason = fmt.Sprintf("destination branch %q is not a tendril/seed-* branch", seedBranch)
		return outcome
	}
	if _, err := runGitCommitCommandFn(ctx, repository, "check-ref-format", "--branch", seedBranch); err != nil {
		outcome.Reason = fmt.Sprintf("destination Seed branch is invalid: %v", err)
		return outcome
	}

	unlockGit := lockRunWorkspaceGit(repository)
	defer unlockGit()

	owned, ok := runWorkspaceOwnedRef(repository, branch, baseCommit)
	if !ok {
		outcome.Reason = "branch is not an owned Sprout isolation branch with the recorded base commit"
		return outcome
	}
	if owned.RunID != runID {
		outcome.Reason = "branch ownership belongs to a different run"
		return outcome
	}

	branchTip, err := runGitCommitCommandFn(ctx, repository, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		outcome.Reason = fmt.Sprintf("temporary branch tip could not be resolved: %v", err)
		return outcome
	}
	if strings.TrimSpace(branchTip) != checkpointCommit {
		outcome.Reason = "temporary branch does not point to the checkpoint commit"
		return outcome
	}

	seedTip, err := runGitCommitCommandFn(ctx, repository, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+seedBranch+"^{commit}")
	if err != nil {
		outcome.Reason = fmt.Sprintf("Seed branch tip could not be resolved: %v", err)
		return outcome
	}
	if strings.TrimSpace(seedTip) != checkpointCommit {
		outcome.Reason = "Seed branch does not point to the checkpoint commit"
		return outcome
	}

	current, err := runGitCommitCommandFn(ctx, repository, "branch", "--show-current")
	if err != nil {
		outcome.Reason = fmt.Sprintf("current branch could not be inspected: %v", err)
		return outcome
	}
	if strings.TrimSpace(current) == branch {
		outcome.Reason = "checked out here"
		return outcome
	}

	worktreePath, err := runGitCommitCommandFn(ctx, repository, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+branch)
	if err != nil {
		outcome.Reason = fmt.Sprintf("linked worktree could not be inspected: %v", err)
		return outcome
	}
	if strings.TrimSpace(worktreePath) != "" {
		outcome.Reason = "checked out in another workspace"
		return outcome
	}

	// Re-prove exact ownership immediately before the destructive operation.
	currentOwned, ok := runWorkspaceOwnedRef(repository, branch, baseCommit)
	if !ok || currentOwned.RunID != owned.RunID {
		outcome.Reason = "branch ownership changed before reclamation"
		return outcome
	}

	if _, err := runGitCommitCommandFn(
		ctx,
		repository,
		"update-ref",
		"-d",
		"refs/heads/"+branch,
		checkpointCommit,
	); err != nil {
		outcome.Reason = fmt.Sprintf("reclamation failed: %v", err)
		return outcome
	}
	if err := ForgetOwnedRef(repository, branch); err != nil {
		const zeroOID = "0000000000000000000000000000000000000000"
		if _, restoreErr := runGitCommitCommandFn(
			ctx,
			repository,
			"update-ref",
			"refs/heads/"+branch,
			checkpointCommit,
			zeroOID,
		); restoreErr != nil {
			outcome.Reason = fmt.Sprintf(
				"ownership cleanup failed after branch deletion: %v; restoring temporary branch also failed: %v",
				err,
				restoreErr,
			)
			return outcome
		}
		outcome.Reason = fmt.Sprintf(
			"ownership cleanup failed after branch deletion: %v; temporary branch restored",
			err,
		)
		return outcome
	}
	outcome.Reclaimed = true
	outcome.Reason = "commits integrated into checkpoint ref"
	return outcome
}

// ReclaimOwnedRef decides and acts on a single owned reference. It never
// reclaims the branch currently checked out, and never one held by another
// workspace — those belong to work in progress.
func ReclaimOwnedRef(ctx context.Context, repository string, ref OwnedRef, credential ResolvedCredential) ReclaimOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := ReclaimOutcome{Branch: ref.Branch}

	if current, err := runGitCommitCommandFn(ctx, repository, "branch", "--show-current"); err == nil {
		if strings.TrimSpace(current) == ref.Branch {
			outcome.Reason = "checked out here"
			return outcome
		}
	}

	// A branch held by any worktree is being worked on by someone right now.
	if out, err := runGitCommitCommandFn(ctx, repository, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+ref.Branch); err == nil {
		if strings.TrimSpace(out) != "" {
			outcome.Reason = "checked out in another workspace"
			return outcome
		}
	}

	switch {
	case branchHasNoWork(ctx, repository, ref):
		outcome.Reason = "no commits beyond its base — nothing to lose"
	default:
		// The branch carries commits. It may only be reclaimed on the same
		// evidence git.prune demands: a merged pull request.
		merged, reason := ownedRefIsMerged(ctx, repository, ref, credential)
		if !merged {
			outcome.Reason = reason
			return outcome
		}
		outcome.Reason = reason
	}

	if _, err := runGitCommitCommandFn(ctx, repository, "branch", "-D", ref.Branch); err != nil {
		outcome.Reason = fmt.Sprintf("reclamation failed: %v", err)
		return outcome
	}
	outcome.Reclaimed = true
	_ = ForgetOwnedRef(repository, ref.Branch)
	return outcome
}

// ReclaimDelegatedWorkspaceOwnedRef is the lifecycle authority for deleting
// the exact branch attached to an explicitly abandoned delegated workspace.
// The caller supplies the inspected branch tip and Fruit state; this function
// rechecks ownership, no-unique-work or merged evidence, default-branch
// protection, and checkout occupancy before deletion.
func ReclaimDelegatedWorkspaceOwnedRef(
	ctx context.Context,
	repository string,
	ref OwnedRef,
	expectedHead string,
	fruitState string,
	configuredBranch string,
	credential ResolvedCredential,
) ReclaimOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := ReclaimOutcome{Branch: ref.Branch}
	repository = strings.TrimSpace(repository)
	if repository == "" || strings.TrimSpace(ref.Branch) == "" || strings.TrimSpace(expectedHead) == "" || ref.Pending || ref.Purpose != PurposeDelegatedWorkspace || strings.TrimSpace(ref.Pollen) == "" || filepath.Clean(ref.Repository) != filepath.Clean(repository) {
		outcome.Reason = "exact delegated-workspace ownership and branch-tip evidence are required"
		return outcome
	}
	if fruitState == FruitStateOpen || fruitState == FruitStateClosedUnmerged {
		outcome.Reason = "branch Fruit is open or closed-unmerged and remains available for review"
		return outcome
	}

	ownedNow, ok := exactDelegatedOwnedRef(repository, ref)
	if !ok {
		outcome.Reason = "exact delegated-workspace ownership changed before reclamation"
		return outcome
	}
	head, err := runGitCommitCommandFn(ctx, repository, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+ref.Branch+"^{commit}")
	if err != nil || strings.TrimSpace(head) != expectedHead {
		outcome.Reason = "branch tip changed or could not be verified before reclamation"
		return outcome
	}

	defaultBranch := ResolveDefaultBranch(ctx, repository, configuredBranch, credential)
	if !defaultBranch.Known() {
		outcome.Reason = "repository default branch is unknown; branch deletion is refused"
		return outcome
	}
	if defaultBranch.IsProtected(ref.Branch) {
		outcome.Reason = "branch is the resolved default branch"
		return outcome
	}

	noUniqueWork := branchHasNoWork(ctx, repository, ref)
	if !noUniqueWork {
		if fruitState != FruitStateMerged {
			outcome.Reason = "branch contains unique Fruit without verified merged evidence"
			return outcome
		}
		merged, reason := ownedRefIsMerged(ctx, repository, ref, credential)
		if !merged {
			outcome.Reason = reason
			return outcome
		}
		outcome.Reason = reason
	} else {
		outcome.Reason = "branch has no commits beyond its recorded base"
	}

	if current, currentErr := runGitCommitCommandFn(ctx, repository, "branch", "--show-current"); currentErr != nil {
		outcome.Reason = "current branch occupancy could not be revalidated"
		return outcome
	} else if strings.TrimSpace(current) == ref.Branch {
		outcome.Reason = "branch remains checked out in the Substrate"
		return outcome
	}
	checkedOut, occupancyErr := runGitCommitCommandFn(ctx, repository, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+ref.Branch)
	if occupancyErr != nil {
		outcome.Reason = "branch checkout occupancy could not be revalidated"
		return outcome
	}
	if strings.TrimSpace(checkedOut) != "" {
		outcome.Reason = "branch remains checked out in another workspace"
		return outcome
	}

	// Revalidate the tuple and tip immediately before the destructive ref
	// update, after any forge lookup has completed.
	ownedNow, ok = exactDelegatedOwnedRef(repository, ref)
	if !ok || ownedNow.Base != ref.Base || ownedNow.Pollen != ref.Pollen {
		outcome.Reason = "exact delegated-workspace ownership changed before branch deletion"
		return outcome
	}
	latestHead, headErr := runGitCommitCommandFn(ctx, repository, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+ref.Branch+"^{commit}")
	if headErr != nil || strings.TrimSpace(latestHead) != expectedHead {
		outcome.Reason = "branch tip changed before branch deletion"
		return outcome
	}
	latestNoUniqueWork := branchHasNoWork(ctx, repository, ref)
	if !latestNoUniqueWork {
		if fruitState != FruitStateMerged {
			outcome.Reason = "branch gained unique Fruit before branch deletion"
			return outcome
		}
		merged, reason := ownedRefIsMerged(ctx, repository, ref, credential)
		if !merged {
			outcome.Reason = reason
			return outcome
		}
		outcome.Reason = reason
	}
	latestDefault := ResolveDefaultBranch(ctx, repository, configuredBranch, credential)
	if !latestDefault.Known() {
		outcome.Reason = "repository default branch became unknown before branch deletion"
		return outcome
	}
	if latestDefault.IsProtected(ref.Branch) {
		outcome.Reason = "branch became protected as the resolved default before deletion"
		return outcome
	}
	current, currentErr := runGitCommitCommandFn(ctx, repository, "branch", "--show-current")
	if currentErr != nil {
		outcome.Reason = "current branch occupancy could not be revalidated before deletion"
		return outcome
	}
	if strings.TrimSpace(current) == ref.Branch {
		outcome.Reason = "branch became checked out in the Substrate before deletion"
		return outcome
	}
	checkedOut, occupancyErr = runGitCommitCommandFn(ctx, repository, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+ref.Branch)
	if occupancyErr != nil {
		outcome.Reason = "branch checkout occupancy could not be revalidated before deletion"
		return outcome
	}
	if strings.TrimSpace(checkedOut) != "" {
		outcome.Reason = "branch became checked out in another workspace before deletion"
		return outcome
	}
	if _, err := runGitCommitCommandFn(ctx, repository, "branch", "-D", ref.Branch); err != nil {
		outcome.Reason = fmt.Sprintf("reclamation failed: %v", err)
		return outcome
	}
	outcome.Reclaimed = true
	if err := ForgetOwnedRef(repository, ref.Branch); err != nil {
		outcome.Reason = fmt.Sprintf("branch was deleted but ownership retirement failed: %v", err)
		return outcome
	}
	if outcome.Reason == "" {
		outcome.Reason = "verified merged branch reclaimed"
	}
	return outcome
}

func exactDelegatedOwnedRef(repository string, expected OwnedRef) (OwnedRef, bool) {
	for _, ref := range OwnedRefsFor(repository) {
		if ref.Branch == expected.Branch && ref.Purpose == PurposeDelegatedWorkspace &&
			ref.Pollen == expected.Pollen && ref.Base == expected.Base && !ref.Pending {
			return ref, true
		}
	}
	return OwnedRef{}, false
}

// ownedRefIsMerged asks the forge whether the branch's tip belongs to a merged
// pull request, and reports why not when it does not.
func ownedRefIsMerged(ctx context.Context, repository string, ref OwnedRef, credential ResolvedCredential) (bool, string) {
	head, err := runGitCommitCommandFn(ctx, repository, "rev-parse", ref.Branch)
	if err != nil {
		return false, "carries commits, and its tip could not be read"
	}
	originURL, err := runGitCommitCommandFn(ctx, repository, "remote", "get-url", "origin")
	if err != nil {
		return false, "carries unpublished commits (no origin remote to verify against)"
	}
	trimmedURL := strings.TrimSpace(originURL)
	owner, repo, err := parseOwnerRepo(trimmedURL)
	if err != nil {
		return false, "carries unpublished commits (origin remote is not a recognisable repository)"
	}
	token, err := pullRequestAPIToken(ctx, credential, trimmedURL)
	if err != nil {
		return false, "carries commits that cannot be verified (the connection has no GitHub API credential)"
	}
	state, err := lookupPullRequestForCommit(ctx, owner, repo, strings.TrimSpace(head), token)
	if err != nil {
		return false, "carries commits whose merge state could not be established"
	}
	if !state.Known {
		return false, "carries commits the remote has never seen — this is unpublished work"
	}
	if !state.Merged {
		return false, "carries commits that are not merged"
	}
	return true, fmt.Sprintf("pull request %d merged", state.Number)
}

// ReclaimOwnedRefs walks every reference Tendril owns in a repository and
// reclaims the ones that are finished. It is called at the points where a
// reference's purpose naturally ends — a run completing, a Pollinator returning
// for its workspace — so that litter is removed by the same act that created
// it, rather than by a cleanup chore later.
func ReclaimOwnedRefs(ctx context.Context, repository string, credential ResolvedCredential) []ReclaimOutcome {
	refs := OwnedRefsFor(repository)
	if len(refs) == 0 {
		return nil
	}
	outcomes := make([]ReclaimOutcome, 0, len(refs))
	for _, ref := range refs {
		if ref.Pending {
			outcomes = append(outcomes, ReclaimOutcome{Branch: ref.Branch, Reason: "allocation is still pending"})
			continue
		}
		// A registered branch that no longer exists is simply forgotten: the
		// registry should not accumulate its own kind of litter.
		if _, err := runGitCommitCommandFn(ctx, repository, "rev-parse", "--verify", "--quiet", "refs/heads/"+ref.Branch); err != nil {
			_ = ForgetOwnedRef(repository, ref.Branch)
			continue
		}
		outcomes = append(outcomes, ReclaimOwnedRef(ctx, repository, ref, credential))
	}
	return outcomes
}

// ReclaimUnusedIsolationBranch is the end of a protective branch's life.
//
// A Sprout run that touches the default branch is moved onto an isolation
// branch first. When the run produces commits, that branch IS the work and is
// left exactly where it is, checked out, for review. When the run produces
// nothing, the branch is pure residue — and, having never been pushed, it
// could never be cleaned up afterwards by anything that requires remote
// evidence. So it is removed by the run that created it: the workspace returns
// to the branch it started on and the empty branch goes with it.
//
// It reports whether it reclaimed, and never returns an error: failing to tidy
// up must not fail a run that otherwise succeeded.
func ReclaimUnusedIsolationBranch(ctx context.Context, repository, branch, returnTo string, credential ResolvedCredential) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	branch = strings.TrimSpace(branch)
	returnTo = strings.TrimSpace(returnTo)
	if branch == "" || returnTo == "" || branch == returnTo {
		return false
	}

	owned := OwnedRefsFor(repository)
	var ref OwnedRef
	for _, candidate := range owned {
		if candidate.Branch == branch {
			ref = candidate
			break
		}
	}
	if ref.Branch == "" {
		return false
	}

	// Only an empty branch is reclaimed here. A branch carrying commits is the
	// run's output, and deleting a run's output to keep a repository tidy
	// would be the worst trade in this codebase.
	if !branchHasNoWork(ctx, repository, ref) {
		return false
	}

	// The workspace must leave the branch before it can be removed, and it
	// returns to exactly where the run found it.
	if _, err := runGitCommitCommandFn(ctx, repository, "checkout", returnTo); err != nil {
		return false
	}
	if _, err := runGitCommitCommandFn(ctx, repository, "branch", "-D", branch); err != nil {
		return false
	}
	_ = ForgetOwnedRef(repository, branch)
	return true
}
