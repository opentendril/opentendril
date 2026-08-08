package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const envMCPCredential = "TENDRIL_MCP_CREDENTIAL"

// loadMCPCredential reads a durable root credential. It checks TENDRIL_POLLINATOR_CREDENTIAL,
// then TENDRIL_MCP_CREDENTIAL. If both are unset, it defaults to
// ~/.config/tendril/pollinators/<pollen> (using TENDRIL_POLLEN).
// If no path is found or the default path does not exist, it returns an empty string
// and no error (the ordinary, unconfigured case). It refuses files that are
// missing (if explicitly requested) or too permissive (any group or other permission),
// returning a safe error that names the path and mode but never the secret.
func loadMCPCredential() (string, error) {
	path := strings.TrimSpace(os.Getenv("TENDRIL_POLLINATOR_CREDENTIAL"))
	if path == "" {
		path = strings.TrimSpace(os.Getenv("TENDRIL_MCP_CREDENTIAL"))
	}

	isDefault := false
	if path == "" {
		pollen := strings.TrimSpace(os.Getenv("TENDRIL_POLLEN"))
		if pollen != "" {
			xdgConfig := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
			if xdgConfig == "" {
				if home, err := os.UserHomeDir(); err == nil {
					xdgConfig = filepath.Join(home, ".config")
				}
			}
			if xdgConfig != "" {
				path = filepath.Join(xdgConfig, "tendril", "pollinators", pollen)
				isDefault = true
			}
		}
	}

	if path == "" {
		return "", nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) && isDefault {
			return "", nil
		}
		return "", fmt.Errorf("credential file %s: %w", path, err)
	}

	// Refuse an over-permissive file. It must not be group- or world-readable or writable.
	// 0o077 checks all bits for group (0o070) and other (0o007).
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("credential file %s is too permissive (mode %04o); must be 0600 or stricter", path, info.Mode().Perm())
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read credential file %s: %w", path, err)
	}

	secret := strings.TrimSpace(string(content))
	if secret == "" {
		return "", fmt.Errorf("credential file %s is empty", path)
	}
	return secret, nil
}
