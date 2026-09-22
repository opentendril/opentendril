package conductor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
	"github.com/opentendril/opentendril/cmd/stem/internal/telemetry"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

type taskContextTestIndex struct {
	symbols []rhizome.Symbol
	files   map[string]rhizome.FileRecord
	queries []string
}

type taskContextTestMemoryIndex struct {
	memories []rhizome.Memory
	queries  []string
	stored   []rhizome.Memory
	err      error
	storeErr error
}

type taskContextTranscriptCapture struct {
	inner      sproutRunner
	transcript *string
}

func (c *taskContextTranscriptCapture) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	result, err := c.inner.Run(ctx, taskPrompt)
	*c.transcript = result.Transcript
	return result, err
}

func (f *taskContextTestMemoryIndex) SearchMemories(_ context.Context, repositoryName, query, _ string, _ int) ([]rhizome.Memory, error) {
	f.queries = append(f.queries, repositoryName+"\x00"+query)
	if f.err != nil {
		return nil, f.err
	}
	return append([]rhizome.Memory(nil), f.memories...), nil
}

func (f *taskContextTestMemoryIndex) StoreMemory(_ context.Context, memory rhizome.Memory) error {
	if f.storeErr != nil {
		return f.storeErr
	}
	f.stored = append(f.stored, memory)
	return nil
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

func TestTaskContextExactSymbolMatchesHavePriority(t *testing.T) {
	root := t.TempDir()
	exactContent := "package fixture\n\nfunc ExactSymbol() {}\n"
	broadContent := "package fixture\n\nfunc BroadSymbol() {}\n"
	writeTaskContextFile(t, root, "z-exact.go", exactContent)
	writeTaskContextFile(t, root, "a-broad.go", broadContent)
	index := &taskContextTestIndex{
		symbols: []rhizome.Symbol{
			{Name: "ExactSymbol", Type: "function", FilePath: "z-exact.go", LineStart: 3, LineEnd: 3},
			{Name: "BroadSymbol", Type: "function", FilePath: "a-broad.go", LineStart: 3, LineEnd: 3},
		},
		files: map[string]rhizome.FileRecord{
			"z-exact.go": {Hash: taskContextContentIdentity([]byte(exactContent))},
			"a-broad.go": {Hash: taskContextContentIdentity([]byte(broadContent))},
		},
	}

	assembly := assembleTaskContextForTest(t, root, "inspect ExactSymbol Broad", index)
	if len(assembly.Manifest.Items) < 2 {
		t.Fatalf("expected exact and broad symbol candidates: %+v", assembly.Manifest.Items)
	}
	if assembly.Manifest.Items[0].Path != "z-exact.go" || assembly.Manifest.Items[1].Path != "a-broad.go" {
		t.Fatalf("exact symbol priority was lost to alphabetical order: %+v", assembly.Manifest.Items)
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

func TestTaskContextOversizedSourceReadRetainsOnlyEvidenceBudget(t *testing.T) {
	root := t.TempDir()
	content := "package fixture\n\n" + strings.Repeat("x", 1<<20) + "\n"
	writeTaskContextFile(t, root, "large.go", content)

	canonical, err := taskContextWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("canonicalize fixture workspace: %v", err)
	}
	workspace, err := os.OpenRoot(canonical)
	if err != nil {
		t.Fatalf("open fixture workspace root: %v", err)
	}
	defer workspace.Close()

	source, err := readTaskContextSourceFile(workspace, "large.go", 64, nil)
	if err != nil {
		t.Fatalf("read oversized fixture: %v", err)
	}
	if len(source.content) > 64 {
		t.Fatalf("oversized source retained %d bytes, want at most 64", len(source.content))
	}
	if source.hash != taskContextContentIdentity([]byte(content)) {
		t.Fatalf("oversized source hash mismatch: got %s", source.hash)
	}

	symbolContent := "package fixture\n\nfunc Target() {}\n" + strings.Repeat("y", 1<<20) + "\n"
	writeTaskContextFile(t, root, "symbol-large.go", symbolContent)
	symbolSource, err := readTaskContextSourceFile(workspace, "symbol-large.go", 64, []rhizome.Symbol{{Name: "Target", LineStart: 3, LineEnd: 3}})
	if err != nil {
		t.Fatalf("read oversized symbol fixture: %v", err)
	}
	if len(symbolSource.content) > 64 || !strings.Contains(string(symbolSource.content), "func Target()") || strings.Contains(string(symbolSource.content), "yyyy") {
		t.Fatalf("symbol evidence was not bounded to the indexed lines: %q", string(symbolSource.content))
	}
	if symbolSource.hash != taskContextContentIdentity([]byte(symbolContent)) {
		t.Fatalf("oversized symbol source hash mismatch: got %s", symbolSource.hash)
	}

	t.Setenv(taskContextMaxBytesEnv, "128")
	t.Setenv(taskContextItemMaxBytesEnv, "64")
	t.Setenv(taskContextMaxItemsEnv, "1")
	assembly := assembleTaskContextForTest(t, root, "inspect large.go", nil)
	if len(assembly.Manifest.Items) != 1 || assembly.Manifest.Items[0].Bytes > 64 {
		t.Fatalf("oversized source exceeded admitted evidence budget: %+v", assembly.Manifest.Items)
	}
}

func TestTaskContextCanonicalizesSourceAndExecutionWorkspace(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeTaskContextFile(t, workspace, "current.go", "workspace evidence\n")

	sourceAlias := filepath.Join(t.TempDir(), "source-link")
	if err := os.Symlink(source, sourceAlias); err != nil {
		t.Fatalf("create source alias: %v", err)
	}
	workspaceAlias := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(workspace, workspaceAlias); err != nil {
		t.Fatalf("create workspace alias: %v", err)
	}
	wantSource, err := filepath.EvalSymlinks(sourceAlias)
	if err != nil {
		t.Fatalf("resolve source alias: %v", err)
	}
	wantWorkspace, err := filepath.EvalSymlinks(workspaceAlias)
	if err != nil {
		t.Fatalf("resolve workspace alias: %v", err)
	}

	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:         "inspect current.go",
		SourceRepository:   sourceAlias,
		ExecutionWorkspace: workspaceAlias,
		StartingRevision:   "revision-1",
	}, nil, "")
	if err != nil {
		t.Fatalf("assemble canonical identity fixture: %v", err)
	}
	if assembly.Manifest.SourceRepository != wantSource {
		t.Fatalf("source repository was not canonicalized: got %q want %q", assembly.Manifest.SourceRepository, wantSource)
	}
	if assembly.Manifest.ExecutionWorkspace != wantWorkspace {
		t.Fatalf("execution workspace was not canonicalized: got %q want %q", assembly.Manifest.ExecutionWorkspace, wantWorkspace)
	}
	if !strings.Contains(assembly.Rendered, "workspace evidence") {
		t.Fatalf("evidence was not read from the execution workspace: %s", assembly.Rendered)
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

func TestTaskContextAdmitsDeterministicAssociatedTest(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo_test.go", "package fixture\n\nfunc TestFoo() {}\n")

	assembly := assembleTaskContextForTest(t, root, "inspect foo.go", nil)
	if len(assembly.Manifest.Items) != 2 {
		t.Fatalf("expected source plus associated test, got %+v", assembly.Manifest.Items)
	}
	if assembly.Manifest.Items[1].Path != "foo_test.go" || assembly.Manifest.Items[1].SelectionReason != taskContextSelectionReasonAssociatedTest {
		t.Fatalf("associated test was not deterministic: %+v", assembly.Manifest.Items)
	}
}

func TestTaskContextAdmitsDeterministicAssociatedDocumentation(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo.md", "Foo design notes\n")

	assembly := assembleTaskContextForTest(t, root, "inspect foo.go", nil)
	if len(assembly.Manifest.Items) != 2 {
		t.Fatalf("expected source plus associated documentation, got %+v", assembly.Manifest.Items)
	}
	if assembly.Manifest.Items[1].Path != "foo.md" || assembly.Manifest.Items[1].SelectionReason != taskContextSelectionReasonAssociatedDocumentation {
		t.Fatalf("associated documentation was not deterministic: %+v", assembly.Manifest.Items)
	}
}

func TestTaskContextAssociatedDiscoveryIsBounded(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo_test.go", "package fixture\n\nfunc TestFoo() {}\n")
	writeTaskContextFile(t, root, "foo.md", "Foo design notes\n")
	for i := 0; i < 100; i++ {
		writeTaskContextFile(t, root, filepath.Join("deep", "nested", "docs", "unrelated", string(rune('a'+i%26))+".md"), "unrelated\n")
	}

	assembly := assembleTaskContextForTest(t, root, "foo.go", nil)
	if assembly.Manifest.CandidateCount != 3 {
		t.Fatalf("associated discovery traversed beyond fixed candidates: candidate count %d, manifest %+v", assembly.Manifest.CandidateCount, assembly.Manifest)
	}
}

func TestTaskContextAssociatedEvidenceRespectsExistingBudgets(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo_test.go", "package fixture\n\nfunc TestFoo() {}\n")
	t.Setenv(taskContextMaxItemsEnv, "1")

	assembly := assembleTaskContextForTest(t, root, "foo.go", nil)
	if len(assembly.Manifest.Items) != 1 || assembly.Manifest.Items[0].Path != "foo.go" {
		t.Fatalf("associated evidence exceeded item budget: %+v", assembly.Manifest.Items)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionBudgetItems] == 0 {
		t.Fatalf("budget omission was not recorded: %+v", assembly.Manifest.OmissionCounts)
	}
}

func TestTaskContextAssociatedEvidencePrecedesProjectMemoryWithinItemBudget(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo_test.go", "package fixture\n\nfunc TestFoo() {}\n")
	memory := &taskContextTestMemoryIndex{memories: []rhizome.Memory{{
		RepositoryName: "fixture",
		Category:       "design",
		Title:          "architecture",
		Content:        "project memory",
	}}}
	t.Setenv(taskContextMaxItemsEnv, "2")

	assemble := func() taskContextAssembly {
		t.Helper()
		assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
			TaskPrompt:           "inspect foo.go",
			SourceRepository:     root,
			ExecutionWorkspace:   root,
			MemoryIndex:          memory,
			MemoryRepositoryName: "fixture",
			StartingRevision:     "revision-1",
		}, nil, "fixture")
		if err != nil {
			t.Fatalf("assemble task context: %v", err)
		}
		return assembly
	}

	first := assemble()
	second := assemble()
	wantPaths := []string{"foo.go", "foo_test.go"}
	for _, assembly := range []taskContextAssembly{first, second} {
		if len(assembly.Manifest.Items) != len(wantPaths) {
			t.Fatalf("item budget admitted unexpected evidence: %+v", assembly.Manifest.Items)
		}
		for index, wantPath := range wantPaths {
			if assembly.Manifest.Items[index].Path != wantPath {
				t.Fatalf("evidence ordering = %+v, want %v", assembly.Manifest.Items, wantPaths)
			}
		}
		if strings.Contains(assembly.Rendered, "project memory") {
			t.Fatalf("project memory exceeded item budget: %q", assembly.Rendered)
		}
	}
	if first.Rendered != second.Rendered {
		t.Fatalf("identical assemblies produced different ordering:\nfirst=%q\nsecond=%q", first.Rendered, second.Rendered)
	}
}

