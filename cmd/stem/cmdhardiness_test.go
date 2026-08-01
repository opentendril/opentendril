package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
)

// Executable-integrity findings. Exposures are constructed directly so these
// measure the check rather than the machine running them.

// cleanTempRoot returns a temporary directory whose whole chain is free of group-
// and other-write permission. Go creates t.TempDir subdirectories 0777, so under
// a permissive umask the fixture would otherwise arrive group-writable.
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

// A symbolic link's own permission bits are 0777 on Linux, so the link is skipped
// and its directory and target carry the verdict.
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

// A sticky directory does not permit replacing another user's file, even when
// world-writable.
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

// An absent path is indeterminate, never a pass.
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
	for _, finding := range findings {
		switch finding.Severity {
		case "ok", "note", "weak":
		default:
			t.Errorf("finding %q has severity %q, which the report cannot render", finding.Title, finding.Severity)
		}
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

// Host-execution configuration exposure. These run in a temporary working
// directory so the loader's candidate paths are the fixture's.

// inCleanWorkingDir runs fn with the process working directory set to a
// permission-narrowed temporary directory, restoring it afterwards.
func inCleanWorkingDir(t *testing.T, fn func(dir string)) {
	t.Helper()

	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cleanTempRoot(t)
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	// Resolve symlinks so comparisons against loader output match on platforms
	// where the temporary directory is itself a link.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	fn(resolved)
}

func writeSubstrates(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "substrates.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write substrates: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod substrates: %v", err)
	}
	return path
}

const dockerSubstrate = "substrates:\n  core:\n    path: .\n"
const hostSubstrate = "substrates:\n  codex-host:\n    provider: host\n    path: .\n"

func TestHostConfigCleanIsOK(t *testing.T) {
	inCleanWorkingDir(t, func(dir string) {
		writeSubstrates(t, dir, dockerSubstrate, 0o644)
		finding := hostExecutionConfigFinding()
		if finding.Severity != "ok" {
			t.Fatalf("severity = %q, want ok (detail: %s)", finding.Severity, finding.Detail)
		}
	})
}

func TestHostConfigAbsentIsOK(t *testing.T) {
	inCleanWorkingDir(t, func(dir string) {
		finding := hostExecutionConfigFinding()
		if finding.Severity != "ok" {
			t.Fatalf("severity = %q, want ok with no configuration present", finding.Severity)
		}
	})
}

// Exposure alone is information, not an escape route.
func TestHostConfigGroupWritableIsANote(t *testing.T) {
	inCleanWorkingDir(t, func(dir string) {
		path := writeSubstrates(t, dir, dockerSubstrate, 0o664)
		finding := hostExecutionConfigFinding()
		if finding.Severity != "note" {
			t.Fatalf("severity = %q, want note", finding.Severity)
		}
		if !strings.Contains(finding.Detail, path) {
			t.Errorf("detail should name the writable file, got:\n%s", finding.Detail)
		}
		if !strings.Contains(finding.Detail, "group-writable") {
			t.Errorf("detail should say why, got:\n%s", finding.Detail)
		}
	})
}

func TestHostConfigWorldWritableIsANote(t *testing.T) {
	inCleanWorkingDir(t, func(dir string) {
		writeSubstrates(t, dir, dockerSubstrate, 0o666)
		finding := hostExecutionConfigFinding()
		if finding.Severity != "note" {
			t.Fatalf("severity = %q, want note", finding.Severity)
		}
		if !strings.Contains(finding.Detail, "world-writable") {
			t.Errorf("detail should say why, got:\n%s", finding.Detail)
		}
	})
}

// Exposure plus an open runtime gate is the escape route, and is the case that
// must read as weak.
func TestHostConfigWritableWithGateOpenIsWeak(t *testing.T) {
	t.Setenv(terrariumAllowHostExecutionEnv, "true")
	inCleanWorkingDir(t, func(dir string) {
		writeSubstrates(t, dir, dockerSubstrate, 0o666)
		finding := hostExecutionConfigFinding()
		if finding.Severity != "weak" {
			t.Fatalf("severity = %q, want weak when the gate is open", finding.Severity)
		}
		if !strings.Contains(finding.Title, "host execution is enabled") {
			t.Errorf("title should name the gate, got %q", finding.Title)
		}
	})
}

