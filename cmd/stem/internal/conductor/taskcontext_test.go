package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
)

type taskContextTestIndex struct {
	symbols []rhizome.Symbol
	files   map[string]rhizome.FileRecord
	queries []string
}

func (f *taskContextTestIndex) GetFile(_ context.Context, _ string, path string) (rhizome.FileRecord, bool, error) {
	record, ok := f.files[path]
	return record, ok, nil
}

func (f *taskContextTestIndex) SearchSymbols(_ context.Context, _ string, query string, _ int) ([]rhizome.Symbol, error) {
	f.queries = append(f.queries, query)
	return append([]rhizome.Symbol(nil), f.symbols...), nil
}

func writeTaskContextFile(t *testing.T, root, path, content string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assembleTaskContextForTest(t *testing.T, root, prompt string, index taskContextRhizomeIndex) taskContextAssembly {
	t.Helper()
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:         prompt,
		SourceRepository:   root,
		ExecutionWorkspace: root,
		StartingRevision:   "revision-1",
	}, index, "fixture")
	if err != nil {
		t.Fatalf("assembleTaskContext failed: %v", err)
	}
	return assembly
}

func TestTaskContextExactFileAnchorsHavePriority(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "anchor.go", "package fixture\n\nfunc Anchor() {}\n")
	writeTaskContextFile(t, root, "symbol.go", "package fixture\n\nfunc Symbol() {}\n")
	index := &taskContextTestIndex{
		symbols: []rhizome.Symbol{{Name: "Symbol", Type: "function", FilePath: "symbol.go", LineStart: 3, LineEnd: 3}},
		files:   map[string]rhizome.FileRecord{"symbol.go": {Hash: taskContextContentIdentity([]byte("package fixture\n\nfunc Symbol() {}\n"))}},
	}

	assembly := assembleTaskContextForTest(t, root, "Please inspect anchor.go and Symbol", index)
	if len(assembly.Manifest.Items) == 0 || assembly.Manifest.Items[0].Path != "anchor.go" {
		t.Fatalf("exact file anchor was not admitted first: %+v", assembly.Manifest.Items)
	}
	if !strings.Contains(assembly.Rendered, "Anchor()") {
		t.Fatalf("anchor content missing from rendered context: %s", assembly.Rendered)
	}
}

func TestTaskContextDifferentTranscriptsSelectDifferentEvidence(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "first.go", "first evidence\n")
	writeTaskContextFile(t, root, "second.go", "second evidence\n")

	first := assembleTaskContextForTest(t, root, "inspect first.go", nil)
	second := assembleTaskContextForTest(t, root, "inspect second.go", nil)
	if first.Rendered == second.Rendered {
		t.Fatalf("different transcripts selected identical context: %q", first.Rendered)
	}
	if !strings.Contains(first.Rendered, "first evidence") || strings.Contains(first.Rendered, "second evidence") {
		t.Fatalf("first transcript selected wrong evidence: %s", first.Rendered)
	}
	if !strings.Contains(second.Rendered, "second evidence") || strings.Contains(second.Rendered, "first evidence") {
		t.Fatalf("second transcript selected wrong evidence: %s", second.Rendered)
	}
}

func TestTaskContextOrderingIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "a.go", "a\n")
	writeTaskContextFile(t, root, "b.go", "b\n")

	first := assembleTaskContextForTest(t, root, "inspect b.go and a.go", nil)
	second := assembleTaskContextForTest(t, root, "inspect b.go and a.go", nil)
	if first.Rendered != second.Rendered {
		t.Fatalf("identical inputs produced different context:\nfirst=%q\nsecond=%q", first.Rendered, second.Rendered)
	}
	if len(first.Manifest.Items) != 2 || first.Manifest.Items[0].Path != "a.go" || first.Manifest.Items[1].Path != "b.go" {
		t.Fatalf("context ordering is not stable: %+v", first.Manifest.Items)
	}
}

