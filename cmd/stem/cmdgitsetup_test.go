package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// TestParseGitSetupArgsDefaultsAndValidation covers the secure defaults and the
// per-posture required-flag enforcement.
func TestParseGitSetupArgsDefaultsAndValidation(t *testing.T) {
	// App posture is the default; managed checkout is the default.
	opts, err := parseGitSetupArgs([]string{"--substrate", "r", "--repo", "o/r", "--app-id", "1", "--key", "/k.pem"})
	if err != nil {
		t.Fatalf("valid app args rejected: %v", err)
	}
	if opts.posture != "app" || opts.checkout != "managed" {
		t.Fatalf("defaults = posture %q checkout %q, want app/managed", opts.posture, opts.checkout)
	}

	for name, args := range map[string][]string{
		"missing substrate":    {"--repo", "o/r", "--app-id", "1", "--key", "/k"},
		"missing repo":         {"--substrate", "r", "--app-id", "1", "--key", "/k"},
		"bad posture":          {"--posture", "nope", "--substrate", "r", "--repo", "o/r"},
		"bad checkout":         {"--substrate", "r", "--repo", "o/r", "--checkout", "nope", "--app-id", "1", "--key", "/k"},
		"app missing key":      {"--substrate", "r", "--repo", "o/r", "--app-id", "1"},
		"pat missing sign-key": {"--posture", "pat", "--substrate", "r", "--repo", "o/r", "--identity-name", "n", "--identity-email", "e@x"},
		"pat missing identity": {"--posture", "pat", "--substrate", "r", "--repo", "o/r", "--sign-key", "k"},
		"repo without slash":   {"--substrate", "r", "--repo", "noslash", "--app-id", "1", "--key", "/k"},
		"unknown flag":         {"--substrate", "r", "--repo", "o/r", "--bogus"},
	} {
		if _, err := parseGitSetupArgs(args); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

// TestParseGitSetupArgsPatDefaultsTokenEnv verifies the pat posture defaults the
// token env when the caller omits it (a low-cognitive-load default).
func TestParseGitSetupArgsPatDefaultsTokenEnv(t *testing.T) {
	opts, err := parseGitSetupArgs([]string{
		"--posture", "pat", "--substrate", "r", "--repo", "o/r",
		"--sign-key", "KEY", "--identity-name", "N", "--identity-email", "e@x",
	})
	if err != nil {
		t.Fatalf("valid pat args rejected: %v", err)
	}
	if opts.tokenEnv != "GITHUB_TOKEN" {
		t.Fatalf("tokenEnv = %q, want the GITHUB_TOKEN default", opts.tokenEnv)
	}
}

// TestGeneratedAppConfigResolves proves the generated app-posture YAML is valid
// and resolves to a GitHub App credential in commit: api mode — the whole point
// of the command is that its output is directly usable.
func TestGeneratedAppConfigResolves(t *testing.T) {
	opts := gitSetupOptions{posture: "app", substrate: "r", repo: "o/r", appID: "4276558", keyPath: "/tmp/k.pem", checkout: "managed"}
	cred := resolveGenerated(t, opts)
	if cred.Method != conductor.CredentialApp {
		t.Fatalf("method = %q, want app", cred.Method)
	}
	if cred.CommitMode != conductor.CommitModeAPI {
		t.Fatalf("commit mode = %q, want api", cred.CommitMode)
	}
	if cred.App.AppID != "4276558" {
		t.Fatalf("app id = %q, want 4276558", cred.App.AppID)
	}
}

// TestGeneratedPatConfigResolves proves the generated pat-posture YAML resolves
// to a PAT credential carrying the dedicated signing key and identity.
func TestGeneratedPatConfigResolves(t *testing.T) {
	opts := gitSetupOptions{
		posture: "pat", substrate: "r", repo: "o/r", tokenEnv: "TENDRIL_GITHUB_PAT",
		signKey: "ABC123", identityName: "Tendril Bot", identityEmail: "bot@example.com", checkout: "managed",
	}
	cred := resolveGenerated(t, opts)
	if cred.Method != conductor.CredentialPAT {
		t.Fatalf("method = %q, want pat", cred.Method)
	}
	if cred.Sign.Method != "gpg" || cred.Sign.Key != "ABC123" {
		t.Fatalf("sign = %+v, want gpg/ABC123", cred.Sign)
	}
	if cred.Identity.Name != "Tendril Bot" || cred.Identity.Email != "bot@example.com" {
		t.Fatalf("identity = %+v, want the configured name/email", cred.Identity)
	}
}

// resolveGenerated writes the generated substrates.yaml to a temp dir and
// resolves the substrate's credential through the real conductor loader.
func resolveGenerated(t *testing.T, opts gitSetupOptions) conductor.ResolvedCredential {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "substrates.yaml"), []byte(renderSubstratesYAML(opts)), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := conductor.LoadSubstratesConfig(dir)
	if err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	spec, isName := conductor.ResolveSubstrate(opts.substrate, cfg)
	if !isName || spec == nil {
		t.Fatalf("generated config did not resolve substrate %q", opts.substrate)
	}
	cred, err := conductor.ResolveSubstrateCredential(*spec, cfg)
	if err != nil {
		t.Fatalf("resolve generated credential: %v", err)
	}
	return cred
}

// TestRenderGrantsYAMLParses proves the generated grant is valid control-plane
// YAML for the named Pollen and substrate.
func TestRenderGrantsYAMLParses(t *testing.T) {
	opts := gitSetupOptions{substrate: "r", grantPollen: "claude"}
	out := renderGrantsYAML(opts)
	for _, want := range []string{"grants:", "claude:", "operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr]", "substrates: [r]"} {
		if !strings.Contains(out, want) {
			t.Errorf("generated grants missing %q:\n%s", want, out)
		}
	}
}

// TestGitSetupGrantRemainsGitOnly is the first-use regression: git setup
// --grant-pollen writes the delegated Git loop and nothing else. seed.grow,
// sprout.watch, and sprout.grow stay absent until the Botanist grants them
// explicitly.
func TestGitSetupGrantRemainsGitOnly(t *testing.T) {
	dir := t.TempDir()
	tendrilDir := filepath.Join(dir, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := gitSetupOptions{substrate: "myrepo", grantPollen: "claude"}
	if err := upsertGrants(filepath.Join(tendrilDir, "grants.yaml"), opts); err != nil {
		t.Fatalf("upsertGrants: %v", err)
	}

	out := renderGrantsYAML(opts)
	for _, banned := range []string{core.CapSeedGrow, core.CapSproutWatch, core.CapSproutGrow} {
		if strings.Contains(out, banned) {
			t.Errorf("generated git grant contains %s:\n%s", banned, out)
		}
	}

	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		t.Fatalf("load grants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	classes := grants[0].OperationClasses
	if len(classes) != 6 {
		t.Errorf("operation-classes = %v, want the six git classes", classes)
	}
	for _, banned := range []string{core.CapSeedGrow, core.CapSproutWatch, core.CapSproutGrow} {
		if contains(classes, banned) {
			t.Errorf("git setup grant includes %s: %v", banned, classes)
		}
	}
	for _, want := range []string{core.CapGitStatus, core.CapGitBranchList, core.CapGitBranch, core.CapGitCommit, core.CapGitPush, core.CapGitPR} {
		if !contains(classes, want) {
			t.Errorf("git setup grant missing %s: %v", want, classes)
		}
	}
}

// TestUpsertMergesMultipleConnections proves a second setup run for a different
// repo is additive: both connections resolve from the one substrates.yaml, and
// a pre-existing comment survives the node-level merge.
func TestUpsertMergesMultipleConnections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "substrates.yaml")

	app := gitSetupOptions{posture: "app", substrate: "repo1", repo: "o/r1", appID: "1", keyPath: "/k1", checkout: "managed"}
	if err := upsertSubstrates(path, app); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	pat := gitSetupOptions{posture: "pat", substrate: "repo2", repo: "o/r2", tokenEnv: "TOK", signKey: "K", identityName: "N", identityEmail: "e@x", checkout: "managed"}
	if err := upsertSubstrates(path, pat); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "Generated by") {
		t.Error("the fresh-file header comment was lost across the merge")
	}
	cfg, err := conductor.LoadSubstratesConfig(dir)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	for name, wantMethod := range map[string]conductor.CredentialMethod{"repo1": conductor.CredentialApp, "repo2": conductor.CredentialPAT} {
		spec, isName := conductor.ResolveSubstrate(name, cfg)
		if !isName || spec == nil {
			t.Fatalf("merged config lost substrate %q", name)
		}
		cred, err := conductor.ResolveSubstrateCredential(*spec, cfg)
		if err != nil {
			t.Fatalf("resolve %q: %v", name, err)
		}
		if cred.Method != wantMethod {
			t.Errorf("%q method = %q, want %q", name, cred.Method, wantMethod)
		}
	}
}

