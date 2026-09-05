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
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
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

const (
	mcpWatchPhytomerID = "tendril-watch"
	mcpWatchHandle     = "seed-watch"
	mcpWatchPollen     = "alpha-pollen"
	mcpWatchSubstrate  = "core"
)

// setupTestMCPWatchTransport stands up the governed daemon mux exactly as
// production does: buildServeMux binds the shared WatchAuthority into MCP,
// Core observes through phytomerObservationSource(history), and Pollinator
// credentials authenticate POST /v1.
func setupTestMCPWatchTransport(t *testing.T, grants []core.DelegationGrant) (*httptest.Server, string, string) {
	t.Helper()

	dir := t.TempDir()
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

	store, err := historydb.Open(context.Background(), filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle:     mcpWatchHandle,
		Pollen:     mcpWatchPollen,
		PhytomerID: mcpWatchPhytomerID,
		Substrate:  mcpWatchSubstrate,
		Status:     core.SeedStatusRunning,
		StartedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}

	bus := eventbus.New()
	sessions, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	coreSvc := core.NewService(sessions).WithPhytomerObservationSource(phytomerObservationSource(store))
	deps := serveDependencies{
		APIKey:                "botanist-key",
		PollinatorCredentials: creds,
		DelegationGate: &receptors.DelegationGate{
			Authorizer:  core.NewDelegationAuthorizer(grants),
			Bus:         bus,
			Pollinators: creds,
		},
		EventBus:    bus,
		Sessions:    sessions,
		History:     store,
		CoreService: coreSvc,
	}
	srv := httptest.NewServer(buildServeMux(deps))
	t.Cleanup(srv.Close)
	return srv, secretAlpha, secretBeta
}

func TestMCPTransport_SproutWatchAuthorized(t *testing.T) {
	srv, secretAlpha, _ := setupTestMCPWatchTransport(t, []core.DelegationGrant{
		{Pollen: "alpha-pollen", OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"core"}},
		{Pollen: "beta-pollen", OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"core"}},
	})

	text, isError, err := invokeMCPTool(t, srv, secretAlpha, receptors.MCPViewSproutWatch, map[string]any{
		"sessionId": mcpWatchPhytomerID,
	})
	if err != nil {
		t.Fatalf("owner sproutWatch HTTP: %v", err)
	}
	if isError {
		t.Fatalf("owner sproutWatch denied: %s", text)
	}
	if !strings.Contains(text, `"phytomerId": "`+mcpWatchPhytomerID+`"`) && !strings.Contains(text, `"phytomerId":"`+mcpWatchPhytomerID+`"`) {
		t.Fatalf("missing phytomerId: %s", text)
	}
	if !strings.Contains(text, `"handle": "`+mcpWatchHandle+`"`) && !strings.Contains(text, `"handle":"`+mcpWatchHandle+`"`) {
		t.Fatalf("missing handle: %s", text)
	}
	if !strings.Contains(text, `"status": "`+core.SeedStatusRunning+`"`) && !strings.Contains(text, `"status":"`+core.SeedStatusRunning+`"`) {
		t.Fatalf("missing status: %s", text)
	}
	for _, banned := range []string{`"intent"`, "intentDigest", "idempotencyKey"} {
		if strings.Contains(text, banned) {
			t.Fatalf("unsafe field %q in observation: %s", banned, text)
		}
	}
}

func TestMCPTransport_SproutWatchDeniedWrongPollen(t *testing.T) {
	srv, _, secretBeta := setupTestMCPWatchTransport(t, []core.DelegationGrant{
		{Pollen: "alpha-pollen", OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"core"}},
		{Pollen: "beta-pollen", OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"core"}},
	})

	text, isError, err := invokeMCPTool(t, srv, secretBeta, receptors.MCPViewSproutWatch, map[string]any{
		"sessionId": mcpWatchPhytomerID,
	})
	if err != nil {
		t.Fatalf("wrong-pollen sproutWatch HTTP: %v", err)
	}
	if !isError {
		t.Fatalf("wrong pollen was allowed: %s", text)
	}
	if !strings.Contains(text, "delegation denied: this phytomer carries a run dispatched by another subject") {
		t.Fatalf("wrong pollen denial missing: %s", text)
	}
	if strings.Contains(text, mcpWatchHandle) || strings.Contains(text, `"phytomerId"`) || strings.Contains(text, `"handle"`) || strings.Contains(text, `"status"`) {
		t.Fatalf("wrong pollen leaked observation identity: %s", text)
	}
}

func TestMCPTransport_SproutWatchDeniedWithoutGrant(t *testing.T) {
	srv, secretAlpha, _ := setupTestMCPWatchTransport(t, []core.DelegationGrant{
		{Pollen: "alpha-pollen", OperationClasses: []string{core.CapSeedGrow}, Substrates: []string{"core"}},
	})

	text, isError, err := invokeMCPTool(t, srv, secretAlpha, receptors.MCPViewSproutWatch, map[string]any{
		"sessionId": mcpWatchPhytomerID,
	})
	if err != nil {
		t.Fatalf("no-grant sproutWatch HTTP: %v", err)
	}
	if !isError {
		t.Fatalf("no sprout.watch grant was allowed: %s", text)
	}
	if !strings.Contains(text, `no active grant covers Pollen "alpha-pollen", operation-class "sprout.watch", substrate "core"`) {
		t.Fatalf("no-grant denial missing: %s", text)
	}
	if strings.Contains(text, mcpWatchHandle) || strings.Contains(text, `"phytomerId"`) || strings.Contains(text, `"handle"`) || strings.Contains(text, `"status"`) {
		t.Fatalf("no-grant leaked observation identity: %s", text)
	}
}
