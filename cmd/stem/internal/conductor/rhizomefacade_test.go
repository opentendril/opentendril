package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The index key is the sharp case: committed into a substrate and merged back,
// a push publishes it. The change set is derived from `git status --porcelain`
// in the mount, which cannot distinguish OpenTendril's writes from the Sprout's.
func TestGeneratedRuntimeArtifactsAreNeverStaged(t *testing.T) {
	for _, path := range []string{
		".tendril/rhizome.key",
		".tendril/rhizome.db",
		".tendril/genome/repomap.md",
		// SQLite writes these beside the database under derived names.
		".tendril/rhizome.db-wal",
		".tendril/rhizome.db-shm",
		".tendril/rhizome.db-journal",
	} {
		if !isGeneratedRuntimeArtifact(path) {
			t.Errorf("isGeneratedRuntimeArtifact(%q) = false; OpenTendril's own state must not reach a commit", path)
		}
		if !shouldIgnoreStagePath(path) {
			t.Errorf("shouldIgnoreStagePath(%q) = false; the staging filter must skip it", path)
		}
	}
}

// The filter must not swallow the Sprout's work. A repository may track its own
// .tendril files, and a task asking to edit one has to survive.
func TestTrackedSubstrateFilesUnderTendrilAreStillStaged(t *testing.T) {
	for _, path := range []string{
		".tendril/config.yaml",
		".tendril/genome/README.md",
		".tendril/genome/naming-conventions.md",
		".tendril/sequences/codex-delegate.yaml",
		"add.go",
	} {
		if isGeneratedRuntimeArtifact(path) {
			t.Errorf("isGeneratedRuntimeArtifact(%q) = true; only OpenTendril's generated state is excluded", path)
		}
		if shouldIgnoreStagePath(path) {
			t.Errorf("shouldIgnoreStagePath(%q) = true; the Sprout's work would be silently dropped", path)
		}
	}
}

// Against a real repository, because the unit cases above were fed paths git
// never produces: `git status --porcelain` collapses an untracked directory to
// a single `?? .tendril/` entry, so a filter reasoning about files matched
// nothing and the whole directory was staged. This drives the actual command.
func TestCollectStageableFilesExcludesGeneratedArtifactsInARealRepository(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if _, err := runGitCommand(context.Background(), repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	// Exactly what a sprout leaves behind: OpenTendril's state beside the
	// Sprout's work, in a repository with no .gitignore.
	if err := os.MkdirAll(filepath.Join(repo, ".tendril", "genome"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for path, content := range map[string]string{
		filepath.Join(".tendril", "rhizome.key"):          "0123456789abcdef0123456789abcdef",
		filepath.Join(".tendril", "rhizome.db"):           "database",
		filepath.Join(".tendril", "genome", "repomap.md"): "# map",
		"agentwork.go": "package main\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	files, err := collectStageableFiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("collectStageableFiles returned error: %v", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file, ".tendril/") {
			t.Errorf("staged OpenTendril's own state %q; a push would publish the index key", file)
		}
	}

	var sawSproutWork bool
	for _, file := range files {
		if file == "agentwork.go" {
			sawSproutWork = true
		}
	}
	if !sawSproutWork {
		t.Fatalf("the Sprout's file was not staged; got %v", files)
	}
}
