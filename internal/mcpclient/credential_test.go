package mcpclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCred(t *testing.T, dir, name, secret string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(secret), mode); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod credential: %v", err)
	}
	return path
}

func TestLoadCredential_Unconfigured(t *testing.T) {
	t.Setenv(EnvPollinatorCredential, "")
	t.Setenv(EnvMCPCredential, "")
	t.Setenv("TENDRIL_POLLEN", "")
	secret, err := LoadCredential()
	if err != nil {
		t.Fatalf("unconfigured lookup: %v", err)
	}
	if secret != "" {
		t.Fatalf("expected empty secret, got %q", secret)
	}
}

func TestLoadCredential_Precedence(t *testing.T) {
	dir := t.TempDir()
	polPath := writeCred(t, dir, "pol.pem", "tendril_refresh_pol", 0o600)
	mcpPath := writeCred(t, dir, "mcp.pem", "tendril_refresh_mcp", 0o600)

	t.Run("TENDRIL_POLLINATOR_CREDENTIAL wins", func(t *testing.T) {
		t.Setenv(EnvPollinatorCredential, polPath)
		t.Setenv(EnvMCPCredential, mcpPath)
		secret, err := LoadCredential()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if secret != "tendril_refresh_pol" {
			t.Fatalf("got %q, want tendril_refresh_pol", secret)
		}
	})

	t.Run("TENDRIL_MCP_CREDENTIAL is next", func(t *testing.T) {
		t.Setenv(EnvPollinatorCredential, "")
		t.Setenv(EnvMCPCredential, mcpPath)
		secret, err := LoadCredential()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if secret != "tendril_refresh_mcp" {
			t.Fatalf("got %q, want tendril_refresh_mcp", secret)
		}
	})

	t.Run("XDG default when TENDRIL_POLLEN is set", func(t *testing.T) {
		xdg := filepath.Join(dir, ".config")
		pollenPath := filepath.Join(xdg, "tendril", "pollinators", "testpollen")
		if err := os.MkdirAll(filepath.Dir(pollenPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(pollenPath, []byte("tendril_refresh_xdg"), 0o600); err != nil {
			t.Fatalf("write xdg: %v", err)
		}
		if err := os.Chmod(pollenPath, 0o600); err != nil {
			t.Fatalf("chmod xdg: %v", err)
		}
		t.Setenv(EnvPollinatorCredential, "")
		t.Setenv(EnvMCPCredential, "")
		t.Setenv("XDG_CONFIG_HOME", xdg)
		t.Setenv("TENDRIL_POLLEN", "testpollen")
		secret, err := LoadCredential()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if secret != "tendril_refresh_xdg" {
			t.Fatalf("got %q, want tendril_refresh_xdg", secret)
		}
	})
}

func TestLoadCredential_MissingExplicitFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv(EnvPollinatorCredential, missing)
	t.Setenv(EnvMCPCredential, "")
	_, err := LoadCredential()
	if err == nil {
		t.Fatal("expected missing explicit file to fail")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error must name the path, got %v", err)
	}
}

func TestLoadCredential_MissingDefaultIsQuiet(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), ".config")
	t.Setenv(EnvPollinatorCredential, "")
	t.Setenv(EnvMCPCredential, "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("TENDRIL_POLLEN", "absent-pollen")
	secret, err := LoadCredential()
	if err != nil {
		t.Fatalf("missing default must be quiet: %v", err)
	}
	if secret != "" {
		t.Fatalf("expected empty secret, got %q", secret)
	}
}

func TestLoadCredential_RefusesGroupOrWorldAccess(t *testing.T) {
	dir := t.TempDir()
	secret := "tendril_refresh_mysecret"
	path := writeCred(t, dir, "cred", secret, 0o644)

	t.Setenv(EnvPollinatorCredential, "")
	t.Setenv(EnvMCPCredential, path)

	_, err := LoadCredential()
	if err == nil {
		t.Fatal("expected 0644 file to be refused")
	}
	if !strings.Contains(err.Error(), "too permissive") || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("expected mode refusal, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("secret leaked into the error")
	}

	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	_, err = LoadCredential()
	if err == nil {
		t.Fatal("expected 0620 file to be refused")
	}
	if !strings.Contains(err.Error(), "too permissive") {
		t.Fatalf("expected mode refusal, got %v", err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCredential()
	if err != nil {
		t.Fatalf("0600 must succeed: %v", err)
	}
	if got != secret {
		t.Fatalf("got %q, want %q", got, secret)
	}
}

func TestLoadCredential_EmptyFile(t *testing.T) {
	path := writeCred(t, t.TempDir(), "empty", "   \n", 0o600)
	t.Setenv(EnvPollinatorCredential, "")
	t.Setenv(EnvMCPCredential, path)
	_, err := LoadCredential()
	if err == nil {
		t.Fatal("expected empty file to fail")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-file error, got %v", err)
	}
}
