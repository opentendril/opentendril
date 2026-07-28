package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCoordinatorProviderSpecUsesDefaultProviderFallback(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("OPENAI_MODEL_NAME", "gpt-worker")
	t.Setenv("OPENAI_API_KEY", "worker-key")
	t.Setenv("COORDINATOR_LLM_PROVIDER", "")
	t.Setenv("COORDINATOR_MODEL_NAME", "gpt-coordinator")
	t.Setenv("COORDINATOR_LOCAL_INFERENCE_URL", "")

	spec := ResolveCoordinatorProviderSpec()

	if spec.Provider != "openai" {
		t.Fatalf("spec.Provider = %q, want %q", spec.Provider, "openai")
	}
	if spec.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("spec.BaseURL = %q, want %q", spec.BaseURL, "https://api.openai.com/v1")
	}
	if spec.Model != "gpt-coordinator" {
		t.Fatalf("spec.Model = %q, want %q", spec.Model, "gpt-coordinator")
	}
	if spec.APIKey != "worker-key" {
		t.Fatalf("spec.APIKey = %q, want %q", spec.APIKey, "worker-key")
	}
	if spec.Endpoint != "/chat/completions" {
		t.Fatalf("spec.Endpoint = %q, want %q", spec.Endpoint, "/chat/completions")
	}
	if spec.Mode != ModeOpenAIish {
		t.Fatalf("spec.Mode = %q, want %q", spec.Mode, ModeOpenAIish)
	}
}

func TestResolveCoordinatorProviderSpecUsesExplicitCoordinatorLocalSettings(t *testing.T) {
	t.Setenv("DEFAULT_LLM_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "worker-key")
	t.Setenv("COORDINATOR_LLM_PROVIDER", "local")
	t.Setenv("COORDINATOR_MODEL_NAME", "qwen2.5:1.5b-instruct")
	t.Setenv("COORDINATOR_LOCAL_INFERENCE_URL", "http://coordinator:11434/v1")

	spec := ResolveCoordinatorProviderSpec()

	if spec.Provider != "local" {
		t.Fatalf("spec.Provider = %q, want %q", spec.Provider, "local")
	}
	if spec.BaseURL != "http://coordinator:11434/v1" {
		t.Fatalf("spec.BaseURL = %q, want %q", spec.BaseURL, "http://coordinator:11434/v1")
	}
	if len(spec.BaseURLs) == 0 || spec.BaseURLs[0] != "http://coordinator:11434/v1" {
		t.Fatalf("spec.BaseURLs = %#v, want to start with coordinator URL", spec.BaseURLs)
	}
	if spec.Model != "qwen2.5:1.5b-instruct" {
		t.Fatalf("spec.Model = %q, want %q", spec.Model, "qwen2.5:1.5b-instruct")
	}
	if spec.Endpoint != "/chat/completions" {
		t.Fatalf("spec.Endpoint = %q, want %q", spec.Endpoint, "/chat/completions")
	}
	if spec.Mode != ModeOpenAIish {
		t.Fatalf("spec.Mode = %q, want %q", spec.Mode, ModeOpenAIish)
	}
}

func TestListModelsUsesOpenAICompatibleEndpointWithoutAPIKey(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s, want /v1/models", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "llama3.2"},
				{"id": "qwen2.5-coder:7b"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider: "local",
		BaseURL:  server.URL + "/v1",
		Endpoint: "/chat/completions",
		Mode:     ModeOpenAIish,
	})

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if authHeader != "" {
		t.Fatalf("Authorization header = %q, want empty", authHeader)
	}
	if len(models) != 2 || models[0] != "llama3.2" || models[1] != "qwen2.5-coder:7b" {
		t.Fatalf("models = %#v, want llama3.2 and qwen2.5-coder:7b", models)
	}
}

