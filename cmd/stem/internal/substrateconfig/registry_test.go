package substrateconfig

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"gopkg.in/yaml.v3"
)

func TestMutateCanonicalCreatesFreshRegistryWithExpectedMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	result, err := MutateCanonical(func(root *yaml.Node) error {
		return addSubstrate(root, "garden", "https://example.com/garden.git")
	})
	if err != nil {
		t.Fatalf("MutateCanonical: %v", err)
	}

	wantPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if result.Destination != wantPath {
		t.Fatalf("destination = %q, want %q", result.Destination, wantPath)
	}
	if !result.Created || result.ImportedLegacy {
		t.Fatalf("mutation result = %+v, want fresh canonical creation", result)
	}
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat fresh canonical registry: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("fresh mode = %o, want 644", got)
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read fresh canonical registry: %v", err)
	}
	if !strings.Contains(string(raw), "garden") {
		t.Fatalf("fresh registry = %q, want garden entry", raw)
	}
}

func TestMutateCanonicalImportsValidLegacyAndPreservesLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyPath := filepath.Join(home, "substrates.yaml")
	legacy := []byte(`# keep this comment
credentials:
  shared:
    auth: GITHUB_TOKEN

substrates:
  # keep this unrelated entry
  existing:
    url: https://example.com/existing.git
`)
	if err := os.WriteFile(legacyPath, legacy, 0o640); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	result, err := MutateCanonical(func(root *yaml.Node) error {
		return addSubstrate(root, "new", "https://example.com/new.git")
	})
	if err != nil {
		t.Fatalf("MutateCanonical legacy import: %v", err)
	}
	if !result.ImportedLegacy || result.Source != legacyPath {
		t.Fatalf("mutation result = %+v, want legacy import from %s", result, legacyPath)
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy registry after import: %v", err)
	}
	if string(unchanged) != string(legacy) {
		t.Fatalf("legacy registry changed during import:\n%s", unchanged)
	}

	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read imported canonical registry: %v", err)
	}
	for _, want := range []string{"keep this comment", "keep this unrelated entry", "existing", "new"} {
		if !strings.Contains(string(canonical), want) {
			t.Fatalf("canonical registry missing %q:\n%s", want, canonical)
		}
	}
	var config conductor.SubstratesConfig
	if err := yaml.Unmarshal(canonical, &config); err != nil {
		t.Fatalf("decode imported canonical registry: %v", err)
	}
	if config.Credentials["shared"].Auth.Method != "pat" || config.Credentials["shared"].Auth.Env != "GITHUB_TOKEN" {
		t.Fatalf("shared credential = %+v, want imported PAT env reference", config.Credentials["shared"])
	}
	if config.Substrates["existing"].URL != "https://example.com/existing.git" || config.Substrates["new"].URL != "https://example.com/new.git" {
		t.Fatalf("imported substrates = %+v, want existing and new URLs", config.Substrates)
	}
}

func TestMutateCanonicalRejectsMalformedLegacyBeforeCreatingCanonical(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyPath := filepath.Join(home, "substrates.yaml")
	if err := os.WriteFile(legacyPath, []byte("substrates: ["), 0o644); err != nil {
		t.Fatalf("write malformed legacy registry: %v", err)
	}

	_, err := MutateCanonical(func(root *yaml.Node) error {
		return addSubstrate(root, "never-written", "https://example.com/never-written.git")
	})
	if err == nil {
		t.Fatal("malformed legacy registry was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".tendril", "substrates.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("malformed legacy import created canonical registry: %v", statErr)
	}
}

func TestMutateCanonicalValidationFailureLeavesNoPartialState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := MutateCanonical(func(root *yaml.Node) error {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "substrates"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "not-a-mapping"},
		)
		return nil
	})
	if err == nil {
		t.Fatal("invalid post-mutation registry was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".tendril", "substrates.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("validation failure left canonical state behind: %v", statErr)
	}
}

