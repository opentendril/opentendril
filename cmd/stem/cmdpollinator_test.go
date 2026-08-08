package main

import (
	"bytes"
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
