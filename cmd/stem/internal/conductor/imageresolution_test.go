package conductor

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// imagePresence is how the fake docker below answers `image inspect`.
type imagePresence int

const (
	imageAbsent imagePresence = iota
	imagePresent
)

// fakeDockerOnPath puts a `docker` ahead of any real one on PATH, answering
// `image inspect` according to presence. Any real build attempt would need a
// daemon, so the tests below must never reach one.
func fakeDockerOnPath(t *testing.T, presence imagePresence) {
	t.Helper()

	inspectExit := "1"
	if presence == imagePresent {
		inspectExit = "0"
	}

	binDir := t.TempDir()
	script := "#!/usr/bin/env bash\n" +
		"if [ \"$1\" = \"image\" ] && [ \"$2\" = \"inspect\" ]; then exit " + inspectExit + "; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestEnsureSproutImageDoesNotMaterializeForAPresentImage is the regression for
// a governed install being unable to grow any Sprout.
//
// The build inputs used to be located from the path this package was COMPILED
// at, and that resolution ran before the presence check — so a deployed Stem
// failed even with nothing to build. The inputs are embedded now, but the
// ordering still matters: an image already present must be satisfied without
// the build inputs being touched at all.
func TestEnsureSproutImageDoesNotMaterializeForAPresentImage(t *testing.T) {
	fakeDockerOnPath(t, imagePresent)

	original := materializeSproutBuildInputsFn
	t.Cleanup(func() { materializeSproutBuildInputsFn = original })

	materialized := false
	materializeSproutBuildInputsFn = func() (string, func(), error) {
		materialized = true
		return "", func() {}, errors.New("build inputs should not have been materialized")
	}

	if err := ensureSproutImage(context.Background(), "opentendril-go:latest"); err != nil {
		t.Fatalf("ensureSproutImage failed for a present image: %v", err)
	}
	if materialized {
		t.Fatal("build inputs were materialized for an image that is already present")
	}
}

// TestEnsureSproutImageDoesNotMaterializeForAnImageItDoesNotBuild guards the
// other early exit: an unrecognised image is not the Stem's to build, so it must
// not pay for the inputs either.
func TestEnsureSproutImageDoesNotMaterializeForAnImageItDoesNotBuild(t *testing.T) {
	fakeDockerOnPath(t, imageAbsent)

	original := materializeSproutBuildInputsFn
	t.Cleanup(func() { materializeSproutBuildInputsFn = original })

	materialized := false
	materializeSproutBuildInputsFn = func() (string, func(), error) {
		materialized = true
		return "", func() {}, nil
	}

	if err := ensureSproutImage(context.Background(), "postgres:16"); err != nil {
		t.Fatalf("ensureSproutImage failed for an image it does not build: %v", err)
	}
	if materialized {
		t.Fatal("build inputs were materialized for an image the Stem does not build")
	}
}

// TestSproutBuildLayoutIsPure pins the mapping, and pins that it asks nothing of
// the filesystem — the property that lets the presence check above stay free.
func TestSproutBuildLayoutIsPure(t *testing.T) {
	testCases := []struct {
		image          string
		wantContext    string
		wantDockerfile string
	}{
		{"opentendril-go:latest", ".", "sprouts/go/Dockerfile"},
		{"opentendril-typescript:latest", ".", "sprouts/typescript/Dockerfile"},
		{"opentendril-node:latest", ".", "sprouts/node/Dockerfile"},
		// The Python image copies relative to its own directory, so its context
		// is that directory rather than the root. Getting this wrong builds an
		// image whose COPY paths cannot resolve.
		{"opentendril-python:latest", "sprouts/python", "sprouts/python/Dockerfile"},
		{verifierImage, ".", "toolchains/go-verifier/Dockerfile"},
		{macrophageFuzzImage, ".", "toolchains/go-fuzz/Dockerfile"},
		{"postgres:16", "", ""},
	}

	for _, testCase := range testCases {
		t.Run(testCase.image, func(t *testing.T) {
			gotContext, gotDockerfile := sproutBuildLayout(testCase.image)
			if gotContext != testCase.wantContext || gotDockerfile != testCase.wantDockerfile {
				t.Fatalf("sproutBuildLayout(%q) = (%q, %q), want (%q, %q)",
					testCase.image, gotContext, gotDockerfile, testCase.wantContext, testCase.wantDockerfile)
			}
		})
	}
}

// TestMaterializeSproutBuildInputsProducesEveryBuildsInputs is the assertion
// that the embedded set is actually sufficient. A missing embed directive is
// invisible until a build fails on a machine that has no checkout — which is
// exactly the failure this whole change exists to remove — so the check is
// derived from the Dockerfiles rather than from a list written beside them.
func TestMaterializeSproutBuildInputsProducesEveryBuildsInputs(t *testing.T) {
	root, cleanup, err := materializeSproutBuildInputs()
	if err != nil {
		t.Fatalf("materializeSproutBuildInputs failed: %v", err)
	}
	defer cleanup()

	for _, image := range []string{
		"opentendril-go:latest",
		"opentendril-typescript:latest",
		"opentendril-node:latest",
		"opentendril-python:latest",
		verifierImage,
		macrophageFuzzImage,
	} {
		buildContext, dockerfile := sproutBuildLayout(image)
		dockerfilePath := filepath.Join(root, filepath.FromSlash(dockerfile))

		payload, readErr := os.ReadFile(dockerfilePath)
		if readErr != nil {
			t.Fatalf("%s: Dockerfile not materialized: %v", image, readErr)
		}

		// Every COPY source that comes from the build context (rather than from
		// an earlier build stage) must exist under the context directory.
		for _, source := range dockerfileContextSources(string(payload)) {
			resolved := filepath.Join(root, filepath.FromSlash(buildContext), filepath.FromSlash(source))
			if _, statErr := os.Stat(resolved); statErr != nil {
				t.Fatalf("%s: COPY %s is not in the embedded build inputs (%v)", image, source, statErr)
			}
		}
	}
}

// dockerfileContextSources returns the COPY sources a Dockerfile takes from its
// build context, skipping `--from=` copies, which come from an earlier stage and
// are not context inputs. The final argument of a COPY is its destination.
func dockerfileContextSources(dockerfile string) []string {
	var sources []string
	for _, line := range strings.Split(dockerfile, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 || !strings.EqualFold(fields[0], "COPY") {
			continue
		}
		arguments := fields[1:]
		if strings.HasPrefix(arguments[0], "--from=") {
			continue
		}
		sources = append(sources, arguments[:len(arguments)-1]...)
	}
	return sources
}

// TestMaterializeSproutBuildInputsCleansUp keeps the temporary tree from
// outliving the build it was written for.
func TestMaterializeSproutBuildInputsCleansUp(t *testing.T) {
	root, cleanup, err := materializeSproutBuildInputs()
	if err != nil {
		t.Fatalf("materializeSproutBuildInputs failed: %v", err)
	}
	if _, statErr := os.Stat(root); statErr != nil {
		t.Fatalf("materialized root is not there: %v", statErr)
	}

	cleanup()

	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("materialized root survived cleanup: stat error = %v", statErr)
	}
}

