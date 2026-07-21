package conductor

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Delegated git commit — the lowest rung of the delegated-execution ladder
// from the Design RFC. RunGitCommit commits the current state of a resolved
// local workspace directory under the substrate's configured commit identity,
// so an external mind never needs to shell out git on the host itself. Like
// commitTerrariumExecution, it runs the Stem's own git against the workspace
// directory on the host — no container is involved.
//
// Attribution rule (security-first, deny-closed): a delegated commit exists
// to be *attributable*, so a missing commit identity — either name or email —
// refuses the whole execution before any git command runs. No commit is ever
// created without a configured identity. This requirement lives ONLY on this
// delegated path: the ordinary Sprout commit path (commitTerrariumExecution)
// keeps its non-breaking ambient-identity default.

// GitCommitExecution is a fully resolved delegated-commit request: a
// workspace on disk, a message, the optional paths to stage, and the
// substrate's resolved credential carrying the commit identity and signing
// configuration.
type GitCommitExecution struct {
	// Workspace is the resolved local workspace directory the commit targets.
	Workspace string
	// Message is the commit message.
	Message string
	// Paths optionally limits staging to the given workspace-relative paths;
	// empty stages all changes.
	Paths []string
	// Credential is the substrate's resolved credential; its Identity must be
	// fully configured (deny-closed) and its Sign configuration is applied
	// when present.
	Credential ResolvedCredential
	// ConfiguredBranch is the substrate's explicitly configured branch, which
	// is the most authoritative answer to "what is the default branch here".
	ConfiguredBranch string
	// AllowDefaultBranchCommit opts OUT of default-branch protection for this
	// substrate. The field is deliberately phrased as the permission rather
	// than the protection, so the zero value is the protected state: a caller
	// that forgets to populate it gets the safe behaviour, and loosening is
	// always an explicit act.
	AllowDefaultBranchCommit bool
}

// GitCommitResult reports a finished delegated commit.
type GitCommitResult struct {
	// Status is "committed" when a commit was created, or "nothing-to-commit"
	// when staging produced no changes (no empty commit is ever created —
	// unlike the Sprout status path, which deliberately allows one).
	Status string
	// CommitHash is the created commit's hash (empty when nothing was
	// committed).
	CommitHash string
}

// ResolveSubstrateCredential resolves a substrate spec against the config's
// named credential profiles into the typed credential the delegated commit
// consumes. Exported for the adapter layer, which owns the wiring between the
// Core's transport-free port and this conductor (the Core itself never
// imports the conductor — see internal/core/boundary_test.go).
func ResolveSubstrateCredential(spec SubstrateSpec, config *SubstratesConfig) (ResolvedCredential, error) {
	var profiles map[string]CredentialProfile
	if config != nil {
		profiles = config.Credentials
	}
	return resolveSubstrateCredential(spec, profiles)
}

// runGitCommitCommandFn is the git seam, injectable for tests that exercise
// validation and the deny-closed identity requirement without a real
// repository.
var runGitCommitCommandFn = runGitCommand

