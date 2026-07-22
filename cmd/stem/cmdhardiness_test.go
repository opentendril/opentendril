package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Executable-integrity findings.
//
// The property under test is that nobody except the owner can replace what the
// Stem runs. These tests construct the exposures directly rather than asserting
// on the host's real layout, so they measure the check rather than the machine
// that happens to run them.

// cleanTempRoot returns a temporary directory whose whole chain is free of
// group- and other-write permission.
//
// This is necessary rather than fussy. Go creates each t.TempDir subdirectory
// with mode 0777, so under a permissive umask — 0002 is the default on several
// distributions — the fixture arrives group-writable and every test would see an
// exposure it did not create. Narrowing the directory and its parent makes these
// tests measure the check instead of the umask of whoever runs them.
func cleanTempRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for _, path := range []string{filepath.Dir(root), root} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("narrow %s: %v", path, err)
		}
	}
	return root
}

// newExecutable builds a directory holding a fake binary and returns its path.
// The directory is created 0755 so a test can widen exactly the one path it is
// about, leaving everything else clean.
func newExecutable(t *testing.T) (dir string, executable string) {
	t.Helper()

	dir = filepath.Join(cleanTempRoot(t), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("narrow bin dir: %v", err)
	}
	executable = filepath.Join(dir, "tendril")
	if err := os.WriteFile(executable, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatalf("narrow executable: %v", err)
	}
	return dir, executable
}

func TestExecutableIntegrityCleanChainIsOK(t *testing.T) {
	_, executable := newExecutable(t)

	finding := executableIntegrityFindingFor(executable)

	if finding.Severity != "ok" {
		t.Fatalf("severity = %q, want ok (detail: %s)", finding.Severity, finding.Detail)
	}
	if !strings.Contains(finding.Title, executable) {
		t.Errorf("title should name the binary examined, got %q", finding.Title)
	}
}

func TestExecutableIntegrityDetectsGroupWritableBinary(t *testing.T) {
	_, executable := newExecutable(t)
	if err := os.Chmod(executable, 0o775); err != nil {
		t.Fatalf("chmod executable: %v", err)
	}

	finding := executableIntegrityFindingFor(executable)

	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak", finding.Severity)
	}
	if !strings.Contains(finding.Detail, executable) {
		t.Errorf("detail should name the offending path, got:\n%s", finding.Detail)
	}
	if !strings.Contains(finding.Detail, "group-writable") {
		t.Errorf("detail should say why the path failed, got:\n%s", finding.Detail)
	}
}

func TestExecutableIntegrityDetectsWorldWritableAncestor(t *testing.T) {
	dir, executable := newExecutable(t)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}

	finding := executableIntegrityFindingFor(executable)

	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak", finding.Severity)
	}
	// The directory is the exposure even though the binary itself is 0755:
	// replacing a file needs write permission on its directory.
	if !strings.Contains(finding.Detail, dir) {
		t.Errorf("detail should name the writable ancestor %q, got:\n%s", dir, finding.Detail)
	}
	if !strings.Contains(finding.Detail, "world-writable") {
		t.Errorf("detail should say why, got:\n%s", finding.Detail)
	}
}

func TestExecutableIntegrityFollowsSymlinkIntoWritableDirectory(t *testing.T) {
	realDir, realExecutable := newExecutable(t)
	if err := os.Chmod(realDir, 0o777); err != nil {
		t.Fatalf("chmod real dir: %v", err)
	}

	linkDir := filepath.Join(cleanTempRoot(t), "sbin")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatalf("create link dir: %v", err)
	}
	if err := os.Chmod(linkDir, 0o755); err != nil {
		t.Fatalf("narrow link dir: %v", err)
	}
	link := filepath.Join(linkDir, "tendril")
	if err := os.Symlink(realExecutable, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	finding := executableIntegrityFindingFor(link)

	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak — the link's own directory is clean, but its target's is not", finding.Severity)
	}
	if !strings.Contains(finding.Detail, realDir) {
		t.Errorf("detail should name the target's writable directory %q, got:\n%s", realDir, finding.Detail)
	}
}

