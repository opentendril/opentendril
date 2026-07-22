package conductor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Branch lifecycle after a merge: classification, and the deletion it gates.
//
// This is the only operation on the delegated ladder that can DESTROY work: a
// branch whose commits exist nowhere else is simply gone.
//
// Merged-ness is never inferred from local history or from a branch name:
//
//   - `git branch --merged` MISSES SQUASH MERGES ENTIRELY — a squash-merged
//     branch reports as unmerged, because its commits never appear in the
//     target's history;
//   - `git diff base...branch` fails for the same reason;
//   - the only reliable signal is asking the forge which pull request a branch
//     tip belongs to and whether that pull request merged.
//
// A branch is deletable only when that evidence says merged.

// Branch classifications. Exactly one of these is deletable.
const (
	// BranchMerged is the only deletable state: the tip belongs to a pull
	// request that merged.
	BranchMerged = "merged"
	// BranchCurrent is checked out here; deleting it is never meaningful.
	BranchCurrent = "current"
	// BranchDefault is the repository's default branch.
	BranchDefault = "default"
	// BranchPullRequestOpen has an open pull request — work in flight.
	BranchPullRequestOpen = "pull-request-open"
	// BranchPullRequestClosed has a pull request that was CLOSED WITHOUT
	// MERGING. Its commits may exist nowhere else; this is rejected work, not
	// finished work.
	BranchPullRequestClosed = "pull-request-closed"
	// BranchUnpushed means the forge has never seen the tip commit. This is
	// local-only work that no remote check can vouch for.
	BranchUnpushed = "unpushed"
	// BranchNoPullRequest means the tip is known to the forge but belongs to
	// no pull request.
	BranchNoPullRequest = "no-pull-request"
	// BranchUnverified means the merge state could not be established at all
	// (no interface credential, or the lookup failed). Deny-closed: unknown
	// is never treated as merged.
	BranchUnverified = "unverified"
	// BranchCheckedOutElsewhere is held by another workspace (an isolated
	// per-Pollinator worktree), so another Pollen is working on it.
	BranchCheckedOutElsewhere = "checked-out-elsewhere"
)

// GitBranchInfo is one local branch and the evidence for its state.
type GitBranchInfo struct {
	// Name is the branch name.
	Name string
	// Head is the branch's tip commit.
	Head string
	// Upstream is its tracking ref ("" when it has none).
	Upstream string
	// Classification is one of the Branch* constants above.
	Classification string
	// PullRequest is the pull request number the tip belongs to (0 when none
	// or unknown).
	PullRequest int
	// Deletable reports whether this branch may be pruned. Only a merged
	// classification is ever true.
	Deletable bool
	// Reason explains the classification in one line, for the Botanist or a Pollen
	// deciding what to do next.
	Reason string
}

// GitBranchListExecution is a fully resolved branch-listing request.
type GitBranchListExecution struct {
	// Workspace is the resolved local workspace directory.
	Workspace string
	// ConfiguredBranch is the substrate's explicitly configured branch, fed to
	// the default-branch resolver.
	ConfiguredBranch string
	// Credential supplies the interface lookup that establishes merge state.
	// Without it every branch is BranchUnverified, and therefore undeletable.
	Credential ResolvedCredential
}

// GitBranchListResult reports every local branch with its evidence.
type GitBranchListResult struct {
	Branches []GitBranchInfo
	// Verified reports whether merge state could be established at all. False
	// means the connection had no interface credential, so nothing is
	// deletable no matter what it looks like locally.
	Verified bool
	// DefaultBranch is the resolved default branch, for context.
	DefaultBranch string
}

// forgePullRequestState is what the interface reports about a commit: the pull
// request it belongs to, and whether that pull request merged.
type forgePullRequestState struct {
	Number int
	State  string
	Merged bool
	Known  bool
}

