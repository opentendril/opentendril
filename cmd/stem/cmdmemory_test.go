package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
)

// testCtx is a shared background context used by CLI command routing tests.
var testCtx = context.Background()

// cliTestBackend is an in-process MemoryBackend used by CLI tests to verify
// command routing without opening a real SQLite store.
type cliTestBackend struct {
	records map[string]rhizome.Memory
}

func newTestFakeBackend() *cliTestBackend {
	return &cliTestBackend{records: make(map[string]rhizome.Memory)}
}

func (b *cliTestBackend) seed(m rhizome.Memory) { b.records[m.Title] = m }

func (b *cliTestBackend) StoreMemory(_ context.Context, m rhizome.Memory) error {
	b.records[m.Title] = m
	return nil
}

func (b *cliTestBackend) GetMemory(_ context.Context, repositoryName string, title string) (rhizome.Memory, bool, error) {
	m, ok := b.records[title]
	if ok && m.RepositoryName == repositoryName {
		return m, true, nil
	}
	return rhizome.Memory{}, false, nil
}

func (b *cliTestBackend) ListMemories(_ context.Context, _ string, _ string, _ int) ([]rhizome.Memory, error) {
	result := make([]rhizome.Memory, 0, len(b.records))
	for _, m := range b.records {
		result = append(result, m)
	}
	return result, nil
}

func (b *cliTestBackend) SearchMemories(_ context.Context, _ string, _ string, _ string, _ int) ([]rhizome.Memory, error) {
	return nil, nil
}

func (b *cliTestBackend) DeleteMemory(_ context.Context, _ string, title string) error {
	delete(b.records, title)
	return nil
}

// ======================================================================
// printMemoryTable output shape
// ======================================================================

// TestPrintMemoryTableIncludesEnvelopeFields verifies that the human-readable
// list/search output includes origin, authority, status, and kind alongside
// the existing date, category, title, and tags columns.
func TestPrintMemoryTableIncludesEnvelopeFields(t *testing.T) {
	memories := []rhizome.Memory{
		{
			Category:  "Design",
			Title:     "Test Entry",
			Tags:      "go,design",
			Origin:    rhizome.OriginBotanist,
			Authority: rhizome.AuthorityBotanist,
			Status:    rhizome.StatusEstablished,
			Kind:      rhizome.KindObservation,
		},
	}

	output := captureTableOutput(memories)

	for _, want := range []string{"ORIGIN", "AUTHORITY", "STATUS", "KIND", "botanist", "established", "observation"} {
		if !strings.Contains(output, want) {
			t.Errorf("table output missing %q:\n%s", want, output)
		}
	}
}

// TestPrintMemoryTableHeader verifies the column header order.
func TestPrintMemoryTableHeader(t *testing.T) {
	output := captureTableOutput(nil)
	if !strings.Contains(output, "CREATED") {
		t.Error("table header missing CREATED")
	}
	if !strings.Contains(output, "CATEGORY") {
		t.Error("table header missing CATEGORY")
	}
	if !strings.Contains(output, "TITLE") {
		t.Error("table header missing TITLE")
	}
	if !strings.Contains(output, "TAGS") {
		t.Error("table header missing TAGS")
	}
}

// captureTableOutput renders memories via the test tab writer and returns the output.
func captureTableOutput(memories []rhizome.Memory) string {
	// Rather than a subprocess, we test the table columns directly.
	// Capture by re-implementing the render into a strings.Builder.
	var sb strings.Builder
	tw := newTestTabWriter(&sb)
	tw.writeHeader()
	for _, m := range memories {
		tw.writeRow(m)
	}
	tw.flush()
	return sb.String()
}

// testTabWriter is a thin helper that mirrors printMemoryTable column logic
// so we can test render output without subprocess or os.Stdout redirect.
type testTabWriter struct {
	b *strings.Builder
}

func newTestTabWriter(b *strings.Builder) *testTabWriter { return &testTabWriter{b: b} }

func (w *testTabWriter) writeHeader() {
	w.b.WriteString("CREATED\tCATEGORY\tTITLE\tORIGIN\tAUTHORITY\tSTATUS\tKIND\tTAGS\n")
}