// TestUpsertGrantUnionsSubstrates proves granting an existing Pollinator access to a
// second repo adds the substrate to its list rather than replacing it, and a
// distinct pollen is kept separate.
func TestUpsertGrantUnionsSubstrates(t *testing.T) {
	dir := t.TempDir()
	tendrilDir := filepath.Join(dir, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tendrilDir, "grants.yaml")

	for _, o := range []gitSetupOptions{
		{substrate: "repo1", grantPollen: "claude"},
		{substrate: "repo2", grantPollen: "claude"},
		{substrate: "repo1", grantPollen: "codex"},
	} {
		if err := upsertGrants(path, o); err != nil {
			t.Fatalf("upsert grant %+v: %v", o, err)
		}
	}

	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		t.Fatalf("load grants: %v", err)
	}
	byPollen := map[string][]string{}
	classesByPollen := map[string][]string{}
	for _, g := range grants {
		byPollen[g.Pollen] = g.Substrates
		classesByPollen[g.Pollen] = g.OperationClasses
	}

	// Every setup run grants the full governed loop — commit, push, and the
	// pull request that finishes it — so an authorised Pollinator never has to
	// leave Tendril for the last mile. Unioning must not duplicate them.
	for pollen, classes := range classesByPollen {
		if len(classes) != 6 {
			t.Errorf("%s operation-classes = %v, want exactly the six granted git classes unioned once", pollen, classes)
		}
		// git.prune is deliberately absent: every other operation on the
		// ladder is recoverable, deletion is not, so the destructive class is
		// opt-in rather than handed to every Pollinator by default.
		for _, unwanted := range classes {
			if unwanted == core.CapGitPrune {
				t.Errorf("%s was granted %s by default — the destructive class must be opt-in", pollen, core.CapGitPrune)
			}
		}
		for _, want := range []string{core.CapGitStatus, core.CapGitBranchList, core.CapGitBranch, core.CapGitCommit, core.CapGitPush, core.CapGitPR} {
			if !contains(classes, want) {
				t.Errorf("%s operation-classes = %v, want %s included", pollen, classes, want)
			}
		}
	}
	if got := byPollen["claude"]; len(got) != 2 || !contains(got, "repo1") || !contains(got, "repo2") {
		t.Errorf("claude substrates = %v, want [repo1 repo2] unioned", got)
	}
	if got := byPollen["codex"]; len(got) != 1 || got[0] != "repo1" {
		t.Errorf("codex substrates = %v, want [repo1]", got)
	}
}