func TestTaskContextLocalMemoryAdmissionAndNoRemoteFallback(t *testing.T) {
	root := t.TempDir()
	memory := &taskContextTestMemoryIndex{memories: []rhizome.Memory{{
		RepositoryName: "fixture",
		Category:       "design",
		Title:          "architecture",
		Content:        "local project memory",
	}}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "use architecture memory",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memory,
		MemoryRepositoryName: "fixture",
		MemoryOmissionReason: "",
		StartingRevision:     "revision-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble local memory: %v", err)
	}
	if len(assembly.Manifest.Items) != 0 {
		t.Fatalf("local memory was unexpectedly admitted: %+v", assembly.Manifest.Items)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryUnclassified] != 1 {
		t.Fatalf("local memory omission count mismatch: %+v", assembly.Manifest.OmissionCounts)
	}
	if strings.Contains(assembly.Rendered, "local project memory") {
		t.Fatalf("local memory content appeared in evidence: %q", assembly.Rendered)
	}

	t.Setenv("TENDRIL_MEMORY_BACKEND", "pinecone")
	t.Setenv("TENDRIL_MEMORY_SQLITE_PATH", filepath.Join(t.TempDir(), "shared.db"))
	withoutLocal, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "use architecture memory",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryOmissionReason: taskContextOmissionMemoryMissing,
		StartingRevision:     "revision-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble without local memory: %v", err)
	}
	if len(withoutLocal.Manifest.Items) != 0 || withoutLocal.Manifest.OmissionCounts[taskContextOmissionMemoryMissing] == 0 {
		t.Fatalf("configured remote/shared memory became automatic context: %+v", withoutLocal.Manifest)
	}
}

func TestTaskContextMemoryOmissionIsFailClosed(t *testing.T) {
	root := t.TempDir()
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "use memory",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          &taskContextTestMemoryIndex{err: errors.New("decrypt memory content")},
		MemoryRepositoryName: "fixture",
		StartingRevision:     "revision-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("malformed optional memory should not fail assembly: %v", err)
	}
	if len(assembly.Manifest.Items) != 0 || assembly.Manifest.OmissionCounts[taskContextOmissionMemoryUnavailable] == 0 {
		t.Fatalf("malformed memory was not omitted as unavailable: %+v", assembly.Manifest)
	}
}

func TestTaskContextSameBasenameMemoryIsolation(t *testing.T) {
	left := filepath.Join(t.TempDir(), "repo")
	right := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(left, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(right, 0o755); err != nil {
		t.Fatal(err)
	}

	assemble := func(root, content string) taskContextAssembly {
		t.Helper()
		assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
			TaskPrompt:           "use architecture memory",
			SourceRepository:     root,
			ExecutionWorkspace:   root,
			MemoryIndex:          &taskContextTestMemoryIndex{memories: []rhizome.Memory{{RepositoryName: "repo", Title: "architecture", Content: content}}},
			MemoryRepositoryName: "repo",
			StartingRevision:     "revision-1",
		}, nil, "repo")
		if err != nil {
			t.Fatalf("assemble %s memory: %v", root, err)
		}
		return assembly
	}

	leftAssembly := assemble(left, "left memory")
	rightAssembly := assemble(right, "right memory")
	if strings.Contains(leftAssembly.Rendered, "left memory") || strings.Contains(leftAssembly.Rendered, "right memory") {
		t.Fatalf("left same-basename repository admitted quarantined memory: %q", leftAssembly.Rendered)
	}
	if strings.Contains(rightAssembly.Rendered, "right memory") || strings.Contains(rightAssembly.Rendered, "left memory") {
		t.Fatalf("right same-basename repository admitted quarantined memory: %q", rightAssembly.Rendered)
	}
	if leftAssembly.Manifest.OmissionCounts[taskContextOmissionMemoryUnclassified] != 1 {
		t.Fatalf("left memory not counted as unclassified: %+v", leftAssembly.Manifest.OmissionCounts)
	}
	if rightAssembly.Manifest.OmissionCounts[taskContextOmissionMemoryUnclassified] != 1 {
		t.Fatalf("right memory not counted as unclassified: %+v", rightAssembly.Manifest.OmissionCounts)
	}
}

