package conductor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func runGitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitCommandWithEnv(ctx, dir, nil, args...)
}

// runGitCommandWithEnv runs git with additional environment entries appended to
// the process environment (e.g. GIT_SSH_COMMAND for SSH-authenticated pushes).
// The output is whitespace-trimmed; parsers that need byte-exact output (for
// example NUL-separated porcelain, whose first status byte may itself be a
// space) must use runGitCommandRawOutput instead.
func runGitCommandWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Start the fixed git executable directly; args are never interpreted by a
	// shell. Caller-controlled refs are separated with git's `--` marker or
	// passed as explicit option values such as `-m`.
	cmd := exec.CommandContext(ctx, "git")
	cmd.Args = append([]string{"git"}, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}

	return strings.TrimSpace(string(output)), nil
}

// runGitCommandRawOutput runs git and returns the output byte-for-byte. Needed
// wherever the format is positional: trimming a porcelain status listing eats
// the leading space of an unstaged-modification entry and every fixed-offset
// slice after it lands one byte into the path.
func runGitCommandRawOutput(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}

// runGitCommandBoundedRawOutput runs git while retaining at most maxOutput
// bytes from stdout and stderr. The truncation flag lets callers preserve a
// deterministic marker without materializing an unbounded diff in memory.
func runGitCommandBoundedRawOutput(ctx context.Context, dir string, maxOutput int, args ...string) (string, bool, error) {
	if maxOutput < 1 {
		return "", false, fmt.Errorf("git output bound must be positive")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var output boundedGitOutput
	output.limit = maxOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", output.truncated, fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}

	return output.String(), output.truncated, nil
}

type boundedGitOutput struct {
	bytes.Buffer
	mu        sync.Mutex
	limit     int
	truncated bool
}

func (output *boundedGitOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()

	remaining := output.limit - output.Len()
	if remaining <= 0 {
		output.truncated = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = output.Buffer.Write(data[:remaining])
		output.truncated = true
		return len(data), nil
	}
	_, _ = output.Buffer.Write(data)
	return len(data), nil
}

func (output *boundedGitOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.Buffer.String()
}

// isGitRepo checks if the given path is inside a git repository.
func isGitRepo(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = path
	err := cmd.Run()
	return err == nil
}

// Delegated git commit — the lowest rung of the delegated-execution ladder
// from the Design RFC. RunGitCommit commits the current state of a resolved
// local workspace directory under the substrate's configured commit identity,
// so a Pollinator never needs to shell out git on the host itself. Like
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
// no status at all: it teaches a Pollinator to distrust the read-side and go back
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
// pollen, i.e. the message's first line) and the optional body (everything
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
	if _, err := githubGraphQLPost(ctx, token, createCommitOnBranchMutation, map[string]any{"input": input}, &response); err != nil {
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

	if err := requireGitHubPushAuth(pushEnv, strings.TrimSpace(originURL), execution.Credential); err != nil {
		return GitPushResult{}, err
	}

	if _, err := runGitPushCommandFn(ctx, execution.Workspace, pushEnv, "push", "origin", "--", "HEAD:refs/heads/"+targetBranch); err != nil {
		return GitPushResult{}, err
	}

	return GitPushResult{Status: "pushed", Branch: targetBranch}, nil
}

// Delegated pull request. It runs on the Stem, the sole secret-holding zone; a
// Sprout stays network-sealed and never talks to GitHub.
//
// Three rules:
//
//  1. The base branch is READ from the repository when the caller does not name
//     one. A default-branch name is never assumed.
//  2. A head branch that IS the default branch is refused outright, before
//     anything is created. There is deliberately no override flag.
//  3. An existing open pull request for the same head branch is returned
//     untouched rather than duplicated, and its title and body are NOT
//     rewritten — a repeat call must not overwrite a description a human edited.

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
// operation-classes, so a Pollinator granted only git.pr must not be able to
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
// tells a Pollinator what it may not do and offers nothing it may do, which sends
// it back to running git on the host: the exact behaviour the governed path
// exists to replace.
//
// It is deliberately the narrowest useful operation: create a branch from the
// current state and switch to it. No delete, no rename, no reset, no upstream
// tracking changes — a branch operation that can destroy work would need a far
// stronger authorization story than "the Pollinator asked".

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

const (
	apiFruitOutcomePreMutationFailure    = "pre-mutation-failure"
	apiFruitOutcomeTargetRefConflict     = "target-ref-conflict"
	apiFruitOutcomeTargetRefAbsent       = "target-ref-absent"
	apiFruitOutcomeUnexpectedState       = "unexpected-target-state"
	apiFruitOutcomeReconciliationFailure = "reconciliation-unavailable"
	apiFruitOutcomeRetryExhausted        = "retry-exhausted"
)

// apiFruitPublicationFailure is the safe failure descriptor shared by the
// managed Sprout and Seed publication paths. Its fields contain only bounded
// state-machine values and an optional GitHub request ID; upstream bodies and
// request contents never cross this boundary.
type apiFruitPublicationFailure struct {
	Phase     string
	Outcome   string
	RetrySafe bool
	RequestID string
	Message   string
}

func (e *apiFruitPublicationFailure) Error() string {
	if e == nil {
		return "api Fruit publication failed"
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "managed API Fruit publication could not establish an authoritative outcome"
	}
	if e.RequestID != "" {
		return fmt.Sprintf("api Fruit publication failed during %s (%s; GitHub request %s): %s", e.Phase, e.Outcome, e.RequestID, message)
	}
	return fmt.Sprintf("api Fruit publication failed during %s (%s): %s", e.Phase, e.Outcome, message)
}

type apiFruitPublicationIntent struct {
	Owner         string
	Repo          string
	Branch        string
	BaseCommit    string
	Headline      string
	Body          string
	CommitMessage string
	Additions     []apiCommitFileAddition
	Deletions     []apiCommitFileDeletion
}

type apiFruitReconciliation struct {
	Outcome string
	OID     string
}

const (
	apiFruitReconciledExact      = "exact-fruit"
	apiFruitReconciledBase       = "base"
	apiFruitReconciledAbsent     = "absent"
	apiFruitReconciledUnexpected = "unexpected"
)

type githubRefResponse struct {
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type githubCommitResponse struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Tree    struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	} `json:"commit"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

type githubTreeResponse struct {
	Tree []struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

type githubBlobResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

func newAPIFruitPublicationFailure(phase, outcome string, retrySafe bool, requestID, message string) error {
	return &apiFruitPublicationFailure{
		Phase:     phase,
		Outcome:   outcome,
		RetrySafe: retrySafe,
		RequestID: safeGitHubRequestID(requestID),
		Message:   message,
	}
}

func apiFruitFailureMessage(outcome string) string {
	switch outcome {
	case apiFruitOutcomePreMutationFailure:
		return "the GitHub mutation was not written; no Fruit commit was accepted"
	case apiFruitOutcomeTargetRefConflict:
		return "the target review ref already exists or is not safely owned; it was not modified"
	case apiFruitOutcomeTargetRefAbsent:
		return "the target review ref is absent after publication was attempted"
	case apiFruitOutcomeUnexpectedState:
		return "the target review ref advanced to an unexpected state; it was not modified"
	case apiFruitOutcomeReconciliationFailure:
		return "read-only GitHub reconciliation could not establish the target state"
	case apiFruitOutcomeRetryExhausted:
		return "the evidence-gated final mutation did not establish an authoritative Fruit"
	default:
		return "managed API Fruit publication could not establish an authoritative outcome"
	}
}

func validateAPIFruitIntent(owner, repo, branch, baseCommit, commitMessage string, additions []apiCommitFileAddition, deletions []apiCommitFileDeletion) (apiFruitPublicationIntent, error) {
	if len(additions) == 0 && len(deletions) == 0 {
		return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "nothing to commit")
	}
	if strings.TrimSpace(branch) == "" || strings.TrimSpace(baseCommit) == "" {
		return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "the target branch and expected base are required")
	}
	headline, body := splitCommitMessage(commitMessage)
	if headline == "" {
		return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "the commit message headline is required")
	}
	intent := apiFruitPublicationIntent{
		Owner:         owner,
		Repo:          repo,
		Branch:        strings.TrimSpace(branch),
		BaseCommit:    strings.TrimSpace(baseCommit),
		Headline:      headline,
		Body:          body,
		CommitMessage: headline,
		Additions:     make([]apiCommitFileAddition, 0, len(additions)),
		Deletions:     make([]apiCommitFileDeletion, 0, len(deletions)),
	}
	if body != "" {
		intent.CommitMessage += "\n\n" + body
	}
	seen := make(map[string]string, len(additions)+len(deletions))
	for _, addition := range additions {
		path := filepath.ToSlash(strings.TrimSpace(addition.Path))
		if invalidAPIFruitPath(path) {
			return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "a Fruit path is invalid")
		}
		if _, err := base64.StdEncoding.DecodeString(addition.Contents); err != nil {
			return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "a Fruit file addition is not valid base64")
		}
		if prior, exists := seen[path]; exists {
			if prior == "addition" {
				return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "a Fruit path was supplied more than once")
			}
			return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "a Fruit path was supplied as both an addition and a deletion")
		}
		seen[path] = "addition"
		intent.Additions = append(intent.Additions, apiCommitFileAddition{Path: path, Contents: addition.Contents})
	}
	for _, deletion := range deletions {
		path := filepath.ToSlash(strings.TrimSpace(deletion.Path))
		if invalidAPIFruitPath(path) {
			return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "a Fruit deletion path is invalid")
		}
		if _, exists := seen[path]; exists {
			return apiFruitPublicationIntent{}, newAPIFruitPublicationFailure("intent", "invalid-intent", false, "", "a Fruit path was supplied more than once")
		}
		seen[path] = "deletion"
		intent.Deletions = append(intent.Deletions, apiCommitFileDeletion{Path: path})
	}
	return intent, nil
}

func invalidAPIFruitPath(path string) bool {
	return path == "" || path == "." || path == ".." || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "../") || strings.Contains(path, "\x00")
}

func githubFruitRefPath(owner, repo, branch string) string {
	return fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, branch)
}

func githubFruitCommitPath(owner, repo, oid string) string {
	return fmt.Sprintf("/repos/%s/%s/git/commits/%s", owner, repo, oid)
}

func githubFruitTreePath(owner, repo, oid string) string {
	return fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, oid)
}

func githubFruitBlobPath(owner, repo, oid string) string {
	return fmt.Sprintf("/repos/%s/%s/git/blobs/%s", owner, repo, oid)
}

func reconcileAPIFruit(ctx context.Context, intent apiFruitPublicationIntent, token string) (apiFruitReconciliation, error) {
	var ref githubRefResponse
	found, err := githubReadREST(ctx, githubFruitRefPath(intent.Owner, intent.Repo, intent.Branch), token, &ref)
	if err != nil {
		return apiFruitReconciliation{}, err
	}
	if !found {
		return apiFruitReconciliation{Outcome: apiFruitReconciledAbsent}, nil
	}
	oid := strings.TrimSpace(ref.Object.SHA)
	if oid == "" {
		return apiFruitReconciliation{}, fmt.Errorf("target ref returned no commit OID")
	}
	if ref.Object.Type != "commit" {
		return apiFruitReconciliation{Outcome: apiFruitReconciledUnexpected, OID: oid}, nil
	}
	if oid == intent.BaseCommit {
		return apiFruitReconciliation{Outcome: apiFruitReconciledBase, OID: oid}, nil
	}
	exact, err := remoteAPIFruitMatches(ctx, intent, token, oid)
	if err != nil {
		return apiFruitReconciliation{}, err
	}
	if exact {
		return apiFruitReconciliation{Outcome: apiFruitReconciledExact, OID: oid}, nil
	}
	return apiFruitReconciliation{Outcome: apiFruitReconciledUnexpected, OID: oid}, nil
}

func remoteAPIFruitMatches(ctx context.Context, intent apiFruitPublicationIntent, token, oid string) (bool, error) {
	var fruit githubCommitResponse
	found, err := githubReadREST(ctx, githubFruitCommitPath(intent.Owner, intent.Repo, oid), token, &fruit)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("GitHub target commit is unavailable")
	}
	if strings.TrimSpace(fruit.SHA) != oid || len(fruit.Parents) != 1 || strings.TrimSpace(fruit.Parents[0].SHA) != intent.BaseCommit {
		return false, nil
	}
	if fruit.Commit.Message != intent.CommitMessage || strings.TrimSpace(fruit.Commit.Tree.SHA) == "" {
		return false, nil
	}

	var baseCommit githubCommitResponse
	found, err = githubReadREST(ctx, githubFruitCommitPath(intent.Owner, intent.Repo, intent.BaseCommit), token, &baseCommit)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("GitHub base commit is unavailable")
	}
	if strings.TrimSpace(baseCommit.SHA) != intent.BaseCommit || strings.TrimSpace(baseCommit.Commit.Tree.SHA) == "" {
		return false, fmt.Errorf("GitHub base commit tree is unavailable")
	}
	baseTree, err := githubFruitTree(ctx, intent.Owner, intent.Repo, baseCommit.Commit.Tree.SHA, token)
	if err != nil {
		return false, err
	}
	headTree, err := githubFruitTree(ctx, intent.Owner, intent.Repo, fruit.Commit.Tree.SHA, token)
	if err != nil {
		return false, err
	}
	baseFiles := githubFruitLeafTree(baseTree)
	headFiles := githubFruitLeafTree(headTree)
	additions := make(map[string]apiCommitFileAddition, len(intent.Additions))
	for _, addition := range intent.Additions {
		additions[addition.Path] = addition
	}
	deletions := make(map[string]struct{}, len(intent.Deletions))
	for _, deletion := range intent.Deletions {
		deletions[deletion.Path] = struct{}{}
	}

	paths := make(map[string]struct{}, len(baseFiles)+len(headFiles))
	for path := range baseFiles {
		paths[path] = struct{}{}
	}
	for path := range headFiles {
		paths[path] = struct{}{}
	}
	for path := range paths {
		baseEntry, baseOK := baseFiles[path]
		headEntry, headOK := headFiles[path]
		if addition, expectedAddition := additions[path]; expectedAddition {
			if !headOK || headEntry.Type != "blob" || (baseOK && sameGitHubTreeEntry(baseEntry, headEntry)) {
				return false, nil
			}
			matches, err := githubFruitBlobMatches(ctx, intent.Owner, intent.Repo, headEntry.SHA, addition.Contents, token)
			if err != nil {
				return false, err
			}
			if !matches {
				return false, nil
			}
			if baseOK && baseEntry.Mode != headEntry.Mode {
				return false, nil
			}
			continue
		}
		if _, expectedDeletion := deletions[path]; expectedDeletion {
			if !baseOK || baseEntry.Type != "blob" || headOK {
				return false, nil
			}
			continue
		}
		if !sameGitHubTreeEntry(baseEntry, headEntry) || baseOK != headOK {
			return false, nil
		}
	}
	return true, nil
}

type githubFruitTreeEntry struct {
	Mode string
	Type string
	SHA  string
}

func githubFruitTree(ctx context.Context, owner, repo, oid, token string) (githubTreeResponse, error) {
	var tree githubTreeResponse
	found, err := githubReadREST(ctx, githubFruitTreePath(owner, repo, oid), token, &tree)
	if err != nil {
		return githubTreeResponse{}, err
	}
	if !found || tree.Truncated {
		return githubTreeResponse{}, fmt.Errorf("GitHub tree state is unavailable or truncated")
	}
	return tree, nil
}

func githubFruitLeafTree(tree githubTreeResponse) map[string]githubFruitTreeEntry {
	files := make(map[string]githubFruitTreeEntry)
	for _, entry := range tree.Tree {
		if entry.Type == "tree" {
			continue
		}
		files[entry.Path] = githubFruitTreeEntry{Mode: entry.Mode, Type: entry.Type, SHA: entry.SHA}
	}
	return files
}

func sameGitHubTreeEntry(a, b githubFruitTreeEntry) bool {
	return a.Mode == b.Mode && a.Type == b.Type && a.SHA == b.SHA
}

func githubFruitBlobMatches(ctx context.Context, owner, repo, oid, expectedContents, token string) (bool, error) {
	var blob githubBlobResponse
	found, err := githubReadREST(ctx, githubFruitBlobPath(owner, repo, oid), token, &blob)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("GitHub Fruit blob is unavailable")
	}
	if blob.Encoding != "base64" {
		return false, nil
	}
	actual, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(blob.Content), ""))
	if err != nil {
		return false, nil
	}
	expected, err := base64.StdEncoding.DecodeString(expectedContents)
	return err == nil && string(actual) == string(expected), nil
}

func apiFruitMutationInput(intent apiFruitPublicationIntent) createCommitOnBranchInput {
	return createCommitOnBranchInput{
		Branch: apiCommitBranch{
			RepositoryNameWithOwner: intent.Owner + "/" + intent.Repo,
			BranchName:              intent.Branch,
		},
		Message:         apiCommitMessage{Headline: intent.Headline, Body: intent.Body},
		ExpectedHeadOid: intent.BaseCommit,
		FileChanges: apiCommitFileChanges{
			Additions: intent.Additions,
			Deletions: intent.Deletions,
		},
	}
}

func createAPIFruitCommit(ctx context.Context, token string, intent apiFruitPublicationIntent) (string, githubRequestMetadata, error) {
	var response createCommitOnBranchResponse
	metadata, err := githubGraphQLPost(ctx, token, createCommitOnBranchMutation, map[string]any{"input": apiFruitMutationInput(intent)}, &response)
	if err != nil {
		return "", metadata, err
	}
	oid := strings.TrimSpace(response.CreateCommitOnBranch.Commit.Oid)
	if oid == "" {
		return "", metadata, &githubMutationError{Operation: "github createCommitOnBranch mutation", Kind: githubMutationPartialResponse, githubRequestMetadata: metadata}
	}
	return oid, metadata, nil
}

func mutationMetadata(err error, fallback githubRequestMetadata) githubRequestMetadata {
	var mutationErr *githubMutationError
	if errors.As(err, &mutationErr) {
		return mutationErr.githubRequestMetadata
	}
	return fallback
}

// publishAPIFruit performs the GraphQL API commit using the provided file
// changes. Every no-OID result is reconciled before a replay is considered, and
// the replay retains the exact same expected head and file intent.
func publishAPIFruit(ctx context.Context, repoPath, branch, baseCommit string, appCredential AppCredential, additions []apiCommitFileAddition, deletions []apiCommitFileDeletion, commitMessage string) (string, error) {
	intent, err := validateAPIFruitIntent("", "", branch, baseCommit, commitMessage, additions, deletions)
	if err != nil {
		return "", err
	}
	originURL, err := runGitCommand(ctx, repoPath, "remote", "get-url", "origin")
	if err != nil {
		return "", newAPIFruitPublicationFailure("preparation", "remote-unavailable", false, "", "the publication repository remote could not be resolved")
	}
	originURL = strings.TrimSpace(originURL)
	owner, repo, err := parseOwnerRepo(originURL)
	if err != nil {
		return "", newAPIFruitPublicationFailure("preparation", "remote-unavailable", false, "", "the publication repository identity could not be resolved")
	}
	intent.Owner, intent.Repo = owner, repo

	token, err := githubAppInstallationToken(ctx, appCredential, originURL)
	if err != nil {
		return "", newAPIFruitPublicationFailure("authentication", "authentication-failed", false, "", "GitHub App authentication could not be established")
	}

	if err := githubCreateRef(ctx, owner, repo, intent.Branch, intent.BaseCommit, token); err != nil {
		var mutationErr *githubMutationError
		if !errors.As(err, &mutationErr) || !mutationErr.RequestWritten {
			return "", newAPIFruitPublicationFailure("target-ref-creation", apiFruitOutcomePreMutationFailure, false, mutationRequestID(err), apiFruitFailureMessage(apiFruitOutcomePreMutationFailure))
		}
		reconciliation, reconcileErr := reconcileAPIFruit(ctx, intent, token)
		if reconcileErr != nil {
			return "", newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, mutationRequestID(err), apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
		}
		if reconciliation.Outcome == apiFruitReconciledExact {
			return reconciliation.OID, nil
		}
		outcome := apiFruitOutcomeTargetRefConflict
		if reconciliation.Outcome == apiFruitReconciledAbsent {
			outcome = apiFruitOutcomeTargetRefAbsent
		}
		return "", newAPIFruitPublicationFailure("target-ref-creation", outcome, false, mutationRequestID(err), apiFruitFailureMessage(outcome))
	}

	commitOID, metadata, commitErr := createAPIFruitCommit(ctx, token, intent)
	if commitErr == nil {
		return commitOID, nil
	}
	metadata = mutationMetadata(commitErr, metadata)
	if !metadata.RequestWritten {
		return "", newAPIFruitPublicationFailure("commit-mutation", apiFruitOutcomePreMutationFailure, false, metadata.RequestID, apiFruitFailureMessage(apiFruitOutcomePreMutationFailure))
	}

	reconciliation, reconcileErr := reconcileAPIFruit(ctx, intent, token)
	if reconcileErr != nil {
		return "", newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, metadata.RequestID, apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	if reconciliation.Outcome == apiFruitReconciledExact {
		return reconciliation.OID, nil
	}
	if reconciliation.Outcome != apiFruitReconciledBase {
		outcome := apiFruitOutcomeUnexpectedState
		if reconciliation.Outcome == apiFruitReconciledAbsent {
			outcome = apiFruitOutcomeTargetRefAbsent
		}
		return "", newAPIFruitPublicationFailure("reconciliation", outcome, false, metadata.RequestID, apiFruitFailureMessage(outcome))
	}

	// Exactly one final identical mutation is allowed after the read proved the
	// ref is still the expected base. No branch state permits another attempt.
	commitOID, secondMetadata, secondErr := createAPIFruitCommit(ctx, token, intent)
	if secondErr == nil {
		return commitOID, nil
	}
	secondMetadata = mutationMetadata(secondErr, secondMetadata)
	secondID := secondMetadata.RequestID
	if secondID == "" {
		secondID = metadata.RequestID
	}
	finalReconciliation, finalErr := reconcileAPIFruit(ctx, intent, token)
	if finalErr != nil {
		return "", newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, secondID, apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	if finalReconciliation.Outcome == apiFruitReconciledExact {
		return finalReconciliation.OID, nil
	}
	return "", newAPIFruitPublicationFailure("commit-mutation", apiFruitOutcomeRetryExhausted, false, secondID, apiFruitFailureMessage(apiFruitOutcomeRetryExhausted))
}

func mutationRequestID(err error) string {
	var mutationErr *githubMutationError
	if errors.As(err, &mutationErr) {
		return mutationErr.RequestID
	}
	return ""
}