// RunGitCommit stages and commits the workspace under the substrate's
// configured commit identity. Enforcement order is deliberate: the identity
// requirement is checked first, so a refused execution aborts before any git
// command (or any other side effect) runs.
//
// Mode routing: when the resolved credential's CommitMode is CommitModeAPI,
// the commit is delegated to runAPICommit — the GitHub GraphQL
// createCommitOnBranch mutation, server-signed by GitHub — instead of the
// local git path below. Local-mode behavior (the default, empty
// CommitMode, or CommitModeLocal) is unchanged.
func RunGitCommit(ctx context.Context, execution GitCommitExecution) (GitCommitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return GitCommitResult{}, fmt.Errorf("git commit workspace is required")
	}
	if strings.TrimSpace(execution.Message) == "" {
		return GitCommitResult{}, fmt.Errorf("git commit message is required")
	}

	// Deny-closed attribution: an unattributable delegated commit must never
	// be created, so both identity fields are required before anything runs.
	// This requirement is local-mode only: in api mode the GitHub App is the
	// identity (GitHub sets author and committer server-side).
	//
	// It is checked first because it costs nothing — no git invocation, no
	// network — so a refusal on these grounds still runs zero commands.
	localMode := execution.Credential.CommitMode != CommitModeAPI
	if localMode {
		if strings.TrimSpace(execution.Credential.Identity.Name) == "" || strings.TrimSpace(execution.Credential.Identity.Email) == "" {
			return GitCommitResult{}, fmt.Errorf("delegated git commit refused: the substrate has no configured commit identity (set identity name and email in substrates.yaml) — an unattributable delegated commit is never created")
		}
	} else if execution.Credential.Method != CredentialApp {
		// Api mode has no meaning against a Personal Access Token, Secure
		// Shell key, or ambient credential — and this is a pure inspection of
		// the credential, so it belongs in the same zero-cost phase.
		return GitCommitResult{}, fmt.Errorf("commit mode %q requires a GitHub App connection (auth.method: app)", CommitModeAPI)
	}

	// Default-branch protection, applied before staging and before any mode
	// routing, and therefore before anything that has to be unwound. This is
	// the earliest point at which the expensive failure (work committed onto
	// the default branch, then reversed off it) can be prevented, so it is
	// where the check belongs. It covers api mode too: that mode commits
	// straight onto the remote branch, where landing on the default branch is
	// worse, not better.
	if err := guardDefaultBranchCommit(ctx, execution); err != nil {
		return GitCommitResult{}, err
	}

	if !localMode {
		return runAPICommit(ctx, execution)
	}

	// Stage: everything when no paths are given, else exactly the given paths.
	addArgs := []string{"add", "-A"}
	if len(execution.Paths) > 0 {
		addArgs = append([]string{"add", "--"}, execution.Paths...)
	}
	if _, err := runGitCommitCommandFn(ctx, execution.Workspace, addArgs...); err != nil {
		return GitCommitResult{}, err
	}

	// Nothing staged means nothing to commit: report it cleanly instead of
	// creating an empty commit.
	staged, err := runGitCommitCommandFn(ctx, execution.Workspace, "diff", "--cached", "--name-only")
	if err != nil {
		return GitCommitResult{}, err
	}
	if strings.TrimSpace(staged) == "" {
		return GitCommitResult{Status: "nothing-to-commit"}, nil
	}

	// Signing and identity config (`-c ...`) must precede the `commit`
	// subcommand — same ordering and precedence as commitTerrariumExecution.
	configArgs := append(signingGitConfigArgs(execution.Credential.Sign), identityGitConfigArgs(execution.Credential.Identity)...)
	commitArgs := append(append([]string{}, configArgs...), "commit", "-m", execution.Message)
	if _, err := runGitCommitCommandFn(ctx, execution.Workspace, commitArgs...); err != nil {
		return GitCommitResult{}, err
	}

	commitHash, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return GitCommitResult{}, err
	}

	return GitCommitResult{Status: "committed", CommitHash: commitHash}, nil
}

// guardDefaultBranchCommit refuses a delegated commit whose target branch is
// the repository's default branch, unless the substrate has explicitly opted
// out. The refusal names the branch, says how Tendril determined it, and
// points at the operation that resolves the situation — a guardrail with no
// stated next move just pushes the caller off the governed path.
func guardDefaultBranchCommit(ctx context.Context, execution GitCommitExecution) error {
	assessment := AssessDefaultBranchCommit(ctx, execution.Workspace, execution.ConfiguredBranch, execution.AllowDefaultBranchCommit)
	if assessment.CommitAllowed {
		return nil
	}
	if assessment.DetachedHead {
		return fmt.Errorf("delegated git commit refused: the workspace is on no branch (detached head) — a commit here is reachable only by hash and is silently stranded by the next checkout. Create a feature branch first (tendril git branch --substrate <name> --branch <feature-branch>), then commit")
	}
	return fmt.Errorf("delegated git commit refused: the workspace is on %q, the repository's default branch — default branch %s. Create a feature branch first (tendril git branch --substrate <name> --branch <feature-branch>), then commit; committing here is what later costs a rebase or a commit reversed off the default branch. To allow it for this repository, set protectDefaultBranch: false on the substrate", assessment.Branch, assessment.DefaultBranch.Describe())
}

// DefaultBranchCommitAssessment is the single answer to "may a commit happen
// in this workspace right now, and why" — computed once and consumed by two
// callers with different jobs: the commit guard, which turns a refusal into an
// error, and git.status, which reports it before anything is attempted.
//
// The sharing is the point, not an optimization. If status answered this
// question with its own logic it would eventually disagree with the guard, and
// a status that says "fine" followed by a commit that is refused is worse than
// no status at all: it teaches a subject to distrust the read-side and go back
// to guessing. One predicate, two consumers, no drift — and an agreement test
// pins it.
type DefaultBranchCommitAssessment struct {
	// Branch is the workspace's current branch ("" when it cannot be read:
	// a repository with no commits, or a detached head).
	Branch string
	// DefaultBranch is how the default branch was resolved, including the
	// undetermined case that engages the protection floor.
	DefaultBranch DefaultBranchResolution
	// OnDefaultBranch is the factual answer: the current branch is the
	// protected default branch. It ignores any opt-out.
	OnDefaultBranch bool
	// DetachedHead reports a workspace on no branch at all. A commit there is
	// reachable only by hash and is trivially lost, so it is refused — and an
	// isolated delegated workspace starts detached on purpose, which makes
	// "create a branch first" the read-side's advice rather than a surprise.
	DetachedHead bool
	// CommitAllowed is the predictive answer, accounting for the substrate's
	// opt-out. This is exactly what the commit guard acts on.
	CommitAllowed bool
}