// TestMergeConnectionForceGate proves an existing named connection is not
// overwritten without --force, and is updated with it.
func TestMergeConnectionForceGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "substrates.yaml")
	base := gitSetupOptions{posture: "app", substrate: "repo1", repo: "o/r1", appID: "1", keyPath: "/k1", checkout: "managed"}
	if err := upsertSubstrates(path, base); err != nil {
		t.Fatalf("fresh write: %v", err)
	}

	changed := base
	changed.repo = "o/other"
	if err := upsertSubstrates(path, changed); err == nil {
		t.Fatal("re-running for an existing connection without --force overwrote it")
	}

	changed.force = true
	if err := upsertSubstrates(path, changed); err != nil {
		t.Fatalf("forced update: %v", err)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "github.com/o/other") {
		t.Error("forced update did not replace the connection's url")
	}
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

type gitHubCall struct {
	Method        string
	Path          string
	Authorization string
}

func genSetupKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func writeAppVerifyFixture(t *testing.T, appID, repo string, pemBytes []byte) string {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	opts := gitSetupOptions{
		posture: "app", substrate: "garden", repo: repo,
		appID: appID, keyPath: keyPath, checkout: "managed",
	}
	if err := os.WriteFile(filepath.Join(dir, "substrates.yaml"), []byte(renderSubstratesYAML(opts)), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return dir
}

func writePATVerifyFixture(t *testing.T, repo, tokenEnv, token string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(tokenEnv, token)
	opts := gitSetupOptions{
		posture: "pat", substrate: "garden", repo: repo,
		tokenEnv: tokenEnv, signKey: "KEY", identityName: "N", identityEmail: "e@x",
		checkout: "managed",
	}
	if err := os.WriteFile(filepath.Join(dir, "substrates.yaml"), []byte(renderSubstratesYAML(opts)), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return dir
}

func rewriteCheckout(t *testing.T, dir, checkoutYAML string) {
	t.Helper()
	path := filepath.Join(dir, "substrates.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	updated := strings.Replace(string(raw), "    checkout: { mode: managed }\n", "    checkout: "+checkoutYAML+"\n", 1)
	if updated == string(raw) {
		t.Fatalf("did not replace managed checkout in %s", raw)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func captureVerifyOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	outDone := make(chan string)
	errDone := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(outR)
		outDone <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(errR)
		errDone <- buf.String()
	}()
	fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-outDone, <-errDone
}

type setupVerifyFakeOpts struct {
	appStatus     int
	installStatus int
	repoStatus    int
	leakyBody     string
	emptyRepo     bool
	defaultBranch string
	commitSHA     string
	branches      map[string]string
	installToken  string
	// contentsPermission is the value in permissions.contents for
	// GET /app/installations/{id}. Empty means the field is absent (treated as
	// no permission). Defaults to "write" when the existing app tests call
	// startSetupVerifyFake (which passes zero opts); set explicitly in Slice 2
	// tests.
	contentsPermission string
}

func startSetupVerifyFake(t *testing.T, appStatus, installStatus, repoStatus int, leakyBody string, calls *[]gitHubCall) {
	t.Helper()
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		appStatus:     appStatus,
		installStatus: installStatus,
		repoStatus:    repoStatus,
		leakyBody:     leakyBody,
		defaultBranch: "trunk",
		commitSHA:     "0123456789abcdef0123456789abcdef01234567",
		installToken:  "ghs_INSTALL_VERIFY_TOKEN",
	}, calls)
}

func startSetupVerifyServer(t *testing.T, opts setupVerifyFakeOpts, calls *[]gitHubCall) {
	t.Helper()
	if opts.defaultBranch == "" {
		opts.defaultBranch = "trunk"
	}
	if opts.commitSHA == "" {
		opts.commitSHA = "0123456789abcdef0123456789abcdef01234567"
	}
	if opts.installToken == "" {
		opts.installToken = "ghs_INSTALL_VERIFY_TOKEN"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, gitHubCall{Method: r.Method, Path: r.URL.Path, Authorization: r.Header.Get("Authorization")})
		switch {
		case r.URL.Path == "/app":
			if opts.appStatus != 0 && opts.appStatus != http.StatusOK {
				w.WriteHeader(opts.appStatus)
				_, _ = w.Write([]byte(opts.leakyBody))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
		case strings.Contains(r.URL.Path, "/access_tokens"):
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": opts.installToken})
		case strings.HasSuffix(r.URL.Path, "/installation"):
			if opts.installStatus != 0 && opts.installStatus != http.StatusOK {
				w.WriteHeader(opts.installStatus)
				_, _ = w.Write([]byte(opts.leakyBody))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99001})
		case strings.HasPrefix(r.URL.Path, "/app/installations/"):
			// Installation-detail endpoint for VerifyAppInstallationContentsWrite.
			perm := opts.contentsPermission
			if perm == "" {
				perm = "write" // safe default: existing non-api tests never reach here
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"permissions": map[string]any{"contents": perm},
			})
		default:
			serveSetupVerifyRepo(w, r, opts)
		}
	}))
	t.Cleanup(srv.Close)
	restore := conductor.RedirectGitHubAPIBaseURL(srv.URL)
	t.Cleanup(restore)
}

