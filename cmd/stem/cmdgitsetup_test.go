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

func startSetupVerifyFake(t *testing.T, appStatus, installStatus, repoStatus int, leakyBody string, calls *[]gitHubCall) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, gitHubCall{Method: r.Method, Path: r.URL.Path, Authorization: r.Header.Get("Authorization")})
		switch {
		case r.URL.Path == "/app":
			if appStatus != 0 && appStatus != http.StatusOK {
				w.WriteHeader(appStatus)
				_, _ = w.Write([]byte(leakyBody))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
		case strings.HasSuffix(r.URL.Path, "/installation"):
			if installStatus != 0 && installStatus != http.StatusOK {
				w.WriteHeader(installStatus)
				_, _ = w.Write([]byte(leakyBody))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99001})
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			status := repoStatus
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
	}))
	t.Cleanup(srv.Close)
	restore := conductor.RedirectGitHubAPIBaseURL(srv.URL)
	t.Cleanup(restore)
}

func assertNoMutatingCalls(t *testing.T, calls []gitHubCall) {
	t.Helper()
	for _, call := range calls {
		switch call.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
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
	if !strings.Contains(stdout, "authenticated to the remote repository") {
		t.Fatalf("stdout = %q, want remote authentication success", stdout)
	}
	assertNoMutatingCalls(t, calls)
	assertNoSecretSubstrings(t, out, append(secretsFromCalls(calls), "ghs_", "BEGIN RSA PRIVATE KEY")...)
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
