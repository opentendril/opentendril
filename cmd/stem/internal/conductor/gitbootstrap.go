package conductor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// GitBootstrapBranchSource records which approved branch-selection input won.
// The source is retained so post-publication readiness can distinguish a branch
// chosen from the repository default from one supplied explicitly by the
// Botanist when the repository supplied no default.
type GitBootstrapBranchSource string

const (
	GitBootstrapBranchFromConfig  GitBootstrapBranchSource = "configured-substrate"
	GitBootstrapBranchFromDefault GitBootstrapBranchSource = "github-default"
	GitBootstrapBranchFromInput   GitBootstrapBranchSource = "botanist-input"
)

const (
	// Git defines this OID for the tree with no entries. Using it directly keeps
	// the inaugural commit content-free: no README, licence, ignore file, or
	// project structure is ever synthesized.
	emptyGitTreeOID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

	GitBootstrapCommitMessage = "chore: bootstrap empty OpenTendril Substrate"
	GitBootstrapAuthorName    = "OpenTendril"
	GitBootstrapAuthorEmail   = "bootstrap@opentendril.com"
)

// GitBootstrapPlan is the read-only result shown to the Botanist before the
// bootstrap mutation. It deliberately carries no installation token.
type GitBootstrapPlan struct {
	Spec         SubstrateSpec
	Credential   ResolvedCredential
	Owner        string
	Repo         string
	Branch       string
	BranchSource GitBootstrapBranchSource
}

// GitBootstrapResult reports setup state only. A bootstrap commit is not Fruit
// and does not represent delegated work or Botanist acceptance of later work.
type GitBootstrapResult struct {
	Repository string
	Branch     string
	CommitOID  string
}

// PrepareGitBootstrap authenticates and inspects a configured managed GitHub
// App/API Substrate without mutating repository state. The returned plan is
// safe to present for explicit Botanist confirmation.
func PrepareGitBootstrap(ctx context.Context, spec SubstrateSpec, cred ResolvedCredential, inputBranch string) (GitBootstrapPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateGitBootstrapPosture(spec, cred); err != nil {
		return GitBootstrapPlan{}, err
	}
	spec.URL = strings.TrimSpace(spec.URL)
	if err := validateGitBootstrapRemoteURL(spec.URL); err != nil {
		return GitBootstrapPlan{}, err
	}
	owner, repo, err := parseOwnerRepo(spec.URL)
	if err != nil {
		return GitBootstrapPlan{}, fmt.Errorf("bootstrap repository: %w", err)
	}

	// Authenticate the App and inspect its installation before any repository
	// inspection. This preserves the fail-closed boundary for invalid Apps and
	// makes the Contents write requirement explicit before confirmation.
	if err := VerifyGitHubAppRemoteAccess(ctx, cred.App, spec.URL); err != nil {
		return GitBootstrapPlan{}, err
	}
	if err := VerifyAppInstallationContentsWrite(ctx, cred.App, spec.URL); err != nil {
		return GitBootstrapPlan{}, err
	}
	token, err := githubAppInstallationToken(ctx, cred.App, spec.URL)
	if err != nil {
		return GitBootstrapPlan{}, classifyInstallationTokenError(err)
	}

	defaultBranch, err := inspectBootstrapRepositoryEmpty(ctx, owner, repo, token)
	if err != nil {
		return GitBootstrapPlan{}, err
	}

	branch, source, err := selectGitBootstrapBranch(spec.Branch, defaultBranch, inputBranch)
	if err != nil {
		return GitBootstrapPlan{}, err
	}
	if err := validateBranchName(branch); err != nil {
		return GitBootstrapPlan{}, fmt.Errorf("bootstrap target branch: %w", err)
	}
	_, exists, err := inspectGitHubRef(ctx, owner, repo, branch, token)
	if err != nil {
		return GitBootstrapPlan{}, err
	}
	if exists {
		return GitBootstrapPlan{}, fmt.Errorf("bootstrap refused: target branch %q already exists on %s/%s; no existing ref is ever overwritten", branch, owner, repo)
	}

	return GitBootstrapPlan{
		Spec:         spec,
		Credential:   cred,
		Owner:        owner,
		Repo:         repo,
		Branch:       branch,
		BranchSource: source,
	}, nil
}