func serveSetupVerifyRepo(w http.ResponseWriter, r *http.Request, opts setupVerifyFakeOpts) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.Join(parts[3:], "/")
	switch {
	case rest == "":
		status := opts.repoStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": opts.defaultBranch})
		} else if opts.leakyBody != "" {
			_, _ = w.Write([]byte(opts.leakyBody))
		}
	case rest == "commits":
		if opts.emptyRepo {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"Git Repository is empty."}`))
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"sha": opts.commitSHA}})
	case strings.HasPrefix(rest, "commits/"):
		if opts.emptyRepo {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"Git Repository is empty.","token":"ghs_LEAKME_INSTALL"}`))
			return
		}
		branch := strings.TrimPrefix(rest, "commits/")
		if sha, ok := opts.branches[branch]; ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha})
			return
		}
		if opts.branches == nil && branch == opts.defaultBranch {
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": opts.commitSHA})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func assertNoMutatingCalls(t *testing.T, calls []gitHubCall) {
	t.Helper()
	for _, call := range calls {
		if strings.EqualFold(call.Method, http.MethodPost) && strings.Contains(strings.ToLower(call.Path), "/access_tokens") {
			continue
		}
		switch call.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			t.Errorf("repository-mutating GitHub HTTP method %s %s", call.Method, call.Path)
		}
		path := strings.ToLower(call.Path)
		for _, banned := range []string{"/git/", "/pulls", "/issues", "/merges"} {
			if strings.Contains(path, banned) {
				t.Errorf("repository-mutating GitHub HTTP path %s %s", call.Method, call.Path)
			}
		}
	}
}