func TestMutateCanonicalPreservesExistingModeAndCanonicalWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	legacyPath := filepath.Join(home, "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		t.Fatalf("mkdir canonical directory: %v", err)
	}
	canonical := []byte("# canonical\nsubstrates:\n  canonical:\n    url: https://example.com/canonical.git\n")
	legacy := []byte("substrates:\n  legacy:\n    url: https://example.com/legacy.git\n")
	if err := os.WriteFile(canonicalPath, canonical, 0o600); err != nil {
		t.Fatalf("write canonical registry: %v", err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0o644); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	result, err := MutateCanonical(func(root *yaml.Node) error { return nil })
	if err != nil {
		t.Fatalf("MutateCanonical canonical selection: %v", err)
	}
	if !result.LegacyIgnored || result.LegacyPath != legacyPath || result.ImportedLegacy {
		t.Fatalf("mutation result = %+v, want canonical win with ignored legacy", result)
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		t.Fatalf("stat canonical registry: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("existing mode = %o, want 600", got)
	}
	updated, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical registry: %v", err)
	}
	if !strings.Contains(string(updated), "canonical") || strings.Contains(string(updated), "legacy") {
		t.Fatalf("canonical registry was not selected:\n%s", updated)
	}
}

func TestMutateCanonicalMalformedCanonicalWinsOverValidLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	legacyPath := filepath.Join(home, "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		t.Fatalf("mkdir canonical directory: %v", err)
	}
	if err := os.WriteFile(canonicalPath, []byte("substrates: ["), 0o600); err != nil {
		t.Fatalf("write malformed canonical registry: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("substrates:\n  legacy:\n    url: https://example.com/legacy.git\n"), 0o644); err != nil {
		t.Fatalf("write valid legacy registry: %v", err)
	}

	_, err := MutateCanonical(func(root *yaml.Node) error { return nil })
	if err == nil {
		t.Fatal("malformed canonical registry fell back to legacy")
	}
	unchanged, readErr := os.ReadFile(canonicalPath)
	if readErr != nil {
		t.Fatalf("read malformed canonical registry after refusal: %v", readErr)
	}
	if string(unchanged) != "substrates: [" {
		t.Fatalf("malformed canonical registry changed during refusal: %q", unchanged)
	}
}

func TestMutateCanonicalValidationFailureLeavesExistingFileUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		t.Fatalf("mkdir canonical directory: %v", err)
	}
	original := []byte("# keep this state\nsubstrates:\n  existing:\n    url: https://example.com/existing.git\n")
	if err := os.WriteFile(canonicalPath, original, 0o600); err != nil {
		t.Fatalf("write canonical registry: %v", err)
	}

	_, err := MutateCanonical(func(root *yaml.Node) error {
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "substrates" {
				root.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "not-a-mapping"}
				return nil
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("invalid post-mutation registry was accepted")
	}
	unchanged, readErr := os.ReadFile(canonicalPath)
	if readErr != nil {
		t.Fatalf("read canonical registry after validation failure: %v", readErr)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("validation failure changed canonical registry:\n%s", unchanged)
	}
}

func addSubstrate(root *yaml.Node, name, url string) error {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "substrates" {
			continue
		}
		substrates := root.Content[i+1]
		if substrates.Kind != yaml.MappingNode {
			return nil
		}
		substrates.Content = append(substrates.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
			&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "url"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: url},
			}},
		)
		return nil
	}

	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "substrates"},
		&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
			{Kind: yaml.MappingNode, Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "url"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: url},
			}},
		}},
	)
	return nil
}

func TestAddCanonicalRejectsDuplicateSubstrateWithoutRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	original := []byte("# keep\nsubstrates:\n  garden:\n    url: https://github.com/acme/garden\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write original registry: %v", err)
	}

	_, err := AddCanonical(AddRequest{
		Name: "garden", Repo: "acme/other", Posture: "app", AppID: "1", KeyPath: "/key.pem", Checkout: "managed",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want duplicate refusal", err)
	}
	unchanged, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read registry after duplicate refusal: %v", readErr)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("duplicate refusal changed registry:\n%s", unchanged)
	}
}

func TestAddCanonicalRejectsMalformedRegistryWithoutRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	original := []byte("substrates: null\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write malformed registry: %v", err)
	}
	_, err := AddCanonical(AddRequest{Name: "garden", Repo: "acme/garden", Posture: "app", AppID: "1", KeyPath: "/key.pem", Checkout: "managed"})
	if err == nil {
		t.Fatal("malformed registry was accepted")
	}
	unchanged, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read malformed registry: %v", readErr)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("malformed registry changed:\n%s", unchanged)
	}
}

func TestAddCanonicalRejectsExplicitEmptyPostureAndCheckout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for name, request := range map[string]AddRequest{
		"posture":  {Name: "posture", Repo: "acme/posture", PostureSet: true, Checkout: "managed", AppID: "1", KeyPath: "/key.pem"},
		"checkout": {Name: "checkout", Repo: "acme/checkout", Posture: "app", CheckoutSet: true, AppID: "1", KeyPath: "/key.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AddCanonical(request); err == nil {
				t.Fatal("explicit empty flag value was accepted")
			}
		})
	}
}

