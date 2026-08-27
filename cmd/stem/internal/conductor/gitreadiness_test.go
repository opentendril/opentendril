package conductor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	readinessInstallToken = "ghs_READINESS_INSTALL_TOKEN"
	readinessPAT          = "github_pat_LEAKME_PAT"
	readinessCommitSHA    = "0123456789abcdef0123456789abcdef01234567"
)

type gitReadinessFake struct {
	appStatus     int
	installStatus int
	repoStatus    int
	installID     int64
	leakyBody     string
	defaultBranch string
	emptyRepo     bool
	commits       map[string]string
	installToken  string
	// installDetailStatus controls the HTTP status of GET /app/installations/{id}.
	// Zero means 200 OK (the default, returns contentsPermission).
	installDetailStatus int
	// contentsPermission is the value returned in permissions.contents when
	// installDetailStatus is 0 or 200. Empty string means the field is absent.
	contentsPermission string
	calls              []recordedGitHubCall
}

func (f *gitReadinessFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	case strings.Contains(r.URL.Path, "/access_tokens"):
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		token := f.installToken
		if token == "" {
			token = readinessInstallToken
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": token})
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
	case strings.HasPrefix(r.URL.Path, "/app/installations/"):
		// Installation-detail endpoint: returns permissions for the installation.
		if f.installDetailStatus != 0 && f.installDetailStatus != http.StatusOK {
			w.WriteHeader(f.installDetailStatus)
			if f.leakyBody != "" {
				_, _ = w.Write([]byte(f.leakyBody))
			}
			return
		}
		perm := f.contentsPermission
		if perm == "" {
			perm = "write"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"permissions": map[string]any{"contents": perm},
		})
	default:
		f.serveRepo(w, r)
	}
}

func (f *gitReadinessFake) serveRepo(w http.ResponseWriter, r *http.Request) {
	owner, repo, rest, ok := parseReposAPIPath(r.URL.Path)
	if !ok || owner != "acme" || repo != "widget" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case rest == "":
		status := f.repoStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			branch := f.defaultBranch
			if branch == "" {
				branch = "trunk"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": branch})
		} else if f.leakyBody != "" {
			_, _ = w.Write([]byte(f.leakyBody))
		}
	case rest == "commits":
		f.serveCommitList(w)
	case strings.HasPrefix(rest, "commits/"):
		branch := strings.TrimPrefix(rest, "commits/")
		f.serveCommit(w, branch)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *gitReadinessFake) serveCommitList(w http.ResponseWriter) {
	if f.emptyRepo {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Git Repository is empty.","token":"ghs_LEAKME_INSTALL"}`))
		return
	}
	sha := readinessCommitSHA
	for _, candidate := range f.commits {
		sha = candidate
		break
	}
	_ = json.NewEncoder(w).Encode([]map[string]any{{"sha": sha}})
}

func (f *gitReadinessFake) serveCommit(w http.ResponseWriter, branch string) {
	if f.emptyRepo {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Git Repository is empty.","token":"ghs_LEAKME_INSTALL"}`))
		return
	}
	if sha, ok := f.commits[branch]; ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha})
		return
	}
	if f.commits == nil {
		want := f.defaultBranch
		if want == "" {
			want = "trunk"
		}
		if branch == want {
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": readinessCommitSHA})
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	if f.leakyBody != "" {
		_, _ = w.Write([]byte(f.leakyBody))
		return
	}
	_, _ = w.Write([]byte(`{"message":"Not Found"}`))
}

func parseReposAPIPath(path string) (owner, repo, rest string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" {
		return "", "", "", false
	}
	return parts[1], parts[2], strings.Join(parts[3:], "/"), true
}

func startReadinessFake(t *testing.T, fake *gitReadinessFake) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	restore := RedirectGitHubAPIBaseURL(srv.URL)
	t.Cleanup(restore)
}

func appReadinessCred(t *testing.T) ResolvedCredential {
	t.Helper()
	_, keyPath := genTestKeyPEM(t)
	return ResolvedCredential{
		Method: CredentialApp,
		App:    AppCredential{AppID: "772211", PrivateKeyPath: keyPath},
	}
}

func patReadinessCred() ResolvedCredential {
	return ResolvedCredential{
		Method:     CredentialPAT,
		TokenEnv:   "TENDRIL_TEST_PAT",
		TokenValue: readinessPAT,
	}
}

