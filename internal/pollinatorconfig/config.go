// Package pollinatorconfig owns the Pollinator-side connection contract.
//
// Connection metadata identifies a Stem origin and a credential reference. It
// never contains credential secret material or Stem authority.
package pollinatorconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	configDirectoryName = "tendril"
	configFileName      = "connections.yaml"
	credentialDirectory = "pollinators"
	schemaVersion       = 1
)

var simpleNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SelectionSource describes where a connection selection came from.
type SelectionSource string

const (
	SelectionExplicit SelectionSource = "explicit"
	SelectionDefault  SelectionSource = "default"
)

// Connection is one Pollinator-to-Stem connection profile.
type Connection struct {
	Endpoint   string `yaml:"endpoint"`
	Credential string `yaml:"credential"`
}

// Config is the versioned Pollinator connection configuration.
type Config struct {
	Version     int                   `yaml:"version"`
	Default     string                `yaml:"default,omitempty"`
	Connections map[string]Connection `yaml:"connections"`
}

// Selection is a validated named connection and its resolution source.
type Selection struct {
	Name       string
	Connection Connection
	Source     SelectionSource
}

// ConfigRoot returns the canonical user-owned configuration root. It follows
// XDG_CONFIG_HOME and otherwise uses the current user's ~/.config directory.
func ConfigRoot() string {
	root := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

// ConfigFile returns the canonical connections metadata path.
func ConfigFile() string {
	return filepath.Join(ConfigRoot(), configDirectoryName, configFileName)
}

// CredentialDir returns the canonical Pollinator credential directory.
func CredentialDir() string {
	return filepath.Join(ConfigRoot(), configDirectoryName, credentialDirectory)
}

// ValidateName validates a profile or credential reference name. Names are
// intentionally narrower than paths so they cannot escape their directory.
func ValidateName(name string) error {
	if !simpleNamePattern.MatchString(name) {
		return fmt.Errorf("name %q must contain only letters, numbers, dots, hyphens, or underscores and start with a letter or number", name)
	}
	return nil
}

// ValidateCredentialReference validates a credential reference without
// accessing the referenced file.
func ValidateCredentialReference(reference string) error {
	if err := ValidateName(reference); err != nil {
		return fmt.Errorf("invalid credential reference: %w", err)
	}
	return nil
}

// ResolveCredentialReference resolves a validated reference below the
// canonical Pollinator credential directory.
func ResolveCredentialReference(reference string) (string, error) {
	if err := ValidateCredentialReference(reference); err != nil {
		return "", err
	}
	root := CredentialDir()
	if root == "" {
		return "", errors.New("cannot resolve Pollinator credential directory: user home is unavailable")
	}
	path := filepath.Join(root, reference)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("credential reference %q escapes the canonical Pollinator credential directory", reference)
	}
	if err := rejectSymlinkComponents(root, path); err != nil {
		return "", err
	}
	return path, nil
}

func rejectSymlinkComponents(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("credential reference path %s must not contain symbolic links", path)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect credential reference path %s: %w", path, err)
		}
		if current == root {
			return nil
		}
		next := filepath.Dir(current)
		if next == current {
			return fmt.Errorf("credential reference path %s is outside the canonical Pollinator credential directory", path)
		}
	}
}

