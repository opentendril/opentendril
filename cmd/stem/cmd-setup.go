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

	remoteURL, err := promptSetupValue(reader, "Target Git remote URL", "https://github.com/opentendril/core.git")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read remote URL: %v\n", err)
		os.Exit(1)
	}

	authEnv, err := promptSetupValue(reader, "PAT environment variable name", "GITHUB_TOKEN")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read PAT environment variable name: %v\n", err)
		os.Exit(1)
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
	if err := os.WriteFile(substratesPath, []byte(formatAgentSubstratesYAML(remoteURL, authEnv)), 0o644); err != nil {
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

func formatAgentSubstratesYAML(remoteURL, authEnv string) string {
	return fmt.Sprintf(`substrates:
  default-agent-workspace:
    url: %q
    branch: "main"
    auth: %q
    provider: docker
`, strings.TrimSpace(remoteURL), strings.TrimSpace(authEnv))
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