func TestAddCanonicalRejectsDuplicateProfileWithoutRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	original := []byte("credentials:\n  garden-connection:\n    auth: { method: app, appId: \"1\", privateKeyPath: /key.pem }\nsubstrates: {}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write original registry: %v", err)
	}

	_, err := AddCanonical(AddRequest{
		Name: "garden", Repo: "acme/garden", Posture: "app", AppID: "2", KeyPath: "/other.pem", Checkout: "managed",
	})
	if err == nil || !strings.Contains(err.Error(), "credential profile") {
		t.Fatalf("error = %v, want duplicate profile refusal", err)
	}
	unchanged, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read registry after duplicate profile refusal: %v", readErr)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("duplicate profile refusal changed registry:\n%s", unchanged)
	}
}

func TestUpdateCanonicalPreservesUnrelatedStateAndProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	original := []byte(`# keep this comment
credentials:
  target-connection:
    auth: { method: pat, env: TARGET_ENV }
    sign: { method: gpg, key: TARGET_GPG }
    identity: { name: Target, email: target@example.com }
    commit: local
  unrelated-connection:
    auth: { method: app, appId: "7", privateKeyPath: /other.pem }
substrates:
  target:
    url: https://github.com/acme/old
    branch: old
    profile: target-connection
    checkout:
      mode: path
      path: /old/path
    commit: local
    readonly: true
  unrelated:
    url: https://github.com/acme/unrelated
    profile: unrelated-connection
    checkout: { mode: ephemeral }
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write original registry: %v", err)
	}

	before, err := LoadCanonical()
	if err != nil {
		t.Fatalf("load before update: %v", err)
	}
	profileBefore := before.Config.Credentials["target-connection"]
	otherBefore := before.Config.Substrates["unrelated"]
	_, err = UpdateCanonical(UpdateRequest{Name: "target", Repo: "acme/new", RepoSet: true})
	if err != nil {
		t.Fatalf("repo-only update: %v", err)
	}
	after, err := LoadCanonical()
	if err != nil {
		t.Fatalf("load after update: %v", err)
	}
	target := after.Config.Substrates["target"]
	if target.URL != "https://github.com/acme/new" || target.Branch != "old" || target.Checkout.Mode != "path" || target.Checkout.Path != "/old/path" || !target.ReadOnly {
		t.Fatalf("repo-only target = %+v, want unrelated target fields preserved", target)
	}
	if got := after.Config.Credentials["target-connection"]; got != profileBefore {
		t.Fatalf("target credential profile changed: before=%+v after=%+v", profileBefore, got)
	}
	if got := after.Config.Substrates["unrelated"]; !reflect.DeepEqual(got, otherBefore) {
		t.Fatalf("unrelated Substrate changed: before=%+v after=%+v", otherBefore, got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated registry: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "keep this comment") || strings.Index(text, "target-connection") > strings.Index(text, "unrelated-connection") {
		t.Fatalf("comments or credential order did not survive:\n%s", text)
	}
}

func TestUpdateCanonicalCheckoutPatchRules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	content := []byte("substrates:\n  garden:\n    url: https://github.com/acme/garden\n    checkout:\n      mode: path\n      path: /old\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	if _, err := UpdateCanonical(UpdateRequest{Name: "garden", CheckoutSet: true, Checkout: "path"}); err == nil {
		t.Fatal("checkout path without --path was accepted")
	}
	if _, err := UpdateCanonical(UpdateRequest{Name: "garden", CheckoutSet: true, Checkout: "managed", PathSet: true, CheckoutPath: "/new"}); err == nil {
		t.Fatal("--path with managed checkout was accepted")
	}
	if _, err := UpdateCanonical(UpdateRequest{Name: "garden", PathSet: true, CheckoutPath: "/new"}); err != nil {
		t.Fatalf("path-only update: %v", err)
	}
	updated, err := LoadCanonical()
	if err != nil {
		t.Fatalf("load path-only update: %v", err)
	}
	if updated.Config.Substrates["garden"].Checkout.Path != "/new" {
		t.Fatalf("path-only checkout = %+v", updated.Config.Substrates["garden"].Checkout)
	}
	if _, err := UpdateCanonical(UpdateRequest{Name: "garden", CheckoutSet: true, Checkout: "ephemeral"}); err != nil {
		t.Fatalf("change away from path checkout: %v", err)
	}
	updated, err = LoadCanonical()
	if err != nil {
		t.Fatalf("load checkout mode update: %v", err)
	}
	checkout := updated.Config.Substrates["garden"].Checkout
	if checkout.Mode != "ephemeral" || checkout.Path != "" {
		t.Fatalf("checkout path survived mode change: %+v", checkout)
	}
}

func TestUpdateCanonicalMalformedRegistryLeavesBytesUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	original := []byte("substrates: [")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write malformed registry: %v", err)
	}
	if _, err := UpdateCanonical(UpdateRequest{Name: "garden", Repo: "acme/garden", RepoSet: true}); err == nil {
		t.Fatal("malformed registry was accepted")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read malformed registry: %v", err)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("malformed registry changed:\n%s", unchanged)
	}
}

func TestUpdateCanonicalRejectsNoPatchAndMissingTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	if err := os.WriteFile(path, []byte("substrates:\n  garden:\n    url: https://github.com/acme/garden\n"), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if _, err := UpdateCanonical(UpdateRequest{Name: "garden"}); err == nil {
		t.Fatal("update without patch flags was accepted")
	}
	if _, err := UpdateCanonical(UpdateRequest{Name: "missing", Branch: "trunk", BranchSet: true}); err == nil {
		t.Fatal("update of missing Substrate was accepted")
	}
	if _, err := UpdateCanonical(UpdateRequest{Name: "garden", Branch: "trunk", BranchSet: true}); err != nil {
		t.Fatalf("branch-only update: %v", err)
	}
	config, err := LoadCanonical()
	if err != nil {
		t.Fatalf("load branch-only update: %v", err)
	}
	if got := config.Config.Substrates["garden"].Branch; got != "trunk" {
		t.Fatalf("branch = %q, want trunk", got)
	}
}

func TestRemoveCanonicalBlocksLiveReferencesAndManagesCredentialProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical directory: %v", err)
	}
	original := []byte(`# keep this comment
credentials:
  shared:
    auth: { method: pat, env: SHARED_TOKEN }
  final:
    auth: { method: pat, env: FINAL_TOKEN }
  unrelated:
    auth: { method: pat, env: OTHER_TOKEN }
substrates:
  target:
    url: https://example.com/target
    profile: shared
    checkout: { mode: managed }
  shared-other:
    url: https://example.com/shared-other
    profile: shared
  final-target:
    url: https://example.com/final
    profile: final
  unrelated:
    url: https://example.com/unrelated
    profile: unrelated
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	now := time.Now()
	grants := []core.DelegationGrant{
		{Pollen: "zeta", Substrates: []string{"target"}, Expires: now.Add(time.Hour)},
		{Pollen: "alpha", Substrates: []string{"target"}, Expires: now.Add(time.Hour)},
		{Pollen: "alpha", Substrates: []string{"target"}, Expires: now.Add(time.Hour)},
		{Pollen: "expired", Substrates: []string{"target"}, Expires: now.Add(-time.Hour)},
		{Pollen: "unrelated", Substrates: []string{"other"}, Expires: now.Add(time.Hour)},
	}
	if _, err := RemoveCanonical("target", grants); err == nil {
		t.Fatal("live exact grant reference did not block removal")
	} else {
		var blocked *RemovalBlockedError
		if !errors.As(err, &blocked) {
			t.Fatalf("error = %v, want RemovalBlockedError", err)
		}
		if got, want := blocked.Pollens, []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("blocking Pollens = %v, want %v", got, want)
		}
		if !strings.Contains(err.Error(), "narrow or remove") {
			t.Fatalf("blocking error = %v, want separate delegation guidance", err)
		}
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry after blocked removal: %v", err)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("blocked removal changed registry:\n%s", unchanged)
	}

	result, err := RemoveCanonical("target", nil)
	if err != nil {
		t.Fatalf("remove target: %v", err)
	}
	if result.CredentialProfile != "shared" || !result.CredentialProfileRetained || result.CredentialProfileRemoved {
		t.Fatalf("shared profile result = %+v, want retained shared profile", result)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry after target removal: %v", err)
	}
	for _, want := range []string{"keep this comment", "shared-other", "final-target", "unrelated", "shared:"} {
		if !strings.Contains(string(updated), want) {
			t.Fatalf("registry after target removal missing %q:\n%s", want, updated)
		}
	}
	if strings.Contains(string(updated), "  target:\n") {
		t.Fatalf("target remained after removal:\n%s", updated)
	}

	result, err = RemoveCanonical("final-target", nil)
	if err != nil {
		t.Fatalf("remove final profile target: %v", err)
	}
	if result.CredentialProfile != "final" || !result.CredentialProfileRemoved || result.CredentialProfileRetained {
		t.Fatalf("final profile result = %+v, want removed final profile", result)
	}
	updated, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry after final target removal: %v", err)
	}
	if strings.Contains(string(updated), "  final:\n") || !strings.Contains(string(updated), "  unrelated:\n") {
		t.Fatalf("profile lifecycle mutation was too broad:\n%s", updated)
	}
}