// AssessDefaultBranchCommit resolves the default branch offline and reports
// whether a commit would be permitted in this workspace.
func AssessDefaultBranchCommit(ctx context.Context, workspace, configuredBranch string, allowDefaultBranchCommit bool) DefaultBranchCommitAssessment {
	if ctx == nil {
		ctx = context.Background()
	}
	assessment := DefaultBranchCommitAssessment{
		DefaultBranch: ResolveDefaultBranchLocal(ctx, workspace, configuredBranch),
		CommitAllowed: true,
	}

	current, err := runGitCommitCommandFn(ctx, workspace, "branch", "--show-current")
	if err != nil {
		// A workspace whose branch cannot be read at all (a repository with no
		// commits yet) is not on the default branch by definition, and the
		// downstream commands report their own failures more precisely than a
		// guess here would.
		return assessment
	}
	assessment.Branch = strings.TrimSpace(current)
	if assessment.Branch == "" {
		// No branch, but a resolvable head means a detached head: committing
		// here produces work reachable only by hash, which the next checkout
		// silently strands. Refuse, and say what to do instead.
		if head, headErr := runGitCommitCommandFn(ctx, workspace, "rev-parse", "HEAD"); headErr == nil && strings.TrimSpace(head) != "" {
			assessment.DetachedHead = true
			assessment.CommitAllowed = allowDefaultBranchCommit
		}
		return assessment
	}

	assessment.OnDefaultBranch = assessment.DefaultBranch.IsProtected(assessment.Branch)
	assessment.CommitAllowed = !assessment.OnDefaultBranch || allowDefaultBranchCommit
	return assessment
}

// API-mode delegated commit (commit: api) — the recommended default git
// connection posture: a GitHub App connection creates the commit server-side
// via the GraphQL createCommitOnBranch mutation, so GitHub itself signs it
// (a verified commit with no local key material) rather than the Stem
// running local git and an optional GPG/SSH signature.
//
// IMPORTANT semantic difference from local mode: createCommitOnBranch creates
// the commit directly ON THE REMOTE BRANCH and advances the remote ref — it
// does not touch the local workspace at all. Api-mode commit therefore also
// PUBLISHES the change; a subsequent push is unnecessary (and would be a
// no-op once the local workspace is later synced, e.g. via `git fetch` +
// reset, since the remote already carries the new commit).

// createCommitOnBranchMutation is the GraphQL document RunGitCommit's api
// mode sends. Its shape follows GitHub's CreateCommitOnBranchInput schema:
// https://docs.github.com/en/graphql/reference/mutations#createcommitonbranch
const createCommitOnBranchMutation = `mutation($input: CreateCommitOnBranchInput!) {
  createCommitOnBranch(input: $input) {
    commit {
      oid
    }
  }
}`

// apiCommitFileAddition is one GraphQL FileAddition: a path plus its full
// current contents, base64-encoded (the mutation always sends whole-file
// contents, never a diff/patch).
type apiCommitFileAddition struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

// apiCommitFileDeletion is one GraphQL FileDeletion: just the path.
type apiCommitFileDeletion struct {
	Path string `json:"path"`
}

// apiCommitFileChanges is the GraphQL FileChanges input: every file this
// commit adds/modifies (Additions) or removes (Deletions).
type apiCommitFileChanges struct {
	Additions []apiCommitFileAddition `json:"additions"`
	Deletions []apiCommitFileDeletion `json:"deletions"`
}

// apiCommitBranch is the GraphQL CommittableBranch input identifying the
// target branch by "owner/repo" and branch name.
type apiCommitBranch struct {
	RepositoryNameWithOwner string `json:"repositoryNameWithOwner"`
	BranchName              string `json:"branchName"`
}

// apiCommitMessage is the GraphQL CommitMessage input: the headline (commit
// subject, i.e. the message's first line) and the optional body (everything
// after the first blank line — conventional commit-message shape).
type apiCommitMessage struct {
	Headline string `json:"headline"`
	Body     string `json:"body,omitempty"`
}

