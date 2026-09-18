package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/substrateconfig"
)

func TestPrintUsageListsSubstrateCommandFamily(t *testing.T) {
	stdout, _ := captureVerifyOutput(t, printUsage)
	if !strings.Contains(stdout, "substrate") {
		t.Fatalf("top-level usage = %q, want substrate command family", stdout)
	}
}

func TestSubstratePollenGatePrecedesEveryOperationalRegistryAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envPollenCLI, "worker")
	canonical := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	original := []byte("substrates: [")
	if err := os.WriteFile(canonical, original, 0o600); err != nil {
		t.Fatalf("write malformed canonical registry: %v", err)
	}

	cases := map[string]func() error{
		"list": func() error { return executeSubstrateList() },
		"get":  func() error { return executeSubstrateGet("garden") },
		"add": func() error {
			return executeSubstrateAdd(substrateconfig.AddRequest{
				Name: "new", Repo: "acme/new", Posture: "app", AppID: "1", KeyPath: "/key.pem", Checkout: "managed",
			})
		},
		"update": func() error {
			return executeSubstrateUpdate(substrateconfig.UpdateRequest{Name: "garden", Repo: "acme/updated", RepoSet: true})
		},
		"verify": func() error { return executeSubstrateVerify(context.Background(), "garden") },
	}
	for operation, run := range cases {
		t.Run(operation, func(t *testing.T) {
			err := run()
			if err == nil || !strings.Contains(err.Error(), "Botanist-only") {
				t.Fatalf("error = %v, want Botanist-only refusal", err)
			}
			unchanged, readErr := os.ReadFile(canonical)
			if readErr != nil {
				t.Fatalf("read canonical registry after refusal: %v", readErr)
			}
			if string(unchanged) != string(original) {
				t.Fatalf("Pollen gate changed malformed canonical registry: %q", unchanged)
			}
		})
	}
}

func TestSubstrateWhitespacePollenIsStillDeclared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envPollenCLI, "   ")
	if err := executeSubstrateList(); err == nil || !strings.Contains(err.Error(), "Botanist-only") {
		t.Fatalf("error = %v, want Botanist-only refusal for whitespace Pollen", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".tendril", "substrates.yaml")); !os.IsNotExist(err) {
		t.Fatalf("whitespace Pollen created canonical registry: %v", err)
	}
}

func TestSubstrateListIsCanonicalSortedAndSecretFree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := "PAT_VALUE_MUST_NOT_APPEAR"
	t.Setenv("SUBSTRATE_SECRET_ENV", secret)
	keyContents := "PRIVATE_KEY_CONTENTS_MUST_NOT_APPEAR"
	keyPath := filepath.Join(home, "private.pem")
	if err := os.WriteFile(keyPath, []byte(keyContents), 0o600); err != nil {
		t.Fatalf("write key fixture: %v", err)
	}
	writeCanonicalSubstrateTestConfig(t, `credentials:
  alpha-connection:
    auth:
      method: pat
      env: SUBSTRATE_SECRET_ENV
      key: /safe/key/reference
      privateKeyPath: `+keyPath+`
      privateKeyEnv: PEM_ENV_NAME
    sign:
      method: gpg
      key: botanist-signing-key
    identity:
      name: Botanist
      email: botanist@example.com
    commit: local
substrates:
  zeta:
    url: https://url-user:URL_SECRET@github.com/acme/zeta?token=URL_QUERY_SECRET#URL_FRAGMENT_SECRET
    profile: alpha-connection
    checkout:
      mode: managed
  alpha:
    url: https://github.com/acme/alpha
    profile: alpha-connection
    checkout:
      mode: path
      path: /work/alpha
`)

	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "substrates.yaml"), []byte("substrates:\n  decoy:\n    url: https://decoy/root\n"), 0o644); err != nil {
		t.Fatalf("write root decoy: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(decoy, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir decoy tendril: %v", err)
	}
	if err := os.WriteFile(filepath.Join(decoy, ".tendril", "substrates.yaml"), []byte("substrates:\n  decoy:\n    url: https://decoy/tendril\n"), 0o644); err != nil {
		t.Fatalf("write tendril decoy: %v", err)
	}
	t.Chdir(decoy)

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateList(); err != nil {
			t.Fatalf("executeSubstrateList: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("list stderr = %q", stderr)
	}
	canonical := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, "registry: "+canonical) {
		t.Fatalf("list output = %q, want canonical source %q", stdout, canonical)
	}
	if strings.Index(stdout, `Substrate "alpha"`) > strings.Index(stdout, `Substrate "zeta"`) {
		t.Fatalf("list output is not alphabetical: %q", stdout)
	}
	for _, forbidden := range []string{secret, keyContents, "URL_SECRET", "URL_QUERY_SECRET", "URL_FRAGMENT_SECRET", "decoy/root", "decoy/tendril"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("list output contains forbidden %q: %q", forbidden, stdout)
		}
	}
	if !strings.Contains(stdout, "https://github.com/acme/zeta") {
		t.Fatalf("list did not render the redacted repository URL: %q", stdout)
	}
}

