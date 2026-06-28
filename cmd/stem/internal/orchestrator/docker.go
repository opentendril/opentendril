package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

// DockerOrchestrator implements the Orchestrator interface using the local Docker daemon.
type DockerOrchestrator struct {
	ImageName       string
	Substrate       string
	SubstrateURL    string
	SubstrateBranch string
}

func NewDockerOrchestrator() *DockerOrchestrator {
	return &DockerOrchestrator{}
}

func (d *DockerOrchestrator) RunTendril(ctx context.Context, taskPrompt string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// The path to the repository on the host.
	sourcePath := d.Substrate
	if sourcePath == "" {
		sourcePath = getEnvOrDefault("OPENTENDRIL_SUBSTRATE", mustGetwd())
	}
	sourcePath = repoRoot(sourcePath)

	mountPath := sourcePath
	var cleanup func()

	if d.SubstrateURL != "" {
		// Clone foreign substrate
		shadowPath, err := cloneForeignSubstrate(d.SubstrateURL, d.SubstrateBranch)
		if err == nil {
			mountPath = shadowPath
			cleanup = func() {
				_ = os.RemoveAll(shadowPath)
			}
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

			cleanup = func() {
				removeShadowWorktree(sourcePath, shadowPath)
			}
		} else {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to create shadow worktree: %v. Using active workspace.\n", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Shadow Git sandboxing disabled.\n", sourcePath)
	}

	imageName := d.resolveImageName(mountPath)
	if err := ensureSproutImage(ctx, imageName); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", err
	}
	session, err := startDockerSession(ctx, imageName, mountPath)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}
	defer session.Close()

	agent, err := newAgent(ctx, mountPath, llm.NewClientFromEnv(), session)
	if err != nil {
		return "", err
	}

	result, err := agent.Run(ctx, taskPrompt)
	if err != nil {
		return "", err
	}

	if err := session.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Sprout session shutdown issue: %v\n", err)
	}

	gitDiff, diffErr := collectGitDiff(ctx, mountPath)
	if diffErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to collect git diff for epigenetic chronicler: %v\n", diffErr)
	} else {
		chronicler := NewEpigeneticChronicler(sourcePath)
		if err := chronicler.TranscribeLearnings(ctx, result.Transcript, gitDiff, session.Logs()); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Epigenetic chronicler skipped: %v\n", err)
		}
	}

	return result.Response, nil
}

func (d *DockerOrchestrator) resolveImageName(workspace string) string {
	if trimmed := strings.TrimSpace(d.ImageName); trimmed != "" {
		return trimmed
	}

	if workspaceHasExtension(workspace, ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx") {
		return "opentendril-typescript:latest"
	}

	if workspaceHasExtension(workspace, ".py") {
		return "opentendril-python:latest"
	}

	return "opentendril-go:latest"
}

func ensureSproutImage(ctx context.Context, imageName string) error {
	buildContext, dockerfile, err := sproutBuildSpec(imageName)
	if err != nil {
		return err
	}
	if buildContext == "" || dockerfile == "" {
		return nil
	}

	if err := exec.CommandContext(ctx, "docker", "image", "inspect", imageName).Run(); err == nil {
		return nil
	}

	fmt.Fprintf(os.Stderr, "🧱 Building %s from %s\n", imageName, dockerfile)
	args := []string{"build", "-f", dockerfile, "-t", imageName, buildContext}
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build %s failed: %w (output: %s)", imageName, err, strings.TrimSpace(string(output)))
	}

	return nil
}

func sproutBuildSpec(imageName string) (string, string, error) {
	coreRoot, err := repoSourceRoot()
	if err != nil {
		return "", "", err
	}

	switch imageName {
	case "opentendril-go:latest":
		return coreRoot, filepath.Join(coreRoot, "tendrils", "go", "Dockerfile"), nil
	case "opentendril-typescript:latest":
		return coreRoot, filepath.Join(coreRoot, "tendrils", "typescript", "Dockerfile"), nil
	case "opentendril-python:latest":
		return filepath.Join(coreRoot, "tendrils", "python"), filepath.Join(coreRoot, "tendrils", "python", "Dockerfile"), nil
	default:
		return "", "", nil
	}
}

func repoSourceRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("unable to determine source root")
	}
	absFile, err := filepath.Abs(file)
	if err == nil {
		file = absFile
	}

	current := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("could not locate repository root from %s", file)
		}
		current = parent
	}
}

func workspaceHasExtension(workspace string, extensions ...string) bool {
	extensionSet := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		extensionSet[strings.ToLower(extension)] = struct{}{}
	}

	ignoredDirs := map[string]struct{}{
		".git":         {},
		".tendril":     {},
		"tendrils":     {},
		"static":       {},
		"scripts":      {},
		"node_modules": {},
		"vendor":       {},
		".venv":        {},
		"venv":         {},
		"dist":         {},
		"build":        {},
		"__pycache__":  {},
	}

	found := false
	_ = filepath.WalkDir(workspace, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if found {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			if _, ok := ignoredDirs[entry.Name()]; ok && path != workspace {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := extensionSet[strings.ToLower(filepath.Ext(entry.Name()))]; ok {
			found = true
			return filepath.SkipDir
		}
		return nil
	})

	return found
}

