package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigParsesResinAndTransporters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.yaml")
	content := `enabled: true
resin:
  enabled: true
  format: json
  level: debug
transporters:
  - type: webhook
    endpoint: http://localhost:9999/telemetry
    api_key: test-key
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if !cfg.Resin.Enabled {
		t.Fatal("resin.enabled = false, want true")
	}
	if cfg.Resin.Format != "json" {
		t.Fatalf("resin.format = %q, want json", cfg.Resin.Format)
	}
	if cfg.Resin.Level != "debug" {
		t.Fatalf("resin.level = %q, want debug", cfg.Resin.Level)
	}
	if len(cfg.Transporters) != 1 {
		t.Fatalf("transporters len = %d, want 1", len(cfg.Transporters))
	}
	if cfg.Transporters[0].Type != "webhook" {
		t.Fatalf("transporter type = %q, want webhook", cfg.Transporters[0].Type)
	}
	if cfg.Transporters[0].Endpoint != "http://localhost:9999/telemetry" {
		t.Fatalf("transporter endpoint = %q", cfg.Transporters[0].Endpoint)
	}
}

func TestLoadConfigAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.yaml")
	if err := os.WriteFile(path, []byte("enabled: true\nresin:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Resin.Format != "json" {
		t.Fatalf("resin.format = %q, want json default", cfg.Resin.Format)
	}
	if cfg.Resin.Level != "info" {
		t.Fatalf("resin.level = %q, want info default", cfg.Resin.Level)
	}
}