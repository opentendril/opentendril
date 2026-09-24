package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestSproutExecutionPortCopiesConductorFailureProvenance(t *testing.T) {
	originalRun := runSproutTerrarium
	runSproutTerrarium = func(context.Context, *conductor.DockerOrchestrator, string) (conductor.SproutRunReport, error) {
		return conductor.SproutRunReport{
			Outcome:        conductor.SproutOutcomeFailed,
			FailureStage:   core.FailureStageTerrariumPreparation,
			DiagnosticCode: core.DiagnosticCodeTerrariumStartFailed,
		}, nil
	}
	t.Cleanup(func() { runSproutTerrarium = originalRun })

	report, err := sproutOperationsWithOneShotHistory(nil, nil, oneShotHistoryOptions{}).Run(context.Background(), core.SproutSpec{
		StepID:     "step-provenance-copy",
		Transcript: "task",
		Substrate:  "/unused/substrate",
	})
	if err != nil {
		t.Fatalf("execution port: %v", err)
	}
	if report.FailureStage != core.FailureStageTerrariumPreparation || report.DiagnosticCode != core.DiagnosticCodeTerrariumStartFailed {
		t.Fatalf("Core report provenance = %q/%q", report.FailureStage, report.DiagnosticCode)
	}
}

// A named substrate must reach the orchestrator by NAME. The execution plan
// looks the spec up by name to apply its identity, signing, auth and readonly
// settings; substituting the resolved local path leaves nothing to match and
// every one of those settings is skipped in silence — a substrate marked
// readonly would be written to and merged back.
func TestResolveSproutSubstrateWiringKeepsTheNameForNamedSubstrates(t *testing.T) {
	localPath := t.TempDir()
	config := &conductor.SubstratesConfig{
		Substrates: map[string]conductor.SubstrateSpec{
			"demo": {
				Path:   localPath,
				Branch: "main",
				Identity: conductor.IdentitySpec{
					Name:  "OpenTendril Sprout",
					Email: "sprout@opentendril.local",
				},
			},
		},
	}

	wiring := resolveSproutSubstrateWiring(core.SproutSpec{Substrate: "demo"}, config)

	if wiring.Substrate != "demo" {
		t.Fatalf("Substrate = %q, want the name %q — the execution plan resolves configuration by name, so a path here silently drops identity, signing, auth and readonly", wiring.Substrate, "demo")
	}
	if wiring.Substrate == localPath {
		t.Fatalf("Substrate was replaced by the resolved local path %q", localPath)
	}
	if wiring.Branch != "main" {
		t.Fatalf("Branch = %q, want main from the named spec", wiring.Branch)
	}
}

// The other half of this contract — that the plan turns the name back into the
// configured identity — is covered by the conductor package, where the
// execution plan is visible.

// An unnamed substrate is a plain path and must pass through untouched, with a
// status file alongside it.
func TestResolveSproutSubstrateWiringPassesPlainPathsThrough(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	wiring := resolveSproutSubstrateWiring(core.SproutSpec{Substrate: repo}, &conductor.SubstratesConfig{})

	if wiring.Substrate != repo {
		t.Fatalf("Substrate = %q, want the path %q unchanged", wiring.Substrate, repo)
	}
	if wiring.StatusPath == "" {
		t.Fatalf("StatusPath is empty; a local substrate still needs its status file")
	}
}