func TestRemoveCanonicalUsesDecodedProfilesForAliases(t *testing.T) {
	t.Run("remaining alias retains shared profile", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".tendril", "substrates.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir canonical directory: %v", err)
		}
		original := []byte(`# keep this comment
profile-anchor: &shared-profile shared
credentials:
  shared:
    auth: { method: pat, env: SHARED_TOKEN }
substrates:
  target:
    url: https://example.com/target
    profile: shared
  remaining:
    url: https://example.com/remaining
    profile: *shared-profile
`)
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatalf("write registry: %v", err)
		}

		result, err := RemoveCanonical("target", nil)
		if err != nil {
			t.Fatalf("remove target: %v", err)
		}
		if result.CredentialProfile != "shared" || !result.CredentialProfileRetained || result.CredentialProfileRemoved {
			t.Fatalf("shared profile result = %+v, want retained shared profile", result)
		}
		updated, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read registry after removal: %v", err)
		}
		text := string(updated)
		for _, want := range []string{"keep this comment", "profile-anchor: &shared-profile shared", "remaining:", "  shared:"} {
			if !strings.Contains(text, want) {
				t.Fatalf("registry after removal missing %q:\n%s", want, text)
			}
		}
	})

	t.Run("removed alias removes final profile and preserves anchor", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".tendril", "substrates.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir canonical directory: %v", err)
		}
		original := []byte(`# keep this comment
profile-anchor: &removed-profile removed
credentials:
  removed:
    auth: { method: pat, env: REMOVED_TOKEN }
  unrelated:
    auth: { method: pat, env: OTHER_TOKEN }
substrates:
  target:
    url: https://example.com/target
    profile: *removed-profile
  unrelated:
    url: https://example.com/unrelated
    profile: unrelated
`)
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatalf("write registry: %v", err)
		}

		result, err := RemoveCanonical("target", nil)
		if err != nil {
			t.Fatalf("remove target: %v", err)
		}
		if result.CredentialProfile != "removed" || !result.CredentialProfileRemoved || result.CredentialProfileRetained {
			t.Fatalf("removed profile result = %+v, want removed final profile", result)
		}
		updated, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read registry after removal: %v", err)
		}
		text := string(updated)
		for _, want := range []string{"keep this comment", "profile-anchor: &removed-profile removed", "unrelated:"} {
			if !strings.Contains(text, want) {
				t.Fatalf("registry after removal missing %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, "  removed:\n") || strings.Contains(text, "  target:\n") {
			t.Fatalf("removed target or credential profile remained:\n%s", text)
		}
	})
}

