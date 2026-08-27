package conductor

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// GitHub App authentication (Design RFC, method: app). The Stem signs a
// short-lived JWT with the App's private key, exchanges it for an installation
// access token (≤1h, cached), and uses that token as the git HTTPS bearer.

// githubAPIBaseURL, githubGraphQLURL, and githubAppHTTPClient are package vars
// so tests can point them at an httptest server.
var (
	githubAPIBaseURL    = "https://api.github.com"
	githubGraphQLURL    = "https://api.github.com/graphql"
	githubAppHTTPClient = &http.Client{Timeout: 30 * time.Second}

	githubEndpointMu sync.RWMutex
	appTokenMu       sync.Mutex
	appTokenCache    = map[string]cachedAppToken{}
)

func currentGitHubAPIBaseURL() string {
	githubEndpointMu.RLock()
	defer githubEndpointMu.RUnlock()
	return githubAPIBaseURL
}

func currentGitHubGraphQLURL() string {
	githubEndpointMu.RLock()
	defer githubEndpointMu.RUnlock()
	return githubGraphQLURL
}

type cachedAppToken struct {
	token  string
	expiry time.Time
}

func b64url(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// loadRSAPrivateKey parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8).
func loadRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found in GitHub App private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key is not RSA")
	}
	return key, nil
}

// appPrivateKeyPEM resolves the private-key bytes from the env var (if set) or
// the key file path.
func appPrivateKeyPEM(app AppCredential) ([]byte, error) {
	if env := strings.TrimSpace(app.PrivateKeyEnv); env != "" {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return []byte(v), nil
		}
	}
	path := strings.TrimSpace(app.PrivateKeyPath)
	if path == "" {
		return nil, errors.New("GitHub App auth has no private key path or env")
	}
	return os.ReadFile(expandHome(path))
}

// mintAppJWT builds and signs the short-lived App JWT (RS256).
func mintAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	if strings.TrimSpace(appID) == "" {
		return "", errors.New("GitHub App auth requires appId")
	}
	header := b64url([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-30 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + b64url(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign App JWT: %w", err)
	}
	return signingInput + "." + b64url(sig), nil
}

// parseOwnerRepo extracts owner/repo from an https or ssh GitHub remote URL.
func parseOwnerRepo(remoteURL string) (owner, repo string, err error) {
	u := strings.TrimSpace(remoteURL)
	u = strings.TrimSuffix(u, ".git")
	// Strip scheme / user@host prefixes, leaving <host>[:/ ]owner/repo.
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if at := strings.LastIndex(u, "@"); at >= 0 {
		u = u[at+1:]
	}
	// Now u looks like "github.com/owner/repo" or "github.com:owner/repo".
	u = strings.Replace(u, ":", "/", 1)
	parts := strings.Split(u, "/")
	if len(parts) < 3 {
		return "", "", fmt.Errorf("cannot parse owner/repo from %q", remoteURL)
	}
	owner = parts[len(parts)-2]
	repo = parts[len(parts)-1]
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("cannot parse owner/repo from %q", remoteURL)
	}
	return owner, repo, nil
}

func githubAppAPIGet(ctx context.Context, path, jwt string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentGitHubAPIBaseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	return doGithubAppRequest(req, out)
}

func githubAppAPIPost(ctx context.Context, path, jwt string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, currentGitHubAPIBaseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	return doGithubAppRequest(req, out)
}

// githubGraphQLRequest is the JSON body of a GraphQL POST: the query/mutation
// document plus its variables.
type githubGraphQLRequest struct {
	Query     string `json:"query"`
	Variables any    `json:"variables"`
}

// githubGraphQLError is one entry of a GraphQL response's top-level "errors"
// array — present even on an HTTP 200 when the mutation itself failed.
type githubGraphQLError struct {
	Message string `json:"message"`
}

