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
	branch := "tendril/pollen-one/work"
	runGitApplyTestGit(t, repository, "worktree", "add", "-q", "-b", branch, workspacePath, head)
	if err := conductor.RegisterOwnedRef(conductor.OwnedRef{
		Repository: repository, Branch: branch, Purpose: conductor.PurposeDelegatedWorkspace,
		Pollen: "pollen-one", Base: head,
	}); err != nil {
		t.Fatal(err)
	}
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

func TestDelegatedGitApplyStatusCommitContinuesSameWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repository := t.TempDir()
	runGitApplyTestGit(t, repository, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitApplyTestGit(t, repository, "add", "base.txt")
	runGitApplyTestGit(t, repository, "commit", "-q", "-m", "base")
	baseHead := runGitApplyTestGit(t, repository, "rev-parse", "HEAD")
	runGitApplyTestGit(t, repository, "update-ref", "refs/remotes/origin/main", baseHead)
	runGitApplyTestGit(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	config := &conductor.SubstratesConfig{Substrates: map[string]conductor.SubstrateSpec{
		"demo": {
			Path: repository, Branch: "main",
			Identity: conductor.IdentitySpec{Name: "Delegated Test", Email: "delegated@example.invalid"},
		},
	}}
	service := core.NewService(nil).WithGit(gitOperationsForConfig(config))
	ctx := core.WithPollen(context.Background(), "pollen-one")
	initial, err := service.GitStatus(ctx, core.GitStatusInput{Substrate: "demo"})
	if err != nil {
		t.Fatalf("initial delegated git.status: %v", err)
	}
	if !initial.Isolated || initial.Pollen != "pollen-one" || initial.Workspace == repository || initial.Head != baseHead {
		t.Fatalf("initial status = %+v, want the exact Pollen's isolated workspace at base HEAD", initial)
	}

	patchSource := filepath.Join(t.TempDir(), "source")
	clone := exec.Command("git", "clone", "-q", repository, patchSource)
	if output, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone apply patch source: %v (%s)", err, output)
	}
	if err := os.WriteFile(filepath.Join(patchSource, "base.txt"), []byte("after apply\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patchCommand := exec.Command("git", "-C", patchSource, "diff", "--binary", "--")
	patch, err := patchCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	applyCtx := core.WithAuthorizedDelegationRequest(ctx, core.DelegationRequest{
		Pollen: "pollen-one", OperationClass: core.CapGitApply, Substrate: "demo", Impact: core.CapabilityImpact(core.CapGitApply),
	})
	applied, err := service.GitApply(applyCtx, core.GitApplyInput{
		Substrate: "demo", ExpectedHead: baseHead, Patch: string(patch),
	})
	if err != nil {
		t.Fatalf("delegated git.apply: %v", err)
	}
	if applied.Status != "applied" || applied.Branch != initial.Branch || applied.Head != baseHead {
		t.Fatalf("apply result = %+v, want changes on the original workspace branch and HEAD", applied)
	}

	beforeCommit, err := service.GitStatus(ctx, core.GitStatusInput{Substrate: "demo"})
	if err != nil {
		t.Fatalf("git.status after apply: %v", err)
	}
	if beforeCommit.Workspace != initial.Workspace || beforeCommit.Pollen != initial.Pollen || beforeCommit.Branch != initial.Branch || beforeCommit.Clean || beforeCommit.ChangeCount != 1 {
		t.Fatalf("status after apply = %+v, want the same dirty delegated workspace", beforeCommit)
	}

	committed, err := service.GitCommit(ctx, core.GitCommitInput{Substrate: "demo", Message: "apply and commit delegated change"})
	if err != nil {
		t.Fatalf("git.commit after apply/status: %v", err)
	}
	if committed.Status != "committed" || committed.CommitHash == "" {
		t.Fatalf("commit result = %+v, want a new commit", committed)
	}
	afterCommit, err := service.GitStatus(ctx, core.GitStatusInput{Substrate: "demo"})
	if err != nil {
		t.Fatalf("git.status after commit: %v", err)
	}
	if afterCommit.Workspace != initial.Workspace || afterCommit.Pollen != initial.Pollen || afterCommit.Branch != initial.Branch || afterCommit.Head != committed.CommitHash || !afterCommit.Clean {
		t.Fatalf("status after commit = %+v, want same Pollen workspace, branch, committed HEAD, and clean state", afterCommit)
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