// lookupPullRequestForCommit asks which pull request a commit belongs to. This
// is the authoritative answer that survives squash merges, and the same
// interface shape the pull-request path already uses.
func lookupPullRequestForCommit(ctx context.Context, owner, repo, sha, token string) (forgePullRequestState, error) {
	var pulls []struct {
		Number   int    `json:"number"`
		State    string `json:"state"`
		MergedAt string `json:"merged_at"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/pulls", owner, repo, sha)
	if err := githubRESTRequest(ctx, http.MethodGet, path, token, nil, &pulls); err != nil {
		// A commit the forge has never seen is reported as an error by the
		// interface; that is a meaningful answer, not a failure — it means
		// local-only work, which is never deletable.
		if strings.Contains(err.Error(), "422") || strings.Contains(err.Error(), "404") {
			return forgePullRequestState{Known: false}, nil
		}
		return forgePullRequestState{}, err
	}
	if len(pulls) == 0 {
		return forgePullRequestState{Known: true}, nil
	}
	// Prefer a merged pull request when a commit belongs to several.
	for _, pull := range pulls {
		if strings.TrimSpace(pull.MergedAt) != "" {
			return forgePullRequestState{Number: pull.Number, State: pull.State, Merged: true, Known: true}, nil
		}
	}
	return forgePullRequestState{Number: pulls[0].Number, State: pulls[0].State, Known: true}, nil
}

// lookupPullRequestsForHead asks which pull requests were ever opened FROM a
// branch name. It exists because the commit-to-pull-request lookup above does
// not reliably associate a pull request that was CLOSED WITHOUT MERGING — that
// branch comes back looking like it never had one, which is a materially
// worse thing to tell someone deciding whether to delete it.
//
// It is used ONLY to downgrade a classification, never to grant deletability.
// The distinction matters: a commit hash is unique forever, but a branch NAME
// can be reused. If a branch called feat/x was merged last year and someone
// creates a new feat/x today, this query finds the old merged pull request
// while the new tip is unpushed work. Trusting it to mark something deletable
// would delete that work. Trusting it to say "there is a closed pull request
// here, be careful" is always safe.
func lookupPullRequestsForHead(ctx context.Context, owner, repo, branch, token string) (forgePullRequestState, error) {
	var pulls []struct {
		Number   int    `json:"number"`
		State    string `json:"state"`
		MergedAt string `json:"merged_at"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=all&head=%s", owner, repo, url.QueryEscape(owner+":"+branch))
	if err := githubRESTRequest(ctx, http.MethodGet, path, token, nil, &pulls); err != nil {
		return forgePullRequestState{}, err
	}
	if len(pulls) == 0 {
		return forgePullRequestState{Known: true}, nil
	}
	// Prefer an open pull request, then the most recent closed one; a merged
	// result is deliberately NOT reported as merged here (see above).
	for _, pull := range pulls {
		if strings.EqualFold(pull.State, "open") {
			return forgePullRequestState{Number: pull.Number, State: pull.State, Known: true}, nil
		}
	}
	return forgePullRequestState{Number: pulls[0].Number, State: pulls[0].State, Known: true}, nil
}

// RunGitBranchList enumerates local branches and classifies each one against
// evidence. It never deletes anything.
func RunGitBranchList(ctx context.Context, execution GitBranchListExecution) (GitBranchListResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return GitBranchListResult{}, fmt.Errorf("branch list workspace is required")
	}

	resolution := ResolveDefaultBranchLocal(ctx, execution.Workspace, execution.ConfiguredBranch)
	result := GitBranchListResult{DefaultBranch: resolution.Branch}

	current := ""
	if out, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current"); err == nil {
		current = strings.TrimSpace(out)
	}

	// %(worktreepath) is non-empty when a branch is checked out somewhere —
	// including another subject's isolated workspace, where another Pollinator is
	// working on it right now.
	raw, err := runGitCommitCommandFn(ctx, execution.Workspace, "for-each-ref",
		"--format=%(refname:short)%09%(objectname)%09%(upstream:short)%09%(worktreepath)", "refs/heads")
	if err != nil {
		return GitBranchListResult{}, err
	}

	// The interface credential establishes merge state. Without one, nothing
	// can be verified — and unverified is never treated as merged.
	var owner, repo, token string
	if originURL, urlErr := runGitCommitCommandFn(ctx, execution.Workspace, "remote", "get-url", "origin"); urlErr == nil {
		trimmed := strings.TrimSpace(originURL)
		if parsedOwner, parsedRepo, parseErr := parseOwnerRepo(trimmed); parseErr == nil {
			if resolvedToken, tokenErr := pullRequestAPIToken(ctx, execution.Credential, trimmed); tokenErr == nil {
				owner, repo, token = parsedOwner, parsedRepo, resolvedToken
				result.Verified = true
			}
		}
	}

	// One lookup per distinct tip: several branches often point at the same
	// commit, and the interface should not be asked twice for one answer.
	stateBySHA := map[string]forgePullRequestState{}

	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) < 2 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		info := GitBranchInfo{Name: strings.TrimSpace(fields[0]), Head: strings.TrimSpace(fields[1])}
		if len(fields) > 2 {
			info.Upstream = strings.TrimSpace(fields[2])
		}
		worktreePath := ""
		if len(fields) > 3 {
			worktreePath = strings.TrimSpace(fields[3])
		}

		switch {
		case info.Name == current:
			info.Classification = BranchCurrent
			info.Reason = "checked out here"
		case resolution.IsProtected(info.Name):
			info.Classification = BranchDefault
			info.Reason = fmt.Sprintf("the repository's default branch (%s)", resolution.Describe())
		case worktreePath != "":
			info.Classification = BranchCheckedOutElsewhere
			info.Reason = fmt.Sprintf("checked out in another workspace (%s) — another Pollinator may be working on it", worktreePath)
		case !result.Verified:
			info.Classification = BranchUnverified
			info.Reason = "merge state could not be established (the connection has no GitHub API credential) — nothing is deletable without evidence"
		default:
			state, ok := stateBySHA[info.Head]
			if !ok {
				fetched, lookupErr := lookupPullRequestForCommit(ctx, owner, repo, info.Head, token)
				if lookupErr != nil {
					state = forgePullRequestState{}
				} else {
					state = fetched
				}
				stateBySHA[info.Head] = state
			}
			// A tip with no associated pull request may still have one that
			// was closed without merging, which the commit lookup misses.
			// This can only make the answer more cautious, never less.
			if state.Known && state.Number == 0 {
				if byHead, headErr := lookupPullRequestsForHead(ctx, owner, repo, info.Name, token); headErr == nil && byHead.Number > 0 {
					state.Number = byHead.Number
					state.State = byHead.State
				}
			}

			info.PullRequest = state.Number
			switch {
			case !state.Known:
				info.Classification = BranchUnpushed
				info.Reason = "its tip commit is unknown to the remote — this is local-only work that no remote check can vouch for"
			case state.Merged:
				info.Classification = BranchMerged
				info.Deletable = true
				info.Reason = fmt.Sprintf("pull request %d merged", state.Number)
			case state.Number > 0 && strings.EqualFold(state.State, "closed"):
				info.Classification = BranchPullRequestClosed
				info.Reason = fmt.Sprintf("pull request %d was closed WITHOUT merging — this is rejected work, and its commits may exist nowhere else", state.Number)
			case state.Number > 0:
				info.Classification = BranchPullRequestOpen
				info.Reason = fmt.Sprintf("pull request %d is still open", state.Number)
			default:
				info.Classification = BranchNoPullRequest
				info.Reason = "its tip is known to the remote but belongs to no pull request"
			}
		}

		result.Branches = append(result.Branches, info)
	}

	if result.Branches == nil {
		result.Branches = []GitBranchInfo{}
	}
	return result, nil
}

