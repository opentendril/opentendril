package conductor

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// syntheticChangeSet installs a fixed change set as the git-diff seam for the
// duration of the test and records the base reference the generator asked
// for.
func syntheticChangeSet(t *testing.T, changedFiles []string, diffErr error) *string {
	t.Helper()
	original := listChangedFilesFn
	t.Cleanup(func() { listChangedFilesFn = original })
	var gotBase string
	listChangedFilesFn = func(ctx context.Context, moduleRoot, baseReference string) ([]string, error) {
		gotBase = baseReference
		return changedFiles, diffErr
	}
	return &gotBase
}

// stepByID finds a generated step, failing the test when it is absent.
func stepByID(t *testing.T, seq *Sequence, id string) *SequenceStep {
	t.Helper()
	for index := range seq.Steps {
		if seq.Steps[index].ID == id {
			return &seq.Steps[index]
		}
	}
	t.Fatalf("sequence has no step %q (steps: %v)", id, seq.Steps)
	return nil
}

// assertScopedBaseSteps checks the three whole-repository steps every
// generated sequence must carry: build, vet, and a format check that
// actually fails on unformatted files.
func assertScopedBaseSteps(t *testing.T, seq *Sequence) {
	t.Helper()
	build := stepByID(t, seq, "verifier-build")
	if strings.Join(build.Command, " ") != "go build ./..." {
		t.Fatalf("build step command = %v", build.Command)
	}
	vet := stepByID(t, seq, "verifier-vet")
	if strings.Join(vet.Command, " ") != "go vet ./..." {
		t.Fatalf("vet step command = %v", vet.Command)
	}
	gofmtStep := stepByID(t, seq, "verifier-gofmt")
	if len(gofmtStep.Command) != 3 || gofmtStep.Command[0] != "sh" || gofmtStep.Command[1] != "-c" {
		t.Fatalf("gofmt step must run through a shell so it can fail on output: %v", gofmtStep.Command)
	}
	// `gofmt -l` exits 0 even when files need formatting, so the script must
	// carry an explicit exit 1 tied to a non-empty listing.
	script := gofmtStep.Command[2]
	if !strings.Contains(script, "gofmt -l") || !strings.Contains(script, "exit 1") {
		t.Fatalf("gofmt script does not fail closed on unformatted files: %q", script)
	}
	// gofmt is not module-aware: handed a directory it recurses everything
	// under it, including nested checkouts of other modules that happen to sit
	// in the workspace. Formatting the module's own packages, as reported by
	// go list, is what keeps the step reporting on the code under test.
	if strings.Contains(script, "gofmt -l .") {
		t.Fatalf("gofmt script walks the workspace instead of the module's packages: %q", script)
	}
	if !strings.Contains(script, "go list") {
		t.Fatalf("gofmt script must derive its file set from go list: %q", script)
	}
	// An unusable package list must stop the step rather than let a gofmt that
	// examined nothing report success.
	if !strings.Contains(script, "no packages to format") {
		t.Fatalf("gofmt script does not fail closed on an empty package list: %q", script)
	}
}

// A change to a low-level package must generate a single test step covering
// exactly that package plus its transitive reverse-dependents, alongside the
// whole-repository build/vet/gofmt steps.
func TestGenerateScopedSequenceForScopedChange(t *testing.T) {
	syntheticModuleGraph(t)
	syntheticChangeSet(t, []string{"internal/core/thing.go"}, nil)

	seq, err := GenerateScopedVerificationSequence(context.Background(), "/module", "")
	if err != nil {
		t.Fatalf("generate returned error: %v", err)
	}
	if seq.Name != "scoped-ci" || seq.OnFailure != sequenceOnFailureHalt {
		t.Fatalf("sequence header = %q/%q", seq.Name, seq.OnFailure)
	}
	assertScopedBaseSteps(t, seq)

	testStep := stepByID(t, seq, "verifier-test")
	want := "go test -json -short " +
		"example.com/scope/cmd/stem " +
		"example.com/scope/internal/core " +
		"example.com/scope/internal/probe " +
		"example.com/scope/internal/receptors"
	if got := strings.Join(testStep.Command, " "); got != want {
		t.Fatalf("test step command = %q, want %q", got, want)
	}
	if len(seq.Steps) != 4 {
		t.Fatalf("expected exactly 4 steps, got %d", len(seq.Steps))
	}
}

