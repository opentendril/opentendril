package mesh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHandleAdminIssueToken(t *testing.T) {
	workspace := t.TempDir()

	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	if err := WriteKeyPair(workspace, pair); err != nil {
		t.Fatalf("WriteKeyPair failed: %v", err)
	}

	server := NewServer(workspace)
	body := strings.NewReader(`{"subject":"mesh-graft","audience":"mesh-graft","ttl":"30m"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/mesh/admin/issue-token", body)
	rec := httptest.NewRecorder()

	server.HandleAdminIssueToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		t.Fatalf("expected token in response")
	}

	if _, err := VerifyWorkspaceToken(workspace, payload.Token, TokenValidationOptions{
		Now:              time.Now().UTC(),
		ExpectedIssuer:   defaultIssuer,
		ExpectedAudience: defaultAudience,
		ExpectedScope:    defaultMeshScope,
	}); err != nil {
		t.Fatalf("VerifyWorkspaceToken failed: %v", err)
	}
}

// newGraftWSServer starts an httptest server backed by HandleGraftWebSocket and
// returns the server together with a key-pair written to the supplied workspace.
func newGraftWSServer(t *testing.T, workspace string) (*httptest.Server, KeyPair) {
	t.Helper()
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if err := WriteKeyPair(workspace, pair); err != nil {
		t.Fatalf("WriteKeyPair: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(NewServer(workspace).HandleGraftWebSocket))
	t.Cleanup(srv.Close)
	return srv, pair
}

// tokenForWorkspace issues a signed JWT whose workspacePath claim is set to the
// given path, signed with the supplied private key (no disk I/O needed).
func tokenForWorkspace(t *testing.T, pair KeyPair, workspacePath string) string {
	t.Helper()
	tok, err := IssueToken(pair.PrivateKey, TokenOptions{
		Issuer:        defaultIssuer,
		Subject:       defaultSubject,
		Audience:      []string{defaultAudience},
		MeshScope:     defaultMeshScope,
		WorkspacePath: workspacePath,
		ExpiresIn:     time.Hour,
		Now:           time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok
}

// dialGraftWS opens a WebSocket connection to the test server using the given
// bearer token and returns (conn, httpResponse, error).
func dialGraftWS(srv *httptest.Server, bearerToken string) (*websocket.Conn, *http.Response, error) {
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	header := http.Header{"Authorization": {"Bearer " + bearerToken}}
	dialer := websocket.Dialer{}
	return dialer.Dial(u, header)
}

// TestHandleGraftWebSocket_WorkspaceMismatchRejected verifies that a token
// carrying a workspacePath different from the server's workspace is rejected
// before the WebSocket upgrade completes.
func TestHandleGraftWebSocket_WorkspaceMismatchRejected(t *testing.T) {
	workspace := t.TempDir()
	srv, pair := newGraftWSServer(t, workspace)

	// Mint a token for a *different* workspace path.
	badToken := tokenForWorkspace(t, pair, "/some/other/workspace")

	_, resp, err := dialGraftWS(srv, badToken)
	if err == nil {
		t.Fatal("expected dial to fail with mismatched workspace token, got nil error")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestHandleGraftWebSocket_CorrectWorkspaceAccepted verifies that a token
// carrying the correct workspacePath still passes the workspace check — no
// regression on the happy path.
func TestHandleGraftWebSocket_CorrectWorkspaceAccepted(t *testing.T) {
	workspace := t.TempDir()
	srv, pair := newGraftWSServer(t, workspace)

	// The Server normalises the workspace through ResolveRepoRoot; since the
	// temp dir is not inside a git repo, ResolveRepoRoot returns the path
	// unchanged, so we can mint the token against the raw path.
	goodToken := tokenForWorkspace(t, pair, workspace)

	conn, resp, err := dialGraftWS(srv, goodToken)
	if err != nil {
		// A 101 Switching Protocols is success; anything else is a failure.
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("expected successful upgrade, got status %d, err: %v", status, err)
	}
	defer conn.Close()

	// Server sends a "graft-status: connected" message immediately after
	// upgrade — read it to confirm the connection is live.
	var msg graftMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if msg.Type != "graft-status" || msg.Status != "connected" {
		t.Errorf("got %+v, want graft-status/connected", msg)
	}
}

// dialGraftWSWithOrigin opens a WebSocket connection like dialGraftWS but
// also sets an Origin header on the upgrade request. The origin argument is
// sent verbatim; pass an empty string to omit the header entirely.
func dialGraftWSWithOrigin(srv *httptest.Server, bearerToken, origin string) (*websocket.Conn, *http.Response, error) {
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	header := http.Header{"Authorization": {"Bearer " + bearerToken}}
	if origin != "" {
		header.Set("Origin", origin)
	}
	dialer := websocket.Dialer{}
	return dialer.Dial(u, header)
}

// TestHandleGraftWebSocket_NoOriginSucceeds verifies that a genuine
// service-to-service dial (no Origin header) still completes the upgrade.
// This is the no-regression companion to the origin-rejection test below.
func TestHandleGraftWebSocket_NoOriginSucceeds(t *testing.T) {
	workspace := t.TempDir()
	srv, pair := newGraftWSServer(t, workspace)

	goodToken := tokenForWorkspace(t, pair, workspace)

	// Dial without setting any Origin header — simulates mesh.Client / CLI.
	conn, resp, err := dialGraftWSWithOrigin(srv, goodToken, "")
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("expected successful upgrade with no Origin header, got status %d, err: %v", status, err)
	}
	defer conn.Close()

	// Server sends "graft-status: connected" immediately after upgrade.
	var msg graftMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if msg.Type != "graft-status" || msg.Status != "connected" {
		t.Errorf("got %+v, want graft-status/connected", msg)
	}
}

// TestHandleGraftWebSocket_OriginHeaderRejected verifies that any WebSocket
// upgrade carrying an Origin header is refused — even when the Origin value
// matches the test server's own URL (proving this is reject-if-present, not
// a same-host allowlist).
func TestHandleGraftWebSocket_OriginHeaderRejected(t *testing.T) {
	workspace := t.TempDir()
	srv, pair := newGraftWSServer(t, workspace)

	goodToken := tokenForWorkspace(t, pair, workspace)

	cases := []struct {
		name   string
		origin string
	}{
		{"foreign origin", "http://attacker.example.com"},
		{"same-host origin", srv.URL}, // must also be rejected
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, resp, err := dialGraftWSWithOrigin(srv, goodToken, tc.origin)
			if err == nil {
				t.Fatalf("expected dial to fail with Origin=%q, got nil error", tc.origin)
			}
			if resp != nil && resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want %d for Origin=%q", resp.StatusCode, http.StatusForbidden, tc.origin)
			}
		})
	}
}

// TestHandleAdminIssueToken_WorkspaceOverrideIgnored verifies that a caller
// supplying a workspacePath in the request body cannot influence the claim
// embedded in the issued token — the token must always carry s.workspace.
func TestHandleAdminIssueToken_WorkspaceOverrideIgnored(t *testing.T) {
	workspace := t.TempDir()

	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if err := WriteKeyPair(workspace, pair); err != nil {
		t.Fatalf("WriteKeyPair: %v", err)
	}

	server := NewServer(workspace)
	// Request body deliberately supplies a different workspacePath.
	body := strings.NewReader(`{"workspacePath":"/attacker/controlled/path"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/mesh/admin/issue-token", body)
	rec := httptest.NewRecorder()

	server.HandleAdminIssueToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify the token and assert the claim is s.workspace, not the attacker value.
	claims, err := VerifyWorkspaceToken(workspace, payload.Token, TokenValidationOptions{
		Now:               time.Now().UTC(),
		ExpectedWorkspace: workspace,
	})
	if err != nil {
		t.Fatalf("VerifyWorkspaceToken: %v", err)
	}
	if claims.WorkspacePath == "/attacker/controlled/path" {
		t.Fatalf("issued token contains caller-controlled workspace path — override not ignored")
	}
	if claims.WorkspacePath != workspace {
		t.Errorf("workspacePath = %q, want %q", claims.WorkspacePath, workspace)
	}
}
