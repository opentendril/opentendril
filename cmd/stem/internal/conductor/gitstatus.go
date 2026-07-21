package conductor

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// The read-side of the delegated git ladder.
//
// Four write operations carry guardrails that exist because a delegation subject guessed
// something it could not see — which branch was the default, whether the
// workspace was already on a feature branch, whether a pull request existed.
// Every one of those guardrails catches a guess; none of them removed the
// reason for guessing. RunGitStatus does: it lets a subject look before it
// acts, so a refusal becomes a prediction instead of a surprise.
//
// Two properties make it trustworthy, and both are deliberate:
//
//  1. It shares the guards' own view. The "may I commit here" answer comes
//     from AssessDefaultBranchCommit — the same predicate the commit guard
//     acts on — not from a reimplementation. A status that disagreed with the
//     write path would be worse than no status, because it would teach an
//     subject to distrust the read-side.
//  2. It makes no network calls, for the same reason: the commit guard is
//     offline, so status must be offline or the two would diverge whenever
//     the interface and the local record differ. It is also strictly
//     read-only — no fetch, no index refresh, nothing that writes to the
//     repository.

// gitStatusFileLimit bounds the reported path list. An unbounded list would
// drop an entire repository's worth of paths into a subject's context on a
// large change; the read-side exists to reduce cognitive load, not create it.
// The total count is always reported, so a truncated list is never mistaken
// for the whole picture.
const gitStatusFileLimit = 50

// GitStatusExecution is a fully resolved status request.
type GitStatusExecution struct {
	// Workspace is the resolved local workspace directory to inspect.
	Workspace string
	// ConfiguredBranch is the substrate's explicitly configured branch, fed to
	// the default-branch resolver.
	ConfiguredBranch string
	// AllowDefaultBranchCommit mirrors the substrate's protectDefaultBranch
	// opt-out, so the predictive answer accounts for it exactly as the commit
	// guard does.
	AllowDefaultBranchCommit bool
}

// GitStatusChange is one changed path and how it changed.
type GitStatusChange struct {
	// Path is the workspace-relative path.
	Path string
	// Kind is "modified", "added", "deleted", "renamed", or "untracked".
	Kind string
}

// GitStatusResult is a workspace's git state: what git says, and what Tendril
// will do about it.
type GitStatusResult struct {
	// --- factual: what git reports ---

	// Branch is the current branch, or "" for a detached head or a repository
	// with no commits (DetachedHead and HasCommits disambiguate).
	Branch string
	// DetachedHead reports a workspace not on any branch.
	DetachedHead bool
	// HasCommits is false for a freshly initialized repository.
	HasCommits bool
	// Head is the current commit hash ("" when there are no commits).
	Head string
	// DefaultBranch is the resolved default branch ("" when undetermined).
	DefaultBranch string
	// DefaultBranchSource records how it was determined, including "unknown"
	// — in which case the protection floor is what is in force.
	DefaultBranchSource string
	// Repository is the "owner/repo" the origin remote points at ("" when
	// there is no origin remote or it cannot be parsed).
	Repository string
	// Upstream is the tracking ref ("" when the branch has never been pushed).
	Upstream string
	// Ahead and Behind count commits relative to Upstream; both are 0 when
	// there is no upstream.
	Ahead  int
	Behind int
	// Clean reports a workspace with no uncommitted changes.
	Clean bool
	// ChangeCount is the TOTAL number of changed paths, whether or not the
	// Changes list below was truncated.
	ChangeCount int
	// Modified, Added, Deleted, Renamed and Untracked count changes by kind.
	Modified  int
	Added     int
	Deleted   int
	Renamed   int
	Untracked int
	// Changes lists changed paths, capped at gitStatusFileLimit.
	Changes []GitStatusChange
	// Truncated reports that Changes holds fewer entries than ChangeCount.
	Truncated bool

	// --- predictive: what Tendril will do ---

	// OnDefaultBranch reports that the workspace sits on the protected
	// default branch. Factual: it ignores any opt-out.
	OnDefaultBranch bool
	// CommitAllowed predicts whether git.commit would be permitted right now,
	// accounting for the substrate's protectDefaultBranch opt-out. It is
	// computed by the same predicate the commit guard acts on.
	CommitAllowed bool
	// BlockedReason explains a false CommitAllowed in one line ("" when a
	// commit is allowed).
	BlockedReason string
}

