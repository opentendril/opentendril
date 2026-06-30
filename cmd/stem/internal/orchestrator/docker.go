package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/llm"
	"github.com/opentendril/core/cmd/stem/internal/sandbox"
)

// DockerOrchestrator implements the Orchestrator interface using the local Docker daemon.
type DockerOrchestrator struct {
	ImageName        string
	Substrate        string
	SubstrateURL     string
	SubstrateBranch  string
	StepID           string
	StatusPath       string
	IsCoordinator    bool
	Tier             llm.ModelTier
	Genotype         string
	Temperature      float64
	DisableMergeBack bool
}

func NewDockerOrchestrator() *DockerOrchestrator {
	return &DockerOrchestrator{}
}

type tendrilRunner interface {
	Run(ctx context.Context, taskPrompt string) (agentResult, error)
}

var (
	ensureSproutImageFn  = ensureSproutImage
	startDockerSessionFn = func(ctx context.Context, imageName, mountPath string, extraEnv ...string) (toolSession, error) {
		return startDockerSession(ctx, imageName, mountPath, extraEnv...)
	}
	newAgentFn = func(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession) (tendrilRunner, error) {
		return newAgent(ctx, workspace, genotypeRoot, genotypeName, client, session)
	}
	stashHostWorkspaceFn      = stashHostWorkspace
	restoreHostStashFn        = restoreHostStash
	createShadowWorktreeFn    = createShadowWorktree
	removeShadowWorktreeFn    = removeShadowWorktree
	injectMycorrhizalCacheFn  = injectMycorrhizalCache
	collectStageableFilesFn   = collectStageableFiles
	collectGitDiffFn          = collectGitDiff
	commitSandboxExecutionFn  = commitSandboxExecution
	mergeSandboxCommitFn      = mergeSandboxCommit
	pushSandboxCommitFn       = pushSandboxCommit
	runContainerFitnessTestFn = runContainerFitnessTest
	generateRepoMapFn         = GenerateRepoMap
)

