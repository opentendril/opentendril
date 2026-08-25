package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// TestWorkspaceResolutionConsistency asserts that all four call sites
// (Git, Stoma, Seed, Sprout) resolve Substrate workspaces identically for
// the same specification, preventing any one adapter from quietly ignoring
// the central resolver.
func TestWorkspaceResolutionConsistency(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	originalWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(originalWD)

	config := conductor.SubstratesConfig{
		Substrates: map[string]conductor.SubstrateSpec{
			"missing_managed": {
				Checkout: conductor.CheckoutSpec{Mode: "managed"},
			},
		},
	}
	b, _ := yaml.Marshal(config)
	if err := os.WriteFile(filepath.Join(dir, "substrates.yaml"), b, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, _ := conductor.LoadSubstratesConfig("")

	expectError := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: expected error, got nil", name)
		} else if !errors.Is(err, conductor.ErrWorkspaceAbsent) {
			t.Errorf("%s: got unexpected error: %v, want conductor.ErrWorkspaceAbsent", name, err)
		}
	}

	// 1. Git (via resolveGitWorkspace)
	_, _, errGit := resolveGitWorkspace(ctx, "missing_managed", cfg)
	expectError("Git", errGit)

	// 2. Stoma (via stomaOperations)
	stoma := stomaOperations()
	_, errStoma := stoma.Run(ctx, core.StomaSpec{Substrate: "missing_managed"})
	expectError("Stoma", errStoma)

	// 3. Seed (via seedOperations)
	seed := seedOperations(nil, nil)
	_, errSeed := seed.Run(ctx, core.SeedSpec{Substrate: "missing_managed", PhytomerID: "tendril-resolver"})
	expectError("Seed", errSeed)

	// 4. Sprout (via sproutOperations)
	sprout := sproutOperations(nil, nil)
	_, errSprout := sprout.Run(ctx, core.SproutSpec{Substrate: "missing_managed"})
	expectError("Sprout", errSprout)
}