// TestMaterializeSproutBuildInputsCarriesTheModuleFiles pins the reason this
// package sits at the module root: the Go Sprout image builds against the
// module, so go.mod and go.sum have to travel with it, and only a root package
// can embed them.
func TestMaterializeSproutBuildInputsCarriesTheModuleFiles(t *testing.T) {
	root, cleanup, err := materializeSproutBuildInputs()
	if err != nil {
		t.Fatalf("materializeSproutBuildInputs failed: %v", err)
	}
	defer cleanup()

	for _, name := range []string{"go.mod", "go.sum"} {
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr != nil {
			t.Fatalf("%s missing from the materialized inputs: %v", name, statErr)
		}
	}

	payload, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read materialized go.mod: %v", err)
	}
	if !strings.Contains(string(payload), "module github.com/opentendril/opentendril") {
		t.Fatalf("materialized go.mod is not this module's: %q", string(payload))
	}
}

// TestSproutBuildLayoutPathsAreSlashed keeps the layout in the embedded FS's
// own vocabulary. io/fs paths are always slash-separated; a backslash here would
// resolve on Linux and silently miss on Windows.
func TestSproutBuildLayoutPathsAreSlashed(t *testing.T) {
	for _, image := range []string{
		"opentendril-go:latest",
		"opentendril-python:latest",
		verifierImage,
	} {
		buildContext, dockerfile := sproutBuildLayout(image)
		for _, candidate := range []string{buildContext, dockerfile} {
			if strings.Contains(candidate, "\\") {
				t.Fatalf("%s: layout path %q is not slash-separated", image, candidate)
			}
			if path.IsAbs(candidate) {
				t.Fatalf("%s: layout path %q is absolute; it must be relative to the materialized root", image, candidate)
			}
		}
	}
}
