package main

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// Where delegation grants are read from on the caller-invoked surfaces, and why
// it is not the working directory.
//
// Grants are the one control-plane artifact that must not be discoverable inside
// a Substrate checkout. A grants file carried by a cloned repository would
// otherwise decide what a Pollen may do the moment a surface is opened inside
// that repository — the Substrate widening its own authority, which is the
// property core.LoadDelegationGrants documents and which resolving from the
// working directory silently defeated.
//
// This applies to the surfaces whose working directory is INCIDENTAL: the
// delegated command line and the in-process stdio server both run wherever the
// caller happens to be standing. The serving Stem is deliberately excluded — its
// unit sets WorkingDirectory to the control plane on purpose, so for that process
// the working directory is a configured fact rather than an accident.
//
// It is also deliberately narrower than "the control plane moves". substrates.yaml
// is DESIGNED to be searched across candidate locations, and the Botanist key at
// .tendril/api-key is documented as working-directory-relative. Only the
// authorization lane is anchored here, because only the authorization lane
// carries the property.

// grantsDirName is the control-plane directory beneath the home.
const grantsDirName = ".tendril"

// resolveGrantsDir returns the directory delegation grants load from.
//
// The anchor is the invoking account's own home, matching where `tendril setup`
// and `tendril init` already write user-global control-plane state.
//
// $HOME is consulted first and the account database second, because a service
// manager need not export $HOME and falling back to the working directory is the
// one outcome this function exists to make impossible. An unresolvable home is
// an error rather than an empty path: core.LoadDelegationGrants joins its
// argument with the filename, so "" would read grants.yaml from the working
// directory — precisely the behaviour being removed.
func resolveGrantsDir() (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, grantsDirName), nil
	}

	// $HOME unset. The account database still knows, and a Stem started by a
	// service manager that does not export it must not lose its control plane.
	if current, err := user.Current(); err == nil && current.HomeDir != "" {
		return filepath.Join(current.HomeDir, grantsDirName), nil
	}

	return "", fmt.Errorf("resolve home directory for the delegation control plane")
}

// warnIfWorkingDirectoryGrantsIgnored reports a grants file in the working
// directory that is no longer consulted.
//
// An operator who placed one there believes it is in force. Passing over it in
// silence reproduces the original defect in the opposite direction: policy that
// is not what the person reading the directory thinks it is. Saying so costs one
// line and is only ever printed when such a file actually exists.
//
// Nothing is printed when the working directory IS the control-plane directory,
// which is the ordinary case for a surface invoked from its own home.
//
// The destination is a parameter so a test can read what was written. A warning
// asserted only by "it did not crash" is a warning nobody has checked the wording
// of, and the wording is the whole value here.
func warnIfWorkingDirectoryGrantsIgnored(out io.Writer, grantsDir string) {
	workingDir, err := os.Getwd()
	if err != nil {
		return
	}

	localDir := filepath.Join(workingDir, grantsDirName)
	if localDir == grantsDir {
		return
	}

	localGrants := filepath.Join(localDir, core.DelegationGrantsFilename)
	if _, statErr := os.Stat(localGrants); statErr != nil {
		return
	}

	fmt.Fprintf(out, "⚠️ Ignoring %s: delegation grants are read only from the control plane at %s, never from a checkout. Move it there for it to take effect.\n", localGrants, grantsDir)
}
