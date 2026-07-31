package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
			"missing_path": {
				Checkout: conductor.CheckoutSpec{Mode: "path", Path: "/path/does/not/exist"},
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
		} else if !strings.Contains(err.Error(), "does not resolve to a local workspace directory") {
			t.Errorf("%s: got unexpected error: %v", name, err)
		}
	}

	// 1. Git (via resolveGitWorkspace)
	_, _, errGit := resolveGitWorkspace(ctx, "missing_path", cfg)
	expectError("Git", errGit)

	// 2. Stoma (via stomaOperations)
	stoma := stomaOperations()
	_, errStoma := stoma.Run(ctx, core.StomaSpec{Substrate: "missing_path"})
	expectError("Stoma", errStoma)

	// 3. Seed (via seedOperations)
	seed := seedOperations()
	_, errSeed := seed.Run(ctx, core.SeedSpec{Substrate: "missing_path"})
	expectError("Seed", errSeed)

	// 4. Sprout (via sproutOperations)
	sprout := sproutOperations(nil, nil)
	_, errSprout := sprout.Run(ctx, core.SproutSpec{Substrate: "missing_path"})
	expectError("Sprout", errSprout)
}
