package pollinatorconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathsUseXDGConfigHome(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	if got, want := ConfigFile(), filepath.Join(xdg, "tendril", "connections.yaml"); got != want {
		t.Fatalf("ConfigFile() = %q, want %q", got, want)
	}
	if got, want := CredentialDir(), filepath.Join(xdg, "tendril", "pollinators"); got != want {
		t.Fatalf("CredentialDir() = %q, want %q", got, want)
	}
}

func TestPathsFallBackToHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	if got, want := ConfigFile(), filepath.Join(home, ".config", "tendril", "connections.yaml"); got != want {
		t.Fatalf("ConfigFile() = %q, want %q", got, want)
	}
}

func TestLoadRejectsMalformedAndInvalidConfigs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "malformed", yaml: "version: [", want: "decode"},
		{name: "missing version", yaml: "connections: {}\n", want: "version"},
		{name: "unsupported version", yaml: "version: 2\nconnections: {}\n", want: "version"},
		{name: "unknown field", yaml: "version: 1\nconnections: {}\nextra: true\n", want: "field extra not found"},
		{name: "default missing", yaml: "version: 1\ndefault: absent\nconnections:\n  local:\n    endpoint: http://127.0.0.1:8080\n    credential: codex\n", want: "default"},
		{name: "missing endpoint", yaml: "version: 1\nconnections:\n  local:\n    credential: codex\n", want: "endpoint"},
		{name: "bad endpoint", yaml: "version: 1\nconnections:\n  local:\n    endpoint: http://user:pass@example.test\n    credential: codex\n", want: "credentials"},
		{name: "bad credential reference", yaml: "version: 1\nconnections:\n  local:\n    endpoint: http://127.0.0.1:8080\n    credential: ../secret\n", want: "credential"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connections.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFile(path)
			if err == nil {
				t.Fatal("LoadFile succeeded for invalid configuration")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadMissingConfigIsActionable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "connections.yaml")
	_, err := LoadFile(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadFile missing error = %v, want os.ErrNotExist", err)
	}
}

func TestEndpointAndCredentialValidation(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:8080",
		"https://stem.example",
		"http://[::1]:8080/",
	}
	for _, endpoint := range valid {
		if got, err := NormalizeEndpoint(endpoint); err != nil || got == "" {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v; want valid origin", endpoint, got, err)
		}
	}
	for _, endpoint := range []string{
		"127.0.0.1:8080",
		"http:///missing-host",
		"http://user:pass@example.test",
		"http://example.test/api",
		"http://example.test?query=yes",
		"http://example.test#fragment",
	} {
		if _, err := NormalizeEndpoint(endpoint); err == nil {
			t.Errorf("NormalizeEndpoint(%q) succeeded; want rejection", endpoint)
		}
	}

	for _, name := range []string{"codex", "pollinator-1", "name.with.dots"} {
		if err := ValidateCredentialReference(name); err != nil {
			t.Errorf("ValidateCredentialReference(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "../secret", "nested/secret", `nested\\secret`, "name with space"} {
		if err := ValidateCredentialReference(name); err == nil {
			t.Errorf("ValidateCredentialReference(%q) succeeded; want rejection", name)
		}
	}
}

func TestSaveLoadAndResolveConnections(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg := Config{
		Version: 1,
		Default: "local",
		Connections: map[string]Connection{
			"local":  {Endpoint: "http://127.0.0.1:8080", Credential: "codex"},
			"backup": {Endpoint: "http://127.0.0.1:8081", Credential: "other"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("connections.yaml mode = %04o, want 0600", got)
	}
	if entries, err := os.ReadDir(filepath.Dir(ConfigFile())); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 || entries[0].Name() != "connections.yaml" {
		t.Fatalf("config directory entries = %v, want only connections.yaml", entries)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	selection, err := loaded.Select("")
	if err != nil || selection.Name != "local" || selection.Source != SelectionDefault {
		t.Fatalf("default selection = %+v, %v", selection, err)
	}
	selection, err = loaded.Select("backup")
	if err != nil || selection.Name != "backup" || selection.Source != SelectionExplicit {
		t.Fatalf("explicit selection = %+v, %v", selection, err)
	}
	path, err := ResolveCredentialReference(selection.Connection.Credential)
	if err != nil || path != filepath.Join(xdg, "tendril", "pollinators", "other") {
		t.Fatalf("credential path = %q, %v", path, err)
	}
}

func TestResolveCredentialReferenceRejectsSymlink(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	credentialDir := filepath.Join(xdg, "tendril", "pollinators")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(credentialDir, "codex")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCredentialReference("codex"); err == nil {
		t.Fatal("ResolveCredentialReference accepted a symlinked credential")
	}
}

func TestSelectRequiresExplicitOrDefaultConnection(t *testing.T) {
	cfg := Config{Version: 1, Connections: map[string]Connection{
		"local": {Endpoint: "http://127.0.0.1:8080", Credential: "codex"},
	}}
	if _, err := cfg.Select("missing"); err == nil {
		t.Fatal("explicit missing connection succeeded")
	}
	if _, err := cfg.Select(""); err == nil {
		t.Fatal("selection without default succeeded")
	}
}
