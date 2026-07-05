package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

func runLLMCmd(ctx context.Context, args []string) {
	if len(args) < 1 {
		printLLMUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		runLLMListCmd(ctx, args[1:])
	case "test":
		runLLMTestCmd(ctx, args[1:])
	case "-h", "--help", "help":
		printLLMUsage()
	default:
		fmt.Printf("Unknown llm command: %s\n", args[0])
		printLLMUsage()
		os.Exit(1)
	}
}

func runLLMListCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("llm list", flag.ExitOnError)
	baseURL := flags.String("url", "", "OpenAI-compatible local server base URL")
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}

	spec := llm.ResolveLocalProviderSpec()
	applyLLMBaseURLOverride(&spec, *baseURL)

	models, err := llm.NewClient(spec).ListModels(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list local models: %v\n", err)
		os.Exit(1)
	}
	for _, model := range models {
		fmt.Println(model)
	}
}

func runLLMTestCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("llm test", flag.ExitOnError)
	baseURL := flags.String("url", "", "OpenAI-compatible local server base URL")
	model := flags.String("model", "", "Model name to use for the test prompt")
	systemPrompt := flags.String("system", "You are a concise assistant.", "System prompt")
	prompt := flags.String("prompt", "Reply with exactly: OpenTendril local LLM ok", "User prompt")
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}

	spec := llm.ResolveLocalProviderSpec()
	applyLLMBaseURLOverride(&spec, *baseURL)
	if trimmed := strings.TrimSpace(*model); trimmed != "" {
		spec.Model = trimmed
	}
	if strings.TrimSpace(spec.Model) == "" {
		fmt.Fprintln(os.Stderr, "No local model configured. Set LOCAL_MODEL_NAME, .tendril/config.yaml llm.providers.local.model, or pass --model.")
		os.Exit(1)
	}

	response, err := llm.NewClient(spec).CallPrompt(ctx, *systemPrompt, *prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Local LLM test failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(response)
}

func applyLLMBaseURLOverride(spec *llm.ProviderSpec, baseURL string) {
	if spec == nil {
		return
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return
	}
	spec.BaseURL = strings.TrimRight(baseURL, "/")
	spec.BaseURLs = []string{spec.BaseURL}
}

func printLLMUsage() {
	fmt.Println("Usage:")
	fmt.Println("  tendril llm list [--url http://localhost:11434/v1]")
	fmt.Println("  tendril llm test [--url http://localhost:11434/v1] [--model llama3.2] [--prompt text]")
}
