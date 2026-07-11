package telemetry

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config maps to .tendril/telemetry.yaml.
type Config struct {
	Enabled      bool                `yaml:"enabled"`
	Resin        ResinConfig         `yaml:"resin"`
	Transporters []TransporterConfig `yaml:"transporters"`
}

// ResinConfig controls local structured logging (Resin sink).
type ResinConfig struct {
	Enabled bool        `yaml:"enabled"`
	Format  string      `yaml:"format"`
	Level   string      `yaml:"level"`
	Amber   AmberConfig `yaml:"amber"`
}

// AmberConfig controls how Resin hardens into Amber: when the active
// resin.log grows past MaxSizeKB it is gzip-compressed into the amber/
// archive directory next to it, keeping at most Keep archives (issue #136).
type AmberConfig struct {
	Enabled bool `yaml:"enabled"`
	// MaxSizeKB uses the same snake_case key style as api_key above.
	MaxSizeKB int `yaml:"max_size_kb"`
	Keep      int `yaml:"keep"`
}

// TransporterConfig describes one external telemetry emitter.
type TransporterConfig struct {
	Type     string   `yaml:"type"`
	Endpoint string   `yaml:"endpoint,omitempty"`
	Port     int      `yaml:"port,omitempty"`
	Brokers  []string `yaml:"brokers,omitempty"`
	Channel  string   `yaml:"channel,omitempty"`
	APIKey   string   `yaml:"api_key,omitempty"`
}

// LoadConfig parses telemetry settings from the given YAML file path.
func LoadConfig(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read telemetry config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return nil, fmt.Errorf("decode telemetry config %s: %w", path, err)
	}

	normalizeConfig(&cfg)
	return &cfg, nil
}

func normalizeConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	if cfg.Resin.Format == "" {
		cfg.Resin.Format = "json"
	}
	if cfg.Resin.Level == "" {
		cfg.Resin.Level = "info"
	}
	if cfg.Resin.Amber.MaxSizeKB <= 0 {
		cfg.Resin.Amber.MaxSizeKB = 1024
	}
	if cfg.Resin.Amber.Keep <= 0 {
		cfg.Resin.Amber.Keep = 5
	}
}