// TestListModelsUsesAnthropicVersionedEndpoint proves discovery reaches the
// Anthropic Models API at /v1/models with the x-api-key + anthropic-version
// headers. The base URL carries no version segment (unlike the OpenAI-shaped
// providers), so before the mode split this request hit /models — a 404 — and
// Anthropic always fell back to the static registry.
func TestListModelsUsesAnthropicVersionedEndpoint(t *testing.T) {
	var (
		path             string
		apiKeyHeader     string
		versionHeader    string
		authBearerHeader string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		path = r.URL.Path
		apiKeyHeader = r.Header.Get("x-api-key")
		versionHeader = r.Header.Get("anthropic-version")
		authBearerHeader = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-opus-4-8"},
				{"id": "claude-sonnet-5"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider: "anthropic",
		BaseURL:  server.URL, // no /v1 segment, matching the real Anthropic base URL
		Endpoint: "/v1/messages",
		Mode:     ModeAnthropic,
		APIKey:   "anthropic-key",
	})

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if path != "/v1/models" {
		t.Fatalf("path = %s, want /v1/models", path)
	}
	if apiKeyHeader != "anthropic-key" {
		t.Fatalf("x-api-key = %q, want anthropic-key", apiKeyHeader)
	}
	if versionHeader != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", versionHeader)
	}
	if authBearerHeader != "" {
		t.Fatalf("Authorization = %q, want empty (Anthropic rejects Bearer auth)", authBearerHeader)
	}
	if len(models) != 2 || models[0] != "claude-opus-4-8" || models[1] != "claude-sonnet-5" {
		t.Fatalf("models = %#v, want claude-opus-4-8 and claude-sonnet-5", models)
	}
}

func TestResolveLocalProviderSpecUsesTendrilConfig(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "")
	t.Setenv("LOCAL_INFERENCE_URL", "")
	t.Setenv("LOCAL_MODEL_NAME", "")
	t.Setenv("DEFAULT_MODEL_NAME", "")

	root := t.TempDir()
	tendrilDir := filepath.Join(root, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir .tendril: %v", err)
	}
	config := []byte(`
llm:
  default-provider: local
  providers:
    local:
      base-url: http://localhost:11434/v1
      model: qwen2.5-coder:7b
      temperature: 0.2
`)
	if err := os.WriteFile(filepath.Join(tendrilDir, "config.yaml"), config, 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousDir)
	})

	spec := ResolveProviderSpec()

	if spec.Provider != "local" {
		t.Fatalf("spec.Provider = %q, want local", spec.Provider)
	}
	if spec.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("spec.BaseURL = %q, want configured URL", spec.BaseURL)
	}
	if spec.Model != "qwen2.5-coder:7b" {
		t.Fatalf("spec.Model = %q, want configured model", spec.Model)
	}
	if spec.Temperature != 0.2 {
		t.Fatalf("spec.Temperature = %v, want 0.2", spec.Temperature)
	}
}

func TestCallSendsAnthropicShapedRequestWithCaching(t *testing.T) {
	var (
		bodyData          []byte
		apiKeyHeader      string
		versionHeader     string
		betaHeader        string
		contentTypeHeader string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		apiKeyHeader = r.Header.Get("x-api-key")
		versionHeader = r.Header.Get("anthropic-version")
		betaHeader = r.Header.Get("anthropic-beta")
		contentTypeHeader = r.Header.Get("Content-Type")
		var err error
		bodyData, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "mock response text"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider:    "anthropic",
		BaseURL:     server.URL,
		Endpoint:    "/v1/messages",
		Mode:        ModeAnthropic,
		APIKey:      "test-key",
		Model:       "claude-test",
		Temperature: 0.5,
	})

	messages := []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "user prompt that is very very long " + strings.Repeat("a", 1000)},
	}

	res, err := client.Call(context.Background(), messages)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if res != "mock response text" {
		t.Fatalf("response = %q, want %q", res, "mock response text")
	}

	if apiKeyHeader != "test-key" {
		t.Fatalf("x-api-key = %q, want test-key", apiKeyHeader)
	}
	if versionHeader != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", versionHeader)
	}
	if betaHeader != "prompt-caching-2024-07-31" {
		t.Fatalf("anthropic-beta = %q, want prompt-caching-2024-07-31", betaHeader)
	}
	if contentTypeHeader != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentTypeHeader)
	}

	var parsedBody map[string]any
	if err := json.Unmarshal(bodyData, &parsedBody); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	if parsedBody["model"] != "claude-test" {
		t.Fatalf("model = %v, want claude-test", parsedBody["model"])
	}

	systemObj, ok := parsedBody["system"].([]any)
	if !ok || len(systemObj) == 0 {
		t.Fatalf("system is missing or empty")
	}
	sysBlock := systemObj[0].(map[string]any)
	if sysBlock["text"] != "system prompt" {
		t.Fatalf("system text = %v", sysBlock["text"])
	}

	messagesArr, ok := parsedBody["messages"].([]any)
	if !ok || len(messagesArr) == 0 {
		t.Fatalf("messages is missing or empty")
	}
	msg1 := messagesArr[0].(map[string]any)
	if msg1["role"] != "user" {
		t.Fatalf("message 1 role = %v", msg1["role"])
	}
	contentArr := msg1["content"].([]any)
	contentBlock := contentArr[0].(map[string]any)
	if _, hasCache := contentBlock["cache_control"]; !hasCache {
		t.Fatalf("message 1 is missing cache_control block")
	}
}