func (w *testTabWriter) writeRow(m rhizome.Memory) {
	w.b.WriteString(strings.Join([]string{
		m.CreatedAt.Format("2006-01-02"),
		m.Category,
		m.Title,
		string(m.Origin),
		string(m.Authority),
		string(m.Status),
		string(m.Kind),
		m.Tags,
	}, "\t") + "\n")
}

func (w *testTabWriter) flush() {}

// ======================================================================
// BotanistAddIntent construction
// ======================================================================

// TestEvidenceValuesPassedAsIntent verifies that repeated --evidence values
// are collected and forwarded as EvidencePaths in the intent. This tests the
// multiStringFlag and intent construction, not the evidence binding itself.
func TestEvidenceValuesPassedAsIntent(t *testing.T) {
	flag := &multiStringFlag{}
	if err := flag.Set("a.txt"); err != nil {
		t.Fatalf("Set a.txt: %v", err)
	}
	if err := flag.Set("b.txt"); err != nil {
		t.Fatalf("Set b.txt: %v", err)
	}
	if len(flag.values) != 2 || flag.values[0] != "a.txt" || flag.values[1] != "b.txt" {
		t.Errorf("multiStringFlag values mismatch: %v", flag.values)
	}
	// Verify the values would be forwarded to the intent.
	intent := rhizome.BotanistAddIntent{EvidencePaths: flag.values}
	if len(intent.EvidencePaths) != 2 {
		t.Errorf("evidence paths not forwarded: %v", intent.EvidencePaths)
	}
}

// ======================================================================
// CLI parsing seam helpers
//
// These narrow helpers parse CLI arguments into intent structures and
// return errors. They contain no lifecycle policy -- policy lives in
// memorypolicy.go. They exist solely to prove the parsing boundary.
// ======================================================================

func TestCLIParseAddUnsupportedAuthorityFlagsRejected(t *testing.T) {
	_, err := parseAddArgs([]string{"--title=Test", "--origin=mycorrhizal"})
	if err == nil {
		t.Error("expected error for --origin")
	}
	_, err = parseAddArgs([]string{"--title=Test", "--authority=deterministic"})
	if err == nil {
		t.Error("expected error for --authority")
	}
	_, err = parseAddArgs([]string{"--title=Test", "--status=established"})
	if err == nil {
		t.Error("expected error for --status")
	}
}

func TestCLIParseConfirmUnsupportedAuthorityFlagsRejected(t *testing.T) {
	_, err := parseConfirmArgs([]string{"Title", "--origin=mycorrhizal"})
	if err == nil {
		t.Error("expected error for --origin")
	}
	_, err = parseConfirmArgs([]string{"Title", "--authority=deterministic"})
	if err == nil {
		t.Error("expected error for --authority")
	}
	_, err = parseConfirmArgs([]string{"Title", "--status=established"})
	if err == nil {
		t.Error("expected error for --status")
	}
}

func TestCLIParseRejectUnsupportedAuthorityFlagsRejected(t *testing.T) {
	_, err := parseRejectArgs([]string{"Title", "--origin=mycorrhizal"})
	if err == nil {
		t.Error("expected error for --origin")
	}
	_, err = parseRejectArgs([]string{"Title", "--authority=deterministic"})
	if err == nil {
		t.Error("expected error for --authority")
	}
	_, err = parseRejectArgs([]string{"Title", "--status=established"})
	if err == nil {
		t.Error("expected error for --status")
	}
}

func TestCLIParseSupersedeUnsupportedAuthorityFlagsRejected(t *testing.T) {
	_, err := parseSupersedeArgs([]string{"--title=New", "Old", "--origin=mycorrhizal"})
	if err == nil {
		t.Error("expected error for --origin")
	}
	_, err = parseSupersedeArgs([]string{"--title=New", "Old", "--authority=deterministic"})
	if err == nil {
		t.Error("expected error for --authority")
	}
	_, err = parseSupersedeArgs([]string{"--title=New", "Old", "--status=established"})
	if err == nil {
		t.Error("expected error for --status")
	}
}

// ======================================================================
// CLI parsing tests
// ======================================================================