func (d *DockerOrchestrator) resolveLLMClient() *llm.Client {
	var client *llm.Client
	tier := llm.TierPremium
	if d != nil && d.Tier != "" {
		tier = d.Tier
	}
	if d != nil && d.IsCoordinator {
		client = llm.NewCoordinatorClientFromEnv()
	} else {
		client = llm.NewClientForTier(tier)
	}
	if d != nil && d.Temperature > 0 {
		client.SetTemperature(d.Temperature)
	}
	return client
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

	substratesConfig, err := LoadSubstratesConfig("")
	if err != nil {
		return "", err
	}

	plan, err := resolveSubstrateExecutionPlan(d, substratesConfig)
	if err != nil {
		return "", err
	}

	if plan.readOnly {
		fmt.Fprintln(os.Stderr, "⚠️ Substrate is configured as READONLY. Discarding sandbox modifications.")
	}

	sourcePath := plan.hostPath
	mountPath := sourcePath
	statusPath := strings.TrimSpace(d.StatusPath)
	gitRepo := false
	var hostStashed bool
	var cleanup func()
	extraEnv := make([]string, 0, 1)
	if plan.readOnly {
		extraEnv = append(extraEnv, "TENDRIL_READONLY=true")
	}

	if plan.remoteClone {
		authValue := ""
		if plan.authRef != "" {
			authValue = strings.TrimSpace(os.Getenv(plan.authRef))
		}

		clonedPath, err := cloneNamedForeignSubstrate(plan.name, plan.cloneURL, plan.cloneBranch, plan.authRef, authValue)
		if err != nil {
			return "", err
		}

		sourcePath = clonedPath
		mountPath = clonedPath
		statusPath = ""
		gitRepo = isGitRepo(sourcePath)
		cleanup = func() {
			_ = os.RemoveAll(clonedPath)
		}
		if !gitRepo {
			if cleanup != nil {
				cleanup()
			}
			return "", fmt.Errorf("cloned substrate %s is not a git repository", clonedPath)
		}

		fmt.Fprintf(os.Stderr, "🍄 Cross-pollinated foreign Substrate: %s\n", plan.cloneURL)
	} else {
		sourcePath = repoRoot(sourcePath)
		gitRepo = isGitRepo(sourcePath)

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

		if gitRepo && !plan.readOnly && !d.DisableMergeBack {
			hostStashed, err = stashHostWorkspaceFn(ctx, sourcePath, stepID)
			if err != nil {
				return "", err
			}
		} else if statusPath != "" {
			fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Tendril state externalization is disabled.\n", sourcePath)
		}

		if gitRepo {
			shadowPath, err := createShadowWorktreeFn(sourcePath, plan.cloneBranch)
			if err == nil {
				mountPath = shadowPath
				injectMycorrhizalCacheFn(sourcePath, shadowPath)
				cleanup = func() {
					removeShadowWorktreeFn(sourcePath, shadowPath)
				}
			} else {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to create shadow worktree: %v. Using active workspace.\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Shadow Git sandboxing disabled.\n", sourcePath)
		}
	}

	if d.Genotype != "" {
		stagePlasmidsForGenotype(sourcePath, mountPath, d.Genotype)
	}

	repoMapMarkdown, err := generateRepoMapFn(mountPath)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", fmt.Errorf("generate repo map: %w", err)
	}

	repoMapPath := filepath.Join(mountPath, ".tendril", "genome", "repomap.md")
	if err := os.MkdirAll(filepath.Dir(repoMapPath), 0o755); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", fmt.Errorf("create repo map directory: %w", err)
	}
	if err := os.WriteFile(repoMapPath, []byte(repoMapMarkdown), 0o644); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", fmt.Errorf("write repo map plasmid: %w", err)
	}

	imageName := d.resolveImageName(mountPath)
	if err := ensureSproutImageFn(ctx, imageName); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", err
	}

	session, err := startDockerSessionFn(ctx, imageName, mountPath, extraEnv...)
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

	agent, err := newAgentFn(ctx, mountPath, sourcePath, d.Genotype, d.resolveLLMClient(), session)
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

	if plan.readOnly {
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
			if hostStashed {
				if restoreErr := restoreHostStashFn(ctx, sourcePath); restoreErr != nil {
					err = errors.Join(err, restoreErr)
				}
			}
			return "", err
		}
	}

	modifiedFiles, diffErr := collectStageableFilesFn(ctx, mountPath, statusRelPath)
	if diffErr != nil {
		if hostStashed {
			if restoreErr := restoreHostStashFn(ctx, sourcePath); restoreErr != nil {
				diffErr = errors.Join(diffErr, restoreErr)
			}
		}
		return "", diffErr
	}

	gitDiff, diffErr := collectGitDiffFn(ctx, mountPath)
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

	commitHash, commitErr := commitSandboxExecutionFn(ctx, mountPath, sourcePath, statusPath, executionStatus, taskPrompt)
	if commitErr != nil {
		if hostStashed {
			if restoreErr := restoreHostStashFn(ctx, sourcePath); restoreErr != nil {
				commitErr = errors.Join(commitErr, restoreErr)
			}
		}
		if runErr != nil {
			return "", errors.Join(runErr, commitErr)
		}
		return "", commitErr
	}

	if d.DisableMergeBack {
		if runErr != nil {
			return commitHash, runErr
		}
		return commitHash, nil
	}

	if plan.remoteClone {
		if pushErr := pushSandboxCommitFn(ctx, mountPath, plan.cloneBranch); pushErr != nil {
			if hostStashed {
				if restoreErr := restoreHostStashFn(ctx, sourcePath); restoreErr != nil {
					pushErr = errors.Join(pushErr, restoreErr)
				}
			}
			if runErr != nil {
				return "", errors.Join(runErr, pushErr)
			}
			return "", pushErr
		}
	} else {
		mergeErr := mergeSandboxCommitFn(ctx, sourcePath, commitHash)
		if mergeErr != nil {
			if runErr != nil {
				if hostStashed {
					if restoreErr := restoreHostStashFn(ctx, sourcePath); restoreErr != nil {
						mergeErr = errors.Join(mergeErr, restoreErr)
					}
				}
				return "", errors.Join(runErr, mergeErr)
			}
			if hostStashed {
				if restoreErr := restoreHostStashFn(ctx, sourcePath); restoreErr != nil {
					mergeErr = errors.Join(mergeErr, restoreErr)
				}
			}
			return "", mergeErr
		}
	}

	var finalErr error
	if runErr != nil {
		finalErr = runErr
	}

	if hostStashed {
		if restoreErr := restoreHostStashFn(ctx, sourcePath); restoreErr != nil {
			finalErr = errors.Join(finalErr, restoreErr)
		}
	}

	if finalErr != nil {
		return "", finalErr
	}

	if gitDiff != "" {
		chronicler := newEpigeneticChroniclerForTier(sourcePath, llm.TierCheapest)
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

type sandboxToolSession struct {
	sandbox sandbox.Sandbox
}

func startDockerSession(ctx context.Context, imageName string, mountPath string, extraEnv ...string) (toolSession, error) {
	provider := sandbox.NewDockerProvider()
	instance, err := provider.Create(ctx, sandbox.SandboxSpec{
		Image:       imageName,
		WorkingDir:  "/app",
		NetworkMode: sandbox.NetworkMode("opentendril-default"),
		Mounts: []sandbox.MountSpec{
			{
				Source: mountPath,
				Target: "/app",
			},
		},
		Environment: buildSandboxEnvironment(extraEnv...),
	})
	if err != nil {
		return nil, err
	}

	return &sandboxToolSession{sandbox: instance}, nil
}

func (s *sandboxToolSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
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

func (s *sandboxToolSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.sandbox == nil {
		return ToolResponse{}, fmt.Errorf("sandbox session is not active")
	}

	payload, err := json.Marshal(call)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("encode tool call: %w", err)
	}

	result, err := s.sandbox.Run(ctx, sandbox.CommandSpec{Stdin: payload})
	if err != nil {
		return ToolResponse{}, err
	}

	var response ToolResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &response); err != nil {
		return ToolResponse{}, fmt.Errorf("decode tool response: %w (payload: %s)", err, strings.TrimSpace(result.Stdout))
	}

	return response, nil
}

