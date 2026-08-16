package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeAuthenticationReturnsSanitized401(t *testing.T) {
	secret := "sk-super-secret-value-that-must-not-leak"
	var sawPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"User not found Bearer ` + secret + `"}}`))
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider:    "openrouter",
		BaseURL:     server.URL,
		Endpoint:    "/chat/completions",
		Mode:        ModeOpenAIish,
		APIKey:      "dummy-key",
		Model:       "anthropic/claude-sonnet-4.6",
		OutputLimit: DefaultOutputFallback,
	})

	err := client.ProbeAuthentication(context.Background())
	if err == nil {
		t.Fatal("ProbeAuthentication() error = nil, want 401")
	}
	if sawPath != "/chat/completions" {
		t.Fatalf("probe path = %q, want the existing chat endpoint", sawPath)
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != 401 {
		t.Fatalf("ProbeAuthentication() = %v, want typed HTTP 401", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(reqErr.SafeMessage(), secret) {
		t.Fatalf("probe leaked a secret: error=%q safe=%q", err.Error(), reqErr.SafeMessage())
	}
	if !strings.Contains(reqErr.SafeMessage(), "User not found") {
		t.Fatalf("SafeMessage() = %q, want the provider explanation", reqErr.SafeMessage())
	}
}

func TestProbeAuthenticationSucceedsOn200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider:    "openai",
		BaseURL:     server.URL,
		Endpoint:    "/chat/completions",
		Mode:        ModeOpenAIish,
		APIKey:      "key",
		Model:       "gpt-test",
		OutputLimit: DefaultOutputFallback,
	})
	if err := client.ProbeAuthentication(context.Background()); err != nil {
		t.Fatalf("ProbeAuthentication() = %v, want nil", err)
	}
}
