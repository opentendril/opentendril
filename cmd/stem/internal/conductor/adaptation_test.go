package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
	"github.com/opentendril/opentendril/roots/llm"
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

func (f *fakeMeristem) CallPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return f.Call(ctx, []llm.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userPrompt}})
}

func TestAdaptFromHistoryCreatesSourceLocalProposals(t *testing.T) {
	workspace := proposalWorkspace(t)
	legacyPath := filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")
	legacy := []byte("legacy adaptation material\n")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy genome: %v", err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0o644); err != nil {
		t.Fatalf("write legacy genome: %v", err)
	}

	fake := &fakeMeristem{responses: []string{"- Errors wrap causes with %w.\n- Keep files repository-specific."}}
	chronicler := &EpigeneticChronicler{workspace: workspace, coordinator: fake}
	commits := []CommitSample{{Hash: "abc123def456789", Message: "feat: add widget", Diff: "diff --git a/widget.go b/widget.go\n+func NewWidget() {}"}}
	if err := chronicler.AdaptFromHistory(context.Background(), commits); err != nil {
		t.Fatalf("AdaptFromHistory: %v", err)
	}
	if len(fake.prompts) != 1 || !strings.Contains(fake.prompts[0], "abc123def456") {
		t.Fatalf("unexpected Meristem prompts: %#v", fake.prompts)
	}

	index, repositoryName, err := openRhizomeIndex(context.Background(), workspace)
	if err != nil {
		t.Fatalf("open source-local Rhizome: %v", err)
	}
	defer index.Close()
	for _, content := range []string{"Errors wrap causes with %w.", "Keep files repository-specific."} {
		memory, found, err := index.GetMemory(context.Background(), repositoryName, proposalTitle(content))
		if err != nil || !found {
			t.Fatalf("proposal %q found=%v err=%v", content, found, err)
		}
		if memory.Origin != rhizome.OriginMycorrhizal || memory.Authority != rhizome.AuthorityNone || memory.Status != rhizome.StatusProposed || memory.Kind != rhizome.KindObservation {
			t.Fatalf("adaptation envelope = %#v", memory)
		}
		if memory.SourceClass != mycorrhizalAdaptationSourceClass {
			t.Errorf("source class = %q, want %q", memory.SourceClass, mycorrhizalAdaptationSourceClass)
		}
		if strings.Contains(memory.Provenance, "diff --git") || strings.Contains(memory.Provenance, "func NewWidget") {
			t.Errorf("raw diff leaked into adaptation provenance: %s", memory.Provenance)
		}
		var provenance proposalProvenance
		if err := json.Unmarshal([]byte(memory.Provenance), &provenance); err != nil {
			t.Fatalf("decode provenance: %v", err)
		}
		if provenance.CommitIdentityCount != 1 {
			t.Errorf("commit identity count = %d, want 1", provenance.CommitIdentityCount)
		}
	}
	gotLegacy, err := os.ReadFile(legacyPath)
	if err != nil || string(gotLegacy) != string(legacy) {
		t.Fatalf("legacy adaptation genome changed: %q err=%v", gotLegacy, err)
	}
}

func TestAdaptFromHistoryUsesDeterministicOrderedCommitIdentity(t *testing.T) {
	commits := []CommitSample{{Hash: "first"}, {Hash: "second"}}
	reversed := []CommitSample{{Hash: "second"}, {Hash: "first"}}
	if adaptationProposalSourceIdentity(commits) == adaptationProposalSourceIdentity(reversed) {
		t.Fatal("ordered commit identities produced the same adaptation source identity")
	}
	if adaptationProposalSourceIdentity(commits) != adaptationProposalSourceIdentity([]CommitSample{{Hash: "first"}, {Hash: "second"}}) {
		t.Fatal("same ordered commit identities were not deterministic")
	}
}

func TestAdaptFromHistoryConsolidatesMultipleChunksIntoProposals(t *testing.T) {
	workspace := proposalWorkspace(t)
	bigDiff := "diff --git a/big.go b/big.go\n" + strings.Repeat("+var padding = 1\n", 2500)
	fake := &fakeMeristem{responses: []string{
		"- Guard clauses preferred.",
		"- Guard clauses preferred.\n- Tables driven tests.",
		"- Guard clauses preferred.\n- Table-driven tests.",
	}}
	chronicler := &EpigeneticChronicler{workspace: workspace, coordinator: fake}
	if err := chronicler.AdaptFromHistory(context.Background(), []CommitSample{{Hash: "aaa111", Message: "refactor", Diff: bigDiff}}); err != nil {
		t.Fatalf("AdaptFromHistory: %v", err)
	}
	if len(fake.prompts) != 3 || !strings.Contains(fake.prompts[2], "proposal candidates") {
		t.Fatalf("expected extraction and proposal consolidation calls, got %d prompts", len(fake.prompts))
	}
	index, repositoryName, err := openRhizomeIndex(context.Background(), workspace)
	if err != nil {
		t.Fatalf("open Rhizome: %v", err)
	}
	defer index.Close()
	if _, found, err := index.GetMemory(context.Background(), repositoryName, proposalTitle("Table-driven tests.")); err != nil || !found {
		t.Fatalf("consolidated proposal found=%v err=%v", found, err)
	}
}

func TestAdaptFromHistoryPropagatesMeristemFailureWithoutPersistence(t *testing.T) {
	workspace := proposalWorkspace(t)
	fake := &fakeMeristem{failWith: errors.New("connection refused")}
	chronicler := &EpigeneticChronicler{workspace: workspace, coordinator: fake}
	err := chronicler.AdaptFromHistory(context.Background(), []CommitSample{{Hash: "bbb222", Message: "fix", Diff: "diff --git a/a.go b/a.go\n+x"}})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("AdaptFromHistory error = %v, want Meristem failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, ".tendril", "rhizome.db")); !os.IsNotExist(statErr) {
		t.Fatalf("failed adaptation should not create proposal store, stat err=%v", statErr)
	}
}

func TestChunkAdaptationCorpusPacksSmallCommits(t *testing.T) {
	commits := []CommitSample{{Hash: "one", Message: "first", Diff: "diff --git a/a.go b/a.go\n+a"}, {Hash: "two", Message: "second", Diff: "diff --git a/b.go b/b.go\n+b"}, {Hash: "empty", Message: "no diff", Diff: "   "}}
	chunks := chunkAdaptationCorpus(commits, 24000)
	if len(chunks) != 1 || !strings.Contains(chunks[0], "## Commit one") || !strings.Contains(chunks[0], "## Commit two") || strings.Contains(chunks[0], "no diff") {
		t.Fatalf("unexpected packed chunks: %s", strings.Join(chunks, "\n"))
	}
}

func TestChunkAdaptationCorpusSplitsAtFileBoundaries(t *testing.T) {
	fileA := "diff --git a/a.go b/a.go\n" + strings.Repeat("+alpha\n", 300)
	fileB := "diff --git a/b.go b/b.go\n" + strings.Repeat("+beta\n", 300)
	chunks := chunkAdaptationCorpus([]CommitSample{{Hash: "split", Message: "big commit", Diff: fileA + "\n" + fileB}}, 3000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	joined := strings.Join(chunks, "\n")
	if !strings.Contains(joined, "a/a.go") || !strings.Contains(joined, "a/b.go") {
		t.Fatalf("file boundaries lost across chunks: %s", joined)
	}
}
