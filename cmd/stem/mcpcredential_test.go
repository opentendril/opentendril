package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var binaryPath string

func TestMain(m *testing.M) {
	// Build the stem binary for end-to-end tests of the CLI/stdio
	tempDir, err := os.MkdirTemp("", "tendril-mcp-test")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	binaryPath = filepath.Join(tempDir, "tendril")
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/stem")
	// Build from the package root
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build tendril: %v\n%s\n", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// runTendrilMCP runs the compiled tendril binary with the "mcp" subcommand
// and the given environment variable map. It returns stdout, stderr and exit code.
// It also feeds EOF to stdin to ensure it shuts down if it started successfully.
func runTendrilMCP(t *testing.T, env map[string]string) (string, string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "mcp")

	cmdEnv := os.Environ()
	for k, v := range env {
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = cmdEnv

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Create a pipe for stdin and close it immediately to simulate EOF
	// so the scanner.Scan() loop breaks and the program exits cleanly.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("failed to create stdin pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	// Close stdin immediately.
	stdin.Close()

	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			// e.g. context timeout
			t.Fatalf("wait error: %v, stderr: %s", err, stderr.String())
		}
	}

	return stdout.String(), stderr.String(), exitCode
}

func TestMCPCredential_Unconfigured(t *testing.T) {
	// "With no credential configured, startup is unchanged. Compare the actual
	// emitted startup sequence against today's, not merely that the process did not error."
	_, stderrBaseline, codeBaseline := runTendrilMCP(t, nil)
	if codeBaseline != 0 {
		t.Fatalf("expected baseline to exit 0 on EOF, got %d. stderr: %s", codeBaseline, stderrBaseline)
	}

	_, stderrWithEmpty, codeWithEmpty := runTendrilMCP(t, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": "",
	})
	if codeWithEmpty != 0 {
		t.Fatalf("expected empty credential to exit 0 on EOF, got %d", codeWithEmpty)
	}

	sanitize2 := func(s string) string {
		importRegexp := regexp.MustCompile(`tendril-[a-f0-9]{24,}`)
		s = importRegexp.ReplaceAllString(s, "tendril-<ID>")
		timeRegexp := regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)
		s = timeRegexp.ReplaceAllString(s, "<TIME>")
		return s
	}

	if sanitize2(stderrBaseline) != sanitize2(stderrWithEmpty) {
		t.Fatalf("Startup sequence changed with empty credential.\nWant:\n%s\nGot:\n%s", sanitize2(stderrBaseline), sanitize2(stderrWithEmpty))
	}
}

func TestMCPCredential_MissingFile(t *testing.T) {
	tempDir := t.TempDir()
	missingPath := filepath.Join(tempDir, "does-not-exist")

	_, stderr, code := runTendrilMCP(t, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": missingPath,
	})

	if code == 0 {
		t.Fatalf("expected missing file to cause an error, but it succeeded")
	}

	if !strings.Contains(stderr, "does-not-exist") {
		t.Errorf("expected error to name the missing file. stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "no such file or directory") {
		t.Errorf("expected error to mention 'no such file'. stderr: %s", stderr)
	}
}

func TestMCPCredential_ModeCheck(t *testing.T) {
	tempDir := t.TempDir()
	credPath := filepath.Join(tempDir, "cred")

	secret := "tendril_mysecret"
	if err := os.WriteFile(credPath, []byte(secret), 0644); err != nil {
		t.Fatal(err)
	}

	// 0644 should be refused
	_, stderr644, code644 := runTendrilMCP(t, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": credPath,
	})
	if code644 == 0 {
		t.Fatalf("expected 0644 file to fail but it succeeded")
	}
	if !strings.Contains(stderr644, "too permissive") || !strings.Contains(stderr644, "0600") {
		t.Errorf("expected error to name the required mode. stderr: %s", stderr644)
	}

	// 0600 should succeed
	if err := os.Chmod(credPath, 0600); err != nil {
		t.Fatal(err)
	}
	stdout600, stderr600, code600 := runTendrilMCP(t, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": credPath,
	})
	if code600 != 0 {
		t.Fatalf("expected 0600 file to succeed, got %d. stderr: %s", code600, stderr600)
	}

	// The secret must never reach output streams
	if strings.Contains(stdout600, secret) {
		t.Errorf("secret leaked into stdout")
	}
	if strings.Contains(stderr600, secret) {
		t.Errorf("secret leaked into stderr")
	}
}

func TestMCPCredential_SecretLeakOnCorruptFile(t *testing.T) {
	// Make sure we test that an error in reading or something doesn't leak the secret.
	// e.g. if the secret doesn't have the tendril_ prefix, it's not checked yet in slice 1.
}