func TestTaskContextRawFTSSpecialInputIsQuotedAndBounded(t *testing.T) {
	root := t.TempDir()
	index := &taskContextTestIndex{}
	assembly := assembleTaskContextForTest(t, root, `find foo" OR bar NEAR baz -- not raw`, index)
	if assembly.Rendered != "" {
		t.Fatalf("empty Rhizome results should produce empty context, got %q", assembly.Rendered)
	}
	if len(index.queries) == 0 {
		t.Fatal("expected lexical Rhizome queries")
	}
	for _, query := range index.queries {
		if !strings.HasPrefix(query, `"`) || !strings.HasSuffix(query, `"`) {
			t.Fatalf("unsafe FTS query %q", query)
		}
		if strings.ContainsAny(query, "(){}[]:*+-") || strings.Contains(query, " OR ") || strings.Contains(query, " NEAR ") {
			t.Fatalf("raw FTS syntax escaped lexical query semantics: %q", query)
		}
	}
}

func TestTaskContextRefusesTraversalSymlinkAndProtectedPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTaskContextFile(t, outside, "outside.go", "outside secret\n")
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(root, "escape.go")); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}
	writeTaskContextFile(t, root, ".git/config", "private\n")
	writeTaskContextFile(t, root, ".tendril/genome/repomap.md", "generated\n")
	writeTaskContextFile(t, root, ".env", "TOKEN=secret\n")
	writeTaskContextFile(t, root, "id_rsa", "private key\n")

	assembly := assembleTaskContextForTest(t, root, "../outside.go escape.go .git/config .tendril/genome/repomap.md .env id_rsa", nil)
	if len(assembly.Manifest.Items) != 0 || assembly.Rendered != "" {
		t.Fatalf("unsafe paths were admitted: %+v / %q", assembly.Manifest.Items, assembly.Rendered)
	}
}

func TestTaskContextOmitsStaleRhizomeSymbols(t *testing.T) {
	root := t.TempDir()
	content := "package fixture\n\nfunc Current() {}\n"
	writeTaskContextFile(t, root, "current.go", content)
	index := &taskContextTestIndex{
		symbols: []rhizome.Symbol{{Name: "Current", Type: "function", FilePath: "current.go", LineStart: 3, LineEnd: 3}},
		files:   map[string]rhizome.FileRecord{"current.go": {Hash: "stale-hash"}},
	}

	assembly := assembleTaskContextForTest(t, root, "inspect Current", index)
	if len(assembly.Manifest.Items) != 0 {
		t.Fatalf("stale Rhizome symbol was admitted: %+v", assembly.Manifest.Items)
	}
}

func TestTaskContextAdmitsExactCurrentRhizomeEvidence(t *testing.T) {
	root := t.TempDir()
	content := "package fixture\n\nfunc Current() {}\n"
	writeTaskContextFile(t, root, "current.go", content)
	index := &taskContextTestIndex{
		symbols: []rhizome.Symbol{{Name: "Current", Type: "function", FilePath: "current.go", LineStart: 3, LineEnd: 3}},
		files:   map[string]rhizome.FileRecord{"current.go": {Hash: taskContextContentIdentity([]byte(content))}},
	}

	assembly := assembleTaskContextForTest(t, root, "inspect Current", index)
	if len(assembly.Manifest.Items) != 1 || assembly.Manifest.Items[0].ContentIdentity != taskContextContentIdentity([]byte(content)) {
		t.Fatalf("current Rhizome evidence was not admitted with current identity: %+v", assembly.Manifest.Items)
	}
	if !strings.Contains(assembly.Rendered, "func Current()") {
		t.Fatalf("current source was not rendered: %s", assembly.Rendered)
	}
}

