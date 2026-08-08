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
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// runTendrilMCPIn runs the compiled binary from a chosen working directory.
//
// It reuses binaryPath built in TestMain rather than building again. The
// existing runTendrilMCP helper cannot be used here because these tests turn on
// what is present in the working directory, which it does not control.
func runTendrilMCPIn(t *testing.T, dir string, env map[string]string) (string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "mcp")
	cmd.Dir = dir

	cmdEnv := os.Environ()
	for k, v := range env {
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = cmdEnv

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// EOF immediately, so a server that started successfully shuts down.
	stdin.Close()

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("wait: %v\nstderr:\n%s", err, stderr.String())
		}
		exitCode = exitErr.ExitCode()
	}
	return stderr.String(), exitCode
}

// governedStemAt stands up a health surface owned by a different principal, so
// the surface under test selects forwarding rather than in-process.
func governedStemAt(t *testing.T) (host, port string) {
	t.Helper()

	otherUID := os.Getuid() + 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(struct {
			Owner *int `json:"owner,omitempty"`
		}{Owner: &otherUID})
	}))
	t.Cleanup(server.Close)

	parts := strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")
	if len(parts) != 2 {
		t.Fatalf("unexpected server URL %q", server.URL)
	}
	return parts[0], parts[1]
}

// credentialFile writes a durable root at the mode the surface requires.
func credentialFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte("tendril_refresh_forwardplanetest"), 0o600); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	return path
}

// corruptPollinatorStore plants an unreadable credential store in the working
// directory's control plane.
func corruptPollinatorStore(t *testing.T, dir string) {
	t.Helper()
	planeDir := filepath.Join(dir, ".tendril")
	if err := os.MkdirAll(planeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(planeDir, core.PollinatorCredentialsFilename)
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
}

// The availability defect. In forwarding mode the local Pollinator store is
// never consulted, so a malformed one in whatever directory the caller started
// from must not stop the session. Before this change it exited 1.
func TestForwardingSurvivesMalformedLocalCredentialStore(t *testing.T) {
	dir := t.TempDir()
	corruptPollinatorStore(t, dir)
	host, port := governedStemAt(t)

	stderr, code := runTendrilMCPIn(t, dir, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": credentialFile(t, dir),
		"TERROIR_HOST":           host,
		"PORT":                   port,
	})

	if code != 0 {
		t.Fatalf("forwarding mode exited %d because of a store it does not read\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "OpenTendril MCP Server ready") {
		t.Fatalf("forwarding mode never became ready\nstderr:\n%s", stderr)
	}
}

// The paired control. The same malformed store IS fatal in-process, where the
// store is genuinely consulted — degrading to "no credentials" would return
// every caller to the weaker declared-Pollen path. Without this test, deleting
// the fatal check entirely would still pass the test above.
func TestInProcessStillRefusesMalformedLocalCredentialStore(t *testing.T) {
	dir := t.TempDir()
	corruptPollinatorStore(t, dir)

	stderr, code := runTendrilMCPIn(t, dir, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": "",
		"TENDRIL_MCP_IN_PROCESS": "1",
	})

	if code == 0 {
		t.Fatalf("in-process mode accepted a malformed credential store\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Pollinator credentials could not be read") {
		t.Fatalf("in-process refusal did not name the credential store\nstderr:\n%s", stderr)
	}
}

// Forwarding mode must not describe a control plane it did not build. These are
// absence assertions: asserting the replacement line is present does not test
// that the misleading ones are gone, so both are needed.
func TestForwardingAnnouncesNoLocalControlPlane(t *testing.T) {
	dir := t.TempDir()
	host, port := governedStemAt(t)

	stderr, code := runTendrilMCPIn(t, dir, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": credentialFile(t, dir),
		"TERROIR_HOST":           host,
		"PORT":                   port,
	})
	if code != 0 {
		t.Fatalf("forwarding mode exited %d\nstderr:\n%s", code, stderr)
	}

	// Each of these describes the in-process control plane. In forwarding mode
	// none of them governs a single frame.
	for _, inert := range []string{
		"Delegation enabled:",
		"No delegation grants configured",
		"Pollinator credential(s) loaded",
		"bound from TENDRIL_POLLEN",
		"No Pollen bound",
	} {
		if strings.Contains(stderr, inert) {
			t.Errorf("forwarding mode announced %q, which governs nothing on this connection\nstderr:\n%s", inert, stderr)
		}
	}

	if !strings.Contains(stderr, "Delegation is governed by the Stem at") {
		t.Errorf("forwarding mode did not name the Stem that actually governs\nstderr:\n%s", stderr)
	}
}

// In-process mode is unchanged. Every line the forwarding test requires to be
// absent must still be present here, or the fix removed them everywhere rather
// than only where they were false.
func TestInProcessStillAnnouncesItsControlPlane(t *testing.T) {
	dir := t.TempDir()

	stderr, code := runTendrilMCPIn(t, dir, map[string]string{
		"TENDRIL_MCP_CREDENTIAL": "",
		"TENDRIL_MCP_IN_PROCESS": "1",
	})
	if code != 0 {
		t.Fatalf("in-process mode exited %d\nstderr:\n%s", code, stderr)
	}

	for _, expected := range []string{
		"No delegation grants configured",
		"No Pollen bound",
		"OpenTendril MCP Server ready",
	} {
		if !strings.Contains(stderr, expected) {
			t.Errorf("in-process startup no longer reports %q\nstderr:\n%s", expected, stderr)
		}
	}

	if strings.Contains(stderr, "Delegation is governed by the Stem at") {
		t.Errorf("in-process mode claimed another Stem governs it\nstderr:\n%s", stderr)
	}
}