func widgetSpec(branch string) SubstrateSpec {
	return SubstrateSpec{URL: "https://github.com/acme/widget", Branch: branch}
}

func assertNoRepositoryMutation(t *testing.T, calls []recordedGitHubCall) {
	t.Helper()
	for _, call := range calls {
		if isGitHubAppTokenIssuance(call.Method, call.Path) {
			continue
		}
		switch call.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "":
		default:
			t.Errorf("repository-mutating GitHub HTTP method %s %s", call.Method, call.Path)
		}
	}
}

func isGitHubAppTokenIssuance(method, path string) bool {
	return strings.EqualFold(method, http.MethodPost) && strings.Contains(strings.ToLower(path), "/access_tokens")
}

func countTokenIssuance(calls []recordedGitHubCall) int {
	n := 0
	for _, call := range calls {
		if isGitHubAppTokenIssuance(call.Method, call.Path) {
			n++
		}
	}
	return n
}

func inspectsGitBase(calls []recordedGitHubCall) bool {
	for _, call := range calls {
		if strings.Contains(call.Path, "/commits") {
			return true
		}
	}
	return false
}

func managedWidgetSpec(branch string) SubstrateSpec {
	spec := widgetSpec(branch)
	spec.Checkout.Mode = "managed"
	return spec
}

func readinessSecrets(calls []recordedGitHubCall, extra ...string) []string {
	secrets := append(collectedAuthSecrets(calls), extra...)
	secrets = append(secrets, readinessInstallToken, readinessPAT, "ghs_LEAKME_INSTALL")
	return secrets
}

func TestVerifyManagedSubstrateGitReadinessAppReady(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk"}
	startReadinessFake(t, fake)

	got, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), appReadinessCred(t))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got.Branch != "trunk" || got.Commit != readinessCommitSHA {
		t.Fatalf("readiness = %+v, want trunk at the reported commit", got)
	}
	assertNoRepositoryMutation(t, fake.calls)
	if countTokenIssuance(fake.calls) != 1 {
		t.Fatalf("token issuance calls = %d, want 1 (authentication, not repository mutation); calls=%+v", countTokenIssuance(fake.calls), fake.calls)
	}
	foundDefault := false
	for _, call := range fake.calls {
		if strings.HasSuffix(call.Path, "/commits/trunk") {
			foundDefault = true
		}
		if strings.HasSuffix(call.Path, "/commits/main") || strings.HasSuffix(call.Path, "/commits/master") {
			t.Fatalf("guessed a branch name: %+v", call)
		}
	}
	if !foundDefault {
		t.Fatalf("did not inspect the repository-reported default branch, calls=%+v", fake.calls)
	}
}

func TestVerifyManagedSubstrateGitReadinessPATReady(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk"}
	startReadinessFake(t, fake)

	got, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), patReadinessCred())
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got.Branch != "trunk" || got.Commit != readinessCommitSHA {
		t.Fatalf("readiness = %+v, want trunk at the reported commit", got)
	}
	assertNoRepositoryMutation(t, fake.calls)
	if countTokenIssuance(fake.calls) != 0 {
		t.Fatalf("PAT readiness must not mint an App installation token, calls=%+v", fake.calls)
	}
}

func TestVerifyManagedSubstrateGitReadinessAppEmptyRepository(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", emptyRepo: true, leakyBody: `{"token":"ghs_LEAKME_INSTALL"}`}
	startReadinessFake(t, fake)

	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), appReadinessCred(t))
	if err == nil {
		t.Fatal("empty repository should fail readiness")
	}
	msg := err.Error()
	assertNoGitBaseDiagnosis(t, msg)
	if strings.Contains(msg, "not installed") || strings.Contains(msg, "inaccessible") || strings.Contains(msg, "rejected the App") {
		t.Fatalf("empty repository must not be reported as auth/install failure: %q", msg)
	}
	assertNoRepositoryMutation(t, fake.calls)
	if countTokenIssuance(fake.calls) != 1 {
		t.Fatalf("App empty-repo inspection should still mint an installation token, calls=%+v", fake.calls)
	}
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