// RunGitBootstrap performs the confirmed bootstrap plan. The final repository
// and target-ref checks are intentionally repeated after confirmation. GitHub
// does not offer a repository-wide zero-ref lease, so the safety boundary is
// the exact target ref: its publication uses expected-absent semantics and no
// force overwrite is possible.
func RunGitBootstrap(ctx context.Context, plan GitBootstrapPlan) (GitBootstrapResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateGitBootstrapPosture(plan.Spec, plan.Credential); err != nil {
		return GitBootstrapResult{}, err
	}
	bootstrapSpec := plan.Spec
	bootstrapSpec.URL = strings.TrimSpace(bootstrapSpec.URL)
	if err := validateGitBootstrapRemoteURL(bootstrapSpec.URL); err != nil {
		return GitBootstrapResult{}, err
	}
	branch := normalizeBranchName(plan.Branch)
	if err := validateBranchName(branch); err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap target branch: %w", err)
	}
	owner, repo := strings.TrimSpace(plan.Owner), strings.TrimSpace(plan.Repo)
	if owner == "" || repo == "" {
		var err error
		owner, repo, err = parseOwnerRepo(bootstrapSpec.URL)
		if err != nil {
			return GitBootstrapResult{}, fmt.Errorf("bootstrap repository: %w", err)
		}
	}

	token, err := githubAppInstallationToken(ctx, plan.Credential.App, bootstrapSpec.URL)
	if err != nil {
		return GitBootstrapResult{}, classifyInstallationTokenError(err)
	}
	if _, err := inspectBootstrapRepositoryEmpty(ctx, owner, repo, token); err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: final empty-repository check failed: %w", err)
	}
	_, exists, err := inspectGitHubRef(ctx, owner, repo, branch, token)
	if err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: final target-ref check failed: %w", err)
	}
	if exists {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: target branch %q appeared before publication; it was not changed", branch)
	}

	workspace, err := os.MkdirTemp("", "opentendril-git-bootstrap-")
	if err != nil {
		return GitBootstrapResult{}, fmt.Errorf("create temporary Git workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	if _, err := runGitBootstrapCommandFn(ctx, workspace, nil, "", "init", "--initial-branch="+branch); err != nil {
		return GitBootstrapResult{}, fmt.Errorf("initialize temporary Git workspace: %w", err)
	}
	if _, err := runGitBootstrapCommandFn(ctx, workspace, nil, "", "remote", "add", "origin", bootstrapSpec.URL); err != nil {
		return GitBootstrapResult{}, fmt.Errorf("configure temporary Git workspace: %w", err)
	}
	commitEnv := []string{
		"GIT_AUTHOR_NAME=" + GitBootstrapAuthorName,
		"GIT_AUTHOR_EMAIL=" + GitBootstrapAuthorEmail,
		"GIT_COMMITTER_NAME=" + GitBootstrapAuthorName,
		"GIT_COMMITTER_EMAIL=" + GitBootstrapAuthorEmail,
	}
	commitOID, err := runGitBootstrapCommandFn(ctx, workspace, commitEnv, "", "commit-tree", emptyGitTreeOID, "-m", GitBootstrapCommitMessage)
	if err != nil {
		return GitBootstrapResult{}, fmt.Errorf("create empty bootstrap root commit: %w", err)
	}
	commitOID = strings.TrimSpace(commitOID)
	if commitOID == "" {
		return GitBootstrapResult{}, fmt.Errorf("create empty bootstrap root commit: Git returned no commit OID")
	}

	gitEnv, err := materializeGitAuth(ctx, plan.Credential, bootstrapSpec.URL)
	if err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap Git authentication: %w", err)
	}
	if err := requireGitHubPushAuth(gitEnv, bootstrapSpec.URL, plan.Credential); err != nil {
		return GitBootstrapResult{}, err
	}
	// Repeat the empty-repository and expected-absent checks after creating the
	// local commit and immediately before publication. Any target-ref race after
	// this point is handled by the same expected-absent lease at the remote.
	if _, err := inspectBootstrapRepositoryEmpty(ctx, owner, repo, token); err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: final pre-publication empty-repository check failed: %w", err)
	}
	_, exists, err = inspectGitHubRef(ctx, owner, repo, branch, token)
	if err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: final pre-publication target-ref check failed: %w", err)
	}
	if exists {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: target branch %q appeared before publication; it was not changed", branch)
	}
	lease := "--force-with-lease=refs/heads/" + branch + ":"
	refspec := commitOID + ":refs/heads/" + branch
	if _, err := runGitBootstrapCommandFn(ctx, workspace, gitEnv, "", "push", "origin", lease, "--", refspec); err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: expected-absent publication of target branch %q was refused; no existing ref was overwritten: %w", branch, err)
	}

	publishedOID, exists, err := inspectGitHubRef(ctx, owner, repo, branch, token)
	if err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: post-publication target-ref verification failed: %w", err)
	}
	if !exists || publishedOID != commitOID {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: target branch %q resolves to %q, want the exact bootstrap commit %q", branch, publishedOID, commitOID)
	}

	verificationSpec := bootstrapSpec
	if plan.BranchSource == GitBootstrapBranchFromInput {
		// With no configured or repository-reported default, preserve the
		// Botanist's explicit branch as the normal readiness target.
		verificationSpec.Branch = branch
	}
	verification, err := verifyGitBootstrapSetupFn(ctx, verificationSpec, plan.Credential)
	if err != nil {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: post-publication VerifySubstrateSetup failed: %w", err)
	}
	if !verification.Managed || !verification.ContentsWrite || verification.GitBase.Branch != branch || verification.GitBase.Commit != commitOID {
		return GitBootstrapResult{}, fmt.Errorf("bootstrap concurrency conflict: post-publication readiness resolved to branch %q at %q, want branch %q at %q", verification.GitBase.Branch, verification.GitBase.Commit, branch, commitOID)
	}

	return GitBootstrapResult{
		Repository: owner + "/" + repo,
		Branch:     branch,
		CommitOID:  commitOID,
	}, nil
}

