package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// DockerOrchestrator implements the Orchestrator interface using the local Docker daemon.
type DockerOrchestrator struct {
	ImageName string
}

func NewDockerOrchestrator() *DockerOrchestrator {
	return &DockerOrchestrator{
		ImageName: "core-tendril:latest",
	}
}

func (d *DockerOrchestrator) RunTendril(ctx context.Context, taskPrompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--network", "core_default",
		"--env-file", ".env",
		"--entrypoint", "python",
		"-e", fmt.Sprintf("TASK_PROMPT=%s", taskPrompt),
		"-e", fmt.Sprintf("OPENAI_API_KEY=%s", os.Getenv("OPENAI_API_KEY")),
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_PASSWORD=tendril_default_db_secret",
		"-e", "REDIS_PASSWORD=tendril_default_redis_secret",
		"-v", fmt.Sprintf("%s:/app", os.Getenv("PWD")),
		"-w", "/app/tendrils/python",
		d.ImageName,
		"-m", "src.tendrilloop",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("docker run failed: %w (output: %s)", err, string(output))
	}

	return string(output), nil
}