// A symbolic link's own permission bits are 0777 on Linux and mean nothing.
// Judging the link by its own mode would report an exposure on every system,
// so the link is skipped and its directory and target carry the verdict.
func TestExecutableIntegrityIgnoresSymlinkOwnPermissions(t *testing.T) {
	_, realExecutable := newExecutable(t)

	linkDir := filepath.Join(cleanTempRoot(t), "sbin")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatalf("create link dir: %v", err)
	}
	if err := os.Chmod(linkDir, 0o755); err != nil {
		t.Fatalf("narrow link dir: %v", err)
	}
	link := filepath.Join(linkDir, "tendril")
	if err := os.Symlink(realExecutable, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	finding := executableIntegrityFindingFor(link)

	if finding.Severity != "ok" {
		t.Fatalf("severity = %q, want ok (detail: %s)", finding.Severity, finding.Detail)
	}
}

// A world-writable directory that is sticky does not permit replacing another
// user's file, which is the whole point of the sticky bit. Reporting it would
// flag every shared temporary directory, wrongly.
func TestExecutableIntegrityTreatsStickyDirectoryAsSafe(t *testing.T) {
	dir, executable := newExecutable(t)
	if err := os.Chmod(dir, os.FileMode(0o777)|os.ModeSticky); err != nil {
		t.Fatalf("chmod sticky: %v", err)
	}

	finding := executableIntegrityFindingFor(executable)

	if finding.Severity != "ok" {
		t.Fatalf("severity = %q, want ok — a sticky directory blocks replacement (detail: %s)", finding.Severity, finding.Detail)
	}
}

// An absent path is indeterminate, never a pass. The RFC's negative requirement
// is that a check which could not be performed must not read as hardy.
func TestExecutableIntegrityMissingPathIsNotAPass(t *testing.T) {
	missing := filepath.Join(cleanTempRoot(t), "absent", "tendril")

	finding := executableIntegrityFindingFor(missing)

	if finding.Severity == "ok" {
		t.Fatalf("severity = ok for a path that does not exist; an unexaminable path must never pass")
	}
	if !strings.Contains(finding.Detail, "not a pass") {
		t.Errorf("detail should say the result is not a pass, got:\n%s", finding.Detail)
	}
}

func TestExecutableResolutionChainIncludesAncestorsAndTargets(t *testing.T) {
	realDir, realExecutable := newExecutable(t)

	linkDir := filepath.Join(cleanTempRoot(t), "sbin")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatalf("create link dir: %v", err)
	}
	if err := os.Chmod(linkDir, 0o755); err != nil {
		t.Fatalf("narrow link dir: %v", err)
	}
	link := filepath.Join(linkDir, "tendril")
	if err := os.Symlink(realExecutable, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	inspected, unresolved := executableResolutionChain(link)
	if len(unresolved) != 0 {
		t.Fatalf("unresolved = %v, want none", unresolved)
	}

	for _, want := range []string{link, linkDir, realExecutable, realDir, "/"} {
		if !containsPath(inspected, want) {
			t.Errorf("resolution chain missing %q\nchain: %v", want, inspected)
		}
	}
}

func TestExecutableResolutionChainReportsBrokenLink(t *testing.T) {
	linkDir := cleanTempRoot(t)
	link := filepath.Join(linkDir, "tendril")
	if err := os.Symlink(filepath.Join(linkDir, "gone"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, unresolved := executableResolutionChain(link)

	if len(unresolved) == 0 {
		t.Fatal("a broken link must be reported as unresolved rather than ignored")
	}
}

// The report informs; it does not gate. Adding a finding must not give the
// command an exit status, so it has to return normally whatever it finds.
func TestHardinessReportReturnsWithoutExiting(t *testing.T) {
	tendrilDir := t.TempDir()

	// A weak posture: this user owns the control-plane directory. If the
	// command ever gained a non-zero exit, this is where it would take it.
	runHardinessCmd(context.Background(), nil)
	findings := collectHardinessFindings(context.Background(), tendrilDir)

	if len(findings) == 0 {
		t.Fatal("expected findings, got none")
	}
	weak := 0
	for _, finding := range findings {
		if finding.Severity == "weak" {
			weak++
		}
	}
	if weak == 0 {
		t.Fatal("expected at least one weak finding for a self-owned control plane")
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
