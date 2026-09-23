package conductor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
)

func proposalWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	if _, err := runGitCommand(context.Background(), workspace, "init", "-q"); err != nil {
		t.Fatalf("init proposal test Substrate: %v", err)
	}
	return workspace
}

type testChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Temperature float64 `json:"temperature"`
	Stream      bool    `json:"stream"`
}

type proposalTestBackend struct {
	memories  map[string]rhizome.Memory
	getErr    error
	storeErr  error
	storeCall int
}

func (b *proposalTestBackend) key(repositoryName, title string) string {
	return repositoryName + "\x00" + title
}

func (b *proposalTestBackend) DeleteMemory(context.Context, string, string) error { return nil }

func (b *proposalTestBackend) GetMemory(_ context.Context, repositoryName, title string) (rhizome.Memory, bool, error) {
	if b.getErr != nil {
		return rhizome.Memory{}, false, b.getErr
	}
	memory, found := b.memories[b.key(repositoryName, title)]
	return memory, found, nil
}

func (b *proposalTestBackend) ListMemories(context.Context, string, string, int) ([]rhizome.Memory, error) {
	return nil, nil
}

func (b *proposalTestBackend) SearchMemories(context.Context, string, string, string, int) ([]rhizome.Memory, error) {
	return nil, nil
}

func (b *proposalTestBackend) StoreMemory(_ context.Context, memory rhizome.Memory) error {
	b.storeCall++
	if b.storeErr != nil {
		return b.storeErr
	}
	b.memories[b.key(memory.RepositoryName, memory.Title)] = memory
	return nil
}

func TestTranscribeLearningsCreatesSourceLocalProposals(t *testing.T) {
	workspace := proposalWorkspace(t)
	legacyPath := filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")
	legacy := []byte("# preserved legacy genome\n\n- old rule\n")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy genome: %v", err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0o644); err != nil {
		t.Fatalf("write legacy genome: %v", err)
	}

	fake := &fakeLLM{response: "- Keep source-local indexes authoritative.\n- Use bounded task context.\n- No durable learnings."}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}
	if _, err := chronicler.TranscribeLearningsWithProvenance(
		context.Background(),
		"raw transcript must not be stored",
		"raw diff must not be stored",
		"raw logs must not be stored",
		"step/unsafe",
		"phytomer-42",
		workspace,
	); err != nil {
		t.Fatalf("TranscribeLearningsWithProvenance: %v", err)
	}

	index, repositoryName, err := openRhizomeIndex(context.Background(), workspace)
	if err != nil {
		t.Fatalf("open source-local Rhizome: %v", err)
	}
	defer index.Close()

	for _, content := range []string{"Keep source-local indexes authoritative.", "Use bounded task context."} {
		title := proposalTitle(content)
		memory, found, err := index.GetMemory(context.Background(), repositoryName, title)
		if err != nil || !found {
			t.Fatalf("proposal %q lookup found=%v err=%v", title, found, err)
		}
		if memory.Content != content {
			t.Errorf("content = %q, want %q", memory.Content, content)
		}
		if memory.Origin != rhizome.OriginMycorrhizal || memory.Authority != rhizome.AuthorityNone || memory.Status != rhizome.StatusProposed {
			t.Errorf("proposal envelope = %s/%s/%s, want mycorrhizal/none/proposed", memory.Origin, memory.Authority, memory.Status)
		}
		if memory.Kind != rhizome.KindObservation {
			t.Errorf("kind = %q, want observation", memory.Kind)
		}
		if memory.ContentIdentity != proposalContentIdentity(content) {
			t.Errorf("content identity = %q, want %q", memory.ContentIdentity, proposalContentIdentity(content))
		}
		if memory.StableID != rhizome.StableMemoryIdentity(repositoryName, title) {
			t.Errorf("stable ID = %q, want deterministic repository/title identity", memory.StableID)
		}
		if memory.SessionID != "phytomer-42" {
			t.Errorf("session ID = %q, want safe Phytomer ID", memory.SessionID)
		}
		for _, secret := range []string{"raw transcript", "raw diff", "raw logs", "step/unsafe"} {
			if strings.Contains(memory.Provenance, secret) {
				t.Errorf("provenance contains unsafe value %q: %s", secret, memory.Provenance)
			}
		}
	}

	gotLegacy, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy genome: %v", err)
	}
	if string(gotLegacy) != string(legacy) {
		t.Fatalf("legacy epigenetics.md changed: %q", gotLegacy)
	}
}

