package conductor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/terrarium"
	"github.com/opentendril/core/roots/llm"
)

const terrariumProviderEnvKey = "TENDRIL_TERRARIUM_PROVIDER"

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
	Provider         string
	Model            string
	BaseURL          string
	Genotype         string
	Temperature      float64
	DisableMergeBack bool
	EventBus         *eventbus.Bus
}

func NewDockerOrchestrator() *DockerOrchestrator {
	return &DockerOrchestrator{}
}

type sproutRunner interface {
	Run(ctx context.Context, taskPrompt string) (agentResult, error)
}

var (
	ensureSproutImageFn     = ensureSproutImage
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, command []string, extraEnv ...string) (toolSession, error) {
		return startTerrariumSession(ctx, providerName, imageName, mountPath, command, extraEnv...)
	}
	newAgentFn = func(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string) (sproutRunner, error) {
		return newAgent(ctx, workspace, genotypeRoot, genotypeName, client, session, eventBus, stepID)
	}
	stashHostWorkspaceFn       = stashHostWorkspace
	restoreHostStashFn         = restoreHostStash
	createShadowWorktreeFn     = createShadowWorktree
	removeShadowWorktreeFn     = removeShadowWorktree
	injectMycorrhizalCacheFn   = injectMycorrhizalCache
	collectStageableFilesFn    = collectStageableFiles
	collectGitDiffFn           = collectGitDiff
	commitTerrariumExecutionFn = commitTerrariumExecution
	mergeTerrariumCommitFn     = mergeTerrariumCommit
	pushTerrariumCommitFn      = pushTerrariumCommit
	runContainerFitnessTestFn  = runContainerFitnessTest
	generateRepoMapFn          = GenerateRepoMap
	generateMemoryMapFn        = GenerateMemoryMap
	runSproutPreflightChecksFn = runSproutPreflightChecks
	runVerifierCommandFn       = runVerifierCommand
	runTreeSitterScanFn        = runTreeSitterScan
)

func (d *DockerOrchestrator) resolveLLMClient() *llm.Client {
	var spec llm.ProviderSpec
	if d != nil && d.IsCoordinator {
		spec = llm.ResolveCoordinatorProviderSpec()
	} else if d != nil && strings.TrimSpace(d.Provider) != "" && strings.TrimSpace(d.Model) != "" {
		spec = llm.ResolveModelProviderSpec(d.Provider, d.Model)
	} else {
		tier := llm.TierPremium
		if d != nil && d.Tier != "" {
			tier = d.Tier
		}
		spec = llm.ResolveTierProviderSpec(tier)
	}

	if d != nil && strings.TrimSpace(d.BaseURL) != "" {
		spec.BaseURL = strings.TrimSpace(d.BaseURL)
		if spec.Provider == "local" {
			spec.BaseURLs = llm.LocalInferenceBaseURLs(spec.BaseURL)
		} else {
			spec.BaseURLs = []string{spec.BaseURL}
		}
	}

	client := llm.NewClient(spec)
	if d != nil && d.Temperature > 0 {
		client.SetTemperature(d.Temperature)
	}
	return client
}

