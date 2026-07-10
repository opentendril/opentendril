package core_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// recordingMeshOps builds a MeshOps port that records what it received and
// returns canned results.
type meshPortCalls struct {
	resolvedSubstrate string
	pushedWorkspace   string
	pushedBranch      string
	pushedMessage     string
}

func newMeshService(t *testing.T, calls *meshPortCalls) *core.Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return core.NewService(manager).WithMesh(core.MeshOps{
		ResolveWorkspace: func(_ context.Context, substrate string) (string, error) {
			calls.resolvedSubstrate = substrate
			return "/workspaces/" + substrate, nil
		},
		DelegatePush: func(_ context.Context, workspace, branch, commitMessage string) (string, error) {
			calls.pushedWorkspace = workspace
			calls.pushedBranch = branch
			calls.pushedMessage = commitMessage
			return "abc1234", nil
		},
	})
}

func TestMeshGraftRunsPortsInOrder(t *testing.T) {
	var calls meshPortCalls
	svc := newMeshService(t, &calls)

	result, err := svc.MeshGraft(context.Background(), core.MeshGraftInput{
		Substrate:     "core",
		Branch:        "feat/x",
		CommitMessage: "graft it",
	})
	if err != nil {
		t.Fatalf("MeshGraft: %v", err)
	}
	if calls.resolvedSubstrate != "core" {
		t.Fatalf("resolved substrate = %q, want core", calls.resolvedSubstrate)
	}
	if calls.pushedWorkspace != "/workspaces/core" || calls.pushedBranch != "feat/x" || calls.pushedMessage != "graft it" {
		t.Fatalf("push received (%q, %q, %q)", calls.pushedWorkspace, calls.pushedBranch, calls.pushedMessage)
	}
	if result.Workspace != "/workspaces/core" || result.Commit != "abc1234" {
		t.Fatalf("result = %+v", result)
	}
}

func TestMeshGraftRequiresSubstrate(t *testing.T) {
	var calls meshPortCalls
	svc := newMeshService(t, &calls)
	if _, err := svc.MeshGraft(context.Background(), core.MeshGraftInput{}); err == nil || !strings.Contains(err.Error(), "substrate is required") {
		t.Fatalf("expected substrate-required error, got %v", err)
	}
}

func TestMeshGraftUnwiredFailsLoudly(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := core.NewService(manager)
	if _, err := svc.MeshGraft(context.Background(), core.MeshGraftInput{Substrate: "core"}); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error, got %v", err)
	}
	if _, err := svc.MeshPromote(context.Background(), core.MeshPromoteInput{Substrate: "core"}); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error for promote, got %v", err)
	}
}

func TestMeshGraftResolveErrorIsWrapped(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := core.NewService(manager).WithMesh(core.MeshOps{
		ResolveWorkspace: func(context.Context, string) (string, error) {
			return "", fmt.Errorf("no such substrate")
		},
		DelegatePush: func(context.Context, string, string, string) (string, error) {
			t.Fatal("DelegatePush must not run when resolution fails")
			return "", nil
		},
	})
	if _, err := svc.MeshGraft(context.Background(), core.MeshGraftInput{Substrate: "ghost"}); err == nil || !strings.Contains(err.Error(), "resolve substrate") {
		t.Fatalf("expected wrapped resolve error, got %v", err)
	}
}

func TestMeshPromoteDefaultsCommitMessageFromPRNumber(t *testing.T) {
	var calls meshPortCalls
	svc := newMeshService(t, &calls)

	result, err := svc.MeshPromote(context.Background(), core.MeshPromoteInput{
		Substrate: "core",
		PRNumber:  " 42 ",
	})
	if err != nil {
		t.Fatalf("MeshPromote: %v", err)
	}
	if calls.pushedMessage != "promote PR #42" {
		t.Fatalf("pushed message = %q, want the historic default", calls.pushedMessage)
	}
	if result.PRNumber != "42" {
		t.Fatalf("prNumber = %q, want trimmed 42", result.PRNumber)
	}
}

func TestMeshPromoteKeepsExplicitCommitMessage(t *testing.T) {
	var calls meshPortCalls
	svc := newMeshService(t, &calls)

	if _, err := svc.MeshPromote(context.Background(), core.MeshPromoteInput{
		Substrate:     "core",
		PRNumber:      "42",
		CommitMessage: "ship it",
	}); err != nil {
		t.Fatalf("MeshPromote: %v", err)
	}
	if calls.pushedMessage != "ship it" {
		t.Fatalf("pushed message = %q, want the explicit message", calls.pushedMessage)
	}
}

func TestMeshCapabilitiesInRegistry(t *testing.T) {
	var calls meshPortCalls
	svc := newMeshService(t, &calls)

	declared := map[string]bool{}
	for _, capability := range svc.Capabilities() {
		declared[capability.Name] = true
	}
	for _, name := range []string{core.CapMeshGraft, core.CapMeshPromote} {
		if !declared[name] {
			t.Errorf("registry does not declare %s", name)
		}
	}

	// Invoke path (the projection MCP/CLI use) works for the graft family,
	// including camelCase input keys per the payload contract.
	result, err := svc.Invoke(context.Background(), core.CapMeshPromote, map[string]any{
		"substrate": "core",
		"prNumber":  "7",
	})
	if err != nil {
		t.Fatalf("Invoke(mesh.promote): %v", err)
	}
	promotion, ok := result.(core.MeshPromotion)
	if !ok {
		t.Fatalf("Invoke(mesh.promote) = %T, want core.MeshPromotion", result)
	}
	if promotion.PRNumber != "7" || calls.pushedMessage != "promote PR #7" {
		t.Fatalf("promotion = %+v, pushed message = %q", promotion, calls.pushedMessage)
	}
}