// createCommitOnBranchInput is the GraphQL CreateCommitOnBranchInput.
// ExpectedHeadOid is the safety check GitHub performs server-side: the
// mutation is refused (not silently rebased) if the branch has moved since
// the workspace's HEAD was read, avoiding a lost-update race.
type createCommitOnBranchInput struct {
	Branch          apiCommitBranch      `json:"branch"`
	Message         apiCommitMessage     `json:"message"`
	ExpectedHeadOid string               `json:"expectedHeadOid"`
	FileChanges     apiCommitFileChanges `json:"fileChanges"`
}

// createCommitOnBranchResponse decodes the mutation's "data" object.
type createCommitOnBranchResponse struct {
	CreateCommitOnBranch struct {
		Commit struct {
			Oid string `json:"oid"`
		} `json:"commit"`
	} `json:"createCommitOnBranch"`
}

// runAPICommit implements the commit: api execution mode. It never touches
// the local git index or working tree state (no staging, no local commit) —
// it reads the workspace's current file contents and the remote's expected
// head, and asks GitHub to create the commit remotely.
func runAPICommit(ctx context.Context, execution GitCommitExecution) (GitCommitResult, error) {
	cred := execution.Credential

	// Identity for an api-mode commit is the GitHub App itself (GitHub sets
	// author and committer server-side), so — unlike local mode — no local
	// identity check runs here. What IS required, deny-closed, is that the
	// connection actually is a GitHub App: api mode has no meaning (and no
	// way to authenticate the mutation) against a PAT, SSH key, or ambient
	// credential.
	if cred.Method != CredentialApp {
		return GitCommitResult{}, fmt.Errorf("commit mode %q requires a GitHub App connection (auth.method: app)", CommitModeAPI)
	}

	originURL, err := runGitCommitCommandFn(ctx, execution.Workspace, "remote", "get-url", "origin")
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: resolve origin remote: %w", err)
	}
	originURL = strings.TrimSpace(originURL)
	owner, repo, err := parseOwnerRepo(originURL)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: %w", err)
	}

	branch, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: determine current branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: unable to determine the workspace's current branch (detached HEAD is not supported)")
	}

	headOid, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: resolve HEAD: %w", err)
	}
	headOid = strings.TrimSpace(headOid)

	additions, deletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, execution.Paths)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: enumerate changes: %w", err)
	}
	// No changes means nothing to commit: report it cleanly instead of
	// asking GitHub to create an empty commit, mirroring the local path.
	if len(additions) == 0 && len(deletions) == 0 {
		return GitCommitResult{Status: "nothing-to-commit"}, nil
	}

	token, err := githubAppInstallationToken(ctx, cred.App, originURL)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: github app auth: %w", err)
	}

	headline, body := splitCommitMessage(execution.Message)
	input := createCommitOnBranchInput{
		Branch: apiCommitBranch{
			RepositoryNameWithOwner: owner + "/" + repo,
			BranchName:              branch,
		},
		Message:         apiCommitMessage{Headline: headline, Body: body},
		ExpectedHeadOid: headOid,
		FileChanges: apiCommitFileChanges{
			Additions: additions,
			Deletions: deletions,
		},
	}

	var response createCommitOnBranchResponse
	if err := githubGraphQLPost(ctx, token, createCommitOnBranchMutation, map[string]any{"input": input}, &response); err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: %w", err)
	}
	oid := strings.TrimSpace(response.CreateCommitOnBranch.Commit.Oid)
	if oid == "" {
		return GitCommitResult{}, fmt.Errorf("api-mode commit: github returned no commit oid")
	}

	return GitCommitResult{Status: "committed", CommitHash: oid}, nil
}

// splitCommitMessage splits a commit message into its headline (first line)
// and body (everything after, trimmed), matching conventional git-commit
// message shape.
func splitCommitMessage(message string) (headline, body string) {
	parts := strings.SplitN(message, "\n", 2)
	headline = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		body = strings.TrimSpace(parts[1])
	}
	return headline, body
}

