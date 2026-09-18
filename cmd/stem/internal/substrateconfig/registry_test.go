package substrateconfig

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestMutateCanonicalSerializesConcurrentMutations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := MutateCanonical(func(root *yaml.Node) error {
				time.Sleep(10 * time.Millisecond)
				return addSubstrate(root, name, "https://example.com/"+name+".git")
			})
			errs <- err
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent mutation: %v", err)
		}
	}

	canonicalPath := filepath.Join(home, ".tendril", "substrates.yaml")
	content, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read concurrent canonical registry: %v", err)
	}
	var config conductor.SubstratesConfig
	if err := yaml.Unmarshal(content, &config); err != nil {
		t.Fatalf("decode concurrent canonical registry: %v", err)
	}
	if len(config.Substrates) != 2 || config.Substrates["first"].URL == "" || config.Substrates["second"].URL == "" {
		t.Fatalf("concurrent mutations lost a Substrate: %+v", config.Substrates)
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