func TestTaskContextBudgetsTruncateItemsAndStopAdmission(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "a.go", "package fixture\n\n"+strings.Repeat("a", 300)+"\n")
	writeTaskContextFile(t, root, "b.go", "package fixture\n\n"+strings.Repeat("b", 300)+"\n")
	writeTaskContextFile(t, root, "c.go", "package fixture\n\n"+strings.Repeat("c", 300)+"\n")
	t.Setenv(taskContextMaxBytesEnv, "180")
	t.Setenv(taskContextItemMaxBytesEnv, "64")
	t.Setenv(taskContextMaxItemsEnv, "2")

	assembly := assembleTaskContextForTest(t, root, "a.go b.go c.go", nil)
	if len(assembly.Rendered) > 180 {
		t.Fatalf("aggregate budget exceeded: %d", len(assembly.Rendered))
	}
	if len(assembly.Manifest.Items) != 2 {
		t.Fatalf("item-count budget not enforced: %d items", len(assembly.Manifest.Items))
	}
	for _, item := range assembly.Manifest.Items {
		if item.Bytes > 64 {
			t.Fatalf("per-item budget exceeded: %+v", item)
		}
	}
}

func TestTaskContextInvalidEnvironmentFailsClosed(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		env  map[string]string
	}{
		{name: "zero", env: map[string]string{taskContextMaxBytesEnv: "0"}},
		{name: "negative", env: map[string]string{taskContextMaxItemsEnv: "-1"}},
		{name: "hard bytes", env: map[string]string{taskContextMaxBytesEnv: "8193"}},
		{name: "hard items", env: map[string]string{taskContextMaxItemsEnv: "33"}},
		{name: "item aggregate", env: map[string]string{taskContextMaxBytesEnv: "128", taskContextItemMaxBytesEnv: "129"}},
		{name: "not integer", env: map[string]string{taskContextItemMaxBytesEnv: "1.5"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(taskContextMaxBytesEnv, "")
			t.Setenv(taskContextItemMaxBytesEnv, "")
			t.Setenv(taskContextMaxItemsEnv, "")
			for key, value := range testCase.env {
				t.Setenv(key, value)
			}
			if _, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{ExecutionWorkspace: root}, nil, ""); err == nil {
				t.Fatal("invalid environment configuration unexpectedly succeeded")
			}
		})
	}
}

func TestTaskContextGitStatePathsAreBoundedEvidence(t *testing.T) {
	root := t.TempDir()
	if _, err := runGitCommand(context.Background(), root, "init", "-q"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.name", "Task Context Test"); err != nil {
		t.Fatalf("git user.name: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.email", "task-context@example.invalid"); err != nil {
		t.Fatalf("git user.email: %v", err)
	}
	writeTaskContextFile(t, root, "tracked.go", "original\n")
	if _, err := runGitCommand(context.Background(), root, "add", "tracked.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "commit", "-m", "fixture"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	writeTaskContextFile(t, root, "tracked.go", "changed\n")

	assembly := assembleTaskContextForTest(t, root, "show current changes", nil)
	if len(assembly.Manifest.Items) != 1 || assembly.Manifest.Items[0].Path != "tracked.go" {
		t.Fatalf("current Git-state path was not selected: %+v", assembly.Manifest.Items)
	}
	if !strings.Contains(assembly.Rendered, "changed") {
		t.Fatalf("current Git-state content missing: %s", assembly.Rendered)
	}
}

func TestTaskContextGitRenameUsesCurrentDestinationPath(t *testing.T) {
	root := t.TempDir()
	if _, err := runGitCommand(context.Background(), root, "init", "-q"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.name", "Task Context Test"); err != nil {
		t.Fatalf("git user.name: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "config", "user.email", "task-context@example.invalid"); err != nil {
		t.Fatalf("git user.email: %v", err)
	}
	writeTaskContextFile(t, root, "old.go", "renamed evidence\n")
	if _, err := runGitCommand(context.Background(), root, "add", "old.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "commit", "-m", "fixture"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	if _, err := runGitCommand(context.Background(), root, "mv", "old.go", "new.go"); err != nil {
		t.Fatalf("git mv: %v", err)
	}

	assembly := assembleTaskContextForTest(t, root, "show current changes", nil)
	if len(assembly.Manifest.Items) != 1 || assembly.Manifest.Items[0].Path != "new.go" {
		t.Fatalf("Git rename selected the wrong path: %+v", assembly.Manifest.Items)
	}
}
