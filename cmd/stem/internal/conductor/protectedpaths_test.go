package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The kernel guard.
//
// These exercise the refusal against real git history rather than against a
// list of strings, because the property that matters is that a commit does not
// merge — not that a helper returns an error.

// newGuardedRepo builds a repository with a protected-paths list committed on
// its default branch, and returns its path.
func newGuardedRepo(t *testing.T, list string) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	if list != "" {
		writeRepoFile(t, repo, protectedPathsFile, list)
	}
	writeRepoFile(t, repo, "readme.md", "seed\n")

	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "seed"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return repo
}

func writeRepoFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	path := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// terrariumCommit produces a commit on a side branch, as a Sprout's run would,
// and returns its hash while leaving the checkout back on the original branch.
func terrariumCommit(t *testing.T, repo string, mutate func()) string {
	t.Helper()
	ctx := context.Background()

	original, err := runGitCommand(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("resolve branch: %v", err)
	}
	originalBranch := strings.TrimSpace(original)

	if _, err := runGitCommand(ctx, repo, "checkout", "-b", "terrarium/run"); err != nil {
		t.Fatalf("branch: %v", err)
	}
	mutate()
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "terrarium run"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	hash, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "checkout", originalBranch); err != nil {
		t.Fatalf("checkout back: %v", err)
	}
	return strings.TrimSpace(hash)
}