// apiCommitFileChangesFromWorkspace enumerates the workspace's current
// changes (tracked modifications, deletions, and untracked files — the same
// scope `git add -A` would stage) via `git status --porcelain`, and reads
// each surviving addition's current file contents. When paths is non-empty,
// only entries whose path is in that list are included, matching the local
// path's optional Paths staging filter.
func apiCommitFileChangesFromWorkspace(ctx context.Context, workspace string, paths []string) ([]apiCommitFileAddition, []apiCommitFileDeletion, error) {
	// -uall recurses into untracked directories instead of reporting the
	// directory itself; -z NUL-separates entries so a path is never
	// corrupted by trimming (the leading space of a worktree-only status
	// code, e.g. " M path", is otherwise indistinguishable from padding —
	// see the identical rationale at docker.go's own -z status read).
	status, err := runGitCommandRawOutput(ctx, workspace, "status", "--porcelain", "-uall", "-z")
	if err != nil {
		return nil, nil, err
	}

	filter := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if normalized := filepath.ToSlash(strings.TrimSpace(p)); normalized != "" {
			filter[normalized] = struct{}{}
		}
	}
	allowed := func(path string) bool {
		if len(filter) == 0 {
			return true
		}
		_, ok := filter[path]
		return ok
	}

	var additions []apiCommitFileAddition
	var deletions []apiCommitFileDeletion
	seenAddition := make(map[string]struct{})
	seenDeletion := make(map[string]struct{})

	addAddition := func(path string) error {
		if _, ok := seenAddition[path]; ok || !allowed(path) {
			return nil
		}
		contents, readErr := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		additions = append(additions, apiCommitFileAddition{
			Path:     path,
			Contents: base64.StdEncoding.EncodeToString(contents),
		})
		seenAddition[path] = struct{}{}
		return nil
	}
	addDeletion := func(path string) {
		if _, ok := seenDeletion[path]; ok || !allowed(path) {
			return
		}
		deletions = append(deletions, apiCommitFileDeletion{Path: path})
		seenDeletion[path] = struct{}{}
	}

	entries := strings.Split(status, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		code := entry[:2]
		path := filepath.ToSlash(entry[3:])

		switch {
		case code[0] == 'R' || code[0] == 'C':
			// A rename/copy entry is "XY newpath", followed by the original
			// path as its own NUL-separated field with no status prefix —
			// the source is a deletion, the destination an addition.
			i++
			var oldPath string
			if i < len(entries) {
				oldPath = filepath.ToSlash(entries[i])
			}
			if err := addAddition(path); err != nil {
				return nil, nil, err
			}
			if oldPath != "" {
				addDeletion(oldPath)
			}
		case strings.Contains(code, "D"):
			addDeletion(path)
		default:
			if err := addAddition(path); err != nil {
				return nil, nil, err
			}
		}
	}

	sort.Slice(additions, func(i, j int) bool { return additions[i].Path < additions[j].Path })
	sort.Slice(deletions, func(i, j int) bool { return deletions[i].Path < deletions[j].Path })

	if additions == nil {
		additions = []apiCommitFileAddition{}
	}
	if deletions == nil {
		deletions = []apiCommitFileDeletion{}
	}
	return additions, deletions, nil
}

// runGitPushCommandFn is the authenticated-push seam, injectable for tests that
// exercise branch resolution and credential materialization without a real
// remote.
var runGitPushCommandFn = runGitCommandWithEnv

// GitPushExecution is a fully resolved delegated-push request: a workspace on
// disk, the branch to push (empty means the workspace's current branch), and
// the substrate's resolved credential carrying the authentication material.
type GitPushExecution struct {
	// Workspace is the resolved local workspace directory the push targets.
	Workspace string
	// Branch optionally names the branch to push; empty pushes the workspace's
	// current branch.
	Branch string
	// Credential is the substrate's resolved credential; its authentication
	// material (Personal Access Token, GitHub App token, or SSH key) is
	// materialized into the push process environment, never persisted or placed
	// on the command line.
	Credential ResolvedCredential
}

// GitPushResult reports a finished delegated push.
type GitPushResult struct {
	// Status is "pushed" when the push command succeeded (git treats an
	// already-current ref as a successful no-op push).
	Status string
	// Branch is the branch that was pushed.
	Branch string
}

// RunGitPush pushes the workspace's branch to its origin remote using the
// substrate's resolved credential. The push runs here on the Stem — the sole
// secret-holding zone — never inside a sealed Sprout, mirroring the
// authenticated push the ordinary Sprout pipeline performs
// (pushTerrariumCommit). The secret travels only in the process environment via
// materializeGitAuth: never in the remote URL, the command line, or
// .git/config.
func RunGitPush(ctx context.Context, execution GitPushExecution) (GitPushResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return GitPushResult{}, fmt.Errorf("git push workspace is required")
	}

	targetBranch := strings.TrimSpace(execution.Branch)
	if targetBranch == "" {
		current, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
		if err != nil {
			return GitPushResult{}, err
		}
		targetBranch = strings.TrimSpace(current)
	}
	if targetBranch == "" {
		return GitPushResult{}, fmt.Errorf("unable to determine branch for push (the workspace has no current branch; pass an explicit branch)")
	}
	targetBranch = strings.TrimPrefix(targetBranch, "refs/heads/")

	originURL, err := runGitCommitCommandFn(ctx, execution.Workspace, "remote", "get-url", "origin")
	if err != nil {
		return GitPushResult{}, err
	}

	pushEnv, authErr := materializeGitAuth(ctx, execution.Credential, strings.TrimSpace(originURL))
	if authErr != nil {
		return GitPushResult{}, authErr
	}

	if _, err := runGitPushCommandFn(ctx, execution.Workspace, pushEnv, "push", "origin", "--", "HEAD:refs/heads/"+targetBranch); err != nil {
		return GitPushResult{}, err
	}

	return GitPushResult{Status: "pushed", Branch: targetBranch}, nil
}

