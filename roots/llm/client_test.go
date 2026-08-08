package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ptr returns a pointer to a float64 value, used to construct *float64 fields
// in ProviderSpec literals where the zero value means "not set".
func ptr(v float64) *float64 { return &v }

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

// TestListModelsAnthropicEmptyAPIKeySetsNoAuthHeaders proves that an
// Anthropic client with no API key sends neither x-api-key nor
// anthropic-version on the models-list request — matching the pre-adapter
// behavior, which gated both headers behind a single non-empty-key check.
// A caught-in-review regression made anthropicAdapter set both
// unconditionally, including an empty x-api-key header, when the API key
// was empty.
func TestListModelsAnthropicEmptyAPIKeySetsNoAuthHeaders(t *testing.T) {
	var apiKeySet, versionSet bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, apiKeySet = r.Header["X-Api-Key"]
		_, versionSet = r.Header["Anthropic-Version"]
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "claude-test"}}})
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider: "anthropic",
		BaseURL:  server.URL,
		Endpoint: "/v1/messages",
		Mode:     ModeAnthropic,
		APIKey:   "",
	})

	if _, err := client.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if apiKeySet {
		t.Errorf("x-api-key header was set with an empty API key, want absent entirely")
	}
	if versionSet {
		t.Errorf("anthropic-version header was set with an empty API key, want absent entirely")
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
	if spec.Temperature == nil || *spec.Temperature != 0.2 {
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
		Temperature: ptr(0.5),
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
		Temperature: ptr(0.5),
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

func TestCallStreamReturnsErrorOnNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid model parameters"))
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

	if err == nil {
		t.Fatalf("CallStream returned nil error, want 400 error")
	}
	if !strings.Contains(err.Error(), "invalid model parameters") {
		t.Errorf("error = %v, want to contain 'invalid model parameters'", err)
	}
	if !strings.Contains(err.Error(), "llm returned 400") {
		t.Errorf("error = %v, want to contain 'llm returned 400'", err)
	}
	if res != "" {
		t.Errorf("res = %q, want empty", res)
	}

	// Assert token channel is closed
	_, ok := <-tokenChan
	if ok {
		t.Errorf("tokenChan is open, want closed")
	}
}

func TestCallStreamAdvancesCandidateLoopOnNon200(t *testing.T) {
	var server1Called, server2Called bool
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server1Called = true
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("overloaded"))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server2Called = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"id":"1","choices":[{"delta":{"content":"server2"}}]}`,
		}
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server2.Close()

	client := NewClient(ProviderSpec{
		Provider: "openai",
		BaseURL:  server1.URL,
		BaseURLs: []string{server1.URL, server2.URL},
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
	if res != "server2" {
		t.Fatalf("res = %q, want 'server2'", res)
	}

	if !server1Called {
		t.Errorf("server1 was not called")
	}
	if !server2Called {
		t.Errorf("server2 was not called")
	}
}

// Failover exists for an address that is down. A refusal is the endpoint
// answering, and the candidates are one endpoint reached several ways, so
// offering the same definitions to the next address only buys the same refusal
// a second time — and delays the downgrade the caller is waiting to hear about.
func TestCallWithToolsDoesNotAdvanceCandidateLoopOnToolRefusal(t *testing.T) {
	var server1Calls, server2Calls int
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server1Calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("tools not supported"))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server2Calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"server2"}}]}`))
	}))
	defer server2.Close()

	client := NewClient(ProviderSpec{
		Provider: "openai",
		BaseURL:  server1.URL,
		BaseURLs: []string{server1.URL, server2.URL},
		Endpoint: "/chat/completions",
		Mode:     ModeOpenAIish,
		APIKey:   "key",
		Model:    "gpt-test",
	})

	tools := []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "readFile"}}}
	_, err := client.CallWithTools(context.Background(), []Message{{Role: "user", Content: "hi"}}, tools, nil)
	if !errors.Is(err, ErrRejectedWithTools) {
		t.Fatalf("error = %v, want it to satisfy errors.Is(err, ErrRejectedWithTools)", err)
	}
	if server1Calls != 1 {
		t.Errorf("server1 calls = %d, want 1", server1Calls)
	}
	if server2Calls != 0 {
		t.Errorf("server2 calls = %d, want 0 — a refusal must not spend the next candidate", server2Calls)
	}
}

// The status check guards both branches from one place, so the branch that no
// longer carries its own check needs its own assertion. Without this, narrowing
// the check to the streaming path leaves the whole package green and a
// non-streaming refusal surfaces as a JSON decode complaint about the refusal
// text rather than as the provider's own explanation.
func TestCallReturnsErrorOnNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("incorrect api key provided"))
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

	res, err := client.Call(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatalf("Call returned nil error, want 401 error")
	}
	if !strings.Contains(err.Error(), "llm returned 401") {
		t.Errorf("error = %v, want to contain 'llm returned 401'", err)
	}
	if !strings.Contains(err.Error(), "incorrect api key provided") {
		t.Errorf("error = %v, want to contain the provider's explanation", err)
	}
	if res != "" {
		t.Errorf("res = %q, want empty", res)
	}
}

