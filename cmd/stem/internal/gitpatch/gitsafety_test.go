package gitpatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestRemoteGitApplyPathBlocksFsmonitor(t *testing.T) {
	repo := t.TempDir()
	marker := filepath.Join(repo, "marker")
	monitor := filepath.Join(repo, "fsmonitor")
	if err := os.WriteFile(monitor, []byte("#!/bin/sh\nprintf 'fs\\n' >> '"+marker+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, output)
		}
	}
	git("init", "--quiet")
	git("config", "core.fsmonitor", monitor)
	if err := runGit(context.Background(), repo, nil, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(text), "fs") {
		t.Fatal("full posture did not run fsmonitor through the patch Git path")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
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
	if err := runGit(context.Background(), repo, nil, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("remote-git ran fsmonitor through the patch Git path: %v", err)
	}
}