func TestSubstrateGetRendersStoredProfileWithoutResolvingSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := "GET_PAT_VALUE_MUST_NOT_APPEAR"
	t.Setenv("GET_SECRET_ENV", secret)
	keyContents := "GET_PRIVATE_KEY_CONTENTS_MUST_NOT_APPEAR"
	keyPath := filepath.Join(home, "app.pem")
	if err := os.WriteFile(keyPath, []byte(keyContents), 0o600); err != nil {
		t.Fatalf("write key fixture: %v", err)
	}
	writeCanonicalSubstrateTestConfig(t, `credentials:
  garden-connection:
    auth:
      method: pat
      env: GET_SECRET_ENV
      privateKeyPath: `+keyPath+`
    sign:
      method: gpg
      key: gpg-key-id
    identity:
      name: Garden Botanist
      email: botanist@example.com
    commit: local
substrates:
  garden:
    url: https://github.com/acme/garden
    path: /stored/path
    branch: trunk
    profile: garden-connection
    checkout:
      mode: path
      path: /checkout/garden
    commit: local
    protectDefaultBranch: true
    readonly: true
    provider: host
    command: [tendril, serve]
    patience:
      growth: 20m
      reap: 1h
      scratch: 30s
`)
	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "substrates.yaml"), []byte("substrates:\n  decoy:\n    url: https://decoy/root\n"), 0o644); err != nil {
		t.Fatalf("write root decoy: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(decoy, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir tendril decoy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(decoy, ".tendril", "substrates.yaml"), []byte("substrates:\n  decoy:\n    url: https://decoy/tendril\n"), 0o644); err != nil {
		t.Fatalf("write tendril decoy: %v", err)
	}
	t.Chdir(decoy)

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateGet("garden"); err != nil {
			t.Fatalf("executeSubstrateGet: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("get stderr = %q", stderr)
	}
	if !strings.Contains(stdout, filepath.Join(home, ".tendril", "substrates.yaml")) || !strings.Contains(stdout, "GET_SECRET_ENV") || !strings.Contains(stdout, "gpg-key-id") {
		t.Fatalf("get output misses safe stored fields: %q", stdout)
	}
	for _, forbidden := range []string{secret, keyContents} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("get output contains forbidden %q: %q", forbidden, stdout)
		}
	}
}

func TestSubstrateGetMissingNameReportsActiveSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCanonicalSubstrateTestConfig(t, "substrates:\n  garden:\n    url: https://github.com/acme/garden\n")

	stdout, _ := captureVerifyOutput(t, func() {
		if err := executeSubstrateGet("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want missing Substrate error", err)
		}
	})
	if !strings.Contains(stdout, filepath.Join(home, ".tendril", "substrates.yaml")) {
		t.Fatalf("missing get output = %q, want active source", stdout)
	}
}

func TestSubstrateListWithoutRegistryReportsCanonicalTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "substrates.yaml"), []byte("substrates:\n  decoy:\n    url: https://decoy\n"), 0o644); err != nil {
		t.Fatalf("write cwd decoy: %v", err)
	}
	t.Chdir(cwd)

	stdout, _ := captureVerifyOutput(t, func() {
		if err := executeSubstrateList(); err != nil {
			t.Fatalf("executeSubstrateList: %v", err)
		}
	})
	canonical := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, "registry: "+canonical) || !strings.Contains(stdout, "No Substrates configured.") {
		t.Fatalf("empty list output = %q", stdout)
	}
	if strings.Contains(stdout, cwd) {
		t.Fatalf("empty list reported cwd-local state: %q", stdout)
	}
}

func TestSubstrateAddAppAndPATPosturesPersistOnlyReferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	grantsPath := filepath.Join(home, ".tendril", "grants.yaml")
	if err := os.MkdirAll(filepath.Dir(grantsPath), 0o755); err != nil {
		t.Fatalf("mkdir grants directory: %v", err)
	}
	originalGrants := []byte("grants:\n  existing: {}\n")
	if err := os.WriteFile(grantsPath, originalGrants, 0o600); err != nil {
		t.Fatalf("write grants fixture: %v", err)
	}

	if err := executeSubstrateAdd(substrateconfig.AddRequest{
		Name: "app-repo", Repo: "acme/app-repo", Posture: "app", AppID: "123", KeyPath: "/keys/app.pem",
		Checkout: "path", CheckoutPath: "/work/app-repo", PathSet: true, Branch: "trunk", BranchSet: true,
	}); err != nil {
		t.Fatalf("add app Substrate: %v", err)
	}
	if err := executeSubstrateAdd(substrateconfig.AddRequest{
		Name: "pat-repo", Repo: "acme/pat-repo", Posture: "pat", SignKey: "GPG-KEY",
		IdentityName: "Botanist", IdentityEmail: "botanist@example.com", Checkout: "managed",
	}); err != nil {
		t.Fatalf("add PAT Substrate: %v", err)
	}

	result, err := substrateconfig.LoadCanonical()
	if err != nil {
		t.Fatalf("load added registry: %v", err)
	}
	app := result.Config.Substrates["app-repo"]
	if app.URL != "https://github.com/acme/app-repo" || app.Branch != "trunk" || app.Checkout.Mode != "path" || app.Checkout.Path != "/work/app-repo" {
		t.Fatalf("app Substrate = %+v", app)
	}
	appProfile := result.Config.Credentials["app-repo-connection"]
	if appProfile.Auth.Method != "app" || appProfile.Auth.AppID != "123" || appProfile.Auth.PrivateKeyPath != "/keys/app.pem" || appProfile.Commit != "api" {
		t.Fatalf("app profile = %+v", appProfile)
	}
	pat := result.Config.Substrates["pat-repo"]
	if pat.Checkout.Mode != "managed" {
		t.Fatalf("PAT checkout = %+v, want managed", pat.Checkout)
	}
	patProfile := result.Config.Credentials["pat-repo-connection"]
	if patProfile.Auth.Method != "pat" || patProfile.Auth.Env != "GITHUB_TOKEN" || patProfile.Sign.Key != "GPG-KEY" || patProfile.Identity.Email != "botanist@example.com" || patProfile.Commit != "" {
		t.Fatalf("PAT profile = %+v", patProfile)
	}
	grants, err := os.ReadFile(grantsPath)
	if err != nil {
		t.Fatalf("read grants after add: %v", err)
	}
	if string(grants) != string(originalGrants) {
		t.Fatalf("add changed grants registry: %q", grants)
	}
}

