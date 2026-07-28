package mesh

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestNormalizeGraftEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"http to ws", "http://example.com", "ws://example.com/v1/mesh/graft", false},
		{"https to wss", "https://example.com", "wss://example.com/v1/mesh/graft", false},
		{"bare host", "example.com", "ws:///example.com/v1/mesh/graft", false},
		{"ws passthrough", "ws://example.com", "ws://example.com/v1/mesh/graft", false},
		{"wss passthrough", "wss://example.com", "wss://example.com/v1/mesh/graft", false},
		{"unsupported scheme", "ftp://example.com", "", true},
		{"empty path gets append", "http://example.com", "ws://example.com/v1/mesh/graft", false},
		{"path ends in v1/mesh/graft left alone", "http://example.com/v1/mesh/graft", "ws://example.com/v1/mesh/graft", false},
		{"trailing slash normalized", "http://example.com/", "ws://example.com/v1/mesh/graft", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeGraftEndpoint(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("normalizeGraftEndpoint() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("normalizeGraftEndpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShortCommitHash(t *testing.T) {
	if got := shortCommitHash("1234567890abcdef"); got != "12345678" {
		t.Errorf("shortCommitHash(>8 chars) = %v, want 12345678", got)
	}
	if got := shortCommitHash("12345678"); got != "12345678" {
		t.Errorf("shortCommitHash(=8 chars) = %v, want 12345678", got)
	}
	if got := shortCommitHash("1234"); got != "1234" {
		t.Errorf("shortCommitHash(<8 chars) = %v, want 1234", got)
	}
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = dir
	_ = cmd.Run()
	return dir
}

func commitTestFile(t *testing.T, dir string, name string, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
	if err != nil {
		t.Fatalf("write file: %v", err)
	}
	cmd := exec.Command("git", "add", name)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}
	cmd = exec.Command("git", "commit", "-m", "test commit: "+name)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}
}

func TestCollectLatestCommitPatch(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		repo := initTestGitRepo(t)
		commitTestFile(t, repo, "file.txt", "content")

		hash, patch, err := collectLatestCommitPatch(context.Background(), repo)
		if err != nil {
			t.Fatalf("collectLatestCommitPatch() failed: %v", err)
		}
		if hash == "" {
			t.Errorf("expected non-empty hash")
		}
		if patch == "" || !strings.Contains(patch, "file.txt") {
			t.Errorf("patch did not contain file.txt: %v", patch)
		}

		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = repo
		out, _ := cmd.CombinedOutput()
		head := strings.TrimSpace(string(out))
		if hash != head {
			t.Errorf("returned hash %q, wanted %q", hash, head)
		}
	})

	t.Run("not a git repo", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := collectLatestCommitPatch(context.Background(), dir)
		if err == nil {
			t.Errorf("expected error for non-git repo, got nil")
		}
	})
}

func newFakeGraftClientServer(t *testing.T, handler func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade failed: %v", err)
			return
		}
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDelegatePushSuccess(t *testing.T) {
	repo := initTestGitRepo(t)
	commitTestFile(t, repo, "file.txt", "content")

	srv := newFakeGraftClientServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var req graftRequest
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		_ = conn.WriteJSON(graftMessage{Type: "graft-log", Stream: "server", Message: "log message"})
		_ = conn.WriteJSON(graftMessage{Type: "graft-result", Status: "complete", CommitHash: "fakehash123"})
	})

	client := NewClient(srv.URL, "fake-token")
	var logBuf bytes.Buffer
	client.SetLogWriter(&logBuf)

	hash, err := client.DelegatePush(context.Background(), repo, "", "")
	if err != nil {
		t.Fatalf("DelegatePush failed: %v", err)
	}
	if hash != "fakehash123" {
		t.Errorf("got hash %q, want fakehash123", hash)
	}
	if !strings.Contains(logBuf.String(), "log message") {
		t.Errorf("expected log buffer to contain 'log message', got %q", logBuf.String())
	}
}

func TestDelegatePushServerError(t *testing.T) {
	repo := initTestGitRepo(t)
	commitTestFile(t, repo, "file.txt", "content")

	srv := newFakeGraftClientServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var req graftRequest
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		_ = conn.WriteJSON(graftMessage{Type: "graft-result", Status: "error", Error: "fake server error"})
	})

	client := NewClient(srv.URL, "fake-token")
	_, err := client.DelegatePush(context.Background(), repo, "", "")
	if err == nil {
		t.Fatalf("expected error from DelegatePush, got nil")
	}
	if !strings.Contains(err.Error(), "fake server error") {
		t.Errorf("expected error to contain 'fake server error', got: %v", err)
	}
}

func TestDelegatePushDialFailure(t *testing.T) {
	repo := initTestGitRepo(t)
	commitTestFile(t, repo, "file.txt", "content")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	client := NewClient(srv.URL, "fake-token")
	_, err := client.DelegatePush(context.Background(), repo, "", "")
	if err == nil {
		t.Fatalf("expected error from DelegatePush for closed server, got nil")
	}
	if !strings.Contains(err.Error(), "mesh graft websocket dial failed") {
		t.Errorf("expected dial failure error, got: %v", err)
	}
}

func TestDelegatePushEmptyWorkspace(t *testing.T) {
	client := NewClient("ws://localhost", "fake-token")
	_, err := client.DelegatePush(context.Background(), t.TempDir(), "", "")
	if err == nil {
		t.Fatalf("expected error for empty workspace, got nil")
	}
	if !strings.Contains(err.Error(), "git rev-parse HEAD failed") {
		t.Errorf("expected git error, got: %v", err)
	}
}