func TestRemoveCanonicalExpiredGrantAllowsExactRemoval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("substrates:\n  target:\n    url: https://example.com/target\n  target-copy:\n    url: https://example.com/target-copy\n"), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	grants := []core.DelegationGrant{
		{Pollen: "expired", Substrates: []string{"target"}, Expires: time.Now().Add(-time.Minute)},
		{Pollen: "substring", Substrates: []string{"target-copy"}, Expires: time.Now().Add(time.Hour)},
	}
	if _, err := RemoveCanonical("target", grants); err != nil {
		t.Fatalf("expired or substring grant blocked removal: %v", err)
	}
	result, err := LoadCanonical()
	if err != nil {
		t.Fatalf("load registry after expired-grant removal: %v", err)
	}
	if _, ok := result.Config.Substrates["target"]; ok {
		t.Fatal("target remained after removal")
	}
	if _, ok := result.Config.Substrates["target-copy"]; !ok {
		t.Fatal("unrelated target-copy was removed")
	}
}

func TestRemoveCanonicalMissingAndMalformedStatesLeaveBytesUnchanged(t *testing.T) {
	t.Run("missing target", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".tendril", "substrates.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir canonical directory: %v", err)
		}
		original := []byte("# keep\nsubstrates:\n  existing:\n    url: https://example.com/existing\n")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatalf("write registry: %v", err)
		}
		if _, err := RemoveCanonical("missing", nil); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want explicit not-found", err)
		}
		unchanged, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read registry: %v", err)
		}
		if string(unchanged) != string(original) {
			t.Fatalf("missing removal changed registry:\n%s", unchanged)
		}
	})

	t.Run("malformed registry", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".tendril", "substrates.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir canonical directory: %v", err)
		}
		original := []byte("substrates: [")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatalf("write malformed registry: %v", err)
		}
		if _, err := RemoveCanonical("target", nil); err == nil {
			t.Fatal("malformed registry was accepted")
		}
		unchanged, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read malformed registry: %v", err)
		}
		if string(unchanged) != string(original) {
			t.Fatalf("malformed removal changed registry: %q", unchanged)
		}
	})

	t.Run("no registry", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if _, err := RemoveCanonical("missing", nil); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want explicit not-found", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".tendril", "substrates.yaml")); !os.IsNotExist(err) {
			t.Fatalf("missing removal created canonical registry: %v", err)
		}
	})
}

