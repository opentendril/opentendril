package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runInitCmd(args []string) {
	fmt.Println("========================================")
	fmt.Println("🌱 Welcome to OpenTendril Setup Wizard! ")
	fmt.Println("========================================")

	scanner := bufio.NewScanner(os.Stdin)

	// Step 1: Detect Ollama
	fmt.Println("\n🔍 Scanning for local LLM providers...")
	ollamaModels := getOllamaModels()

	defaultProvider := "anthropic"
	localModel := ""
	inferenceURL := ""

	if len(ollamaModels) > 0 {
		fmt.Printf("✅ Detected local Ollama with %d model(s):\n", len(ollamaModels))
		for i, m := range ollamaModels {
			fmt.Printf("  %d) %s\n", i+1, m)
		}
		fmt.Println("Would you like to use Ollama for local, private execution? (y/n)")
		fmt.Print("> ")
		if scanner.Scan() {
			ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
			if ans == "y" || ans == "yes" {
				defaultProvider = "local"
				inferenceURL = defaultLocalInferenceURL()
				localModel = selectOllamaModel(ollamaModels)
				// Let user override model choice
				fmt.Printf("Auto-selected model: %s\n", localModel)
				fmt.Printf("Press Enter to use it, or type a different model name: ")
				if scanner.Scan() {
					if override := strings.TrimSpace(scanner.Text()); override != "" {
						localModel = override
					}
				}
				if !containsModel(ollamaModels, localModel) {
					fmt.Printf("⚠️  Model %q is not in Ollama's local model list.\n", localModel)
					fmt.Printf("   Pull it first with: ollama pull %s\n", localModel)
				}
				fmt.Printf("✅ Using Ollama model: %s\n", localModel)
			}
		}
	} else {
		fmt.Println("ℹ️  No local Ollama instance detected at localhost:11434.")
		fmt.Println("   Start Ollama with: ollama serve")
	}

	// If they didn't choose local, let's ask about cloud
	cloudEnvKey := ""
	cloudEnvValue := ""
	if defaultProvider != "local" {
		fmt.Println("\n☁️  Cloud Provider Selection")
		fmt.Println("Which cloud provider would you like to use?")
		fmt.Println("1) Anthropic (Claude 3.5 Sonnet/Opus)")
		fmt.Println("2) OpenAI (GPT-4o)")
		fmt.Println("3) xAI (Grok)")
		fmt.Println("4) Google (Gemini)")
		fmt.Print("Enter number (1-4): ")

		if scanner.Scan() {
			choice := strings.TrimSpace(scanner.Text())
			switch choice {
			case "1":
				defaultProvider = "anthropic"
				cloudEnvKey = "ANTHROPIC_API_KEY"
			case "2":
				defaultProvider = "openai"
				cloudEnvKey = "OPENAI_API_KEY"
			case "3":
				defaultProvider = "grok"
				cloudEnvKey = "GROK_API_KEY"
			case "4":
				defaultProvider = "google"
				cloudEnvKey = "GOOGLE_API_KEY"
			default:
				defaultProvider = "anthropic"
				cloudEnvKey = "ANTHROPIC_API_KEY"
				fmt.Println("Invalid choice, defaulting to anthropic.")
			}
			if cloudEnvKey != "" && cloudEnvValue == "" {
				fmt.Printf("Enter %s: ", cloudEnvKey)
				if scanner.Scan() {
					cloudEnvValue = strings.TrimSpace(scanner.Text())
				}
			}
		}
	}

	// Step 2: GitHub PAT (needed for terrarium git clones/pushes over HTTPS)
	githubToken := promptGitHubToken(scanner)

	// Step 3: Write Configuration
	fmt.Println("\n💾 Saving Configuration...")

	// Determine paths. User-global tendril config lives under ~/.tendril,
	// consistent with `tendril setup` and the workspace-local .tendril/ dir.
	homeDir, _ := os.UserHomeDir()
	configDir := filepath.Join(homeDir, ".tendril")
	os.MkdirAll(configDir, 0755)

	envPath := filepath.Join(configDir, ".env")

	// Try to write to current directory's core/.env if it exists (for dev mode)
	if _, err := os.Stat("core/.env"); err == nil {
		envPath = "core/.env"
	} else if _, err := os.Stat(".env"); err == nil {
		envPath = ".env"
	}

	keys := []string{"DEFAULT_LLM_PROVIDER"}
	values := map[string]string{"DEFAULT_LLM_PROVIDER": defaultProvider}
	if defaultProvider == "local" {
		keys = append(keys, "LOCAL_INFERENCE_URL", "LOCAL_MODEL_NAME")
		values["LOCAL_INFERENCE_URL"] = inferenceURL
		values["LOCAL_MODEL_NAME"] = localModel
	}
	if cloudEnvKey != "" && cloudEnvValue != "" {
		keys = append(keys, cloudEnvKey)
		values[cloudEnvKey] = cloudEnvValue
	}
	if githubToken != "" {
		keys = append(keys, "GITHUB_TOKEN")
		values["GITHUB_TOKEN"] = githubToken
	}

	if err := upsertEnvFile(envPath, keys, values); err != nil {
		fmt.Printf("❌ Failed to write .env file: %v\n", err)
	} else {
		fmt.Printf("✅ Saved to %s\n", envPath)
	}

	// Step 4: Offer a minimal substrates.yaml when none exists yet
	maybeScaffoldSubstrates(scanner)

	// Step 5: Print next steps
	homeBin := filepath.Join(homeDir, ".local", "bin", "tendril")
	fmt.Println()
	fmt.Println("════════════════════════════════════════")
	fmt.Println("  🎉 OpenTendril Setup Complete!")
	fmt.Println("════════════════════════════════════════")
	if defaultProvider == "local" {
		fmt.Printf("  Provider : Ollama (local, private)\n")
		fmt.Printf("  Model    : %s\n", localModel)
		fmt.Printf("  URL      : %s\n", inferenceURL)
	} else {
		fmt.Printf("  Provider : %s\n", defaultProvider)
	}
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("  1. Start the Stem server:   tendril serve")
	fmt.Println("  2. Chat in a new terminal:  tendril chat")
	fmt.Println()
	fmt.Println("  Model Context Protocol integration:")
	fmt.Println("    claude mcp add opentendril -- tendril mcp")
	fmt.Println("  Claude Desktop — add to claude_desktop_config.json")
	fmt.Println("  (Mac: ~/Library/Application Support/Claude/, Linux: ~/.config/Claude/):")
	fmt.Printf("    {\"mcpServers\": {\"opentendril\": {\"command\": \"%s\", \"args\": [\"mcp\"]}}}\n", homeBin)
	fmt.Println()
	fmt.Println("  ⚠️  That runs the Stem AS WHOEVER LAUNCHES IT, in their own directory.")
	fmt.Println("      Correct for a single-user install. If you are running the Stem under")
	fmt.Println("      its own principal, it bypasses that boundary — see docs/GUIDE-INSTALL.md")
	fmt.Println("      for the surface a credential-bearing Pollinator uses instead.")
	fmt.Println("════════════════════════════════════════")

	go func() {
		cmd := exec.Command("docker", "pull", "opentendril-typescript:latest")
		_ = cmd.Start()
	}()
}

