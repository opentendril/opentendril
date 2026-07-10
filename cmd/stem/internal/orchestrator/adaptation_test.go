package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/core/roots/llm"
)

type fakeMeristem struct {
	responses []string
	prompts   []string
	failWith  error
}

func (f *fakeMeristem) Call(ctx context.Context, messages []llm.Message) (string, error) {
	if f.failWith != nil {
		return "", f.failWith
	}

	var userPrompt string
	for _, message := range messages {
		if message.Role == "user" {
			userPrompt = message.Content
		}
	}
	f.prompts = append(f.prompts, userPrompt)

	index := len(f.prompts) - 1
	if index >= len(f.responses) {
		return "- No recurring traits.", nil
	}
	return f.responses[index], nil
}

func (f *fakeMeristem) CallStream(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (string, error) {
	return f.Call(ctx, messages)
}

func TestAdaptFromHistoryWritesTraitsToGenome(t *testing.T) {
	workspace := t.TempDir()
	fake := &fakeMeristem{
		responses: []string{"- Errors are wrapped with fmt.Errorf and %w.\n- Filenames use mergedlowercase.go."},
	}

	chronicler := &EpigeneticChronicler{workspace: workspace, coordinator: fake}
	commits := []CommitSample{
		{Hash: "abc123def456789", Message: "feat: add widget", Diff: "diff --git a/widget.go b/widget.go\n+func NewWidget() {}"},
	}

	if err := chronicler.AdaptFromHistory(context.Background(), commits); err != nil {
		t.Fatalf("AdaptFromHistory failed: %v", err)
	}

	if len(fake.prompts) != 1 {
		t.Fatalf("expected 1 Meristem call for a single chunk, got %d", len(fake.prompts))
	}
	if !strings.Contains(fake.prompts[0], "abc123def456") {
		t.Fatalf("prompt missing short commit hash: %s", fake.prompts[0])
	}
	if !strings.Contains(fake.prompts[0], "feat: add widget") {
		t.Fatalf("prompt missing commit message: %s", fake.prompts[0])
	}

	content, err := os.ReadFile(filepath.Join(workspace, ".tendril", "genome", "epigenetics.md"))
	if err != nil {
		t.Fatalf("read genome: %v", err)
	}
	if !strings.HasPrefix(string(content), epigeneticGenomeHeader) {
		t.Fatalf("genome missing header: %s", content)
	}
	if !strings.Contains(string(content), "mergedlowercase.go") {
		t.Fatalf("genome missing extracted trait: %s", content)
	}
}

func TestAdaptFromHistoryConsolidatesMultipleChunks(t *testing.T) {
	workspace := t.TempDir()

	bigDiff := "diff --git a/big.go b/big.go\n" + strings.Repeat("+var padding = 1\n", 2500)
	fake := &fakeMeristem{
		responses: []string{
			"- Guard clauses preferred.",
			"- Guard clauses preferred.\n- Tables driven tests.",
			"- Guard clauses preferred.\n- Table-driven tests.",
		},
	}

	chronicler := &EpigeneticChronicler{workspace: workspace, coordinator: fake}
	commits := []CommitSample{
		{Hash: "aaa111", Message: "refactor: split handlers", Diff: bigDiff},
	}

	if err := chronicler.AdaptFromHistory(context.Background(), commits); err != nil {
		t.Fatalf("AdaptFromHistory failed: %v", err)
	}

	if len(fake.prompts) != 3 {
		t.Fatalf("expected 2 extraction calls + 1 consolidation call, got %d", len(fake.prompts))
	}
	if !strings.Contains(fake.prompts[2], "Trait candidates") {
		t.Fatalf("final call is not a consolidation prompt: %s", fake.prompts[2])
	}

	content, err := os.ReadFile(filepath.Join(workspace, ".tendril", "genome", "epigenetics.md"))
	if err != nil {
		t.Fatalf("read genome: %v", err)
	}
	if !strings.Contains(string(content), "Table-driven tests") {
		t.Fatalf("genome missing consolidated trait: %s", content)
	}
}

func TestAdaptFromHistoryPropagatesMeristemFailure(t *testing.T) {
	workspace := t.TempDir()
	fake := &fakeMeristem{failWith: fmt.Errorf("connection refused")}

	chronicler := &EpigeneticChronicler{workspace: workspace, coordinator: fake}
	commits := []CommitSample{
		{Hash: "bbb222", Message: "fix: typo", Diff: "diff --git a/a.go b/a.go\n+x"},
	}

	err := chronicler.AdaptFromHistory(context.Background(), commits)
	if err == nil {
		t.Fatal("expected error when Meristem is unreachable")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")); !os.IsNotExist(statErr) {
		t.Fatal("genome must not be written when Adaptation fails")
	}
}

func TestChunkAdaptationCorpusPacksSmallCommits(t *testing.T) {
	commits := []CommitSample{
		{Hash: "one", Message: "first", Diff: "diff --git a/a.go b/a.go\n+a"},
		{Hash: "two", Message: "second", Diff: "diff --git a/b.go b/b.go\n+b"},
		{Hash: "empty", Message: "no diff", Diff: "   "},
	}

	chunks := chunkAdaptationCorpus(commits, 24000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0], "## Commit one") || !strings.Contains(chunks[0], "## Commit two") {
		t.Fatalf("chunk missing commits: %s", chunks[0])
	}
	if strings.Contains(chunks[0], "no diff") {
		t.Fatalf("empty-diff commit must be skipped: %s", chunks[0])
	}
}

func TestChunkAdaptationCorpusSplitsAtFileBoundaries(t *testing.T) {
	fileA := "diff --git a/a.go b/a.go\n" + strings.Repeat("+alpha\n", 300)
	fileB := "diff --git a/b.go b/b.go\n" + strings.Repeat("+beta\n", 300)
	commits := []CommitSample{
		{Hash: "split", Message: "big commit", Diff: fileA + "\n" + fileB},
	}

	chunks := chunkAdaptationCorpus(commits, 3000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for index, chunk := range chunks {
		if len(chunk) > 3000 {
			t.Fatalf("chunk %d exceeds limit: %d chars", index, len(chunk))
		}
		if !strings.Contains(chunk, "## Commit split") {
			t.Fatalf("chunk %d missing commit header: %s", index, chunk)
		}
	}

	joined := strings.Join(chunks, "\n")
	if !strings.Contains(joined, "a/a.go") || !strings.Contains(joined, "a/b.go") {
		t.Fatalf("file boundaries lost across chunks: %s", joined)
	}
}

func TestSplitDiffSegmentsHardSplitsOversizedFile(t *testing.T) {
	giant := "diff --git a/huge.go b/huge.go\n" + strings.Repeat("+line of code here\n", 500)

	segments := splitDiffSegments(giant, 2048)
	if len(segments) < 2 {
		t.Fatalf("expected hard split, got %d segment(s)", len(segments))
	}

	var total int
	for index, segment := range segments {
		if len(segment) > 2048 {
			t.Fatalf("segment %d exceeds limit: %d chars", index, len(segment))
		}
		total += len(segment)
	}
	if total < len(giant)-len(segments)*2 {
		t.Fatalf("content lost during hard split: %d of %d chars retained", total, len(giant))
	}
}
