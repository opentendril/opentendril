package configurator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// ConfiguratorTendril handles the generation of .tendril configurations
// using an LLM to interpret user requests.
type ConfiguratorTendril struct {
	TriggersDir string
	client      *openai.Client
}

func NewConfiguratorTendril(triggersDir string) *ConfiguratorTendril {
	apiKey := os.Getenv("OPENAI_API_KEY")
	client := openai.NewClient(apiKey)
	return &ConfiguratorTendril{
		TriggersDir: triggersDir,
		client:      client,
	}
}

// Execute processes a user task aimed at configuration management
func (c *ConfiguratorTendril) Execute(ctx context.Context, taskPrompt string) (string, error) {
	log.Printf("Sprouting Configurator Tendril for task: %s", taskPrompt)

	systemPrompt := `You are the OpenTendril Configurator Tendril.
Your job is to generate bash scripts for 'Hormonal Triggers' to secure the system.
A Hormonal Trigger is an executable script placed in '.tendril/transduction/hormonal-triggers/'.
It receives a JSON payload path as $1 containing {"persona": "...", "task": "..."}.
If the trigger exits with > 0, the task is blocked. If 0, it is allowed.

Respond ONLY with the raw bash script code. Do not use markdown blocks like ` + "```bash" + ` or ` + "```" + `.
Do not include any explanations. Only output the script.`

	req := openai.ChatCompletionRequest{
		Model: openai.GPT4o,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: systemPrompt,
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: taskPrompt,
			},
		},
	}

	resp, err := c.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("Configurator failed to generate script: %w", err)
	}

	scriptContent := strings.TrimSpace(resp.Choices[0].Message.Content)
	scriptContent = strings.TrimPrefix(scriptContent, "```bash")
	scriptContent = strings.TrimPrefix(scriptContent, "```")
	scriptContent = strings.TrimSuffix(scriptContent, "```")
	scriptContent = strings.TrimSpace(scriptContent)

	if !strings.HasPrefix(scriptContent, "#!") {
		scriptContent = "#!/bin/bash\n\n" + scriptContent
	}

	// Determine a slug for the filename based on the prompt or just use a generic one
	filename := "trigger-generated.sh"
	if strings.Contains(strings.ToLower(taskPrompt), "block") {
		filename = "block-trigger.sh"
	}

	os.MkdirAll(c.TriggersDir, 0755)

	targetPath := filepath.Join(c.TriggersDir, filename)

	// Create file as executable
	out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("Failed to write trigger file %s: %w", filename, err)
	}
	defer out.Close()

	if _, err := out.WriteString(scriptContent); err != nil {
		return "", fmt.Errorf("Failed to write script content: %w", err)
	}

	return fmt.Sprintf("✅ Configurator Tendril successfully generated and saved '%s' to '%s'.", filename, c.TriggersDir), nil
}
