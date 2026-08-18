package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pointAt(t *testing.T, url string) {
	t.Helper()
	host := strings.TrimPrefix(url, "http://")
	parts := strings.Split(host, ":")
	if len(parts) != 2 {
		t.Fatalf("unexpected server URL %q", url)
	}
	t.Setenv("TERROIR_HOST", parts[0])
	t.Setenv("PORT", parts[1])
}

func TestProbeOwner_PublishesOwner(t *testing.T) {
	owner := 42
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("probed %s, want /health", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Owner *int `json:"owner,omitempty"`
		}{Owner: &owner})
	}))
	t.Cleanup(server.Close)
	pointAt(t, server.URL)

	probe := ProbeOwner(context.Background())
	if !probe.Reached {
		t.Fatal("expected probe to reach the Stem")
	}
	if probe.Owner == nil || *probe.Owner != owner {
		t.Fatalf("owner = %v, want %d", probe.Owner, owner)
	}
	if !strings.Contains(probe.Address, partsHost(server.URL)) {
		t.Fatalf("address %q does not name the probed host", probe.Address)
	}
}

func TestProbeOwner_NoOwnerPublished(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(struct {
			Owner *int `json:"owner,omitempty"`
		}{})
	}))
	t.Cleanup(server.Close)
	pointAt(t, server.URL)

	probe := ProbeOwner(context.Background())
	if !probe.Reached {
		t.Fatal("expected a decoded health report")
	}
	if probe.Owner != nil {
		t.Fatalf("expected no owner, got %d", *probe.Owner)
	}
}

func TestProbeOwner_NothingAnswers(t *testing.T) {
	t.Setenv("TERROIR_HOST", "127.0.0.1")
	t.Setenv("PORT", "65534")

	probe := ProbeOwner(context.Background())
	if probe.Reached {
		t.Fatal("expected an unreachable address not to be reached")
	}
	if probe.Owner != nil {
		t.Fatalf("expected no owner, got %d", *probe.Owner)
	}
}

func TestProbeOwner_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// dwell: keep the handler open beyond ProbeOwner's 2-second timeout
		time.Sleep(3 * time.Second)
	}))
	t.Cleanup(server.Close)
	pointAt(t, server.URL)

	start := time.Now()
	probe := ProbeOwner(context.Background())
	elapsed := time.Since(start)
	if probe.Reached {
		t.Fatal("expected timeout to leave Reached false")
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("probe delayed beyond bound, took %v", elapsed)
	}
}

func partsHost(url string) string {
	host := strings.TrimPrefix(url, "http://")
	if i := strings.Index(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}
