package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// The Stem's executable identity, recorded so another account can measure it.
//
// Executable integrity asks whether anyone but the owner can replace the binary
// the Stem runs. Answered from the Stem's own process that is exact. Answered
// from an account hosting Pollinators — the account that most needs to know —
// there is nothing to inspect, because that account runs a different binary.
//
// So the Stem writes down which binary it is. The record exists because tendril
// hardiness typed at a shell measures the binary that invocation resolved, which
// need not be the one ExecStart names. It is read by the Stem's own account.
// Ownership for outside callers is published on the health surface instead.

// stemIdentityFilename is the record, in the Stem's control-plane directory.
const stemIdentityFilename = "stem.json"

// stemIdentity is what the Stem knows about itself that another account cannot
// determine on its own.
type stemIdentity struct {
	// Executable is the resolved path of the running binary.
	Executable string `json:"executable"`
	// UID is the user the Stem runs as.
	UID int `json:"uid"`
}

func stemIdentityPath(tendrilDir string) string {
	return filepath.Join(tendrilDir, stemIdentityFilename)
}

// recordStemIdentity writes the running Stem's identity into the control plane.
//
// A failure is reported and never fatal: not being able to describe itself must
// not stop a Ramet serving. The consequence is a posture report that says it
// could not establish the Stem's binary, which is the honest outcome.
func recordStemIdentity(tendrilDir string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}

	payload, err := json.MarshalIndent(stemIdentity{Executable: executable, UID: os.Getuid()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode stem identity: %w", err)
	}
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", tendrilDir, err)
	}
	// The record exists because tendril hardiness typed at a shell measures the
	// binary that invocation resolved, which need not be the one ExecStart names.
	// It is read by the Stem's own account. Ownership for outside callers is
	// published on the health surface instead.
	if err := os.WriteFile(stemIdentityPath(tendrilDir), append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("write stem identity: %w", err)
	}
	return nil
}

// readStemIdentity returns the recorded identity, and whether one was readable.
func readStemIdentity(tendrilDir string) (stemIdentity, bool) {
	content, err := os.ReadFile(stemIdentityPath(tendrilDir))
	if err != nil {
		return stemIdentity{}, false
	}
	var identity stemIdentity
	if err := json.Unmarshal(content, &identity); err != nil {
		return stemIdentity{}, false
	}
	if identity.Executable == "" {
		return stemIdentity{}, false
	}
	return identity, true
}