// Delegated pull request — the top rung of the delegated-execution ladder, and
// the last mile that previously forced a delegation subject off Tendril's governed path
// (shelling out to the GitHub command line tool, or guessing at credentials).
// Like the push it runs here on the Stem, the sole secret-holding zone; a
// Sprout stays network-sealed and never talks to GitHub.
//
// Three rules are enforced, all of them "look at reality before acting"
// rather than "assume and fail later":
//
//  1. The base branch is READ from the repository (its real default branch)
//     when the caller does not name one. A default branch is never assumed to
//     be "main" — that assumption is the recurring, expensive failure this
//     capability exists to design out (work opened against, or landed on, the
//     wrong branch, then paid for in rebases and reversed commits).
//  2. A head branch that IS the default branch is refused outright, before
//     anything is created. There is deliberately no override flag: the way
//     past the guard is to name a real feature branch, not to let the caller
//     wave it through.
//  3. An existing open pull request for the same head branch is returned
//     untouched rather than duplicated. Its title and body are deliberately
//     NOT rewritten — a repeat call must not silently overwrite a description
//     a human may have edited.

// GitPRExecution is a fully resolved delegated pull-request request: a
// workspace on disk, the pull request's content, the optional head/base
// branches, and the substrate's resolved credential carrying the API token.
type GitPRExecution struct {
	// Workspace is the resolved local workspace directory whose origin remote
	// and current branch address the pull request.
	Workspace string
	// Title is the pull request title.
	Title string
	// Body is the optional pull request description.
	Body string
	// Head optionally names the branch to open the pull request from; empty
	// uses the workspace's current branch.
	Head string
	// Base optionally names the branch to merge into; empty resolves the
	// repository's real default branch from the GitHub API.
	Base string
	// Draft opens the pull request as a draft.
	Draft bool
	// Credential is the substrate's resolved credential. Its GitHub App
	// installation token or Personal Access Token authenticates the API calls;
	// a connection with neither is refused deny-closed.
	Credential ResolvedCredential
}

// GitPRResult reports a finished delegated pull-request operation.
type GitPRResult struct {
	// Status is "created" for a newly opened pull request, or "exists" when an
	// open pull request for the same head branch was already there.
	Status string
	// Number is the pull request number.
	Number int
	// URL is the pull request's web address.
	URL string
	// Head is the branch the pull request was opened from.
	Head string
	// Base is the branch the pull request merges into, as actually resolved.
	Base string
}

// githubPullRequest is the subset of GitHub's pull-request resource this path
// reads back, shared by the list and create calls.
type githubPullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Base    struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// createPullRequestBody is GitHub's REST create-a-pull-request payload.
type createPullRequestBody struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body,omitempty"`
	Draft bool   `json:"draft,omitempty"`
}

// pullRequestAPIToken resolves the bearer token the pull-request API calls
// authenticate with, per connection posture. Deny-closed: a posture that
// cannot reach the API at all (Secure Shell, or no credential) is refused with
// an error naming the two postures that work, rather than letting the caller
// discover it through an opaque GitHub failure. Secure Shell keys can push
// code but cannot open a pull request — that is a property of the transport,
// not a Tendril limitation.
func pullRequestAPIToken(ctx context.Context, cred ResolvedCredential, originURL string) (string, error) {
	switch cred.Method {
	case CredentialApp:
		token, err := githubAppInstallationToken(ctx, cred.App, originURL)
		if err != nil {
			return "", fmt.Errorf("github app auth: %w", err)
		}
		return token, nil
	case CredentialPAT:
		if strings.TrimSpace(cred.TokenValue) == "" {
			return "", fmt.Errorf("delegated pull request refused: the substrate's Personal Access Token environment variable (%s) is empty", cred.TokenEnv)
		}
		return cred.TokenValue, nil
	default:
		return "", fmt.Errorf("delegated pull request refused: the substrate's connection (auth method %q) has no GitHub API credential — opening a pull request requires a GitHub App (auth.method: app) or a fine-grained Personal Access Token (auth.method: pat)", cred.Method)
	}
}

