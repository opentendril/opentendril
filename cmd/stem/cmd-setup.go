package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runSetupCmd(args []string) {
	if len(args) == 0 {
		printSetupUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "agent":
		runSetupAgentCmd()
	case "-h", "--help", "help":
		printSetupUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown setup command: %s\n", args[0])
		printSetupUsage()
		os.Exit(1)
	}
}

func runSetupAgentCmd() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintln(os.Stderr, "OpenTendril agent bootstrap")

	choices := agentSubstrateChoices{}

	var err error
	choices.remoteURL, err = promptSetupValue(reader, "Target Git remote URL", "https://github.com/opentendril/core.git")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read remote URL: %v\n", err)
		os.Exit(1)
	}

	authMethod, err := promptSetupValue(reader, "Auth method (pat/ssh/none)", "pat")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read auth method: %v\n", err)
		os.Exit(1)
	}
	switch strings.ToLower(strings.TrimSpace(authMethod)) {
	case "ssh":
		choices.authMethod = "ssh"
		choices.authKey, _ = promptSetupValue(reader, "SSH private key path", "~/.ssh/id_ed25519")
	case "none":
		choices.authMethod = "none"
	default:
		choices.authMethod = "pat"
		choices.authEnv, _ = promptSetupValue(reader, "PAT environment variable name", "GITHUB_TOKEN")
	}

	checkoutMode, _ := promptSetupValue(reader, "Checkout mode (ephemeral/managed/path)", "ephemeral")
	switch strings.ToLower(strings.TrimSpace(checkoutMode)) {
	case "managed":
		choices.checkoutMode = "managed"
	case "path":
		choices.checkoutMode = "path"
		choices.checkoutPath, _ = promptSetupValue(reader, "Checkout directory", "~/ot-workspaces/agent")
	default:
		choices.checkoutMode = "ephemeral"
	}

	signMethod, _ := promptSetupValue(reader, "Commit signing (none/ssh/gpg)", "none")
	switch strings.ToLower(strings.TrimSpace(signMethod)) {
	case "ssh":
		choices.signMethod = "ssh"
		choices.signKey, _ = promptSetupValue(reader, "SSH signing key path", "~/.ssh/id_ed25519")
	case "gpg":
		choices.signMethod = "gpg"
		choices.signKey, _ = promptSetupValue(reader, "GPG signing key id", "")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		fmt.Fprintf(os.Stderr, "Failed to resolve home directory: %v\n", err)
		os.Exit(1)
	}

	tendrilDir := filepath.Join(homeDir, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %v\n", tendrilDir, err)
		os.Exit(1)
	}

	substratesPath := filepath.Join(tendrilDir, "substrates.yaml")
	if err := os.WriteFile(substratesPath, []byte(formatAgentSubstratesYAML(choices)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write %s: %v\n", substratesPath, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Wrote substrate config to %s\n", substratesPath)
	fmt.Fprintln(os.Stderr, "MCP configuration snippet:")

	snippet := agentMCPConfigSnippet()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snippet); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to encode MCP configuration snippet: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "Use the default-agent-workspace substrate when calling sproutTendril or runSequence.")
}

func promptSetupValue(reader *bufio.Reader, label, defaultValue string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s [%s]: ", label, defaultValue)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}

	value := strings.TrimSpace(line)
	if value == "" {
		value = strings.TrimSpace(defaultValue)
	}
	return value, nil
}

// agentSubstrateChoices holds the answers gathered by `tendril setup agent`.
type agentSubstrateChoices struct {
	remoteURL    string
	authMethod   string // pat | ssh | none
	authEnv      string // pat
	authKey      string // ssh
	checkoutMode string // ephemeral | managed | path
	checkoutPath string // path mode
	signMethod   string // "" | ssh | gpg
	signKey      string
}

func formatAgentSubstratesYAML(c agentSubstrateChoices) string {
	var b strings.Builder
	b.WriteString("substrates:\n")
	b.WriteString("  default-agent-workspace:\n")
	fmt.Fprintf(&b, "    url: %q\n", strings.TrimSpace(c.remoteURL))
	b.WriteString("    branch: \"main\"\n")

	switch c.authMethod {
	case "ssh":
		b.WriteString("    auth:\n      method: ssh\n")
		fmt.Fprintf(&b, "      key: %q\n", strings.TrimSpace(c.authKey))
	case "none":
		b.WriteString("    auth:\n      method: none\n")
	default:
		// Scalar form keeps the config compact and back-compatible.
		fmt.Fprintf(&b, "    auth: %q\n", strings.TrimSpace(c.authEnv))
	}

	if mode := strings.TrimSpace(c.checkoutMode); mode != "" && mode != "ephemeral" {
		fmt.Fprintf(&b, "    checkout:\n      mode: %s\n", mode)
		if mode == "path" && strings.TrimSpace(c.checkoutPath) != "" {
			fmt.Fprintf(&b, "      path: %q\n", strings.TrimSpace(c.checkoutPath))
		}
	}

	if method := strings.TrimSpace(c.signMethod); method != "" {
		fmt.Fprintf(&b, "    sign:\n      method: %s\n", method)
		if strings.TrimSpace(c.signKey) != "" {
			fmt.Fprintf(&b, "      key: %q\n", strings.TrimSpace(c.signKey))
		}
	}

	b.WriteString("    provider: docker\n")
	return b.String()
}

type setupMCPConfigSnippet struct {
	MCPServers map[string]setupMCPServer `json:"mcpServers"`
}

type setupMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func agentMCPConfigSnippet() setupMCPConfigSnippet {
	return setupMCPConfigSnippet{
		MCPServers: map[string]setupMCPServer{
			"opentendril": {
				Command: "tendril",
				Args:    []string{"serve", "mcp", "stdio"},
			},
		},
	}
}

func printSetupUsage() {
	fmt.Println("Usage: tendril setup agent")
	fmt.Println("  agent  Bootstrap ~/.tendril/substrates.yaml for a sandboxed agent workspace")
}
