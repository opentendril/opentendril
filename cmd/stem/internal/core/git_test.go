package core

import (
	"context"
	"strings"
	"testing"
)

// newGitService wires a Service with a stubbed git port and returns the
// captured spec of the last commit.
func newGitService(t *testing.T) (*Service, *GitCommitSpec) {
	t.Helper()
	captured := &GitCommitSpec{}
	svc := NewService(nil).WithGit(GitOperations{
		Commit: func(_ context.Context, spec GitCommitSpec) (GitCommitResult, error) {
			*captured = spec
			return GitCommitResult{Status: "committed", CommitHash: "deadbeef"}, nil
		},
	})
	return svc, captured
}

func TestGitCommitValidatesInput(t *testing.T) {
	svc, _ := newGitService(t)
	ctx := context.Background()

	if _, err := svc.GitCommit(ctx, GitCommitInput{Message: "chore: tidy"}); err == nil {
		t.Fatal("missing substrate accepted")
	}
	if _, err := svc.GitCommit(ctx, GitCommitInput{Substrate: "core"}); err == nil {
		t.Fatal("missing message accepted")
	}
	if _, err := svc.GitCommit(ctx, GitCommitInput{Substrate: "core", Message: "  "}); err == nil {
		t.Fatal("blank message accepted")
	}
}

func TestGitCommitNotWired(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.GitCommit(context.Background(), GitCommitInput{Substrate: "core", Message: "chore: tidy"})
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired git commit error = %v, want a not-wired report", err)
	}
}

func TestGitPushValidatesInput(t *testing.T) {
	captured := &GitPushSpec{}
	svc := NewService(nil).WithGit(GitOperations{
		Push: func(_ context.Context, spec GitPushSpec) (GitPushResult, error) {
			*captured = spec
			return GitPushResult{Status: "pushed", Branch: spec.Branch}, nil
		},
	})
	ctx := context.Background()

	if _, err := svc.GitPush(ctx, GitPushInput{}); err == nil {
		t.Fatal("missing substrate accepted")
	}
	if _, err := svc.GitPush(ctx, GitPushInput{Substrate: "  "}); err == nil {
		t.Fatal("blank substrate accepted")
	}

	// A push with only a substrate is valid: the branch is optional (empty
	// means the workspace's current branch), unlike commit which requires a
	// message.
	if _, err := svc.GitPush(ctx, GitPushInput{Substrate: " core ", Branch: " feature/x "}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if captured.Substrate != "core" || captured.Branch != "feature/x" {
		t.Fatalf("spec = %+v, want trimmed substrate/branch", captured)
	}
}

func TestGitPushNotWired(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.GitPush(context.Background(), GitPushInput{Substrate: "core"})
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired git push error = %v, want a not-wired report", err)
	}
}

func TestGitCommitPathsNormalized(t *testing.T) {
	svc, captured := newGitService(t)
	ctx := context.Background()

	if _, err := svc.GitCommit(ctx, GitCommitInput{Substrate: "core", Message: "chore: tidy", Paths: []string{" a.go ", "", "  ", "b.go"}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(captured.Paths) != 2 || captured.Paths[0] != "a.go" || captured.Paths[1] != "b.go" {
		t.Fatalf("spec paths = %v, want the trimmed non-blank entries", captured.Paths)
	}

	// An all-blank path list degrades to "stage all changes" (nil), the same
	// as omitting it.
	if _, err := svc.GitCommit(ctx, GitCommitInput{Substrate: "core", Message: "chore: tidy", Paths: []string{"", "  "}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if captured.Paths != nil {
		t.Fatalf("spec paths = %v, want nil for an all-blank list", captured.Paths)
	}
}

func TestGitPRValidatesInput(t *testing.T) {
	captured := &GitPRSpec{}
	svc := NewService(nil).WithGit(GitOperations{
		PullRequest: func(_ context.Context, spec GitPRSpec) (GitPRResult, error) {
			*captured = spec
			return GitPRResult{Status: "created", Number: 1}, nil
		},
	})
	ctx := context.Background()

	if _, err := svc.GitPR(ctx, GitPRInput{Title: "feat: x"}); err == nil {
		t.Fatal("missing substrate accepted")
	}
	if _, err := svc.GitPR(ctx, GitPRInput{Substrate: "  ", Title: "feat: x"}); err == nil {
		t.Fatal("blank substrate accepted")
	}
	if _, err := svc.GitPR(ctx, GitPRInput{Substrate: "core"}); err == nil {
		t.Fatal("missing title accepted")
	}
	if _, err := svc.GitPR(ctx, GitPRInput{Substrate: "core", Title: "   "}); err == nil {
		t.Fatal("blank title accepted")
	}

	// Head and base are optional at this layer on purpose: an omitted head is
	// read from the workspace and an omitted base is read from the repository
	// by the execution port. Neither is defaulted to a guessed branch name
	// here — the Core must not invent branches.
	if _, err := svc.GitPR(ctx, GitPRInput{Substrate: " core ", Title: " feat: grow ", Draft: true}); err != nil {
		t.Fatalf("pull request: %v", err)
	}
	if captured.Substrate != "core" || captured.Title != "feat: grow" {
		t.Fatalf("spec = %+v, want trimmed substrate/title", captured)
	}
	if captured.Head != "" || captured.Base != "" {
		t.Fatalf("spec = %+v, want empty head/base passed through for the execution port to resolve", captured)
	}
	if !captured.Draft {
		t.Fatalf("spec = %+v, want draft carried through", captured)
	}
}

func TestGitPRNotWired(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.GitPR(context.Background(), GitPRInput{Substrate: "core", Title: "feat: x"})
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired git pull request error = %v, want a not-wired report", err)
	}
}

func TestGitBranchValidatesInput(t *testing.T) {
	captured := &GitBranchSpec{}
	svc := NewService(nil).WithGit(GitOperations{
		Branch: func(_ context.Context, spec GitBranchSpec) (GitBranchResult, error) {
			*captured = spec
			return GitBranchResult{Status: "created", Branch: spec.Branch}, nil
		},
	})
	ctx := context.Background()

	if _, err := svc.GitBranch(ctx, GitBranchInput{Branch: "feat/x"}); err == nil {
		t.Fatal("missing substrate accepted")
	}
	if _, err := svc.GitBranch(ctx, GitBranchInput{Substrate: "core"}); err == nil {
		t.Fatal("missing branch accepted")
	}
	if _, err := svc.GitBranch(ctx, GitBranchInput{Substrate: "core", Branch: "  "}); err == nil {
		t.Fatal("blank branch accepted")
	}
	if _, err := svc.GitBranch(ctx, GitBranchInput{Substrate: " core ", Branch: " feat/x "}); err != nil {
		t.Fatalf("branch: %v", err)
	}
	if captured.Substrate != "core" || captured.Branch != "feat/x" {
		t.Fatalf("spec = %+v, want trimmed substrate/branch", captured)
	}
}

func TestGitBranchNotWired(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.GitBranch(context.Background(), GitBranchInput{Substrate: "core", Branch: "feat/x"})
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("unwired git branch error = %v, want a not-wired report", err)
	}
}
