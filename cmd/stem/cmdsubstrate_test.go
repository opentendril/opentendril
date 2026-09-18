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

func TestSubstrateUsageListsRemove(t *testing.T) {
	stdout, _ := captureVerifyOutput(t, printSubstrateUsage)
	if !strings.Contains(stdout, "<list|get|add|update|remove|verify>") || !strings.Contains(stdout, "tendril substrate remove <name>") {
		t.Fatalf("substrate usage = %q, want remove command", stdout)
	}
}

func TestSubstrateRemoveNameArguments(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", want: "requires <name>"},
		{name: "too many", args: []string{"one", "two"}, want: "requires <name>"},
		{name: "flag", args: []string{"--force"}, want: "requires <name>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseSubstrateNameArgs("remove", test.args); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
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
	grantsPath := filepath.Join(home, ".tendril", "grants.yaml")
	malformedGrants := []byte("grants: [")
	if err := os.WriteFile(grantsPath, malformedGrants, 0o600); err != nil {
		t.Fatalf("write malformed grants: %v", err)
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
		"remove": func() error { return executeSubstrateRemove("garden") },
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
			grants, readErr := os.ReadFile(grantsPath)
			if readErr != nil {
				t.Fatalf("read grants after refusal: %v", readErr)
			}
			if string(grants) != string(malformedGrants) {
				t.Fatalf("Pollen gate changed malformed grants: %q", grants)
			}
		})
	}
}

func TestSubstrateRemoveBlocksLiveGrantsBeforeRegistryMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := writeCanonicalSubstrateTestConfig(t, `substrates:
  garden:
    url: https://github.com/acme/garden
`)
	grantsPath := filepath.Join(home, ".tendril", "grants.yaml")
	originalGrants := []byte(`grants:
  zeta:
    operationClasses: [git.status]
    substrates: [garden]
  alpha:
    operationClasses: [git.status]
    substrates: [garden]
  expired:
    operationClasses: [git.status]
    substrates: [garden]
    expires: 2000-01-01
`)
	if err := os.WriteFile(grantsPath, originalGrants, 0o600); err != nil {
		t.Fatalf("write grants: %v", err)
	}
	beforeRegistry, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry before blocked removal: %v", err)
	}

	err = executeSubstrateRemove("garden")
	if err == nil || !strings.Contains(err.Error(), `Substrate "garden"`) || !strings.Contains(err.Error(), "alpha, zeta") || !strings.Contains(err.Error(), "narrow or remove") {
		t.Fatalf("error = %v, want deterministic actionable blocker", err)
	}
	afterRegistry, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry after blocked removal: %v", err)
	}
	if string(afterRegistry) != string(beforeRegistry) {
		t.Fatalf("blocked removal changed registry:\n%s", afterRegistry)
	}
	afterGrants, err := os.ReadFile(grantsPath)
	if err != nil {
		t.Fatalf("read grants after blocked removal: %v", err)
	}
	if string(afterGrants) != string(originalGrants) {
		t.Fatalf("blocked removal changed grants:\n%s", afterGrants)
	}
}

func TestSubstrateRemovePermitsNoGrantAndPreservesGrantBytes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := writeCanonicalSubstrateTestConfig(t, `credentials:
  garden-profile:
    auth: { method: pat, env: GARDEN_TOKEN }
substrates:
  garden:
    url: https://github.com/acme/garden
    profile: garden-profile
`)
	grantsPath := filepath.Join(home, ".tendril", "grants.yaml")
	originalGrants := []byte("# no grants\ngrants: {}\n")
	if err := os.WriteFile(grantsPath, originalGrants, 0o600); err != nil {
		t.Fatalf("write grants: %v", err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateRemove("garden"); err != nil {
			t.Fatalf("executeSubstrateRemove: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("remove stderr = %q", stderr)
	}
	canonical := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, `Removed Substrate "garden" from `+canonical) || !strings.Contains(stdout, `Removed unreferenced credential profile "garden-profile"`) {
		t.Fatalf("remove output = %q, want success and profile lifecycle", stdout)
	}
	if _, err := os.Stat(registry); err != nil {
		t.Fatalf("registry was not retained after successful removal: %v", err)
	}
	afterGrants, err := os.ReadFile(grantsPath)
	if err != nil {
		t.Fatalf("read grants after successful removal: %v", err)
	}
	if string(afterGrants) != string(originalGrants) {
		t.Fatalf("successful removal changed grants:\n%s", afterGrants)
	}
}

func TestSubstrateRemoveRejectsMalformedGrantsBeforeRegistryWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := writeCanonicalSubstrateTestConfig(t, `substrates:
  garden:
    url: https://github.com/acme/garden
`)
	originalRegistry, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry before malformed grants test: %v", err)
	}
	grantsPath := filepath.Join(home, ".tendril", "grants.yaml")
	malformed := []byte("grants: [")
	if err := os.WriteFile(grantsPath, malformed, 0o600); err != nil {
		t.Fatalf("write malformed grants: %v", err)
	}

	err = executeSubstrateRemove("garden")
	if err == nil || !strings.Contains(err.Error(), "delegation grants") {
		t.Fatalf("error = %v, want malformed grants failure", err)
	}
	unchanged, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry after malformed grants: %v", err)
	}
	if string(unchanged) != string(originalRegistry) {
		t.Fatalf("malformed grants changed registry:\n%s", unchanged)
	}
}