// GitPruneExecution is a fully resolved prune request.
type GitPruneExecution struct {
	// Workspace is the resolved local workspace directory.
	Workspace string
	// ConfiguredBranch is the substrate's explicitly configured branch.
	ConfiguredBranch string
	// Credential supplies the interface lookup that establishes merge state.
	Credential ResolvedCredential
	// Confirm actually deletes. Without it the operation reports what it
	// WOULD delete and changes nothing.
	//
	// This inverts the usual convention, where the destructive action is the
	// default and a dry run is opt-in, and the inversion is the point: for an
	// operation invoked by a Pollinator that may have misread its instructions,
	// the safe path must be the one taken by accident.
	Confirm bool
}

// GitPrunedBranch records a deleted branch and how to restore it.
type GitPrunedBranch struct {
	Name string
	// Head is the tip the branch pointed at, so an unwanted prune is a
	// one-line recovery rather than a reflog expedition.
	Head        string
	PullRequest int
}

// GitPruneResult reports what was (or would be) removed, and why everything
// else was kept.
type GitPruneResult struct {
	// Confirmed reports whether this run actually deleted anything.
	Confirmed bool
	// Deleted lists branches removed (or, when not confirmed, the branches
	// that WOULD be removed).
	Deleted []GitPrunedBranch
	// Kept lists every branch that was not removed, with the reason.
	Kept []GitBranchInfo
	// Verified reports whether merge state could be established at all.
	Verified bool
}

// RunGitPrune deletes local branches whose pull request merged, and nothing
// else. Every branch is re-classified against fresh evidence at deletion time:
// a list produced a minute ago is not authority to delete.
func RunGitPrune(ctx context.Context, execution GitPruneExecution) (GitPruneResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	listed, err := RunGitBranchList(ctx, GitBranchListExecution{
		Workspace:        execution.Workspace,
		ConfiguredBranch: execution.ConfiguredBranch,
		Credential:       execution.Credential,
	})
	if err != nil {
		return GitPruneResult{}, err
	}

	result := GitPruneResult{Confirmed: execution.Confirm, Verified: listed.Verified}
	for _, branch := range listed.Branches {
		// The single gate. Deletable is set only by the merged classification,
		// which is set only from interface evidence — never from a name, never
		// from local history.
		if !branch.Deletable || branch.Classification != BranchMerged {
			result.Kept = append(result.Kept, branch)
			continue
		}

		if !execution.Confirm {
			result.Deleted = append(result.Deleted, GitPrunedBranch{Name: branch.Name, Head: branch.Head, PullRequest: branch.PullRequest})
			continue
		}

		// -D rather than -d is required precisely because a squash-merged
		// branch looks unmerged to git. That is why the interface evidence
		// above is not optional: it is the only thing standing where git's own
		// safety check cannot.
		if _, deleteErr := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "-D", branch.Name); deleteErr != nil {
			kept := branch
			kept.Deletable = false
			kept.Reason = fmt.Sprintf("deletion failed: %v", deleteErr)
			result.Kept = append(result.Kept, kept)
			continue
		}
		result.Deleted = append(result.Deleted, GitPrunedBranch{Name: branch.Name, Head: branch.Head, PullRequest: branch.PullRequest})
	}

	if result.Deleted == nil {
		result.Deleted = []GitPrunedBranch{}
	}
	if result.Kept == nil {
		result.Kept = []GitBranchInfo{}
	}
	return result, nil
}
