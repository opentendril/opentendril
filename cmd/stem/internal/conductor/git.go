package conductor

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Delegated git commit — the lowest rung of the delegated-execution ladder
// from the Design RFC. RunGitCommit commits the current state of a resolved
// local workspace directory under the substrate's configured commit identity,
// so an external agent never needs to shell out git on the host itself. Like
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

	if execution.Credential.CommitMode == CommitModeAPI {
		return runAPICommit(ctx, execution)
	}

	// Deny-closed attribution: an unattributable delegated commit must never
	// be created, so both identity fields are required before anything runs.
	// This requirement is local-mode only: in api mode the GitHub App is the
	// identity (GitHub sets author and committer server-side), so runAPICommit
	// never reaches here.
	if strings.TrimSpace(execution.Credential.Identity.Name) == "" || strings.TrimSpace(execution.Credential.Identity.Email) == "" {
		return GitCommitResult{}, fmt.Errorf("delegated git commit refused: the substrate has no configured commit identity (set identity name and email in substrates.yaml) — an unattributable delegated commit is never created")
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
