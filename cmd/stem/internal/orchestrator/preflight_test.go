package orchestrator

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHostInferenceHealthURLRewritesDockerHost(t *testing.T) {
	got := hostInferenceHealthURL("http://host.docker.internal:11434/v1")
	want := "http://localhost:11434/api/tags"
	if got != want {
		t.Fatalf("hostInferenceHealthURL() = %q, want %q", got, want)
	}
}

func TestCheckLocalInferenceReachableConnectionRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = checkLocalInferenceReachable(ctx, "http://"+addr+"/v1")
	if err == nil {
		t.Fatal("expected connection refused error")
	}
	if !strings.Contains(err.Error(), "❌ Ollama is not responding at") {
		t.Fatalf("error = %q, want Ollama guidance prefix", err.Error())
	}
	if !strings.Contains(err.Error(), "http://"+addr+"/v1") {
		t.Fatalf("error = %q, want original inference URL preserved", err.Error())
	}
}

func TestCheckLocalInferenceReachableHealthyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	inferenceURL := strings.TrimSuffix(server.URL, "/") + "/v1"
	if err := checkLocalInferenceReachable(ctx, inferenceURL); err != nil {
		t.Fatalf("checkLocalInferenceReachable() error = %v", err)
	}
}

func TestRunSproutPreflightChecksLocalProviderRequiresOllama(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		t.Skip("docker tests disabled")
	}

	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_INFERENCE_URL", "http://127.0.0.1:1/v1")

	err := runSproutPreflightChecks(context.Background())
	if err == nil {
		t.Fatal("runSproutPreflightChecks() error = nil, want Ollama failure")
	}
	if strings.Contains(err.Error(), "Docker daemon is not responding") {
		t.Skip("docker daemon unavailable in test environment")
	}
	if !strings.Contains(err.Error(), "❌ Ollama is not responding at http://127.0.0.1:1/v1") {
		t.Fatalf("error = %q, want Ollama guidance", err.Error())
	}
}

func TestRunSproutPreflightChecksRequiresDocker(t *testing.T) {
	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		t.Skip("docker tests disabled")
	}

	t.Setenv("DEFAULT_LLM_PROVIDER", "anthropic")
	if err := runSproutPreflightChecks(context.Background()); err != nil {
		if strings.Contains(err.Error(), "Docker daemon is not responding") {
			t.Skip("docker daemon unavailable in test environment")
		}
		t.Fatalf("runSproutPreflightChecks() error = %v", err)
	}
}
