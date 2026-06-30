package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRecordGenomicFitness(t *testing.T) {
	workspace := t.TempDir()
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	if err := os.MkdirAll(genomeDir, 0o755); err != nil {
		t.Fatalf("mkdir genome dir: %v", err)
	}

	mustWriteGenomeFile(t, filepath.Join(genomeDir, "alpha.md"), strings.TrimSpace(`
# Alpha
- shared rule
* alpha only
This line should be ignored.
`))
	mustWriteGenomeFile(t, filepath.Join(genomeDir, "beta.md"), strings.TrimSpace(`
- shared rule
- beta only
`))
	mustWriteGenomeFile(t, filepath.Join(genomeDir, "epigenetics.md"), strings.TrimSpace(`
# Epigenetic Learnings
- epigenetic only
`))

	seedFitness := GenomicFitness{
		Rules: map[string]int{
			"existing rule": 7,
			"shared rule":   1,
		},
		Plasmids: map[string]int{
			"alpha.md": 2,
			"stale.md": 9,
		},
	}
	seedFitnessBytes, err := json.MarshalIndent(seedFitness, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed fitness: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genomeDir, "fitness.json"), append(seedFitnessBytes, '\n'), 0o644); err != nil {
		t.Fatalf("seed fitness: %v", err)
	}

	if err := RecordGenomicFitness(workspace, true); err != nil {
		t.Fatalf("RecordGenomicFitness returned error: %v", err)
	}

	recorded := readGenomeFitness(t, filepath.Join(genomeDir, "fitness.json"))
	if got := recorded.Rules["existing rule"]; got != 7 {
		t.Fatalf("existing rule score = %d, want 7", got)
	}
	if got := recorded.Rules["shared rule"]; got != 3 {
		t.Fatalf("shared rule score = %d, want 3", got)
	}
	if got := recorded.Rules["alpha only"]; got != 1 {
		t.Fatalf("alpha only score = %d, want 1", got)
	}
	if got := recorded.Rules["beta only"]; got != 1 {
		t.Fatalf("beta only score = %d, want 1", got)
	}
	if got := recorded.Rules["epigenetic only"]; got != 1 {
		t.Fatalf("epigenetic only score = %d, want 1", got)
	}
	if got := recorded.Plasmids["alpha.md"]; got != 3 {
		t.Fatalf("alpha.md score = %d, want 3", got)
	}
	if got := recorded.Plasmids["beta.md"]; got != 1 {
		t.Fatalf("beta.md score = %d, want 1", got)
	}
	if got := recorded.Plasmids["stale.md"]; got != 9 {
		t.Fatalf("stale.md score = %d, want 9", got)
	}
	if _, ok := recorded.Plasmids["epigenetics.md"]; ok {
		t.Fatalf("epigenetics.md should not be tracked as a plasmid: %#v", recorded.Plasmids)
	}
}

func TestGenomeEvolutionPass(t *testing.T) {
	workspace := t.TempDir()
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	if err := os.MkdirAll(genomeDir, 0o755); err != nil {
		t.Fatalf("mkdir genome dir: %v", err)
	}

	mustWriteGenomeFile(t, filepath.Join(genomeDir, "alpha.md"), "- alpha plasmid\n")
	mustWriteGenomeFile(t, filepath.Join(genomeDir, "beta.md"), "- beta plasmid\n")
	mustWriteGenomeFile(t, filepath.Join(genomeDir, "epigenetics.md"), strings.TrimSpace(`
# Epigenetic Learnings
- keep commits small
- remove dead code
* keep commits small
- prefer deterministic tests
`))

	fitness := GenomicFitness{
		Rules: map[string]int{
			"keep commits small":         2,
			"remove dead code":           -3,
			"prefer deterministic tests": 1,
		},
		Plasmids: map[string]int{
			"alpha.md": -6,
			"beta.md":  -4,
		},
	}
	fitnessBytes, err := json.MarshalIndent(fitness, "", "  ")
	if err != nil {
		t.Fatalf("marshal fitness: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genomeDir, "fitness.json"), append(fitnessBytes, '\n'), 0o644); err != nil {
		t.Fatalf("seed fitness: %v", err)
	}

	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_INFERENCE_URL", "http://example.invalid")
	t.Setenv("LOCAL_STANDARD_MODEL", "test-standard-model")
	t.Setenv("LOCAL_CHEAPEST_MODEL", "test-cheapest-model")

	var seenRequest testChatRequest
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected /v1/chat/completions, got %s", r.URL.Path)
		}

		if err := json.NewDecoder(r.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if seenRequest.Model != "test-standard-model" {
			t.Fatalf("expected model test-standard-model, got %q", seenRequest.Model)
		}
		if len(seenRequest.Messages) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(seenRequest.Messages))
		}

		userPrompt := seenRequest.Messages[1].Content
		if strings.Count(userPrompt, "keep commits small") != 1 {
			t.Fatalf("expected deduped keep commits small rule, prompt was:\n%s", userPrompt)
		}
		if strings.Contains(userPrompt, "remove dead code") {
			t.Fatalf("pruned rule should not be sent to the LLM, prompt was:\n%s", userPrompt)
		}
		if !strings.Contains(userPrompt, "prefer deterministic tests") {
			t.Fatalf("expected remaining rule in prompt, got:\n%s", userPrompt)
		}

		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "- distilled rule one\n- distilled rule two\n",
					},
				},
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	t.Setenv("LOCAL_INFERENCE_URL", server.URL+"/v1")

	if err := EvolveGenome(context.Background(), workspace); err != nil {
		t.Fatalf("EvolveGenome returned error: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 LLM call, got %d", got)
	}

	if _, err := os.Stat(filepath.Join(genomeDir, "alpha.md")); !os.IsNotExist(err) {
		t.Fatalf("alpha.md should have been disabled, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(genomeDir, "alpha.md.disabled")); err != nil {
		t.Fatalf("alpha.md.disabled should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(genomeDir, "beta.md")); err != nil {
		t.Fatalf("beta.md should remain active: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(genomeDir, "epigenetics.md"))
	if err != nil {
		t.Fatalf("read evolved epigenetics: %v", err)
	}

	gotGenome := strings.TrimSpace(string(content))
	wantGenome := "- distilled rule one\n- distilled rule two"
	if gotGenome != wantGenome {
		t.Fatalf("evolved genome = %q, want %q", gotGenome, wantGenome)
	}
}

func mustWriteGenomeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readGenomeFitness(t *testing.T, path string) GenomicFitness {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fitness: %v", err)
	}

	var fitness GenomicFitness
	if err := json.Unmarshal(content, &fitness); err != nil {
		t.Fatalf("decode fitness: %v", err)
	}
	if fitness.Rules == nil {
		fitness.Rules = map[string]int{}
	}
	if fitness.Plasmids == nil {
		fitness.Plasmids = map[string]int{}
	}

	return fitness
}