func (d *DockerOrchestrator) RunSprout(ctx context.Context, taskPrompt string) (result string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx := context.WithoutCancel(ctx)

	if err := runSproutPreflightChecksFn(ctx); err != nil {
		return "", err
	}

	stepID := strings.TrimSpace(d.StepID)
	if stepID == "" {
		stepID = newSproutExecutionID("step")
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
		fmt.Fprintln(os.Stderr, "⚠️ Substrate is configured as READONLY. Discarding terrarium modifications.")
	}

	sourcePath := plan.hostPath
	mountPath := sourcePath
	statusPath := strings.TrimSpace(d.StatusPath)
	gitRepo := false
	var hostStashed bool
	var hostRestorePath string
	var cleanup func()

	defer func() {
		if !hostStashed || strings.TrimSpace(hostRestorePath) == "" {
			return
		}
		if restoreErr := restoreHostStashFn(cleanupCtx, hostRestorePath); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()
	extraEnv := make([]string, 0, 2)
	if plan.readOnly {
		extraEnv = append(extraEnv, "TENDRIL_READONLY=true")
	}
	// ssh/none/app substrates authenticate without the ambient PAT — keep it out
	// of the terrarium so it is never exposed to sprout code (RFC #222).
	if m := plan.credential.Method; m == CredentialSSH || m == CredentialNone || m == CredentialApp {
		extraEnv = append(extraEnv, suppressGitHubPATEnvSentinel+"=true")
	}

	if plan.remoteClone {
		clonedPath, persistent, err := cloneNamedForeignSubstrate(plan.name, plan.cloneURL, plan.cloneBranch, plan.credential)
		if err != nil {
			return "", err
		}

		sourcePath = clonedPath
		mountPath = clonedPath
		statusPath = ""
		gitRepo = isGitRepo(sourcePath)
		// Ephemeral checkouts are removed after the run; managed/path checkouts
		// persist (they are OT-owned or user-chosen and refreshed on reuse).
		if !persistent {
			cleanup = func() {
				_ = os.RemoveAll(clonedPath)
			}
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
			if existing, err := loadSproutStatus(statusPath); err != nil {
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
			branchOutput, err := runGitCommand(ctx, sourcePath, "rev-parse", "--abbrev-ref", "HEAD")
			if err == nil {
				currentBranch := strings.TrimSpace(branchOutput)
				if currentBranch == "main" || currentBranch == "master" {
					newBranch := fmt.Sprintf("sprout/task-%s", stepID)
					fmt.Fprintf(os.Stderr, "🛡️  Branch Protection: Auto-branching from %s to %s\n", currentBranch, newBranch)
					if _, err := runGitCommand(ctx, sourcePath, "checkout", "-b", newBranch); err != nil {
						return "", fmt.Errorf("branch protection failed: could not create isolation branch %s: %w", newBranch, err)
					}
				}
			}
			hostStashed, err = stashHostWorkspaceFn(ctx, sourcePath, stepID)
			if err != nil {
				return "", err
			}
			if hostStashed {
				hostRestorePath = sourcePath
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
			fmt.Fprintf(os.Stderr, "⚠️ Directory %s is not a git repository. Shadow Git terrariuming disabled.\n", sourcePath)
		}
	}

	if d.Genotype != "" {
		if err := stagePlasmidsForGenotype(sourcePath, mountPath, d.Genotype); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return "", err
		}
	}

	repoMapMarkdown, err := generateRepoMapFn(ctx, mountPath)
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

	memoryMapMarkdown, memErr := generateMemoryMapFn(ctx, mountPath)
	if memErr == nil && memoryMapMarkdown != "" {
		memoryMapPath := filepath.Join(mountPath, ".tendril", "genome", "memorymap.md")
		_ = os.WriteFile(memoryMapPath, []byte(memoryMapMarkdown), 0o644)
	}

	imageName := d.resolveImageName(mountPath)
	if err := ensureSproutImageFn(ctx, imageName); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return "", err
	}

	// Use the substrate-configured provider if set, otherwise fall back to env/default.
	providerName := resolveTerrariumProviderName(d)
	if plan.provider != "" {
		providerName = plan.provider
	}
	session, err := startTerrariumSessionFn(ctx, providerName, imageName, mountPath, plan.command, extraEnv...)
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

	agent, err := newAgentFn(ctx, mountPath, sourcePath, d.Genotype, d.resolveLLMClient(), session, d.EventBus, stepID)
	if err != nil {
		return "", err
	}

	agentResult, runErr := agent.Run(ctx, taskPrompt)

	if err := session.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Sprout session shutdown issue: %v\n", err)
	}

	if !gitRepo {
		if runErr != nil {
			return "", runErr
		}
		return agentResult.Response, nil
	}

	if plan.readOnly {
		if runErr != nil {
			return "", runErr
		}
		return agentResult.Response, nil
	}

	var statusRelPath string
	if statusPath != "" {
		var err error
		statusRelPath, err = workspaceRelativePath(sourcePath, statusPath)
		if err != nil {
			return "", err
		}
	}

	modifiedFiles, diffErr := collectStageableFilesFn(ctx, mountPath, statusRelPath)
	if diffErr != nil {
		return "", diffErr
	}

	gitDiff, diffErr := collectGitDiffFn(ctx, mountPath)
	if diffErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to collect git diff for epigenetic chronicler: %v\n", diffErr)
	}

	executionStatus := sproutExecutionStatus{
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

	commitHash, commitErr := commitTerrariumExecutionFn(ctx, mountPath, sourcePath, statusPath, executionStatus, taskPrompt, plan.credential.Sign)
	if commitErr != nil {
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
		if pushErr := pushTerrariumCommitFn(ctx, mountPath, plan.cloneBranch, plan.credential); pushErr != nil {
			if runErr != nil {
				return "", errors.Join(runErr, pushErr)
			}
			return "", pushErr
		}
	} else {
		mergeErr := mergeTerrariumCommitFn(ctx, sourcePath, commitHash)
		if mergeErr != nil {
			if runErr != nil {
				return "", errors.Join(runErr, mergeErr)
			}
			return "", mergeErr
		}
	}

	if runErr != nil {
		return "", runErr
	}

	if gitDiff != "" {
		chronicler := newEpigeneticChroniclerForTier(sourcePath, llm.TierCheapest)
		if err := chronicler.TranscribeLearnings(ctx, agentResult.Transcript, gitDiff, session.Logs()); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Epigenetic chronicler skipped: %v\n", err)
		}
	}

	return agentResult.Response, nil
}

