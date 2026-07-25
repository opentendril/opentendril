package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestCLIDelegationImpactWiring(t *testing.T) {
	// 1. Medium impact (authorized, confirmAbove is High)
	t.Run("medium impact authorized", func(t *testing.T) {
		writeGrants(t, "grants:\n  claude:\n    operationClasses: [git.commit]\n    substrates: [demo]\n    confirmAbove:\n      impact: high\n")
		t.Setenv(envPollenCLI, "claude")
		delegation := newCLIDelegation(context.Background())
		defer delegation.Close()

		ctx := delegation.Authorize(context.Background(), core.CapGitCommit, "demo")
		if got := core.PollenFromContext(ctx); got != "claude" {
			t.Fatalf("context Pollen = %q, want claude", got)
		}
	})

	// 2. High impact (denied because confirmAbove is High, causing os.Exit(1))
	t.Run("high impact denied", func(t *testing.T) {
		if os.Getenv("BE_CRASHER") == "1" {
			writeGrants(t, "grants:\n  claude:\n    operationClasses: [git.push]\n    substrates: [demo]\n    confirmAbove:\n      impact: high\n")
			t.Setenv(envPollenCLI, "claude")
			delegation := newCLIDelegation(context.Background())
			delegation.Authorize(context.Background(), core.CapGitPush, "demo")
			return
		}

		cmd := exec.Command(os.Args[0], "-test.run=TestCLIDelegationImpactWiring/high_impact_denied")
		cmd.Env = append(os.Environ(), "BE_CRASHER=1")
		out, err := cmd.CombinedOutput()
		if e, ok := err.(*exec.ExitError); ok && !e.Success() {
			if !strings.Contains(string(out), "requires human confirmation") {
				t.Fatalf("expected denial due to confirmation threshold, got output: %s", string(out))
			}
			return
		}
		t.Fatalf("process ran with err %v, want exit status 1. Output: %s", err, string(out))
	})
}