func TestCallSendsOpenAIShapedRequestWithoutCaching(t *testing.T) {
	var (
		bodyData          []byte
		authHeader        string
		betaHeader        string
		contentTypeHeader string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		authHeader = r.Header.Get("Authorization")
		betaHeader = r.Header.Get("anthropic-beta")
		contentTypeHeader = r.Header.Get("Content-Type")
		var err error
		bodyData, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "mock openai response"}},
			},
		})
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider:    "openai",
		BaseURL:     server.URL,
		Endpoint:    "/chat/completions",
		Mode:        ModeOpenAIish,
		APIKey:      "test-key-openai",
		Model:       "gpt-test",
		Temperature: 0.5,
	})

	messages := []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "user prompt " + strings.Repeat("a", 1000)},
	}

	res, err := client.Call(context.Background(), messages)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if res != "mock openai response" {
		t.Fatalf("response = %q, want %q", res, "mock openai response")
	}

	if authHeader != "Bearer test-key-openai" {
		t.Fatalf("Authorization = %q, want Bearer test-key-openai", authHeader)
	}
	if betaHeader != "" {
		t.Fatalf("anthropic-beta = %q, want empty", betaHeader)
	}
	if contentTypeHeader != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentTypeHeader)
	}

	var parsedBody map[string]any
	if err := json.Unmarshal(bodyData, &parsedBody); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	if parsedBody["model"] != "gpt-test" {
		t.Fatalf("model = %v, want gpt-test", parsedBody["model"])
	}

	if _, ok := parsedBody["system"]; ok {
		t.Fatalf("expected no top-level system field")
	}

	messagesArr, ok := parsedBody["messages"].([]any)
	if !ok || len(messagesArr) != 2 {
		t.Fatalf("messages length = %d, want 2", len(messagesArr))
	}
	sysMsg := messagesArr[0].(map[string]any)
	if sysMsg["role"] != "system" || sysMsg["content"] != "system prompt" {
		t.Fatalf("unexpected system message inside messages array: %v", sysMsg)
	}
}

func TestCallStreamParsesAnthropicChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []string{
			`{"type":"message_start","message":{}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		}
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider: "anthropic",
		BaseURL:  server.URL,
		Endpoint: "/v1/messages",
		Mode:     ModeAnthropic,
		APIKey:   "key",
		Model:    "claude-test",
	})

	tokenChan := make(chan string, 10)
	res, err := client.CallStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, tokenChan)
	if err != nil {
		t.Fatalf("CallStream failed: %v", err)
	}
	if res != "hello world" {
		t.Fatalf("response = %q, want 'hello world'", res)
	}

	var received []string
	for tItem := range tokenChan {
		received = append(received, tItem)
	}
	if len(received) != 2 || received[0] != "hello" || received[1] != " world" {
		t.Fatalf("tokens = %v, want ['hello', ' world']", received)
	}
}

func TestCallStreamParsesOpenAIChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []string{
			`{"id":"1","choices":[{"delta":{"role":"assistant"}}]}`,
			`{"id":"1","choices":[{"delta":{"content":"hello"}}]}`,
			`{"id":"1","choices":[{"delta":{"content":" world"}}]}`,
			`{"id":"1","choices":[{"delta":{}}]}`,
		}
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider: "openai",
		BaseURL:  server.URL,
		Endpoint: "/chat/completions",
		Mode:     ModeOpenAIish,
		APIKey:   "key",
		Model:    "gpt-test",
	})

	tokenChan := make(chan string, 10)
	res, err := client.CallStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, tokenChan)
	if err != nil {
		t.Fatalf("CallStream failed: %v", err)
	}
	if res != "hello world" {
		t.Fatalf("response = %q, want 'hello world'", res)
	}

	var received []string
	for tItem := range tokenChan {
		received = append(received, tItem)
	}
	if len(received) != 2 || received[0] != "hello" || received[1] != " world" {
		t.Fatalf("tokens = %v, want ['hello', ' world']", received)
	}
}