func countSetupTokenIssuance(calls []gitHubCall) int {
	n := 0
	for _, call := range calls {
		if strings.EqualFold(call.Method, http.MethodPost) && strings.Contains(strings.ToLower(call.Path), "/access_tokens") {
			n++
		}
	}
	return n
}

func inspectsGitBase(calls []gitHubCall) bool {
	for _, call := range calls {
		if strings.Contains(call.Path, "/commits") {
			return true
		}
	}
	return false
}

func secretsFromCalls(calls []gitHubCall) []string {
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

func TestRunGitSetupVerifyAppRemoteSuccess(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyFake(t, http.StatusOK, http.StatusOK, http.StatusOK, "", &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if !runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("expected verify success")
		}
	})
	out := stdout + stderr
	if !strings.Contains(stdout, "the repository is ready as a managed Substrate") {
		t.Fatalf("stdout = %q, want Git-base readiness success", stdout)
	}
	if !strings.Contains(stdout, `Git base ready: branch "trunk"`) {
		t.Fatalf("stdout = %q, want the repository-reported default branch", stdout)
	}
	if countSetupTokenIssuance(calls) != 1 {
		t.Fatalf("App verify should issue one installation token, calls=%+v", calls)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "ghs_INSTALL_VERIFY_TOKEN", "ghs_", "BEGIN RSA PRIVATE KEY")...)
}