func (s *sandboxToolSession) Close() error {
	if s == nil || s.sandbox == nil {
		return nil
	}
	return s.sandbox.Stop(context.Background())
}

func (s *sandboxToolSession) Logs() string {
	if s == nil || s.sandbox == nil {
		return ""
	}

	logs, err := s.sandbox.SnapshotLogs(context.Background())
	if err != nil {
		return ""
	}
	return logs.Stderr
}

func buildSandboxEnvironment(extraEnv ...string) map[string]string {
	values := make(map[string]string)

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
			values[key] = value
		}
	}

	for _, entry := range extraEnv {
		key, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		values[key] = value
	}

	return values
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
func createShadowWorktree(sourcePath, substrateBranch string) (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	runID := hex.EncodeToString(bytes)

	shadowPath := filepath.Join(os.TempDir(), fmt.Sprintf("opentendril-sandbox-%s", runID))

	branch := strings.TrimSpace(substrateBranch)
	var cmd *exec.Cmd
	if branch == "" {
		// Create the worktree pointing to HEAD (or a detached HEAD)
		// We use --detach to avoid checking out the current branch which might be locked
		cmd = exec.Command("git", "worktree", "add", "--detach", shadowPath, "HEAD")
	} else if localBranchExists(sourcePath, branch) {
		cmd = exec.Command("git", "worktree", "add", shadowPath, branch)
	} else {
		cmd = exec.Command("git", "worktree", "add", "-b", branch, shadowPath, "HEAD")
	}
	cmd.Dir = sourcePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add failed: %w, output: %s", err, string(output))
	}

	return shadowPath, nil
}

func localBranchExists(sourcePath, branch string) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false
	}

	ref := "refs/heads/" + strings.TrimPrefix(branch, "refs/heads/")
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Dir = sourcePath
	return cmd.Run() == nil
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

