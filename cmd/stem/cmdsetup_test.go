package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/substrateconfig"
	"gopkg.in/yaml.v3"
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

	canonical := filepath.Join(tmpHome, ".tendril", "substrates.yaml")
	persisted, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read compatibility registry: %v", err)
	}
	var config conductor.SubstratesConfig
	if err := yaml.Unmarshal(persisted, &config); err != nil {
		t.Fatalf("decode compatibility registry: %v", err)
	}
	spec, ok := config.Substrates["default-workspace"]
	if !ok {
		t.Fatalf("compatibility registry has no default-workspace: %s", persisted)
	}
	if spec.URL != "https://github.com/opentendril/opentendril.git" || spec.Provider != "docker" {
		t.Fatalf("default-workspace = %+v, want default URL and docker provider", spec)
	}
	if _, err := os.Stat(filepath.Join(tmpHome, ".tendril", "grants.yaml")); !os.IsNotExist(err) {
		t.Fatalf("compatibility setup created delegation grants: %v", err)
	}
}

func TestExecuteSetupSubstrateRefusesDeclaredPollenBeforePromptOrMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envPollenCLI, "worker")

	_, stderr := captureVerifyOutput(t, func() {
		err := executeSetupSubstrate()
		if err == nil || !strings.Contains(err.Error(), "Botanist-only") {
			t.Fatalf("error = %v, want Botanist-only refusal", err)
		}
	})
	if stderr != "" {
		t.Fatalf("declared Pollen setup substrate prompted or wrote output before refusal: %q", stderr)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".tendril", "substrates.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("declared Pollen setup substrate created canonical registry: %v", statErr)
	}
}

func TestCompatibilitySetupMutationPreservesUnrelatedEntriesAndRefusesOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	canonical := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	original := []byte("# keep this comment\ncredentials:\n  unrelated:\n    auth: GITHUB_TOKEN\nsubstrates:\n  unrelated:\n    url: https://example.com/unrelated.git\n")
	if err := os.WriteFile(canonical, original, 0o644); err != nil {
		t.Fatalf("write canonical registry: %v", err)
	}

	choices := substrateChoices{remoteURL: "https://example.com/default.git", authMethod: "none", checkoutMode: "ephemeral"}
	result, err := substrateconfig.MutateCanonical(func(root *yaml.Node) error {
		return addCompatibilitySubstrate(root, choices)
	})
	if err != nil {
		t.Fatalf("compatibility setup mutation: %v", err)
	}
	if result.Destination != canonical {
		t.Fatalf("destination = %q, want %q", result.Destination, canonical)
	}
	updated, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read updated canonical registry: %v", err)
	}
	for _, want := range []string{"keep this comment", "unrelated", "default-workspace"} {
		if !strings.Contains(string(updated), want) {
			t.Fatalf("updated registry missing %q:\n%s", want, updated)
		}
	}

	beforeRefusal := append([]byte(nil), updated...)
	if _, err := substrateconfig.MutateCanonical(func(root *yaml.Node) error {
		return addCompatibilitySubstrate(root, choices)
	}); err == nil {
		t.Fatal("compatibility setup overwrote an existing default-workspace")
	}
	afterRefusal, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read canonical registry after refusal: %v", err)
	}
	if string(afterRefusal) != string(beforeRefusal) {
		t.Fatalf("refused compatibility setup changed canonical registry:\n%s", afterRefusal)
	}
	if _, err := os.Stat(filepath.Join(home, ".tendril", "grants.yaml")); !os.IsNotExist(err) {
		t.Fatalf("compatibility setup created delegation grants: %v", err)
	}
}
