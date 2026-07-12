package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigParsesSequenceAndSproutEntries(t *testing.T) {
	path := writeConfig(t, `enabled: true
schedules:
  evening-ci:
    cron: "0 19 * * 1-5"
    sequence: local-ci
    provider: local
    model: llama3.2
    overlap: queue
    retries: 2
  nightly-review:
    cron: "30 21 * * *"
    sprout:
      transcript: "Review open PRs; dry-run comments only."
      substrate: opentendril-core
      genotype: github-ops
      provider: local
      model: llama3.2
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if len(cfg.Schedules) != 2 {
		t.Fatalf("schedules len = %d, want 2", len(cfg.Schedules))
	}

	ci := cfg.Schedules["evening-ci"]
	if ci.Cron != "0 19 * * 1-5" {
		t.Fatalf("evening-ci cron = %q", ci.Cron)
	}
	if ci.Sequence != "local-ci" {
		t.Fatalf("evening-ci sequence = %q, want local-ci", ci.Sequence)
	}
	if ci.Sprout != nil {
		t.Fatal("evening-ci sprout != nil, want nil")
	}
	if ci.Provider != "local" || ci.Model != "llama3.2" {
		t.Fatalf("evening-ci provider/model = %q/%q", ci.Provider, ci.Model)
	}
	if ci.Overlap != OverlapQueue {
		t.Fatalf("evening-ci overlap = %q, want queue", ci.Overlap)
	}
	if ci.Retries != 2 {
		t.Fatalf("evening-ci retries = %d, want 2", ci.Retries)
	}

	review := cfg.Schedules["nightly-review"]
	if review.Sprout == nil {
		t.Fatal("nightly-review sprout = nil, want set")
	}
	if review.Sprout.Transcript != "Review open PRs; dry-run comments only." {
		t.Fatalf("nightly-review transcript = %q", review.Sprout.Transcript)
	}
	if review.Sprout.Substrate != "opentendril-core" {
		t.Fatalf("nightly-review substrate = %q", review.Sprout.Substrate)
	}
	if review.Sprout.Genotype != "github-ops" {
		t.Fatalf("nightly-review genotype = %q", review.Sprout.Genotype)
	}
	if review.Overlap != OverlapSkip {
		t.Fatalf("nightly-review overlap = %q, want skip default", review.Overlap)
	}
	if review.Retries != 0 {
		t.Fatalf("nightly-review retries = %d, want 0 default", review.Retries)
	}
}

func TestLoadConfigMissingFileDisablesScheduling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil for a missing file", err)
	}
	if cfg.Enabled {
		t.Fatal("enabled = true, want false for a missing file")
	}
	if len(cfg.Schedules) != 0 {
		t.Fatalf("schedules len = %d, want 0", len(cfg.Schedules))
	}
}

func TestLoadConfigRejectsInvalidYAML(t *testing.T) {
	path := writeConfig(t, "enabled: [not\n  closed")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig() error = nil, want decode error")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "bad cron",
			content: `enabled: true
schedules:
  broken:
    cron: "61 19 * * *"
    sequence: local-ci
`,
			wantErr: "broken",
		},
		{
			name: "missing cron",
			content: `enabled: true
schedules:
  broken:
    sequence: local-ci
`,
			wantErr: "missing cron",
		},
		{
			name: "both sequence and sprout",
			content: `enabled: true
schedules:
  broken:
    cron: "0 19 * * *"
    sequence: local-ci
    sprout:
      transcript: "do things"
`,
			wantErr: "exactly one",
		},
		{
			name: "neither sequence nor sprout",
			content: `enabled: true
schedules:
  broken:
    cron: "0 19 * * *"
`,
			wantErr: "exactly one",
		},
		{
			name: "sprout without transcript",
			content: `enabled: true
schedules:
  broken:
    cron: "0 19 * * *"
    sprout:
      substrate: opentendril-core
`,
			wantErr: "transcript",
		},
		{
			name: "bad overlap",
			content: `enabled: true
schedules:
  broken:
    cron: "0 19 * * *"
    sequence: local-ci
    overlap: parallel
`,
			wantErr: "overlap",
		},
		{
			name: "negative retries",
			content: `enabled: true
schedules:
  broken:
    cron: "0 19 * * *"
    sequence: local-ci
    retries: -1
`,
			wantErr: "retries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.content)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatal("LoadConfig() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadConfig() error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfigEmptyFileIsDisabled(t *testing.T) {
	path := writeConfig(t, "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("enabled = true, want false for an empty file")
	}
	if len(cfg.Schedules) != 0 {
		t.Fatalf("schedules len = %d, want 0", len(cfg.Schedules))
	}
}