// A change set the generator cannot compute must fail closed to the whole
// module: `go test -json -short ./...`, never a silently narrowed run.
func TestGenerateScopedSequenceFailsClosedWhenDiffFails(t *testing.T) {
	syntheticModuleGraph(t)
	syntheticChangeSet(t, nil, fmt.Errorf("synthetic git failure"))

	seq, err := GenerateScopedVerificationSequence(context.Background(), "/module", "")
	if err != nil {
		t.Fatalf("generate returned error: %v", err)
	}
	assertScopedBaseSteps(t, seq)
	testStep := stepByID(t, seq, "verifier-test")
	if got := strings.Join(testStep.Command, " "); got != "go test -json -short ./..." {
		t.Fatalf("test step command = %q, want whole-module fallback", got)
	}
}

// A module definition change widens the scope to the whole module through
// TestScopeForChanges, and the generator must honor it.
func TestGenerateScopedSequenceWholeModuleForModuleDefinitionChange(t *testing.T) {
	syntheticModuleGraph(t)
	syntheticChangeSet(t, []string{"go.mod"}, nil)

	seq, err := GenerateScopedVerificationSequence(context.Background(), "/module", "")
	if err != nil {
		t.Fatalf("generate returned error: %v", err)
	}
	testStep := stepByID(t, seq, "verifier-test")
	if got := strings.Join(testStep.Command, " "); got != "go test -json -short ./..." {
		t.Fatalf("test step command = %q, want whole-module fallback", got)
	}
}

// A known-inert change (documentation only) yields an empty scope: build,
// vet and gofmt still run, but no test step is generated at all.
func TestGenerateScopedSequenceEmptyScopeOmitsTestStep(t *testing.T) {
	syntheticModuleGraph(t)
	syntheticChangeSet(t, []string{"README.md", "docs/design.md"}, nil)

	seq, err := GenerateScopedVerificationSequence(context.Background(), "/module", "")
	if err != nil {
		t.Fatalf("generate returned error: %v", err)
	}
	assertScopedBaseSteps(t, seq)
	if len(seq.Steps) != 3 {
		t.Fatalf("expected exactly build/vet/gofmt, got %d steps: %v", len(seq.Steps), seq.Steps)
	}
	for _, step := range seq.Steps {
		if step.ID == "verifier-test" {
			t.Fatalf("inert change must not generate a test step")
		}
	}
}

// The base reference defaults to origin/main and an explicit override is
// passed through to the change-set seam verbatim.
func TestGenerateScopedSequenceBaseReference(t *testing.T) {
	syntheticModuleGraph(t)
	gotBase := syntheticChangeSet(t, []string{"internal/isolated/only.go"}, nil)

	if _, err := GenerateScopedVerificationSequence(context.Background(), "/module", ""); err != nil {
		t.Fatalf("generate returned error: %v", err)
	}
	if *gotBase != DefaultScopedVerificationBaseReference {
		t.Fatalf("default base = %q, want %q", *gotBase, DefaultScopedVerificationBaseReference)
	}

	if _, err := GenerateScopedVerificationSequence(context.Background(), "/module", "release/v2"); err != nil {
		t.Fatalf("generate returned error: %v", err)
	}
	if *gotBase != "release/v2" {
		t.Fatalf("override base = %q, want %q", *gotBase, "release/v2")
	}
}

// The generated sequence must be valid by the sequence engine's own rules
// (step identifiers, dependency edges, onFailure mode), so it can be saved
// and run through the standard machinery unmodified.
func TestGenerateScopedSequenceRoundTripsThroughSave(t *testing.T) {
	syntheticModuleGraph(t)
	syntheticChangeSet(t, []string{"internal/core/thing.go"}, nil)

	seq, err := GenerateScopedVerificationSequence(context.Background(), "/module", "")
	if err != nil {
		t.Fatalf("generate returned error: %v", err)
	}
	path := t.TempDir() + "/scoped-ci.yaml"
	if err := SaveSequence(path, seq); err != nil {
		t.Fatalf("generated sequence failed to save: %v", err)
	}
	loaded, err := LoadSequence(path)
	if err != nil {
		t.Fatalf("generated sequence failed to load: %v", err)
	}
	if len(loaded.Steps) != len(seq.Steps) {
		t.Fatalf("round-trip changed step count: %d != %d", len(loaded.Steps), len(seq.Steps))
	}
}

// GenerateScopedVerificationSequence rejects a missing module root: with no
// module there is nothing meaningful to verify.
func TestGenerateScopedSequenceRequiresModuleRoot(t *testing.T) {
	if _, err := GenerateScopedVerificationSequence(context.Background(), "  ", ""); err == nil {
		t.Fatal("expected an error for a blank module root")
	}
}
