package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestRemoteGitCompatAdmitsOnlyADenyingBinary(t *testing.T) {
	t.Setenv(core.DeploymentPostureEnv, core.DeploymentPostureRemoteGit)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, core.DeploymentPostureFilename), []byte("remote-git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	message, code := remoteGitCompatResult(dir)
	if code != 0 || message != remoteGitCompatLine {
		t.Fatalf("compat = %q, %d", message, code)
	}

	t.Setenv(core.DeploymentPostureEnv, "")
	message, code = remoteGitCompatResult(dir)
	if code != 0 || message != remoteGitCompatLine {
		t.Fatalf("posture file alone = %q, %d", message, code)
	}
	message, code = remoteGitCompatResult(t.TempDir())
	if code == 0 || message != "" {
		t.Fatalf("full posture = %q, %d", message, code)
	}

	t.Setenv(core.DeploymentPostureEnv, core.DeploymentPostureRemoteGit)
	message, code = remoteGitCompatResult(t.TempDir())
	if code == 0 || strings.Contains(message, "git-admitted") {
		t.Fatalf("env without file = %q, %d", message, code)
	}

	if err := os.WriteFile(filepath.Join(dir, core.DeploymentPostureFilename), []byte("full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	message, code = remoteGitCompatResult(dir)
	if code == 0 || message != "" {
		t.Fatalf("mismatched posture = %q, %d", message, code)
	}
}

func TestRemoteGitCompatRejectsAShortCommand(t *testing.T) {
	message, code, ok := handleRemoteGitCompat([]string{"remote-git-compat"})
	if !ok || code == 0 || message != "" {
		t.Fatalf("short command = %q, %d, %t", message, code, ok)
	}
	if _, _, ok := handleRemoteGitCompat([]string{"serve"}); ok {
		t.Fatal("serve was handled as the compatibility probe")
	}
}