func TestTranscribeLearningsSkipsSentinelAndEmptyDiff(t *testing.T) {
	workspace := proposalWorkspace(t)
	fake := &fakeLLM{response: "- No durable learnings."}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}
	post, err := chronicler.TranscribeLearnings(context.Background(), "transcript", "diff", "logs")
	if err != nil {
		t.Fatalf("sentinel transcription: %v", err)
	}
	if !post.RequestsMade {
		t.Fatal("sentinel request was not counted")
	}
	if _, err := os.Stat(filepath.Join(workspace, ".tendril", "rhizome.db")); !os.IsNotExist(err) {
		t.Fatalf("sentinel should create no proposal store, stat err=%v", err)
	}

	noCall := &fakeLLM{response: "- should not be called"}
	chronicler.client = noCall
	post, err = chronicler.TranscribeLearnings(context.Background(), "transcript", "   ", "logs")
	if err != nil {
		t.Fatalf("empty diff transcription: %v", err)
	}
	if post.RequestsMade || len(noCall.calls) != 0 {
		t.Fatal("empty diff made a provider request")
	}
}

func TestTranscribeLearningsDoesNotOverwriteExistingProposal(t *testing.T) {
	workspace := proposalWorkspace(t)
	index, repositoryName, err := openRhizomeIndex(context.Background(), workspace)
	if err != nil {
		t.Fatalf("open Rhizome: %v", err)
	}
	content := "Do not demote an established proposal."
	title := proposalTitle(content)
	existing := rhizome.Memory{
		RepositoryName:  repositoryName,
		Category:        mycorrhizalProposalCategory,
		Title:           title,
		Content:         content,
		Origin:          rhizome.OriginMycorrhizal,
		Authority:       rhizome.AuthorityBotanist,
		Status:          rhizome.StatusEstablished,
		Kind:            rhizome.KindObservation,
		Provenance:      `{"decision":"botanist"}`,
		SourceClass:     mycorrhizalPostRunSourceClass,
		SourceIdentity:  "original-source",
		ContentIdentity: proposalContentIdentity(content),
		StableID:        rhizome.StableMemoryIdentity(repositoryName, title),
	}
	if err := index.StoreMemory(context.Background(), existing); err != nil {
		index.Close()
		t.Fatalf("seed proposal: %v", err)
	}
	index.Close()

	chronicler := &EpigeneticChronicler{workspace: workspace, client: &fakeLLM{response: "- Do not demote an established proposal."}}
	if _, err := chronicler.TranscribeLearningsWithProvenance(context.Background(), "transcript", "diff", "logs", "step", "session", workspace); err != nil {
		t.Fatalf("repeat transcription: %v", err)
	}

	index, _, err = openRhizomeIndex(context.Background(), workspace)
	if err != nil {
		t.Fatalf("reopen Rhizome: %v", err)
	}
	defer index.Close()
	got, found, err := index.GetMemory(context.Background(), repositoryName, title)
	if err != nil || !found {
		t.Fatalf("lookup existing proposal found=%v err=%v", found, err)
	}
	if got.Status != rhizome.StatusEstablished || got.Authority != rhizome.AuthorityBotanist || got.Provenance != existing.Provenance || got.SourceIdentity != existing.SourceIdentity {
		t.Fatalf("existing Botanist decision was changed: %#v", got)
	}
}

