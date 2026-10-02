package core

import (
	"context"
	"testing"
)

func TestDelegatedWorkspaceControlPlaneRejectsPollinatorAndRequiresConfirmation(t *testing.T) {
	called := 0
	service := NewService(nil).WithDelegatedWorkspace(DelegatedWorkspaceOperations{
		Inspect: func(_ context.Context, _ DelegatedWorkspaceTargetSpec) (DelegatedWorkspaceReport, error) {
			called++
			return DelegatedWorkspaceReport{Pollen: "claude"}, nil
		},
		Abandon: func(_ context.Context, _ DelegatedWorkspaceTargetSpec, _ bool) (DelegatedWorkspaceAbandonResult, error) {
			called++
			return DelegatedWorkspaceAbandonResult{WorktreeRemoved: true}, nil
		},
	})
	input := DelegatedWorkspaceInput{Pollen: "claude", Substrate: "core"}
	if _, err := service.InspectDelegatedWorkspace(WithPollen(context.Background(), "claude"), input); err == nil {
		t.Fatal("Pollinator inspection was accepted")
	}
	if _, err := service.AbandonDelegatedWorkspace(WithPollen(context.Background(), "claude"), DelegatedWorkspaceAbandonInput{
		Pollen: "claude", Substrate: "core", Confirm: true,
	}); err == nil {
		t.Fatal("Pollinator abandonment was accepted")
	}
	if _, err := service.AbandonDelegatedWorkspace(context.Background(), DelegatedWorkspaceAbandonInput{
		Pollen: "claude", Substrate: "core",
	}); err == nil {
		t.Fatal("abandonment without explicit confirmation was accepted")
	}
	if called != 0 {
		t.Fatalf("control-plane port was called %d times for rejected requests", called)
	}

	if _, err := service.InspectDelegatedWorkspace(context.Background(), input); err != nil {
		t.Fatalf("Botanist inspection was refused: %v", err)
	}
	if called != 1 {
		t.Fatalf("Botanist inspect calls = %d, want one", called)
	}
	for _, capability := range service.Capabilities() {
		if capability.Name == "workspace.inspect" || capability.Name == "workspace.abandon" {
			t.Fatalf("Botanist control-plane operation leaked into governed registry: %s", capability.Name)
		}
	}
}