func TestTaskContextSourceLocalMemorySameBasenameIsolation(t *testing.T) {
	makeRepository := func(content string) string {
		t.Helper()
		root := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir source repository: %v", err)
		}
		index, repositoryName, err := openRhizomeIndex(context.Background(), root)
		if err != nil {
			t.Fatalf("open source-local Rhizome index: %v", err)
		}
		if err := index.StoreMemory(context.Background(), rhizome.Memory{
			RepositoryName: repositoryName,
			Category:       "design",
			Title:          "architecture",
			Content:        content,
		}); err != nil {
			_ = index.Close()
			t.Fatalf("store source-local memory: %v", err)
		}
		if err := index.Close(); err != nil {
			t.Fatalf("close source-local Rhizome index: %v", err)
		}
		return root
	}

	leftRoot := makeRepository("left source memory")
	rightRoot := makeRepository("right source memory")
	leftIndex, leftName, leftOmission, closeLeft := openTaskContextSourceMemory(context.Background(), leftRoot)
	if leftOmission != "" || leftIndex == nil || closeLeft == nil {
		t.Fatalf("left source-local memory binding unavailable: index=%v omission=%q", leftIndex != nil, leftOmission)
	}
	defer closeLeft()
	rightIndex, rightName, rightOmission, closeRight := openTaskContextSourceMemory(context.Background(), rightRoot)
	if rightOmission != "" || rightIndex == nil || closeRight == nil {
		t.Fatalf("right source-local memory binding unavailable: index=%v omission=%q", rightIndex != nil, rightOmission)
	}
	defer closeRight()

	assemble := func(root, repositoryName string, index taskContextMemoryIndex) taskContextAssembly {
		t.Helper()
		assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
			TaskPrompt:           "use architecture memory",
			SourceRepository:     root,
			ExecutionWorkspace:   root,
			MemoryIndex:          index,
			MemoryRepositoryName: repositoryName,
			StartingRevision:     "revision-1",
		}, nil, repositoryName)
		if err != nil {
			t.Fatalf("assemble source-local memory: %v", err)
		}
		return assembly
	}

	leftAssembly := assemble(leftRoot, leftName, leftIndex)
	rightAssembly := assemble(rightRoot, rightName, rightIndex)
	if strings.Contains(leftAssembly.Rendered, "left source memory") || strings.Contains(leftAssembly.Rendered, "right source memory") {
		t.Fatalf("left automatic memory was not quarantined: %q", leftAssembly.Rendered)
	}
	if strings.Contains(rightAssembly.Rendered, "right source memory") || strings.Contains(rightAssembly.Rendered, "left source memory") {
		t.Fatalf("right automatic memory was not quarantined: %q", rightAssembly.Rendered)
	}
	if leftAssembly.Manifest.OmissionCounts[taskContextOmissionMemoryUnclassified] != 1 {
		t.Fatalf("left memory not counted as unclassified: %+v", leftAssembly.Manifest.OmissionCounts)
	}
	if rightAssembly.Manifest.OmissionCounts[taskContextOmissionMemoryUnclassified] != 1 {
		t.Fatalf("right memory not counted as unclassified: %+v", rightAssembly.Manifest.OmissionCounts)
	}
}

