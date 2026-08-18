package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/healthmon"
	"github.com/opentendril/opentendril/cmd/stem/internal/mesh"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

const localSocketTestBotanistKey = "botanist-key"

func testUnixSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ot-stem-sock-*")
	if err != nil {
		t.Fatalf("mkdir temp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "stem.sock")
}

func newLocalSocketMux(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	bus := eventbus.New()
	t.Cleanup(bus.Shutdown)
	sessions, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	pending := core.NewPendingConfirmationStore()
	gate := &receptors.DelegationGate{
		Authorizer: core.NewDelegationAuthorizer(nil).WithPendingStore(pending, time.Hour),
		Bus:        bus,
	}
	return buildServeMux(serveDependencies{
		APIKey:         localSocketTestBotanistKey,
		AdminKey:       localSocketTestBotanistKey,
		DelegationGate: gate,
		EventBus:       bus,
		Sessions:       sessions,
		CoreService:    core.NewService(sessions),
		HealthMonitor:  healthmon.New(bus, time.Hour),
		TendrilDir:     dir,
		MeshServer:     mesh.NewServer(dir),
		PendingStore:   pending,
		Networked:      false,
	})
}

func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func getUnix(t *testing.T, socketPath, path, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://localhost"+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := unixHTTPClient(socketPath).Do(req)
	if err != nil {
		t.Fatalf("unix GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func startTCPHandler(t *testing.T, mux http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return "http://" + ln.Addr().String()
}

func leaveStaleUnixSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		t.Fatal("listen did not return a UnixListener")
	}
	unixLn.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatalf("close seed listener: %v", err)
	}
}

func TestOpenLocalSocketServesAuthenticatedMux(t *testing.T) {
	path := testUnixSocketPath(t)
	mux := newLocalSocketMux(t)
	sock := startOptionalLocalSocket(mux, path)
	if sock == nil {
		t.Fatal("startOptionalLocalSocket returned nil for an absolute unused path")
	}
	t.Cleanup(func() { _ = sock.Close() })

	health := getUnix(t, path, "/health", "")
	if health.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(health.Body)
		t.Fatalf("/health status = %d, want 200; body=%s", health.StatusCode, body)
	}
	var report map[string]any
	if err := json.NewDecoder(health.Body).Decode(&report); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	if overall, _ := report["overall"].(bool); !overall {
		t.Fatalf("/health overall = %v, want true", report["overall"])
	}

	ok := getUnix(t, path, "/v1/phytomers", localSocketTestBotanistKey)
	if ok.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(ok.Body)
		t.Fatalf("botanist GET /v1/phytomers status = %d, want 200; body=%s", ok.StatusCode, body)
	}

	missing := getUnix(t, path, "/v1/phytomers", "")
	if missing.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d, want 401", missing.StatusCode)
	}

	wrong := getUnix(t, path, "/v1/phytomers", "wrong-key")
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bearer status = %d, want 401", wrong.StatusCode)
	}
}

func TestLocalSocketWebSocketAuth(t *testing.T) {
	path := testUnixSocketPath(t)
	mux := newLocalSocketMux(t)
	sock := startOptionalLocalSocket(mux, path)
	if sock == nil {
		t.Fatal("startOptionalLocalSocket returned nil")
	}
	t.Cleanup(func() { _ = sock.Close() })

	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}

	conn, resp, err := dialer.Dial("ws://localhost/ws", http.Header{
		"Authorization": []string{"Bearer " + localSocketTestBotanistKey},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
		t.Fatalf("authenticated /ws upgrade failed: err=%v status=%d", err, status)
	}
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("authenticated /ws status = %d, want 101", resp.StatusCode)
	}

	_, denied, err := dialer.Dial("ws://localhost/ws", nil)
	if err == nil {
		t.Fatal("unauthenticated /ws upgrade succeeded")
	}
	if denied == nil {
		t.Fatal("unauthenticated /ws returned no HTTP response")
	}
	defer denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /ws status = %d, want 401", denied.StatusCode)
	}

	_, wrong, err := dialer.Dial("ws://localhost/ws", http.Header{
		"Authorization": []string{"Bearer wrong-key"},
	})
	if err == nil {
		t.Fatal("wrong-bearer /ws upgrade succeeded")
	}
	if wrong == nil {
		t.Fatal("wrong-bearer /ws returned no HTTP response")
	}
	defer wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-bearer /ws status = %d, want 401", wrong.StatusCode)
	}
}

