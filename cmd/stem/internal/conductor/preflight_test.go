package conductor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/roots/llm"
)

func TestCheckLocalInferenceReachableConnectionRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := llm.NewClient(llm.ProviderSpec{
		Provider: "local",
		BaseURL:  "http://" + addr + "/v1",
		Model:    "present-model",
		Mode:     llm.ModeOpenAIish,
	})
	err = checkLocalInferenceReachable(ctx, client)
	if err == nil {
		t.Fatal("expected connection refused error")
	}
	var reachabilityErr *llm.ProviderReachabilityError
	if !errors.As(err, &reachabilityErr) {
		t.Fatalf("error = %q, want typed reachability error", err.Error())
	}
	if reachabilityErr.FailureClass() != llm.ReachabilityFailureConnection {
		t.Fatalf("failure class = %q, want connection failure", reachabilityErr.FailureClass())
	}
	if !strings.Contains(err.Error(), "http://"+addr+"/v1") {
		t.Fatalf("error = %q, want configured endpoint", err.Error())
	}
}

func TestCheckLocalInferenceReachableHealthyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"present-model"}]}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	inferenceURL := strings.TrimSuffix(server.URL, "/") + "/v1"
	client := llm.NewClient(llm.ProviderSpec{
		Provider: "local",
		BaseURL:  inferenceURL,
		Model:    "present-model",
		Mode:     llm.ModeOpenAIish,
	})
	if err := checkLocalInferenceReachable(ctx, client); err != nil {
		t.Fatalf("checkLocalInferenceReachable() error = %v", err)
	}
}

func TestCheckLocalInferenceReachableAcceptsOllamaDefaultTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3.2:latest"}]}`))
	}))
	defer server.Close()

	client := llm.NewClient(llm.ProviderSpec{
		Provider: "local",
		BaseURL:  server.URL + "/v1",
		Model:    "llama3.2",
		Mode:     llm.ModeOpenAIish,
	})
	if err := checkLocalInferenceReachable(context.Background(), client); err != nil {
		t.Fatalf("checkLocalInferenceReachable() error = %v, want Ollama's default-tag alias accepted", err)
	}
}

func TestCheckLocalInferenceReachableMissingModelFailsBeforeUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"different-model"}]}`))
	}))
	defer server.Close()

	client := llm.NewClient(llm.ProviderSpec{
		Provider: "local",
		BaseURL:  server.URL + "/v1",
		Model:    "present-model",
		Mode:     llm.ModeOpenAIish,
	})
	err := checkLocalInferenceReachable(context.Background(), client)
	if err == nil {
		t.Fatal("checkLocalInferenceReachable() error = nil, want model-unavailable failure")
	}
	var reachabilityErr *llm.ProviderReachabilityError
	if !errors.As(err, &reachabilityErr) {
		t.Fatalf("error = %v, want typed reachability error", err)
	}
	if got := reachabilityErr.FailureClass(); got != llm.ReachabilityFailureModelUnavailable {
		t.Fatalf("failure class = %q, want model-unavailable", got)
	}
}

func TestLocalPreflightAndRuntimeUseSameResolvedEndpoint(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"present-model"}]}`))
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := llm.NewClient(llm.ProviderSpec{
		Provider: "local",
		BaseURL:  server.URL + "/v1",
		Model:    "present-model",
		Endpoint: "/chat/completions",
		Mode:     llm.ModeOpenAIish,
	})
	if err := checkLocalInferenceReachable(context.Background(), client); err != nil {
		t.Fatalf("preflight error = %v", err)
	}
	if _, err := client.Call(context.Background(), []llm.Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("runtime call error = %v", err)
	}
	if got, want := strings.Join(paths, ","), "/v1/models,/v1/chat/completions"; got != want {
		t.Fatalf("request paths = %q, want %q", got, want)
	}
}

func TestCheckLocalInferenceReachableDoesNotRewriteExplicitHostAlias(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"present-model"}]}`))
	}))
	defer server.Close()

	client := llm.NewClient(llm.ProviderSpec{
		Provider: "local",
		BaseURL:  server.URL + "/v1",
		Model:    "present-model",
		Mode:     llm.ModeOpenAIish,
	})
	if err := checkLocalInferenceReachable(context.Background(), client); err != nil {
		t.Fatalf("checkLocalInferenceReachable() error = %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("preflight path = %q, want the exact OpenAI-compatible endpoint", gotPath)
	}
}

func TestRunSproutPreflightChecksLocalProviderRequiresOllama(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		t.Skip("docker tests disabled")
	}

	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_INFERENCE_URL", "http://127.0.0.1:1/v1")

	err := runSproutPreflightChecks(context.Background(), llm.NewClient(llm.ResolveProviderSpec()))
	if err == nil {
		t.Fatal("runSproutPreflightChecks() error = nil, want Ollama failure")
	}
	if strings.Contains(err.Error(), "Docker daemon is not responding") {
		t.Skip("docker daemon unavailable in test environment")
	}
	if !strings.Contains(err.Error(), "http://127.0.0.1:1/v1") || !strings.Contains(err.Error(), "connection refused/unreachable") {
		t.Fatalf("error = %q, want safe endpoint reachability guidance", err.Error())
	}
}

func TestRunSproutPreflightChecksRequiresDocker(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		t.Skip("docker tests disabled")
	}

	clearLLMEnv(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "anthropic")
	if err := runSproutPreflightChecks(context.Background(), llm.NewClient(llm.ProviderSpec{Provider: "anthropic"})); err != nil {
		if strings.Contains(err.Error(), "Docker daemon is not responding") {
			t.Skip("docker daemon unavailable in test environment")
		}
		t.Fatalf("runSproutPreflightChecks() error = %v", err)
	}
}
