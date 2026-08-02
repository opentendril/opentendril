package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// plantGrants plants a grants file naming one Pollen, so which file was read is
// visible from the loaded grant rather than inferred from a path.
func plantGrants(t *testing.T, dir, pollen string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	body := "grants:\n  " + pollen + ":\n    operationClasses: [\"git.status\"]\n    substrates: [\"demo\"]\n"
	if err := os.WriteFile(filepath.Join(dir, core.DelegationGrantsFilename), []byte(body), 0o600); err != nil {
		t.Fatalf("write grants: %v", err)
	}
}

// The property this whole change exists for: with a grants file in BOTH the home
// control plane and the working directory, the home one is what governs.
//
// Naming a different Pollen in each file is what makes this discriminating. A
// test asserting only "grants loaded" passes against the defect, because the
// defect also loads grants — just the wrong ones.
func TestGrantsResolveFromControlPlaneNotWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(checkout)

	plantGrants(t, filepath.Join(home, grantsDirName), "control-plane-pollen")
	plantGrants(t, filepath.Join(checkout, grantsDirName), "checkout-pollen")

	dir, err := resolveGrantsDir()
	if err != nil {
		t.Fatalf("resolveGrantsDir: %v", err)
	}

	grants, err := core.LoadDelegationGrants(dir)
	if err != nil {
		t.Fatalf("LoadDelegationGrants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("got %d grant(s), want exactly 1", len(grants))
	}
	if grants[0].Pollen != "control-plane-pollen" {
		t.Errorf("grant Pollen = %q, want %q — the working directory's grants file governed", grants[0].Pollen, "control-plane-pollen")
	}
}

// A checkout carrying grants must not be able to authorise anything when the
// control plane holds none. This is the escalation case stated at
// core.LoadDelegationGrants, expressed as behaviour.
func TestCheckoutGrantsCannotAuthorizeWithEmptyControlPlane(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(checkout)

	plantGrants(t, filepath.Join(checkout, grantsDirName), "checkout-pollen")

	dir, err := resolveGrantsDir()
	if err != nil {
		t.Fatalf("resolveGrantsDir: %v", err)
	}
	grants, err := core.LoadDelegationGrants(dir)
	if err != nil {
		t.Fatalf("LoadDelegationGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("got %d grant(s) from an empty control plane, want 0 — a checkout widened its own authority", len(grants))
	}
}

func TestResolveGrantsDirIsHomeAnchored(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(checkout)

	dir, err := resolveGrantsDir()
	if err != nil {
		t.Fatalf("resolveGrantsDir: %v", err)
	}
	if want := filepath.Join(home, grantsDirName); dir != want {
		t.Errorf("resolveGrantsDir() = %q, want %q", dir, want)
	}
}

// An unset HOME must never degrade to the working directory. The account
// database still resolves a home, so the result stays outside the checkout —
// the outcome that matters is the absence of a working-directory path, which is
// asserted directly rather than via the specific fallback value.
func TestResolveGrantsDirNeverFallsBackToWorkingDirectory(t *testing.T) {
	checkout := t.TempDir()
	t.Setenv("HOME", "")
	t.Chdir(checkout)

	dir, err := resolveGrantsDir()
	if err != nil {
		// Refusing is acceptable; silently reading the checkout is not.
		return
	}

	// Resolved against the working directory, not compared as a string. A
	// relative result such as "./.tendril" IS the working directory and must
	// fail here — an earlier version of this test compared literals and let
	// exactly that through.
	absolute, absErr := filepath.Abs(dir)
	if absErr != nil {
		t.Fatalf("resolve %q: %v", dir, absErr)
	}
	if absolute == filepath.Join(checkout, grantsDirName) {
		t.Errorf("resolveGrantsDir() = %q, which resolves into the working directory %q with HOME unset", dir, checkout)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("resolveGrantsDir() = %q is relative, so it follows the working directory wherever the process moves", dir)
	}
}

func TestWarnNamesTheIgnoredFileAndTheControlPlane(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	t.Chdir(checkout)
	plantGrants(t, filepath.Join(checkout, grantsDirName), "checkout-pollen")

	grantsDir := filepath.Join(home, grantsDirName)
	var out bytes.Buffer
	warnIfWorkingDirectoryGrantsIgnored(&out, grantsDir)

	got := out.String()
	if got == "" {
		t.Fatal("no warning written for an ignored working-directory grants file")
	}
	// The wording is the value: an operator must learn which file was skipped
	// and where to put it instead.
	if want := filepath.Join(checkout, grantsDirName, core.DelegationGrantsFilename); !strings.Contains(got, want) {
		t.Errorf("warning does not name the ignored file %q: %s", want, got)
	}
	if !strings.Contains(got, grantsDir) {
		t.Errorf("warning does not name the control plane %q: %s", grantsDir, got)
	}
}

func TestNoWarningWithoutAWorkingDirectoryGrantsFile(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	warnIfWorkingDirectoryGrantsIgnored(&out, filepath.Join(home, grantsDirName))

	if got := out.String(); got != "" {
		t.Errorf("warned with no working-directory grants file present: %s", got)
	}
}

// Invoked from the control plane itself, nothing has been ignored and nothing
// should be said. Without this the guard would warn about the very file it read.
func TestNoWarningWhenWorkingDirectoryIsTheControlPlane(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	plantGrants(t, filepath.Join(home, grantsDirName), "control-plane-pollen")

	var out bytes.Buffer
	warnIfWorkingDirectoryGrantsIgnored(&out, filepath.Join(home, grantsDirName))

	if got := out.String(); got != "" {
		t.Errorf("warned about the control plane's own grants file: %s", got)
	}
}