func TestTaskContextSourceLocalMemoryMissingAndMalformedAreOmitted(t *testing.T) {
	missing := t.TempDir()
	index, repositoryName, omission, closeIndex := openTaskContextSourceMemory(context.Background(), missing)
	if index != nil || repositoryName != "" || closeIndex != nil || omission != taskContextOmissionMemoryMissing {
		t.Fatalf("missing source-local memory binding = index %v repository %q omission %q close %v", index != nil, repositoryName, omission, closeIndex != nil)
	}

	malformed := t.TempDir()
	tendril := filepath.Join(malformed, tendrilStateDirectory)
	if err := os.MkdirAll(tendril, 0o755); err != nil {
		t.Fatalf("mkdir malformed memory state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tendril, rhizomeIndexDatabase), []byte("not a real database"), 0o600); err != nil {
		t.Fatalf("write malformed database: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tendril, rhizomeIndexKeyFile), []byte("bad-key"), 0o600); err != nil {
		t.Fatalf("write malformed key: %v", err)
	}
	index, repositoryName, omission, closeIndex = openTaskContextSourceMemory(context.Background(), malformed)
	if index != nil || repositoryName != "" || closeIndex != nil || omission != taskContextOmissionMemoryUnavailable {
		t.Fatalf("malformed source-local memory binding = index %v repository %q omission %q close %v", index != nil, repositoryName, omission, closeIndex != nil)
	}
}

func TestTaskContextSourceLocalMemoryRejectsSymlinkedState(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, tendrilStateDirectory)
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatalf("mkdir source-local state: %v", err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, rhizomeIndexDatabase), []byte("database"), 0o600); err != nil {
		t.Fatalf("write external database: %v", err)
	}
	if err := os.WriteFile(filepath.Join(external, rhizomeIndexKeyFile), make([]byte, 32), 0o600); err != nil {
		t.Fatalf("write external key: %v", err)
	}
	if err := os.Symlink(filepath.Join(external, rhizomeIndexDatabase), filepath.Join(state, rhizomeIndexDatabase)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(external, rhizomeIndexKeyFile), filepath.Join(state, rhizomeIndexKeyFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	index, repositoryName, omission, closeIndex := openTaskContextSourceMemory(context.Background(), root)
	if index != nil || repositoryName != "" || closeIndex != nil || omission != taskContextOmissionMemoryUnavailable {
		t.Fatalf("symlinked source-local memory binding = index %v repository %q omission %q close %v", index != nil, repositoryName, omission, closeIndex != nil)
	}
}

func TestTaskContextSourceLocalMemoryUsesMatchingLocalKey(t *testing.T) {
	t.Setenv(heartwood.KeyEnvVar, "")
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir source repository: %v", err)
	}
	index, repositoryName, err := openRhizomeIndex(context.Background(), root)
	if err != nil {
		t.Fatalf("open source-local Rhizome index: %v", err)
	}
	if err := index.StoreMemory(context.Background(), rhizome.Memory{RepositoryName: repositoryName, Title: "local", Content: "local key memory"}); err != nil {
		_ = index.Close()
		t.Fatalf("store source-local memory: %v", err)
	}
	if err := index.Close(); err != nil {
		t.Fatalf("close source-local memory: %v", err)
	}
	t.Setenv(heartwood.KeyEnvVar, "a different environment key")

	memoryIndex, gotRepositoryName, omission, closeMemory := openTaskContextSourceMemory(context.Background(), root)
	if omission != "" || memoryIndex == nil || gotRepositoryName != repositoryName || closeMemory == nil {
		t.Fatalf("source-local key binding = index %v repository %q omission %q close %v", memoryIndex != nil, gotRepositoryName, omission, closeMemory != nil)
	}
	defer closeMemory()
	memories, err := memoryIndex.SearchMemories(context.Background(), repositoryName, "*", "", 10)
	if err != nil || len(memories) != 1 || memories[0].Content != "local key memory" {
		t.Fatalf("source-local memory search = memories=%+v err=%v", memories, err)
	}
}

func TestTaskContextAssociatedSymlinkToPrivateMaterialIsOmitted(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, ".env", "PRIVATE=must not enter context\n")
	if err := os.Symlink(".env", filepath.Join(root, "foo_test.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	assembly := assembleTaskContextForTest(t, root, "foo.go", nil)
	if len(assembly.Manifest.Items) != 1 || assembly.Manifest.Items[0].Path != "foo.go" {
		t.Fatalf("private associated symlink was admitted: %+v", assembly.Manifest.Items)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionPathSecurity] == 0 {
		t.Fatalf("private associated symlink omission was not classified: %+v", assembly.Manifest.OmissionCounts)
	}
}

func TestTaskContextManifestRecordsSafeAggregateAndItemFacts(t *testing.T) {
	root := t.TempDir()
	content := "package fixture\n\nfunc Foo() {}\n"
	writeTaskContextFile(t, root, "foo.go", content)
	t.Setenv(taskContextMaxBytesEnv, "64")
	t.Setenv(taskContextItemMaxBytesEnv, "32")

	assembly := assembleTaskContextForTest(t, root, "foo.go", nil)
	manifest := assembly.Manifest
	if manifest.EffectiveMaxBytes != 64 || manifest.EffectiveItemMaxBytes != 32 || manifest.EffectiveMaxItems != taskContextDefaultMaxItems {
		t.Fatalf("manifest limits = %+v", manifest)
	}
	if manifest.CandidateCount == 0 || manifest.AdmittedCount != 1 || manifest.AdmittedBytes != manifest.Items[0].Bytes {
		t.Fatalf("manifest aggregate facts = %+v", manifest)
	}
	item := manifest.Items[0]
	if item.SourceClass == "" || item.SourceIdentity != "foo.go" || item.SelectionReason != taskContextSelectionReasonExplicitFile || item.ContentReference == "" || item.ContentReference == item.ContentIdentity {
		t.Fatalf("manifest item lacks safe provenance facts: %+v", item)
	}
}

func TestTaskContextTruncatedAndOmittedManifestReasons(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\n"+strings.Repeat("x", 256)+"\n")
	writeTaskContextFile(t, root, "foo_test.go", "package fixture\n\nfunc TestFoo() {}\n")
	t.Setenv(taskContextMaxBytesEnv, "64")
	t.Setenv(taskContextItemMaxBytesEnv, "32")
	t.Setenv(taskContextMaxItemsEnv, "1")

	assembly := assembleTaskContextForTest(t, root, "foo.go", nil)
	if len(assembly.Manifest.Items) != 1 || !assembly.Manifest.Items[0].Truncated {
		t.Fatalf("truncation was not recorded: %+v", assembly.Manifest)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionBudgetBytes] == 0 && assembly.Manifest.OmissionCounts[taskContextOmissionBudgetItems] == 0 {
		t.Fatalf("budget omission was not recorded: %+v", assembly.Manifest)
	}
}

func TestTaskContextStaleAndPathSecurityReasonsAreStable(t *testing.T) {
	root := t.TempDir()
	content := "package fixture\n\nfunc Current() {}\n"
	writeTaskContextFile(t, root, "current.go", content)
	index := &taskContextTestIndex{
		symbols: []rhizome.Symbol{{Name: "Current", Type: "function", FilePath: "current.go", LineStart: 3, LineEnd: 3}},
		files:   map[string]rhizome.FileRecord{"current.go": {Hash: "stale-hash"}},
	}
	assembly := assembleTaskContextForTest(t, root, "../escape.go Current", index)
	if assembly.Manifest.OmissionCounts[taskContextOmissionStaleEvidence] == 0 || assembly.Manifest.OmissionCounts[taskContextOmissionPathSecurity] == 0 {
		t.Fatalf("unstable omission reasons: %+v", assembly.Manifest.OmissionCounts)
	}
}

func TestTaskContextProvenanceContainsNoTranscriptSearchTermsOrHostPaths(t *testing.T) {
	root := t.TempDir()
	writeTaskContextFile(t, root, "foo.go", "secret evidence that must not enter provenance\n")
	assembly := assembleTaskContextForTest(t, root, "inspect foo.go with secret-search-term", nil)
	event := taskContextObservationEvent("step-1", "phytomer-1", "fixture", assembly.Manifest)
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal provenance event: %v", err)
	}
	encoded := string(payload)
	for _, forbidden := range []string{root, "secret evidence", "secret-search-term", "Transcript"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("provenance event leaked %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(encoded, "selectionReason") || !strings.Contains(encoded, "contentRef") {
		t.Fatalf("safe provenance fields missing: %s", encoded)
	}
}

func TestTaskContextObservationEventIncludesSafeMemoryLifecycleMetadata(t *testing.T) {
	root := t.TempDir()
	evidenceContent := []byte("package fixture\n\nfunc Evidence() {}\n")
	writeTaskContextFile(t, root, "foo.go", string(evidenceContent))
	manifestJSON := newEvidenceManifestJSON(t, "foo.go", evidenceContent)
	var evidenceManifest rhizome.EvidenceManifest
	if err := json.Unmarshal([]byte(manifestJSON), &evidenceManifest); err != nil {
		t.Fatalf("parse evidence manifest: %v", err)
	}

	const memoryContent = "private memory content must never enter observation telemetry"
	memory := newEstablishedBotanistMemory("fixture", "foo-fact", memoryContent)
	memory.Kind = rhizome.KindFact
	memory.RevisionMetadata = manifestJSON
	memory.RevisionIdentity = "/absolute/private/revision"
	memory.ContentIdentity = evidenceManifest.ManifestHash
	index := &taskContextTestMemoryIndex{memories: []rhizome.Memory{memory}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "foo fact",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		StartingRevision:     "revision-1",
		MemoryIndex:          index,
		MemoryRepositoryName: "fixture",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	event := taskContextObservationEvent("step-1", "session-1", "fixture", assembly.Manifest)
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal raw observation: %v", err)
	}
	safe := telemetry.SanitizeObservationEvent(event)
	safeJSON, err := json.Marshal(safe)
	if err != nil {
		t.Fatalf("marshal sanitized observation: %v", err)
	}
	for _, encoded := range []string{string(raw), string(safeJSON)} {
		for _, forbidden := range []string{memoryContent, memory.RevisionMetadata, memory.RevisionIdentity, "foo.go", root} {
			if strings.Contains(encoded, forbidden) {
				t.Fatalf("observation leaked %q: %s", forbidden, encoded)
			}
		}
	}

	items, ok := safe.Data["items"].([]map[string]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("sanitized observation items = %#v, want one memory item", safe.Data["items"])
	}
	item := items[0]
	for field, want := range map[string]interface{}{
		"sourceClass": taskContextEvidenceMemory,
		"origin":      "botanist",
		"authority":   "botanist",
		"status":      "established",
		"kind":        "fact",
		"validityRef": taskContextShortReference(evidenceManifest.ManifestHash),
		"contentRef":  taskContextShortContentReference(taskContextContentIdentity([]byte(memoryContent))),
	} {
		if item[field] != want {
			t.Errorf("sanitized memory field %q = %#v, want %#v; item=%v", field, item[field], want, item)
		}
	}
}

func TestTaskContextManagedBackingSourceReferenceOmitsWorkspacePath(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeTaskContextFile(t, workspace, "foo.go", "workspace evidence\n")
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:         "foo.go",
		SourceRepository:   source,
		ExecutionWorkspace: workspace,
		StartingRevision:   "revision-1",
	}, nil, "")
	if err != nil {
		t.Fatalf("assemble managed backing source: %v", err)
	}
	event := taskContextObservationEvent("step-1", "phytomer-1", "fixture", assembly.Manifest)
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), source) || strings.Contains(string(encoded), workspace) {
		t.Fatalf("managed source/workspace path leaked from event: %s", encoded)
	}
	if event.Data["substrateRef"] != taskContextShortReference(assembly.Manifest.SourceRepository) {
		t.Fatalf("event did not bind provenance to backing source: %+v", event.Data)
	}
}

func TestRunSproutEmitsOneTaskContextEventPerGrowthIncludingZeroEvidence(t *testing.T) {
	root := newOutcomeTestRepo(t)
	bus := eventbus.New()
	var received []eventbus.Event
	bus.Subscribe(eventbus.EventTaskContextAssembled, func(event eventbus.Event) {
		received = append(received, event)
	})
	stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "done"}, nil)
	originalNewSprout := newSproutFn
	var renderedContext string
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID, sessionID, renderedTaskContext string) (sproutRunner, error) {
		renderedContext = renderedTaskContext
		return originalNewSprout(ctx, workspace, genotypeRoot, genotypeName, client, session, eventBus, stepID, sessionID, renderedTaskContext)
	}
	t.Cleanup(func() { newSproutFn = originalNewSprout })
	orch := &DockerOrchestrator{Substrate: root, StepID: "task-context-growth", SessionID: "phytomer-1", EventBus: bus, DisableMergeBack: true}
	if _, err := orch.RunSprout(context.Background(), "no matching evidence"); err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("task-context event count = %d, want exactly one: %+v", len(received), received)
	}
	if received[0].SessionID != "phytomer-1" || received[0].Source != "task-context-growth" {
		t.Fatalf("task-context event correlation = %+v", received[0])
	}
	if received[0].Data["admittedCount"] != 0 {
		t.Fatalf("empty-context growth admitted evidence: %+v", received[0].Data)
	}
	if renderedContext != "" {
		t.Fatalf("empty-context growth injected a raw context block: %q", renderedContext)
	}
}

