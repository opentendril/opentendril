package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

// DockerOrchestrator implements the Orchestrator interface using the local Docker daemon.
type DockerOrchestrator struct {
	ImageName       string
	Substrate       string
	SubstrateURL    string
	SubstrateBranch string
	StepID          string
	StatusPath      string
}

func NewDockerOrchestrator() *DockerOrchestrator {
	return &DockerOrchestrator{}
}

func (d *DockerOrchestrator) RunTendril(ctx context.Context, taskPrompt string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	stepID := strings.TrimSpace(d.StepID)
	if stepID == "" {
		stepID = newTendrilExecutionID("step")
		d.StepID = stepID
	}

	// The path to the repository on the host.
	sourcePath := d.Substrate
	if sourcePath == "" {
		sourcePath = getEnvOrDefault("OPENTENDRIL_SUBSTRATE", mustGetwd())
	}
	sourcePath = repoRoot(sourcePath)
	gitRepo := isGitRepo(sourcePath)

	statusPath := strings.TrimSpace(d.StatusPath)
	if statusPath != "" && !filepath.IsAbs(statusPath) {
		statusPath = filepath.Join(sourcePath, statusPath)
	}

	if gitRepo && statusPath != "" {
		if existing, err := loadTendrilStatus(statusPath); err != nil {
			return "", err
		} else if existing != nil && strings.TrimSpace(existing.StepID) == stepID {
			switch strings.ToLower(strings.TrimSpace(existing.Status)) {
			case "complete":
				message := fmt.Sprintf("Step %s already completed. Skipping.", stepID)
				fmt.Fprintln(os.Stderr, message)
				return message, nil
			case "failed":
				errText := strings.TrimSpace(existing.Error)
				if errText == "" {
					errText = "previous execution failed"
				}
				fmt.Fprintf(os.Stderr, "⚠️ Resumption halted for %s: %s\n", stepID, errText)
				return "", fmt.Errorf("step %s previously failed: %s", stepID, errText)
			}
		}
	}

	hostStashed := false
	if gitRepo {
		var err error
		hostStashed, err = stashHostWorkspace(ctx, sourcePath, stepID)
		if err != nil {
			return "", err
		}
	} else if statusPath != "" {
		fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Tendril state externalization is disabled.\n", sourcePath)
	}

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

	result, runErr := agent.Run(ctx, taskPrompt)

	if err := session.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Sprout session shutdown issue: %v\n", err)
	}

	if !gitRepo {
		if runErr != nil {
			return "", runErr
		}
		return result.Response, nil
	}

	var statusRelPath string
	if statusPath != "" {
		var err error
		statusRelPath, err = workspaceRelativePath(sourcePath, statusPath)
		if err != nil {
			return "", err
		}
	}

	modifiedFiles, diffErr := collectStageableFiles(ctx, mountPath, statusRelPath)
	if diffErr != nil {
		if hostStashed {
			if restoreErr := restoreHostStash(ctx, sourcePath); restoreErr != nil {
				diffErr = errors.Join(diffErr, restoreErr)
			}
		}
		return "", diffErr
	}

	gitDiff, diffErr := collectGitDiff(ctx, mountPath)
	if diffErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to collect git diff for epigenetic chronicler: %v\n", diffErr)
	}

	executionStatus := tendrilExecutionStatus{
		StepID:        stepID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		FilesModified: modifiedFiles,
	}
	if runErr != nil {
		executionStatus.Status = "failed"
		executionStatus.Error = runErr.Error()
	} else {
		executionStatus.Status = "complete"
	}

	commitHash, commitErr := commitSandboxExecution(ctx, mountPath, sourcePath, statusPath, executionStatus, taskPrompt)
	if commitErr != nil {
		if hostStashed {
			if restoreErr := restoreHostStash(ctx, sourcePath); restoreErr != nil {
				commitErr = errors.Join(commitErr, restoreErr)
			}
		}
		if runErr != nil {
			return "", errors.Join(runErr, commitErr)
		}
		return "", commitErr
	}

	mergeErr := mergeSandboxCommit(ctx, sourcePath, commitHash)
	if mergeErr != nil {
		if runErr != nil {
			if hostStashed {
				if restoreErr := restoreHostStash(ctx, sourcePath); restoreErr != nil {
					mergeErr = errors.Join(mergeErr, restoreErr)
				}
			}
			return "", errors.Join(runErr, mergeErr)
		}
		if hostStashed {
			if restoreErr := restoreHostStash(ctx, sourcePath); restoreErr != nil {
				mergeErr = errors.Join(mergeErr, restoreErr)
			}
		}
		return "", mergeErr
	}

	var finalErr error
	if runErr != nil {
		finalErr = runErr
	}

	if hostStashed {
		if restoreErr := restoreHostStash(ctx, sourcePath); restoreErr != nil {
			finalErr = errors.Join(finalErr, restoreErr)
		}
	}

	if finalErr != nil {
		return "", finalErr
	}

	if gitDiff != "" {
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

type tendrilExecutionStatus struct {
	StepID        string   `json:"stepId"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
	Timestamp     string   `json:"timestamp"`
	FilesModified []string `json:"filesModified"`
}

func newTendrilExecutionID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
}

func runGitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}

	return strings.TrimSpace(string(output)), nil
}

func stashHostWorkspace(ctx context.Context, root, runID string) (bool, error) {
	statusOutput, err := runGitCommand(ctx, root, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("host pre-flight status check failed: %w", err)
	}
	if strings.TrimSpace(statusOutput) == "" {
		return false, nil
	}

	stashName := fmt.Sprintf("opentendril-host-pre-flight-stash-%s", runID)
	if _, err := runGitCommand(ctx, root, "stash", "save", "-u", stashName); err != nil {
		return false, fmt.Errorf("git stash save failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "🧺 Stashed host workspace as %s\n", stashName)
	return true, nil
}

func restoreHostStash(ctx context.Context, root string) error {
	if _, err := runGitCommand(ctx, root, "stash", "pop"); err != nil {
		return fmt.Errorf("git stash pop failed: %w", err)
	}

	return nil
}

func loadTendrilStatus(path string) (*tendrilExecutionStatus, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tendril status %s: %w", path, err)
	}

	var status tendrilExecutionStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return nil, fmt.Errorf("decode tendril status %s: %w", path, err)
	}

	return &status, nil
}

func writeTendrilStatus(path string, status tendrilExecutionStatus) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create tendril status directory: %w", err)
	}

	payload, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("encode tendril status: %w", err)
	}
	payload = append(payload, '\n')

	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write tendril status %s: %w", path, err)
	}

	return nil
}

func workspaceRelativePath(rootPath, targetPath string) (string, error) {
	rootPath = strings.TrimSpace(rootPath)
	targetPath = strings.TrimSpace(targetPath)
	if rootPath == "" || targetPath == "" {
		return "", nil
	}

	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(rootPath, targetPath)
	}

	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace relative path for %s: %w", targetPath, err)
	}

	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s escapes workspace %s", targetPath, rootPath)
	}

	return filepath.ToSlash(rel), nil
}

func collectStageableFiles(ctx context.Context, mountPath string, excludedPaths ...string) ([]string, error) {
	output, err := runGitCommand(ctx, mountPath, "status", "--porcelain")
	if err != nil {
		return nil, err
	}

	excluded := make(map[string]struct{}, len(excludedPaths))
	for _, path := range excludedPaths {
		normalized := filepath.ToSlash(strings.TrimSpace(path))
		if normalized == "" {
			continue
		}
		excluded[normalized] = struct{}{}
	}

	stageable := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(line) < 3 {
			continue
		}

		pathPart := strings.TrimSpace(line[3:])
		if pathPart == "" {
			continue
		}

		paths := []string{pathPart}
		if strings.Contains(pathPart, " -> ") {
			paths = strings.Split(pathPart, " -> ")
		}

		for _, path := range paths {
			normalized := filepath.ToSlash(strings.TrimSpace(path))
			if normalized == "" {
				continue
			}
			if _, ok := excluded[normalized]; ok {
				continue
			}
			if shouldIgnoreStagePath(normalized) {
				continue
			}
			stageable[normalized] = struct{}{}
		}
	}

	if len(stageable) == 0 {
		return []string{}, nil
	}

	files := make([]string, 0, len(stageable))
	for path := range stageable {
		files = append(files, path)
	}
	sort.Strings(files)

	return files, nil
}

func shouldIgnoreStagePath(path string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" {
		return true
	}

	lowerPath := strings.ToLower(normalized)
	if strings.HasSuffix(lowerPath, ".log") {
		return true
	}

	ignoredSegments := map[string]struct{}{
		".cache":      {},
		"build":       {},
		"dist":        {},
		"tmp":         {},
		"__pycache__": {},
	}

	for _, segment := range strings.Split(normalized, "/") {
		if _, ok := ignoredSegments[strings.ToLower(segment)]; ok {
			return true
		}
	}

	return false
}

func commitSandboxExecution(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus tendrilExecutionStatus, taskPrompt string) (string, error) {
	stagePaths := append([]string{}, executionStatus.FilesModified...)

	if strings.TrimSpace(statusPath) != "" {
		statusRelPath, err := workspaceRelativePath(sourcePath, statusPath)
		if err != nil {
			return "", err
		}

		statusSandboxPath := filepath.Join(mountPath, filepath.FromSlash(statusRelPath))
		if err := writeTendrilStatus(statusSandboxPath, executionStatus); err != nil {
			return "", err
		}

		stagePaths = append(stagePaths, statusRelPath)
	}

	stageSet := make(map[string]struct{}, len(stagePaths))
	uniqueStagePaths := make([]string, 0, len(stagePaths))
	for _, path := range stagePaths {
		normalized := filepath.ToSlash(strings.TrimSpace(path))
		if normalized == "" {
			continue
		}
		if _, ok := stageSet[normalized]; ok {
			continue
		}
		stageSet[normalized] = struct{}{}
		uniqueStagePaths = append(uniqueStagePaths, normalized)
	}

	if len(uniqueStagePaths) > 0 {
		addArgs := append([]string{"add", "-A", "--"}, uniqueStagePaths...)
		if _, err := runGitCommand(ctx, mountPath, addArgs...); err != nil {
			return "", err
		}
	}

	commitMessage := buildTendrilCommitMessage(executionStatus.StepID, taskPrompt, executionStatus.Status, executionStatus.Error)
	commitArgs := []string{"commit", "-m", commitMessage}
	if len(uniqueStagePaths) == 0 {
		commitArgs = append([]string{"commit", "--allow-empty"}, "-m", commitMessage)
	}

	if _, err := runGitCommand(ctx, mountPath, commitArgs...); err != nil {
		return "", err
	}

	commitHash, err := runGitCommand(ctx, mountPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}

	return commitHash, nil
}

func mergeSandboxCommit(ctx context.Context, sourcePath, commitHash string) error {
	if _, err := runGitCommand(ctx, sourcePath, "merge", "--ff-only", commitHash); err != nil {
		return err
	}

	return nil
}

func buildTendrilCommitMessage(stepID, taskPrompt, status, failureError string) string {
	if strings.ToLower(strings.TrimSpace(status)) == "failed" {
		return fmt.Sprintf("tendril(%s) [INCOMPLETE]: %s", strings.TrimSpace(stepID), summarizeTendrilFailureError(failureError))
	}

	return fmt.Sprintf("tendril(%s): %s", strings.TrimSpace(stepID), summarizeTendrilPrompt(taskPrompt))
}

func summarizeTendrilPrompt(taskPrompt string) string {
	summary := strings.Join(strings.Fields(strings.TrimSpace(taskPrompt)), " ")
	if summary == "" {
		return "tendril task"
	}

	const maxRunes = 72
	runes := []rune(summary)
	if len(runes) <= maxRunes {
		return summary
	}

	summary = strings.TrimRight(string(runes[:maxRunes]), " ,.;:-")
	if summary == "" {
		summary = string(runes[:maxRunes])
	}

	return summary + "..."
}

func summarizeTendrilFailureError(failureError string) string {
	summary := strings.Join(strings.Fields(strings.TrimSpace(failureError)), " ")
	if summary == "" {
		return "execution failed"
	}

	const maxRunes = 120
	runes := []rune(summary)
	if len(runes) <= maxRunes {
		return summary
	}

	summary = strings.TrimRight(string(runes[:maxRunes]), " ,.;:-")
	if summary == "" {
		summary = string(runes[:maxRunes])
	}

	return summary + "..."
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