// RunGitPullRequest opens a pull request for a branch that has already been
// pushed. It never pushes: git.pr and git.push are separately grantable
// operation-classes, so a subject granted only git.pr must not be able to
// publish a branch as a side effect.
func RunGitPullRequest(ctx context.Context, execution GitPRExecution) (GitPRResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return GitPRResult{}, fmt.Errorf("pull request workspace is required")
	}
	if strings.TrimSpace(execution.Title) == "" {
		return GitPRResult{}, fmt.Errorf("pull request title is required")
	}

	originURL, err := runGitCommitCommandFn(ctx, execution.Workspace, "remote", "get-url", "origin")
	if err != nil {
		return GitPRResult{}, fmt.Errorf("pull request: resolve origin remote: %w", err)
	}
	originURL = strings.TrimSpace(originURL)
	owner, repo, err := parseOwnerRepo(originURL)
	if err != nil {
		return GitPRResult{}, fmt.Errorf("pull request: %w", err)
	}

	// Head is read from actual workspace state when unnamed — never assumed.
	head := strings.TrimPrefix(strings.TrimSpace(execution.Head), "refs/heads/")
	if head == "" {
		current, branchErr := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
		if branchErr != nil {
			return GitPRResult{}, fmt.Errorf("pull request: determine current branch: %w", branchErr)
		}
		head = strings.TrimSpace(current)
	}
	if head == "" {
		return GitPRResult{}, fmt.Errorf("pull request: unable to determine the head branch (the workspace has no current branch; pass an explicit head)")
	}

	token, err := pullRequestAPIToken(ctx, execution.Credential, originURL)
	if err != nil {
		return GitPRResult{}, err
	}

	// Base is READ, never assumed — through the shared resolver, so this path
	// and the Sprout path agree on what the default branch is.
	base := strings.TrimPrefix(strings.TrimSpace(execution.Base), "refs/heads/")
	resolution := ResolveDefaultBranch(ctx, execution.Workspace, execution.Base, execution.Credential)
	if base == "" {
		if !resolution.Known() {
			return GitPRResult{}, fmt.Errorf("pull request: could not determine the default branch for %s/%s (%s) — pass an explicit base", owner, repo, resolution.Describe())
		}
		base = resolution.Branch
	}

	// Deny-closed guard: opening a pull request FROM the default branch means
	// the work was committed to the wrong branch. Refuse while it is still
	// cheap to fix, instead of after a merge that must be unpicked. The floor
	// applies when the default branch could not be determined, so an unknown
	// answer hardens rather than disables the guard.
	if head == base || resolution.IsProtected(head) {
		return GitPRResult{}, fmt.Errorf("delegated pull request refused: the head branch %q is the repository's default branch — commit the work on a feature branch and open the pull request from that (a pull request from the default branch into itself is the shape that later costs a rebase or a reversed commit)", head)
	}

	// Look before creating: an open pull request for this head branch is
	// returned as-is, so a repeat call is idempotent and never duplicates.
	var existing []githubPullRequest
	listPath := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s", owner, repo, url.QueryEscape(owner+":"+head))
	if err := githubRESTRequest(ctx, http.MethodGet, listPath, token, nil, &existing); err != nil {
		return GitPRResult{}, fmt.Errorf("pull request: check for an existing pull request: %w", err)
	}
	if len(existing) > 0 {
		open := existing[0]
		existingBase := strings.TrimSpace(open.Base.Ref)
		if existingBase == "" {
			existingBase = base
		}
		return GitPRResult{
			Status: "exists",
			Number: open.Number,
			URL:    open.HTMLURL,
			Head:   head,
			Base:   existingBase,
		}, nil
	}

	var created githubPullRequest
	body := createPullRequestBody{
		Title: execution.Title,
		Head:  head,
		Base:  base,
		Body:  execution.Body,
		Draft: execution.Draft,
	}
	if err := githubRESTRequest(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), token, body, &created); err != nil {
		return GitPRResult{}, fmt.Errorf("pull request: %w", err)
	}
	if created.Number == 0 {
		return GitPRResult{}, fmt.Errorf("pull request: github returned no pull request number")
	}

	return GitPRResult{
		Status: "created",
		Number: created.Number,
		URL:    created.HTMLURL,
		Head:   head,
		Base:   base,
	}, nil
}

// Delegated branch creation — the operation that makes default-branch
// protection actionable. Without it, refusing a commit on the default branch
// tells a subject what it may not do and offers nothing it may do, which sends
// it back to running git on the host: the exact behaviour the governed path
// exists to replace.
//
// It is deliberately the narrowest useful operation: create a branch from the
// current state and switch to it. No delete, no rename, no reset, no upstream
// tracking changes — a branch operation that can destroy work would need a far
// stronger authorization story than "the subject asked".