func TestRunSproutTaskContextDeliveryAndTranscriptPersistence(t *testing.T) {
	root := newOutcomeTestRepo(t)
	selectedEvidence := "QUALIFICATION-SELECTED-EVIDENCE-RAW"

	t.Setenv(historydb.EnvEncryptAtRest, "off")
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open HistoryDB: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	bus := eventbus.New()
	bus.AttachSink(store, 0, "historydb")
	t.Cleanup(bus.Shutdown)

	stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "unused"}, nil)
	client := &fakeLLM{response: `{"final":"done"}`}
	createShadow := createShadowWorktreeFn
	createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) {
		shadowPath := filepath.Join(root, "shadow-worktree")
		if err := os.MkdirAll(shadowPath, 0o755); err != nil {
			return "", err
		}
		writeTaskContextFile(t, shadowPath, "evidence.go", "package fixture\n\n// "+selectedEvidence+"\n")
		return shadowPath, nil
	}
	t.Cleanup(func() { createShadowWorktreeFn = createShadow })
	startSession := startTerrariumSessionFn
	startTerrariumSessionFn = func(context.Context, string, string, string, bool, []string, []string, time.Duration, ...terrarium.ActivationObserver) (toolSession, error) {
		return &fakeSession{tools: []ToolDefinition{{Name: "readFile", Description: "read a file"}}}, nil
	}
	t.Cleanup(func() { startTerrariumSessionFn = startSession })
	startSprout := newSproutFn
	var deliveredTaskContext string
	var deliveredSourcePath string
	var producedTranscript string
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, _ llmCaller, session toolSession, eventBus *eventbus.Bus, stepID, sessionID, renderedTaskContext string) (sproutRunner, error) {
		deliveredTaskContext = renderedTaskContext
		deliveredSourcePath = genotypeRoot
		created, err := newSprout(ctx, workspace, genotypeRoot, genotypeName, client, session, eventBus, stepID, sessionID, renderedTaskContext)
		if err != nil {
			return nil, err
		}
		return &taskContextTranscriptCapture{inner: created, transcript: &producedTranscript}, nil
	}
	t.Cleanup(func() { newSproutFn = startSprout })

	const stepID = "qualification-step-context-20260920-0123456789abcdef"
	const sessionID = "phytomer-qualification-20260920-0123456789abcdef"
	orch := &DockerOrchestrator{
		Substrate:        root,
		StepID:           stepID,
		SessionID:        sessionID,
		EventBus:         bus,
		DisableMergeBack: true,
	}
	if _, err := orch.RunSprout(context.Background(), "inspect evidence.go"); err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	bus.Shutdown()

	if len(client.calls) == 0 || len(client.calls[0]) == 0 {
		t.Fatal("expected a first provider-facing system request")
	}
	if client.calls[0][0].Role != "system" {
		t.Fatalf("first provider request role = %q, want system", client.calls[0][0].Role)
	}
	if !strings.Contains(client.calls[0][0].Content, selectedEvidence) {
		t.Fatalf("first provider request omitted selected raw evidence (source %q, delivered context %q): %s", deliveredSourcePath, deliveredTaskContext, client.calls[0][0].Content)
	}
	for _, required := range []string{
		"Task-specific Substrate evidence was supplied separately.",
		"See the task-context provenance manifest for selection facts.",
	} {
		if !strings.Contains(producedTranscript, required) {
			t.Fatalf("Sprout producer transcript missing %q: %s", required, producedTranscript)
		}
	}
	if strings.Contains(producedTranscript, selectedEvidence) {
		t.Fatalf("Sprout producer transcript leaked selected raw evidence: %s", producedTranscript)
	}

	records, err := store.LoadEvents(context.Background(), sessionID, 100)
	if err != nil {
		t.Fatalf("load persisted events: %v", err)
	}
	var contextRecord, transcriptRecord *historydb.EventRecord
	for index := range records {
		record := records[index]
		switch record.Type {
		case string(eventbus.EventTaskContextAssembled):
			contextRecord = &record
		case string(eventbus.EventSproutTranscript):
			transcriptRecord = &record
		}
	}
	if contextRecord == nil || transcriptRecord == nil {
		t.Fatalf("missing persisted task-context/transcript events: %+v", records)
	}
	if contextRecord.Source != stepID || contextRecord.SessionID != sessionID {
		t.Fatalf("task-context correlation = source %q/session %q", contextRecord.Source, contextRecord.SessionID)
	}
	if transcriptRecord.Source != stepID || transcriptRecord.SessionID != sessionID {
		t.Fatalf("transcript correlation = source %q/session %q", transcriptRecord.Source, transcriptRecord.SessionID)
	}
	admittedCount := 0
	switch value := contextRecord.Data["admittedCount"].(type) {
	case int:
		admittedCount = value
	case float64:
		admittedCount = int(value)
	}
	if admittedCount <= 0 {
		t.Fatalf("persisted admittedCount = %#v, want > 0; source=%q event=%#v", contextRecord.Data["admittedCount"], deliveredSourcePath, contextRecord.Data)
	}
	transcript, ok := transcriptRecord.Data["transcript"].(string)
	if !ok {
		t.Fatalf("persisted transcript has unexpected shape: %#v", transcriptRecord.Data["transcript"])
	}
	for _, required := range []string{
		"Task-specific Substrate evidence was supplied separately.",
		"See the task-context provenance manifest for selection facts.",
	} {
		if !strings.Contains(transcript, required) {
			t.Fatalf("persisted transcript missing %q: %s", required, transcript)
		}
	}
	if strings.Contains(transcript, selectedEvidence) {
		t.Fatalf("persisted transcript leaked selected raw evidence: %s", transcript)
	}
}

// ---------------------------------------------------------------------------
// Slice 4 — Memory-context eligibility and evidence-bound validation
// ---------------------------------------------------------------------------

// newEstablishedBotanistMemory returns a minimal rhizome.Memory that passes
// the Slice 4 eligibility gate (status=established, authority=botanist).
func newEstablishedBotanistMemory(repositoryName, title, content string) rhizome.Memory {
	return rhizome.Memory{
		RepositoryName: repositoryName,
		Title:          title,
		Content:        content,
		Status:         rhizome.StatusEstablished,
		Authority:      rhizome.AuthorityBotanist,
		Origin:         rhizome.OriginBotanist,
		Kind:           rhizome.KindObservation,
		StableID:       rhizome.StableMemoryIdentity(repositoryName, title),
	}
}

// newEvidenceManifestJSON returns a minimal v1 JSON blob for one file entry.
// The hash must match what sha256 of fileContent produces.
func newEvidenceManifestJSON(t *testing.T, relPath string, fileContent []byte) string {
	t.Helper()
	sum := sha256.Sum256(fileContent)
	contentHash := hex.EncodeToString(sum[:])
	files := []rhizome.EvidenceFile{{RelativePath: relPath, ContentHash: contentHash}}
	return evidenceManifestJSONForTest(t, files, "")
}

