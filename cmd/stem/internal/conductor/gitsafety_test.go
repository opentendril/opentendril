package conductor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestRemoteGitBlocksRepositoryLocalGitExecution(t *testing.T) {
	repo, marker := hostileGitRepository(t)

	runHostileGit(t, repo, marker, "status")
	if !markerContains(t, marker, "fs") {
		t.Fatal("full posture did not run the repository fsmonitor; the regression would not detect a bypass")
	}
	clearMarker(t, marker)
	runHostileGit(t, repo, marker, "add", "hello.txt")
	if !markerContains(t, marker, "filter") {
		t.Fatal("full posture did not run the repository clean filter; the regression would not detect a bypass")
	}
	clearMarker(t, marker)
	runHostileGit(t, repo, marker, "commit", "-m", "hello")
	if !markerContains(t, marker, "hook") {
		t.Fatal("full posture did not run the repository hook; the regression would not detect a bypass")
	}

	useRemoteGitPosture(t)
	clearMarker(t, marker)
	runHostileGit(t, repo, marker, "status")
	runHostileGit(t, repo, marker, "add", "hello.txt")
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runHostileGit(t, repo, marker, "add", "second.txt")
	runHostileGit(t, repo, marker, "commit", "-m", "second")
	if text := readMarker(t, marker); text != "" {
		t.Fatalf("remote-git ran repository Git configuration: %q", text)
	}
}

func TestAmbiguousPostureDoesNotRunRepositoryGitConfiguration(t *testing.T) {
	repo, marker := hostileGitRepository(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(core.DeploymentPostureEnv, core.DeploymentPostureRemoteGit)
	_, err := runGitCommand(context.Background(), repo, "status")
	if err == nil || !strings.Contains(err.Error(), "remote-git git command refused") {
		t.Fatalf("status error = %v; want a static refusal", err)
	}
	if text := readMarker(t, marker); text != "" {
		t.Fatalf("ambiguous posture ran repository Git configuration: %q", text)
	}
}

func TestRemoteGitDiffDriverDoesNotRun(t *testing.T) {
	repo, marker := hostileGitRepository(t)
	driver := filepath.Join(repo, "diff-driver")
	writeExecutable(t, driver, "#!/bin/sh\nprintf 'diff\\n' >> '"+marker+"'\nexit 0\n")
	gitSetup(t, repo, "config", "diff.mine.command", driver)
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=pwn diff=mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useRemoteGitPosture(t)
	clearMarker(t, marker)
	_, _ = runGitCommand(context.Background(), repo, "diff")
	if text := readMarker(t, marker); strings.Contains(text, "diff") {
		t.Fatalf("remote-git ran a repository diff driver: %q", text)
	}
}

func hostileGitRepository(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	marker := filepath.Join(repo, "marker")
	hooks := filepath.Join(repo, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(hooks, "pre-commit"), "#!/bin/sh\nprintf 'hook\\n' >> '"+marker+"'\n")
	writeExecutable(t, filepath.Join(repo, "fsmonitor"), "#!/bin/sh\nprintf 'fs\\n' >> '"+marker+"'\nexit 0\n")
	writeExecutable(t, filepath.Join(repo, "filter"), "#!/bin/sh\nprintf 'filter\\n' >> '"+marker+"'\ncat\n")
	gitSetup(t, repo, "init", "--quiet", "--initial-branch=main")
	gitSetup(t, repo, "config", "user.name", "Bot")
	gitSetup(t, repo, "config", "user.email", "bot@example.com")
	gitSetup(t, repo, "config", "commit.gpgsign", "false")
	gitSetup(t, repo, "config", "core.hooksPath", hooks)
	gitSetup(t, repo, "config", "core.fsmonitor", filepath.Join(repo, "fsmonitor"))
	gitSetup(t, repo, "config", "filter.pwn.clean", filepath.Join(repo, "filter"))
	gitSetup(t, repo, "config", "filter.pwn.required", "true")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, marker
}

func useRemoteGitPosture(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(core.DeploymentPostureEnv, core.DeploymentPostureRemoteGit)
	plane := filepath.Join(home, ".tendril")
	if err := os.Mkdir(plane, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plane, core.DeploymentPostureFilename), []byte("remote-git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runHostileGit(t *testing.T, repo, marker string, args ...string) {
	t.Helper()
	if _, err := runGitCommand(context.Background(), repo, args...); err != nil {
		t.Fatalf("git %s: %v (marker %q)", strings.Join(args, " "), err, readMarker(t, marker))
	}
}

func gitSetup(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, output)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func clearMarker(t *testing.T, marker string) {
	t.Helper()
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func readMarker(t *testing.T, marker string) string {
	t.Helper()
	text, err := os.ReadFile(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(text)
}

func markerContains(t *testing.T, marker, token string) bool {
	t.Helper()
	return strings.Contains(readMarker(t, marker), token)
}