func (d *DockerOrchestrator) resolveImageName(workspace string) string {
	if trimmed := strings.TrimSpace(d.ImageName); trimmed != "" {
		return trimmed
	}

	// A go.mod at the workspace root is the definitive marker of a Go module —
	// the same role package.json plays for node — so it must win before the
	// extension heuristics below. Otherwise a Go-primary repo that carries a
	// TypeScript subtree (e.g. a ui/ front-end) resolves to the toolchain-less
	// typescript image and every `go build`/`go test` step fails.
	if _, err := os.Stat(filepath.Join(workspace, "go.mod")); err == nil {
		return "opentendril-go:latest"
	}

	if _, err := os.Stat(filepath.Join(workspace, "package.json")); err == nil {
		return "opentendril-node:latest"
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
		return coreRoot, filepath.Join(coreRoot, "sprouts", "go", "Dockerfile"), nil
	case macrophageFuzzImage:
		return coreRoot, filepath.Join(coreRoot, "sprouts", "go-fuzz", "Dockerfile"), nil
	case verifierImage:
		return coreRoot, filepath.Join(coreRoot, "sprouts", "go-verifier", "Dockerfile"), nil
	case treeSitterImage:
		return filepath.Join(coreRoot, "sprouts", "tree-sitter"), filepath.Join(coreRoot, "sprouts", "tree-sitter", "Dockerfile"), nil
	case "opentendril-typescript:latest":
		return coreRoot, filepath.Join(coreRoot, "sprouts", "typescript", "Dockerfile"), nil
	case "opentendril-node:latest":
		return coreRoot, filepath.Join(coreRoot, "sprouts", "node", "Dockerfile"), nil
	case "opentendril-python:latest":
		return filepath.Join(coreRoot, "sprouts", "python"), filepath.Join(coreRoot, "sprouts", "python", "Dockerfile"), nil
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
		"sprouts":      {},
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

type terrariumToolSession struct {
	terrarium terrarium.Terrarium
}

func startTerrariumSession(ctx context.Context, providerName, imageName string, mountPath string, command []string, extraEnv ...string) (toolSession, error) {
	provider, err := terrarium.NewProvider(ctx, providerName)
	if err != nil {
		return nil, err
	}

	instance, err := provider.Create(ctx, terrarium.TerrariumSpec{
		Image:          imageName,
		WorkingDir:     "/app",
		NetworkMode:    terrarium.NetworkModeNone,
		RunAsUser:      "1000:1000",
		CPUQuota:       "1.0",
		MemoryLimitMB:  2048,
		ReadOnlyRootFS: false,
		PidsLimit:      512,
		Timeout:        10 * time.Minute,
		Mounts: []terrarium.MountSpec{
			{
				Source: mountPath,
				Target: "/app",
			},
		},
		Command:     command,
		Environment: buildTerrariumEnvironment(extraEnv...),
	})
	if err != nil {
		return nil, err
	}

	return &terrariumToolSession{terrarium: instance}, nil
}

func resolveTerrariumProviderName(d *DockerOrchestrator) string {
	if providerName := strings.TrimSpace(os.Getenv(terrariumProviderEnvKey)); providerName != "" {
		return providerName
	}
	if d == nil {
		return terrarium.ProviderDocker
	}

	switch strings.ToLower(strings.TrimSpace(d.Substrate)) {
	case terrarium.ProviderDocker, terrarium.ProviderGVisor, terrarium.ProviderHost:
		return strings.ToLower(strings.TrimSpace(d.Substrate))
	default:
		return terrarium.ProviderDocker
	}
}

func (s *terrariumToolSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
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

func (s *terrariumToolSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.terrarium == nil {
		return ToolResponse{}, fmt.Errorf("terrarium session is not active")
	}

	payload, err := json.Marshal(call)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("encode tool call: %w", err)
	}

	result, err := s.terrarium.Run(ctx, terrarium.CommandSpec{Stdin: payload})
	if err != nil {
		return ToolResponse{}, err
	}

	var response ToolResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &response); err != nil {
		return ToolResponse{}, fmt.Errorf("decode tool response: %w (payload: %s)", err, strings.TrimSpace(result.Stdout))
	}

	return response, nil
}

func (s *terrariumToolSession) Close() error {
	if s == nil || s.terrarium == nil {
		return nil
	}
	return s.terrarium.Stop(context.Background())
}

func (s *terrariumToolSession) Logs() string {
	if s == nil || s.terrarium == nil {
		return ""
	}

	logs, err := s.terrarium.SnapshotLogs(context.Background())
	if err != nil {
		return ""
	}
	return logs.Stderr
}

// suppressGitHubPATEnvSentinel, when passed via extraEnv, keeps the ambient
// GitHub PAT out of the terrarium (used for ssh/none substrates). It is stripped
// from the resulting environment and never surfaces in the container.
const suppressGitHubPATEnvSentinel = "TENDRIL_SUPPRESS_GITHUB_PAT"

func buildTerrariumEnvironment(extraEnv ...string) map[string]string {
	values := make(map[string]string)

	for _, key := range []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GOOGLE_API_KEY",
		"GROK_API_KEY",
		"OPENROUTER_API_KEY",
		"OPENTENDRIL_API_KEY",
		"NVIDIA_API_KEY",
		"DEFAULT_LLM_PROVIDER",
		"LOCAL_INFERENCE_URL",
		"LOCAL_MODEL_NAME",
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values[key] = value
		}
	}

	suppressPAT := false
	for _, entry := range extraEnv {
		if strings.TrimSpace(entry) == suppressGitHubPATEnvSentinel+"=true" {
			suppressPAT = true
		}
	}

	// GitHub PAT: accept either variable name on the host and expose the
	// resolved value under both names inside the terrarium — unless suppressed
	// because the substrate authenticates over SSH or anonymously.
	if !suppressPAT {
		if _, pat := resolveGitHubPAT(); pat != "" {
			values[gitHubTokenEnv] = pat
			values[gitHubPATLegacyEnv] = pat
		}
	}

	for _, entry := range extraEnv {
		key, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		if strings.TrimSpace(key) == suppressGitHubPATEnvSentinel {
			continue // internal sentinel — never expose it to the terrarium
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

func runSproutPreflightChecks(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.CommandContext(ctx, "docker", "info")
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = output
		return fmt.Errorf("❌ Docker daemon is not responding. OpenTendril requires Docker to spawn secure Sprouts.")
	}

	env := buildTerrariumEnvironment()
	if !strings.EqualFold(strings.TrimSpace(env["DEFAULT_LLM_PROVIDER"]), "local") {
		return nil
	}

	inferenceURL := strings.TrimSpace(env["LOCAL_INFERENCE_URL"])
	if inferenceURL == "" {
		inferenceURL = "http://localhost:11434/v1"
	}

	return checkLocalInferenceReachable(ctx, inferenceURL)
}

func checkLocalInferenceReachable(ctx context.Context, inferenceURL string) error {
	checkURL := hostInferenceHealthURL(inferenceURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if isConnectionRefused(err) {
			return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
		}
		return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return nil
	}

	return fmt.Errorf("❌ Ollama is not responding at %s. Please ensure Ollama is running.", inferenceURL)
}

func hostInferenceHealthURL(inferenceURL string) string {
	trimmed := strings.TrimSpace(inferenceURL)
	trimmed = strings.ReplaceAll(trimmed, "host.docker.internal", "localhost")

	if strings.HasSuffix(trimmed, "/v1") {
		return strings.TrimSuffix(trimmed, "/v1") + "/api/tags"
	}
	if strings.HasSuffix(trimmed, "/v1/") {
		return strings.TrimSuffix(trimmed, "/v1/") + "/api/tags"
	}

	if strings.Contains(trimmed, "/api/") {
		return trimmed
	}

	return strings.TrimRight(trimmed, "/") + "/api/tags"
}

func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if sysErr, ok := opErr.Err.(syscall.Errno); ok && sysErr == syscall.ECONNREFUSED {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "connection refused")
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

	shadowPath := filepath.Join(os.TempDir(), fmt.Sprintf("opentendril-terrarium-%s", runID))

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

// injectMycorrhizalCache hard-links dependency directories from the host to the shadow terrarium.
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

type sproutExecutionStatus struct {
	StepID        string   `json:"stepId"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
	Timestamp     string   `json:"timestamp"`
	FilesModified []string `json:"filesModified"`
}

func newSproutExecutionID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
}

func runGitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitCommandWithEnv(ctx, dir, nil, args...)
}

// runGitCommandWithEnv runs git with additional environment entries appended to
// the process environment (e.g. GIT_SSH_COMMAND for SSH-authenticated pushes).
func runGitCommandWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
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

func loadSproutStatus(path string) (*sproutExecutionStatus, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tendril status %s: %w", path, err)
	}

	var status sproutExecutionStatus
	if err := json.Unmarshal(content, &status); err != nil {
		return nil, fmt.Errorf("decode tendril status %s: %w", path, err)
	}

	return &status, nil
}

func writeSproutStatus(path string, status sproutExecutionStatus) error {
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

func commitTerrariumExecution(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, sign ResolvedSigning) (string, error) {
	stagePaths := append([]string{}, executionStatus.FilesModified...)

	if strings.TrimSpace(statusPath) != "" {
		statusRelPath, err := workspaceRelativePath(sourcePath, statusPath)
		if err != nil {
			return "", err
		}

		statusTerrariumPath := filepath.Join(mountPath, filepath.FromSlash(statusRelPath))
		if err := writeSproutStatus(statusTerrariumPath, executionStatus); err != nil {
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

	commitMessage := buildSproutCommitMessage(executionStatus.StepID, taskPrompt, executionStatus.Status, executionStatus.Error)
	// Signing config (`-c ...`) must precede the `commit` subcommand.
	signArgs := signingGitConfigArgs(sign)
	commitArgs := append(append([]string{}, signArgs...), "commit", "-m", commitMessage)
	if len(uniqueStagePaths) == 0 {
		commitArgs = append(append([]string{}, signArgs...), "commit", "--allow-empty", "-m", commitMessage)
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

func mergeTerrariumCommit(ctx context.Context, sourcePath, commitHash string) error {
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

func buildSproutCommitMessage(stepID, taskPrompt, status, failureError string) string {
	if strings.ToLower(strings.TrimSpace(status)) == "failed" {
		return fmt.Sprintf("tendril(%s) [INCOMPLETE]: %s", strings.TrimSpace(stepID), summarizeSproutFailureError(failureError))
	}

	return fmt.Sprintf("tendril(%s): %s", strings.TrimSpace(stepID), summarizeSproutPrompt(taskPrompt))
}

func summarizeSproutPrompt(taskPrompt string) string {
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

func summarizeSproutFailureError(failureError string) string {
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

// cloneForeignSubstrate clones a remote repository into a temporary terrarium.
func cloneForeignSubstrate(url, branch string) (string, error) {
	path, _, err := cloneNamedForeignSubstrate("", url, branch, ResolvedCredential{})
	return path, err
}

// cloneNamedForeignSubstrate materializes a foreign substrate and returns its
// path plus whether that path is persistent (managed/path checkout) — the caller
// removes only non-persistent (ephemeral) checkouts.
func cloneNamedForeignSubstrate(name, url, branch string, cred ResolvedCredential) (string, bool, error) {
	checkout, err := resolveCheckoutPlan(name, cred.Checkout)
	if err != nil {
		return "", false, err
	}

	// Resolve git auth (mints a fresh GitHub App token when needed). The token
	// travels only in the process environment via an inline credential helper —
	// never in the clone URL, the command line, or the persisted .git/config, so
	// it can't leak into the mounted terrarium.
	gitEnv, err := materializeGitAuth(context.Background(), cred, url)
	if err != nil {
		return "", false, err
	}

	dest := checkout.dir
	if dest == "" {
		if dest, err = ephemeralCheckoutPath(name); err != nil {
			return "", false, err
		}
	}

	// Reuse a persistent checkout that already exists: refresh it to a clean,
	// current tree instead of failing to clone into a non-empty directory.
	if checkout.persistent && isGitRepo(dest) {
		if err := refreshExistingCheckout(dest, branch, gitEnv); err != nil {
			return "", false, err
		}
		return dest, true, nil
	}
	if checkout.persistent {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", false, fmt.Errorf("prepare checkout dir: %w", err)
		}
	}

	args := []string{"-c", "protocol.ext.allow=never", "clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", url, dest)

	cmd := exec.Command("git", args...)
	if len(gitEnv) > 0 {
		cmd.Env = append(os.Environ(), gitEnv...)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}

	return dest, checkout.persistent, nil
}

func pushTerrariumCommit(ctx context.Context, mountPath, branch string, cred ResolvedCredential) error {
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

	// Re-resolve auth for the push against the (tokenless) origin URL. For a
	// GitHub App this mints a fresh installation token; the credential travels
	// only in the process environment, never persisted to .git/config.
	originURL, _ := runGitCommand(ctx, mountPath, "remote", "get-url", "origin")
	pushEnv, authErr := materializeGitAuth(ctx, cred, strings.TrimSpace(originURL))
	if authErr != nil {
		return authErr
	}
	if _, err := runGitCommandWithEnv(ctx, mountPath, pushEnv, "push", "origin", "HEAD:"+targetBranch); err != nil {
		return err
	}

	return nil
}

func stagePlasmidsForGenotype(sourcePath, targetPath, genotypeName string) error {
	genotype, err := loadGenotypeContext(sourcePath, genotypeName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to read genotype %s for plasmid staging: %v\n", genotypeName, err)
		return nil
	}
	if genotype == nil {
		fmt.Fprintf(os.Stderr, "⚠️ Genotype %s not found for plasmid staging\n", genotypeName)
		return nil
	}
	if len(genotype.Plasmids) == 0 {
		return nil
	}

	var sigVerifyFailed bool
	allowedPlasmids := make(map[string]struct{}, len(genotype.Plasmids))
	for _, allowed := range genotype.Plasmids {
		allowed = strings.TrimSpace(allowed)
		if allowed != "" {
			allowedPlasmids[strings.ToLower(allowed)] = struct{}{}
		}
	}

	for _, name := range genotype.Plasmids {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := allowedPlasmids[strings.ToLower(name)]; !ok {
			fmt.Fprintf(os.Stderr, "⚠️ Skipping staging of non-allowlisted plasmid %s\n", name)
			continue
		}

		denied := false
		for _, deny := range genotype.DenyPlasmids {
			if strings.EqualFold(name, deny) {
				denied = true
				break
			}
		}
		if denied {
			fmt.Fprintf(os.Stderr, "⚠️ Skipping staging of denied plasmid %s\n", name)
			continue
		}

		sourceFile, err := FindPlasmidSource(sourcePath, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to locate plasmid %s: %v\n", name, err)
			continue
		}

		if genotype.RequirePlasmidSignatures {
			key, err := NodeSigningKey()
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to load node signing key for plasmid %s: %v\n", name, err)
				sigVerifyFailed = true
				continue
			}
			if err := VerifyPlasmidSignature(sourceFile, key); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️ Failed to verify plasmid signature for %s: %v\n", name, err)
				sigVerifyFailed = true
				continue
			}
		}

		destDir := filepath.Join(targetPath, ".tendril", "genome")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to create terrarium genome directory: %v\n", err)
			continue
		}

		destFile := filepath.Join(destDir, filepath.Base(sourceFile))
		if err := CopyMarkdownFile(sourceFile, destFile); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to stage terrarium plasmid %s: %v\n", name, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "🧬 Staged terrarium plasmid: %s -> %s\n", name, destFile)
	}

	if sigVerifyFailed {
		return fmt.Errorf("one or more required plasmid signature checks failed")
	}
	return nil
}