func evidenceManifestJSONForTest(t *testing.T, files []rhizome.EvidenceFile, manifestHash string) string {
	t.Helper()
	manifestBytes, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("marshal evidence files: %v", err)
	}
	if manifestHash == "" {
		manifestSum := sha256.Sum256(manifestBytes)
		manifestHash = hex.EncodeToString(manifestSum[:])
	}
	blob, err := json.Marshal(rhizome.EvidenceManifest{
		Version:      "v1",
		Files:        files,
		ManifestHash: manifestHash,
	})
	if err != nil {
		t.Fatalf("marshal evidence manifest: %v", err)
	}
	return string(blob)
}

// TestTaskContextEligibilityFilterAdmitsEstablishedBotanistAuthority verifies
// that a memory with status=established and authority=botanist is admitted.
func TestTaskContextEligibilityFilterAdmitsEstablishedBotanistAuthority(t *testing.T) {
	root := t.TempDir()
	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{
		newEstablishedBotanistMemory("fixture", "constraint-alpha", "do not leak passwords"),
	}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "constraint alpha",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.Contains(assembly.Rendered, "do not leak passwords") {
		t.Fatalf("established botanist memory was not admitted; rendered=%q omissions=%v", assembly.Rendered, assembly.Manifest.OmissionCounts)
	}
	if !strings.Contains(assembly.Rendered, "Untrusted repository knowledge context") {
		t.Fatalf("admitted memory rendering did not identify repository knowledge as untrusted: %q", assembly.Rendered)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryUnclassified] != 0 {
		t.Fatalf("admitted memory incorrectly counted as unclassified: %v", assembly.Manifest.OmissionCounts)
	}
}

// TestTaskContextEligibilityFilterAdmitsDeterministicAuthority verifies that
// established+deterministic is also eligible.
func TestTaskContextEligibilityFilterAdmitsDeterministicAuthority(t *testing.T) {
	root := t.TempDir()
	mem := rhizome.Memory{
		RepositoryName: "fixture",
		Title:          "hash-rule",
		Content:        "deterministic substrate fact",
		Status:         rhizome.StatusEstablished,
		Authority:      rhizome.AuthorityDeterministic,
		Origin:         rhizome.OriginSubstrate,
		Kind:           rhizome.KindFact,
		StableID:       rhizome.StableMemoryIdentity("fixture", "hash-rule"),
	}
	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "hash rule",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.Contains(assembly.Rendered, "deterministic substrate fact") {
		t.Fatalf("established deterministic memory was not admitted; rendered=%q omissions=%v", assembly.Rendered, assembly.Manifest.OmissionCounts)
	}
}

// TestTaskContextEligibilityFilterRejectsIneligibleStatuses verifies that each
// non-established status is individually omitted with the correct reason.
func TestTaskContextEligibilityFilterRejectsIneligibleStatuses(t *testing.T) {
	cases := []struct {
		status  rhizome.Status
		wantKey string
	}{
		{rhizome.StatusProposed, taskContextOmissionMemoryProposed},
		{rhizome.StatusStale, taskContextOmissionMemoryStale},
		{rhizome.StatusConflicted, taskContextOmissionMemoryConflicted},
		{rhizome.StatusRejected, taskContextOmissionMemoryRejected},
		{rhizome.StatusSuperseded, taskContextOmissionMemorySuperseded},
		{rhizome.StatusUnclassified, taskContextOmissionMemoryUnclassified},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.status), func(t *testing.T) {
			root := t.TempDir()
			mem := rhizome.Memory{
				RepositoryName: "fixture",
				Title:          "test-mem",
				Content:        "secret content",
				Status:         tc.status,
				Authority:      rhizome.AuthorityBotanist,
				Origin:         rhizome.OriginBotanist,
				Kind:           rhizome.KindObservation,
				StableID:       rhizome.StableMemoryIdentity("fixture", "test-mem"),
			}
			memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
			assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
				TaskPrompt:           "test mem",
				SourceRepository:     root,
				ExecutionWorkspace:   root,
				MemoryIndex:          memIdx,
				MemoryRepositoryName: "fixture",
				StartingRevision:     "rev-1",
			}, nil, "fixture")
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if strings.Contains(assembly.Rendered, "secret content") {
				t.Fatalf("ineligible memory (status=%s) leaked into context: %q", tc.status, assembly.Rendered)
			}
			if assembly.Manifest.OmissionCounts[tc.wantKey] != 1 {
				t.Fatalf("status=%s: omission counts=%v want %s=1", tc.status, assembly.Manifest.OmissionCounts, tc.wantKey)
			}
		})
	}
}

// TestTaskContextEligibilityFilterRejectsEstablishedNoneAuthority verifies
// that established+none authority is rejected (conflicted, not admitted).
func TestTaskContextEligibilityFilterRejectsEstablishedNoneAuthority(t *testing.T) {
	root := t.TempDir()
	mem := rhizome.Memory{
		RepositoryName: "fixture",
		Title:          "bad-established",
		Content:        "should be rejected",
		Status:         rhizome.StatusEstablished,
		Authority:      rhizome.AuthorityNone,
		Origin:         rhizome.OriginBotanist,
		Kind:           rhizome.KindObservation,
		StableID:       rhizome.StableMemoryIdentity("fixture", "bad-established"),
	}
	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "bad established",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if strings.Contains(assembly.Rendered, "should be rejected") {
		t.Fatalf("established+none authority memory was admitted: %q", assembly.Rendered)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryConflicted] != 1 {
		t.Fatalf("expected conflicted omission, got: %v", assembly.Manifest.OmissionCounts)
	}
}