func TestVerifyManagedSubstrateGitReadinessPATEmptyRepository(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", emptyRepo: true, leakyBody: `{"token":"github_pat_LEAKME_PAT"}`}
	startReadinessFake(t, fake)

	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), patReadinessCred())
	if err == nil {
		t.Fatal("empty repository should fail readiness")
	}
	msg := err.Error()
	assertNoGitBaseDiagnosis(t, msg)
	if strings.Contains(msg, "not set") || strings.Contains(msg, "inaccessible") {
		t.Fatalf("empty repository must not be reported as missing token or inaccessible: %q", msg)
	}
	assertNoRepositoryMutation(t, fake.calls)
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

func TestVerifyManagedSubstrateGitReadinessConfiguredBranchReady(t *testing.T) {
	fake := &gitReadinessFake{
		defaultBranch: "trunk",
		commits:       map[string]string{"release": readinessCommitSHA},
	}
	startReadinessFake(t, fake)

	got, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec("refs/heads/release"), appReadinessCred(t))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got.Branch != "release" {
		t.Fatalf("branch = %q, want the configured branch", got.Branch)
	}
	foundConfigured := false
	for _, call := range fake.calls {
		if strings.HasSuffix(call.Path, "/commits/release") {
			foundConfigured = true
		}
	}
	if !foundConfigured {
		t.Fatalf("configured branch was not inspected, calls=%+v", fake.calls)
	}
	assertNoRepositoryMutation(t, fake.calls)
}

func TestVerifyManagedSubstrateGitReadinessConfiguredBranchMissing(t *testing.T) {
	fake := &gitReadinessFake{
		defaultBranch: "trunk",
		commits:       map[string]string{"trunk": readinessCommitSHA},
		leakyBody:     `{"token":"ghs_LEAKME_INSTALL"}`,
	}
	startReadinessFake(t, fake)

	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec("release"), patReadinessCred())
	if err == nil {
		t.Fatal("missing configured branch should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, `configured branch "release"`) {
		t.Fatalf("error = %q, want configured branch diagnosis", msg)
	}
	if strings.Contains(msg, "no Git base") || strings.Contains(msg, "contains no commit") {
		t.Fatalf("missing branch must stay distinct from empty repository: %q", msg)
	}
	assertNoRepositoryMutation(t, fake.calls)
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

func TestVerifyManagedSubstrateGitReadinessInaccessibleDistinctFromEmpty(t *testing.T) {
	t.Run("app", func(t *testing.T) {
		fake := &gitReadinessFake{installStatus: http.StatusNotFound, repoStatus: http.StatusNotFound}
		startReadinessFake(t, fake)
		_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), appReadinessCred(t))
		if err == nil {
			t.Fatal("inaccessible repository should fail")
		}
		msg := err.Error()
		if !strings.Contains(msg, "does not exist or is inaccessible") {
			t.Fatalf("error = %q, want inaccessible repository", msg)
		}
		if strings.Contains(msg, "no Git base") || strings.Contains(msg, "contains no commit") {
			t.Fatalf("inaccessible must stay distinct from empty: %q", msg)
		}
		assertNoRepositoryMutation(t, fake.calls)
		assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
	})
	t.Run("pat", func(t *testing.T) {
		fake := &gitReadinessFake{repoStatus: http.StatusNotFound, leakyBody: `{"token":"github_pat_LEAKME_PAT"}`}
		startReadinessFake(t, fake)
		_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), patReadinessCred())
		if err == nil {
			t.Fatal("inaccessible repository should fail")
		}
		msg := err.Error()
		if !strings.Contains(msg, "does not exist or is inaccessible") {
			t.Fatalf("error = %q, want inaccessible repository", msg)
		}
		if strings.Contains(msg, "no Git base") || strings.Contains(msg, "contains no commit") {
			t.Fatalf("inaccessible must stay distinct from empty: %q", msg)
		}
		assertNoRepositoryMutation(t, fake.calls)
		assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
	})
}