// defaultLocalInferenceURL picks the Ollama endpoint for the environment the
// Stem runs in: host.docker.internal when running inside a container,
// localhost for host runs. The LLM client also falls back across
// host.docker.internal/localhost/127.0.0.1 at request time.
func defaultLocalInferenceURL() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "http://host.docker.internal:11434/v1"
	}
	return "http://localhost:11434/v1"
}

func containsModel(models []string, name string) bool {
	for _, m := range models {
		if m == name {
			return true
		}
	}
	return false
}

// promptGitHubToken offers to capture a GitHub PAT for terrarium git
// operations. Returns "" when skipped or already present in the environment.
func promptGitHubToken(scanner *bufio.Scanner) string {
	fmt.Println("\n🔑 GitHub Access (optional)")
	if os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN") != "" {
		fmt.Println("✅ A GitHub token is already set in your environment. Skipping.")
		return ""
	}
	fmt.Println("Terrariums need a GitHub PAT in the environment to clone and push")
	fmt.Println("over HTTPS. Tip: `gh auth token` prints one, and .envrc.example")
	fmt.Println("shows how to load it automatically with direnv.")
	fmt.Print("Paste a GITHUB_TOKEN (press Enter to skip): ")
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

// maybeScaffoldSubstrates offers to write a minimal substrates.yaml when the
// standard locations have none.
func maybeScaffoldSubstrates(scanner *bufio.Scanner) {
	for _, candidate := range []string{"substrates.yaml", filepath.Join(".tendril", "substrates.yaml")} {
		if _, err := os.Stat(candidate); err == nil {
			return
		}
	}

	fmt.Println("\n📦 No substrates.yaml found.")
	fmt.Println("A substrates.yaml names the repositories Sprouts may work on.")
	fmt.Print("Scaffold a minimal one for the current directory? (y/n): ")
	if !scanner.Scan() {
		return
	}
	ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if ans != "y" && ans != "yes" {
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("⚠️  Could not resolve current directory: %v\n", err)
		return
	}

	// A path substrate is mounted into the Terrarium. Pointing one at the
	// directory holding the control plane would mount grants, credentials and
	// the issued-credential store into a Sprout — the one thing the trust model
	// forbids. Refuse rather than write it.
	if _, err := os.Stat(filepath.Join(cwd, ".tendril")); err == nil {
		fmt.Println("⚠️  Not scaffolding: this directory holds the control plane (.tendril).")
		fmt.Println("    A path substrate here would mount credentials and grants into a Sprout.")
		fmt.Println("    Name a repository instead:  tendril git setup --substrate <name> --repo <owner/repo>")
		return
	}

	content := fmt.Sprintf(`substrates:
  workspace:
    path: %s
    branch: main
    auth: GITHUB_TOKEN
    readonly: false
`, cwd)

	if err := os.WriteFile("substrates.yaml", []byte(content), 0o644); err != nil {
		fmt.Printf("⚠️  Failed to write substrates.yaml: %v\n", err)
		return
	}
	fmt.Println("✅ Wrote substrates.yaml (edit it to add remote url/branch entries).")
}

// upsertEnvFile writes key=value pairs into the .env file at path: existing
// keys are replaced in place, new keys are appended. Re-running init never
// produces duplicate keys.
func upsertEnvFile(path string, keys []string, values map[string]string) error {
	var lines []string
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else if trimmed := strings.TrimRight(string(content), "\n"); trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}

	remaining := make(map[string]bool, len(keys))
	for _, key := range keys {
		remaining[key] = true
	}

	for i, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		key, _, ok := strings.Cut(stripped, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if remaining[key] {
			lines[i] = key + "=" + values[key]
			delete(remaining, key)
		}
	}

	appended := false
	for _, key := range keys {
		if !remaining[key] {
			continue
		}
		if !appended {
			lines = append(lines, "", "# Added by tendril init")
			appended = true
		}
		lines = append(lines, key+"="+values[key])
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func getOllamaModels() []string {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	var models []string
	for _, m := range data.Models {
		models = append(models, m.Name)
	}
	return models
}

func selectOllamaModel(models []string) string {
	// Try to auto-detect the best coding model
	for _, m := range models {
		if strings.Contains(strings.ToLower(m), "coder") {
			return m
		}
	}
	// Fallback to first model
	if len(models) > 0 {
		return models[0]
	}
	return ""
}
