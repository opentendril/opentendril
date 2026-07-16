package conductor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Change-scoped continuous integration, host side: generate a verification
// sequence covering exactly what a change touched. Whole-repository build,
// vet and format checks always run (cheap, and they catch cross-package
// breakage), while tests narrow to the affected packages and their
// transitive reverse-dependents via TestScopeForChanges. Everything inherits
// that function's fail-closed posture: an uncomputable change set or an
// uncertain attribution widens the test step to the whole module, never
// narrows it.

// DefaultScopedVerificationBaseReference is the git reference the change set
// is diffed against when the caller does not name one.
const DefaultScopedVerificationBaseReference = "origin/main"

// listChangedFilesFn is the change-set seam, injectable for tests that drive
// the generator without a real git repository.
var listChangedFilesFn = listChangedFiles

// listChangedFiles returns the files changed between the merge base of
// baseReference and HEAD, via `git diff --name-only <base>...HEAD` — the
// three-dot form, so commits already on the base branch never count as part
// of the change.
func listChangedFiles(ctx context.Context, moduleRoot, baseReference string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "diff", "--name-only", baseReference+"...HEAD")
	command.Dir = moduleRoot
	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("git diff failed: %v: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("git diff failed: %w", err)
	}
	var changedFiles []string
	for _, line := range strings.Split(string(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			changedFiles = append(changedFiles, line)
		}
	}
	return changedFiles, nil
}

// gofmtCheckScript makes the format check an honest verifier step: plain
// `gofmt -l` exits 0 whether or not files need formatting — it only *prints*
// the offenders — so a bare exit-code verdict over it would falsely pass.
// The script captures the listing and exits 1 when it is non-empty, naming
// the unformatted files in the step output.
const gofmtCheckScript = `unformatted="$(gofmt -l .)"; if [ -n "$unformatted" ]; then echo "unformatted Go files:"; echo "$unformatted"; exit 1; fi`

// GenerateScopedVerificationSequence assembles the scoped-ci sequence for the
// module rooted at moduleRoot, diffing against baseReference (defaulting to
// DefaultScopedVerificationBaseReference when blank).
//
// The generated steps, chained linearly (the sequence runs with a
// concurrency limit of 1, so a chain buys deterministic cheap-first ordering
// at no cost):
//
//  1. `go build ./...`  — whole repository, catches cross-package breakage;
//  2. `go vet ./...`    — whole repository, same rationale;
//  3. the gofmt check   — see gofmtCheckScript;
//  4. `go test -json -short <scope>` — the affected packages, or `./...` on
//     the fail-closed whole-module fallback, or NO step at all when the
//     change is known inert (for example documentation only).
//
// The test step is a single invocation rather than a per-package fan-out:
// SequenceStep.Parallel means parallel LLM sprouting, not command fan-out,
// and `go test` already parallelizes across packages internally. `-json`
// opts the step into the skip-aware verdict (see reportGoTestVerifier), and
// `-short` follows the sealed verifier convention:
// tests that need the network or a Docker daemon gate on testing.Short(), so
// under the seal they surface as skips — which the verdict reports as
// blocked/unverified — instead of failures that blame the code for the seal.
//
// Fail closed: when the change set itself cannot be computed, the test step
// covers the whole module.
func GenerateScopedVerificationSequence(ctx context.Context, moduleRoot, baseReference string) (*Sequence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(moduleRoot) == "" {
		return nil, fmt.Errorf("scoped verification module root is required")
	}
	if strings.TrimSpace(baseReference) == "" {
		baseReference = DefaultScopedVerificationBaseReference
	}

	var packagePatterns []string
	wholeModule := false
	changedFiles, diffErr := listChangedFilesFn(ctx, moduleRoot, baseReference)
	if diffErr != nil {
		// Fail closed: without a trustworthy change set every narrowing
		// decision would be a guess, so the whole module is tested.
		wholeModule = true
	} else {
		var scopeErr error
		packagePatterns, wholeModule, scopeErr = TestScopeForChanges(ctx, moduleRoot, changedFiles)
		if scopeErr != nil {
			return nil, scopeErr
		}
	}

	steps := []SequenceStep{
		{
			ID:      "verifier-build",
			Status:  sequenceStatusPending,
			Command: []string{"go", "build", "./..."},
		},
		{
			ID:        "verifier-vet",
			Status:    sequenceStatusPending,
			DependsOn: []string{"verifier-build"},
			Command:   []string{"go", "vet", "./..."},
		},
		{
			ID:        "verifier-gofmt",
			Status:    sequenceStatusPending,
			DependsOn: []string{"verifier-vet"},
			Command:   []string{"sh", "-c", gofmtCheckScript},
		},
	}
	testPatterns := packagePatterns
	if wholeModule {
		testPatterns = []string{"./..."}
	}
	if len(testPatterns) > 0 {
		steps = append(steps, SequenceStep{
			ID:        "verifier-test",
			Status:    sequenceStatusPending,
			DependsOn: []string{"verifier-gofmt"},
			Command:   append([]string{"go", "test", "-json", "-short"}, testPatterns...),
		})
	}
	// An empty scope with no whole-module fallback means every changed file
	// is known inert: build, vet and format still run, but there is nothing
	// to test.

	return &Sequence{
		Name:             "scoped-ci",
		ConcurrencyLimit: 1,
		OnFailure:        sequenceOnFailureHalt,
		Steps:            steps,
	}, nil
}