// NormalizeEndpoint validates and canonicalizes a Stem URL origin.
func NormalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("endpoint is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}
	if u.Scheme == "" {
		return "", errors.New("endpoint must include a scheme")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("endpoint must include a host")
	}
	if u.User != nil {
		return "", errors.New("endpoint must not contain credentials")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("endpoint must not contain a query string")
	}
	if u.Fragment != "" {
		return "", errors.New("endpoint must not contain a fragment")
	}
	if u.Opaque != "" {
		return "", errors.New("endpoint must be a URL origin")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("endpoint must not contain an application path")
	}
	if u.RawPath != "" && u.RawPath != "/" {
		return "", errors.New("endpoint must not contain an application path")
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("endpoint scheme %q is unsupported; use http or https", u.Scheme)
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	u.ForceQuery = false
	return strings.TrimRight(u.String(), "/"), nil
}

// Validate checks the complete schema and normalizes endpoint values in the
// profile map. It does not read any credential files.
func (c Config) Validate() error {
	if c.Version != schemaVersion {
		if c.Version == 0 {
			return fmt.Errorf("configuration version is required and must be %d", schemaVersion)
		}
		return fmt.Errorf("unsupported configuration version %d; expected %d", c.Version, schemaVersion)
	}
	if c.Connections == nil {
		return errors.New("connections is required")
	}
	for name, connection := range c.Connections {
		if err := ValidateName(name); err != nil {
			return fmt.Errorf("invalid connection %q: %w", name, err)
		}
		endpoint, err := NormalizeEndpoint(connection.Endpoint)
		if err != nil {
			return fmt.Errorf("connection %q: %w", name, err)
		}
		if err := ValidateCredentialReference(connection.Credential); err != nil {
			return fmt.Errorf("connection %q: %w", name, err)
		}
		connection.Endpoint = endpoint
		c.Connections[name] = connection
	}
	if c.Default != "" {
		if err := ValidateName(c.Default); err != nil {
			return fmt.Errorf("invalid default connection: %w", err)
		}
		if _, ok := c.Connections[c.Default]; !ok {
			return fmt.Errorf("default connection %q does not exist", c.Default)
		}
	}
	return nil
}

// Load reads and validates the canonical configuration.
func Load() (Config, error) {
	return LoadFile(ConfigFile())
}

// LoadFile reads and validates a configuration at path. It is primarily
// useful for tests; user-facing callers should use Load so the canonical path
// cannot be replaced by an alternate configuration flag.
func LoadFile(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("load Pollinator connection config %s: %w", path, err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode Pollinator connection config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Config{}, fmt.Errorf("decode Pollinator connection config %s: multiple YAML documents are not allowed", path)
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode Pollinator connection config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate Pollinator connection config %s: %w", path, err)
	}
	return cfg, nil
}

// Save validates and atomically writes the canonical configuration with
// user-only directory and file permissions.
func Save(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refuse to save invalid Pollinator connection config: %w", err)
	}
	path := ConfigFile()
	if ConfigRoot() == "" {
		return errors.New("cannot save Pollinator connection config: user home is unavailable")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Pollinator config directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restrict Pollinator config directory %s: %w", dir, err)
	}
	contents, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode Pollinator connection config: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".connections.yaml-*")
	if err != nil {
		return fmt.Errorf("create atomic Pollinator config file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("restrict temporary Pollinator config file: %w", err)
	}
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write Pollinator connection config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync Pollinator connection config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close Pollinator connection config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("atomically replace Pollinator connection config %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict Pollinator connection config %s: %w", path, err)
	}
	return nil
}

// Select resolves an explicit profile, or the configured default when name is
// empty. It never invents a localhost profile.
func (c Config) Select(name string) (Selection, error) {
	if name != "" {
		if err := ValidateName(name); err != nil {
			return Selection{}, fmt.Errorf("invalid connection selection: %w", err)
		}
		connection, ok := c.Connections[name]
		if !ok {
			return Selection{}, fmt.Errorf("connection %q does not exist", name)
		}
		return Selection{Name: name, Connection: connection, Source: SelectionExplicit}, nil
	}
	if c.Default == "" {
		return Selection{}, errors.New("no connection selected: specify --connection <name> or configure a default with 'tendril-mcp connection use <name>'")
	}
	connection, ok := c.Connections[c.Default]
	if !ok {
		return Selection{}, fmt.Errorf("configured default connection %q does not exist", c.Default)
	}
	return Selection{Name: c.Default, Connection: connection, Source: SelectionDefault}, nil
}