func TestProposalIdentityCollisionsAndStoreErrorsFailClosed(t *testing.T) {
	ctx := context.Background()
	content := "A deterministic proposal content value."
	title := proposalTitle(content)

	for _, existingStatus := range []rhizome.Status{rhizome.StatusRejected, rhizome.StatusEstablished} {
		t.Run(string(existingStatus), func(t *testing.T) {
			backend := &proposalTestBackend{memories: map[string]rhizome.Memory{}}
			backend.memories[backend.key("repo", title)] = rhizome.Memory{RepositoryName: "repo", Title: title, Content: content, Status: existingStatus, Authority: rhizome.AuthorityBotanist, Origin: rhizome.OriginMycorrhizal, Kind: rhizome.KindObservation, StableID: rhizome.StableMemoryIdentity("repo", title)}
			if err := persistMycorrhizalProposals(ctx, backend, "repo", []string{content}, mycorrhizalPostRunSourceClass, "source", "session", `{}`); err != nil {
				t.Fatalf("repeat proposal: %v", err)
			}
			if backend.storeCall != 0 {
				t.Fatalf("existing %s proposal was overwritten", existingStatus)
			}
		})
	}

	backend := &proposalTestBackend{memories: map[string]rhizome.Memory{}}
	backend.memories[backend.key("repo", title)] = rhizome.Memory{RepositoryName: "repo", Title: title, Content: "different content", Status: rhizome.StatusRejected, Authority: rhizome.AuthorityBotanist, Origin: rhizome.OriginMycorrhizal, Kind: rhizome.KindObservation, StableID: rhizome.StableMemoryIdentity("repo", title)}
	if err := persistMycorrhizalProposals(ctx, backend, "repo", []string{content}, mycorrhizalPostRunSourceClass, "source", "session", `{}`); err == nil || !strings.Contains(err.Error(), "identity collision") {
		t.Fatalf("collision error = %v, want fail-closed identity collision", err)
	}

	backend = &proposalTestBackend{memories: map[string]rhizome.Memory{}, storeErr: errors.New("store unavailable")}
	if err := persistMycorrhizalProposals(ctx, backend, "repo", []string{content}, mycorrhizalPostRunSourceClass, "source", "session", `{}`); err == nil || !strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("store error = %v, want reported persistence failure", err)
	}
}

func TestProposalLifecycleEligibilityConfirmAndReject(t *testing.T) {
	ctx := context.Background()
	workspace := proposalWorkspace(t)
	index, repositoryName, err := openRhizomeIndex(ctx, workspace)
	if err != nil {
		t.Fatalf("open Rhizome: %v", err)
	}
	defer index.Close()

	content := "Use narrow interfaces for repository boundaries."
	if err := persistMycorrhizalProposals(ctx, index, repositoryName, []string{content}, mycorrhizalPostRunSourceClass, "source", "session", `{}`); err != nil {
		t.Fatalf("persist proposal: %v", err)
	}
	assembly, err := assembleTaskContext(ctx, taskContextAssemblyInput{TaskPrompt: "*", SourceRepository: workspace, ExecutionWorkspace: workspace, MemoryIndex: index, MemoryRepositoryName: repositoryName}, nil, "lifecycle-step")
	if err != nil {
		t.Fatalf("assemble proposed context: %v", err)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryProposed] != 1 || strings.Contains(assembly.Rendered, content) {
		t.Fatalf("proposed memory was eligible: rendered=%q omissions=%v", assembly.Rendered, assembly.Manifest.OmissionCounts)
	}

	title := proposalTitle(content)
	if _, err := rhizome.ApplyConfirm(ctx, index, repositoryName, title); err != nil {
		t.Fatalf("confirm proposal: %v", err)
	}
	assembly, err = assembleTaskContext(ctx, taskContextAssemblyInput{TaskPrompt: "*", SourceRepository: workspace, ExecutionWorkspace: workspace, MemoryIndex: index, MemoryRepositoryName: repositoryName}, nil, "lifecycle-step")
	if err != nil {
		t.Fatalf("assemble confirmed context: %v", err)
	}
	if !strings.Contains(assembly.Rendered, content) {
		t.Fatalf("confirmed memory was not eligible: rendered=%q omissions=%v", assembly.Rendered, assembly.Manifest.OmissionCounts)
	}

	rejectedContent := "Reject this proposed learning."
	if err := persistMycorrhizalProposals(ctx, index, repositoryName, []string{rejectedContent}, mycorrhizalPostRunSourceClass, "source", "session", `{}`); err != nil {
		t.Fatalf("persist rejected proposal: %v", err)
	}
	if _, err := rhizome.ApplyReject(ctx, index, repositoryName, proposalTitle(rejectedContent)); err != nil {
		t.Fatalf("reject proposal: %v", err)
	}
	assembly, err = assembleTaskContext(ctx, taskContextAssemblyInput{TaskPrompt: "*", SourceRepository: workspace, ExecutionWorkspace: workspace, MemoryIndex: index, MemoryRepositoryName: repositoryName}, nil, "lifecycle-step")
	if err != nil {
		t.Fatalf("assemble rejected context: %v", err)
	}
	if assembly.Manifest.OmissionCounts[taskContextOmissionMemoryRejected] != 1 || strings.Contains(assembly.Rendered, rejectedContent) {
		t.Fatalf("rejected memory was eligible: rendered=%q omissions=%v", assembly.Rendered, assembly.Manifest.OmissionCounts)
	}
}

