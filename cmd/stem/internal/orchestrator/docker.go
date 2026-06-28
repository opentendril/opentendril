package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DockerOrchestrator implements the Orchestrator interface using the local Docker daemon.
type DockerOrchestrator struct {
	ImageName       string
	Substrate       string
	SubstrateURL    string
	SubstrateBranch string
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

	// The path to the repository on the host
	sourcePath := d.Substrate
	if sourcePath == "" {
		sourcePath = getEnvOrDefault("OPENTENDRIL_SUBSTRATE", mustGetwd())
	}
	sourcePath = repoRoot(sourcePath)

	mountPath := sourcePath

	if d.SubstrateURL != "" {
		// Clone foreign substrate
		shadowPath, err := cloneForeignSubstrate(d.SubstrateURL, d.SubstrateBranch)
		if err == nil {
			mountPath = shadowPath
			// Ensure cleanup after execution
			defer os.RemoveAll(shadowPath)
			fmt.Fprintf(os.Stderr, "🍄 Cross-pollinated foreign Substrate: %s\n", d.SubstrateURL)
		} else {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to cross-pollinate substrate: %v\n", err)
		}
	} else if isGitRepo(sourcePath) {
		// Use local shadow git sandbox
		shadowPath, err := createShadowWorktree(sourcePath)
		if err == nil {
			mountPath = shadowPath

			// Inject node_modules, .venv, vendor from host if they exist
			injectMycorrhizalCache(sourcePath, shadowPath)

			// Ensure cleanup after execution
			defer func() {
				removeShadowWorktree(sourcePath, shadowPath)
			}()
		} else {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to create shadow worktree: %v. Using active workspace.\n", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Shadow Git sandboxing disabled.\n", sourcePath)
	}

	// Mount workspace and set working directory
	args = append(args,
		"-v", fmt.Sprintf("%s:/app", mountPath),
		"-w", "/app/tendrils/python",
		d.ImageName,
		"-m", "src.tendrilloop",
	)

	fmt.Fprintf(os.Stderr, "🚀 Executing docker: %s %s\n", "docker", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	runLogs := string(output)
	if err != nil {
		return runLogs, fmt.Errorf("docker run failed: %w (output: %s)", err, runLogs)
	}

	gitDiff, diffErr := collectGitDiff(ctx, mountPath)
	if diffErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to collect git diff for epigenetic chronicler: %v\n", diffErr)
	} else {
		chronicler := NewEpigeneticChronicler(sourcePath)
		if err := chronicler.TranscribeLearnings(ctx, taskPrompt, gitDiff, runLogs); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Epigenetic chronicler skipped: %v\n", err)
		}
	}

	return runLogs, nil
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

// isGitRepo checks if the given path is inside a git repository.
func isGitRepo(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = path
	err := cmd.Run()
	return err == nil
}

// createShadowWorktree creates a new git worktree in a temporary directory.
func createShadowWorktree(sourcePath string) (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	runID := hex.EncodeToString(bytes)

	shadowPath := filepath.Join(os.TempDir(), fmt.Sprintf("opentendril-sandbox-%s", runID))

	// Create the worktree pointing to HEAD (or a detached HEAD)
	// We use --detach to avoid checking out the current branch which might be locked
	cmd := exec.Command("git", "worktree", "add", "--detach", shadowPath, "HEAD")
	cmd.Dir = sourcePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add failed: %w, output: %s", err, string(output))
	}

	return shadowPath, nil
}

// injectMycorrhizalCache hard-links dependency directories from the host to the shadow sandbox.
func injectMycorrhizalCache(sourcePath, shadowPath string) {
	cacheDirs := []string{"node_modules", ".venv", "venv", "vendor"}

	for _, dir := range cacheDirs {
		srcDir := filepath.Join(sourcePath, dir)
		if info, err := os.Stat(srcDir); err == nil && info.IsDir() {
			dstDir := filepath.Join(shadowPath, dir)
			// Use cp -rl to recursively hard-link the directory
			cmd := exec.Command("cp", "-rl", srcDir, dstDir)
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to inject mycorrhizal cache %s: %v\n", dir, err)
			} else {
				fmt.Fprintf(os.Stderr, "🍄 Injected Mycorrhizal Cache: %s\n", dir)
			}
		}
	}
}

// removeShadowWorktree securely removes the temporary git worktree.
func removeShadowWorktree(sourcePath, shadowPath string) {
	// First tell git to remove the worktree references
	cmd := exec.Command("git", "worktree", "remove", "--force", shadowPath)
	cmd.Dir = sourcePath
	_ = cmd.Run()

	// Ensure the directory is actually gone
	_ = os.RemoveAll(shadowPath)
}

func collectGitDiff(ctx context.Context, mountPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", mountPath, "diff", "--no-color", "--binary")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w (output: %s)", err, string(output))
	}
	return strings.TrimSpace(string(output)), nil
}

// cloneForeignSubstrate clones a remote repository into a temporary sandbox.
func cloneForeignSubstrate(url, branch string) (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	runID := hex.EncodeToString(bytes)

	shadowPath := filepath.Join(os.TempDir(), fmt.Sprintf("opentendril-substrate-%s", runID))

	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}

	// Inject GitHub PAT if present
	if pat := os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN"); pat != "" && strings.Contains(url, "github.com") {
		url = strings.Replace(url, "https://github.com", fmt.Sprintf("https://%s@github.com", pat), 1)
	}

	args = append(args, url, shadowPath)

	cmd := exec.Command("git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}

	return shadowPath, nil
}
