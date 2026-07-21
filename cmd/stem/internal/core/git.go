package core

import (
	"context"
	"fmt"
	"strings"
)

// The git capability family: the delegated-execution ladder from the Design
// RFC. It lets an external mind ask the Stem to do git work — under the
// substrate's configured connection — instead of shelling out git on the host
// itself and guessing at credentials. Three operation-classes exist today:
// git.commit (commit the workspace under the configured identity), git.push
// (Stem-mediated authenticated push) and git.pr (open a pull request through
// the GitHub API). Each is separately grantable, and the family stays
// deliberately narrow: no branch, no checkout, no merge.
//
// Attribution model (security-first): a delegated commit exists to be
// *attributable*, so the execution refuses to commit when the resolved
// substrate credential carries no commit identity — deny-closed, enforced in
// the conductor (see RunGitCommit). The git execution itself lives outside
// the Core in the conductor (which the Core is structurally forbidden from
// importing — see boundary_test.go), so it is injected as a transport-free
// function port via WithGit, the same template as PassthroughOperations.

// GitCommitInput asks the Stem to commit the current state of a substrate's
// workspace under the substrate's configured commit identity.
type GitCommitInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Message is the commit message.
	Message string `json:"message"`
	// Paths optionally limits staging to the given workspace-relative paths;
	// empty stages all changes.
	Paths []string `json:"paths,omitempty"`
	// Origin records which surface invoked the commit (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// GitCommitSpec is the fully resolved, transport-free commit request handed
// to the GitOperations port.
type GitCommitSpec struct {
	Substrate string
	Message   string
	Paths     []string
	Origin    string
}

// GitCommitResult is the outcome of a finished delegated commit.
type GitCommitResult struct {
	// Status is "committed" when a commit was created, or "nothing-to-commit"
	// when the workspace held no changes (no empty commit is ever created).
	Status string `json:"status"`
	// CommitHash is the created commit's hash (empty when nothing was
	// committed).
	CommitHash string `json:"commitHash,omitempty"`
}

// GitPushInput asks the Stem to push the substrate's current branch to its
// remote using the substrate's configured credential. The push runs on the
// Stem (the sole secret-holding zone), never inside a sealed Sprout — a
// delegated push is the Stem's mediated egress with the connection's dedicated
// Personal Access Token.
type GitPushInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Branch optionally names the branch to push; empty pushes the workspace's
	// current branch.
	Branch string `json:"branch,omitempty"`
	// Origin records which surface invoked the push (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// GitPushSpec is the fully resolved, transport-free push request handed to the
// GitOperations port.
type GitPushSpec struct {
	Substrate string
	Branch    string
	Origin    string
}

// GitPushResult is the outcome of a finished delegated push.
type GitPushResult struct {
	// Status is always "pushed" when the push command succeeded (git treats an
	// already-current ref as a successful no-op push).
	Status string `json:"status"`
	// Branch is the branch that was pushed.
	Branch string `json:"branch,omitempty"`
}

// GitPRInput asks the Stem to open a pull request for a branch that has
// already been published. Opening a pull request is a separate operation-class
// from pushing on purpose: operation-classes are separately grantable, so a
// subject granted only git.pr must never be able to publish a branch as a side
// effect. The subject's loop is git.commit → git.push → git.pr.
type GitPRInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Title is the pull request title.
	Title string `json:"title"`
	// Body is the optional pull request description.
	Body string `json:"body,omitempty"`
	// Head optionally names the branch to open the pull request from; empty
	// uses the workspace's current branch (a read of actual state, never an
	// assumed name).
	Head string `json:"head,omitempty"`
	// Base optionally names the branch to merge into; empty resolves the
	// repository's real default branch from the GitHub API. A default branch
	// is never assumed to be "main" — assuming it is the failure this
	// capability exists to design out.
	Base string `json:"base,omitempty"`
	// Draft opens the pull request as a draft.
	Draft bool `json:"draft,omitempty"`
	// Origin records which surface invoked the pull request (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// GitPRSpec is the fully resolved, transport-free pull-request request handed
