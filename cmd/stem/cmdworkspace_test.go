package main

import (
	"context"
	"strings"
	"testing"
)

func TestParseWorkspaceCommandRequiresExactIdentityAndConfirmation(t *testing.T) {
	if _, err := parseWorkspaceCommandArgs([]string{"inspect", "--pollen", "claude", "--substrate", "core"}); err != nil {
		t.Fatalf("parse inspect: %v", err)
	}
	if _, err := parseWorkspaceCommandArgs([]string{"inspect", "--pollen", " claude", "--substrate", "core"}); err == nil {
		t.Fatal("inspect accepted a Pollen with surrounding whitespace")
	}
	if _, err := parseWorkspaceCommandArgs([]string{"abandon", "--pollen", "claude", "--substrate", "core"}); err == nil {
		t.Fatal("abandon accepted a missing confirmation flag")
	}
	if _, err := parseWorkspaceCommandArgs([]string{"abandon", "--pollen", "claude", "--substrate", "core", "--confirm"}); err != nil {
		t.Fatalf("parse confirmed abandon: %v", err)
	}
}

func TestParseRunWorkspaceCommandsUseExactAllocationIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"list"},
		{"inspect", "--run-id", "allocation-1"},
		{"reconcile", "--run-id", "allocation-1"},
		{"abandon", "--run-id", "allocation-1", "--confirm"},
	} {
		if _, err := parseWorkspaceCommandArgs(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"reconcile"},
		{"inspect", "--run-id", " allocation-1"},
		{"abandon", "--run-id", "allocation-1"},
		{"abandon", "--run-id", "allocation-1", "--confirm", "--substrate", "core"},
		{"list", "--confirm"},
	} {
		if _, err := parseWorkspaceCommandArgs(args); err == nil {
			t.Fatalf("parse %v unexpectedly succeeded", args)
		}
	}
}

func TestWorkspaceCommandRefusesDeclaredPollenBeforeConfigAccess(t *testing.T) {
	t.Setenv(envPollenCLI, "claude")
	err := executeWorkspaceCommand(context.Background(), workspaceCommandOptions{
		operation: "inspect", pollen: "claude", substrate: "missing", dir: "/definitely/not/a/config/path",
	})
	if err == nil || !strings.Contains(err.Error(), "Botanist-only") {
		t.Fatalf("workspace command error = %v, want Botanist posture refusal before config access", err)
	}
}

func TestRunWorkspaceListRefusesDeclaredPollenBeforeHistoryAccess(t *testing.T) {
	t.Setenv(envPollenCLI, "claude")
	err := executeWorkspaceCommand(context.Background(), workspaceCommandOptions{
		operation: "list", dir: "/definitely/not/a/config/path",
	})
	if err == nil || !strings.Contains(err.Error(), "Botanist-only") {
		t.Fatalf("workspace list error = %v, want Botanist posture refusal before state inspection", err)
	}
}
