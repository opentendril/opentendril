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

	appTokenMu    sync.Mutex
	appTokenCache = map[string]cachedAppToken{}
)

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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	return doGithubAppRequest(req, out)
}

func githubAppAPIPost(ctx context.Context, path, jwt string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAPIBaseURL+path, nil)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubGraphQLURL, strings.NewReader(string(body)))
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

	req, err := http.NewRequestWithContext(ctx, method, githubAPIBaseURL+path, payload)
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

func doGithubAppRequest(req *http.Request, out any) error {
	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("github app api request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github app api %s returned %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode github app api response: %w", err)
		}
	}
	return nil
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