// githubGraphQLPost issues a GraphQL request against api.github.com/graphql,
// authenticated with an installation access token (not the App JWT — GraphQL
// operations like createCommitOnBranch act as the installation, the same
// bearer the git HTTPS credential helper uses). decodeInto receives the
// decoded "data" object; a non-empty top-level "errors" array is always
// reported as an error, even on an HTTP 200 (GraphQL's error-reporting
// convention differs from the REST helpers above).
func githubGraphQLPost(ctx context.Context, installationToken, query string, variables any, decodeInto any) error {
	body, err := json.Marshal(githubGraphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("encode github graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, currentGitHubGraphQLURL(), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+installationToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github graphql request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github graphql returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var envelope struct {
		Data   json.RawMessage      `json:"data"`
		Errors []githubGraphQLError `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode github graphql response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			messages = append(messages, e.Message)
		}
		return fmt.Errorf("github graphql request failed: %s", strings.Join(messages, "; "))
	}
	if decodeInto != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, decodeInto); err != nil {
			return fmt.Errorf("decode github graphql data: %w", err)
		}
	}
	return nil
}

// githubRESTRequest issues an authenticated REST request against
// api.github.com and decodes the JSON response into out (nil to discard).
//
// Unlike the two helpers above it is bearer-agnostic: the token may be a
// GitHub App installation token OR a fine-grained Personal Access Token,
// because both connection postures authenticate a REST call with the exact
// same header. That is why the pull-request path uses REST rather than
// GraphQL — one request shape serves both postures, and owner/repo is enough
// to address the resource (GraphQL would need a node-identifier lookup first).
func githubRESTRequest(ctx context.Context, method, path, token string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode github rest request: %w", err)
		}
		payload = strings.NewReader(string(encoded))
	}

	req, err := http.NewRequestWithContext(ctx, method, currentGitHubAPIBaseURL()+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github api %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode github api response: %w", err)
		}
	}
	return nil
}

// githubAppHTTPError is a non-2xx GitHub App REST response. Verification maps
// Status onto secret-safe diagnostics and must not print Body.
type githubAppHTTPError struct {
	Path   string
	Status int
	Body   string
}

func (e *githubAppHTTPError) Error() string {
	return fmt.Sprintf("github app api %s returned %d: %s", e.Path, e.Status, strings.TrimSpace(e.Body))
}

func doGithubAppRequest(req *http.Request, out any) error {
	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github app api request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &githubAppHTTPError{Path: req.URL.Path, Status: resp.StatusCode, Body: string(body)}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode github app api response: %w", err)
		}
	}
	return nil
}

// RedirectGitHubAPIBaseURL points GitHub REST calls at baseURL and returns a
// restore function. Tests use this with an httptest server; production callers
// must not.
func RedirectGitHubAPIBaseURL(baseURL string) func() {
	githubEndpointMu.Lock()
	orig := githubAPIBaseURL
	githubAPIBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	githubEndpointMu.Unlock()
	resetGitHubAppTokenCache()
	return func() {
		githubEndpointMu.Lock()
		githubAPIBaseURL = orig
		githubEndpointMu.Unlock()
		resetGitHubAppTokenCache()
	}
}

func resetGitHubAppTokenCache() {
	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()
}

func redirectGitHubGraphQLURL(url string) func() {
	githubEndpointMu.Lock()
	orig := githubGraphQLURL
	githubGraphQLURL = url
	githubEndpointMu.Unlock()
	return func() {
		githubEndpointMu.Lock()
		githubGraphQLURL = orig
		githubEndpointMu.Unlock()
	}
}

// resolveInstallationID returns the configured installation id, or discovers it
// for the repo the substrate points at.
func resolveInstallationID(ctx context.Context, app AppCredential, jwt, repoURL string) (int64, error) {
	if app.InstallationID > 0 {
		return app.InstallationID, nil
	}
	owner, repo, err := parseOwnerRepo(repoURL)
	if err != nil {
		return 0, err
	}
	var installation struct {
		ID int64 `json:"id"`
	}
	if err := githubAppAPIGet(ctx, fmt.Sprintf("/repos/%s/%s/installation", owner, repo), jwt, &installation); err != nil {
		return 0, err
	}
	if installation.ID == 0 {
		return 0, fmt.Errorf("no GitHub App installation found for %s/%s", owner, repo)
	}
	return installation.ID, nil
}

// githubAppInstallationToken mints (or returns a cached) installation access
// token for the substrate's repo. The token is used as the git HTTPS password.
func githubAppInstallationToken(ctx context.Context, app AppCredential, repoURL string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cacheKey := fmt.Sprintf("app:%s:inst:%d:repo:%s", app.AppID, app.InstallationID, repoURL)

	appTokenMu.Lock()
	defer appTokenMu.Unlock()
	if cached, ok := appTokenCache[cacheKey]; ok && time.Now().Before(cached.expiry) {
		return cached.token, nil
	}

	pemBytes, err := appPrivateKeyPEM(app)
	if err != nil {
		return "", err
	}
	key, err := loadRSAPrivateKey(pemBytes)
	if err != nil {
		return "", err
	}
	jwt, err := mintAppJWT(app.AppID, key, time.Now())
	if err != nil {
		return "", err
	}

	installationID, err := resolveInstallationID(ctx, app, jwt, repoURL)
	if err != nil {
		return "", err
	}

	var tokenResp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := githubAppAPIPost(ctx, fmt.Sprintf("/app/installations/%d/access_tokens", installationID), jwt, &tokenResp); err != nil {
		return "", err
	}
	if strings.TrimSpace(tokenResp.Token) == "" {
		return "", errors.New("github app returned an empty installation token")
	}

	expiry := tokenResp.ExpiresAt
	if expiry.IsZero() {
		expiry = time.Now().Add(55 * time.Minute)
	}
	appTokenCache[cacheKey] = cachedAppToken{token: tokenResp.Token, expiry: expiry.Add(-1 * time.Minute)}
	return tokenResp.Token, nil
}

// githubCreateRef creates a Git ref via the GitHub REST API.
func githubCreateRef(ctx context.Context, owner, repo, ref, sha, token string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
	body := map[string]string{
		"ref": "refs/heads/" + ref,
		"sha": sha,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode create-ref request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, currentGitHubAPIBaseURL()+path, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	if err := doGithubAppRequest(req, nil); err != nil {
		var httpErr *githubAppHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusUnprocessableEntity {
			return fmt.Errorf("branch %s already exists or SHA is invalid", ref)
		}
		return err
	}
	return nil
}

// VerifyGitHubAppRemoteAccess proves the App credential can authenticate to
// repoURL without minting an installation token and without mutating the
// remote (no branch, commit, push, or pull request).
//
// It reuses the existing App authentication path: PEM load/parse, JWT mint,
// and repository installation lookup. Success requires a valid App JWT and a
// repository installation for repoURL.
func VerifyGitHubAppRemoteAccess(ctx context.Context, app AppCredential, repoURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	pemBytes, err := appPrivateKeyPEM(app)
	if err != nil {
		return fmt.Errorf("GitHub App private key is unusable: %w", err)
	}
	key, err := loadRSAPrivateKey(pemBytes)
	if err != nil {
		return fmt.Errorf("GitHub App private key is malformed: %w", err)
	}
	jwt, err := mintAppJWT(app.AppID, key, time.Now())
	if err != nil {
		return fmt.Errorf("GitHub App credentials could not mint a JWT: %w", err)
	}

	if err := githubAppAPIGet(ctx, "/app", jwt, nil); err != nil {
		return classifyAppCredentialError(err)
	}

	owner, repo, err := parseOwnerRepo(repoURL)
	if err != nil {
		return err
	}

	var installation struct {
		ID int64 `json:"id"`
	}
	installPath := fmt.Sprintf("/repos/%s/%s/installation", owner, repo)
	if err := githubAppAPIGet(ctx, installPath, jwt, &installation); err != nil {
		return classifyAppInstallationError(ctx, err, owner, repo)
	}
	if installation.ID == 0 {
		return fmt.Errorf("GitHub App is not installed on %s/%s", owner, repo)
	}
	return nil
}

func classifyAppCredentialError(err error) error {
	var httpErr *githubAppHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("GitHub rejected the App credentials (HTTP %d). Check that appId matches the private key", httpErr.Status)
		}
	}
	return fmt.Errorf("GitHub App remote verification failed: %s", redactCredentialMaterial(err.Error()))
}

func classifyAppInstallationError(ctx context.Context, err error, owner, repo string) error {
	var httpErr *githubAppHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("GitHub rejected the App credentials (HTTP %d). Check that appId matches the private key", httpErr.Status)
		case http.StatusNotFound:
			visible, probeErr := githubRepoPubliclyVisible(ctx, owner, repo)
			if probeErr != nil {
				return fmt.Errorf("GitHub App is not installed on %s/%s, or the repository does not exist or is inaccessible", owner, repo)
			}
			if visible {
				return fmt.Errorf("GitHub App is not installed on %s/%s", owner, repo)
			}
			return fmt.Errorf("repository %s/%s does not exist or is inaccessible", owner, repo)
		}
	}
	return fmt.Errorf("GitHub App remote verification failed: %s", redactCredentialMaterial(err.Error()))
}

// githubRepoPubliclyVisible reports whether GET /repos/{owner}/{repo} succeeds
// without credentials. A public repository that exists returns true; a missing
// or private repository returns false. Used only to distinguish "App not
// installed" from "repository does not exist or is inaccessible" after an
// installation lookup 404. It never sends App credentials.
func githubRepoPubliclyVisible(ctx context.Context, owner, repo string) (bool, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentGitHubAPIBaseURL()+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("github app api request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, &githubAppHTTPError{Path: req.URL.Path, Status: resp.StatusCode}
	}
}

func redactCredentialMaterial(s string) string {
	if s == "" {
		return s
	}
	if i := strings.Index(s, "-----BEGIN "); i >= 0 {
		if j := strings.Index(s[i:], "-----END "); j >= 0 {
			rest := s[i+j:]
			if k := strings.Index(rest[9:], "-----"); k >= 0 {
				s = s[:i] + "[redacted-pem]" + rest[9+k+5:]
			} else {
				s = s[:i] + "[redacted-pem]"
			}
		} else {
			s = s[:i] + "[redacted-pem]"
		}
	}
	s = redactPrefixedSecrets(s, "ghs_")
	s = redactPrefixedSecrets(s, "ghp_")
	s = redactPrefixedSecrets(s, "github_pat_")
	s = redactJWTMaterial(s)
	return s
}

func redactPrefixedSecrets(s, prefix string) string {
	var b strings.Builder
	start := 0
	for {
		i := strings.Index(s[start:], prefix)
		if i < 0 {
			b.WriteString(s[start:])
			return b.String()
		}
		i += start
		b.WriteString(s[start:i])
		b.WriteString(prefix)
		b.WriteString("[redacted]")
		end := i + len(prefix)
		for end < len(s) {
			c := s[end]
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
				end++
				continue
			}
			break
		}
		start = end
	}
}

func redactJWTMaterial(s string) string {
	const header = "eyJ"
	start := 0
	for {
		i := strings.Index(s[start:], header)
		if i < 0 {
			return s
		}
		i += start
		end := i
		dots := 0
		for end < len(s) {
			c := s[end]
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
				end++
				continue
			}
			if c == '.' {
				dots++
				end++
				continue
			}
			break
		}
		if dots >= 2 {
			s = s[:i] + "[redacted-jwt]" + s[end:]
			start = i + len("[redacted-jwt]")
			continue
		}
		start = i + len(header)
	}
}