func TestRemoveCanonicalImportsLegacyAndPreservesExternalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyPath := filepath.Join(home, "substrates.yaml")
	legacy := []byte(`# legacy comment
credentials:
  target-profile:
    auth: { method: app, appId: "1", privateKeyPath: /keys/target.pem }
  keep-profile:
    auth: { method: pat, env: KEEP_TOKEN }
substrates:
  target:
    url: https://example.com/target
    profile: target-profile
  keep:
    url: https://example.com/keep
    profile: keep-profile
`)
	if err := os.WriteFile(legacyPath, legacy, 0o640); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}
	secret := filepath.Join(home, "target.pem")
	workspace := filepath.Join(home, ".tendril", "substrates", "target")
	fruit := filepath.Join(home, ".tendril", "fruit-sentinel")
	if err := os.MkdirAll(filepath.Dir(fruit), 0o700); err != nil {
		t.Fatalf("mkdir external sentinel directory: %v", err)
	}
	for _, path := range []string{secret, fruit} {
		if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
			t.Fatalf("write sentinel %s: %v", path, err)
		}
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("mkdir workspace sentinel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("workspace"), 0o600); err != nil {
		t.Fatalf("write workspace sentinel: %v", err)
	}

	result, err := RemoveCanonical("target", nil)
	if err != nil {
		t.Fatalf("remove legacy target: %v", err)
	}
	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	if !result.ImportedLegacy || result.Source != legacyPath || result.Destination != canonicalPath {
		t.Fatalf("remove result = %+v, want legacy import into canonical", result)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy registry after removal: %v", err)
	}
	if string(legacyAfter) != string(legacy) {
		t.Fatalf("legacy registry changed:\n%s", legacyAfter)
	}
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical registry after removal: %v", err)
	}
	if strings.Contains(string(canonical), "  target:\n") || strings.Contains(string(canonical), "target-profile") || !strings.Contains(string(canonical), "keep") {
		t.Fatalf("canonical removal did not remove only target and its final profile:\n%s", canonical)
	}
	for _, path := range []string{secret, fruit, filepath.Join(workspace, "keep.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("external sentinel %s was changed or removed: %v", path, err)
		}
	}
}

func TestRemoveCanonicalMissingProfileDoesNotTriggerBroadCleanup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir canonical directory: %v", err)
	}
	content := []byte(`credentials:
  unrelated:
    auth: { method: pat, env: OTHER_TOKEN }
substrates:
  target:
    url: https://example.com/target
    profile: already-missing
  unrelated:
    url: https://example.com/unrelated
    profile: unrelated
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	result, err := RemoveCanonical("target", nil)
	if err != nil {
		t.Fatalf("remove target with missing profile: %v", err)
	}
	if result.CredentialProfile != "already-missing" || result.CredentialProfileRemoved || result.CredentialProfileRetained {
		t.Fatalf("missing profile result = %+v, want no profile mutation", result)
	}
	updated, err := LoadCanonical()
	if err != nil {
		t.Fatalf("load registry after missing-profile removal: %v", err)
	}
	if _, ok := updated.Config.Credentials["unrelated"]; !ok {
		t.Fatal("unrelated credential profile was removed")
	}
	if _, ok := updated.Config.Substrates["unrelated"]; !ok {
		t.Fatal("unrelated Substrate was removed")
	}
}
