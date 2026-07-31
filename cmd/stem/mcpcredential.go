package main

import (
	"fmt"
	"os"
	"strings"
)

const envMCPCredential = "TENDRIL_MCP_CREDENTIAL"

// loadMCPCredential reads a durable root credential from the file named by
// TENDRIL_MCP_CREDENTIAL. If the variable is unset, it returns an empty string
// and no error (the ordinary, unconfigured case). It refuses files that are
// missing or too permissive (group- or world-readable), returning a safe error
// that names the path and mode but never the secret.
func loadMCPCredential() (string, error) {
	path := strings.TrimSpace(os.Getenv(envMCPCredential))
	if path == "" {
		return "", nil
	}

	info, err := os.Stat(path)
	if err != nil {
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
