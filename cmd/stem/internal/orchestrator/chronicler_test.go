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

type testChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Temperature float64 `json:"temperature"`
	Stream      bool    `json:"stream"`
}

func TestTranscribeLearningsWritesGenomeFile(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_MODEL_NAME", "test-model")

	var seenRequest testChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected /v1/chat/completions, got %s", r.URL.Path)
		}

		if err := json.NewDecoder(r.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if seenRequest.Model != "test-model" {
			t.Fatalf("expected model test-model, got %q", seenRequest.Model)
		}
		if len(seenRequest.Messages) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(seenRequest.Messages))
		}
		if seenRequest.Messages[0].Role != "system" {
			t.Fatalf("expected system role first, got %q", seenRequest.Messages[0].Role)
		}

		userPrompt := seenRequest.Messages[1].Content
		if !strings.Contains(userPrompt, "task transcript") {
			t.Fatalf("user prompt missing transcript: %s", userPrompt)
		}
		if !strings.Contains(userPrompt, "diff --git a/app.go b/app.go") {
			t.Fatalf("user prompt missing diff: %s", userPrompt)
		}
		if !strings.Contains(userPrompt, "run logs") {
			t.Fatalf("user prompt missing logs: %s", userPrompt)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "- Architectural gotcha: keep genome writes on the host.\n- Dependency requirement: prefer OpenAI-compatible chat endpoints.\n",
					},
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("LOCAL_INFERENCE_URL", server.URL+"/v1")

	workspace := t.TempDir()
	chronicler := NewEpigeneticChronicler(workspace)

	err := chronicler.TranscribeLearnings(
		context.Background(),
		"task transcript",
		"diff --git a/app.go b/app.go\n+fmt.Println(\"hi\")\n",
		"run logs",
	)
	if err != nil {
		t.Fatalf("TranscribeLearnings returned error: %v", err)
	}

	genomePath := filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")
	content, err := os.ReadFile(genomePath)
	if err != nil {
		t.Fatalf("read genome: %v", err)
	}

	body := string(content)
	if !strings.HasPrefix(body, epigeneticGenomeHeader) {
		t.Fatalf("genome missing header: %s", body)
	}
	if !strings.Contains(body, "- Architectural gotcha: keep genome writes on the host.") {
		t.Fatalf("genome missing first bullet: %s", body)
	}
	if !strings.Contains(body, "- Dependency requirement: prefer OpenAI-compatible chat endpoints.") {
		t.Fatalf("genome missing second bullet: %s", body)
	}
}

func TestTranscribeLearningsAppendsToExistingGenome(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_MODEL_NAME", "test-model")

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "- Naming rule: prefer kebab-case for genome entries.",
					},
				},
			},
		})
	}))
	defer server.Close()

	t.Setenv("LOCAL_INFERENCE_URL", server.URL+"/v1")

	workspace := t.TempDir()
	genomePath := filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")
	if err := os.MkdirAll(filepath.Dir(genomePath), 0o755); err != nil {
		t.Fatalf("mkdir genome dir: %v", err)
	}
	if err := os.WriteFile(genomePath, []byte(epigeneticGenomeHeader+"\n\n- Existing learning: keep responses brief.\n"), 0o644); err != nil {
		t.Fatalf("seed genome: %v", err)
	}

	chronicler := NewEpigeneticChronicler(workspace)
	err := chronicler.TranscribeLearnings(
		context.Background(),
		"transcript",
		"diff --git a/pkg/main.go b/pkg/main.go\n+fmt.Println(\"ok\")\n",
		"logs",
	)
	if err != nil {
		t.Fatalf("TranscribeLearnings returned error: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 LLM call, got %d", got)
	}

	content, err := os.ReadFile(genomePath)
	if err != nil {
		t.Fatalf("read genome: %v", err)
	}

	body := string(content)
	if strings.Count(body, epigeneticGenomeHeader) != 1 {
		t.Fatalf("expected single header, got: %s", body)
	}
	if !strings.Contains(body, "- Existing learning: keep responses brief.") {
		t.Fatalf("existing bullet missing: %s", body)
	}
	if !strings.Contains(body, "- Naming rule: prefer kebab-case for genome entries.") {
		t.Fatalf("appended bullet missing: %s", body)
	}
	if strings.Index(body, "- Existing learning: keep responses brief.") > strings.Index(body, "- Naming rule: prefer kebab-case for genome entries.") {
		t.Fatalf("existing bullet should appear before appended bullet: %s", body)
	}
}

func TestTranscribeLearningsSkipsEmptyDiff(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_MODEL_NAME", "test-model")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LLM should not be called when diff is empty")
	}))
	defer server.Close()

	t.Setenv("LOCAL_INFERENCE_URL", server.URL+"/v1")

	workspace := t.TempDir()
	chronicler := NewEpigeneticChronicler(workspace)
	if err := chronicler.TranscribeLearnings(context.Background(), "transcript", "   ", "logs"); err != nil {
		t.Fatalf("expected nil error for empty diff, got %v", err)
	}

	genomePath := filepath.Join(workspace, ".tendril", "genome", "epigenetics.md")
	if _, err := os.Stat(genomePath); !os.IsNotExist(err) {
		t.Fatalf("genome should not be created for empty diff, stat err=%v", err)
	}
}
