package core_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

func newTestCore(t *testing.T) *core.Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return core.NewService(manager)
}

func TestRegistryMatchesCanonicalNames(t *testing.T) {
	svc := newTestCore(t)

	got := make([]string, 0)
	for _, c := range svc.Capabilities() {
		got = append(got, c.Name)
	}
	sort.Strings(got)

	want := core.CapabilityNames()
	if len(got) != len(want) {
		t.Fatalf("registry has %d capabilities, canonical list has %d: %v vs %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("capability mismatch at %d: registry %q vs canonical %q", i, got[i], want[i])
		}
	}
}

func TestEveryCapabilityIsInvokable(t *testing.T) {
	svc := newTestCore(t)
	for _, c := range svc.Capabilities() {
		if c.Invoke == nil {
			t.Errorf("capability %q has a nil Invoke", c.Name)
		}
		if c.InputSchema == nil {
			t.Errorf("capability %q has a nil InputSchema", c.Name)
		}
	}
}

func TestSessionLifecycleThroughCore(t *testing.T) {
	ctx := context.Background()
	svc := newTestCore(t)

	// create
	created, err := svc.CreateSession(ctx, core.CreateSessionInput{
		Origin:      session.OriginCLI,
		Preferences: session.Preferences{Model: "claude-sonnet"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Preferences.Model != "claude-sonnet" {
		t.Fatalf("unexpected created session: %+v", created)
	}

	// get
	got, err := svc.GetSession(ctx, core.GetSessionInput{SessionID: created.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("get returned %q, want %q", got.ID, created.ID)
	}

	// update
	updated, err := svc.UpdateSessionPreferences(ctx, core.UpdateSessionInput{
		SessionID:   created.ID,
		Preferences: session.Preferences{Genotype: "verifier"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Preferences.Genotype != "verifier" || updated.Preferences.Model != "claude-sonnet" {
		t.Fatalf("update did not merge preferences: %+v", updated.Preferences)
	}

	// list
	sessions, err := svc.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("list returned %d sessions, want 1", len(sessions))
	}

	// delete
	if err := svc.DeleteSession(ctx, core.DeleteSessionInput{SessionID: created.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetSession(ctx, core.GetSessionInput{SessionID: created.ID}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
}

func TestNotFoundIsSentinel(t *testing.T) {
	ctx := context.Background()
	svc := newTestCore(t)

	if _, err := svc.GetSession(ctx, core.GetSessionInput{SessionID: "tendril-missing"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get missing: want ErrNotFound, got %v", err)
	}
	if err := svc.DeleteSession(ctx, core.DeleteSessionInput{SessionID: "tendril-missing"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("delete missing: want ErrNotFound, got %v", err)
	}
	if _, err := svc.UpdateSessionPreferences(ctx, core.UpdateSessionInput{SessionID: "tendril-missing"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("update missing: want ErrNotFound, got %v", err)
	}
	if _, err := svc.SessionHistory(ctx, core.SessionHistoryInput{SessionID: "tendril-missing"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("history missing: want ErrNotFound, got %v", err)
	}
}

func TestInvokeByName(t *testing.T) {
	ctx := context.Background()
	svc := newTestCore(t)

	// Invoke create through the generic registry path (the MCP/CLI projection).
	result, err := svc.Invoke(ctx, core.CapCreateSession, map[string]any{
		"origin":      session.OriginMCP,
		"preferences": map[string]any{"model": "gpt-4o"},
	})
	if err != nil {
		t.Fatalf("invoke create: %v", err)
	}
	sess, ok := result.(session.Session)
	if !ok {
		t.Fatalf("invoke create returned %T, want session.Session", result)
	}
	if sess.Preferences.Model != "gpt-4o" {
		t.Fatalf("invoke create ignored preferences: %+v", sess.Preferences)
	}

	if _, err := svc.Invoke(ctx, "session.nope", nil); err == nil {
		t.Error("invoke unknown capability: want error, got nil")
	}
}
