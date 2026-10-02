package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestDelegatedGitStatusBranchFromDefaultStatusPreservesExactOwnership(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repository := t.TempDir()
	runGitApplyTestGit(t, repository, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitApplyTestGit(t, repository, "add", "base.txt")
	runGitApplyTestGit(t, repository, "commit", "-q", "-m", "base")
	baseHead := runGitApplyTestGit(t, repository, "rev-parse", "HEAD")
	runGitApplyTestGit(t, repository, "update-ref", "refs/remotes/origin/main", baseHead)
	runGitApplyTestGit(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	config := &conductor.SubstratesConfig{Substrates: map[string]conductor.SubstrateSpec{
		"demo": {Path: repository, Branch: "main"},
	}}
	service := core.NewService(nil).WithGit(gitOperationsForConfig(config))
	ctx := core.WithPollen(context.Background(), "pollen-one")

	initial, err := service.GitStatus(ctx, core.GitStatusInput{Substrate: "demo"})
	if err != nil {
		t.Fatalf("initial git.status: %v", err)
	}
	if !initial.Isolated || initial.Pollen != "pollen-one" || initial.Workspace == repository {
		t.Fatalf("initial status = %+v, want this Pollen's isolated workspace", initial)
	}

	fromDefault, err := service.GitBranch(ctx, core.GitBranchInput{
		Substrate: "demo", Branch: "feat/from-default", FromDefault: true,
	})
	if err != nil {
		t.Fatalf("git.branch from default: %v", err)
	}
	if fromDefault.Status != "created" || fromDefault.Branch != "feat/from-default" {
		t.Fatalf("fromDefault result = %+v, want the new branch created", fromDefault)
	}
	afterDefault, err := service.GitStatus(ctx, core.GitStatusInput{Substrate: "demo"})
	if err != nil {
		t.Fatalf("git.status after fromDefault branch: %v", err)
	}
	if afterDefault.Workspace != initial.Workspace || afterDefault.Pollen != initial.Pollen || afterDefault.Branch != "feat/from-default" {
		t.Fatalf("status after fromDefault = %+v, want the same delegated workspace on the new branch", afterDefault)
	}
	requireExactDelegatedBranchOwnership(t, repository, "feat/from-default", "pollen-one", baseHead)

	ordinary, err := service.GitBranch(ctx, core.GitBranchInput{Substrate: "demo", Branch: "feat/ordinary"})
	if err != nil {
		t.Fatalf("ordinary git.branch creation: %v", err)
	}
	if ordinary.Status != "created" || ordinary.Branch != "feat/ordinary" {
		t.Fatalf("ordinary result = %+v, want the new branch created", ordinary)
	}
	afterOrdinary, err := service.GitStatus(ctx, core.GitStatusInput{Substrate: "demo"})
	if err != nil {
		t.Fatalf("git.status after ordinary branch: %v", err)
	}
	if afterOrdinary.Workspace != initial.Workspace || afterOrdinary.Pollen != initial.Pollen || afterOrdinary.Branch != "feat/ordinary" {
		t.Fatalf("status after ordinary branch = %+v, want the same delegated workspace on the new branch", afterOrdinary)
	}
	requireExactDelegatedBranchOwnership(t, repository, "feat/ordinary", "pollen-one", baseHead)
}

func requireExactDelegatedBranchOwnership(t *testing.T, repository, branch, pollen, base string) {
	t.Helper()
	var matching []conductor.OwnedRef
	for _, owned := range conductor.OwnedRefsFor(repository) {
		if owned.Branch == branch {
			matching = append(matching, owned)
		}
	}
	if len(matching) != 1 {
		t.Fatalf("ownership records for %s = %+v, want exactly one", branch, matching)
	}
	owned := matching[0]
	if owned.Repository != filepath.Clean(repository) || owned.Purpose != conductor.PurposeDelegatedWorkspace || owned.Pollen != pollen || owned.Base != base || owned.Pending || !owned.RetainEmpty {
		t.Fatalf("ownership for %s = %+v, want exact finalized repository/Pollen/base ownership", branch, owned)
	}
}
