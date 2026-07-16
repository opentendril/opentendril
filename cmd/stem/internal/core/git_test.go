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