type dockerSproutSession struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderr     bytes.Buffer
	stderrDone chan struct{}
	callMu     sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

func startDockerSession(ctx context.Context, imageName string, mountPath string) (*dockerSproutSession, error) {
	args := []string{
		"run", "-i", "--rm",
		"--network", "opentendril-default",
		"--add-host=host.docker.internal:host-gateway",
		"-v", fmt.Sprintf("%s:/app", mountPath),
		"-w", "/app",
	}

	if envFile := strings.TrimSpace(os.Getenv("TENDRIL_ENV_FILE")); envFile != "" {
		if _, err := os.Stat(envFile); err == nil {
			args = append(args, "--env-file", envFile)
		}
	} else if _, err := os.Stat(".env"); err == nil {
		args = append(args, "--env-file", ".env")
	}

	for _, key := range []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GOOGLE_API_KEY",
		"GROK_API_KEY",
		"OPENROUTER_API_KEY",
		"OPENTENDRIL_API_KEY",
		"NVIDIA_API_KEY",
		"GITHUB_PERSONAL_ACCESS_TOKEN",
		"GITHUB_TOKEN",
		"DEFAULT_LLM_PROVIDER",
		"LOCAL_INFERENCE_URL",
		"LOCAL_MODEL_NAME",
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			args = append(args, "-e", fmt.Sprintf("%s=%s", key, value))
		}
	}

	args = append(args, imageName)

	fmt.Fprintf(os.Stderr, "🚀 Executing docker: %s %s\n", "docker", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "docker", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create sprout stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("create sprout stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("create sprout stderr pipe: %w", err)
	}

	session := &dockerSproutSession{
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReader(stdout),
		stderrDone: make(chan struct{}),
	}

	go func() {
		defer close(session.stderrDone)
		_, _ = io.Copy(io.MultiWriter(os.Stderr, &session.stderr), stderr)
	}()

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("docker run failed: %w", err)
	}

	return session, nil
}

func (s *dockerSproutSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
	response, err := s.Call(ctx, ToolCall{Tool: "listAvailableTools", Arguments: map[string]any{}})
	if err != nil {
		return nil, err
	}
	if strings.ToLower(strings.TrimSpace(response.Status)) != "success" {
		if strings.TrimSpace(response.Error) != "" {
			return nil, fmt.Errorf("listAvailableTools failed: %s", response.Error)
		}
		return nil, fmt.Errorf("listAvailableTools failed: %v", response.Output)
	}

	return decodeToolDefinitions(response.Output)
}

func (s *dockerSproutSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.cmd == nil {
		return ToolResponse{}, fmt.Errorf("sprout session is not active")
	}

	s.callMu.Lock()
	defer s.callMu.Unlock()

	if err := json.NewEncoder(s.stdin).Encode(call); err != nil {
		return ToolResponse{}, fmt.Errorf("write tool call: %w", err)
	}

	line, err := s.readResponseLine()
	if err != nil {
		return ToolResponse{}, err
	}

	var response ToolResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return ToolResponse{}, fmt.Errorf("decode tool response: %w (payload: %s)", err, strings.TrimSpace(string(line)))
	}

	return response, nil
}

func (s *dockerSproutSession) readResponseLine() ([]byte, error) {
	for {
		line, err := s.stdout.ReadBytes('\n')
		if err != nil {
			if len(bytes.TrimSpace(line)) == 0 {
				return nil, fmt.Errorf("read tool response: %w", err)
			}
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		return trimmed, nil
	}
}

func (s *dockerSproutSession) Close() error {
	if s == nil {
		return nil
	}

	s.closeOnce.Do(func() {
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.cmd != nil {
			s.closeErr = s.cmd.Wait()
		}
		<-s.stderrDone
	})

	return s.closeErr
}

func (s *dockerSproutSession) Logs() string {
	if s == nil {
		return ""
	}
	return s.stderr.String()
}

func decodeToolDefinitions(output any) ([]ToolDefinition, error) {
	if output == nil {
		return nil, nil
	}

	raw, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("marshal tool inventory: %w", err)
	}

	var wrapped struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Tools) > 0 {
		return wrapped.Tools, nil
	}

	var tools []ToolDefinition
	if err := json.Unmarshal(raw, &tools); err == nil && len(tools) > 0 {
		return tools, nil
	}

	var single ToolDefinition
	if err := json.Unmarshal(raw, &single); err == nil && strings.TrimSpace(single.Name) != "" {
		return []ToolDefinition{single}, nil
	}

	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
		return nil, fmt.Errorf("unrecognized tool inventory payload: %s", trimmed)
	}

	return nil, nil
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
