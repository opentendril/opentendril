package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunGitCommitValidatesExecution covers the plain-input requirements
// before any git command runs.
func TestRunGitCommitValidatesExecution(t *testing.T) {
	originalRun := runGitCommitCommandFn
	commands := 0
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	defer func() { runGitCommitCommandFn = originalRun }()

	identity := ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}}
	if _, err := RunGitCommit(context.Background(), GitCommitExecution{Message: "chore: tidy", Credential: identity}); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if _, err := RunGitCommit(context.Background(), GitCommitExecution{Workspace: "/tmp/workspace", Credential: identity}); err == nil {
		t.Fatal("missing message accepted")
	}
	if commands != 0 {
		t.Fatalf("%d git command(s) ran for invalid executions, want 0", commands)
	}
}

// TestRunGitCommitRequiresConfiguredIdentity is the deny-closed attribution
// rule: a delegated commit exists to be attributable, so a missing commit
// identity — name, email, or both — refuses the whole execution before any
// git command runs. No commit is ever created.
func TestRunGitCommitRequiresConfiguredIdentity(t *testing.T) {
	originalRun := runGitCommitCommandFn
	commands := 0
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	defer func() { runGitCommitCommandFn = originalRun }()

	for _, credential := range []ResolvedCredential{
		{},
		{Identity: ResolvedIdentity{Name: "OpenTendril Bot"}},
		{Identity: ResolvedIdentity{Email: "bot@example.com"}},
		{Identity: ResolvedIdentity{Name: "  ", Email: "\t"}}, // whitespace-only counts as unset
	} {
		_, err := RunGitCommit(context.Background(), GitCommitExecution{
			Workspace:  "/tmp/workspace",
			Message:    "chore: tidy",
			Credential: credential,
		})
		if err == nil || !strings.Contains(err.Error(), "no configured commit identity") {
			t.Fatalf("identity %+v: error = %v, want a refused-without-identity report", credential.Identity, err)
		}
	}
	if commands != 0 {
		t.Fatalf("%d git command(s) ran for identity-less executions, want 0", commands)
	}
}

// newGitCommitRepo initializes a real repository with an ambient git identity
// and one initial commit, so RunGitCommit exercises real staging and
// committing.
func newGitCommitRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return repo
}

// TestRunGitCommitCreatesAttributedCommit proves a real commit is created and
// attributed (author and committer) to the configured identity — never to the
// ambient one — and that the reported hash is the repository's new HEAD.
func TestRunGitCommitCreatesAttributedCommit(t *testing.T) {
	ctx := context.Background()
	repo := newGitCommitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "grown.txt"), []byte("grown\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "chore: record delegated growth",
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "committed" || result.CommitHash == "" {
		t.Fatalf("result = %+v, want a committed status with a hash", result)
	}

	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	if result.CommitHash != strings.TrimSpace(head) {
		t.Fatalf("reported hash %q is not HEAD %q", result.CommitHash, head)
	}

	attribution, err := runGitCommand(ctx, repo, "log", "-1", "--format=%an|%ae|%cn|%ce|%s")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	want := "OpenTendril Bot|bot@example.com|OpenTendril Bot|bot@example.com|chore: record delegated growth"
	if strings.TrimSpace(attribution) != want {
		t.Fatalf("attribution = %q, want %q", attribution, want)
	}
}

// TestRunGitCommitNothingToCommit proves a clean workspace returns cleanly —
// no error, no empty commit (unlike the Sprout status path, which
// deliberately allows one).
func TestRunGitCommitNothingToCommit(t *testing.T) {
	ctx := context.Background()
	repo := newGitCommitRepo(t)
	before, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "chore: nothing here",
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "nothing-to-commit" || result.CommitHash != "" {
		t.Fatalf("result = %+v, want a hash-less nothing-to-commit status", result)
	}

	after, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	if before != after {
		t.Fatalf("HEAD moved from %q to %q; an empty delegated commit must never be created", before, after)
	}
}

// TestRunGitPushValidatesWorkspace covers the plain-input requirement before
// any git command runs.
func TestRunGitPushValidatesWorkspace(t *testing.T) {
	originalRead := runGitCommitCommandFn
	originalPush := runGitPushCommandFn
	commands := 0
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	runGitPushCommandFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	defer func() { runGitCommitCommandFn = originalRead; runGitPushCommandFn = originalPush }()

	if _, err := RunGitPush(context.Background(), GitPushExecution{}); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if commands != 0 {
		t.Fatalf("%d git command(s) ran for an invalid execution, want 0", commands)
	}
}

// newGitPushRepoWithRemote builds a working repository on branch main with one
// commit and a bare origin remote on disk, so RunGitPush exercises a real
// authenticated-shaped push (no token needed for a local file remote).
func newGitPushRepoWithRemote(t *testing.T) (workspace, remote string) {
	t.Helper()
	ctx := context.Background()
	remote = t.TempDir()
	if _, err := runGitCommand(ctx, remote, "init", "--bare"); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	workspace = t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"checkout", "-b", "main"},
		{"commit", "--allow-empty", "-m", "initial"},
		{"remote", "add", "origin", remote},
	} {
		if _, err := runGitCommand(ctx, workspace, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return workspace, remote
}

// TestRunGitPushPushesCurrentBranch proves the workspace's current branch lands
// on the remote when no explicit branch is given.
func TestRunGitPushPushesCurrentBranch(t *testing.T) {
	ctx := context.Background()
	workspace, remote := newGitPushRepoWithRemote(t)

	result, err := RunGitPush(ctx, GitPushExecution{Workspace: workspace})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.Status != "pushed" || result.Branch != "main" {
		t.Fatalf("result = %+v, want pushed on main", result)
	}

	local, err := runGitCommand(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	remoteRef, err := runGitCommand(ctx, remote, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("git rev-parse remote ref: %v", err)
	}
	if strings.TrimSpace(local) != strings.TrimSpace(remoteRef) {
		t.Fatalf("remote ref %q does not match local HEAD %q", remoteRef, local)
	}
}

// TestRunGitPushExplicitBranch proves an explicit branch is pushed and the
// refs/heads/ prefix is tolerated.
func TestRunGitPushExplicitBranch(t *testing.T) {
	ctx := context.Background()
	workspace, remote := newGitPushRepoWithRemote(t)

	result, err := RunGitPush(ctx, GitPushExecution{Workspace: workspace, Branch: "refs/heads/main"})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.Branch != "main" {
		t.Fatalf("branch = %q, want the refs/heads/ prefix stripped to main", result.Branch)
	}
	if _, err := runGitCommand(ctx, remote, "rev-parse", "refs/heads/main"); err != nil {
		t.Fatalf("remote did not receive refs/heads/main: %v", err)
	}
}

// TestRunGitCommitStagesOnlyGivenPaths proves the optional path list bounds
// staging: the named file is committed, the unnamed one stays uncommitted.
func TestRunGitCommitStagesOnlyGivenPaths(t *testing.T) {
	ctx := context.Background()
	repo := newGitCommitRepo(t)
	for _, name := range []string{"wanted.txt", "unwanted.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "chore: commit only the wanted file",
		Paths:      []string{"wanted.txt"},
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "committed" {
		t.Fatalf("result = %+v, want committed", result)
	}

	committed, err := runGitCommand(ctx, repo, "show", "--name-only", "--format=", "HEAD")
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	if !strings.Contains(committed, "wanted.txt") || strings.Contains(committed, "unwanted.txt") {
		t.Fatalf("committed files = %q, want only wanted.txt", committed)
	}
}
