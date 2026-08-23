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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOwnerRepo(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/opentendril/opentendril.git":   {"opentendril", "opentendril"},
		"https://github.com/opentendril/opentendril":       {"opentendril", "opentendril"},
		"git@github.com:opentendril/opentendril.git":       {"opentendril", "opentendril"},
		"ssh://git@github.com/opentendril/opentendril.git": {"opentendril", "opentendril"},
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

	restore := RedirectGitHubAPIBaseURL(srv.URL)
	defer restore()
	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()

	app := AppCredential{AppID: "4276558", PrivateKeyPath: keyPath}
	tok, err := githubAppInstallationToken(context.Background(), app, "https://github.com/opentendril/opentendril.git")
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
	if _, err := githubAppInstallationToken(context.Background(), app, "https://github.com/opentendril/opentendril.git"); err != nil {
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
	restore := RedirectGitHubAPIBaseURL(srv.URL)
	defer restore()
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

type recordedGitHubCall struct {
	Method        string
	Path          string
	Authorization string
}

type appVerifyFake struct {
	appStatus     int
	installStatus int
	repoStatus    int
	installID     int64
	leakyBody     string
	calls         []recordedGitHubCall
}

func (f *appVerifyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, recordedGitHubCall{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
	})
	switch {
	case r.URL.Path == "/app":
		if f.appStatus != 0 && f.appStatus != http.StatusOK {
			w.WriteHeader(f.appStatus)
			if f.leakyBody != "" {
				_, _ = w.Write([]byte(f.leakyBody))
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	case strings.HasSuffix(r.URL.Path, "/installation"):
		if f.installStatus != 0 && f.installStatus != http.StatusOK {
			w.WriteHeader(f.installStatus)
			if f.leakyBody != "" {
				_, _ = w.Write([]byte(f.leakyBody))
			}
			return
		}
		id := f.installID
		if id == 0 {
			id = 99001
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	case strings.HasPrefix(r.URL.Path, "/repos/"):
		status := f.repoStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func startAppVerifyFake(t *testing.T, fake *appVerifyFake) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	restore := RedirectGitHubAPIBaseURL(srv.URL)
	t.Cleanup(restore)
}

func assertNoMutatingGitHubCalls(t *testing.T, calls []recordedGitHubCall) {
	t.Helper()
	for _, call := range calls {
		switch call.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "":
		default:
			t.Errorf("mutating GitHub HTTP method %s %s", call.Method, call.Path)
		}
		path := strings.ToLower(call.Path)
		for _, banned := range []string{"access_tokens", "/git/", "/pulls", "/issues", "/merges"} {
			if strings.Contains(path, banned) {
				t.Errorf("mutating GitHub HTTP path %s %s", call.Method, call.Path)
			}
		}
	}
}

func collectedAuthSecrets(calls []recordedGitHubCall) []string {
	var secrets []string
	for _, call := range calls {
		if auth := strings.TrimSpace(call.Authorization); auth != "" {
			secrets = append(secrets, auth)
			if bearer, ok := strings.CutPrefix(auth, "Bearer "); ok {
				secrets = append(secrets, bearer)
			}
		}
	}
	return secrets
}

func assertNoSecrets(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Errorf("secret material appeared in diagnostic %q", text)
		}
	}
}

func TestVerifyGitHubAppRemoteAccessSuccess(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	fake := &appVerifyFake{}
	startAppVerifyFake(t, fake)

	err := VerifyGitHubAppRemoteAccess(context.Background(), AppCredential{AppID: "772211", PrivateKeyPath: keyPath}, "https://github.com/acme/widget")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	assertNoMutatingGitHubCalls(t, fake.calls)
	if len(fake.calls) != 2 {
		t.Fatalf("calls = %+v, want GET /app and GET installation only", fake.calls)
	}
	if fake.calls[0].Path != "/app" || fake.calls[0].Method != http.MethodGet {
		t.Fatalf("first call = %+v, want GET /app", fake.calls[0])
	}
	if fake.calls[1].Path != "/repos/acme/widget/installation" || fake.calls[1].Method != http.MethodGet {
		t.Fatalf("second call = %+v, want GET installation", fake.calls[1])
	}
}

func TestVerifyGitHubAppRemoteAccessWrongAppID(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	fake := &appVerifyFake{
		appStatus: http.StatusUnauthorized,
		leakyBody: `{"message":"Bad credentials","token":"ghs_LEAKME_INSTALL"}`,
	}
	startAppVerifyFake(t, fake)

	err := VerifyGitHubAppRemoteAccess(context.Background(), AppCredential{AppID: "881199", PrivateKeyPath: keyPath}, "https://github.com/acme/widget")
	if err == nil {
		t.Fatal("wrong App ID should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rejected the App credentials") {
		t.Fatalf("error = %q, want rejected App credentials", msg)
	}
	assertNoMutatingGitHubCalls(t, fake.calls)
	secrets := append(collectedAuthSecrets(fake.calls), "ghs_LEAKME_INSTALL")
	assertNoSecrets(t, msg, secrets...)
}

func TestVerifyGitHubAppRemoteAccessMalformedPEM(t *testing.T) {
	const marker = "SECRETPEM-TEST-MARKER"
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(keyPath, []byte("-----BEGIN RSA PRIVATE KEY-----\n"+marker+"\n-----END RSA PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &appVerifyFake{}
	startAppVerifyFake(t, fake)

	err := VerifyGitHubAppRemoteAccess(context.Background(), AppCredential{AppID: "772211", PrivateKeyPath: keyPath}, "https://github.com/acme/widget")
	if err == nil {
		t.Fatal("malformed PEM should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "malformed") {
		t.Fatalf("error = %q, want malformed PEM", msg)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("malformed PEM must not call GitHub, got %+v", fake.calls)
	}
	assertNoSecrets(t, msg, marker)
}

func TestVerifyGitHubAppRemoteAccessNotInstalled(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	fake := &appVerifyFake{
		installStatus: http.StatusNotFound,
		repoStatus:    http.StatusOK,
		leakyBody:     `{"message":"Not Found","token":"ghs_LEAKME_INSTALL"}`,
	}
	startAppVerifyFake(t, fake)

	err := VerifyGitHubAppRemoteAccess(context.Background(), AppCredential{AppID: "772211", PrivateKeyPath: keyPath}, "https://github.com/acme/widget")
	if err == nil {
		t.Fatal("missing installation should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not installed on acme/widget") {
		t.Fatalf("error = %q, want not installed", msg)
	}
	if strings.Contains(msg, "does not exist") {
		t.Fatalf("public repo with no installation must not be reported as missing: %q", msg)
	}
	assertNoMutatingGitHubCalls(t, fake.calls)
	assertNoSecrets(t, msg, append(collectedAuthSecrets(fake.calls), "ghs_LEAKME_INSTALL")...)
}

func TestVerifyGitHubAppRemoteAccessInaccessibleRepo(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	fake := &appVerifyFake{
		installStatus: http.StatusNotFound,
		repoStatus:    http.StatusNotFound,
	}
	startAppVerifyFake(t, fake)

	err := VerifyGitHubAppRemoteAccess(context.Background(), AppCredential{AppID: "772211", PrivateKeyPath: keyPath}, "https://github.com/acme/widget")
	if err == nil {
		t.Fatal("inaccessible repository should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist or is inaccessible") {
		t.Fatalf("error = %q, want inaccessible repository", msg)
	}
	assertNoMutatingGitHubCalls(t, fake.calls)
	assertNoSecrets(t, msg, collectedAuthSecrets(fake.calls)...)
}

func TestVerifyGitHubAppRemoteAccessPinnedInstallationStillProbesRepo(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	fake := &appVerifyFake{}
	startAppVerifyFake(t, fake)

	app := AppCredential{AppID: "772211", InstallationID: 42, PrivateKeyPath: keyPath}
	if err := VerifyGitHubAppRemoteAccess(context.Background(), app, "https://github.com/acme/widget"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	foundInstall := false
	for _, call := range fake.calls {
		if call.Path == "/repos/acme/widget/installation" {
			foundInstall = true
		}
	}
	if !foundInstall {
		t.Fatalf("pinned installation id must still GET the repository installation, got %+v", fake.calls)
	}
	assertNoMutatingGitHubCalls(t, fake.calls)
}

func TestRedactCredentialMaterial(t *testing.T) {
	in := "pem -----BEGIN RSA PRIVATE KEY-----\nSECRETPEM-TEST-MARKER\n-----END RSA PRIVATE KEY----- token ghs_LEAKME_INSTALL jwt eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiIxIn0.sig"
	out := redactCredentialMaterial(in)
	for _, secret := range []string{"SECRETPEM-TEST-MARKER", "ghs_LEAKME_INSTALL", "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiIxIn0.sig"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redact left %q in %q", secret, out)
		}
	}
}