func TestRunGitSetupVerifyAppWrongAppID(t *testing.T) {
	dir := writeAppVerifyFixture(t, "881199", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyFake(t, http.StatusUnauthorized, 0, 0, `{"message":"Bad credentials","token":"ghs_LEAKME_INSTALL"}`, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("wrong App ID should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "rejected the App credentials") {
		t.Fatalf("output = %q, want rejected App credentials", out)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "ghs_LEAKME_INSTALL")...)
}

func TestRunGitSetupVerifyAppMalformedPEM(t *testing.T) {
	const marker = "SECRETPEM-TEST-MARKER"
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", []byte("-----BEGIN RSA PRIVATE KEY-----\n"+marker+"\n-----END RSA PRIVATE KEY-----\n"))
	var calls []gitHubCall
	startSetupVerifyFake(t, http.StatusOK, http.StatusOK, http.StatusOK, "", &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("malformed PEM should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "malformed") {
		t.Fatalf("output = %q, want malformed PEM", out)
	}
	if len(calls) != 0 {
		t.Fatalf("malformed PEM must not call GitHub, got %+v", calls)
	}
	assertNoSecretSubstrings(t, out, marker)
}

func TestRunGitSetupVerifyAppNotInstalled(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyFake(t, http.StatusOK, http.StatusNotFound, http.StatusOK, `{"token":"ghs_LEAKME_INSTALL"}`, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("missing installation should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "not installed on acme/widget") {
		t.Fatalf("output = %q, want not installed", out)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "ghs_LEAKME_INSTALL")...)
}

func TestRunGitSetupVerifyAppInaccessibleRepo(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyFake(t, http.StatusOK, http.StatusNotFound, http.StatusNotFound, "", &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("inaccessible repository should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "does not exist or is inaccessible") {
		t.Fatalf("output = %q, want inaccessible repository", out)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, secretsFromCalls(calls)...)
}

func TestRunGitSetupVerifyAppEmptyRepository(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		appStatus: http.StatusOK, installStatus: http.StatusOK, repoStatus: http.StatusOK,
		emptyRepo: true, leakyBody: `{"token":"ghs_LEAKME_INSTALL"}`,
	}, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("empty repository should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "no Git base") || !strings.Contains(out, "git setup --verify") {
		t.Fatalf("output = %q, want actionable no-Git-base diagnosis", out)
	}
	if strings.Contains(out, "not installed") || strings.Contains(out, "inaccessible") {
		t.Fatalf("empty repository must stay distinct from auth failures: %q", out)
	}
	if countSetupTokenIssuance(calls) != 1 {
		t.Fatalf("App empty-repo verify should still issue an installation token, calls=%+v", calls)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "ghs_LEAKME_INSTALL", "ghs_INSTALL_VERIFY_TOKEN")...)
}

func TestRunGitSetupVerifyPATRemoteSuccess(t *testing.T) {
	dir := writePATVerifyFixture(t, "acme/widget", "TENDRIL_TEST_PAT", "github_pat_LEAKME_PAT")
	var calls []gitHubCall
	startSetupVerifyFake(t, 0, 0, http.StatusOK, "", &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if !runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("expected verify success")
		}
	})
	out := stdout + stderr
	if !strings.Contains(stdout, "the repository is ready as a managed Substrate") {
		t.Fatalf("stdout = %q, want Git-base readiness success", stdout)
	}
	if countSetupTokenIssuance(calls) != 0 {
		t.Fatalf("PAT verify must not mint an App installation token, calls=%+v", calls)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "github_pat_LEAKME_PAT")...)
}

func TestRunGitSetupVerifyPATEmptyRepository(t *testing.T) {
	dir := writePATVerifyFixture(t, "acme/widget", "TENDRIL_TEST_PAT", "github_pat_LEAKME_PAT")
	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		repoStatus: http.StatusOK, emptyRepo: true, leakyBody: `{"token":"github_pat_LEAKME_PAT"}`,
	}, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("empty repository should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "no Git base") || !strings.Contains(out, "OpenTendril Substrate") {
		t.Fatalf("output = %q, want actionable no-Git-base diagnosis", out)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "github_pat_LEAKME_PAT")...)
}

func TestRunGitSetupVerifyPATInaccessibleRepo(t *testing.T) {
	dir := writePATVerifyFixture(t, "acme/widget", "TENDRIL_TEST_PAT", "github_pat_LEAKME_PAT")
	var calls []gitHubCall
	startSetupVerifyFake(t, 0, 0, http.StatusNotFound, `{"token":"github_pat_LEAKME_PAT"}`, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("inaccessible repository should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "does not exist or is inaccessible") {
		t.Fatalf("output = %q, want inaccessible repository", out)
	}
	if strings.Contains(out, "no Git base") {
		t.Fatalf("inaccessible must stay distinct from empty: %q", out)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "github_pat_LEAKME_PAT")...)
}

func TestRunGitSetupVerifyPathAppKeepsCredentialOnly(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	rewriteCheckout(t, dir, "{ mode: path, path: /tmp/ot-verify-path }")
	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		appStatus: http.StatusOK, installStatus: http.StatusOK, repoStatus: http.StatusOK,
		emptyRepo: true,
	}, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if !runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("path checkout should keep credential-only success")
		}
	})
	out := stdout + stderr
	if !strings.Contains(stdout, "authenticated to the remote repository") {
		t.Fatalf("stdout = %q, want pre-slice App success", stdout)
	}
	if strings.Contains(out, "ready as a managed Substrate") {
		t.Fatalf("path checkout must not be described as managed: %q", out)
	}
	if strings.Contains(out, "Git base ready") || strings.Contains(out, "no Git base") {
		t.Fatalf("path checkout must not acquire managed Git-base readiness: %q", out)
	}
	if inspectsGitBase(calls) {
		t.Fatalf("path checkout must not inspect Git base, calls=%+v", calls)
	}
	if countSetupTokenIssuance(calls) != 0 {
		t.Fatalf("path checkout must not mint an installation token, calls=%+v", calls)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, secretsFromCalls(calls)...)
}

