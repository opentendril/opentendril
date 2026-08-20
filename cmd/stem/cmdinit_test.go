package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeOllamaURLEmptyModelsIsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	t.Cleanup(server.Close)

	got := probeOllamaURL(server.Client(), server.URL+"/api/tags")
	if !got.reachable {
		t.Fatal("reachable = false, want true for a running instance with zero models")
	}
	if len(got.models) != 0 {
		t.Fatalf("models = %#v, want empty", got.models)
	}
}

func TestProbeOllamaURLReportsModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.2"},{"name":"qwen2.5-coder:7b"}]}`))
	}))
	t.Cleanup(server.Close)

	got := probeOllamaURL(server.Client(), server.URL+"/api/tags")
	if !got.reachable {
		t.Fatal("reachable = false, want true")
	}
	if len(got.models) != 2 || got.models[0] != "llama3.2" || got.models[1] != "qwen2.5-coder:7b" {
		t.Fatalf("models = %#v", got.models)
	}
}

func TestProbeOllamaURLUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	got := probeOllamaURL(server.Client(), server.URL+"/api/tags")
	if got.reachable {
		t.Fatal("reachable = true, want false on non-200")
	}

	got = probeOllamaURL(server.Client(), "http://127.0.0.1:1/api/tags")
	if got.reachable {
		t.Fatal("reachable = true, want false when the instance does not answer")
	}
}

func TestAcceptLocalOllamaDefaultsYes(t *testing.T) {
	for _, ans := range []string{"", "y", "Y", "yes", " llama "} {
		if !acceptLocalOllama(ans) {
			t.Errorf("acceptLocalOllama(%q) = false, want true", ans)
		}
	}
	for _, ans := range []string{"n", "N", "no", "NO"} {
		if acceptLocalOllama(ans) {
			t.Errorf("acceptLocalOllama(%q) = true, want false", ans)
		}
	}
}

func TestSelectOllamaModelEmpty(t *testing.T) {
	if got := selectOllamaModel(nil); got != "" {
		t.Fatalf("selectOllamaModel(nil) = %q, want empty", got)
	}
	if got := selectOllamaModel([]string{}); got != "" {
		t.Fatalf("selectOllamaModel(empty) = %q, want empty", got)
	}
}