func TestVerifyManagedSubstrateGitReadinessWrongAppCredential(t *testing.T) {
	fake := &gitReadinessFake{
		appStatus: http.StatusUnauthorized,
		leakyBody: `{"message":"Bad credentials","token":"ghs_LEAKME_INSTALL"}`,
	}
	startReadinessFake(t, fake)

	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), appReadinessCred(t))
	if err == nil {
		t.Fatal("wrong App credentials should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rejected the App credentials") {
		t.Fatalf("error = %q, want rejected App credentials", msg)
	}
	if strings.Contains(msg, "no Git base") {
		t.Fatalf("credential failure must stay distinct from empty repository: %q", msg)
	}
	if countTokenIssuance(fake.calls) != 0 {
		t.Fatalf("rejected App credentials must not mint an installation token, calls=%+v", fake.calls)
	}
	assertNoRepositoryMutation(t, fake.calls)
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

func TestVerifyManagedSubstrateGitReadinessMalformedAppKey(t *testing.T) {
	const marker = "SECRETPEM-TEST-MARKER"
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, []byte("-----BEGIN RSA PRIVATE KEY-----\n"+marker+"\n-----END RSA PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &gitReadinessFake{}
	startReadinessFake(t, fake)

	cred := ResolvedCredential{Method: CredentialApp, App: AppCredential{AppID: "772211", PrivateKeyPath: keyPath}}
	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), cred)
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

func TestVerifyManagedSubstrateGitReadinessAppNotInstalled(t *testing.T) {
	fake := &gitReadinessFake{
		installStatus: http.StatusNotFound,
		repoStatus:    http.StatusOK,
		leakyBody:     `{"token":"ghs_LEAKME_INSTALL"}`,
	}
	startReadinessFake(t, fake)

	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), appReadinessCred(t))
	if err == nil {
		t.Fatal("missing installation should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not installed on acme/widget") {
		t.Fatalf("error = %q, want not installed", msg)
	}
	if strings.Contains(msg, "no Git base") || strings.Contains(msg, "does not exist") {
		t.Fatalf("missing installation must stay distinct from empty/inaccessible: %q", msg)
	}
	if countTokenIssuance(fake.calls) != 0 {
		t.Fatalf("missing installation must not mint an installation token, calls=%+v", fake.calls)
	}
	assertNoRepositoryMutation(t, fake.calls)
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

func TestVerifyManagedSubstrateGitReadinessPATPresenceIsNotReadiness(t *testing.T) {
	fake := &gitReadinessFake{repoStatus: http.StatusNotFound}
	startReadinessFake(t, fake)

	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), patReadinessCred())
	if err == nil {
		t.Fatal("a present PAT for an inaccessible repository must not pass")
	}
	if !strings.Contains(err.Error(), "does not exist or is inaccessible") {
		t.Fatalf("error = %q, want inaccessible, not token-presence success", err)
	}
}

func TestVerifyManagedSubstrateGitReadinessSecretsNeverAppear(t *testing.T) {
	fake := &gitReadinessFake{
		emptyRepo:     true,
		defaultBranch: "trunk",
		leakyBody:     `{"token":"ghs_LEAKME_INSTALL","pem":"-----BEGIN RSA PRIVATE KEY-----\nSECRETPEM-TEST-MARKER\n-----END RSA PRIVATE KEY-----"}`,
	}
	startReadinessFake(t, fake)

	_, err := VerifyManagedSubstrateGitReadiness(context.Background(), widgetSpec(""), appReadinessCred(t))
	if err == nil {
		t.Fatal("expected empty-repo failure")
	}
	assertNoSecrets(t, err.Error(), append(readinessSecrets(fake.calls), "SECRETPEM-TEST-MARKER", "BEGIN RSA PRIVATE KEY")...)
}

func TestVerifySubstrateSetupManagedInvokesGitBase(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk"}
	startReadinessFake(t, fake)

	got, err := VerifySubstrateSetup(context.Background(), managedWidgetSpec(""), appReadinessCred(t))
	if err != nil {
		t.Fatalf("managed setup should succeed, got %v", err)
	}
	if !got.Managed {
		t.Fatal("managed checkout must report Managed")
	}
	if got.GitBase.Branch != "trunk" || got.GitBase.Commit != readinessCommitSHA {
		t.Fatalf("Git base = %+v, want trunk at the reported commit", got.GitBase)
	}
	if !inspectsGitBase(fake.calls) {
		t.Fatalf("managed checkout must inspect Git base, calls=%+v", fake.calls)
	}
	if countTokenIssuance(fake.calls) != 1 {
		t.Fatalf("managed App setup should mint an installation token, calls=%+v", fake.calls)
	}
}