// TestTaskContextEvidenceValidationAdmitsCurrentFile verifies that a memory
// with RevisionMetadata containing a v1 manifest whose file hash matches the
// workspace is admitted, and that the manifest item has a non-empty ValidityRef.
func TestTaskContextEvidenceValidationAdmitsCurrentFile(t *testing.T) {
	root := t.TempDir()
	fileContent := []byte("package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo.go", string(fileContent))

	manifestJSON := newEvidenceManifestJSON(t, "foo.go", fileContent)
	mem := newEstablishedBotanistMemory("fixture", "foo-constraint", "foo must not panic")
	mem.RevisionMetadata = manifestJSON

	var evidenceManifest rhizome.EvidenceManifest
	if err := json.Unmarshal([]byte(manifestJSON), &evidenceManifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	mem.ContentIdentity = evidenceManifest.ManifestHash

	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "foo constraint",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.Contains(assembly.Rendered, "foo must not panic") {
		t.Fatalf("evidence-bound memory was not admitted; rendered=%q omissions=%v", assembly.Rendered, assembly.Manifest.OmissionCounts)
	}
	if len(assembly.Manifest.Items) == 0 {
		t.Fatal("no manifest items after evidence-valid memory admission")
	}
	var memItem *taskContextManifestItem
	for i := range assembly.Manifest.Items {
		if assembly.Manifest.Items[i].Kind == taskContextEvidenceMemory {
			memItem = &assembly.Manifest.Items[i]
			break
		}
	}
	if memItem == nil {
		t.Fatalf("no project-memory item in manifest: %+v", assembly.Manifest.Items)
	}
	if memItem.ValidityRef == "" {
		t.Fatalf("evidence-bound manifest item has empty ValidityRef: %+v", memItem)
	}
	if memItem.Origin != "botanist" || memItem.Authority != "botanist" || memItem.Status != "established" {
		t.Fatalf("manifest item envelope fields incorrect: origin=%q authority=%q status=%q", memItem.Origin, memItem.Authority, memItem.Status)
	}
}

// TestTaskContextEvidenceValidationRejectsChangedFile verifies that when a
// file in the workspace has changed since the evidence manifest was created,
// the memory is omitted as stale.
func TestTaskContextEvidenceValidationRejectsChangedFile(t *testing.T) {
	root := t.TempDir()
	originalContent := []byte("package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo.go", string(originalContent))

	manifestJSON := newEvidenceManifestJSON(t, "foo.go", originalContent)
	mem := newEstablishedBotanistMemory("fixture", "foo-stale-constraint", "stale memory")
	mem.RevisionMetadata = manifestJSON
	var evidenceManifest rhizome.EvidenceManifest
	if err := json.Unmarshal([]byte(manifestJSON), &evidenceManifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	mem.ContentIdentity = evidenceManifest.ManifestHash

	// Mutate the file after binding evidence.
	writeTaskContextFile(t, root, "foo.go", "package fixture\n\nfunc Foo() { /* changed */ }\n")

	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "foo stale constraint",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if strings.Contains(assembly.Rendered, "stale memory") {
		t.Fatalf("stale evidence-bound memory was admitted: %q", assembly.Rendered)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryStale] != 1 {
		t.Fatalf("expected stale omission, got: %v", assembly.Manifest.OmissionCounts)
	}
}

// TestTaskContextEvidenceValidationPersistsStaleToCanonicaWorkspace verifies
// that when the execution workspace equals the source repository and the memory
// has stale evidence, the conductor persists a stale transition via StoreMemory.
func TestTaskContextEvidenceValidationPersistsStaleToCanonicaWorkspace(t *testing.T) {
	root := t.TempDir()
	originalContent := []byte("package fixture\n\nfunc Bar() {}\n")
	writeTaskContextFile(t, root, "bar.go", string(originalContent))

	manifestJSON := newEvidenceManifestJSON(t, "bar.go", originalContent)
	mem := newEstablishedBotanistMemory("fixture", "bar-constraint", "bar must be stable")
	mem.RevisionMetadata = manifestJSON
	var evidenceManifest rhizome.EvidenceManifest
	if err := json.Unmarshal([]byte(manifestJSON), &evidenceManifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	mem.ContentIdentity = evidenceManifest.ManifestHash

	// Change the file to trigger stale detection.
	writeTaskContextFile(t, root, "bar.go", "package fixture\n\nfunc Bar() { /* modified */ }\n")

	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "bar constraint",
		SourceRepository:     root,
		ExecutionWorkspace:   root, // same as source — stale persistence should fire
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if strings.Contains(assembly.Rendered, "bar must be stable") {
		t.Fatalf("stale memory should not have been rendered")
	}
	if len(memIdx.stored) != 1 {
		t.Fatalf("expected 1 StoreMemory call for stale persistence, got %d; stored=%+v", len(memIdx.stored), memIdx.stored)
	}
	if memIdx.stored[0].Status != rhizome.StatusStale {
		t.Fatalf("persisted stale memory has wrong status: %s", memIdx.stored[0].Status)
	}
	if memIdx.stored[0].Title != "bar-constraint" {
		t.Fatalf("persisted stale memory has wrong title: %q", memIdx.stored[0].Title)
	}
}

func TestTaskContextEvidenceValidationPersistsStaleForCanonicalWorkspaceAlias(t *testing.T) {
	parent := t.TempDir()
	sourceRoot := filepath.Join(parent, "source")
	workspaceAlias := filepath.Join(parent, "workspace-alias")
	if err := os.Mkdir(sourceRoot, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.Symlink(sourceRoot, workspaceAlias); err != nil {
		t.Fatalf("symlink workspace alias: %v", err)
	}

	originalContent := []byte("package fixture\n\nfunc Alias() {}\n")
	writeTaskContextFile(t, sourceRoot, "alias.go", string(originalContent))
	manifestJSON := newEvidenceManifestJSON(t, "alias.go", originalContent)
	memory := newEstablishedBotanistMemory("fixture", "alias-constraint", "alias must remain stable")
	memory.RevisionMetadata = manifestJSON
	var evidenceManifest rhizome.EvidenceManifest
	if err := json.Unmarshal([]byte(manifestJSON), &evidenceManifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	memory.ContentIdentity = evidenceManifest.ManifestHash
	writeTaskContextFile(t, sourceRoot, "alias.go", "package fixture\n\nfunc Alias() { /* changed */ }\n")

	memoryIndex := &taskContextTestMemoryIndex{memories: []rhizome.Memory{memory}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "alias constraint",
		SourceRepository:     filepath.Join(sourceRoot, "."),
		ExecutionWorkspace:   workspaceAlias,
		MemoryIndex:          memoryIndex,
		MemoryRepositoryName: "fixture",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryStale] != 1 {
		t.Fatalf("expected stale omission through canonical alias, got: %v", assembly.Manifest.OmissionCounts)
	}
	if len(memoryIndex.stored) != 1 || memoryIndex.stored[0].Status != rhizome.StatusStale {
		t.Fatalf("canonical source stale transition was not persisted: %+v", memoryIndex.stored)
	}
}

func TestTaskContextEvidenceValidationStoreFailureFailsCanonicalAssembly(t *testing.T) {
	root := t.TempDir()
	originalContent := []byte("package fixture\n\nfunc Failure() {}\n")
	writeTaskContextFile(t, root, "failure.go", string(originalContent))
	manifestJSON := newEvidenceManifestJSON(t, "failure.go", originalContent)
	memory := newEstablishedBotanistMemory("fixture", "store-failure", "must not be silently stale")
	memory.RevisionMetadata = manifestJSON
	var evidenceManifest rhizome.EvidenceManifest
	if err := json.Unmarshal([]byte(manifestJSON), &evidenceManifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	memory.ContentIdentity = evidenceManifest.ManifestHash
	writeTaskContextFile(t, root, "failure.go", "package fixture\n\nfunc Failure() { /* changed */ }\n")

	memoryIndex := &taskContextTestMemoryIndex{
		memories: []rhizome.Memory{memory},
		storeErr: errors.New("store unavailable"),
	}
	_, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "store failure",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memoryIndex,
		MemoryRepositoryName: "fixture",
	}, nil, "fixture")
	if err == nil || !strings.Contains(err.Error(), "persist source-local stale transition") {
		t.Fatalf("StoreMemory failure did not fail canonical assembly: %v", err)
	}
}

// TestTaskContextEvidenceValidationDoesNotPersistStaleForNonCanonicalWorkspace
// verifies that when the execution workspace differs from the source repository
// (e.g. a managed-run terrarium), stale detection still omits but does NOT
// attempt to StoreMemory.
func TestTaskContextEvidenceValidationDoesNotPersistStaleForNonCanonicalWorkspace(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "source")
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	for _, dir := range []string{sourceRoot, workspaceRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	originalContent := []byte("package fixture\n\nfunc Baz() {}\n")
	// Write original content manifest referencing a workspace file.
	writeTaskContextFile(t, workspaceRoot, "baz.go", string(originalContent))
	manifestJSON := newEvidenceManifestJSON(t, "baz.go", originalContent)
	// Change the workspace file to trigger stale.
	writeTaskContextFile(t, workspaceRoot, "baz.go", "package fixture\n\nfunc Baz() { /* changed */ }\n")

	mem := newEstablishedBotanistMemory("fixture", "baz-constraint", "baz must be pure")
	mem.RevisionMetadata = manifestJSON
	var evidenceManifest rhizome.EvidenceManifest
	if err := json.Unmarshal([]byte(manifestJSON), &evidenceManifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	mem.ContentIdentity = evidenceManifest.ManifestHash

	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "baz constraint",
		SourceRepository:     sourceRoot,
		ExecutionWorkspace:   workspaceRoot, // different from source — no persistence
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if strings.Contains(assembly.Rendered, "baz must be pure") {
		t.Fatalf("stale memory should not have been rendered")
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryStale] != 1 {
		t.Fatalf("expected stale omission: %v", assembly.Manifest.OmissionCounts)
	}
	if len(memIdx.stored) != 0 {
		t.Fatalf("StoreMemory must not be called for non-canonical workspace; calls=%d", len(memIdx.stored))
	}
}

// TestTaskContextEvidenceValidationRejectsMalformedManifest verifies that a
// memory with RevisionMetadata that is not valid JSON or has wrong version is
// omitted as conflicted (fail closed).
func TestTaskContextEvidenceValidationRejectsMalformedManifest(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
	}{
		{"invalid-json", `{not json}`},
		{"wrong-version", `{"version":"v2","files":[{"relativePath":"foo.go","contentHash":"abc"}],"manifestHash":"abc"}`},
		{"empty-files", `{"version":"v1","files":[],"manifestHash":"abc"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTaskContextFile(t, root, "foo.go", "package fixture\n")
			mem := newEstablishedBotanistMemory("fixture", "bad-manifest-"+tc.name, "private content")
			mem.RevisionMetadata = tc.metadata
			memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
			assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
				TaskPrompt:           "bad manifest " + tc.name,
				SourceRepository:     root,
				ExecutionWorkspace:   root,
				MemoryIndex:          memIdx,
				MemoryRepositoryName: "fixture",
				StartingRevision:     "rev-1",
			}, nil, "fixture")
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if strings.Contains(assembly.Rendered, "private content") {
				t.Fatalf("malformed manifest memory admitted: %q", assembly.Rendered)
			}
			if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryConflicted] != 1 {
				t.Fatalf("expected conflicted omission for %s, got: %v", tc.name, assembly.Manifest.OmissionCounts)
			}
		})
	}
}

func TestTaskContextEvidenceValidationRejectsStructurallyMalformedV1Manifest(t *testing.T) {
	fileHash := taskContextContentIdentity([]byte("package fixture\n"))
	cases := []struct {
		name          string
		files         []rhizome.EvidenceFile
		manifestHash  string
		workspaceFile string
	}{
		{
			name:          "malformed-content-hash",
			files:         []rhizome.EvidenceFile{{RelativePath: "foo.go", ContentHash: "not-a-sha256"}},
			workspaceFile: "foo.go",
		},
		{
			name: "duplicate-path",
			files: []rhizome.EvidenceFile{
				{RelativePath: "foo.go", ContentHash: fileHash},
				{RelativePath: "foo.go", ContentHash: fileHash},
			},
			workspaceFile: "foo.go",
		},
		{
			name: "unsorted-paths",
			files: []rhizome.EvidenceFile{
				{RelativePath: "z.go", ContentHash: fileHash},
				{RelativePath: "a.go", ContentHash: fileHash},
			},
			workspaceFile: "z.go",
		},
		{
			name:          "malformed-manifest-hash",
			files:         []rhizome.EvidenceFile{{RelativePath: "foo.go", ContentHash: fileHash}},
			manifestHash:  "not-a-sha256",
			workspaceFile: "foo.go",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTaskContextFile(t, root, tc.workspaceFile, "package fixture\n")
			manifestJSON := evidenceManifestJSONForTest(t, tc.files, tc.manifestHash)
			var storedManifest rhizome.EvidenceManifest
			if err := json.Unmarshal([]byte(manifestJSON), &storedManifest); err != nil {
				t.Fatalf("parse manifest: %v", err)
			}
			memory := newEstablishedBotanistMemory("fixture", "structural-"+tc.name, "private structural memory")
			memory.RevisionMetadata = manifestJSON
			memory.ContentIdentity = storedManifest.ManifestHash
			memoryIndex := &taskContextTestMemoryIndex{memories: []rhizome.Memory{memory}}
			assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
				TaskPrompt:           "structural " + tc.name,
				SourceRepository:     root,
				ExecutionWorkspace:   root,
				MemoryIndex:          memoryIndex,
				MemoryRepositoryName: "fixture",
			}, nil, "fixture")
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if strings.Contains(assembly.Rendered, "private structural memory") {
				t.Fatalf("structurally malformed manifest was admitted: %q", assembly.Rendered)
			}
			if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryConflicted] != 1 {
				t.Fatalf("expected conflicted omission for %s, got: %v", tc.name, assembly.Manifest.OmissionCounts)
			}
			if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryStale] != 0 {
				t.Fatalf("structurally malformed manifest was classified stale: %v", assembly.Manifest.OmissionCounts)
			}
		})
	}
}

// TestTaskContextMemoryPriorityConstraintPrecedesFact verifies that a
// constraint-kind memory has higher priority (lower number → admitted first)
// than an observation-kind memory when competing for the same item budget.
func TestTaskContextMemoryPriorityConstraintPrecedesFact(t *testing.T) {
	root := t.TempDir()
	observation := newEstablishedBotanistMemory("fixture", "obs-1", "observation content")
	observation.Kind = rhizome.KindObservation

	constraint := newEstablishedBotanistMemory("fixture", "cons-1", "constraint content")
	constraint.Kind = rhizome.KindConstraint

	t.Setenv(taskContextMaxItemsEnv, "1")
	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{observation, constraint}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "obs cons",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.Contains(assembly.Rendered, "constraint content") {
		t.Fatalf("constraint-kind memory was not admitted first; rendered=%q", assembly.Rendered)
	}
	if strings.Contains(assembly.Rendered, "observation content") {
		t.Fatalf("observation-kind memory should have been displaced by budget: rendered=%q", assembly.Rendered)
	}
}

// TestTaskContextEvidenceValidationManifestHashMismatchIsConflicted verifies
// that a memory whose stored ContentIdentity disagrees with the recomputed
// manifest hash fails closed as conflicted.
func TestTaskContextEvidenceValidationManifestHashMismatchIsConflicted(t *testing.T) {
	root := t.TempDir()
	fileContent := []byte("package fixture\n\nfunc Foo() {}\n")
	writeTaskContextFile(t, root, "foo.go", string(fileContent))

	manifestJSON := newEvidenceManifestJSON(t, "foo.go", fileContent)
	mem := newEstablishedBotanistMemory("fixture", "hash-mismatch", "private content")
	mem.RevisionMetadata = manifestJSON
	// Deliberately set wrong ContentIdentity.
	mem.ContentIdentity = "000000000000000000000000000000000000000000000000000000000000cafe"

	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "hash mismatch",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if strings.Contains(assembly.Rendered, "private content") {
		t.Fatalf("hash-mismatched memory was admitted: %q", assembly.Rendered)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryConflicted] != 1 {
		t.Fatalf("expected conflicted omission: %v", assembly.Manifest.OmissionCounts)
	}
}

// TestTaskContextEvidenceValidationNoManifestAdmitsWithoutValidityRef verifies
// that a memory without RevisionMetadata (no file evidence) is still admitted
// when eligible, and its manifest item has an empty ValidityRef.
func TestTaskContextEvidenceValidationNoManifestAdmitsWithoutValidityRef(t *testing.T) {
	root := t.TempDir()
	mem := newEstablishedBotanistMemory("fixture", "repository-wide", "repository-wide fact")
	// No RevisionMetadata — repository-wide memory.

	memIdx := &taskContextTestMemoryIndex{memories: []rhizome.Memory{mem}}
	assembly, err := assembleTaskContext(context.Background(), taskContextAssemblyInput{
		TaskPrompt:           "repository wide",
		SourceRepository:     root,
		ExecutionWorkspace:   root,
		MemoryIndex:          memIdx,
		MemoryRepositoryName: "fixture",
		StartingRevision:     "rev-1",
	}, nil, "fixture")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.Contains(assembly.Rendered, "repository-wide fact") {
		t.Fatalf("repository-wide memory was not admitted; rendered=%q omissions=%v", assembly.Rendered, assembly.Manifest.OmissionCounts)
	}
	if len(assembly.Manifest.Items) == 0 {
		t.Fatal("no manifest items")
	}
	var memItem *taskContextManifestItem
	for i := range assembly.Manifest.Items {
		if assembly.Manifest.Items[i].Kind == taskContextEvidenceMemory {
			memItem = &assembly.Manifest.Items[i]
			break
		}
	}
	if memItem == nil {
		t.Fatalf("no project-memory manifest item: %+v", assembly.Manifest.Items)
	}
	if memItem.ValidityRef != "" {
		t.Fatalf("repository-wide memory should have empty ValidityRef, got %q", memItem.ValidityRef)
	}
}
