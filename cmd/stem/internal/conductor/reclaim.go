package conductor

import (
	"context"
	"fmt"
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
