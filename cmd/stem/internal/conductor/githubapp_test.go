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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOwnerRepo(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/opentendril/core.git":   {"opentendril", "core"},
		"https://github.com/opentendril/core":       {"opentendril", "core"},
		"git@github.com:opentendril/core.git":       {"opentendril", "core"},
		"ssh://git@github.com/opentendril/core.git": {"opentendril", "core"},
	}
	for url, want := range cases {
		owner, repo, err := parseOwnerRepo(url)
		if err != nil || owner != want[0] || repo != want[1] {
			t.Fatalf("parseOwnerRepo(%q) = %q/%q err=%v, want %q/%q", url, owner, repo, err, want[0], want[1])
		}
	}
	if _, _, err := parseOwnerRepo("not-a-url"); err == nil {
		t.Fatalf("expected error for malformed URL")
	}
}

func genTestKeyPEM(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	return key, path
}

func TestMintAppJWTIsVerifiable(t *testing.T) {
	key, _ := genTestKeyPEM(t)
	jwt, err := mintAppJWT("4276558", key, time.Now())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt should have 3 parts, got %d", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	_ = json.Unmarshal(claimsJSON, &claims)
	if claims.Iss != "4276558" || claims.Exp <= claims.Iat {
		t.Fatalf("bad claims: %+v", claims)
	}
}

func TestLoadRSAPrivateKey(t *testing.T) {
	key, path := genTestKeyPEM(t)
	pemBytes, _ := os.ReadFile(path)
	got, err := loadRSAPrivateKey(pemBytes)
	if err != nil || got.N.Cmp(key.N) != 0 {
		t.Fatalf("PKCS1 load failed: %v", err)
	}
	// PKCS8 form
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	p8pem := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	if _, err := loadRSAPrivateKey(p8pem); err != nil {
		t.Fatalf("PKCS8 load failed: %v", err)
	}
	if _, err := loadRSAPrivateKey([]byte("garbage")); err == nil {
		t.Fatalf("expected error for non-PEM input")
	}
}

func TestGithubAppInstallationToken(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)

	var installCalls, tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation") && r.Method == http.MethodGet:
			installCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99001})
		case strings.Contains(r.URL.Path, "/access_tokens") && r.Method == http.MethodPost:
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_installation_token",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origBase := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = origBase }()
	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()

	app := AppCredential{AppID: "4276558", PrivateKeyPath: keyPath}
	tok, err := githubAppInstallationToken(context.Background(), app, "https://github.com/opentendril/core.git")
	if err != nil {
		t.Fatalf("token mint failed: %v", err)
	}
	if tok != "ghs_installation_token" {
		t.Fatalf("token = %q, want ghs_installation_token", tok)
	}
	if installCalls != 1 || tokenCalls != 1 {
		t.Fatalf("calls = install:%d token:%d, want 1/1", installCalls, tokenCalls)
	}

	// Second call is served from cache — no new API traffic.
	if _, err := githubAppInstallationToken(context.Background(), app, "https://github.com/opentendril/core.git"); err != nil {
		t.Fatalf("cached call failed: %v", err)
	}
	if installCalls != 1 || tokenCalls != 1 {
		t.Fatalf("cache miss: calls = install:%d token:%d, want still 1/1", installCalls, tokenCalls)
	}
}

func TestPinnedInstallationSkipsDiscovery(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	var installCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/installation") {
			installCalls++
		}
		if strings.Contains(r.URL.Path, "/access_tokens") {
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_x", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	origBase := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = origBase }()
	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()

	app := AppCredential{AppID: "1", InstallationID: 42, PrivateKeyPath: keyPath}
	if _, err := githubAppInstallationToken(context.Background(), app, "https://github.com/o/r.git"); err != nil {
		t.Fatalf("token failed: %v", err)
	}
	if installCalls != 0 {
		t.Fatalf("pinned installation should skip discovery, got %d discovery calls", installCalls)
	}
}

func TestResolveAppCredential(t *testing.T) {
	rc, err := resolveSubstrateCredential(SubstrateSpec{Auth: AuthSpec{
		Method: "app", AppID: "4276558", PrivateKeyPath: "~/x.pem",
	}}, nil)
	if err != nil || rc.Method != CredentialApp || rc.App.AppID != "4276558" {
		t.Fatalf("resolve app: %+v err=%v", rc, err)
	}
}

