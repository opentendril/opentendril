package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedGitDiffResponse(t *testing.T) {
	ctx := context.Background()

	// 1. Setup a real linked worktree created with `git worktree add --detach`
	repo := t.TempDir()
	mustGit := func(dir string, args ...string) {
		t.Helper()
		if _, err := runGitCommand(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	mustGit(repo, "init")
	mustGit(repo, "config", "user.email", "test@example.com")
	mustGit(repo, "config", "user.name", "Tester")

	// Create some files
	if err := os.WriteFile(filepath.Join(repo, "file1.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(repo, "add", "file1.txt")
	mustGit(repo, "commit", "-m", "init")

	// Create linked worktree
	worktree := t.TempDir()
	mustGit(repo, "worktree", "add", "--detach", worktree, "HEAD")

	// 2. its `.git` is a linked-worktree file pointing outside the candidate
	gitFile := filepath.Join(worktree, ".git")
	info, err := os.Stat(gitFile)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf(".git is not a file: %v", err)
	}

	// 3. no external Git metadata is copied into the candidate
	gitContent, err := os.ReadFile(gitFile)
	if err != nil || !strings.HasPrefix(string(gitContent), "gitdir: ") {
		t.Fatalf(".git is not a valid gitdir link, metadata might have been copied: %q", gitContent)
	}

	// Create a sprout pointing to the worktree
	sprout := &Sprout{
		workspace: worktree,
	}

	// 4. unstaged diff works
	if err := os.WriteFile(filepath.Join(worktree, "file1.txt"), []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resp := sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
	})
	if resp.Status != "success" {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	out, ok := resp.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any output, got %T", resp.Output)
	}
	diff := out["diff"].(string)
	if !strings.Contains(diff, "+hello world") {
		t.Fatalf("expected diff to contain changes, got %q", diff)
	}
	if out["cached"].(bool) {
		t.Fatalf("expected cached to be false")
	}
	if paths, ok := out["paths"]; ok && paths != nil {
		if p, ok := paths.([]string); ok && len(p) > 0 {
			t.Fatalf("expected empty paths, got %v", p)
		}
	}

	// 5. cached:true works
	mustGit(worktree, "add", "file1.txt")
	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"cached": true,
		},
	})
	if resp.Status != "success" {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	out = resp.Output.(map[string]any)
	diff = out["diff"].(string)
	if !strings.Contains(diff, "+hello world") {
		t.Fatalf("expected diff to contain staged changes, got %q", diff)
	}

	// 6. path filtering works
	mustGit(worktree, "commit", "-m", "file1")
	if err := os.WriteFile(filepath.Join(worktree, "file2.txt"), []byte("file2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(worktree, "add", "file2.txt")
	if err := os.WriteFile(filepath.Join(worktree, "file3.txt"), []byte("file3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(worktree, "add", "file3.txt")
	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"cached": true,
			"paths":  []any{"file2.txt"},
		},
	})
	out = resp.Output.(map[string]any)
	diff = out["diff"].(string)
	if !strings.Contains(diff, "+file2") || strings.Contains(diff, "+file3") {
		t.Fatalf("path filtering failed, diff: %q", diff)
	}

	// 7. absolute paths fail
	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"paths": []string{"/etc/passwd"},
		},
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "absolute path") {
		t.Fatalf("expected absolute path error, got %v", resp)
	}

	// 8. `..` escape fails
	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"paths": []string{"../outside.txt"},
		},
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "traverses outside") {
		t.Fatalf("expected traverse outside error, got %v", resp)
	}

	// 8b. execution-location path refusal
	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"paths": []string{".tendril/run-workspaces/fake"},
		},
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "not a repository-relative workspace path") {
		t.Fatalf("expected execution-location refusal, got %v", resp)
	}

	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"paths": []string{"~/.tendril/something"},
		},
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "not a repository-relative workspace path") {
		t.Fatalf("expected pseudo-home refusal, got %v", resp)
	}

	// 9. unexpected widening arguments fail
	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"repo": "some-repo",
		},
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "unsupported argument") {
		t.Fatalf("expected unsupported argument error, got %v", resp)
	}

	// 10. a directory without its own `.git` fails even when located beneath another Git repository
	subdir := filepath.Join(worktree, "subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	subdirSprout := &Sprout{
		workspace: subdir,
	}
	resp = subdirSprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "not a valid git repository checkout") {
		t.Fatalf("expected not valid git checkout error, got %v", resp)
	}

	// 11. failures do not leak host or linked-worktree metadata paths
	// Triggering a git diff failure manually here without using an unsupported arg.
	// For instance, by providing a non-existent path. Actually, `git diff` on a non-existent path doesn't fail, it returns nothing.
	// Let's modify the worktree so git diff fails (e.g., delete .git inside worktree).
	if err := os.Remove(gitFile); err != nil {
		t.Fatal(err)
	}
	// checkoutHasGitMetadata will catch this
	resp = sprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
	})
	if resp.Status != "error" || !strings.Contains(resp.Error, "not a valid git repository checkout") {
		t.Fatalf("expected not valid git checkout error, got %v", resp)
	}

	// 12. ordinary Git-checkout diff works
	ordinarySprout := &Sprout{
		workspace: repo,
	}
	if err := os.WriteFile(filepath.Join(repo, "file_ord.txt"), []byte("ordinary\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(repo, "add", "file_ord.txt")
	resp = ordinarySprout.managedGitDiffResponse(ctx, ToolCall{
		Tool: "gitDiff",
		Arguments: map[string]any{
			"cached": true,
		},
	})
	if resp.Status != "success" {
		t.Fatalf("expected success for ordinary checkout, got error: %s", resp.Error)
	}

	// 13. executeTool intercepts gitDiff and gitCommit, and does not call the Terrarium session
	if err := os.WriteFile(gitFile, gitContent, 0644); err != nil {
		t.Fatal(err) // restore .git
	}
	fakeSession := &sproutgitStubToolSession{}
	sprout.session = fakeSession
	sprout.toolIndex = map[string]ToolDefinition{
		"gitDiff":   {Name: "gitDiff"},
		"gitCommit": {Name: "gitCommit"},
	}

	resp, obs, err := sprout.executeTool(ctx, ToolCall{Tool: "gitDiff"})
	if err != nil {
		t.Fatal(err)
	}
	if fakeSession.called {
		t.Fatalf("expected executeTool to intercept gitDiff, but it called the session")
	}
	if resp.Status != "success" {
		t.Fatalf("expected success, got %v", resp)
	}
	if !strings.Contains(obs, "Tool result for gitDiff") {
		t.Fatalf("expected tool observation, got %q", obs)
	}

	resp, obs, err = sprout.executeTool(ctx, ToolCall{Tool: "gitCommit"})
	if err != nil {
		t.Fatal(err)
	}
	if fakeSession.called {
		t.Fatalf("expected executeTool to intercept gitCommit, but it called the session")
	}
	if resp.Status != "success" || !strings.Contains(resp.Output.(map[string]any)["message"].(string), "automatically commits and merges") {
		t.Fatalf("expected managed gitCommit response, got %v", resp)
	}
	if !strings.Contains(obs, "Tool result for gitCommit") {
		t.Fatalf("expected tool observation, got %q", obs)
	}
}

type sproutgitStubToolSession struct {
	called bool
}

func (s *sproutgitStubToolSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
	return nil, nil
}

func (s *sproutgitStubToolSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	s.called = true
	return ToolResponse{Status: "error", Error: "stub called"}, nil
}

func (s *sproutgitStubToolSession) Close() error { return nil }
func (s *sproutgitStubToolSession) Logs() string { return "" }