// to the GitOperations port.
type GitPRSpec struct {
	Substrate string
	Title     string
	Body      string
	Head      string
	Base      string
	Draft     bool
	Origin    string
}

// GitPRResult is the outcome of a finished delegated pull-request operation.
type GitPRResult struct {
	// Status is "created" when a new pull request was opened, or "exists" when
	// an open pull request for the same head branch was already there (the
	// existing one is returned untouched — a repeat call never duplicates and
	// never rewrites a description a human may have edited).
	Status string `json:"status"`
	// Number is the pull request number.
	Number int `json:"number"`
	// URL is the pull request's web address.
	URL string `json:"url,omitempty"`
	// Head is the branch the pull request was opened from.
	Head string `json:"head,omitempty"`
	// Base is the branch the pull request merges into, as actually resolved.
	Base string `json:"base,omitempty"`
}

// GitBranchInput asks the Stem to create (or switch to) a feature branch in a
// substrate's workspace. It exists so that default-branch protection has a
// correct next move: a subject told "commit on a feature branch" must be able
// to make one through Tendril, or the guardrail simply pushes it off the
// governed path.
type GitBranchInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Branch is the branch to create and switch to.
	Branch string `json:"branch"`
	// Origin records which surface invoked the operation (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// GitBranchSpec is the fully resolved, transport-free branch request handed to
// the GitOperations port.
type GitBranchSpec struct {
	Substrate string
	Branch    string
	Origin    string
}

// GitBranchResult is the outcome of a finished branch operation.
type GitBranchResult struct {
	// Status is "created" for a new branch, or "switched" when the branch
	// already existed and the workspace moved onto it (an existing branch is
	// never reset — that would discard work).
	Status string `json:"status"`
	// Branch is the branch now checked out.
	Branch string `json:"branch"`
	// PreviousBranch is the branch the workspace was on beforehand.
	PreviousBranch string `json:"previousBranch,omitempty"`
}