func headHash(t *testing.T, repo string) string {
	t.Helper()
	hash, err := runGitCommand(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(hash)
}

const guardList = "# fixture\nGUARDRAILS.md\ncmd/stem/internal/core/\n.github/protected-paths\n"

func TestMergeAllowsCommitThatTouchesNothingProtected(t *testing.T) {
	repo := newGuardedRepo(t, guardList)
	commit := terrariumCommit(t, repo, func() {
		writeRepoFile(t, repo, "docs/notes.md", "ordinary work\n")
	})

	if err := mergeTerrariumCommit(context.Background(), repo, commit); err != nil {
		t.Fatalf("an ordinary commit was refused: %v", err)
	}
	if got := headHash(t, repo); got != commit {
		t.Fatalf("merge did not fast-forward: HEAD = %s, want %s", got, commit)
	}
}

func TestMergeRefusesCommitTouchingProtectedFile(t *testing.T) {
	repo := newGuardedRepo(t, guardList)
	before := headHash(t, repo)
	commit := terrariumCommit(t, repo, func() {
		writeRepoFile(t, repo, "GUARDRAILS.md", "rewritten by a sprout\n")
	})

	err := mergeTerrariumCommit(context.Background(), repo, commit)
	if err == nil {
		t.Fatal("a commit rewriting a protected file was merged")
	}
	if !strings.Contains(err.Error(), "GUARDRAILS.md") {
		t.Errorf("refusal should name the offending path, got: %v", err)
	}
	if got := headHash(t, repo); got != before {
		t.Fatalf("HEAD moved despite refusal: %s", got)
	}
}

// A directory rule protects everything beneath it, which is the form most of
// the real list takes.
func TestMergeRefusesPathMatchedByDirectoryRule(t *testing.T) {
	repo := newGuardedRepo(t, guardList)
	before := headHash(t, repo)
	commit := terrariumCommit(t, repo, func() {
		writeRepoFile(t, repo, "cmd/stem/internal/core/delegation.go", "package core\n")
	})

	err := mergeTerrariumCommit(context.Background(), repo, commit)
	if err == nil {
		t.Fatal("a commit inside a protected directory was merged")
	}
	if !strings.Contains(err.Error(), "cmd/stem/internal/core") {
		t.Errorf("refusal should name the matching rule, got: %v", err)
	}
	if got := headHash(t, repo); got != before {
		t.Fatalf("HEAD moved despite refusal: %s", got)
	}
}

// A repository that declares nothing protected merges normally. This is the
// case for every Substrate that is not this project, and getting it wrong would
// refuse all ordinary work rather than hardening anything.
func TestMergeAllowsWhenNoListIsPresent(t *testing.T) {
	repo := newGuardedRepo(t, "")
	commit := terrariumCommit(t, repo, func() {
		writeRepoFile(t, repo, "GUARDRAILS.md", "no list here, so nothing is protected\n")
	})

	if err := mergeTerrariumCommit(context.Background(), repo, commit); err != nil {
		t.Fatalf("a repository with no list refused a merge: %v", err)
	}
}

// A damaged control must not degrade into an absent one.
func TestMergeRefusesWhenListIsMalformed(t *testing.T) {
	repo := newGuardedRepo(t, "GUARDRAILS.md\n[unclosed\n")
	before := headHash(t, repo)
	commit := terrariumCommit(t, repo, func() {
		writeRepoFile(t, repo, "docs/notes.md", "ordinary work\n")
	})

	err := mergeTerrariumCommit(context.Background(), repo, commit)
	if err == nil {
		t.Fatal("a malformed list allowed the merge; a damaged control must refuse")
	}
	if !strings.Contains(err.Error(), "malformed pattern") {
		t.Errorf("error should identify the malformed pattern, got: %v", err)
	}
	if got := headHash(t, repo); got != before {
		t.Fatalf("HEAD moved despite refusal: %s", got)
	}
}

// The obvious bypass: delete the list in the same commit that edits a kernel
// file. The rules are read from the checkout as it stands before the merge, so
// the commit is judged by the list it is trying to remove.
func TestMergeRefusesCommitThatDeletesTheListWhileEditingKernel(t *testing.T) {
	repo := newGuardedRepo(t, guardList)
	before := headHash(t, repo)
	commit := terrariumCommit(t, repo, func() {
		if err := os.Remove(filepath.Join(repo, protectedPathsFile)); err != nil {
			t.Fatalf("remove list: %v", err)
		}
		writeRepoFile(t, repo, "GUARDRAILS.md", "rewritten by a sprout\n")
	})

	err := mergeTerrariumCommit(context.Background(), repo, commit)
	if err == nil {
		t.Fatal("a commit disabled the guard by deleting the list in the same change")
	}
	if got := headHash(t, repo); got != before {
		t.Fatalf("HEAD moved despite refusal: %s", got)
	}
}

func TestMatchProtectedPathSemantics(t *testing.T) {
	rules, err := loadProtectedPaths(writeListTo(t, "GUARDRAILS.md\nscripts/\ncmd/stem/*.go\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := []struct {
		path      string
		protected bool
	}{
		{"GUARDRAILS.md", true},
		{"docs/GUARDRAILS.md", false},
		{"scripts/check-taxonomy.sh", true},
		{"scripts", true},
		{"scripts-extra/thing.sh", false},
		{"cmd/stem/main.go", true},
		{"cmd/stem/internal/core/delegation.go", false},
		{"readme.md", false},
	}
	for _, tc := range cases {
		got := matchProtectedPath(rules, tc.path) != nil
		if got != tc.protected {
			t.Errorf("%q protected = %v, want %v", tc.path, got, tc.protected)
		}
	}
}

func writeListTo(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	writeRepoFile(t, root, protectedPathsFile, content)
	return root
}

func TestLoadProtectedPathsAbsentIsNotAnError(t *testing.T) {
	rules, err := loadProtectedPaths(t.TempDir())
	if err != nil {
		t.Fatalf("an absent list must not be an error: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected no rules, got %d", len(rules))
	}
}

// The repository's own list must parse, or the guard protects nothing here.
func TestRepositoryListParses(t *testing.T) {
	root := "../../../.."
	if _, err := os.Stat(filepath.Join(root, protectedPathsFile)); err != nil {
		t.Skipf("repository list not reachable from the test working directory: %v", err)
	}
	rules, err := loadProtectedPaths(root)
	if err != nil {
		t.Fatalf("the repository's own protected-paths list does not parse: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("the repository's list parsed to zero rules")
	}
	// The list must protect itself, or it can be removed in passing.
	if matchProtectedPath(rules, protectedPathsFile) == nil {
		t.Error("the protected-paths list does not protect itself")
	}
}
