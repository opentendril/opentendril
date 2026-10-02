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

func TestWorkspaceCommandRefusesDeclaredPollenBeforeConfigAccess(t *testing.T) {
	t.Setenv(envPollenCLI, "claude")
	err := executeWorkspaceCommand(context.Background(), workspaceCommandOptions{
		operation: "inspect", pollen: "claude", substrate: "missing", dir: "/definitely/not/a/config/path",
	})
	if err == nil || !strings.Contains(err.Error(), "Botanist-only") {
		t.Fatalf("workspace command error = %v, want Botanist posture refusal before config access", err)
	}
}
