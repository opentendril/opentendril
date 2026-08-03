package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDockerOnPath puts a `docker` ahead of any real one on PATH. It answers
// `image inspect` with inspectExit and records every invocation, so a test can
// say what the caller asked docker and in what order.
func fakeDockerOnPath(t *testing.T, inspectExit int) *[]string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "invocations.log")
	script := "#!/usr/bin/env bash\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"if [ \"$1\" = \"image\" ] && [ \"$2\" = \"inspect\" ]; then exit " + itoa(inspectExit) + "; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	invocations := &[]string{}
	t.Cleanup(func() {
		payload, err := os.ReadFile(logPath)
		if err != nil {
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
			if line != "" {
				*invocations = append(*invocations, line)
			}
		}
	})

	return invocations
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	return string(rune('0' + value))
}

// TestEnsureSproutImageDoesNotResolveBuildContextForAPresentImage is the
// regression for a governed install being unable to grow any Sprout.
//
// sproutBuildSpec locates the Dockerfiles from the path this package was
// COMPILED at, which exists only inside the tree the binary was built in. It
// was called before the presence check, so a deployed Stem failed even when it
// had nothing to build. The fix is an ordering one, and the assertion has to be
// about ordering: a present image must be satisfied without the build spec
// being consulted at all.
func TestEnsureSproutImageDoesNotResolveBuildContextForAPresentImage(t *testing.T) {
	fakeDockerOnPath(t, 0) // inspect succeeds: the image is present

	original := sproutBuildSpecFn
	t.Cleanup(func() { sproutBuildSpecFn = original })

	// Stands in for what a deployed binary actually gets back: the source tree
	// it was compiled in is not there to be read.
	consulted := false
	sproutBuildSpecFn = func(imageName string) (string, string, error) {
		consulted = true
		return "", "", errors.New("could not locate repository root from /elsewhere/docker.go")
	}

	if err := ensureSproutImage(context.Background(), "opentendril-go:latest"); err != nil {
		t.Fatalf("ensureSproutImage failed for a present image: %v", err)
	}
	if consulted {
		t.Fatal("the build spec was resolved for an image that is already present; a deployed Stem fails here even with nothing to build")
	}
}

// TestEnsureSproutImageStillReportsBuildSpecFailureWhenABuildIsNeeded is the
// other side of the ordering: the resolution failure must still surface when it
// genuinely blocks a build. Without this, moving the presence check could
// silently swallow the error instead of narrowing when it applies.
func TestEnsureSproutImageStillReportsBuildSpecFailureWhenABuildIsNeeded(t *testing.T) {
	fakeDockerOnPath(t, 1) // inspect fails: the image is absent

	original := sproutBuildSpecFn
	t.Cleanup(func() { sproutBuildSpecFn = original })
	sproutBuildSpecFn = func(imageName string) (string, string, error) {
		return "", "", errors.New("could not locate repository root from /elsewhere/docker.go")
	}

	err := ensureSproutImage(context.Background(), "opentendril-go:latest")
	if err == nil {
		t.Fatal("ensureSproutImage succeeded though the image is absent and its build context is unresolvable")
	}
	if !strings.Contains(err.Error(), "could not locate repository root") {
		t.Fatalf("error %q does not carry the resolution failure", err)
	}
}

// TestLocateModuleRootDistinguishesUnreadableFromAbsent pins the second defect.
// The walk treated every os.Stat failure as "no go.mod here", so it stepped
// past the directory that held one and reported it missing. A permission denial
// and an absent file are different answers.
func TestLocateModuleRootDistinguishesUnreadableFromAbsent(t *testing.T) {
	t.Run("absent walks up and reports not located", func(t *testing.T) {
		root := t.TempDir()
		start := filepath.Join(root, "a", "b", "c")
		if err := os.MkdirAll(start, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		_, err := locateModuleRoot(start, "origin.go")
		if err == nil {
			t.Fatal("expected a failure walking a tree with no go.mod")
		}
		if !strings.Contains(err.Error(), "could not locate repository root") {
			t.Fatalf("error %q is not the not-located failure", err)
		}
	})

	t.Run("found is reported before the walk exhausts", func(t *testing.T) {
		root := t.TempDir()
		start := filepath.Join(root, "a", "b")
		if err := os.MkdirAll(start, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}

		got, err := locateModuleRoot(start, "origin.go")
		if err != nil {
			t.Fatalf("locateModuleRoot failed: %v", err)
		}
		if got != root {
			t.Fatalf("locateModuleRoot = %q, want %q", got, root)
		}
	})

	t.Run("unreadable stops the walk and names the reason", func(t *testing.T) {
		root := t.TempDir()
		// A regular file used as a directory component makes os.Stat return
		// ENOTDIR — a non-ErrNotExist failure, the same branch a permission
		// denial takes, without depending on the test's own uid. Running as
		// root would defeat a chmod-based fixture; this holds for any uid.
		barrier := filepath.Join(root, "barrier")
		if err := os.WriteFile(barrier, []byte("not a directory\n"), 0o644); err != nil {
			t.Fatalf("write barrier: %v", err)
		}
		// A go.mod above the barrier, so a walk that treats the failure as
		// "absent" would sail past it and succeed — which is the defect.
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}

		got, err := locateModuleRoot(filepath.Join(barrier, "inner"), "origin.go")
		if err == nil {
			t.Fatalf("locateModuleRoot returned %q; an unreadable component was treated as absent and the walk continued past it", got)
		}
		if !strings.Contains(err.Error(), "could not read") {
			t.Fatalf("error %q does not report the failure as unreadable", err)
		}
		if strings.Contains(err.Error(), "could not locate repository root") {
			t.Fatalf("error %q reports 'not located' for a directory that could not be read", err)
		}
	})
}