func TestSubstrateVerifyUsesExistingReadinessSeamAndCanonicalSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VERIFY_TOKEN_ENV", "VERIFY_PAT_VALUE_MUST_NOT_APPEAR")
	writeCanonicalSubstrateTestConfig(t, `credentials:
  garden-connection:
    auth: { method: pat, env: VERIFY_TOKEN_ENV }
substrates:
  garden:
    url: https://github.com/acme/garden
    profile: garden-connection
    checkout: { mode: ephemeral }
`)

	oldVerify := verifySubstrateSetup
	called := false
	verifySubstrateSetup = func(ctx context.Context, spec conductor.SubstrateSpec, cred conductor.ResolvedCredential) (conductor.SubstrateSetupVerification, error) {
		called = true
		if spec.URL != "https://github.com/acme/garden" || cred.TokenEnv != "VERIFY_TOKEN_ENV" {
			t.Fatalf("readiness seam inputs = spec=%+v cred=%+v", spec, cred)
		}
		return conductor.SubstrateSetupVerification{}, nil
	}
	t.Cleanup(func() { verifySubstrateSetup = oldVerify })

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateVerify(context.Background(), "garden"); err != nil {
			t.Fatalf("executeSubstrateVerify: %v", err)
		}
	})
	if !called || !strings.Contains(stdout, filepath.Join(home, ".tendril", "substrates.yaml")) {
		t.Fatalf("verify output/call = called %v, stdout %q", called, stdout)
	}
	if strings.Contains(stdout, "VERIFY_PAT_VALUE_MUST_NOT_APPEAR") || strings.Contains(stderr, "VERIFY_PAT_VALUE_MUST_NOT_APPEAR") {
		t.Fatalf("verify output leaked PAT value: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestSubstrateVerifyCredentialFailureReportsSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCanonicalSubstrateTestConfig(t, `credentials:
  garden-connection:
    auth: { method: unknown }
substrates:
  garden:
    url: https://github.com/acme/garden
    profile: garden-connection
`)

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateVerify(context.Background(), "garden"); err == nil {
			t.Fatal("unknown credential method was accepted")
		}
	})
	if !strings.Contains(stdout, filepath.Join(home, ".tendril", "substrates.yaml")) {
		t.Fatalf("credential failure output = %q, want active source", stdout)
	}
	if !strings.Contains(stderr, "resolve credential") || !strings.Contains(stderr, "unknown auth method") {
		t.Fatalf("credential failure stderr = %q", stderr)
	}
}

func TestSubstrateVerifyMissingNameReportsSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCanonicalSubstrateTestConfig(t, "substrates:\n  garden:\n    url: https://github.com/acme/garden\n")

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateVerify(context.Background(), "missing"); err == nil {
			t.Fatal("missing Substrate verification was accepted")
		}
	})
	if !strings.Contains(stdout, filepath.Join(home, ".tendril", "substrates.yaml")) || !strings.Contains(stderr, "not found") {
		t.Fatalf("missing verify output = stdout %q stderr %q", stdout, stderr)
	}
}

func TestSubstrateStoredConfigRejectsMalformedSections(t *testing.T) {
	for _, content := range []string{"substrates: null\n", "credentials: []\n", "null\n"} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(content), " ", "-"), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeCanonicalSubstrateTestConfig(t, content)
			if err := executeSubstrateList(); err == nil {
				t.Fatalf("malformed stored config %q was accepted", content)
			}
		})
	}
}

func writeCanonicalSubstrateTestConfig(t *testing.T, content string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve test home: %v", err)
	}
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write canonical registry: %v", err)
	}
	return path
}
