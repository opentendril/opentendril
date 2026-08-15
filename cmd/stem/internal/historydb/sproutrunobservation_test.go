package historydb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFreshSchemaIncludesObservationColumns(t *testing.T) {
	store := openTestStore(t)
	for _, column := range []string{"provider", "observation"} {
		var name string
		if err := store.db.QueryRow(`SELECT name FROM pragma_table_info('sproutruns') WHERE name = ?`, column).Scan(&name); err != nil {
			t.Fatalf("fresh sproutruns missing %s column: %v", column, err)
		}
	}
}

func TestSproutRunObservationRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	run := SproutRun{
		RunID:                    "run-observe",
		SessionID:                "s1",
		Provider:                 "openrouter",
		Model:                    "anthropic/claude-sonnet-4.6",
		Status:                   "withered",
		StartedAt:                time.Now().UTC(),
		FinishedAt:               time.Now().UTC(),
		Outcome:                  "failed",
		FailureCategory:          "provider-auth-rejected",
		ProviderRequestAttempted: true,
		ToolInvocations:          0,
		ProviderDiagnostic: &ProviderDiagnostic{
			StatusCode: 401,
			Message:    "User not found",
			Provider:   "openrouter",
		},
	}
	if err := store.RecordSproutRun(ctx, run); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	loaded := loadRun(t, store, "run-observe")
	if loaded.Provider != "openrouter" {
		t.Fatalf("Provider = %q, want openrouter", loaded.Provider)
	}
	if loaded.FailureCategory != "provider-auth-rejected" {
		t.Fatalf("FailureCategory = %q", loaded.FailureCategory)
	}
	if loaded.Outcome != "failed" {
		t.Fatalf("Outcome = %q", loaded.Outcome)
	}
	if !loaded.ProviderRequestAttempted {
		t.Fatal("ProviderRequestAttempted = false")
	}
	if loaded.ToolInvocations != 0 {
		t.Fatalf("ToolInvocations = %d, want 0", loaded.ToolInvocations)
	}
	if loaded.ProviderDiagnostic == nil || loaded.ProviderDiagnostic.StatusCode != 401 || loaded.ProviderDiagnostic.Message != "User not found" {
		t.Fatalf("ProviderDiagnostic = %+v", loaded.ProviderDiagnostic)
	}

	raw, err := json.Marshal(loaded)
	if err != nil {
		t.Fatalf("marshal loaded run: %v", err)
	}
	if strings.Contains(string(raw), "sk-") || strings.Contains(strings.ToLower(string(raw)), "bearer ") {
		t.Fatalf("persisted JSON leaked a credential: %s", raw)
	}
}

func TestSproutRunObservationCompatibilityWriteDoesNotErase(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID:                    "run-keep",
		Status:                   "withered",
		StartedAt:                time.Now().UTC(),
		FailureCategory:          "provider-auth-rejected",
		ProviderRequestAttempted: true,
		ProviderDiagnostic:       &ProviderDiagnostic{StatusCode: 401, Message: "User not found"},
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "run-keep", Status: "withered",
	}); err != nil {
		t.Fatalf("compat write: %v", err)
	}
	loaded := loadRun(t, store, "run-keep")
	if loaded.FailureCategory != "provider-auth-rejected" {
		t.Fatalf("compat write erased observation: %+v", loaded)
	}
}