// A declared host substrate is weak on its own: the gate's state cannot be
// established from an arbitrary invocation and must not be assumed shut.
func TestHostConfigWritableWithHostSubstrateIsWeak(t *testing.T) {
	inCleanWorkingDir(t, func(dir string) {
		writeSubstrates(t, dir, hostSubstrate, 0o664)
		finding := hostExecutionConfigFinding()
		if finding.Severity != "weak" {
			t.Fatalf("severity = %q, want weak when a host substrate is declared", finding.Severity)
		}
	})
}

// An unset variable means "not visible from here", never "not set".
func TestHostExecutionGateStateDistinguishesUnsetFromFalse(t *testing.T) {
	t.Setenv(terrariumAllowHostExecutionEnv, "")
	if _, known := hostExecutionGateState(); !known {
		t.Error("an explicitly empty variable is present and therefore known")
	}

	os.Unsetenv(terrariumAllowHostExecutionEnv)
	open, known := hostExecutionGateState()
	if known {
		t.Error("an absent variable must report as unknown, not as a closed gate")
	}
	if open {
		t.Error("an unknown gate must not report as open")
	}
}

// Control-plane reachability. Trusted definitions are trusted because a Sprout
// cannot write them; a control plane inside a repository can be written.

func TestControlPlaneOutsideRepositoryIsOK(t *testing.T) {
	root := cleanTempRoot(t)
	finding := controlPlaneReachabilityFinding(filepath.Join(root, ".tendril"))
	if finding.Severity != "ok" {
		t.Fatalf("severity = %q, want ok (detail: %s)", finding.Severity, finding.Detail)
	}
}

func TestControlPlaneInsideRepositoryIsWeak(t *testing.T) {
	root := cleanTempRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	controlPlane := filepath.Join(root, ".tendril")

	finding := controlPlaneReachabilityFinding(controlPlane)

	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak", finding.Severity)
	}
	if !strings.Contains(finding.Detail, root) {
		t.Errorf("detail should name the repository, got:\n%s", finding.Detail)
	}
}

// A worktree's .git is a file, not a directory, and must count the same.
func TestControlPlaneInsideWorktreeIsWeak(t *testing.T) {
	root := cleanTempRoot(t)
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	finding := controlPlaneReachabilityFinding(filepath.Join(root, ".tendril"))
	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak for a worktree", finding.Severity)
	}
}

// A repository several levels up still reaches the control plane.
func TestControlPlaneInNestedDirectoryIsWeak(t *testing.T) {
	root := cleanTempRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	finding := controlPlaneReachabilityFinding(filepath.Join(nested, ".tendril"))
	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak for a nested control plane", finding.Severity)
	}
}

// Cross-account executable integrity: the Stem records which binary it is, so an
// account that runs a different one can still measure the right file.

func TestExecutableIntegrityUsesTheRecordedStemBinary(t *testing.T) {
	root := cleanTempRoot(t)
	tendrilDir := filepath.Join(root, ".tendril")

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stemBinary := filepath.Join(bin, "tendril")
	if err := os.WriteFile(stemBinary, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	// Set the mode explicitly: WriteFile's permission argument is masked by the
	// process umask, which differs between a developer machine and CI.
	if err := os.Chmod(stemBinary, 0o777); err != nil {
		t.Fatalf("chmod binary: %v", err)
	}
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir control plane: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tendrilDir, stemIdentityFilename),
		[]byte(`{"executable":"`+stemBinary+`","uid":1001}`+"\n"), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}

	finding := executableIntegrityFinding(tendrilDir, "/proc")

	if !strings.Contains(finding.Title, "The Stem's binary") {
		t.Errorf("finding should say it measured the Stem's binary, got %q", finding.Title)
	}
	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak — the recorded binary is world-writable", finding.Severity)
	}
	if !strings.Contains(finding.Detail, stemBinary) {
		t.Errorf("detail should name the recorded binary, got:\n%s", finding.Detail)
	}
}

