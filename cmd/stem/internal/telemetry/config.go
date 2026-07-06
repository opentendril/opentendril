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
	Enabled bool   `yaml:"enabled"`
	Format  string `yaml:"format"`
	Level   string `yaml:"level"`
}

// TransporterConfig describes one external telemetry emitter.
type TransporterConfig struct {
	Type     string   `yaml:"type"`
	Endpoint string   `yaml:"endpoint,omitempty"`
	Port     int      `yaml:"port,omitempty"`
	Brokers  []string `yaml:"brokers,omitempty"`
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
}