func TestVerifySubstrateSetupManagedEmptyStillFails(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", emptyRepo: true}
	startReadinessFake(t, fake)

	_, err := VerifySubstrateSetup(context.Background(), managedWidgetSpec(""), appReadinessCred(t))
	if err == nil {
		t.Fatal("managed empty repository should fail Git-base readiness")
	}
	assertNoGitBaseDiagnosis(t, err.Error())
	if !inspectsGitBase(fake.calls) {
		t.Fatalf("managed checkout must inspect Git base, calls=%+v", fake.calls)
	}
}

func TestVerifySubstrateSetupPathDoesNotAcquireManagedReadiness(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", emptyRepo: true}
	startReadinessFake(t, fake)

	spec := widgetSpec("")
	spec.Checkout.Mode = "path"
	spec.Checkout.Path = "/tmp/ot-verify-path"
	got, err := VerifySubstrateSetup(context.Background(), spec, appReadinessCred(t))
	if err != nil {
		t.Fatalf("path checkout must keep credential-only success, got %v", err)
	}
	if got.Managed {
		t.Fatal("path checkout must not report Managed")
	}
	if got.GitBase != (SubstrateGitReadiness{}) {
		t.Fatalf("path checkout must not return a Git base, got %+v", got.GitBase)
	}
	if inspectsGitBase(fake.calls) {
		t.Fatalf("path checkout must not inspect Git base, calls=%+v", fake.calls)
	}
	if countTokenIssuance(fake.calls) != 0 {
		t.Fatalf("path checkout must not mint an installation token, calls=%+v", fake.calls)
	}
}

func TestVerifySubstrateSetupEphemeralDoesNotAcquireManagedReadiness(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", emptyRepo: true}
	startReadinessFake(t, fake)

	spec := widgetSpec("")
	spec.Checkout.Mode = "ephemeral"
	got, err := VerifySubstrateSetup(context.Background(), spec, patReadinessCred())
	if err != nil {
		t.Fatalf("ephemeral checkout must keep credential-only success, got %v", err)
	}
	if got.Managed {
		t.Fatal("ephemeral checkout must not report Managed")
	}
	if inspectsGitBase(fake.calls) {
		t.Fatalf("ephemeral PAT must not inspect Git base, calls=%+v", fake.calls)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("ephemeral PAT must not call GitHub, calls=%+v", fake.calls)
	}
}

