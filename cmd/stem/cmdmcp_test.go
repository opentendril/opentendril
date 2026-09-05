package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

func TestNewInProcessMCPHandlerReconcilesOrphansBeforeServing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Chdir(dir)
	dbPath := filepath.Join(dir, ".tendril", "history.db")
	t.Setenv(historydb.EnvDBPath, dbPath)
	t.Setenv(envPollen, "")

	store, err := historydb.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.RecordSeedRun(ctx, historydb.SeedRun{
		Handle: "seed-orphan", Pollen: "codex", PhytomerID: "tendril-orphan",
		Substrate: "core", Status: core.SeedStatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}
	if _, err := store.AcceptContinuation(ctx, historydb.ContinuationAcceptance{
		PhytomerID: "tendril-orphan", Pollen: "codex", Substrate: "core", Handle: "seed-orphan",
		IdempotencyKey: "k-orphan", Intent: "SECRET_ORPHAN_INTENT",
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	handler, release, err := newInProcessMCPHandler(ctx)
	if err != nil {
		t.Fatalf("in-process MCP: %v", err)
	}
	defer release()

	text, isError := mcpProcessTool(t, handler, receptors.MCPViewSproutWatch, map[string]any{
		"sessionId": "tendril-orphan",
	})
	if isError {
		t.Fatalf("operator watch after reconcile: %s", text)
	}
	if !strings.Contains(text, `"status": "withered"`) && !strings.Contains(text, `"status":"withered"`) {
		t.Fatalf("orphan seed was not terminalized: %s", text)
	}
	if !strings.Contains(text, `"deliveryState": "failed"`) && !strings.Contains(text, `"deliveryState":"failed"`) {
		t.Fatalf("orphan continuation was not failed: %s", text)
	}
	if strings.Contains(text, "SECRET_ORPHAN_INTENT") {
		t.Fatalf("raw orphan intent leaked: %s", text)
	}

	denied, isError := mcpProcessTool(t, handler, "seedGrow", map[string]any{
		"substrate": "core",
		"goal":      "should be denied without pollen",
		"verify":    []string{"true"},
		"detached":  true,
	})
	if !isError || !strings.Contains(denied, "delegation denied") {
		t.Fatalf("unbound pollen seedGrow: isError=%v text=%q", isError, denied)
	}
}

func TestNewInProcessMCPHandlerPersistenceUnavailableFailsHonestly(t *testing.T) {
	ctx := context.Background()
	t.Chdir(t.TempDir())
	t.Setenv(historydb.EnvDBLogging, "false")
	t.Setenv(envPollen, "codex")

	handler, release, err := newInProcessMCPHandler(ctx)
	if err != nil {
		t.Fatalf("disabled history should still start: %v", err)
	}
	defer release()

	continueText, isError := mcpProcessTool(t, handler, "phytomerContinue", map[string]any{
		"sessionId":      "tendril-1",
		"intent":         "keep going",
		"idempotencyKey": "k1",
	})
	if !isError {
		t.Fatalf("continue without history succeeded: %s", continueText)
	}
	if !strings.Contains(continueText, core.ErrContinuationHistoryUnavailable.Error()) &&
		!strings.Contains(continueText, "delegation denied") {
		t.Fatalf("continue without history: %s", continueText)
	}

	watchText, isError := mcpProcessTool(t, handler, receptors.MCPViewSproutWatch, map[string]any{
		"sessionId": "tendril-1",
	})
	if !isError {
		t.Fatalf("watch without history succeeded: %s", watchText)
	}
}

func mcpProcessTool(t *testing.T, handler *receptors.MCPHandler, name string, args map[string]any) (string, bool) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal tools/call %s: %v", name, err)
	}
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(handler.ProcessMCPMessage(payload), &response); err != nil {
		t.Fatalf("decode tools/call %s: %v", name, err)
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		t.Fatalf("tools/call %s protocol error: %s", name, response.Error)
	}
	text := ""
	if len(response.Result.Content) > 0 {
		text = response.Result.Content[0].Text
	}
	return text, response.Result.IsError
}