// GitBranchExecution is a fully resolved delegated branch request.
type GitBranchExecution struct {
	// Workspace is the resolved local workspace directory.
	Workspace string
	// Branch is the branch to create and switch to.
	Branch string
	// ConfiguredBranch is the substrate's explicitly configured branch, fed to
	// the default-branch resolver.
	ConfiguredBranch string
	// Credential is the substrate's resolved credential, used only to let the
	// resolver ask the interface which branch is the default.
	Credential ResolvedCredential
}

// GitBranchResult reports a finished branch operation.
type GitBranchResult struct {
	// Status is "created" for a new branch, or "switched" when it already
	// existed.
	Status string
	// Branch is the branch now checked out.
	Branch string
	// PreviousBranch is the branch the workspace was on beforehand.
	PreviousBranch string
}

// invalidBranchNameChars are shell/ref characters refused outright, so a
// branch name can never be a vector for argument or path injection even
// though every git invocation here is already argument-safe.
const invalidBranchNameChars = " \t\n\\:?*[]~^\"'`$;|&<>()"

// validateBranchName refuses names git would reject, plus a conservative
// superset that keeps a delegated caller from constructing anything exotic.
func validateBranchName(branch string) error {
	name := strings.TrimSpace(branch)
	switch {
	case name == "":
		return fmt.Errorf("branch name is required")
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("branch name %q may not start with a dash (it would be read as a flag)", name)
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/"):
		return fmt.Errorf("branch name %q may not start or end with a slash", name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("branch name %q may not contain %q", name, "..")
	case strings.HasSuffix(name, ".lock"):
		return fmt.Errorf("branch name %q may not end with .lock", name)
	case strings.ContainsAny(name, invalidBranchNameChars):
		return fmt.Errorf("branch name %q contains a character that is not allowed", name)
	}
	return nil
}

// RunGitBranch creates the branch and switches to it, or switches to it when
// it already exists. An existing branch is never reset or force-moved: the
// look-before-acting rule that returns an existing pull request untouched
// applies here too, and the cost of getting it wrong is higher — a force-move
// discards commits.
func RunGitBranch(ctx context.Context, execution GitBranchExecution) (GitBranchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return GitBranchResult{}, fmt.Errorf("git branch workspace is required")
	}
	branch := strings.TrimPrefix(strings.TrimSpace(execution.Branch), "refs/heads/")
	if err := validateBranchName(branch); err != nil {
		return GitBranchResult{}, err
	}

	// Refuse to create a branch named as the repository's default branch. The
	// same reasoning as the pull-request guard: a second branch by that name
	// is never what the caller meant, and the confusion it creates is paid
	// for later.
	resolution := ResolveDefaultBranchLocal(ctx, execution.Workspace, execution.ConfiguredBranch)
	if resolution.IsProtected(branch) {
		return GitBranchResult{}, fmt.Errorf("delegated branch refused: %q is the repository's default branch (default branch %s) — choose a feature branch name", branch, resolution.Describe())
	}

	previous := ""
	if current, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current"); err == nil {
		previous = strings.TrimSpace(current)
	}
	if previous == branch {
		return GitBranchResult{Status: "switched", Branch: branch, PreviousBranch: previous}, nil
	}

	// Look before acting: does the branch already exist?
	_, existsErr := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	exists := existsErr == nil

	// Uncommitted work is carried onto a NEW branch (that is the normal
	// "I started editing before branching" recovery, and it loses nothing).
	// Switching to an EXISTING branch with a dirty workspace is refused: git
	// would either fail on conflicting files or silently carry the changes
	// somewhere the caller did not intend.
	if exists {
		status, err := runGitCommandRawOutput(ctx, execution.Workspace, "status", "--porcelain", "-uall", "-z")
		if err != nil {
			return GitBranchResult{}, err
		}
		if strings.TrimSpace(strings.ReplaceAll(status, "\x00", "")) != "" {
			return GitBranchResult{}, fmt.Errorf("delegated branch refused: the workspace has uncommitted changes and %q already exists — commit or set those changes aside before switching, so work is never carried onto a branch you did not expect", branch)
		}
		if _, err := runGitCommitCommandFn(ctx, execution.Workspace, "checkout", branch); err != nil {
			return GitBranchResult{}, err
		}
		return GitBranchResult{Status: "switched", Branch: branch, PreviousBranch: previous}, nil
	}

	if _, err := runGitCommitCommandFn(ctx, execution.Workspace, "checkout", "-b", branch); err != nil {
		return GitBranchResult{}, err
	}
	return GitBranchResult{Status: "created", Branch: branch, PreviousBranch: previous}, nil
}
