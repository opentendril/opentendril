package substrateconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
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