func TestSubstrateRemoveIgnoresCwdLocalGrants(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := writeCanonicalSubstrateTestConfig(t, `substrates:
  garden:
    url: https://github.com/acme/garden
`)
	decoy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(decoy, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir cwd grants decoy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(decoy, ".tendril", "grants.yaml"), []byte(`grants:
  decoy-pollen:
    operationClasses: [git.status]
    substrates: [garden]
`), 0o600); err != nil {
		t.Fatalf("write cwd grants decoy: %v", err)
	}
	decoyRegistryPath := filepath.Join(decoy, ".tendril", "substrates.yaml")
	decoyRegistry := []byte(`substrates:
  garden:
    url: https://github.com/decoy/garden
`)
	if err := os.WriteFile(decoyRegistryPath, decoyRegistry, 0o600); err != nil {
		t.Fatalf("write cwd registry decoy: %v", err)
	}
	t.Chdir(decoy)

	if err := executeSubstrateRemove("garden"); err != nil {
		t.Fatalf("cwd-local grants blocked removal: %v", err)
	}
	if _, err := os.Stat(registry); err != nil {
		t.Fatalf("canonical registry was not retained: %v", err)
	}
	unchangedDecoy, err := os.ReadFile(decoyRegistryPath)
	if err != nil {
		t.Fatalf("read cwd registry decoy: %v", err)
	}
	if string(unchangedDecoy) != string(decoyRegistry) {
		t.Fatalf("cwd registry decoy changed during removal:\n%s", unchangedDecoy)
	}
}

func TestSubstrateRemoveReportsLegacyLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyPath := filepath.Join(home, "substrates.yaml")
	legacy := []byte(`substrates:
  garden:
    url: https://github.com/acme/garden
  keep:
    url: https://github.com/acme/keep
`)
	if err := os.WriteFile(legacyPath, legacy, 0o640); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateRemove("garden"); err != nil {
			t.Fatalf("executeSubstrateRemove: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("remove stderr = %q", stderr)
	}
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, "Imported legacy registry from "+legacyPath+" into "+canonicalPath) || !strings.Contains(stdout, "canonical registry is now active") {
		t.Fatalf("remove output = %q, want legacy import observability", stdout)
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy registry after removal: %v", err)
	}
	if string(unchanged) != string(legacy) {
		t.Fatalf("legacy registry changed during removal:\n%s", unchanged)
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

func TestSubstrateAddImportsLegacyAndReportsLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := []byte(`credentials:
  existing-connection:
    auth: { method: pat, env: LEGACY_TOKEN }
substrates:
  existing:
    url: https://github.com/acme/existing
    profile: existing-connection
    checkout: { mode: managed }
`)
	legacyPath := filepath.Join(home, "substrates.yaml")
	if err := os.WriteFile(legacyPath, legacy, 0o640); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateAdd(substrateconfig.AddRequest{
			Name: "new", Repo: "acme/new", Posture: "app", AppID: "123", KeyPath: "/key.pem", Checkout: "managed",
		}); err != nil {
			t.Fatalf("executeSubstrateAdd: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("add stderr = %q", stderr)
	}
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, `Added Substrate "new" to `+canonicalPath) ||
		!strings.Contains(stdout, "Imported legacy registry from "+legacyPath+" into "+canonicalPath) ||
		!strings.Contains(stdout, "canonical registry is now active") {
		t.Fatalf("add output = %q, want success and import lifecycle messages", stdout)
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy registry after add: %v", err)
	}
	if string(unchanged) != string(legacy) {
		t.Fatalf("legacy registry changed during add:\n%s", unchanged)
	}
	result, err := substrateconfig.LoadCanonical()
	if err != nil {
		t.Fatalf("load imported registry after add: %v", err)
	}
	if _, ok := result.Config.Substrates["existing"]; !ok {
		t.Fatal("add did not import existing legacy Substrate")
	}
	if _, ok := result.Config.Substrates["new"]; !ok {
		t.Fatal("add did not persist new Substrate")
	}
}