func TestRunGitSetupVerifyEphemeralPATKeepsCredentialOnly(t *testing.T) {
	dir := writePATVerifyFixture(t, "acme/widget", "TENDRIL_TEST_PAT", "github_pat_LEAKME_PAT")
	rewriteCheckout(t, dir, "{ mode: ephemeral }")
	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{emptyRepo: true}, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if !runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("ephemeral checkout should keep credential-only success")
		}
	})
	out := stdout + stderr
	if !strings.Contains(stdout, "authentication material present") {
		t.Fatalf("stdout = %q, want pre-slice PAT success", stdout)
	}
	if strings.Contains(out, "ready as a managed Substrate") {
		t.Fatalf("ephemeral checkout must not be described as managed: %q", out)
	}
	if strings.Contains(out, "Git base ready") || strings.Contains(out, "no Git base") {
		t.Fatalf("ephemeral checkout must not acquire managed Git-base readiness: %q", out)
	}
	if inspectsGitBase(calls) || len(calls) != 0 {
		t.Fatalf("ephemeral PAT must not call GitHub, calls=%+v", calls)
	}
	assertNoSecretSubstrings(t, out, "github_pat_LEAKME_PAT")
}

func TestGitSetupCLIContainsNoGitHubAuthImplementation(t *testing.T) {
	src, err := os.ReadFile("cmdgitsetup.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, banned := range []string{
		"crypto/rsa",
		"encoding/pem",
		"mintAppJWT",
		"loadRSAPrivateKey",
		"/access_tokens",
		"githubAppAPI",
		"RS256",
		"githubReadinessGET",
		"inspectRepositoryGitBase",
		"default_branch",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("CLI adapter contains GitHub auth implementation %q", banned)
		}
	}
}

func assertNoSecretSubstrings(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Errorf("secret material appeared in output %q", text)
		}
	}
}

// ----------------------------------------------------------------------------
// Slice 2: CLI-layer managed commit:api fail-early readiness tests
// ----------------------------------------------------------------------------

// TestRunGitSetupVerifyManagedAPIAppWithWriteSucceeds verifies that a managed
// App+commit:api Substrate with contents:write passes verify and reports the
// write-permission confirmation in its output.
func TestRunGitSetupVerifyManagedAPIAppWithWriteSucceeds(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		appStatus: http.StatusOK, installStatus: http.StatusOK, repoStatus: http.StatusOK,
		defaultBranch: "trunk", commitSHA: "0123456789abcdef0123456789abcdef01234567",
		installToken: "ghs_INSTALL_VERIFY_TOKEN", contentsPermission: "write",
	}, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if !runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("managed App+api with write permission should pass verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(stdout, "the repository is ready as a managed Substrate") {
		t.Fatalf("stdout = %q, want managed Substrate readiness", stdout)
	}
	if !strings.Contains(stdout, "contents write") {
		t.Fatalf("stdout = %q, want contents write permission confirmed", stdout)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "ghs_INSTALL_VERIFY_TOKEN", "ghs_")...)
}

// TestRunGitSetupVerifyManagedAPIAppReadOnlyFails verifies that a managed
// App+commit:api Substrate whose installation has only read permission fails
// verify with an actionable message naming the required permission.
func TestRunGitSetupVerifyManagedAPIAppReadOnlyFails(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		appStatus: http.StatusOK, installStatus: http.StatusOK, repoStatus: http.StatusOK,
		defaultBranch: "trunk", commitSHA: "0123456789abcdef0123456789abcdef01234567",
		installToken: "ghs_INSTALL_VERIFY_TOKEN", contentsPermission: "read",
	}, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		if runGitSetupVerify(context.Background(), gitSetupOptions{substrate: "garden", dir: dir, verify: true}) {
			t.Error("managed App+api with read-only permission should fail verify")
		}
	})
	out := stdout + stderr
	if !strings.Contains(out, "write") {
		t.Fatalf("output = %q, want missing write permission mentioned", out)
	}
	if !strings.Contains(out, "Contents") {
		t.Fatalf("output = %q, want GitHub Contents permission mentioned", out)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "ghs_INSTALL_VERIFY_TOKEN")...)
}
