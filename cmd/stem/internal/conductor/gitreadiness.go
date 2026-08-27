package conductor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SubstrateGitReadiness is a successful remote Git-base check for a configured
// managed Substrate. Branch is the branch that was required (explicit
// configuration, or the repository's reported default). Commit is the SHA that
// branch resolves to.
type SubstrateGitReadiness struct {
	Owner  string
	Repo   string
	Branch string
	Commit string
}

// SubstrateSetupVerification is the result of setup verification for a
// configured Substrate. Managed is true only when checkout mode is managed and
// the Git-base contract succeeded. Path and ephemeral checkouts never set
// Managed; they keep credential-only verification.
//
// ContentsWrite is true when the Substrate uses commit:api with GitHub App auth
// and the App installation was confirmed to have repository contents write
// permission. It is false for all non-API, non-App, and non-managed checkouts.
type SubstrateSetupVerification struct {
	Managed       bool
	ContentsWrite bool
	GitBase       SubstrateGitReadiness
}

func managedCheckout(spec SubstrateSpec) bool {
	return strings.EqualFold(strings.TrimSpace(spec.Checkout.Mode), "managed")
}

// VerifySubstrateSetup is the setup-verification policy. Managed checkouts
// require Git-base readiness. Path and ephemeral checkouts keep credential-only
// verification: App remote authentication, or PAT presence. This function does
// not invent path/ephemeral Git-base semantics.
func VerifySubstrateSetup(ctx context.Context, spec SubstrateSpec, cred ResolvedCredential) (SubstrateSetupVerification, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if managedCheckout(spec) {
		gitBase, err := VerifyManagedSubstrateGitReadiness(ctx, spec, cred)
		if err != nil {
			return SubstrateSetupVerification{}, err
		}
		contentsWrite, err := verifyManagedAPICommitReadiness(ctx, spec, cred)
		if err != nil {
			return SubstrateSetupVerification{}, err
		}
		return SubstrateSetupVerification{Managed: true, ContentsWrite: contentsWrite, GitBase: gitBase}, nil
	}
	if err := verifyNonManagedCredential(ctx, spec, cred); err != nil {
		return SubstrateSetupVerification{}, err
	}
	return SubstrateSetupVerification{Managed: false}, nil
}

func verifyNonManagedCredential(ctx context.Context, spec SubstrateSpec, cred ResolvedCredential) error {
	switch cred.Method {
	case CredentialApp:
		return VerifyGitHubAppRemoteAccess(ctx, cred.App, spec.URL)
	case CredentialPAT:
		if strings.TrimSpace(cred.TokenValue) == "" {
			env := strings.TrimSpace(cred.TokenEnv)
			if env == "" {
				env = "the configured token environment variable"
			}
			return fmt.Errorf("Personal Access Token is not set in this environment (%s)", env)
		}
		return nil
	default:
		return nil
	}
}

// verifyManagedAPICommitReadiness enforces that a managed Substrate configured
// with commit:api uses GitHub App authentication, and then confirms that the App
// installation has the repository contents write permission required to create
// the Fruit review ref and commit. It is read-only: no branch, commit, push, or
// pull request is created.
//
// For managed Substrates that do not use commit:api the function is a no-op and
// returns (false, nil). The bool return is true only when the App was confirmed
// to hold contents write permission.
func verifyManagedAPICommitReadiness(ctx context.Context, spec SubstrateSpec, cred ResolvedCredential) (bool, error) {
	if strings.ToLower(strings.TrimSpace(spec.Commit)) != CommitModeAPI {
		// Not an API-commit substrate; no additional readiness check required.
		return false, nil
	}
	if cred.Method != CredentialApp {
		return false, fmt.Errorf(
			"commit mode %q requires auth method \"app\" (GitHub signs the commit server-side); "+
				"this Substrate uses auth method %q",
			CommitModeAPI, cred.Method,
		)
	}
	if err := VerifyAppInstallationContentsWrite(ctx, cred.App, spec.URL); err != nil {
		return false, err
	}
	return true, nil
}

// VerifyManagedSubstrateGitReadiness proves a configured managed Substrate can
// be used as a Git base for governed work. It is read-only with respect to the
// repository: it does not clone, create a checkout or worktree, create a
// commit, create, delete, or rename a branch, push, open a pull request, or
// otherwise mutate repository content.
//
// For GitHub App posture it reuses the existing App credential diagnostics,
// then mints a short-lived installation token for inspection. Token issuance is
// authentication, not repository mutation. For PAT posture it uses the resolved
// token for the same inspection; token presence alone is not readiness.
func VerifyManagedSubstrateGitReadiness(ctx context.Context, spec SubstrateSpec, cred ResolvedCredential) (SubstrateGitReadiness, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner, repo, err := parseOwnerRepo(spec.URL)
	if err != nil {
		return SubstrateGitReadiness{}, err
	}
	token, err := readinessInspectionToken(ctx, cred, spec.URL)
	if err != nil {
		return SubstrateGitReadiness{}, err
	}
	return inspectRepositoryGitBase(ctx, owner, repo, spec.Branch, token)
}