func TestByteIdentity(t *testing.T) {
	adapters := []struct {
		name    string
		adapter providerAdapter
		spec    ProviderSpec
	}{
		{"anthropic", anthropicAdapter{}, ProviderSpec{Model: "claude-3", Temperature: ptr(0.5)}},
		{"openai", openAIishAdapter{}, ProviderSpec{Model: "gpt-4", Temperature: ptr(0.5)}},
	}

	scenarios := []struct {
		name     string
		messages []Message
		stream   bool
	}{
		{"system-user-assistant", []Message{
			{Role: "system", Content: "system instruction"},
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
			{Role: "user", Content: "how are you?"},
		}, false},
		{"long-caching", []Message{
			{Role: "system", Content: "system instruction"},
			{Role: "user", Content: "A long message to trigger caching: " + strings.Repeat("X", 1005)},
		}, false},
		{"repomap-caching", []Message{
			{Role: "system", Content: "system instruction"},
			{Role: "user", Content: "Please review repomap.md"},
		}, false},
		{"streaming", []Message{
			{Role: "user", Content: "stream this"},
		}, true},
	}

	for _, ad := range adapters {
		for _, sc := range scenarios {
			t.Run(ad.name+"-"+sc.name, func(t *testing.T) {
				actual, err := ad.adapter.BuildChatRequest(ad.spec, sc.messages, nil, sc.stream)
				if err != nil {
					t.Fatalf("BuildChatRequest failed: %v", err)
				}

				fixturePath := filepath.Join("testdata", ad.name+"-"+sc.name+".json")
				expected, err := os.ReadFile(fixturePath)
				if err != nil {
					t.Fatalf("Failed to read fixture %s: %v", fixturePath, err)
				}

				if string(actual) != string(expected) {
					t.Errorf("Marshalled bytes differ from fixture.\nExpected: %s\nActual:   %s", string(expected), string(actual))
				}
			})
		}
	}
}

func TestMessageOmitsToolFields(t *testing.T) {
	msg := Message{Role: "user", Content: "hello"}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal Message: %v", err)
	}
	expected := `{"role":"user","content":"hello"}`
	if string(b) != expected {
		t.Errorf("Message serialization failed. Expected %s, got %s", expected, string(b))
	}
}

// Both adapters accept tool definitions and, for now, ignore them: emitting
// them onto the wire belongs to the per-family slices that follow. So this
// asserts acceptance only, and is named for that. The claim it does NOT make —
// that today's text still comes back through the new return types — is pinned
// by TestCallSendsOpenAIShapedRequestWithoutCaching,
// TestCallSendsAnthropicShapedRequestWithCaching and
// TestAnthropicPromptCachingPayload, each of which goes red when a parser drops
// its text into the new Result.
func TestAdaptersAcceptToolDefinitionsWithoutEmittingThem(t *testing.T) {
	adapters := []struct {
		name    string
		adapter providerAdapter
		spec    ProviderSpec
	}{
		{"anthropic", anthropicAdapter{}, ProviderSpec{Model: "claude-3", Temperature: ptr(0.5)}},
		{"openai", openAIishAdapter{}, ProviderSpec{Model: "gpt-4", Temperature: ptr(0.5)}},
	}

	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolFunction{
			Name:        "get_weather",
			Description: "Gets the weather",
			Parameters:  map[string]any{"type": "object"},
		},
	}}

	for _, ad := range adapters {
		t.Run(ad.name, func(t *testing.T) {
			_, err := ad.adapter.BuildChatRequest(ad.spec, []Message{{Role: "user", Content: "hi"}}, tools, false)
			if err != nil {
				t.Fatalf("BuildChatRequest with tools failed: %v", err)
			}
		})
	}
}

// The endpoint capability is operator-declared, so the YAML key is the whole
// interface. Nothing asserted that it reached the spec, which meant an operator
// could set it correctly and be driven natively anyway.
func TestAcceptsToolDefinitionsReachesProviderSpec(t *testing.T) {
	t.Run("declared false", func(t *testing.T) {
		clearProviderKeys(t)
		chdirWithTendrilConfig(t, `
llm:
  default-provider: local
  providers:
    local:
      base-url: http://localhost:11434/v1
      model: llama3.2
      accepts-tool-definitions: false
`)
		t.Setenv("DEFAULT_LLM_PROVIDER", "local")

		spec := ResolveTierProviderSpec(TierPremium)
		if spec.AcceptsToolDefinitions == nil {
			t.Fatal("AcceptsToolDefinitions = nil, want an explicit false")
		}
		if *spec.AcceptsToolDefinitions {
			t.Fatal("AcceptsToolDefinitions = true, want false")
		}
		if NewClient(spec).ToolDefinitionsCapable() {
			t.Error("ToolDefinitionsCapable() = true for an endpoint declared incapable")
		}
	})

	t.Run("unset attempts native", func(t *testing.T) {
		clearProviderKeys(t)
		chdirWithTendrilConfig(t, `
llm:
  default-provider: local
  providers:
    local:
      base-url: http://localhost:11434/v1
      model: llama3.2
`)
		t.Setenv("DEFAULT_LLM_PROVIDER", "local")

		spec := ResolveTierProviderSpec(TierPremium)
		if spec.AcceptsToolDefinitions != nil {
			t.Fatalf("AcceptsToolDefinitions = %v, want nil when the key is absent", *spec.AcceptsToolDefinitions)
		}
		if !NewClient(spec).ToolDefinitionsCapable() {
			t.Error("ToolDefinitionsCapable() = false with no declaration; unset must attempt native carriage")
		}
	})
}
