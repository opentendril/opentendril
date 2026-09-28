package conductor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/gitpatch"
)

func newGitApplyRepo(t *testing.T) (string, string, string, string) {
	t.Helper()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "feature/apply")
	if err := os.MkdirAll(filepath.Join(repo, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		"a.txt":       []byte("alpha\n"),
		"z.txt":       []byte("zulu\n"),
		"tmp/owned":   []byte("before\n"),
		"payload.bin": bytes.Repeat([]byte{0, 1, 2, 3, 4, 5}, 100),
	} {
		if err := os.WriteFile(filepath.Join(repo, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "add", "a.txt", "z.txt", "tmp/owned", "payload.bin")
	gitIn(t, repo, "commit", "-q", "-m", "base")
	return repo, gitIn(t, repo, "rev-parse", "HEAD"), gitIn(t, repo, "branch", "--show-current"), "demo"
}

func gitApplyPatchFromChanges(t *testing.T, repo string, changes map[string][]byte) []byte {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	clone := exec.Command("git", "clone", "-q", repo, source)
	if output, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone apply patch source: %v (%s)", err, output)
	}
	for name, content := range changes {
		if err := os.WriteFile(filepath.Join(source, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("git", "-C", source, "diff", "--binary", "--")
	patch, err := command.Output()
	if err != nil {
		t.Fatalf("generate Git patch: %v", err)
	}
	if len(patch) == 0 {
		t.Fatal("generated empty patch")
	}
	return patch
}

type gitApplySnapshot struct {
	Head   string
	Branch string
	Status string
	Index  []byte
	Files  map[string]string
}

func snapshotGitApplyWorkspace(t *testing.T, repo string) gitApplySnapshot {
	t.Helper()
	indexPath := gitIn(t, repo, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repo, indexPath)
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	statusCommand := exec.Command("git", "-C", repo, "status", "--porcelain", "-uall", "-z")
	status, err := statusCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]string)
	err = filepath.WalkDir(repo, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(repo, path)
		if relErr != nil {
			return relErr
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == "." {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			files[relative] = "symlink:" + target
		case info.IsDir():
			files[relative] = "directory"
		case info.Mode().IsRegular():
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			files[relative] = string(content)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return gitApplySnapshot{
		Head:   gitIn(t, repo, "rev-parse", "HEAD"),
		Branch: gitIn(t, repo, "branch", "--show-current"),
		Status: string(status),
		Index:  index,
		Files:  files,
	}
}

func runGitApplyForTest(repo, substrate, head string, patch []byte) (GitApplyResult, error) {
	return RunGitApply(context.Background(), GitApplyExecution{
		Workspace: repo, Substrate: substrate, ExpectedHead: head, Patch: patch,
	})
}

func TestRunGitApplyTextPatchLeavesChangesUnstagedAndStateStable(t *testing.T) {
	repo, head, branch, substrate := newGitApplyRepo(t)
	patch := gitApplyPatchFromChanges(t, repo, map[string][]byte{
		"z.txt": []byte("zebra\n"),
		"a.txt": []byte("alpha changed\n"),
	})
	indexPath := gitIn(t, repo, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repo, indexPath)
	}
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runGitApplyForTest(repo, substrate, head, patch)
	if err != nil {
		t.Fatalf("RunGitApply: %v", err)
	}
	if result.Status != "applied" || result.Substrate != substrate || result.Branch != branch || result.Head != head {
		t.Fatalf("result = %+v, want applied on unchanged %s/%s", result, branch, head)
	}
	if !reflect.DeepEqual(result.ChangedPaths, []string{"a.txt", "z.txt"}) {
		t.Fatalf("changed paths = %q, want sorted relative paths [a.txt z.txt]", result.ChangedPaths)
	}
	if got := gitIn(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD changed to %s, want %s", got, head)
	}
	if got := gitIn(t, repo, "branch", "--show-current"); got != branch {
		t.Fatalf("branch changed to %s, want %s", got, branch)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexAfter, indexBefore) {
		t.Fatal("git.apply changed the index")
	}
	if staged := gitIn(t, repo, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("changes became staged: %q", staged)
	}
	if got := string(mustRead(t, filepath.Join(repo, "a.txt"))); got != "alpha changed\n" {
		t.Fatalf("a.txt = %q, want updated content", got)
	}
}

func TestRunGitApplyReportsNewUntrackedFilePath(t *testing.T) {
	repo, head, _, substrate := newGitApplyRepo(t)
	source := filepath.Join(t.TempDir(), "source")
	if output, err := exec.Command("git", "clone", "-q", repo, source).CombinedOutput(); err != nil {
		t.Fatalf("clone apply patch source: %v (%s)", err, output)
	}
	if err := os.WriteFile(filepath.Join(source, "created.txt"), []byte("created\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "add", "-N", "created.txt")
	patch, err := exec.Command("git", "-C", source, "diff", "--binary", "--").Output()
	if err != nil {
		t.Fatal(err)
	}
	result, err := runGitApplyForTest(repo, substrate, head, patch)
	if err != nil {
		t.Fatalf("RunGitApply new file patch: %v", err)
	}
	if !reflect.DeepEqual(result.ChangedPaths, []string{"created.txt"}) {
		t.Fatalf("changed paths = %q, want [created.txt]", result.ChangedPaths)
	}
	if got := string(mustRead(t, filepath.Join(repo, "created.txt"))); got != "created\n" {
		t.Fatalf("created.txt = %q", got)
	}
}

func TestRunGitApplyBinaryPatch(t *testing.T) {
	repo, head, _, substrate := newGitApplyRepo(t)
	changed := bytes.Repeat([]byte{9, 8, 0, 7, 6, 5}, 100)
	patch := gitApplyPatchFromChanges(t, repo, map[string][]byte{"payload.bin": changed})
	if !bytes.Contains(patch, []byte("GIT binary patch")) {
		t.Fatalf("generated patch does not contain a Git binary patch: %s", patch[:min(len(patch), 80)])
	}
	result, err := runGitApplyForTest(repo, substrate, head, patch)
	if err != nil {
		t.Fatalf("RunGitApply binary patch: %v", err)
	}
	if !reflect.DeepEqual(result.ChangedPaths, []string{"payload.bin"}) {
		t.Fatalf("changed paths = %v, want [payload.bin]", result.ChangedPaths)
	}
	if got := mustRead(t, filepath.Join(repo, "payload.bin")); !bytes.Equal(got, changed) {
		t.Fatalf("binary content differs after apply")
	}
}

func TestRunGitApplyRechecksHeadAndBranchAfterPreflight(t *testing.T) {
	for _, mutation := range []string{"HEAD", "branch"} {
		t.Run(mutation, func(t *testing.T) {
			repo, head, branch, substrate := newGitApplyRepo(t)
			original := mustRead(t, filepath.Join(repo, "a.txt"))
			patch := gitApplyPatchFromChanges(t, repo, map[string][]byte{"a.txt": []byte("patch result\n")})
			originalCheck := checkGitApply
			originalApply := applyGitPatch
			var applyCalls int
			checkGitApply = func(ctx context.Context, workspace string, patch []byte) error {
				if err := gitpatch.Check(ctx, workspace, patch); err != nil {
					return err
				}
				if mutation == "HEAD" {
					gitIn(t, workspace, "commit", "-q", "--allow-empty", "-m", "concurrent head change")
				} else {
					gitIn(t, workspace, "checkout", "-q", "-b", "concurrent-branch")
				}
				return nil
			}
			applyGitPatch = func(ctx context.Context, workspace string, patch []byte) error {
				applyCalls++
				return originalApply(ctx, workspace, patch)
			}
			t.Cleanup(func() {
				checkGitApply = originalCheck
				applyGitPatch = originalApply
			})

			if _, err := runGitApplyForTest(repo, substrate, head, patch); err == nil {
				t.Fatal("git.apply accepted workspace identity changed after preflight")
			}
			if applyCalls != 0 {
				t.Fatalf("patch apply called %d times after HEAD/branch changed", applyCalls)
			}
			if got := mustRead(t, filepath.Join(repo, "a.txt")); !bytes.Equal(got, original) {
				t.Fatalf("patch changed a.txt after workspace identity changed: %q", got)
			}
			if mutation == "HEAD" {
				if gitIn(t, repo, "branch", "--show-current") != branch {
					t.Fatal("empty commit unexpectedly changed branch")
				}
				if gitIn(t, repo, "rev-parse", "HEAD") == head {
					t.Fatal("test did not change HEAD during the preflight seam")
				}
			} else if gitIn(t, repo, "branch", "--show-current") == branch {
				t.Fatal("test did not change branch during the preflight seam")
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestRunGitApplyFailuresLeaveWorkspaceUnchanged(t *testing.T) {
	t.Run("stale head", func(t *testing.T) {
		repo, head, _, substrate := newGitApplyRepo(t)
		patch := gitApplyPatchFromChanges(t, repo, map[string][]byte{"a.txt": []byte("new\n")})
		before := snapshotGitApplyWorkspace(t, repo)
		wrong := strings.Repeat("f", len(head))
		if _, err := runGitApplyForTest(repo, substrate, wrong, patch); err == nil {
			t.Fatal("stale expectedHead was accepted")
		}
		if after := snapshotGitApplyWorkspace(t, repo); !reflect.DeepEqual(after, before) {
			t.Fatal("stale expectedHead changed the workspace")
		}
	})

	for _, dirty := range []string{"staged", "unstaged", "untracked"} {
		t.Run(dirty+" workspace", func(t *testing.T) {
			repo, head, _, substrate := newGitApplyRepo(t)
			switch dirty {
			case "staged":
				if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("staged\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitIn(t, repo, "add", "a.txt")
			case "unstaged":
				if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("unstaged\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "untracked":
				if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			patch := gitApplyPatchFromChanges(t, repo, map[string][]byte{"z.txt": []byte("new\n")})
			before := snapshotGitApplyWorkspace(t, repo)
			if _, err := runGitApplyForTest(repo, substrate, head, patch); err == nil {
				t.Fatal("dirty workspace was accepted")
			}
			if after := snapshotGitApplyWorkspace(t, repo); !reflect.DeepEqual(after, before) {
				t.Fatal("dirty-workspace refusal changed the workspace")
			}
		})
	}

	for _, test := range []struct {
		name  string
		patch []byte
	}{
		{name: "malformed", patch: []byte("not a Git patch\n")},
		{name: "inapplicable", patch: []byte("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-not alpha\n+replacement\n")},
		{name: "invalid UTF-8", patch: []byte{0xff, 0xfe}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, head, _, substrate := newGitApplyRepo(t)
			before := snapshotGitApplyWorkspace(t, repo)
			if _, err := runGitApplyForTest(repo, substrate, head, test.patch); err == nil {
				t.Fatal("invalid patch was accepted")
			} else if strings.Contains(err.Error(), string(test.patch)) || strings.Contains(err.Error(), repo) {
				t.Fatalf("unsafe patch/error leakage: %v", err)
			}
			if after := snapshotGitApplyWorkspace(t, repo); !reflect.DeepEqual(after, before) {
				t.Fatal("rejected patch changed the workspace")
			}
		})
	}
}

func TestRunGitApplyRejectsPathEscapesAndSymlinks(t *testing.T) {
	for _, test := range []struct {
		name  string
		patch func(string) []byte
	}{
		{
			name: "absolute path",
			patch: func(string) []byte {
				return []byte("diff --git a/tmp/owned b/tmp/owned\n--- /tmp/owned\n+++ /tmp/owned\n@@ -1 +1 @@\n-before\n+after\n")
			},
		},
		{
			name: "traversal path",
			patch: func(string) []byte {
				return []byte("diff --git a/../outside b/../outside\n--- a/../outside\n+++ b/../outside\n@@ -0,0 +1 @@\n+escape\n")
			},
		},
		{
			name: "symlink escape",
			patch: func(string) []byte {
				return []byte("diff --git a/escape/owned b/escape/owned\nnew file mode 100644\n--- /dev/null\n+++ b/escape/owned\n@@ -0,0 +1 @@\n+escape\n")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, head, _, substrate := newGitApplyRepo(t)
			external := t.TempDir()
			if test.name == "symlink escape" {
				if err := os.Symlink(external, filepath.Join(repo, "escape")); err != nil {
					t.Fatal(err)
				}
				gitIn(t, repo, "add", "escape")
				gitIn(t, repo, "commit", "-q", "-m", "add symlink")
				head = gitIn(t, repo, "rev-parse", "HEAD")
			}
			before := snapshotGitApplyWorkspace(t, repo)
			patch := test.patch(external)
			if _, err := runGitApplyForTest(repo, substrate, head, patch); err == nil {
				t.Fatal("unsafe path was accepted")
			}
			if after := snapshotGitApplyWorkspace(t, repo); !reflect.DeepEqual(after, before) {
				t.Fatal("path containment refusal changed the workspace")
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("path escape wrote outside the workspace: %v", entries)
			}
		})
	}
}

func TestRunGitApplyInjectedApplyFailureIsSafe(t *testing.T) {
	repo, head, _, substrate := newGitApplyRepo(t)
	patch := gitApplyPatchFromChanges(t, repo, map[string][]byte{"a.txt": []byte("changed\n")})
	before := snapshotGitApplyWorkspace(t, repo)
	original := applyGitPatch
	applyGitPatch = func(context.Context, string, []byte) error { return errors.New("patch secret /raw/git/output") }
	t.Cleanup(func() { applyGitPatch = original })
	if _, err := runGitApplyForTest(repo, substrate, head, patch); !errors.Is(err, gitpatch.ErrApplyFailed) {
		t.Fatalf("forced apply error = %v, want safe apply failure", err)
	} else if strings.Contains(err.Error(), "patch secret") || strings.Contains(err.Error(), repo) {
		t.Fatalf("forced apply output leaked: %v", err)
	}
	if after := snapshotGitApplyWorkspace(t, repo); !reflect.DeepEqual(after, before) {
		t.Fatal("injected apply failure changed the workspace")
	}
}
