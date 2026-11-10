package gitsafety

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestArgsAreEmptyForTheFullPosture(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(core.DeploymentPostureEnv, "")
	args, err := Args(context.Background(), t.TempDir(), nil)
	if err != nil || args != nil {
		t.Fatalf("Args() = %v, %v; want no arguments", args, err)
	}
}

func TestArgsStayEmptyWhenHomeIsNotADirectory(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked-home")
	if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", blocked)
	t.Setenv(core.DeploymentPostureEnv, "")
	args, err := Args(context.Background(), t.TempDir(), nil)
	if err != nil || args != nil {
		t.Fatalf("Args() = %v, %v; want no arguments", args, err)
	}
	t.Setenv(core.DeploymentPostureEnv, core.DeploymentPostureRemoteGit)
	args, err = Args(context.Background(), t.TempDir(), nil)
	if err == nil || args != nil || !strings.Contains(err.Error(), "remote-git git command refused") {
		t.Fatalf("Args() = %v, %v; want a static refusal", args, err)
	}
}

func TestArgsFailClosedWhenPostureIsAmbiguous(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(core.DeploymentPostureEnv, core.DeploymentPostureRemoteGit)
	args, err := Args(context.Background(), t.TempDir(), nil)
	if err == nil || args != nil || !strings.Contains(err.Error(), "remote-git git command refused") {
		t.Fatalf("Args() = %v, %v; want a static refusal", args, err)
	}
}

func TestArgsOmitCredentialHelperWhenTheStemSuppliesOne(t *testing.T) {
	dir := useRemoteGitHome(t)
	withHelper, err := Args(context.Background(), dir, []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
	})
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	if slices.Contains(withHelper, "credential.helper=") {
		t.Fatalf("Stem credential helper was replaced: %v", withHelper)
	}
	without, err := Args(context.Background(), dir, []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	if !slices.Contains(without, "credential.helper=") {
		t.Fatalf("local credential helper was left in force: %v", without)
	}
	for _, want := range []string{"core.hooksPath=/dev/null", "core.fsmonitor=", "diff.external=", "core.sshCommand=", "protocol.ext.allow=never"} {
		if !slices.Contains(without, want) {
			t.Fatalf("missing %s in %v", want, without)
		}
	}
}

func TestInsertPlacesSafetyAfterCallerConfiguration(t *testing.T) {
	got := Insert(
		[]string{"-c", "user.name=Bot", "-c", "user.email=bot@example.com", "commit", "-m", "msg"},
		[]string{"-c", "core.hooksPath=/dev/null"},
	)
	want := []string{
		"-c", "user.name=Bot",
		"-c", "user.email=bot@example.com",
		"-c", "core.hooksPath=/dev/null",
		"commit", "-m", "msg",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Insert() = %v, want %v", got, want)
	}
}

func TestArgsOverrideConfiguredFilterAndDiffCommands(t *testing.T) {
	useRemoteGitHome(t)
	repo := t.TempDir()
	gitIn(t, repo, "init", "--quiet")
	gitIn(t, repo, "config", "filter.pwn.clean", "touch-marker")
	gitIn(t, repo, "config", "diff.mine.command", "touch-marker")
	gitIn(t, repo, "config", "merge.mine.driver", "touch-marker")
	args, err := Args(context.Background(), repo, os.Environ())
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	for _, want := range []string{
		"filter.pwn.clean=",
		"filter.pwn.smudge=",
		"filter.pwn.process=",
		"filter.pwn.required=false",
		"diff.mine.command=",
		"diff.mine.textconv=",
		"merge.mine.driver=",
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %s in %v", want, args)
		}
	}
	joined := strings.Join(args, "\n")
	if strings.Contains(joined, "touch-marker") {
		t.Fatalf("safety arguments copied a configured command: %v", args)
	}
}

func useRemoteGitHome(t *testing.T) string {
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
	return t.TempDir()
}

func gitIn(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, output)
	}
}