func TestVerifySubstrateSetupPathAppDiagnosticsUnchanged(t *testing.T) {
	fake := &gitReadinessFake{
		appStatus: http.StatusUnauthorized,
		leakyBody: `{"message":"Bad credentials","token":"ghs_LEAKME_INSTALL"}`,
	}
	startReadinessFake(t, fake)

	spec := widgetSpec("")
	spec.Checkout.Mode = "path"
	spec.Checkout.Path = "/tmp/ot-verify-path"
	_, err := VerifySubstrateSetup(context.Background(), spec, appReadinessCred(t))
	if err == nil {
		t.Fatal("path App with rejected credentials should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rejected the App credentials") {
		t.Fatalf("error = %q, want rejected App credentials", msg)
	}
	if strings.Contains(msg, "no Git base") {
		t.Fatalf("path credential failure must not become a Git-base failure: %q", msg)
	}
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

func assertNoGitBaseDiagnosis(t *testing.T, msg string) {
	t.Helper()
	for _, want := range []string{
		"no Git base",
		"no commit",
		"OpenTendril Substrate",
		"git setup --verify",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want %q", msg, want)
		}
	}
}

// managedAPIWidgetSpec returns a managed Substrate spec with commit:api set.
func managedAPIWidgetSpec(branch string) SubstrateSpec {
	spec := managedWidgetSpec(branch)
	spec.Commit = CommitModeAPI
	return spec
}

// ----------------------------------------------------------------------------
// Slice 2: managed commit:api fail-early readiness tests
// ----------------------------------------------------------------------------

// TestVerifySubstrateSetupManagedAPIRequiresAppAuth verifies that a managed
// Substrate with commit:api is refused at verify time when the credential is not
// a GitHub App (e.g. a PAT), with an actionable error before any Seed runs.
func TestVerifySubstrateSetupManagedAPIRequiresAppAuth(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk"}
	startReadinessFake(t, fake)

	cred := patReadinessCred()
	cred.CommitMode = CommitModeAPI
	_, err := VerifySubstrateSetup(context.Background(), managedAPIWidgetSpec(""), cred)
	if err == nil {
		t.Fatal("managed commit:api with PAT should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, CommitModeAPI) {
		t.Fatalf("error = %q, want commit mode %q mentioned", msg, CommitModeAPI)
	}
	if !strings.Contains(msg, "app") {
		t.Fatalf("error = %q, want auth method \"app\" mentioned", msg)
	}
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

// TestVerifySubstrateSetupManagedAPIAppWithWritePassesAndSetsContentsWrite
// verifies that a managed App+commit:api Substrate that has contents:write passes
// and reports ContentsWrite=true on the verification result.
func TestVerifySubstrateSetupManagedAPIAppWithWritePassesAndSetsContentsWrite(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", contentsPermission: "write"}
	startReadinessFake(t, fake)

	cred := appReadinessCred(t)
	cred.CommitMode = CommitModeAPI
	got, err := VerifySubstrateSetup(context.Background(), managedAPIWidgetSpec(""), cred)
	if err != nil {
		t.Fatalf("managed App+api with write permission should succeed, got %v", err)
	}
	if !got.Managed {
		t.Fatal("managed checkout must report Managed")
	}
	if !got.ContentsWrite {
		t.Fatal("App installation with contents:write must set ContentsWrite=true")
	}
	assertNoRepositoryMutation(t, fake.calls)

	// Verify the installation-detail path was called (read-only GET).
	// Exclude the token-issuance path (/access_tokens) which also has the
	// /app/installations/ prefix but is a separate endpoint.
	foundDetail := false
	for _, call := range fake.calls {
		if strings.HasPrefix(call.Path, "/app/installations/") && !strings.Contains(call.Path, "/access_tokens") {
			foundDetail = true
			if call.Method != "GET" {
				t.Errorf("installation-detail call must be GET, got %s", call.Method)
			}
		}
	}
	if !foundDetail {
		t.Fatalf("installation-detail endpoint was not called, calls=%+v", fake.calls)
	}
}

// TestVerifySubstrateSetupProfileAPIAppWithWritePassesAndSetsContentsWrite
// verifies that a managed App Substrate whose credential profile supplies commit:api
// (while the inline substrate spec has no commit mode) correctly resolves the
// effective commit mode, exercises the contents-write probe, and reports
// ContentsWrite=true.
func TestVerifySubstrateSetupProfileAPIAppWithWritePassesAndSetsContentsWrite(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", contentsPermission: "write"}
	startReadinessFake(t, fake)

	// Create a credential that represents resolving a profile with commit:api,
	// while the substrate spec itself is just a standard managed spec (no inline commit).
	cred := appReadinessCred(t)
	cred.CommitMode = CommitModeAPI

	got, err := VerifySubstrateSetup(context.Background(), managedWidgetSpec(""), cred)
	if err != nil {
		t.Fatalf("managed App with profile-supplied api mode and write permission should succeed, got %v", err)
	}
	if !got.Managed {
		t.Fatal("managed checkout must report Managed")
	}
	if !got.ContentsWrite {
		t.Fatal("App installation with contents:write must set ContentsWrite=true when commit mode is api via profile")
	}
	assertNoRepositoryMutation(t, fake.calls)
}

// TestVerifySubstrateSetupManagedAPIAppReadPermissionFails verifies that a
// managed App+commit:api Substrate that has only contents:read (not write) fails
// with an actionable message naming the missing permission.
func TestVerifySubstrateSetupManagedAPIAppReadPermissionFails(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", contentsPermission: "read"}
	startReadinessFake(t, fake)

	cred := appReadinessCred(t)
	cred.CommitMode = CommitModeAPI
	_, err := VerifySubstrateSetup(context.Background(), managedAPIWidgetSpec(""), cred)
	if err == nil {
		t.Fatal("managed App+api with only read permission should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "write") {
		t.Fatalf("error = %q, want missing write permission mentioned", msg)
	}
	if !strings.Contains(msg, "Contents") {
		t.Fatalf("error = %q, want GitHub Contents permission mentioned", msg)
	}
	assertNoRepositoryMutation(t, fake.calls)
	assertNoSecrets(t, msg, readinessSecrets(fake.calls)...)
}

// TestVerifySubstrateSetupManagedAPINoPermissionFails verifies that a managed
// App+commit:api Substrate whose installation has no contents permission at all
// fails with an actionable message.
func TestVerifySubstrateSetupManagedAPINoPermissionFails(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", contentsPermission: "none"}
	startReadinessFake(t, fake)

	cred := appReadinessCred(t)
	cred.CommitMode = CommitModeAPI
	_, err := VerifySubstrateSetup(context.Background(), managedAPIWidgetSpec(""), cred)
	if err == nil {
		t.Fatal("managed App+api with no contents permission should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "write") {
		t.Fatalf("error = %q, want write permission diagnosis", msg)
	}
	assertNoRepositoryMutation(t, fake.calls)
}

// TestVerifySubstrateSetupManagedNonAPIDoesNotSetContentsWrite verifies that a
// managed Substrate without commit:api never sets ContentsWrite, even when the
// credential is a GitHub App.
func TestVerifySubstrateSetupManagedNonAPIDoesNotSetContentsWrite(t *testing.T) {
	fake := &gitReadinessFake{defaultBranch: "trunk", contentsPermission: "write"}
	startReadinessFake(t, fake)

	// managedWidgetSpec does not set commit:api.
	got, err := VerifySubstrateSetup(context.Background(), managedWidgetSpec(""), appReadinessCred(t))
	if err != nil {
		t.Fatalf("managed non-API App should succeed, got %v", err)
	}
	if !got.Managed {
		t.Fatal("managed checkout must report Managed")
	}
	if got.ContentsWrite {
		t.Fatal("non-API managed Substrate must not set ContentsWrite")
	}
	// The installation-detail endpoint must not be called for non-API substrates.
	// Exclude the token-issuance path (/access_tokens) which shares the prefix.
	for _, call := range fake.calls {
		if strings.HasPrefix(call.Path, "/app/installations/") && !strings.Contains(call.Path, "/access_tokens") {
			t.Errorf("non-API managed Substrate called installation-detail: %s %s", call.Method, call.Path)
		}
	}
}

// TestVerifyAppInstallationContentsWriteIsReadOnly confirms that
// VerifyAppInstallationContentsWrite issues only GET requests (plus the token
// issuance POST that is already permitted). No ref, commit, branch, or pull
// request may be created.
func TestVerifyAppInstallationContentsWriteIsReadOnly(t *testing.T) {
	fake := &gitReadinessFake{contentsPermission: "write"}
	startReadinessFake(t, fake)

	err := VerifyAppInstallationContentsWrite(context.Background(), appReadinessCred(t).App, "https://github.com/acme/widget")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	// No token issuance: VerifyAppInstallationContentsWrite uses the App JWT only.
	if countTokenIssuance(fake.calls) != 0 {
		t.Fatalf("VerifyAppInstallationContentsWrite must not mint an installation token, calls=%+v", fake.calls)
	}
	for _, call := range fake.calls {
		if strings.EqualFold(call.Method, "POST") && !strings.Contains(call.Path, "/access_tokens") {
			t.Errorf("unexpected mutating POST: %s %s", call.Method, call.Path)
		}
		if strings.EqualFold(call.Method, "PUT") || strings.EqualFold(call.Method, "PATCH") || strings.EqualFold(call.Method, "DELETE") {
			t.Errorf("mutating GitHub HTTP method %s %s", call.Method, call.Path)
		}
	}
}

// TestVerifyAppInstallationContentsWriteInstallDetailUnauthorized verifies that
// an HTTP 401 from the installation-detail endpoint surfaces as a credential
// rejection rather than a permission failure.
func TestVerifyAppInstallationContentsWriteInstallDetailUnauthorized(t *testing.T) {
	fake := &gitReadinessFake{
		installDetailStatus: http.StatusUnauthorized,
		leakyBody:           `{"message":"Bad credentials","token":"ghs_LEAKME_INSTALL"}`,
	}
	startReadinessFake(t, fake)

	err := VerifyAppInstallationContentsWrite(context.Background(), appReadinessCred(t).App, "https://github.com/acme/widget")
	if err == nil {
		t.Fatal("401 on install detail should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rejected the App credentials") {
		t.Fatalf("error = %q, want rejected App credentials", msg)
	}
	assertNoSecrets(t, msg, append(readinessSecrets(fake.calls), "ghs_LEAKME_INSTALL")...)
}
