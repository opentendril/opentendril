package conductor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// TestEgressPolicyDenyAllByDefault: with no allow-list (no grant, or a grant
// without egress hosts) every URL is denied — the zero-configuration secure
// default.
func TestEgressPolicyDenyAllByDefault(t *testing.T) {
	for _, policy := range []EgressPolicy{{}, NewEgressPolicy(nil), NewEgressPolicy([]string{" ", ""})} {
		if err := policy.Authorize("https://proxy.golang.org/anything"); err == nil {
			t.Fatal("empty egress policy authorized a host; deny-all must be the default")
		}
	}
}

func TestEgressPolicyExactHostMatching(t *testing.T) {
	policy := NewEgressPolicy([]string{"proxy.golang.org", "127.0.0.1:8080"})

	if err := policy.Authorize("https://proxy.golang.org/mod/x"); err != nil {
		t.Fatalf("allow-listed host denied: %v", err)
	}
	if err := policy.Authorize("http://127.0.0.1:8080/artifact"); err != nil {
		t.Fatalf("allow-listed host:port denied: %v", err)
	}
	if err := policy.Authorize("https://evil.example.com/"); err == nil {
		t.Fatal("non-allow-listed host authorized")
	}
	if err := policy.Authorize("https://sub.proxy.golang.org/"); err == nil {
		t.Fatal("subdomain authorized; matching must be exact (no wildcards)")
	}
	if err := policy.Authorize("ftp://proxy.golang.org/"); err == nil {
		t.Fatal("non-http scheme authorized")
	}
	if err := policy.Authorize("://not-a-url"); err == nil {
		t.Fatal("unparseable URL authorized")
	}
}

// TestFetchEgressPayloadsDenyAllBlocksHost proves deny-all egress at the
// mediation seam with a live listener: with an empty allow-list the Stem
// never contacts the server at all.
func TestFetchEgressPayloadsDenyAllBlocksHost(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()

	_, err := fetchEgressPayloads(context.Background(), NewEgressPolicy(nil), []PassthroughFetch{
		{URL: server.URL + "/artifact", Path: "artifact.bin"},
	})
	if err == nil || !strings.Contains(err.Error(), "egress denied") {
		t.Fatalf("deny-all fetch error = %v, want an egress denial", err)
	}
	if hits.Load() != 0 {
		t.Fatal("a denied fetch still reached the server")
	}
}

// TestFetchEgressPayloadsAllowListOpensHost proves the matching grant's
// allow-list opens exactly the listed host: the Stem performs the fetch and
// delivers the payload read-only under the terrarium egress directory.
func TestFetchEgressPayloadsAllowListOpensHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mediated content"))
	}))
	defer server.Close()

	serverHost := hostOf(t, server.URL)
	payloads, err := fetchEgressPayloads(context.Background(), NewEgressPolicy([]string{serverHost}), []PassthroughFetch{
		{URL: server.URL + "/artifact", Path: "inputs/artifact.txt"},
	})
	if err != nil {
		t.Fatalf("allow-listed fetch failed: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(payloads))
	}
	if string(payloads[0].Content) != "mediated content" {
		t.Fatalf("payload content = %q", payloads[0].Content)
	}
	if payloads[0].Path != passthroughEgressDirectory+"/inputs/artifact.txt" {
		t.Fatalf("payload path = %q, want it anchored under %s", payloads[0].Path, passthroughEgressDirectory)
	}
	if payloads[0].Mode != 0o444 {
		t.Fatalf("payload mode = %o, want read-only 444", payloads[0].Mode)
	}

	// The same policy still blocks any other host: opening one listed host
	// opens exactly that host.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer other.Close()
	if _, err := fetchEgressPayloads(context.Background(), NewEgressPolicy([]string{serverHost}), []PassthroughFetch{
		{URL: other.URL + "/artifact", Path: "artifact.bin"},
	}); err == nil {
		t.Fatal("a host outside the allow-list was fetched")
	}
}

func TestPassthroughEgressPathRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "  ", "/etc/passwd", "../escape", "a/../../escape", ".."} {
		if _, err := passthroughEgressPath(bad); err == nil {
			t.Fatalf("destination %q accepted; it must stay under %s", bad, passthroughEgressDirectory)
		}
	}
	got, err := passthroughEgressPath("nested/./file.txt")
	if err != nil {
		t.Fatalf("clean relative destination rejected: %v", err)
	}
	if got != passthroughEgressDirectory+"/nested/file.txt" {
		t.Fatalf("destination = %q", got)
	}
}

// TestRunPassthroughDeniesEgressBeforeAnyTerrarium asserts mediation order:
// an egress denial aborts the execution before the Terrarium seam is ever
// reached, so no container (and no other side effect) exists for a denied
// delegated invocation.
func TestRunPassthroughDeniesEgressBeforeAnyTerrarium(t *testing.T) {
	originalRun := runPassthroughCommandFn
	terrariumReached := false
	runPassthroughCommandFn = func(ctx context.Context, execution PassthroughExecution, payloads []terrarium.FilePayload, timeout time.Duration) (PassthroughResult, error) {
		terrariumReached = true
		return PassthroughResult{}, nil
	}
	defer func() { runPassthroughCommandFn = originalRun }()

	_, err := RunPassthrough(context.Background(), PassthroughExecution{
		Workspace: t.TempDir(),
		Command:   []string{"true"},
		Fetches:   []PassthroughFetch{{URL: "https://evil.example.com/x", Path: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "egress denied") {
		t.Fatalf("error = %v, want an egress denial", err)
	}
	if terrariumReached {
		t.Fatal("a denied execution still reached the Terrarium seam")
	}
}

// TestRunPassthroughThreadsPayloadsAndTimeout covers the authorized path up
// to the Terrarium seam: mediated payloads and the execution bound arrive at
// the container runner intact.
func TestRunPassthroughThreadsPayloadsAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()

	originalRun := runPassthroughCommandFn
	var gotPayloads []terrarium.FilePayload
	var gotTimeout time.Duration
	runPassthroughCommandFn = func(ctx context.Context, execution PassthroughExecution, payloads []terrarium.FilePayload, timeout time.Duration) (PassthroughResult, error) {
		gotPayloads = payloads
		gotTimeout = timeout
		return PassthroughResult{ExitCode: 0}, nil
	}
	defer func() { runPassthroughCommandFn = originalRun }()

	_, err := RunPassthrough(context.Background(), PassthroughExecution{
		Workspace: t.TempDir(),
		Command:   []string{"true"},
		Fetches:   []PassthroughFetch{{URL: server.URL + "/p", Path: "p.bin"}},
		Egress:    []string{hostOf(t, server.URL)},
		Timeout:   42 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(gotPayloads) != 1 || string(gotPayloads[0].Content) != "payload" {
		t.Fatalf("payloads = %v, want the mediated fetch content", gotPayloads)
	}
	if gotTimeout != 42*time.Second {
		t.Fatalf("timeout = %v, want 42s", gotTimeout)
	}
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return parsed.Host
}