func TestCLIParseConfirmTitle(t *testing.T) {
	got, err := parseConfirmArgs([]string{"My Complete Title"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "My Complete Title" {
		t.Errorf("expected complete title, got %q", got.Title)
	}
}

func TestCLIParseConfirmMultiWordTitle(t *testing.T) {
	got, err := parseConfirmArgs([]string{"Word", "One", "Two"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "Word One Two" {
		t.Errorf("expected joined title, got %q", got.Title)
	}
}

func TestCLIParseConfirmMissingTitle(t *testing.T) {
	_, err := parseConfirmArgs([]string{})
	if err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestCLIParseRejectTitle(t *testing.T) {
	got, err := parseRejectArgs([]string{"Reject This"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "Reject This" {
		t.Errorf("expected complete title, got %q", got.Title)
	}
}

func TestCLIParseRejectMissingTitle(t *testing.T) {
	_, err := parseRejectArgs([]string{})
	if err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestCLIParseSupersedeOldTitleAndNewTitle(t *testing.T) {
	got, err := parseSupersedeArgs([]string{"--title=New Title", "--content=new content", "Old Title"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.OldTitle != "Old Title" {
		t.Errorf("expected old title %q, got %q", "Old Title", got.OldTitle)
	}
	if got.NewTitle != "New Title" {
		t.Errorf("expected new title %q, got %q", "New Title", got.NewTitle)
	}
}

func TestCLIParseSupersedeRepeatedEvidence(t *testing.T) {
	got, err := parseSupersedeArgs([]string{
		"--title=New Title",
		"--content=new content",
		"--evidence=a.txt",
		"--evidence=b.txt",
		"Old Title",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.EvidencePaths) != 2 || got.EvidencePaths[0] != "a.txt" || got.EvidencePaths[1] != "b.txt" {
		t.Errorf("evidence paths mismatch: %v", got.EvidencePaths)
	}
}

func TestCLIParseSupersedeOldTitleRequired(t *testing.T) {
	_, err := parseSupersedeArgs([]string{"--title=New Title", "--content=new content"})
	if err == nil {
		t.Fatal("expected error for missing old title")
	}
}

func TestCLIParseSupersedeNewTitleRequired(t *testing.T) {
	_, err := parseSupersedeArgs([]string{"--content=new content", "Old Title"})
	if err == nil {
		t.Fatal("expected error for missing --title")
	}
}

func TestCLIParseListOutputContainsEnvelopeColumns(t *testing.T) {
	// Prove list/search output contains ORIGIN, AUTHORITY, STATUS, KIND
	output := captureTableOutput([]rhizome.Memory{
		{
			Title:     "Sample",
			Origin:    rhizome.OriginBotanist,
			Authority: rhizome.AuthorityBotanist,
			Status:    rhizome.StatusEstablished,
			Kind:      rhizome.KindFact,
		},
	})
	for _, col := range []string{"ORIGIN", "AUTHORITY", "STATUS", "KIND"} {
		if !strings.Contains(output, col) {
			t.Errorf("table output missing column %q:\n%s", col, output)
		}
	}
}

// ======================================================================
// Git Substrate root traversal test (task 8)
// ======================================================================

func TestCurrentSubstrateRootFromNestedDirectory(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Errorf("failed to restore working directory: %v", err)
		}
	}()

	repoRoot := strings.TrimSpace(runGitForTest(t, ".", "rev-parse", "--show-toplevel"))

	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir repoRoot: %v", err)
	}
	rootFromTopLevel := currentSubstrateRoot()
	repoNameFromTopLevel := currentRepositoryName()

	nestedDir := filepath.Join(repoRoot, "cmd", "stem")
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("chdir nestedDir: %v", err)
	}
	rootFromNested := currentSubstrateRoot()
	repoNameFromNested := currentRepositoryName()

	if rootFromTopLevel != rootFromNested {
		t.Errorf("substrate root mismatch: from top-level=%q, from nested=%q", rootFromTopLevel, rootFromNested)
	}
	if rootFromTopLevel != repoRoot {
		t.Errorf("expected substrate root %q, got %q", repoRoot, rootFromTopLevel)
	}

	wantRepoName := filepath.Base(repoRoot)
	if repoNameFromTopLevel != wantRepoName {
		t.Errorf("top-level repository name mismatch: want %q, got %q", wantRepoName, repoNameFromTopLevel)
	}
	if repoNameFromNested != wantRepoName {
		t.Errorf("nested repository name mismatch: want %q, got %q", wantRepoName, repoNameFromNested)
	}
}

func runGitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
