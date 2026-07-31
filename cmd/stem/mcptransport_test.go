package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func setupTestMCPTransport(t *testing.T) (*httptest.Server, string, string, *core.StomaSpec) {
	t.Helper()

	dir := t.TempDir()
	tendrilDir := filepath.Join(dir, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	botanistKey := "botanist-key"
	secretAlpha, _, err := core.IssuePollinatorCredential(dir, "alpha-pollen", "")
	if err != nil {
		t.Fatalf("issue alpha: %v", err)
	}
	secretBeta, _, err := core.IssuePollinatorCredential(dir, "beta-pollen", "")
	if err != nil {
		t.Fatalf("issue beta: %v", err)
	}

	creds, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load creds: %v", err)
	}

	grants := []core.DelegationGrant{
		{
			Pollen:           "alpha-pollen",
			OperationClasses: []string{core.CapStomaPass},
			Substrates:       []string{"core"},
		},
		{
			Pollen:           "beta-pollen",
			OperationClasses: []string{core.CapGitCommit},
			Substrates:       []string{"core"},
		},
	}
	bus := eventbus.New()
	delegationGate := &receptors.DelegationGate{
		Authorizer:  core.NewDelegationAuthorizer(grants),
		Bus:         bus,
		Pollinators: creds,
	}

	stomaSpec := &core.StomaSpec{}
	sessions, _ := session.NewManager(context.Background(), nil)
	coreSvc := core.NewService(sessions).
		WithStoma(core.StomaOperations{
			Run: func(ctx context.Context, spec core.StomaSpec) (core.StomaPassResult, error) {
				// Removed *stomaSpec = spec to prevent data race
				return core.StomaPassResult{Status: "completed", ExitCode: 0, Stdout: "ran"}, nil
			},
		}).
		WithGit(core.GitOperations{
			Commit: func(ctx context.Context, spec core.GitCommitSpec) (core.GitCommitResult, error) {
				return core.GitCommitResult{Status: "committed", CommitHash: "abc123"}, nil
			},
		})

	deps := serveDependencies{
		APIKey:                botanistKey,
		PollinatorCredentials: creds,
		DelegationGate:        delegationGate,
		EventBus:              bus,
		Sessions:              sessions,
		CoreService:           coreSvc,
	}
	mux := buildServeMux(deps)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv, secretAlpha, secretBeta, stomaSpec
}

func invokeMCPTool(t *testing.T, srv *httptest.Server, token string, name string, args map[string]any) (string, bool, error) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1", bytes.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("unexpected HTTP status %d: %s", resp.StatusCode, string(body))
	}

	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", false, err
	}
	if response.Error != nil {
		return response.Error.Message, true, nil
	}
	text := ""
	if len(response.Result.Content) > 0 {
		text = response.Result.Content[0].Text
	}
	return text, response.Result.IsError, nil
}

func TestMCPTransport_CredentialAuthorized(t *testing.T) {
	srv, secretAlpha, _, _ := setupTestMCPTransport(t)
	text, isError, err := invokeMCPTool(t, srv, secretAlpha, core.CapStomaPass, map[string]any{"substrate": "core", "command": []string{"ls"}})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if isError {
		t.Fatalf("authorized call was denied: %q", text)
	}
}

func TestMCPTransport_CredentialDenied(t *testing.T) {
	srv, secretAlpha, _, _ := setupTestMCPTransport(t)
	// alpha-pollen does not have git.commit
	text, isError, err := invokeMCPTool(t, srv, secretAlpha, core.CapGitCommit, map[string]any{"substrate": "core", "message": "msg"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !isError {
		t.Fatalf("unauthorized call was allowed")
	}
	if !strings.Contains(text, "delegation denied: no active grant covers Pollen \"alpha-pollen\", operation-class \"git.commit\", substrate \"core\"") {
		t.Fatalf("denial text incorrect: %q", text)
	}
}

func TestMCPTransport_NoCredentialGetsNoDelegation(t *testing.T) {
	srv, _, _, _ := setupTestMCPTransport(t)
	// Authenticate with Botanist key
	text, isError, err := invokeMCPTool(t, srv, "botanist-key", core.CapStomaPass, map[string]any{"substrate": "core", "command": []string{"ls"}})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !isError {
		t.Fatalf("botanist key call was allowed")
	}
	if !strings.Contains(text, "delegation is not configured") {
		t.Fatalf("denial text incorrect: %q", text)
	}
}

func TestMCPTransport_ConcurrentCallersIsolated(t *testing.T) {
	srv, secretAlpha, secretBeta, _ := setupTestMCPTransport(t)
	var wg sync.WaitGroup
	errs := make(chan string, 100)

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			text, isError, err := invokeMCPTool(t, srv, secretAlpha, core.CapStomaPass, map[string]any{"substrate": "core", "command": []string{"ls"}})
			if err != nil || isError {
				errs <- "alpha failed stoma.pass: " + text
			}
			text, isError, err = invokeMCPTool(t, srv, secretAlpha, core.CapGitCommit, map[string]any{"substrate": "core", "message": "msg"})
			if err != nil || !isError {
				errs <- "alpha allowed git.commit: " + text
			}
		}()
		go func() {
			defer wg.Done()
			text, isError, err := invokeMCPTool(t, srv, secretBeta, core.CapGitCommit, map[string]any{"substrate": "core", "message": "msg"})
			if err != nil || isError {
				errs <- "beta failed git.commit: " + text
			}
			text, isError, err = invokeMCPTool(t, srv, secretBeta, core.CapStomaPass, map[string]any{"substrate": "core", "command": []string{"ls"}})
			if err != nil || !isError {
				errs <- "beta allowed stoma.pass: " + text
			}
		}()
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
