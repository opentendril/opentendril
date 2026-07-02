package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloneForeignSubstrate(t *testing.T) {
	// Use a known public repository that is small
	url := "https://github.com/torvalds/test-tlb.git"
	branch := "master"

	shadowPath, err := cloneForeignSubstrate(url, branch)
	if err != nil {
		t.Fatalf("cloneForeignSubstrate failed: %v", err)
	}

	// Clean up after test
	defer os.RemoveAll(shadowPath)

	// Verify the directory exists
	info, err := os.Stat(shadowPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("Shadow path %s was not created or is not a directory", shadowPath)
	}

	// Verify it contains a .git directory (is a valid clone)
	gitPath := filepath.Join(shadowPath, ".git")
	gitInfo, err := os.Stat(gitPath)
	if err != nil || !gitInfo.IsDir() {
		t.Fatalf(".git directory not found in shadow path, clone failed")
	}
}

func TestShouldIgnoreStagePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "log file", path: "logs/run.log", want: true},
		{name: "cache dir", path: ".cache/index", want: true},
		{name: "build dir", path: "build/output.bin", want: true},
		{name: "tmp dir", path: "tmp/work/file.txt", want: true},
		{name: "pycache", path: "pkg/__pycache__/module.cpython-312.pyc", want: true},
		{name: "source file", path: "cmd/main.go", want: false},
		{name: "markdown file", path: "docs/notes.md", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIgnoreStagePath(tt.path); got != tt.want {
				t.Fatalf("shouldIgnoreStagePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestWorkspaceRelativePath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "pkg", "service", "handler.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}

	rel, err := workspaceRelativePath(root, target)
	if err != nil {
		t.Fatalf("workspaceRelativePath returned error: %v", err)
	}
	if rel != "pkg/service/handler.go" {
		t.Fatalf("workspaceRelativePath = %q, want %q", rel, "pkg/service/handler.go")
	}

	if _, err := workspaceRelativePath(root, filepath.Join(root, "..", "escape.go")); err == nil {
		t.Fatalf("expected escaping path to fail")
	}
}

func TestTendrilStatusRoundTrip(t *testing.T) {
	root := t.TempDir()
	statusPath := filepath.Join(root, "tendril-status.json")
	record := tendrilExecutionStatus{
		StepID:        "step-123",
		Status:        "failed",
		Error:         "pytest failed",
		Timestamp:     "2026-06-29T00:00:00Z",
		FilesModified: []string{"cmd/main.go", "docs/notes.md"},
	}

	if err := writeTendrilStatus(statusPath, record); err != nil {
		t.Fatalf("writeTendrilStatus failed: %v", err)
	}

	loaded, err := loadTendrilStatus(statusPath)
	if err != nil {
		t.Fatalf("loadTendrilStatus failed: %v", err)
	}
	if loaded == nil {
		t.Fatalf("expected status record, got nil")
	}
	if loaded.StepID != record.StepID || loaded.Status != record.Status || loaded.Error != record.Error || loaded.Timestamp != record.Timestamp {
		t.Fatalf("loaded status mismatch: %#v", loaded)
	}
	if strings.Join(loaded.FilesModified, ",") != strings.Join(record.FilesModified, ",") {
		t.Fatalf("loaded files mismatch: %#v", loaded.FilesModified)
	}
}

func TestBuildTendrilCommitMessage(t *testing.T) {
	success := buildTendrilCommitMessage("step-1", "refactor the cache layer with a cleaner boundary", "complete", "")
	if !strings.HasPrefix(success, "tendril(step-1): ") {
		t.Fatalf("unexpected success message: %s", success)
	}

	failure := buildTendrilCommitMessage("step-2", "ignored", "failed", "pytest: 3 tests failed")
	if !strings.Contains(failure, "[INCOMPLETE]: pytest: 3 tests failed") {
		t.Fatalf("unexpected failure message: %s", failure)
	}
}

func TestHostWorkspaceStashRoundTrip(t *testing.T) {
	repo := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (output: %s)", args, err, strings.TrimSpace(string(output)))
		}
	}

	seedPath := filepath.Join(repo, "seed.txt")
	if err := os.WriteFile(seedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "add", "seed.txt"); err != nil {
		t.Fatalf("stage seed: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	if err := os.WriteFile(seedPath, []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	untrackedPath := filepath.Join(repo, "scratch.txt")
	if err := os.WriteFile(untrackedPath, []byte("scratch\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	stashed, err := stashHostWorkspace(context.Background(), repo, "step-123")
	if err != nil {
		t.Fatalf("stashHostWorkspace failed: %v", err)
	}
	if !stashed {
		t.Fatalf("expected stashHostWorkspace to stash dirty repo")
	}

	cleanStatus, err := runGitCommand(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status after stash: %v", err)
	}
	if strings.TrimSpace(cleanStatus) != "" {
		t.Fatalf("expected clean repo after stash, got %q", cleanStatus)
	}

	if err := restoreHostStash(context.Background(), repo); err != nil {
		t.Fatalf("restoreHostStash failed: %v", err)
	}

	restoredStatus, err := runGitCommand(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status after restore: %v", err)
	}
	if strings.TrimSpace(restoredStatus) == "" {
		t.Fatalf("expected dirty repo after stash pop, got clean status")
	}
}

func TestStagePlasmidsForGenotype(t *testing.T) {
	root := t.TempDir()
	genotypesDir := filepath.Join(root, ".tendril", "genotypes")
	plasmidsDir := filepath.Join(genotypesDir, "plasmids")
	if err := os.MkdirAll(plasmidsDir, 0o755); err != nil {
		t.Fatalf("mkdir plasmids dir: %v", err)
	}

	writeJSONFile(t, filepath.Join(genotypesDir, "frontend-dev.json"), map[string]any{
		"name":         "frontend-dev",
		"instructions": "write React code",
		"plasmids":     []string{"react-conventions", "tailwind-styling"},
	})

	reactContent := "# React Conventions\nUse functional components.\n"
	tailwindContent := "# Tailwind Styling\nUse flexbox layouts.\n"

	if err := os.WriteFile(filepath.Join(plasmidsDir, "react-conventions.md"), []byte(reactContent), 0o644); err != nil {
		t.Fatalf("write react-conventions plasmid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plasmidsDir, "tailwind-styling.md"), []byte(tailwindContent), 0o644); err != nil {
		t.Fatalf("write tailwind-styling plasmid: %v", err)
	}

	destRoot := t.TempDir()
	stagePlasmidsForGenotype(root, destRoot, "frontend-dev")

	reactDest := filepath.Join(destRoot, ".tendril", "genome", "react-conventions.md")
	tailwindDest := filepath.Join(destRoot, ".tendril", "genome", "tailwind-styling.md")

	if _, err := os.Stat(reactDest); err != nil {
		t.Fatalf("expected react-conventions plasmid to be staged in terrarium: %v", err)
	}
	if _, err := os.Stat(tailwindDest); err != nil {
		t.Fatalf("expected tailwind-styling plasmid to be staged in terrarium: %v", err)
	}

	c1, _ := os.ReadFile(reactDest)
	c2, _ := os.ReadFile(tailwindDest)

	if string(c1) != reactContent {
		t.Fatalf("react conventions content mismatch, got %q", string(c1))
	}
	if string(c2) != tailwindContent {
		t.Fatalf("tailwind styling content mismatch, got %q", string(c2))
	}
}

func writeJSONFile(t *testing.T, path string, payload map[string]any) {
	t.Helper()

	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	content = append(content, '\n')

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