// Without a readable record the finding still reports, and says what it measured.
func TestExecutableIntegritySaysWhenItCouldNotReadTheRecord(t *testing.T) {
	tendrilDir := filepath.Join(cleanTempRoot(t), ".tendril")

	finding := executableIntegrityFinding(tendrilDir, "/proc")

	if strings.Contains(finding.Title, "The Stem's binary") {
		t.Error("no record exists, so the finding must not claim to have measured the Stem's binary")
	}
	if !strings.Contains(finding.Detail, "not necessarily the Stem's") {
		t.Errorf("detail should say which binary it measured, got:\n%s", finding.Detail)
	}
}

func TestRecordedIdentityRoundTrips(t *testing.T) {
	tendrilDir := filepath.Join(cleanTempRoot(t), ".tendril")
	if err := recordStemIdentity(tendrilDir); err != nil {
		t.Fatalf("record: %v", err)
	}

	identity, ok := readStemIdentity(tendrilDir)
	if !ok {
		t.Fatal("the record just written was not readable")
	}
	if identity.Executable == "" {
		t.Error("the record carries no executable path")
	}
	if identity.UID != os.Getuid() {
		t.Errorf("uid = %d, want %d", identity.UID, os.Getuid())
	}

	info, err := os.Stat(stemIdentityPath(tendrilDir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// An owner can always write its own file, so a binary belonging to another
// principal is replaceable however narrow its mode.
func TestExecutableOwnedByAnotherPrincipalIsWeak(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping test as root: hardiness check ignores root-owned paths by design")
	}

	root := cleanTempRoot(t)
	tendrilDir := filepath.Join(root, ".tendril")

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stemBinary := filepath.Join(bin, "tendril")
	if err := os.WriteFile(stemBinary, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.Chmod(stemBinary, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir control plane: %v", err)
	}

	// The record claims a Stem uid this test's files do not belong to.
	foreign := os.Getuid() + 1
	if err := os.WriteFile(filepath.Join(tendrilDir, stemIdentityFilename),
		[]byte(fmt.Sprintf(`{"executable":%q,"uid":%d}`+"\n", stemBinary, foreign)), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}

	finding := executableIntegrityFinding(tendrilDir, "/proc")

	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak — the binary belongs to another principal", finding.Severity)
	}
	if !strings.Contains(finding.Detail, "not the Stem") {
		t.Errorf("detail should name the foreign owner, got:\n%s", finding.Detail)
	}
}

// With no recorded uid, only modes are judged — the previous behaviour.
func TestOwnershipUnknownJudgesModesOnly(t *testing.T) {
	_, executable := newExecutable(t)
	finding := executableIntegrityFindingFor(executable)
	if finding.Severity != "ok" {
		t.Fatalf("severity = %q, want ok when ownership cannot be compared", finding.Severity)
	}
}

// Running-image findings. The Stem records which process it is; the report
// resolves that process's image and compares it to the binary on disk. These
// build a stand-in procfs tree so the comparison is exercised without depending
// on a live process.

// newProcfs builds a stand-in procfs holding one process directory whose exe
// link points at image, and returns the tree root and the directory's mtime.
//
// The link is created before the mtime is read: writing inside the directory
// updates it, so reading first would record a value the report never sees.
func newProcfs(t *testing.T, pid int, image string) (root string, start time.Time) {
	t.Helper()

	root = t.TempDir()
	procDir := filepath.Join(root, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatalf("create process dir: %v", err)
	}
	if err := os.Symlink(image, filepath.Join(procDir, "exe")); err != nil {
		t.Fatalf("link exe: %v", err)
	}
	info, err := os.Stat(procDir)
	if err != nil {
		t.Fatalf("stat process dir: %v", err)
	}
	return root, info.ModTime()
}

// writeIdentity records an identity naming executable, uid, pid and start.
func writeIdentity(t *testing.T, tendrilDir, executable string, uid, pid int, start time.Time) {
	t.Helper()

	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir control plane: %v", err)
	}
	payload, err := json.Marshal(stemIdentity{Executable: executable, UID: uid, PID: pid, StartTime: start})
	if err != nil {
		t.Fatalf("encode identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tendrilDir, stemIdentityFilename), append(payload, '\n'), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
}

// requireProcfsSupport skips where the running image cannot be resolved at all.
// The report only attempts it on Linux, so elsewhere there is no behaviour here
// to measure — as distinct from a behaviour that passes.
func requireProcfsSupport(t *testing.T) {
	t.Helper()

	if runtime.GOOS != "linux" {
		t.Skip("the running image is resolved through procfs, which this platform does not provide")
	}
}

// TestRunningImageDivergenceIsReported: a Stem still executing a replaced
// binary is the state this finding exists to surface. Naming only the path on
// disk would describe a file nothing is running.
func TestRunningImageDivergenceIsReported(t *testing.T) {
	requireProcfsSupport(t)

	_, onDisk := newExecutable(t)
	running := onDisk + ".previous"
	const pid = 4242
	procfs, start := newProcfs(t, pid, running)

	tendrilDir := filepath.Join(filepath.Dir(filepath.Dir(onDisk)), ".tendril")
	writeIdentity(t, tendrilDir, onDisk, os.Getuid(), pid, start)

	finding := executableIntegrityFinding(tendrilDir, procfs)

	if !finding.Diverged {
		t.Errorf("Diverged = false while the running image %q differs from the binary on disk %q", running, onDisk)
	}
	if !strings.Contains(finding.Detail, running) {
		t.Errorf("detail does not name the running image %q, so the report describes only the file on disk:\n%s", running, finding.Detail)
	}
	if !strings.Contains(finding.Title, "diverges") {
		t.Errorf("title does not carry the divergence, so a reader scanning titles misses it: %q", finding.Title)
	}
}

// TestRunningImageAgreementIsReported: the ordinary state must not be flagged,
// or the divergence note stops meaning anything.
func TestRunningImageAgreementIsReported(t *testing.T) {
	requireProcfsSupport(t)

	_, onDisk := newExecutable(t)
	const pid = 4243
	procfs, start := newProcfs(t, pid, onDisk)

	tendrilDir := filepath.Join(filepath.Dir(filepath.Dir(onDisk)), ".tendril")
	writeIdentity(t, tendrilDir, onDisk, os.Getuid(), pid, start)

	finding := executableIntegrityFinding(tendrilDir, procfs)

	if finding.Diverged {
		t.Errorf("Diverged = true while the running image and the binary on disk are both %q", onDisk)
	}
	if !strings.Contains(finding.Detail, "matches the binary on disk") {
		t.Errorf("detail does not state that the images agree:\n%s", finding.Detail)
	}
}

// TestUnestablishedRunningImageLeavesSeverityAlone: whether the running image
// could be resolved says nothing about whether another account can replace the
// binary on disk. Coupling them reported a proven exposure as a note, and a
// report with no weak findings states that nothing measurable is wrong.
//
// A record carrying no process at all is not a corner case: it is what every
// installation holds until the Stem restarts after an upgrade.
func TestUnestablishedRunningImageLeavesSeverityAlone(t *testing.T) {
	requireProcfsSupport(t)

	dir, onDisk := newExecutable(t)
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatalf("widen bin dir: %v", err)
	}
	tendrilDir := filepath.Join(filepath.Dir(dir), ".tendril")
	foreign := os.Getuid() + 1

	// An empty tree resolves no process, whichever identifier is recorded.
	empty := t.TempDir()

	for _, tc := range []struct {
		name  string
		pid   int
		start time.Time
	}{
		{"record predates the running-image fields", 0, time.Time{}},
		{"recorded process is gone", 4244, time.Now()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeIdentity(t, tendrilDir, onDisk, foreign, tc.pid, tc.start)

			finding := executableIntegrityFinding(tendrilDir, empty)

			if finding.Severity != "weak" {
				t.Errorf("severity = %q, want weak: the binary belongs to another principal and that was established, "+
					"whatever the running image turned out to be", finding.Severity)
			}
			if !strings.Contains(finding.Detail, "has not been established") {
				t.Errorf("detail does not say the running image is unestablished:\n%s", finding.Detail)
			}
		})
	}
}

// TestStaleRecordedProcessIsNotTrusted: an identifier outlives the process that
// held it. Comparing against whatever holds it now would attribute a stranger's
// image to the Stem, so a start time that disagrees must read as unestablished.
func TestStaleRecordedProcessIsNotTrusted(t *testing.T) {
	requireProcfsSupport(t)

	_, onDisk := newExecutable(t)
	stranger := onDisk + ".stranger"
	const pid = 4245
	procfs, start := newProcfs(t, pid, stranger)

	tendrilDir := filepath.Join(filepath.Dir(filepath.Dir(onDisk)), ".tendril")
	writeIdentity(t, tendrilDir, onDisk, os.Getuid(), pid, start.Add(-time.Hour))

	finding := executableIntegrityFinding(tendrilDir, procfs)

	if !strings.Contains(finding.Detail, "has not been established") {
		t.Errorf("a start time that disagrees with the recorded one must read as unestablished:\n%s", finding.Detail)
	}
	if strings.Contains(finding.Detail, stranger) {
		t.Errorf("detail names %q, the image of whatever now holds the recorded identifier:\n%s", stranger, finding.Detail)
	}
	if finding.Diverged {
		t.Error("Diverged = true from a comparison against an unrelated process")
	}
}

func TestIsolationTierExplicitAndUnsatisfiableIsWeak(t *testing.T) {
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "gvisor")
	original := conductor.CheckGVisorReadinessFn
	conductor.CheckGVisorReadinessFn = func(context.Context) error { return errors.New("not found") }
	t.Cleanup(func() { conductor.CheckGVisorReadinessFn = original })

	finding := isolationTierFinding(context.Background())
	if finding.Severity != "weak" {
		t.Fatalf("severity = %q, want weak", finding.Severity)
	}
	if !strings.Contains(finding.Detail, "gvisor") || !strings.Contains(finding.Detail, "Docker") {
		t.Errorf("detail should name both what was asked for and what the host offers, got: %s", finding.Detail)
	}
}

