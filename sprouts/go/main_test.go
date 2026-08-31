package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processState returns the single-letter state from /proc/<pid>/stat, or "" if it
// cannot be read. The comm field is parenthesised and may itself contain spaces
// and parentheses, so the state is taken from after the final ")".
func processState(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	stat := string(data)
	lastParen := strings.LastIndex(stat, ")")
	if lastParen < 0 {
		return ""
	}
	fields := strings.Fields(stat[lastParen+1:])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// waitUntilNotRunning reports whether pid stops running before the timeout.
//
// Killing a process group is asynchronous, and a killed process stays in the
// process table as a zombie until its parent reaps it. Here the orphan's parent
// was killed too, so the orphan is reparented to init and its entry disappears
// only once init reaps it — which is why checking once, or after a fixed sleep,
// races. A zombie satisfies what is actually being asserted (the orphan is
// dead), and accepting it also keeps this honest in a container with no reaping
// init, where an orphan can stay a zombie indefinitely.
func waitUntilNotRunning(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		if processState(pid) == "Z" {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func mkArgs(kvs ...any) map[string]any {
	m := make(map[string]any)
	for i := 0; i < len(kvs); i += 2 {
		m[kvs[i].(string)] = kvs[i+1]
	}
	return m
}

func TestReadFileTool(t *testing.T) {
	ws := t.TempDir()

	t.Run("success", func(t *testing.T) {
		content := "hello world"
		err := os.WriteFile(filepath.Join(ws, "test.txt"), []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
		res := readFileTool(ws, mkArgs("path", "test.txt"))
		if res.Status != "success" {
			t.Fatalf("expected success, got %v: %v", res.Status, res.Error)
		}
		out, ok := res.Output.(readFileOutput)
		if !ok || out.Content != content {
			t.Errorf("unexpected output: %+v", res.Output)
		}
	})

	t.Run("missing path", func(t *testing.T) {
		res := readFileTool(ws, mkArgs())
		if res.Status != "error" || !strings.Contains(res.Error, "requires a path") {
			t.Errorf("expected missing path error, got %v: %v", res.Status, res.Error)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		res := readFileTool(ws, mkArgs("path", "missing.txt"))
		if res.Status != "error" {
			t.Errorf("expected error for missing file, got success")
		}
	})

	t.Run("escapes workspace root", func(t *testing.T) {
		res := readFileTool(ws, mkArgs("path", "../../etc/passwd"))
		if res.Status != "error" || !strings.Contains(res.Error, "escapes the workspace root") {
			t.Errorf("expected escape error, got %v: %v", res.Status, res.Error)
		}
	})
}

func TestWriteFileTool(t *testing.T) {
	ws := t.TempDir()

	t.Run("success overwrite", func(t *testing.T) {
		res1 := writeFileTool(ws, mkArgs("path", "out.txt", "content", "first\n"))
		if res1.Status != "success" {
			t.Fatalf("first write failed: %v", res1.Error)
		}
		res2 := writeFileTool(ws, mkArgs("path", "out.txt", "content", "second\n"))
		if res2.Status != "success" {
			t.Fatalf("second write failed: %v", res2.Error)
		}
		b, err := os.ReadFile(filepath.Join(ws, "out.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "second\n" {
			t.Errorf("expected overwrite, got %q", string(b))
		}
	})

	t.Run("success append", func(t *testing.T) {
		writeFileTool(ws, mkArgs("path", "app.txt", "content", "first\n"))
		res := writeFileTool(ws, mkArgs("path", "app.txt", "content", "second\n", "append", true))
		if res.Status != "success" {
			t.Fatalf("append failed: %v", res.Error)
		}
		b, err := os.ReadFile(filepath.Join(ws, "app.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "first\nsecond\n" {
			t.Errorf("expected appended content, got %q", string(b))
		}
	})

	t.Run("missing content", func(t *testing.T) {
		res := writeFileTool(ws, mkArgs("path", "bad.txt"))
		if res.Status != "error" || !strings.Contains(res.Error, "requires a string content field") {
			t.Errorf("expected missing content error, got %v: %v", res.Status, res.Error)
		}
	})

	t.Run("rejects pseudo-home execution paths", func(t *testing.T) {
		res := writeFileTool(ws, mkArgs("path", "~/tendril/.tendril/run-workspaces/abc/HELLO.md", "content", "nope"))
		if res.Status != "error" || !strings.Contains(res.Error, "repository-relative") {
			t.Fatalf("pseudo-home path was accepted: %+v", res)
		}
	})

	t.Run("rejects run-workspace execution paths", func(t *testing.T) {
		res := writeFileTool(ws, mkArgs("path", ".tendril/run-workspaces/abc/HELLO.md", "content", "nope"))
		if res.Status != "error" || !strings.Contains(res.Error, "repository-relative") {
			t.Fatalf("run-workspace path was accepted: %+v", res)
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		res := writeFileTool(ws, mkArgs("path", "a/b/c/new.txt", "content", "deep"))
		if res.Status != "success" {
			t.Fatalf("write deep file failed: %v", res.Error)
		}
		b, err := os.ReadFile(filepath.Join(ws, "a/b/c/new.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "deep" {
			t.Errorf("expected 'deep', got %q", string(b))
		}
	})
}

func TestListFilesTool(t *testing.T) {
	ws := t.TempDir()

	os.MkdirAll(filepath.Join(ws, "nested"), 0755)
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(ws, "c.txt"), []byte("c"), 0644)
	os.WriteFile(filepath.Join(ws, "nested", "b.txt"), []byte("b"), 0644)

	t.Run("success on populated dir", func(t *testing.T) {
		res := listFilesTool(ws, mkArgs("path", "."))
		if res.Status != "success" {
			t.Fatalf("expected success: %v", res.Error)
		}
		out := res.Output.(listFilesOutput)
		if len(out.Entries) != 4 {
			t.Errorf("expected 4 entries, got %d", len(out.Entries))
		}
		expectedPaths := []string{"a.txt", "c.txt", "nested", "nested/b.txt"}
		for i, expected := range expectedPaths {
			if out.Entries[i].Path != expected {
				t.Errorf("expected entry %d to be %q, got %q", i, expected, out.Entries[i].Path)
			}
		}
	})

	t.Run("confirms skipDirs exclusion", func(t *testing.T) {
		os.MkdirAll(filepath.Join(ws, ".git"), 0755)
		os.WriteFile(filepath.Join(ws, ".git", "config"), []byte("cfg"), 0644)
		res := listFilesTool(ws, mkArgs("path", "."))
		out := res.Output.(listFilesOutput)
		for _, e := range out.Entries {
			if strings.Contains(e.Path, ".git") {
				t.Errorf("expected .git to be excluded, but found %q", e.Path)
			}
		}
	})

	t.Run("maxEntries truncation", func(t *testing.T) {
		res := listFilesTool(ws, mkArgs("path", ".", "maxEntries", 2))
		out := res.Output.(listFilesOutput)
		if !out.Truncated {
			t.Errorf("expected truncation")
		}
		if len(out.Entries) > 2 {
			t.Errorf("expected at most 2 entries, got %d", len(out.Entries))
		}
	})

	t.Run("maxDepth", func(t *testing.T) {
		res := listFilesTool(ws, mkArgs("path", ".", "maxDepth", 1))
		out := res.Output.(listFilesOutput)
		for _, e := range out.Entries {
			if e.Path == "nested/b.txt" {
				t.Errorf("expected maxDepth to exclude nested/b.txt")
			}
		}
	})
}

func setupGitRepo(t *testing.T) string {
	ws := t.TempDir()
	if _, err := runGit(ws, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ws, "config", "user.name", "Test User"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ws, "config", "user.email", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestGitCommitTool(t *testing.T) {
	ws := setupGitRepo(t)

	t.Run("success", func(t *testing.T) {
		os.WriteFile(filepath.Join(ws, "file.txt"), []byte("data"), 0644)
		res := gitCommitTool(ws, mkArgs("message", "initial commit"))
		if res.Status != "success" {
			t.Fatalf("commit failed: %v", res.Error)
		}
		out := res.Output.(commitOutput)
		if !out.Committed || out.Hash == "" {
			t.Errorf("expected commit output: %+v", out)
		}
		logOut, err := runGit(ws, "log", "-1", "--format=%s")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(logOut) != "initial commit" {
			t.Errorf("unexpected commit message in git log: %q", logOut)
		}
	})

	t.Run("nothing to commit", func(t *testing.T) {
		res := gitCommitTool(ws, mkArgs("message", "another commit"))
		if res.Status != "success" {
			t.Fatalf("expected success: %v", res.Error)
		}
		out := res.Output.(commitOutput)
		if out.Committed || out.Message != "nothing to commit" {
			t.Errorf("expected nothing to commit, got: %+v", out)
		}
	})

	t.Run("explicit paths", func(t *testing.T) {
		os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0644)
		os.WriteFile(filepath.Join(ws, "b.txt"), []byte("b"), 0644)
		res := gitCommitTool(ws, mkArgs("message", "commit a", "paths", []any{"a.txt"}))
		if res.Status != "success" {
			t.Fatalf("commit a failed: %v", res.Error)
		}
		statusOut, _ := runGit(ws, "status", "--porcelain")
		if !strings.Contains(statusOut, "?? b.txt") {
			t.Errorf("expected b.txt to remain unstaged: %q", statusOut)
		}
	})
}

func TestGitDiffTool(t *testing.T) {
	ws := setupGitRepo(t)
	os.WriteFile(filepath.Join(ws, "tracked.txt"), []byte("v1\n"), 0644)
	gitCommitTool(ws, mkArgs("message", "init"))

	t.Run("unstaged diff", func(t *testing.T) {
		os.WriteFile(filepath.Join(ws, "tracked.txt"), []byte("v2\n"), 0644)
		res := gitDiffTool(ws, mkArgs())
		if res.Status != "success" {
			t.Fatalf("diff failed: %v", res.Error)
		}
		out := res.Output.(diffOutput)
		if !strings.Contains(out.Diff, "-v1") || !strings.Contains(out.Diff, "+v2") {
			t.Errorf("expected unstaged diff to show change, got: %q", out.Diff)
		}
	})

	t.Run("staged diff", func(t *testing.T) {
		runGit(ws, "add", "tracked.txt")
		resCached := gitDiffTool(ws, mkArgs("cached", true))
		if resCached.Status != "success" {
			t.Fatalf("diff --cached failed: %v", resCached.Error)
		}
		outCached := resCached.Output.(diffOutput)
		if !strings.Contains(outCached.Diff, "-v1") || !strings.Contains(outCached.Diff, "+v2") {
			t.Errorf("expected staged diff to show change, got: %q", outCached.Diff)
		}

		resUnstaged := gitDiffTool(ws, mkArgs())
		outUnstaged := resUnstaged.Output.(diffOutput)
		if outUnstaged.Diff != "" {
			t.Errorf("expected empty unstaged diff, got: %q", outUnstaged.Diff)
		}
	})
}

func TestExecCommandTool(t *testing.T) {
	ws := t.TempDir()

	t.Run("success", func(t *testing.T) {
		res := execCommandTool(ws, mkArgs("command", "echo hello"))
		if res.Status != "success" {
			t.Fatalf("exec failed: %v", res.Error)
		}
		out := res.Output.(commandOutput)
		if strings.TrimSpace(out.Stdout) != "hello" || out.ExitCode != 0 {
			t.Errorf("unexpected output: %+v", out)
		}
	})

	t.Run("non-zero exit", func(t *testing.T) {
		res := execCommandTool(ws, mkArgs("command", "echo out; echo err >&2; exit 3"))
		if res.Status != "error" {
			t.Fatalf("expected error status, got success")
		}
		out := res.Output.(commandOutput)
		if out.ExitCode != 3 {
			t.Errorf("expected exit code 3, got %d", out.ExitCode)
		}
		if strings.TrimSpace(out.Stdout) != "out" {
			t.Errorf("expected stdout 'out', got %q", out.Stdout)
		}
		if strings.TrimSpace(out.Stderr) != "err" {
			t.Errorf("expected stderr 'err', got %q", out.Stderr)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		res := execCommandTool(ws, mkArgs("command", "echo partial; sleep 5", "timeoutSeconds", 1))
		elapsed := time.Since(start)

		if res.Status != "error" {
			t.Fatalf("expected error status, got success")
		}
		if elapsed > 2*time.Second {
			t.Errorf("expected timeout to occur promptly, but it took %v", elapsed)
		}
		out := res.Output.(commandOutput)
		if out.ExitCode != -1 {
			t.Errorf("expected exit code -1, got %d", out.ExitCode)
		}
		if strings.TrimSpace(out.Stdout) != "partial" {
			t.Errorf("expected stdout 'partial', got %q", out.Stdout)
		}
	})

	t.Run("timeout process group kill", func(t *testing.T) {
		pidFile := filepath.Join(ws, "marker.pid")
		// Start a process in the background of the shell script, but still in the same process group.
		// We use a simple marker that saves its PID and sleeps.
		cmdStr := "sleep 5 & echo $! > " + filepath.ToSlash(pidFile) + "; sleep 5"

		start := time.Now()
		res := execCommandTool(ws, mkArgs("command", cmdStr, "timeoutSeconds", 1))
		elapsed := time.Since(start)

		if res.Status != "error" {
			t.Fatalf("expected error status, got success")
		}
		if elapsed > 2*time.Second {
			t.Errorf("expected timeout to happen quickly, but it took %v", elapsed)
		}

		pidBytes, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatalf("failed to read marker pid: %v", err)
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			t.Fatalf("invalid marker pid: %v", err)
		}

		if !waitUntilNotRunning(pid, 5*time.Second) {
			t.Errorf("process %d is still running after the timeout, process group was not killed", pid)
		}
	})

	t.Run("cwd argument", func(t *testing.T) {
		os.MkdirAll(filepath.Join(ws, "subdir"), 0755)
		res := execCommandTool(ws, mkArgs("command", "pwd", "cwd", "subdir"))
		if res.Status != "success" {
			t.Fatalf("exec failed: %v", res.Error)
		}
		out := res.Output.(commandOutput)
		if !strings.HasSuffix(strings.TrimSpace(out.Stdout), "subdir") {
			t.Errorf("expected stdout to end with 'subdir', got %q", out.Stdout)
		}
	})
}

func TestExecuteToolDispatch(t *testing.T) {
	ws := t.TempDir()

	t.Run("empty tool name", func(t *testing.T) {
		res := executeTool(ws, toolCall{Tool: ""})
		if res.Status != "error" || !strings.Contains(res.Error, "tool name is required") {
			t.Errorf("unexpected error: %v", res.Error)
		}
	})

	t.Run("unknown tool name", func(t *testing.T) {
		res := executeTool(ws, toolCall{Tool: "unknown_tool"})
		if res.Status != "error" || !strings.Contains(res.Error, `unsupported tool "unknown_tool"`) {
			t.Errorf("unexpected error: %v", res.Error)
		}
	})

	t.Run("catalog dispatch parity", func(t *testing.T) {
		tools := availableTools()
		for _, def := range tools {
			res := executeTool(ws, toolCall{Tool: def.Name, Arguments: mkArgs()})
			if res.Status == "error" && strings.Contains(res.Error, "unsupported tool") {
				t.Errorf("catalog advertises %q but dispatch does not handle it", def.Name)
			}
		}
	})
}
