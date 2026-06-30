package mesh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
