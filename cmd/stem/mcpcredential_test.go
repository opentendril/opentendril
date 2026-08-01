package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	binaryPath = filepath.Join(tempDir, "tendril")
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/stem")
	// Build from the package root
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build tendril: %v\n%s\n", err, out)
		os.RemoveAll(tempDir)
		os.Exit(1)
	}

	exitCode := m.Run()
	os.RemoveAll(tempDir)
	os.Exit(exitCode)
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
	_, stderrWithEmpty, codeWithEmpty := runTendrilMCP(t, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": "",
		"TERROIR_HOST":           "127.0.0.1",
		"PORT":                   "65534",
	})
	if codeWithEmpty != 0 {
		t.Fatalf("expected empty credential to exit 0 on EOF, got %d", codeWithEmpty)
	}

	sanitize := func(s string) string {
		importRegexp := regexp.MustCompile(`tendril-[a-f0-9]{24,}`)
		s = importRegexp.ReplaceAllString(s, "tendril-<ID>")
		timeRegexp := regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)
		s = timeRegexp.ReplaceAllString(s, "<TIME>")
		// The `Loaded substrates config` line is generated dynamically, so filter it out to
		// make the golden assertion completely deterministic against the core start lines.
		lines := strings.Split(s, "\n")
		var out []string
		for _, line := range lines {
			if !strings.Contains(line, "Loaded substrates config") && line != "" {
				out = append(out, strings.TrimSpace(line))
			}
		}
		return strings.Join(out, "\n")
	}

	got := sanitize(stderrWithEmpty)
	want := strings.Join([]string{
		"🚀 OpenTendril MCP Stdio Server initializing...",
		"🪴 MCP interactions bound to Tendril session tendril-<ID>",
		"🔏 No delegation grants configured: every delegated invocation is denied (secure default)",
		"🔏 No Pollen bound (TENDRIL_POLLEN is unset): delegated capabilities are denied over MCP (deny-closed)",
		"🟢 OpenTendril MCP Server ready. Listening on stdio.",
		"🛑 OpenTendril MCP Stdio Server exiting.",
	}, "\n")

	if got != want {
		t.Fatalf("Startup sequence changed with empty credential.\nWant:\n%s\nGot:\n%s", want, got)
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

	secret := "tendril_root_mysecret"
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

	// 0620 should be refused (group writable)
	if err := os.Chmod(credPath, 0620); err != nil {
		t.Fatal(err)
	}
	_, stderr620, code620 := runTendrilMCP(t, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": credPath,
	})
	if code620 == 0 {
		t.Fatalf("expected 0620 file to fail but it succeeded")
	}
	if !strings.Contains(stderr620, "too permissive") || !strings.Contains(stderr620, "0600") {
		t.Errorf("expected error to name the required mode. stderr: %s", stderr620)
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

func TestMCPCredential_RefusalOutput(t *testing.T) {
	otherUID := os.Getuid() + 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report := struct {
			Owner *int `json:"owner,omitempty"`
		}{
			Owner: &otherUID,
		}
		json.NewEncoder(w).Encode(report)
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	hostPart, portPart := "", ""
	parts := strings.Split(host, ":")
	if len(parts) == 2 {
		hostPart = parts[0]
		portPart = parts[1]
	}

	_, stderr, code := runTendrilMCP(t, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": "",
		"TERROIR_HOST":           hostPart,
		"PORT":                   portPart,
	})

	if code == 0 {
		t.Fatalf("expected refusal to exit non-zero, got 0")
	}

	if strings.Contains(stderr, "OpenTendril MCP Server ready") {
		t.Fatalf("startup sequence printed readiness line before refusing. stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Stem is owned by another user") {
		t.Fatalf("expected refusal message in stderr, got:\n%s", stderr)
	}
}