func TestSubstrateUpdateImportsLegacyAppliesPatchAndReportsLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := []byte(`credentials:
  garden-connection:
    auth: { method: pat, env: LEGACY_TOKEN }
substrates:
  garden:
    url: https://github.com/acme/garden
    branch: main
    profile: garden-connection
    checkout:
      mode: path
      path: /legacy/path
`)
	legacyPath := filepath.Join(home, "substrates.yaml")
	if err := os.WriteFile(legacyPath, legacy, 0o640); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateUpdate(substrateconfig.UpdateRequest{Name: "garden", Branch: "feature", BranchSet: true}); err != nil {
			t.Fatalf("executeSubstrateUpdate: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("update stderr = %q", stderr)
	}
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, `Updated Substrate "garden" in `+canonicalPath) ||
		!strings.Contains(stdout, "Imported legacy registry from "+legacyPath+" into "+canonicalPath) ||
		!strings.Contains(stdout, "canonical registry is now active") {
		t.Fatalf("update output = %q, want success and import lifecycle messages", stdout)
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy registry after update: %v", err)
	}
	if string(unchanged) != string(legacy) {
		t.Fatalf("legacy registry changed during update:\n%s", unchanged)
	}
	result, err := substrateconfig.LoadCanonical()
	if err != nil {
		t.Fatalf("load imported registry after update: %v", err)
	}
	garden := result.Config.Substrates["garden"]
	if garden.URL != "https://github.com/acme/garden" || garden.Branch != "feature" || garden.Profile != "garden-connection" || garden.Checkout.Mode != "path" || garden.Checkout.Path != "/legacy/path" {
		t.Fatalf("updated garden = %+v, want only branch patched", garden)
	}
}

func TestSubstrateListReportsIgnoredLegacyWithoutDisplayingIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCanonicalSubstrateTestConfig(t, `substrates:
  canonical:
    url: https://github.com/acme/canonical
`)
	legacyPath := filepath.Join(home, "substrates.yaml")
	if err := os.WriteFile(legacyPath, []byte(`substrates:
  legacy-only:
    url: https://github.com/acme/legacy-only
`), 0o640); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateList(); err != nil {
			t.Fatalf("executeSubstrateList: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("list stderr = %q", stderr)
	}
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, "registry: "+canonicalPath) ||
		!strings.Contains(stdout, "legacy registry ignored: "+legacyPath+" (canonical registry is active)") ||
		!strings.Contains(stdout, `Substrate "canonical"`) {
		t.Fatalf("list output = %q, want canonical and ignored-legacy observability", stdout)
	}
	for _, forbidden := range []string{"legacy-only", "https://github.com/acme/legacy-only"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("list output displayed legacy content %q: %q", forbidden, stdout)
		}
	}
}

func TestSubstrateGetReportsIgnoredLegacyWithoutDisplayingIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeCanonicalSubstrateTestConfig(t, `substrates:
  garden:
    url: https://github.com/acme/garden
`)
	legacyPath := filepath.Join(home, "substrates.yaml")
	if err := os.WriteFile(legacyPath, []byte(`substrates:
  legacy-only:
    url: https://github.com/acme/legacy-only
`), 0o640); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		if err := executeSubstrateGet("garden"); err != nil {
			t.Fatalf("executeSubstrateGet: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("get stderr = %q", stderr)
	}
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if !strings.Contains(stdout, "registry: "+canonicalPath) ||
		!strings.Contains(stdout, "legacy registry ignored: "+legacyPath+" (canonical registry is active)") ||
		!strings.Contains(stdout, `Substrate "garden"`) {
		t.Fatalf("get output = %q, want canonical and ignored-legacy observability", stdout)
	}
	for _, forbidden := range []string{"legacy-only", "https://github.com/acme/legacy-only"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("get output displayed legacy content %q: %q", forbidden, stdout)
		}
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
