package scheduler

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Overlap policies control what happens when a schedule fires while its
// previous run is still growing.
const (
	OverlapSkip  = "skip"
	OverlapQueue = "queue"
)

// Config maps to .tendril/schedules.yaml.
type Config struct {
	Enabled   bool             `yaml:"enabled"`
	Schedules map[string]Entry `yaml:"schedules"`
}

// Entry is one named schedule. Each fire grows exactly one of a Sequence
// (by name) or an ad-hoc Sprout — the two are mutually exclusive.
type Entry struct {
	// Cron is a 5-field expression (minute hour day-of-month month
	// day-of-week), evaluated in local time.
	Cron string `yaml:"cron"`

	// Sequence names a conductor sequence to grow. Mutually exclusive
	// with Sprout.
	Sequence string `yaml:"sequence,omitempty"`

	// Sprout describes an ad-hoc Sprout to grow. Mutually exclusive with
	// Sequence.
	Sprout *SproutSpec `yaml:"sprout,omitempty"`

	// Provider and Model optionally override the daemon defaults.
	Provider string `yaml:"provider,omitempty"`
	Model    string `yaml:"model,omitempty"`

	// Overlap is OverlapSkip (default) or OverlapQueue.
	Overlap string `yaml:"overlap,omitempty"`

	// Retries is how many times a failed run is re-grown (default 0).
	Retries int `yaml:"retries,omitempty"`
}

// SproutSpec describes the ad-hoc Sprout a schedule grows.
type SproutSpec struct {
	Transcript string `yaml:"transcript"`
	Substrate  string `yaml:"substrate,omitempty"`
	Genotype   string `yaml:"genotype,omitempty"`
	Provider   string `yaml:"provider,omitempty"`
	Model      string `yaml:"model,omitempty"`
}

// LoadConfig parses scheduler settings from the given YAML file path. A
// missing file is not an error: scheduling is simply disabled, matching the
// tolerance serve extends to telemetry.yaml. Every cron expression is
// validated at load so a bad schedules.yaml fails fast rather than at fire
// time.
func LoadConfig(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read scheduler config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode scheduler config %s: %w", path, err)
	}

	normalizeConfig(&cfg)
	if err := validateConfig(&cfg); err != nil {
		return Config{}, fmt.Errorf("scheduler config %s: %w", path, err)
	}
	return cfg, nil
}

func normalizeConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	for name, entry := range cfg.Schedules {
		if entry.Overlap == "" {
			entry.Overlap = OverlapSkip
			cfg.Schedules[name] = entry
		}
	}
}

func validateConfig(cfg *Config) error {
	// Walk entries in sorted order so a file with several problems always
	// reports the same one first.
	names := make([]string, 0, len(cfg.Schedules))
	for name := range cfg.Schedules {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := cfg.Schedules[name]

		if strings.TrimSpace(entry.Cron) == "" {
			return fmt.Errorf("schedule %q: missing cron expression", name)
		}
		if _, err := Parse(entry.Cron); err != nil {
			return fmt.Errorf("schedule %q: %w", name, err)
		}

		hasSequence := entry.Sequence != ""
		hasSprout := entry.Sprout != nil
		if hasSequence == hasSprout {
			return fmt.Errorf("schedule %q: exactly one of sequence or sprout is required", name)
		}
		if hasSprout && strings.TrimSpace(entry.Sprout.Transcript) == "" {
			return fmt.Errorf("schedule %q: sprout requires a transcript", name)
		}

		switch entry.Overlap {
		case OverlapSkip, OverlapQueue:
		default:
			return fmt.Errorf("schedule %q: overlap %q is not %q or %q", name, entry.Overlap, OverlapSkip, OverlapQueue)
		}

		if entry.Retries < 0 {
			return fmt.Errorf("schedule %q: retries must not be negative, got %d", name, entry.Retries)
		}
	}
	return nil
}