func readinessInspectionToken(ctx context.Context, cred ResolvedCredential, repoURL string) (string, error) {
	switch cred.Method {
	case CredentialApp:
		if err := VerifyGitHubAppRemoteAccess(ctx, cred.App, repoURL); err != nil {
			return "", err
		}
		token, err := githubAppInstallationToken(ctx, cred.App, repoURL)
		if err != nil {
			return "", classifyInstallationTokenError(err)
		}
		if strings.TrimSpace(token) == "" {
			return "", errors.New("GitHub App returned an empty installation token")
		}
		return token, nil
	case CredentialPAT:
		if strings.TrimSpace(cred.TokenValue) == "" {
			env := strings.TrimSpace(cred.TokenEnv)
			if env == "" {
				env = "the configured token environment variable"
			}
			return "", fmt.Errorf("Personal Access Token is not set in this environment (%s)", env)
		}
		return cred.TokenValue, nil
	default:
		return "", fmt.Errorf("substrate connection (auth method %q) cannot inspect GitHub repository readiness", cred.Method)
	}
}

func classifyInstallationTokenError(err error) error {
	var httpErr *githubAppHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("GitHub rejected the App credentials (HTTP %d). Check that appId matches the private key", httpErr.Status)
		}
	}
	return fmt.Errorf("GitHub App installation token could not be issued: %s", redactCredentialMaterial(err.Error()))
}

func inspectRepositoryGitBase(ctx context.Context, owner, repo, configuredBranch, token string) (SubstrateGitReadiness, error) {
	// GitHub's REST repository payload uses default_branch; this is their
	// wire format, not an OpenTendril external contract.
	var repository struct {
		DefaultBranch string `json:"default_branch"`
	}
	repoPath := fmt.Sprintf("/repos/%s/%s", owner, repo)
	if err := githubReadinessGET(ctx, repoPath, token, &repository); err != nil {
		return SubstrateGitReadiness{}, classifyReadinessRepoError(err, owner, repo)
	}

	branch := normalizeBranchName(configuredBranch)
	configured := branch != ""
	if !configured {
		branch = strings.TrimSpace(repository.DefaultBranch)
	}
	if branch == "" {
		return SubstrateGitReadiness{}, fmt.Errorf("could not resolve a Git base branch for %s/%s (the Substrate has no configured branch and the repository did not report a default branch)", owner, repo)
	}

	var commit struct {
		SHA string `json:"sha"`
	}
	commitPath := fmt.Sprintf("/repos/%s/%s/commits/%s", owner, repo, url.PathEscape(branch))
	if err := githubReadinessGET(ctx, commitPath, token, &commit); err != nil {
		return SubstrateGitReadiness{}, classifyReadinessCommitError(ctx, err, owner, repo, branch, configured, token)
	}
	sha := strings.TrimSpace(commit.SHA)
	if sha == "" {
		return SubstrateGitReadiness{}, noGitBaseError(owner, repo)
	}
	return SubstrateGitReadiness{Owner: owner, Repo: repo, Branch: branch, Commit: sha}, nil
}

func classifyReadinessRepoError(err error, owner, repo string) error {
	var httpErr *githubAppHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("GitHub refused repository inspection (HTTP %d) for %s/%s", httpErr.Status, owner, repo)
		case http.StatusNotFound:
			return fmt.Errorf("repository %s/%s does not exist or is inaccessible", owner, repo)
		case http.StatusConflict:
			return noGitBaseError(owner, repo)
		}
	}
	return fmt.Errorf("Git repository readiness check failed: %s", redactCredentialMaterial(err.Error()))
}

func classifyReadinessCommitError(ctx context.Context, err error, owner, repo, branch string, configured bool, token string) error {
	var httpErr *githubAppHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusConflict:
			return noGitBaseError(owner, repo)
		case http.StatusNotFound, http.StatusUnprocessableEntity:
			empty, emptyErr := repositoryHasNoCommits(ctx, owner, repo, token)
			if emptyErr == nil && empty {
				return noGitBaseError(owner, repo)
			}
			if configured {
				return fmt.Errorf("configured branch %q does not exist or does not resolve to a commit on %s/%s", branch, owner, repo)
			}
			return fmt.Errorf("repository %s/%s default branch %q does not resolve to a commit", owner, repo, branch)
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("GitHub refused repository inspection (HTTP %d) for %s/%s", httpErr.Status, owner, repo)
		}
	}
	return fmt.Errorf("Git repository readiness check failed: %s", redactCredentialMaterial(err.Error()))
}

func repositoryHasNoCommits(ctx context.Context, owner, repo, token string) (bool, error) {
	var commits []struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits?per_page=1", owner, repo)
	if err := githubReadinessGET(ctx, path, token, &commits); err != nil {
		var httpErr *githubAppHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusConflict {
			return true, nil
		}
		return false, err
	}
	return len(commits) == 0, nil
}

func noGitBaseError(owner, repo string) error {
	return fmt.Errorf("repository %s/%s has no Git base (it contains no commit). Create an initial commit before using it as an OpenTendril Substrate, then rerun tendril git setup --verify", owner, repo)
}

func githubReadinessGET(ctx context.Context, path, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentGitHubAPIBaseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return doGithubAppRequest(req, out)
}