func TestGitTokenCredentialEnv(t *testing.T) {
	env := gitTokenCredentialEnv("ghs_tok")
	foundToken := false
	for _, e := range env {
		if e == gitTokenCredentialEnvVar+"=ghs_tok" {
			foundToken = true
			continue
		}
		if strings.Contains(e, "ghs_tok") {
			t.Fatalf("token leaked outside %s: %q", gitTokenCredentialEnvVar, e)
		}
	}
	if !foundToken {
		t.Fatalf("no %s=ghs_tok in %v", gitTokenCredentialEnvVar, env)
	}
	if !strings.Contains(strings.Join(env, "\n"), "GIT_CONFIG_KEY_1=credential.helper") {
		t.Fatalf("credential.helper not configured via GIT_CONFIG_*: %v", env)
	}
}

func TestAppCredentialWarnings(t *testing.T) {
	if w := credentialWarning(SubstrateSpec{Auth: AuthSpec{Method: "app", PrivateKeyPath: "/x.pem"}}, nil); !strings.Contains(w, "appId") {
		t.Fatalf("missing appId should warn, got %q", w)
	}
	if w := credentialWarning(SubstrateSpec{Auth: AuthSpec{Method: "app", AppID: "1"}}, nil); !strings.Contains(w, "privateKey") {
		t.Fatalf("missing key should warn, got %q", w)
	}
}

// TestGithubAppLive is an opt-in end-to-end check against the real GitHub API,
// run only when OT_LIVE_APP_ID / OT_LIVE_APP_KEY / OT_LIVE_APP_REPO are set.
// It never prints the token.
func TestGithubAppLive(t *testing.T) {
	appID := os.Getenv("OT_LIVE_APP_ID")
	keyPath := os.Getenv("OT_LIVE_APP_KEY")
	repo := os.Getenv("OT_LIVE_APP_REPO")
	if appID == "" || keyPath == "" || repo == "" {
		t.Skip("set OT_LIVE_APP_ID / OT_LIVE_APP_KEY / OT_LIVE_APP_REPO to run the live GitHub App check")
	}
	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()

	token, err := githubAppInstallationToken(context.Background(), AppCredential{AppID: appID, PrivateKeyPath: keyPath}, repo)
	if err != nil {
		t.Fatalf("live token mint failed: %v", err)
	}
	if !strings.HasPrefix(token, "ghs_") {
		t.Fatalf("unexpected token shape")
	}
	// Prove the token actually authenticates: list the installation's repos.
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/installation/repositories", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("live api call failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("installation token did not authenticate: status %d", resp.StatusCode)
	}
	var out struct {
		TotalCount int `json:"total_count"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("live check OK: token authenticated, installation can see %d repo(s)\n", out.TotalCount)

	cred := ResolvedCredential{Method: CredentialApp, App: AppCredential{AppID: appID, PrivateKeyPath: keyPath}}
	authEnv, err := materializeGitAuth(context.Background(), cred, repo)
	if err != nil {
		t.Fatalf("materializeGitAuth failed: %v", err)
	}

	// The credential helper serves the real token to git via stdin.
	fill := exec.Command("git", "credential", "fill")
	fill.Env = append(os.Environ(), authEnv...)
	fill.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	fillOut, err := fill.Output()
	if err != nil {
		t.Fatalf("git credential fill failed: %v", err)
	}
	if !strings.Contains(string(fillOut), "username=x-access-token") || !strings.Contains(string(fillOut), "password="+token) {
		t.Fatalf("credential helper did not serve the expected credentials")
	}
	fmt.Println("live check OK: credential helper serves x-access-token + the installation token")

	// End-to-end: clone with auth in the environment only, and prove the token
	// never lands in argv or the persisted .git/config.
	dest := filepath.Join(t.TempDir(), "clone")
	clone := exec.Command("git", "clone", "--depth", "1", "--", repo, dest)
	clone.Env = append(os.Environ(), authEnv...)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("env-auth clone failed: %v (%s)", err, out)
	}
	cfg, _ := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if strings.Contains(string(cfg), token) || strings.Contains(string(cfg), "x-access-token") {
		t.Fatalf(".git/config leaked the token — hardening regressed")
	}
	fmt.Println("live check OK: env-auth clone succeeded and .git/config is token-free")
}
