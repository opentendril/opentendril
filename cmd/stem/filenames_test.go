package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGoFilenamesFollowConvention enforces the GUARDRAILS filesystem rule for
// Go source files: merged lowercase, no hyphens, and no underscores except the
// `_test.go` suffix and legitimate platform build-constraint suffixes
// (`_GOOS.go`, `_GOARCH.go`, `_GOOS_GOARCH.go`). Nothing else guarded Go
// filenames, so the `cmd-*.go` kebab-case drift went unnoticed for a long time;
// this test makes any regression a hard failure.
func TestGoFilenamesFollowConvention(t *testing.T) {
	root := moduleRoot(t)

	skipDir := map[string]struct{}{
		".git": {}, ".claude": {}, "vendor": {}, "node_modules": {}, "dist": {}, "testdata": {},
	}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if _, skip := skipDir[entry.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		base := entry.Name()
		if !strings.HasSuffix(base, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)

		if strings.Contains(base, "-") {
			t.Errorf("Go filename must be merged lowercase (no hyphens): %s", rel)
			return nil
		}
		if !strings.Contains(base, "_") {
			return nil
		}
		name := strings.TrimSuffix(base, ".go")

		// Peel the sanctioned suffixes off the end, then judge what is left.
		// Checking only that a name *ends* in `_test` accepted every underscore
		// before it, so `a_b_test.go` passed a rule that forbids it; the same
		// applied to a build-constraint suffix, where only the final token was
		// examined. Peeling first is what makes the check mean what it says.
		name = strings.TrimSuffix(name, "_test")
		parts := strings.Split(name, "_")
		for len(parts) > 1 && isGoBuildToken(parts[len(parts)-1]) {
			parts = parts[:len(parts)-1]
		}
		if len(parts) > 1 {
			t.Errorf("Go filename must be merged lowercase; underscores are only the `_test` suffix or a build-constraint suffix: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
}

// moduleRoot returns the repository/module root (the directory holding go.mod)
// by walking up from this test file.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate go.mod from test file")
		}
		dir = parent
	}
}

// isGoBuildToken reports whether tok is a GOOS or GOARCH value the Go toolchain
// treats as a filename build constraint.
func isGoBuildToken(tok string) bool {
	switch tok {
	// GOOS
	case "aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos",
		"ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris",
		"wasip1", "windows", "zos":
		return true
	// GOARCH
	case "386", "amd64", "amd64p32", "arm", "armbe", "arm64", "arm64be",
		"loong64", "mips", "mipsle", "mips64", "mips64le", "mips64p32",
		"mips64p32le", "ppc", "ppc64", "ppc64le", "riscv", "riscv64", "s390",
		"s390x", "sparc", "sparc64", "wasm":
		return true
	}
	return false
}