// RunGitStatus inspects the workspace and reports its state. It never mutates
// the repository and never touches the network.
func RunGitStatus(ctx context.Context, execution GitStatusExecution) (GitStatusResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return GitStatusResult{}, fmt.Errorf("git status workspace is required")
	}

	result := GitStatusResult{Clean: true, CommitAllowed: true}

	// A repository with no commits yet is a real state a subject needs
	// described, not an error: refusing it would send the subject back to
	// guessing at the moment it is most confused.
	if head, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "HEAD"); err == nil {
		result.Head = strings.TrimSpace(head)
		result.HasCommits = result.Head != ""
	}

	// The assessment carries the current branch, the resolved default branch,
	// and the commit prediction — all from the guards' own code path.
	assessment := AssessDefaultBranchCommit(ctx, execution.Workspace, execution.ConfiguredBranch, execution.AllowDefaultBranchCommit)
	result.Branch = assessment.Branch
	result.DefaultBranch = assessment.DefaultBranch.Branch
	result.DefaultBranchSource = string(assessment.DefaultBranch.Source)
	result.OnDefaultBranch = assessment.OnDefaultBranch
	result.CommitAllowed = assessment.CommitAllowed
	result.DetachedHead = assessment.DetachedHead
	if !result.CommitAllowed {
		if assessment.DetachedHead {
			result.BlockedReason = "the workspace is on no branch (detached head) — create a feature branch with git.branch first"
		} else {
			result.BlockedReason = fmt.Sprintf("the workspace is on %q, the repository's default branch (default branch %s) — create a feature branch with git.branch first", assessment.Branch, assessment.DefaultBranch.Describe())
		}
	}

	if originURL, err := runGitCommitCommandFn(ctx, execution.Workspace, "remote", "get-url", "origin"); err == nil {
		if owner, repo, parseErr := parseOwnerRepo(strings.TrimSpace(originURL)); parseErr == nil {
			result.Repository = owner + "/" + repo
		}
	}

	// Upstream, and the ahead/behind counts against it. A branch that has
	// never been pushed simply has none — reported as such rather than as an
	// error, since "not pushed yet" is exactly what the subject needs to know.
	if upstream, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		result.Upstream = strings.TrimSpace(upstream)
		if counts, countErr := runGitCommitCommandFn(ctx, execution.Workspace, "rev-list", "--left-right", "--count", result.Upstream+"...HEAD"); countErr == nil {
			fields := strings.Fields(strings.TrimSpace(counts))
			if len(fields) == 2 {
				result.Behind, _ = strconv.Atoi(fields[0])
				result.Ahead, _ = strconv.Atoi(fields[1])
			}
		}
	}

	changes, err := gitStatusChanges(ctx, execution.Workspace)
	if err != nil {
		return GitStatusResult{}, err
	}
	result.ChangeCount = len(changes)
	result.Clean = result.ChangeCount == 0
	for _, change := range changes {
		switch change.Kind {
		case "modified":
			result.Modified++
		case "added":
			result.Added++
		case "deleted":
			result.Deleted++
		case "renamed":
			result.Renamed++
		case "untracked":
			result.Untracked++
		}
	}
	if len(changes) > gitStatusFileLimit {
		result.Changes = changes[:gitStatusFileLimit]
		result.Truncated = true
	} else {
		result.Changes = changes
	}
	if result.Changes == nil {
		result.Changes = []GitStatusChange{}
	}

	return result, nil
}

// gitStatusChanges reads the workspace's changes via porcelain status. The -z
// form is used for the same reason the api-commit path uses it: a NUL-
// separated entry can never be corrupted by trimming, whereas the leading
// space of a worktree-only status code is indistinguishable from padding.
func gitStatusChanges(ctx context.Context, workspace string) ([]GitStatusChange, error) {
	raw, err := runGitCommandRawOutput(ctx, workspace, "status", "--porcelain", "-uall", "-z")
	if err != nil {
		return nil, err
	}

	var changes []GitStatusChange
	entries := strings.Split(raw, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		code := entry[:2]
		path := filepath.ToSlash(entry[3:])

		switch {
		case code == "??":
			changes = append(changes, GitStatusChange{Path: path, Kind: "untracked"})
		case code[0] == 'R' || code[0] == 'C':
			// A rename/copy entry is followed by its original path as a
			// separate NUL-separated field; consume it so it is not read as
			// another change.
			i++
			changes = append(changes, GitStatusChange{Path: path, Kind: "renamed"})
		case strings.Contains(code, "D"):
			changes = append(changes, GitStatusChange{Path: path, Kind: "deleted"})
		case strings.Contains(code, "A"):
			changes = append(changes, GitStatusChange{Path: path, Kind: "added"})
		default:
			changes = append(changes, GitStatusChange{Path: path, Kind: "modified"})
		}
	}
	return changes, nil
}
