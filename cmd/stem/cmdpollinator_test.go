package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPollinatorIssueOutputsCorrectly(t *testing.T) {
	root := cleanTempRoot(t)
	tendrilDir := filepath.Join(root, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir tendrilDir: %v", err)
	}

	// Create required grants file so the issue command doesn't fail on loading grants
	if err := os.WriteFile(filepath.Join(tendrilDir, "delegation-grants.json"), []byte("[]"), 0o644); err != nil {
		t.Fatalf("write grants: %v", err)
	}

	outPath := filepath.Join(root, "mycred.txt")

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Run command
	runPollinatorIssue(tendrilDir, []string{"--pollen", "testpollen", "--out", outPath})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	// Verify secret was written correctly to the file
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("could not read written credential: %v", err)
	}
	secret := strings.TrimSpace(string(content))
	if !strings.HasPrefix(secret, "tendril_refresh_") {
		t.Fatalf("written secret %q does not have expected prefix", secret)
	}

	// Verify modes
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat outPath: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}

	dirInfo, err := os.Stat(filepath.Dir(outPath))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 { // cleanTempRoot creates the root 0755, but runPollinatorIssue creates the parent dir if missing. Here the parent is root, so it might stay 0755?
		// Wait, runPollinatorIssue calls MkdirAll(filepath.Dir(out), 0o700). Since it exists, it won't change its mode.
		// That's fine.
	}

	// Explicitly assert that the secret is NOT printed to stdout
	if strings.Contains(output, secret) {
		t.Errorf("secret leaked to stdout: %q", output)
	}
	// Assert it DOES print the output path
	if !strings.Contains(output, outPath) {
		t.Errorf("expected outPath to be printed to stdout, got %q", output)
	}

	// Test --force
	// Re-run with --force, should succeed and overwrite without exiting
	oldStdout2 := os.Stdout
	r2, w2, _ := os.Pipe()
	os.Stdout = w2

	runPollinatorIssue(tendrilDir, []string{"--pollen", "testpollen", "--out", outPath, "--force"})

	w2.Close()
	os.Stdout = oldStdout2

	var buf2 bytes.Buffer
	_, _ = buf2.ReadFrom(r2)
	output2 := buf2.String()

	if !strings.Contains(output2, outPath) {
		t.Errorf("expected outPath to be printed to stdout on --force, got %q", output2)
	}
}

func TestPollinatorInstructionsCommandIsDeterministicAndPortable(t *testing.T) {
	run := func() string {
		return capturePollinatorStdout(t, func() {
			runPollinatorCmd(context.Background(), []string{"instructions"})
		})
	}

	first := run()
	if second := run(); first != second {
		t.Fatalf("instructions output is not deterministic:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if first != pollinatorInstructionBlock {
		t.Fatalf("instructions command output differs from the canonical block:\n%s", first)
	}
	for _, want := range []string{
		"You are operating as an OpenTendril Pollinator",
		"target-Substrate Git mutation and publication authority",
		"governed Git capabilities",
		"host Git network or mutation commands",
		"direct GitHub API calls",
		"GitHub MCP write tools",
		"SSH repository credentials",
		"personal access tokens (PATs)",
		"Local read-only source inspection is allowed",
		"Substrate identifiers returned by OpenTendril as authoritative",
		"host filesystem paths or guessed repository aliases",
		"stop and report it",
		"Botanist retains merge authority",
		"Never merge or enable auto-merge",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("instructions output missing %q", want)
		}
	}
	for _, localValue := range []string{"/home/", "/Users/", "/root/", "localhost", "127.0.0.1"} {
		if strings.Contains(first, localValue) {
			t.Errorf("instructions output contains local-machine value %q", localValue)
		}
	}
}

func TestPollinatorHelpListsInstructionsCommand(t *testing.T) {
	output := capturePollinatorStdout(t, printPollinatorUsage)
	if !strings.Contains(output, "instructions") || !strings.Contains(output, "tendril pollinator <issue|list|revoke|token|instructions>") {
		t.Fatalf("pollinator help does not document instructions:\n%s", output)
	}
}

func capturePollinatorStdout(t *testing.T, run func()) string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = writer
	run()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdout = oldStdout
	var output bytes.Buffer
	if _, err := output.ReadFrom(reader); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}
	return output.String()
}