func TestOpenLocalSocketRefusesRelativePath(t *testing.T) {
	_, err := openLocalSocket("stem.sock")
	if err == nil {
		t.Fatal("relative path was accepted")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("error = %v, want absolute-path refusal", err)
	}
}

func TestOpenLocalSocketLeavesOrdinaryFileUntouched(t *testing.T) {
	path := testUnixSocketPath(t)
	const payload = "not-a-socket"
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	_, err := openLocalSocket(path)
	if err == nil {
		t.Fatal("ordinary file was treated as a bindable socket")
	}
	if !strings.Contains(err.Error(), "not a Unix socket") {
		t.Fatalf("error = %v, want non-socket refusal", err)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("decoy disappeared: %v", readErr)
	}
	if string(got) != payload {
		t.Fatalf("decoy contents = %q, want %q", got, payload)
	}
}

func TestOpenLocalSocketRefusesLiveListener(t *testing.T) {
	path := testUnixSocketPath(t)
	live, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	_, err = openLocalSocket(path)
	if err == nil {
		t.Fatal("live listener was replaced")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("error = %v, want in-use refusal", err)
	}

	conn, dialErr := net.Dial("unix", path)
	if dialErr != nil {
		t.Fatalf("original listener stopped accepting: %v", dialErr)
	}
	_ = conn.Close()
}

func TestOpenLocalSocketRecoversSameOwnerStaleSocket(t *testing.T) {
	path := testUnixSocketPath(t)
	leaveStaleUnixSocket(t, path)

	sock, err := openLocalSocket(path)
	if err != nil {
		t.Fatalf("stale same-owner socket was not recovered: %v", err)
	}
	t.Cleanup(func() { _ = sock.Close() })

	sock.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	go serveLocalSocket(sock)

	resp := getUnix(t, path, "/ready", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("recovered socket status = %d, want 204", resp.StatusCode)
	}
}

func TestLocalSocketCloseRemovesOnlyOwnedSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "ot-stem-sock-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	owned := filepath.Join(dir, "stem.sock")
	other := filepath.Join(dir, "other.sock")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write other: %v", err)
	}

	sock, err := openLocalSocket(owned)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sock.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := os.Lstat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket still present: %v", err)
	}
	got, readErr := os.ReadFile(other)
	if readErr != nil {
		t.Fatalf("unrelated path was removed: %v", readErr)
	}
	if string(got) != "keep" {
		t.Fatalf("unrelated path contents = %q", got)
	}
}

func TestLocalSocketFailureLeavesTCPServing(t *testing.T) {
	mux := newLocalSocketMux(t)
	tcpURL := startTCPHandler(t, mux)

	path := testUnixSocketPath(t)
	if err := os.WriteFile(path, []byte("blocker"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	if sock := startOptionalLocalSocket(mux, path); sock != nil {
		_ = sock.Close()
		t.Fatal("local listener started over an ordinary file")
	}

	resp, err := http.Get(tcpURL + "/health")
	if err != nil {
		t.Fatalf("TCP /health after local failure: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("TCP /health status = %d, want 200", resp.StatusCode)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("blocker disappeared: %v", readErr)
	}
	if string(got) != "blocker" {
		t.Fatalf("blocker contents = %q", got)
	}
}

func TestUnsetLocalSocketCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if sock := startOptionalLocalSocket(newLocalSocketMux(t), ""); sock != nil {
		_ = sock.Close()
		t.Fatal("empty path created a local socket")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty path created files: %v", entries)
	}
}

func TestLocalSocketDoesNotChangeTCPBindPosture(t *testing.T) {
	t.Setenv(EnvLocalSocket, "/run/opentendril/stem.sock")
	t.Setenv(EnvTerroirHost, "")
	if got := serveListenHost(); got != "127.0.0.1" {
		t.Fatalf("serveListenHost() = %q, want 127.0.0.1 with local socket configured", got)
	}
	if isNetworkedBindHost(serveListenHost()) {
		t.Fatal("local socket configuration classified the TCP bind as off-host")
	}

	t.Setenv(EnvTerroirHost, "0.0.0.0")
	if got := serveListenHost(); got != "0.0.0.0" {
		t.Fatalf("TERROIR_HOST=0.0.0.0 became %q when local socket was set", got)
	}
	if !isNetworkedBindHost(serveListenHost()) {
		t.Fatal("TERROIR_HOST=0.0.0.0 lost off-host classification when local socket was set")
	}
}