func validateGitBootstrapPosture(spec SubstrateSpec, cred ResolvedCredential) error {
	if !managedCheckout(spec) {
		return fmt.Errorf("bootstrap is supported only for a managed Substrate (checkout.mode: managed)")
	}
	if cred.Method != CredentialApp {
		return fmt.Errorf("bootstrap is supported only for the managed GitHub App posture (auth.method: app)")
	}
	commitMode := strings.ToLower(strings.TrimSpace(spec.Commit))
	if commitMode == "" {
		commitMode = strings.ToLower(strings.TrimSpace(cred.CommitMode))
	}
	if commitMode != CommitModeAPI {
		return fmt.Errorf("bootstrap is supported only for the managed GitHub App/API posture (commit: api)")
	}
	return nil
}

func validateGitBootstrapRemoteURL(remoteURL string) error {
	trimmed := strings.TrimSpace(remoteURL)
	if trimmed == "" {
		return errors.New("bootstrap requires a repository URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return errors.New("bootstrap requires a valid repository URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("bootstrap refuses repository URLs containing embedded credentials or query data")
	}
	return nil
}

func inspectBootstrapRepositoryEmpty(ctx context.Context, owner, repo, token string) (string, error) {
	var repository struct {
		DefaultBranch string `json:"default_branch"`
	}
	repoPath := fmt.Sprintf("/repos/%s/%s", owner, repo)
	if err := githubReadinessGET(ctx, repoPath, token, &repository); err != nil {
		return "", classifyReadinessRepoError(err, owner, repo)
	}
	empty, err := repositoryHasNoCommits(ctx, owner, repo, token)
	if err != nil {
		return "", fmt.Errorf("could not inspect whether repository %s/%s is empty: %s", owner, repo, redactCredentialMaterial(err.Error()))
	}
	if !empty {
		return "", fmt.Errorf("bootstrap refused: repository %s/%s already contains commits; no existing Git state is modified", owner, repo)
	}
	return normalizeBranchName(repository.DefaultBranch), nil
}

func selectGitBootstrapBranch(configuredBranch, defaultBranch, inputBranch string) (string, GitBootstrapBranchSource, error) {
	if branch := normalizeBranchName(configuredBranch); branch != "" {
		return branch, GitBootstrapBranchFromConfig, nil
	}
	if branch := normalizeBranchName(defaultBranch); branch != "" {
		return branch, GitBootstrapBranchFromDefault, nil
	}
	if branch := normalizeBranchName(inputBranch); branch != "" {
		return branch, GitBootstrapBranchFromInput, nil
	}
	return "", "", fmt.Errorf("bootstrap requires an explicit Botanist branch because the Substrate and GitHub repository provide no target branch")
}

func inspectGitHubRef(ctx context.Context, owner, repo, branch, token string) (string, bool, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, url.PathEscape(branch))
	if err := githubReadinessGET(ctx, path, token, &ref); err != nil {
		var httpErr *githubAppHTTPError
		if errors.As(err, &httpErr) && (httpErr.Status == http.StatusNotFound || httpErr.Status == http.StatusConflict) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect target ref %q: %s", branch, redactCredentialMaterial(err.Error()))
	}
	oid := strings.TrimSpace(ref.Object.SHA)
	if oid == "" {
		return "", true, fmt.Errorf("target ref %q exists but GitHub returned no object OID", branch)
	}
	return oid, true, nil
}

var runGitBootstrapCommandFn = runGitBootstrapCommand

func runGitBootstrapCommand(ctx context.Context, dir string, extraEnv []string, input string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), extraEnv...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, redactCredentialMaterial(strings.TrimSpace(string(output))))
	}
	return strings.TrimSpace(string(output)), nil
}

var verifyGitBootstrapSetupFn = VerifySubstrateSetup