func TestIsolationTierExplicitAndSatisfiableIsOK(t *testing.T) {
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "gvisor")
	original := conductor.CheckGVisorReadinessFn
	conductor.CheckGVisorReadinessFn = func(context.Context) error { return nil }
	t.Cleanup(func() { conductor.CheckGVisorReadinessFn = original })

	finding := isolationTierFinding(context.Background())
	if finding.Severity == "weak" {
		t.Fatalf("severity = %q, want ok/note (not weak)", finding.Severity)
	}
}

func TestIsolationTierPreferredAndAvailableIsOK(t *testing.T) {
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "") // Ensure no explicit choice
	original := conductor.CheckGVisorReadinessFn
	conductor.CheckGVisorReadinessFn = func(context.Context) error { return nil }
	t.Cleanup(func() { conductor.CheckGVisorReadinessFn = original })

	finding := isolationTierFinding(context.Background())
	if finding.Severity != "ok" {
		t.Fatalf("severity = %q, want ok", finding.Severity)
	}
	if !strings.Contains(strings.ToLower(finding.Title), "gvisor") || !strings.Contains(strings.ToLower(finding.Title), "preferred and available") {
		t.Errorf("title should report gvisor as preferred and available, got: %s", finding.Title)
	}
}

func TestIsolationTierFallenBackIsNote(t *testing.T) {
	t.Setenv("TENDRIL_TERRARIUM_PROVIDER", "")
	original := conductor.CheckGVisorReadinessFn
	conductor.CheckGVisorReadinessFn = func(context.Context) error { return errors.New("not found") }
	t.Cleanup(func() { conductor.CheckGVisorReadinessFn = original })

	finding := isolationTierFinding(context.Background())
	if finding.Severity != "note" {
		t.Fatalf("severity = %q, want note", finding.Severity)
	}
	if !strings.Contains(strings.ToLower(finding.Title), "docker") || !strings.Contains(strings.ToLower(finding.Title), "fell back") {
		t.Errorf("title should report docker as a fallback, got: %s", finding.Title)
	}
}
