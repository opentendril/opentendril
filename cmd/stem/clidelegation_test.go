package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// writeGrants puts a grants file in the control plane newCLIDelegation reads,
// and moves the working directory somewhere else entirely.
//
// The working directory is deliberately NOT where the file lands. Every test
// using this helper therefore also demonstrates that grants are anchored to the
// control plane rather than to wherever the invocation happens to run — the
// property that stops a Substrate checkout supplying its own grants.
func writeGrants(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".tendril", "grants.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write grants: %v", err)
	}
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
}

const cliGrants = `grants:
  claude:
    operationClasses: [git.status, git.commit]
    substrates: [demo]
`

// TestBotanistInvocationIsUngated: with no Pollen declared, a command line run
// is a Botanist at a terminal — nothing is authorised, nothing is stamped, and
// the context is returned untouched.
func TestBotanistInvocationIsUngated(t *testing.T) {
	writeGrants(t, cliGrants)
	t.Setenv(envPollenCLI, "")

	delegation := newCLIDelegation(context.Background())
	defer delegation.Close()
	if delegation.Pollen != "" {
		t.Fatalf("Pollen = %q, want none for a Botanist invocation", delegation.Pollen)
	}

	ctx := delegation.Authorize(context.Background(), core.CapGitPush, "anything-at-all")
	if got := core.PollenFromContext(ctx); got != "" {
		t.Fatalf("context carries Pollen %q for an undelegated invocation", got)
	}
}

// TestDeclaredPollenIsStampedWhenGranted: a permitted operation returns a
// context carrying the Pollen, which is what routes work into that
// Pollinator's isolated workspace.
func TestDeclaredPollenIsStampedWhenGranted(t *testing.T) {
	writeGrants(t, cliGrants)
	t.Setenv(envPollenCLI, "claude")

	delegation := newCLIDelegation(context.Background())
	defer delegation.Close()
	if delegation.Pollen != "claude" {
		t.Fatalf("Pollen = %q, want claude", delegation.Pollen)
	}

	ctx := delegation.Authorize(context.Background(), core.CapGitStatus, "demo")
	if got := core.PollenFromContext(ctx); got != "claude" {
		t.Fatalf("context Pollen = %q, want claude — without it the work runs in the wrong workspace", got)
	}
}

// TestNonDelegableClassIsNotGated: declaring a Pollen must not turn every
// command into a delegated one, and must not silently grant anything on a
// capability the delegation model does not cover.
func TestNonDelegableClassIsNotGated(t *testing.T) {
	writeGrants(t, cliGrants)
	t.Setenv(envPollenCLI, "claude")

	delegation := newCLIDelegation(context.Background())
	defer delegation.Close()

	ctx := delegation.Authorize(context.Background(), core.CapGenomeView, "demo")
	if got := core.PollenFromContext(ctx); got != "" {
		t.Fatalf("a non-delegable capability stamped Pollen %q", got)
	}
}

// TestDeclaredPollenIsAuditControlNotBoundary documents, as an executable
// statement, the property this tier does NOT have: the Pollen is declared by
// the caller, so any caller can claim any identity. If this ever becomes false
// — if the Pollen starts being bound by something the caller cannot set — this
// test should fail and the documentation claiming it is only an audit control
// should be revisited.
func TestDeclaredPollenIsAuditControlNotBoundary(t *testing.T) {
	writeGrants(t, cliGrants)

	// The "impostor" holds no grant at all...
	t.Setenv(envPollenCLI, "impostor")
	impostor := newCLIDelegation(context.Background())
	defer impostor.Close()
	if impostor.Pollen != "impostor" {
		t.Fatalf("Pollen = %q, want the declared value taken at face value", impostor.Pollen)
	}

	// ...but nothing stops it declaring the granted identity instead, because
	// the environment belongs to the caller. This is the limitation the tier
	// above exists to remove.
	t.Setenv(envPollenCLI, "claude")
	claimed := newCLIDelegation(context.Background())
	defer claimed.Close()
	ctx := claimed.Authorize(context.Background(), core.CapGitStatus, "demo")
	if core.PollenFromContext(ctx) != "claude" {
		t.Fatal("a caller could not declare the granted Pollen — if that is now true, this tier is stronger than documented")
	}
}
