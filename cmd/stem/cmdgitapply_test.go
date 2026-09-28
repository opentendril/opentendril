package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestGitApplyCLIRequiresFlagsAndReadsPatchOnlyFromStdin(t *testing.T) {
	command, ok := lookupGitCommand("apply")
	if !ok || command.capability != core.CapGitApply {
		t.Fatalf("CLI apply registration = %+v, found=%v", command, ok)
	}
	input, err := parseGitArgs(command.capability, []string{
		"--substrate", "demo", "--expected-head", "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatalf("parse git.apply arguments: %v", err)
	}
	if _, exists := input["patch"]; exists {
		t.Fatal("CLI arguments supplied patch bytes; patch must come only from stdin")
	}
	patch := []byte("diff --git a/a.txt b/a.txt\n")
	input, err = readGitApplyInput(input, bytes.NewReader(patch))
	if err != nil {
		t.Fatalf("read patch stdin: %v", err)
	}
	if got, _ := input["patch"].(string); got != string(patch) {
		t.Fatalf("stdin patch = %q, want %q", got, patch)
	}

	for _, args := range [][]string{
		{"--substrate", "demo", "--expected-head", "0123456789abcdef0123456789abcdef01234567", "--patch", "file.diff"},
		{"--json", `{"substrate":"demo","expectedHead":"0123456789abcdef0123456789abcdef01234567","patch":"inline"}`},
	} {
		if _, err := parseGitArgs(core.CapGitApply, args); err == nil {
			t.Fatalf("accepted non-stdin patch input: %v", args)
		}
	}
}

func runGitApplyTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(command.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestResolveExistingGitApplyWorkspaceRequiresNamedConfiguredSubstrateAndPollen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repository := t.TempDir()
	runGitApplyTestGit(t, repository, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitApplyTestGit(t, repository, "add", "base.txt")
	runGitApplyTestGit(t, repository, "commit", "-q", "-m", "base")
	head := runGitApplyTestGit(t, repository, "rev-parse", "HEAD")
	workspacePath := filepath.Join(os.Getenv("HOME"), ".tendril", "workspaces", "demo", "pollen-one")
	if err := os.MkdirAll(filepath.Dir(workspacePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitApplyTestGit(t, repository, "worktree", "add", "-q", "-b", "feature/apply", workspacePath, head)
	config := &conductor.SubstratesConfig{Substrates: map[string]conductor.SubstrateSpec{
		"demo": {Checkout: conductor.CheckoutSpec{Mode: "path", Path: repository}},
	}}
	ctx := core.WithPollen(context.Background(), "pollen-one")
	resolved, spec, err := resolveExistingGitApplyWorkspace(ctx, "demo", config)
	if err != nil {
		t.Fatalf("resolve configured path-mode Substrate: %v", err)
	}
	if spec == nil || resolved.Path != workspacePath || resolved.Path == repository || !resolved.Isolated {
		t.Fatalf("resolved path-mode workspace = %+v, spec=%+v; must use Pollen worktree, never configured raw checkout", resolved, spec)
	}
	for _, substrate := range []string{repository, "unknown"} {
		if _, _, err := resolveExistingGitApplyWorkspace(ctx, substrate, config); err == nil {
			t.Fatalf("Pollinator-selected or unknown substrate %q was accepted", substrate)
		}
	}
	if _, _, err := resolveExistingGitApplyWorkspace(context.Background(), "demo", config); err == nil {
		t.Fatal("git.apply resolved without a trusted Pollen")
	}
}

func TestGitApplyCLIBoundsStdinReadAndPreservesInvalidBytesForCore(t *testing.T) {
	input := map[string]any{"substrate": "demo"}
	tooLarge := strings.NewReader(strings.Repeat("x", core.MaxGitApplyPatchBytes+4096))
	got, err := readGitApplyInput(input, tooLarge)
	if err != nil {
		t.Fatalf("bounded readGitApplyInput: %v", err)
	}
	patch, _ := got["patch"].(string)
	if len(patch) != core.MaxGitApplyPatchBytes+1 {
		t.Fatalf("stdin read length = %d, want only max+1 bytes to signal oversize", len(patch))
	}

	got, err = readGitApplyInput(map[string]any{}, bytes.NewReader([]byte{0xff, 0xfe}))
	if err != nil {
		t.Fatal(err)
	}
	if patch, _ := got["patch"].(string); patch != string([]byte{0xff, 0xfe}) {
		t.Fatal("invalid stdin bytes were transformed before Core validation")
	}
}