// GitStatusInput asks the Stem to report a substrate's git state. It is the
// read-side of the ladder: every write operation carries a guardrail that
// exists because a delegation subject guessed something it could not see, and this is how
// it sees instead. The answer is computed from the guards' own view, so a
// prediction here and a refusal there can never disagree.
type GitStatusInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Origin records which surface invoked the operation (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// GitStatusSpec is the fully resolved, transport-free status request handed to
// the GitOperations port.
type GitStatusSpec struct {
	Substrate string
	Origin    string
}

// GitStatusChange is one changed path and how it changed.
type GitStatusChange struct {
	Path string `json:"path"`
	// Kind is "modified", "added", "deleted", "renamed", or "untracked".
	Kind string `json:"kind"`
}

// GitStatusResult is a workspace's git state: what git reports, and what
// Tendril will do about it.
type GitStatusResult struct {
	// Branch is the current branch ("" for a detached head or a repository
	// with no commits).
	Branch string `json:"branch"`
	// DetachedHead reports a workspace not on any branch.
	DetachedHead bool `json:"detachedHead,omitempty"`
	// HasCommits is false for a freshly initialized repository.
	HasCommits bool `json:"hasCommits"`
	// Head is the current commit hash ("" when there are no commits).
	Head string `json:"head,omitempty"`
	// DefaultBranch is the resolved default branch ("" when undetermined).
	DefaultBranch string `json:"defaultBranch,omitempty"`
	// DefaultBranchSource is how it was determined: config, api, remote-head,
	// or unknown — in which case the protection floor is what is in force.
	DefaultBranchSource string `json:"defaultBranchSource,omitempty"`
	// Repository is the "owner/repo" the origin remote points at.
	Repository string `json:"repository,omitempty"`
	// Upstream is the tracking ref ("" when the branch has never been pushed).
	Upstream string `json:"upstream,omitempty"`
	// Ahead and Behind count commits relative to Upstream.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	// Clean reports a workspace with no uncommitted changes.
	Clean bool `json:"clean"`
	// ChangeCount is the total number of changed paths, whether or not
	// Changes was truncated.
	ChangeCount int `json:"changeCount"`
	// Modified, Added, Deleted, Renamed and Untracked count changes by kind.
	Modified  int `json:"modified"`
	Added     int `json:"added"`
	Deleted   int `json:"deleted"`
	Renamed   int `json:"renamed"`
	Untracked int `json:"untracked"`
	// Changes lists changed paths, bounded so a large change cannot flood an
	// subject's context; Truncated says the list is shorter than ChangeCount.
	Changes   []GitStatusChange `json:"changes"`
	Truncated bool              `json:"truncated,omitempty"`
	// OnDefaultBranch reports that the workspace sits on the protected
	// default branch, ignoring any opt-out.
	OnDefaultBranch bool `json:"onDefaultBranch"`
	// CommitAllowed predicts whether git.commit would be permitted right now.
	CommitAllowed bool `json:"commitAllowed"`
	// BlockedReason explains a false CommitAllowed ("" when allowed).
	BlockedReason string `json:"blockedReason,omitempty"`
	// Workspace is the directory this status describes. A delegated caller
	// works in its own isolated worktree, not the substrate's checkout, and
	// a subject that cannot see that it is isolated will eventually assume it
	// is not — the same "invited to guess" failure the read-side exists to
	// remove. So the isolation is reported rather than implied.
	Workspace string `json:"workspace,omitempty"`
	// Isolated reports that Workspace is a per-subject worktree.
	Isolated bool `json:"isolated"`
	// Subject is the delegation subject the workspace belongs to ("" for a
	// direct, non-delegated invocation).
	Subject string `json:"subject,omitempty"`
}

// GitBranchListInput asks the Stem to classify a substrate's local branches.
// Read-only, and separate from git.prune on purpose: seeing what is stale must
// not require the ability to remove it.
type GitBranchListInput struct {
	Substrate string `json:"substrate"`
	Origin    string `json:"origin,omitempty"`
}

// GitBranchListSpec is the transport-free branch-listing request.
type GitBranchListSpec struct {
	Substrate string
	Origin    string
}

// GitBranchInfo is one local branch and the evidence for its state.
type GitBranchInfo struct {
	Name string `json:"name"`
	Head string `json:"head"`
	// Upstream is its tracking ref ("" when it has none).
	Upstream string `json:"upstream,omitempty"`
	// Classification is merged, current, default, pull-request-open,
	// pull-request-closed, unpushed, no-pull-request, unverified, or
	// checked-out-elsewhere.
	Classification string `json:"classification"`
	// PullRequest is the pull request the tip belongs to (0 when none).
	PullRequest int `json:"pullRequest,omitempty"`
	// Deletable is true only for a merged branch.
	Deletable bool `json:"deletable"`
	// Reason explains the classification in one line.
	Reason string `json:"reason"`
}

// GitBranchListResult reports every local branch with its evidence.
type GitBranchListResult struct {
	Branches []GitBranchInfo `json:"branches"`
	// Verified reports whether merge state could be established at all; false
	// means nothing is deletable regardless of how it looks locally.
	Verified bool `json:"verified"`
	// DefaultBranch is the resolved default branch, for context.
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

// GitPruneInput asks the Stem to delete local branches whose pull request
// merged. It reports by default; Confirm actually deletes.
type GitPruneInput struct {
	Substrate string `json:"substrate"`
	// Confirm performs the deletion. Omitted or false reports what would be
	// deleted and changes nothing — the safe path is the one taken by
	// accident, which matters most for the ladder operation that can destroy
	// work.
	Confirm bool   `json:"confirm,omitempty"`
	Origin  string `json:"origin,omitempty"`
}

// GitPruneSpec is the transport-free prune request.
type GitPruneSpec struct {
	Substrate string
	Confirm   bool
	Origin    string
}

// GitPrunedBranch records a deleted branch and how to restore it.
type GitPrunedBranch struct {
	Name string `json:"name"`
	// Head is the tip the branch pointed at, so an unwanted prune is a
	// one-line recovery.
	Head        string `json:"head"`
	PullRequest int    `json:"pullRequest,omitempty"`
}

// GitPruneResult reports what was (or would be) removed, and why the rest was
// kept.
type GitPruneResult struct {
	// Confirmed reports whether this run actually deleted anything.
	Confirmed bool `json:"confirmed"`
	// Deleted lists branches removed, or — when not confirmed — the branches
	// that would be.
	Deleted []GitPrunedBranch `json:"deleted"`
	// Kept lists every branch not removed, with the reason.
	Kept []GitBranchInfo `json:"kept"`
	// Verified reports whether merge state could be established at all.
	Verified bool `json:"verified"`
}

// GitOperations is the injection port for delegated git execution. Each member
// may be nil, in which case the corresponding capability reports that it is not
// wired rather than acting.
type GitOperations struct {
	// Commit stages and commits the spec against the resolved workspace under
	// the substrate's configured commit identity. Implementations own
	// substrate resolution, credential resolution, and the deny-closed
	// identity requirement.
	Commit func(ctx context.Context, spec GitCommitSpec) (GitCommitResult, error)
	// Push pushes the resolved workspace's branch to its remote using the
	// substrate's resolved credential. Implementations own substrate
	// resolution, credential resolution, and the Stem-side authenticated push.
	Push func(ctx context.Context, spec GitPushSpec) (GitPushResult, error)
	// PullRequest opens a pull request for the resolved workspace's branch via
	// the GitHub API using the substrate's resolved credential. Implementations
	// own substrate resolution, credential resolution, base-branch resolution,
	// and the duplicate/default-branch guards.
	PullRequest func(ctx context.Context, spec GitPRSpec) (GitPRResult, error)
	// Branch creates or switches to a branch in the resolved workspace.
	// Implementations own substrate resolution and the protected-name and
	// dirty-workspace guards.
	Branch func(ctx context.Context, spec GitBranchSpec) (GitBranchResult, error)
	// Status reports the resolved workspace's git state. Implementations own
	// substrate resolution and must compute the predictive fields from the
	// same predicate the write-side guards use.
	Status func(ctx context.Context, spec GitStatusSpec) (GitStatusResult, error)
	// BranchList classifies the workspace's local branches.
	BranchList func(ctx context.Context, spec GitBranchListSpec) (GitBranchListResult, error)
	// Prune deletes merged branches. Implementations must never delete on
	// anything weaker than forge evidence that the branch's pull request
	// merged.
	Prune func(ctx context.Context, spec GitPruneSpec) (GitPruneResult, error)
}

// WithGit wires the delegated git execution port onto the Service and returns
// the Service for chaining.
func (s *Service) WithGit(operations GitOperations) *Service {
	s.git = operations
	return s
}

// GitCommit validates the request and runs the delegated commit to completion
// via the injected execution port.
func (s *Service) GitCommit(ctx context.Context, in GitCommitInput) (GitCommitResult, error) {
	if s.git.Commit == nil {
		return GitCommitResult{}, fmt.Errorf("git.commit is not wired: construct the Core with WithGit(GitOperations{Commit: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitCommitResult{}, fmt.Errorf("substrate is required")
	}
	if strings.TrimSpace(in.Message) == "" {
		return GitCommitResult{}, fmt.Errorf("message is required")
	}
	// Blank path entries are dropped rather than handed to git verbatim; an
	// all-blank list degrades to "stage all changes", the same as omitting it.
	paths := make([]string, 0, len(in.Paths))
	for _, path := range in.Paths {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	if len(paths) == 0 {
		paths = nil
	}

	spec := GitCommitSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Message:   in.Message,
		Paths:     paths,
		Origin:    in.Origin,
	}
	return s.git.Commit(ctx, spec)
}

// GitPush validates the request and runs the delegated push to completion via
// the injected execution port.
func (s *Service) GitPush(ctx context.Context, in GitPushInput) (GitPushResult, error) {
	if s.git.Push == nil {
		return GitPushResult{}, fmt.Errorf("git.push is not wired: construct the Core with WithGit(GitOperations{Push: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitPushResult{}, fmt.Errorf("substrate is required")
	}
	spec := GitPushSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Branch:    strings.TrimSpace(in.Branch),
		Origin:    in.Origin,
	}
	return s.git.Push(ctx, spec)
}

// GitPR validates the request and opens the pull request to completion via the
// injected execution port.
func (s *Service) GitPR(ctx context.Context, in GitPRInput) (GitPRResult, error) {
	if s.git.PullRequest == nil {
		return GitPRResult{}, fmt.Errorf("git.pr is not wired: construct the Core with WithGit(GitOperations{PullRequest: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitPRResult{}, fmt.Errorf("substrate is required")
	}
	if strings.TrimSpace(in.Title) == "" {
		return GitPRResult{}, fmt.Errorf("title is required")
	}
	spec := GitPRSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Title:     strings.TrimSpace(in.Title),
		Body:      in.Body,
		Head:      strings.TrimSpace(in.Head),
		Base:      strings.TrimSpace(in.Base),
		Draft:     in.Draft,
		Origin:    in.Origin,
	}
	return s.git.PullRequest(ctx, spec)
}

// GitBranch validates the request and runs the branch operation to completion
// via the injected execution port.
func (s *Service) GitBranch(ctx context.Context, in GitBranchInput) (GitBranchResult, error) {
	if s.git.Branch == nil {
		return GitBranchResult{}, fmt.Errorf("git.branch is not wired: construct the Core with WithGit(GitOperations{Branch: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitBranchResult{}, fmt.Errorf("substrate is required")
	}
	if strings.TrimSpace(in.Branch) == "" {
		return GitBranchResult{}, fmt.Errorf("branch is required")
	}
	spec := GitBranchSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Branch:    strings.TrimSpace(in.Branch),
		Origin:    in.Origin,
	}
	return s.git.Branch(ctx, spec)
}

// GitStatus validates the request and reports the workspace's state via the
// injected execution port.
func (s *Service) GitStatus(ctx context.Context, in GitStatusInput) (GitStatusResult, error) {
	if s.git.Status == nil {
		return GitStatusResult{}, fmt.Errorf("git.status is not wired: construct the Core with WithGit(GitOperations{Status: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitStatusResult{}, fmt.Errorf("substrate is required")
	}
	return s.git.Status(ctx, GitStatusSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Origin:    in.Origin,
	})
}

// GitBranchList validates the request and classifies the substrate's branches
// via the injected execution port.
func (s *Service) GitBranchList(ctx context.Context, in GitBranchListInput) (GitBranchListResult, error) {
	if s.git.BranchList == nil {
		return GitBranchListResult{}, fmt.Errorf("git.branch.list is not wired: construct the Core with WithGit(GitOperations{BranchList: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitBranchListResult{}, fmt.Errorf("substrate is required")
	}
	return s.git.BranchList(ctx, GitBranchListSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Origin:    in.Origin,
	})
}

// GitPrune validates the request and runs the prune via the injected execution
// port. Confirm is passed through untouched: the Core must not "helpfully"
// default a destructive flag on.
func (s *Service) GitPrune(ctx context.Context, in GitPruneInput) (GitPruneResult, error) {
	if s.git.Prune == nil {
		return GitPruneResult{}, fmt.Errorf("git.prune is not wired: construct the Core with WithGit(GitOperations{Prune: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitPruneResult{}, fmt.Errorf("substrate is required")
	}
	return s.git.Prune(ctx, GitPruneSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Confirm:   in.Confirm,
		Origin:    in.Origin,
	})
}

// gitCapabilities declares the git family's registry entry, bound to this
// Service's typed method — identical in shape to the other families.
func (s *Service) gitCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapGitCommit,
			Description: "Commit the current state of a substrate's workspace under the substrate's configured commit identity; refused when no identity is configured (deny-closed — an unattributable delegated commit is never created).",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"message":   stringProp("The commit message."),
				"paths": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional workspace-relative paths to stage; empty stages all changes.",
				},
				"origin": stringProp("Interaction origin recorded on the commit (cli, mcp, rest)."),
			}, []string{"substrate", "message"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitCommitInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitCommit(ctx, in)
			},
		},
		{
			Name:        CapGitPush,
			Description: "Push a substrate's branch to its remote using the substrate's configured credential; the push runs on the Stem (the secret zone), never inside a sealed Sprout.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"branch":    stringProp("The branch to push; omit to push the workspace's current branch."),
				"origin":    stringProp("Interaction origin recorded on the push (cli, mcp, rest)."),
			}, []string{"substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitPushInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitPush(ctx, in)
			},
		},
		{
			Name:        CapGitPR,
			Description: "Open a pull request for a substrate's already-pushed branch using the substrate's configured credential. The base branch is read from the repository (never assumed); an existing open pull request for the same head branch is returned instead of duplicated; a head branch that IS the default branch is refused.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"title":     stringProp("The pull request title."),
				"body":      stringProp("The pull request description."),
				"head":      stringProp("The branch to open the pull request from; omit to use the workspace's current branch."),
				"base":      stringProp("The branch to merge into; omit to use the repository's actual default branch."),
				"draft":     map[string]any{"type": "boolean", "description": "Open the pull request as a draft."},
				"origin":    stringProp("Interaction origin recorded on the pull request (cli, mcp, rest)."),
			}, []string{"substrate", "title"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitPRInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitPR(ctx, in)
			},
		},
		{
			Name:        CapGitBranch,
			Description: "Create (or switch to) a feature branch in a substrate's workspace — the governed way to get off the default branch before committing. An existing branch is switched to, never reset; a branch named as the repository's default branch is refused.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"branch":    stringProp("The feature branch to create and switch to."),
				"origin":    stringProp("Interaction origin recorded on the operation (cli, mcp, rest)."),
			}, []string{"substrate", "branch"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitBranchInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitBranch(ctx, in)
			},
		},
		{
			Name:        CapGitStatus,
			Description: "Report a substrate's git state: current branch, resolved default branch and how it was determined, uncommitted changes, ahead/behind against the upstream, and whether a commit would be allowed right now. Read-only, no network. Call this before committing to predict a refusal instead of discovering it.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"origin":    stringProp("Interaction origin recorded on the operation (cli, mcp, rest)."),
			}, []string{"substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitStatusInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitStatus(ctx, in)
			},
		},
		{
			Name:        CapGitBranchList,
			Description: "Classify a substrate's local branches against evidence from GitHub: which are merged, which have an open or closed-without-merging pull request, which were never pushed, and which are held by another subject's workspace. Read-only. Merge state comes from the forge because a squash-merged branch looks unmerged to git itself.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"origin":    stringProp("Interaction origin recorded on the operation (cli, mcp, rest)."),
			}, []string{"substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitBranchListInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitBranchList(ctx, in)
			},
		},
		{
			Name:        CapGitPrune,
			Description: "Delete local branches whose pull request merged, and nothing else. Reports what it WOULD delete unless confirm is true. Never deletes the current or default branch, a branch with an open or closed-unmerged pull request, one the remote has never seen, or one held by another subject's workspace.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"confirm":   map[string]any{"type": "boolean", "description": "Actually delete. Omit to report what would be deleted and change nothing."},
				"origin":    stringProp("Interaction origin recorded on the operation (cli, mcp, rest)."),
			}, []string{"substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitPruneInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitPrune(ctx, in)
			},
		},
	}
}