func runContainerFitnessTest(ctx context.Context, imageName, shadowPath, fitnessTest string) error {
	if strings.TrimSpace(fitnessTest) == "" {
		return nil
	}
	if strings.TrimSpace(imageName) == "" {
		return fmt.Errorf("fitness test image name is empty")
	}
	if strings.TrimSpace(shadowPath) == "" {
		return fmt.Errorf("fitness test shadow path is empty")
	}

	args := []string{
		"run",
		"--rm",
		"-v", fmt.Sprintf("%s:/app", shadowPath),
		"-w", "/app",
		imageName,
		"sh",
		"-c",
		fitnessTest,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker fitness test failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
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
	return cloneNamedForeignSubstrate("", url, branch, "", "")
}

func cloneNamedForeignSubstrate(name, url, branch, authRef, authValue string) (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	runID := hex.EncodeToString(bytes)

	prefix := "opentendril-substrate"
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		prefix = fmt.Sprintf("%s-%s", prefix, sanitizeTempComponent(trimmed))
	}
	shadowPath := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s", prefix, runID))

	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}

	resolvedAuthRef := strings.TrimSpace(authRef)
	resolvedAuthValue := strings.TrimSpace(authValue)
	if resolvedAuthRef != "" && resolvedAuthValue == "" {
		resolvedAuthValue = strings.TrimSpace(os.Getenv(resolvedAuthRef))
	}
	if resolvedAuthValue == "" && resolvedAuthRef == "" && strings.Contains(url, "github.com") {
		if pat := strings.TrimSpace(os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN")); pat != "" {
			resolvedAuthRef = "GITHUB_PERSONAL_ACCESS_TOKEN"
			resolvedAuthValue = pat
		}
	}
	if resolvedAuthValue != "" && !strings.Contains(url, "@") && strings.HasPrefix(url, "https://") {
		url = strings.Replace(url, "https://", "https://"+resolvedAuthValue+"@", 1)
	}

	args = append(args, url, shadowPath)

	cmd := exec.Command("git", args...)
	if resolvedAuthRef != "" && resolvedAuthValue != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", resolvedAuthRef, resolvedAuthValue))
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}

	return shadowPath, nil
}

func pushSandboxCommit(ctx context.Context, mountPath, branch string) error {
	targetBranch := strings.TrimSpace(branch)
	if targetBranch == "" {
		currentBranch, err := runGitCommand(ctx, mountPath, "branch", "--show-current")
		if err != nil {
			return err
		}
		targetBranch = strings.TrimSpace(currentBranch)
	}
	if targetBranch == "" {
		return fmt.Errorf("unable to determine branch for push")
	}
	targetBranch = strings.TrimPrefix(targetBranch, "refs/heads/")

	commitMessage, err := runGitCommand(ctx, mountPath, "log", "-1", "--pretty=%B", "HEAD")
	if err != nil {
		return err
	}

	if delegated, err := delegateGitPushIfConfigured(ctx, mountPath, targetBranch, commitMessage); delegated {
		return err
	}

	if _, err := runGitCommand(ctx, mountPath, "push", "origin", "HEAD:"+targetBranch); err != nil {
		return err
	}

	return nil
}

func stagePlasmidsForGenotype(sourcePath, targetPath, genotypeName string) {
	genotypePath := filepath.Join(sourcePath, ".tendril", "genotypes", genotypeName+".json")
	content, err := os.ReadFile(genotypePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to read genotype %s for plasmid staging: %v\n", genotypeName, err)
		return
	}

	var metadata struct {
		Plasmids []string `json:"plasmids"`
	}
	if err := json.Unmarshal(content, &metadata); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to parse genotype %s JSON: %v\n", genotypeName, err)
		return
	}

	for _, name := range metadata.Plasmids {
		sourceFile, err := FindPlasmidSource(sourcePath, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to locate plasmid %s: %v\n", name, err)
			continue
		}

		destDir := filepath.Join(targetPath, ".tendril", "genome")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to create sandbox genome directory: %v\n", err)
			continue
		}

		destFile := filepath.Join(destDir, filepath.Base(sourceFile))
		if err := CopyMarkdownFile(sourceFile, destFile); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to stage sandbox plasmid %s: %v\n", name, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "🧬 Staged sandbox plasmid: %s -> %s\n", name, destFile)
	}
}
