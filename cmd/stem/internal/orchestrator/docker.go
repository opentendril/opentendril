package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DockerOrchestrator implements the Orchestrator interface using the local Docker daemon.
type DockerOrchestrator struct {
	ImageName string
}

func NewDockerOrchestrator() *DockerOrchestrator {
	return &DockerOrchestrator{
		ImageName: "opentendril-tendril:latest",
	}
}

func (d *DockerOrchestrator) RunTendril(ctx context.Context, taskPrompt string) (string, error) {
	// Build the docker run arguments
	args := []string{
		"run", "--rm",
		"--network", "opentendril-default",
		// Allow the container to reach Ollama running on the host
		"--add-host=host.docker.internal:host-gateway",
		"--env-file", ".env",
		"--entrypoint", "python",
		"-e", fmt.Sprintf("TASK_PROMPT=%s", taskPrompt),
		// Inject credentials from host environment
		"-e", fmt.Sprintf("REDIS_PASSWORD=%s", getEnvOrDefault("REDIS_PASSWORD", "tendril_default_redis_secret")),
		"-e", fmt.Sprintf("POSTGRES_USER=%s", getEnvOrDefault("POSTGRES_USER", "postgres")),
		"-e", fmt.Sprintf("POSTGRES_PASSWORD=%s", getEnvOrDefault("POSTGRES_PASSWORD", "tendril_default_db_secret")),
	}

	// Inject cloud API keys if present
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GROK_API_KEY", "OPENROUTER_API_KEY"} {
		if val := os.Getenv(key); val != "" {
			args = append(args, "-e", fmt.Sprintf("%s=%s", key, val))
		}
	}

	// Inject local LLM config — allows Ollama or vLLM to be used inside the container
	if provider := os.Getenv("DEFAULT_LLM_PROVIDER"); provider != "" {
		args = append(args, "-e", fmt.Sprintf("DEFAULT_LLM_PROVIDER=%s", provider))
	}
	if inferenceURL := os.Getenv("LOCAL_INFERENCE_URL"); inferenceURL != "" {
		// Rewrite localhost → host.docker.internal so the container can reach the host
		inferenceURL = strings.ReplaceAll(inferenceURL, "localhost", "host.docker.internal")
		inferenceURL = strings.ReplaceAll(inferenceURL, "127.0.0.1", "host.docker.internal")
		args = append(args, "-e", fmt.Sprintf("LOCAL_INFERENCE_URL=%s", inferenceURL))
	}
	if modelName := os.Getenv("LOCAL_MODEL_NAME"); modelName != "" {
		args = append(args, "-e", fmt.Sprintf("LOCAL_MODEL_NAME=%s", modelName))
	}

	// Mount workspace and set working directory
	args = append(args,
		"-v", fmt.Sprintf("%s:/app", getEnvOrDefault("PWD", mustGetwd())),
		"-w", "/app/tendrils/python",
		d.ImageName,
		"-m", "src.tendrilloop",
	)

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("docker run failed: %w (output: %s)", err, string(output))
	}

	return string(output), nil
}

func getEnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}
