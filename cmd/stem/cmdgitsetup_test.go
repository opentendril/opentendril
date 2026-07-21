package main

import (
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
// YAML for the named subject and substrate.
func TestRenderGrantsYAMLParses(t *testing.T) {
	opts := gitSetupOptions{substrate: "r", grantSubject: "claude"}
	out := renderGrantsYAML(opts)
	for _, want := range []string{"grants:", "claude:", "operationClasses: [git.branch, git.commit, git.push, git.pr]", "substrates: [r]"} {
		if !strings.Contains(out, want) {
			t.Errorf("generated grants missing %q:\n%s", want, out)
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

// TestUpsertGrantUnionsSubstrates proves granting an existing agent access to a
// second repo adds the substrate to its list rather than replacing it, and a
// distinct subject is kept separate.
func TestUpsertGrantUnionsSubstrates(t *testing.T) {
	dir := t.TempDir()
	tendrilDir := filepath.Join(dir, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tendrilDir, "grants.yaml")

	for _, o := range []gitSetupOptions{
		{substrate: "repo1", grantSubject: "claude"},
		{substrate: "repo2", grantSubject: "claude"},
		{substrate: "repo1", grantSubject: "codex"},
	} {
		if err := upsertGrants(path, o); err != nil {
			t.Fatalf("upsert grant %+v: %v", o, err)
		}
	}

	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		t.Fatalf("load grants: %v", err)
	}
	bySubject := map[string][]string{}
	classesBySubject := map[string][]string{}
	for _, g := range grants {
		bySubject[g.Subject] = g.Substrates
		classesBySubject[g.Subject] = g.OperationClasses
	}

	// Every setup run grants the full governed loop — commit, push, and the
	// pull request that finishes it — so an authorised agent never has to
	// leave Tendril for the last mile. Unioning must not duplicate them.
	for subject, classes := range classesBySubject {
		if len(classes) != 4 {
			t.Errorf("%s operation-classes = %v, want exactly the four git classes unioned once", subject, classes)
		}
		for _, want := range []string{core.CapGitBranch, core.CapGitCommit, core.CapGitPush, core.CapGitPR} {
			if !contains(classes, want) {
				t.Errorf("%s operation-classes = %v, want %s included", subject, classes, want)
			}
		}
	}
	if got := bySubject["claude"]; len(got) != 2 || !contains(got, "repo1") || !contains(got, "repo2") {
		t.Errorf("claude substrates = %v, want [repo1 repo2] unioned", got)
	}
	if got := bySubject["codex"]; len(got) != 1 || got[0] != "repo1" {
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
