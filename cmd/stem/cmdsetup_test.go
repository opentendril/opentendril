package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSubstrateMCPConfigSnippet(t *testing.T) {
	snippet := substrateMCPConfigSnippet()
	server, ok := snippet.MCPServers["opentendril"]
	if !ok {
		t.Fatal("snippet missing opentendril server")
	}
	if server.Command != "tendril" {
		t.Fatalf("command = %q, want tendril", server.Command)
	}
	if len(server.Args) != 1 || server.Args[0] != "mcp" {
		t.Fatalf("args = %v, want [mcp]", server.Args)
	}
	for _, arg := range server.Args {
		if arg == "serve" || arg == "stdio" {
			t.Fatalf("single-user snippet still advertises tendril serve mcp stdio: %v", server.Args)
		}
	}
}

func TestFormatAgentSubstratesYAML(t *testing.T) {
	t.Run("pat uses compact scalar form", func(t *testing.T) {
		out := formatSubstratesYAML(substrateChoices{
			remoteURL: "https://github.com/o/r.git", authMethod: "pat", authEnv: "GITHUB_TOKEN",
		})
		if !strings.Contains(out, `auth: "GITHUB_TOKEN"`) {
			t.Fatalf("expected scalar auth, got:\n%s", out)
		}
		if strings.Contains(out, "method:") || strings.Contains(out, "checkout:") || strings.Contains(out, "sign:") {
			t.Fatalf("pat/ephemeral/no-sign should be minimal, got:\n%s", out)
		}
	})

	t.Run("ssh + managed + signing", func(t *testing.T) {
		out := formatSubstratesYAML(substrateChoices{
			remoteURL: "git@github.com:o/r.git", authMethod: "ssh", authKey: "~/.ssh/id_ot",
			checkoutMode: "managed", signMethod: "ssh", signKey: "~/.ssh/id_ot",
		})
		for _, want := range []string{"method: ssh", `key: "~/.ssh/id_ot"`, "checkout:", "mode: managed", "sign:"} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing %q in:\n%s", want, out)
			}
		}
	})

	t.Run("none auth", func(t *testing.T) {
		out := formatSubstratesYAML(substrateChoices{remoteURL: "https://x/r.git", authMethod: "none"})
		if !strings.Contains(out, "method: none") {
			t.Fatalf("expected method: none, got:\n%s", out)
		}
	})

	t.Run("github app", func(t *testing.T) {
		out := formatSubstratesYAML(substrateChoices{
			remoteURL: "https://github.com/o/r.git", authMethod: "app",
			appID: "4276558", appKeyPath: "~/.tendril/app.pem",
		})
		for _, want := range []string{"method: app", `appId: "4276558"`, `privateKeyPath: "~/.tendril/app.pem"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing %q in:\n%s", want, out)
			}
		}
	})
}

// TestSetupSubstrateCompletionGuidance asserts that the completion text printed
// after a successful substrate setup points the developer toward the direct
// coding workflow rather than Sprout/Sequence dispatch operations.
func TestSetupSubstrateCompletionGuidance(t *testing.T) {
	// Capture stderr: the completion message is written there alongside the
	// MCP snippet (which goes to stdout). Redirect stderr to a pipe so we can
	// inspect it without mixing with test output.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	// Provide minimal stdin input so promptSetupValue never blocks. The
	// defaults are accepted for every field by sending empty lines.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		w.Close()
		os.Stderr = origStderr
		t.Fatalf("os.Pipe (stdin): %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = stdinR

	// Write defaults for: remoteURL, authMethod, (no extra for pat),
	// checkoutMode, (no extra for ephemeral), signMethod, (no extra for none).
	// Send six newlines so every prompt gets its default.
	go func() {
		defer stdinW.Close()
		for i := 0; i < 6; i++ {
			_, _ = io.WriteString(stdinW, "\n")
		}
	}()

	// Override HOME so the function can write substrates.yaml without touching
	// the real home directory.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// runSetupSubstrateCmd reads from os.Stdin and writes guidance to os.Stderr.
	runSetupSubstrateCmd()

	w.Close()
	os.Stderr = origStderr
	os.Stdin = origStdin
	stdinR.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	r.Close()
	got := buf.String()

	// Required: the direct workflow commands must be discoverable.
	for _, want := range []string{
		"tendril serve",
		"tendril chat",
		"default-workspace",
		"--",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("completion guidance missing %q\ngot:\n%s", want, got)
		}
	}

	// Prohibited: stale Sprout/Sequence dispatch references must not appear.
	for _, banned := range []string{
		"sproutGrow",
		"sequenceGrow",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("completion guidance contains stale reference %q\ngot:\n%s", banned, got)
		}
	}
}