func TestProposalRepetitionAndFitnessCannotPromote(t *testing.T) {
	workspace := proposalWorkspace(t)
	content := "Repeated model output remains a proposal."
	chronicler := &EpigeneticChronicler{workspace: workspace, client: &fakeLLM{response: "- " + content}}
	for range 2 {
		if _, err := chronicler.TranscribeLearningsWithProvenance(context.Background(), "transcript", "diff", "logs", "step", "session", workspace); err != nil {
			t.Fatalf("repeat transcription: %v", err)
		}
	}
	if err := RecordGenomicFitness(workspace, true); err != nil {
		t.Fatalf("RecordGenomicFitness: %v", err)
	}
	index, repositoryName, err := openRhizomeIndex(context.Background(), workspace)
	if err != nil {
		t.Fatalf("open Rhizome: %v", err)
	}
	defer index.Close()
	got, found, err := index.GetMemory(context.Background(), repositoryName, proposalTitle(content))
	if err != nil || !found {
		t.Fatalf("proposal lookup found=%v err=%v", found, err)
	}
	if got.Status != rhizome.StatusProposed || got.Authority != rhizome.AuthorityNone {
		t.Fatalf("repetition or fitness promoted proposal: %#v", got)
	}
}

func TestReduceGenomeFileRemainsExplicit(t *testing.T) {
	workspace := proposalWorkspace(t)
	genomePath := filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")
	if err := os.MkdirAll(filepath.Dir(genomePath), 0o755); err != nil {
		t.Fatalf("mkdir genome: %v", err)
	}
	if err := os.WriteFile(genomePath, []byte(epigeneticGenomeHeader+"\n\n- old rule\n"), 0o644); err != nil {
		t.Fatalf("write genome: %v", err)
	}
	fake := &fakeLLM{response: "- reduced rule"}
	chronicler := &EpigeneticChronicler{workspace: workspace, client: fake}
	if usage, err := chronicler.ReduceGenomeFile(context.Background()); err != nil || !usage.RequestsMade {
		t.Fatalf("explicit reduction usage=%#v err=%v", usage, err)
	}
	content, err := os.ReadFile(genomePath)
	if err != nil || !strings.Contains(string(content), "- reduced rule") {
		t.Fatalf("explicit genome reduction result=%q err=%v", content, err)
	}
}

func proposalTitle(content string) string {
	return "proposal:" + proposalContentIdentity(content)
}

func proposalContentIdentity(content string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
