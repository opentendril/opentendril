package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

func TestLoadSubstratesConfigSearchOrder(t *testing.T) {
	t.Run("current dir wins", func(t *testing.T) {
		root, cwd := prepareSubstrateConfigRepo(t)
		writeSubstratesYAML(t, filepath.Join(root, "substrates.yaml"), `
substrates:
  repo-root:
    url: https://example.com/repo-root.git
`)
		writeSubstratesYAML(t, filepath.Join(cwd, "substrates.yaml"), `
substrates:
  current-dir:
    url: https://example.com/current-dir.git
`)

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}
		if config == nil {
			t.Fatalf("expected config, got nil")
		}
		if len(config.Substrates) != 1 {
			t.Fatalf("substrate count = %d, want 1", len(config.Substrates))
		}
		if _, ok := config.Substrates["current-dir"]; !ok {
			t.Fatalf("expected current-dir substrate from cwd to win, got %#v", config.Substrates)
		}
	})

	t.Run(".tendril wins over repo root", func(t *testing.T) {
		root, cwd := prepareSubstrateConfigRepo(t)
		writeSubstratesYAML(t, filepath.Join(root, "substrates.yaml"), `
substrates:
  repo-root:
    url: https://example.com/repo-root.git
`)
		writeSubstratesYAML(t, filepath.Join(cwd, ".tendril", "substrates.yaml"), `
substrates:
  tendril-dir:
    url: https://example.com/tendril-dir.git
`)

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}
		if config == nil {
			t.Fatalf("expected config, got nil")
		}
		if len(config.Substrates) != 1 {
			t.Fatalf("substrate count = %d, want 1", len(config.Substrates))
		}
		if _, ok := config.Substrates["tendril-dir"]; !ok {
			t.Fatalf("expected tendril-dir substrate from .tendril to win, got %#v", config.Substrates)
		}
	})

	t.Run("repo root fallback", func(t *testing.T) {
		root, _ := prepareSubstrateConfigRepo(t)
		writeSubstratesYAML(t, filepath.Join(root, "substrates.yaml"), `
substrates:
  repo-root:
    url: https://example.com/repo-root.git
`)

		config, err := LoadSubstratesConfig("")
		if err != nil {
			t.Fatalf("LoadSubstratesConfig failed: %v", err)
		}
		if config == nil {
			t.Fatalf("expected config, got nil")
		}
		if len(config.Substrates) != 1 {
			t.Fatalf("substrate count = %d, want 1", len(config.Substrates))
		}
		if _, ok := config.Substrates["repo-root"]; !ok {
			t.Fatalf("expected repo-root substrate from repo root to win, got %#v", config.Substrates)
		}
	})
}

func TestResolveSubstrateAndPlanOverrides(t *testing.T) {
	root := t.TempDir()
	substratePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(substratePath, 0o755); err != nil {
		t.Fatalf("mkdir substrate path: %v", err)
	}
	t.Setenv("TOKEN_ENV", "token-value")

	cwd := chdirToTempDir(t)

	writeSubstratesYAML(t, filepath.Join(cwd, "substrates.yaml"), fmt.Sprintf(`
substrates:
  core:
    path: %s
    url: https://example.com/core.git
    branch: main
    auth: TOKEN_ENV
    readonly: true
  remote:
    url: https://example.com/remote.git
    branch: develop
`, substratePath))

	config, err := LoadSubstratesConfig("")
	if err != nil {
		t.Fatalf("LoadSubstratesConfig failed: %v", err)
	}

	spec, isName := ResolveSubstrate("core", config)
	if !isName {
		t.Fatalf("expected core to resolve as a named substrate")
	}
	if spec == nil {
		t.Fatalf("expected substrate spec, got nil")
	}
	if spec.Path != substratePath {
		t.Fatalf("resolved path = %q, want %q", spec.Path, substratePath)
	}
	if spec.URL != "https://example.com/core.git" {
		t.Fatalf("resolved URL = %q, want https://example.com/core.git", spec.URL)
	}
	if spec.Branch != "main" {
		t.Fatalf("resolved branch = %q, want main", spec.Branch)
	}
	if spec.Auth.Env != "TOKEN_ENV" {
		t.Fatalf("resolved auth env = %q, want TOKEN_ENV", spec.Auth.Env)
	}
	if spec.Auth.Method != "pat" {
		t.Fatalf("scalar auth should decode to method pat, got %q", spec.Auth.Method)
	}
	if !spec.ReadOnly {
		t.Fatalf("expected read-only substrate")
	}

	plainSpec, plainIsName := ResolveSubstrate("/tmp/standalone", config)
	if plainIsName {
		t.Fatalf("expected path substrate to not be treated as a named substrate")
	}
	if plainSpec == nil || plainSpec.Path != "/tmp/standalone" {
		t.Fatalf("expected path fallback to preserve the input path, got %#v", plainSpec)
	}

	localPlan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{
		Substrate: "core",
	}, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan(local) failed: %v", err)
	}
	if localPlan.remoteClone {
		t.Fatalf("expected local plan to stay local")
	}
	if localPlan.hostPath != substratePath {
		t.Fatalf("local hostPath = %q, want %q", localPlan.hostPath, substratePath)
	}
	if localPlan.cloneURL != "https://example.com/core.git" {
		t.Fatalf("local cloneURL = %q, want config URL", localPlan.cloneURL)
	}
	if localPlan.cloneBranch != "main" {
		t.Fatalf("local cloneBranch = %q, want config branch", localPlan.cloneBranch)
	}
	if !localPlan.readOnly {
		t.Fatalf("expected local plan to inherit readOnly")
	}
	if localPlan.authRef != "TOKEN_ENV" {
		t.Fatalf("local authRef = %q, want TOKEN_ENV", localPlan.authRef)
	}

	overridePlan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{
		Substrate:       "core",
		SubstrateURL:    "https://override.example/core.git",
		SubstrateBranch: "release",
	}, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan(override) failed: %v", err)
	}
	if !overridePlan.remoteClone {
		t.Fatalf("expected URL override to trigger remote clone mode")
	}
	if overridePlan.cloneURL != "https://override.example/core.git" {
		t.Fatalf("override cloneURL = %q, want explicit override", overridePlan.cloneURL)
	}
	if overridePlan.cloneBranch != "release" {
		t.Fatalf("override cloneBranch = %q, want explicit override", overridePlan.cloneBranch)
	}
	if overridePlan.hostPath != substratePath {
		t.Fatalf("override hostPath = %q, want %q", overridePlan.hostPath, substratePath)
	}
	if !overridePlan.readOnly {
		t.Fatalf("expected override plan to retain readOnly")
	}

	remotePlan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{
		Substrate: "remote",
	}, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan(remote) failed: %v", err)
	}
	if !remotePlan.remoteClone {
		t.Fatalf("expected remote-only substrate to clone dynamically")
	}
	if remotePlan.cloneURL != "https://example.com/remote.git" {
		t.Fatalf("remote cloneURL = %q, want config URL", remotePlan.cloneURL)
	}
	if remotePlan.cloneBranch != "develop" {
		t.Fatalf("remote cloneBranch = %q, want develop", remotePlan.cloneBranch)
	}
}

func TestRunSproutReadOnlySkipsHostMutations(t *testing.T) {
	root := t.TempDir()
	if _, err := runGitCommand(context.Background(), root, "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	chdirToTempDir(t)
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir repo root: %v", err)
	}

	writeSubstratesYAML(t, filepath.Join(root, "substrates.yaml"), fmt.Sprintf(`
substrates:
  readonly:
    path: %s
    url: https://example.com/readonly.git
    branch: main
    readonly: true
`, root))

	originalPreflight := runSproutPreflightChecksFn
	originalEnsure := ensureSproutImageFn
	originalStart := startTerrariumSessionFn
	originalNewSprout := newSproutFn
	originalStash := stashHostWorkspaceFn
	originalRestore := restoreHostStashFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalInjectCache := injectMycorrhizalCacheFn
	originalCollectFiles := collectStageableFilesFn
	originalCollectDiff := collectGitDiffFn
	originalCommit := commitTerrariumExecutionFn
	originalMerge := mergeTerrariumCommitFn
	originalPush := pushTerrariumCommitFn

	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		ensureSproutImageFn = originalEnsure
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNewSprout
		stashHostWorkspaceFn = originalStash
		restoreHostStashFn = originalRestore
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		injectMycorrhizalCacheFn = originalInjectCache
		collectStageableFilesFn = originalCollectFiles
		collectGitDiffFn = originalCollectDiff
		commitTerrariumExecutionFn = originalCommit
		mergeTerrariumCommitFn = originalMerge
		pushTerrariumCommitFn = originalPush
	})

	runSproutPreflightChecksFn = func(ctx context.Context) error { return nil }

	var capturedExtraEnv []string
	var capturedRepoMap string
	ensureSproutImageFn = func(ctx context.Context, imageName string) error {
		return nil
	}
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, command []string, extraEnv ...string) (toolSession, error) {
		capturedExtraEnv = append([]string{}, extraEnv...)
		repoMapPath := filepath.Join(mountPath, ".tendril", "genome", "repomap.md")
		content, err := os.ReadFile(repoMapPath)
		if err != nil {
			t.Fatalf("expected repo map plasmid at %s: %v", repoMapPath, err)
		}
		capturedRepoMap = string(content)
		return &stubToolSession{}, nil
	}
	origNewSproutFn := newSproutFn
	newSproutFn = func(ctx context.Context, workspace string, genotypeRoot string, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID string, sessionID string) (sproutRunner, error) {
		return &stubSproutRunner{result: sproutResult{Response: "read-only result", Transcript: "transcript"}}, nil
	}
	defer func() {
		newSproutFn = origNewSproutFn
	}()
	stashHostWorkspaceFn = func(ctx context.Context, root, runID string) (bool, error) {
		t.Fatalf("stashHostWorkspace should not run for read-only substrates")
		return false, nil
	}
	restoreHostStashFn = func(ctx context.Context, root string) error {
		t.Fatalf("restoreHostStash should not run for read-only substrates")
		return nil
	}
	createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) {
		shadowPath := filepath.Join(root, "shadow-worktree")
		if err := os.MkdirAll(shadowPath, 0o755); err != nil {
			return "", err
		}
		return shadowPath, nil
	}
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {
		_ = os.RemoveAll(shadowPath)
	}
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
	collectStageableFilesFn = func(ctx context.Context, mountPath string, excludedPaths ...string) ([]string, error) {
		t.Fatalf("collectStageableFiles should not run for read-only substrates")
		return nil, nil
	}
	collectGitDiffFn = func(ctx context.Context, mountPath string) (string, error) {
		t.Fatalf("collectGitDiff should not run for read-only substrates")
		return "", nil
	}
	commitTerrariumExecutionFn = func(ctx context.Context, mountPath, sourcePath, statusPath string, executionStatus sproutExecutionStatus, taskPrompt string, credential ResolvedCredential) (string, error) {
		t.Fatalf("commitTerrariumExecution should not run for read-only substrates")
		return "", nil
	}
	mergeTerrariumCommitFn = func(ctx context.Context, sourcePath, commitHash string) error {
		t.Fatalf("mergeTerrariumCommit should not run for read-only substrates")
		return nil
	}
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential) error {
		t.Fatalf("pushTerrariumCommit should not run for read-only substrates")
		return nil
	}

	output, err := (&DockerOrchestrator{
		Substrate: "readonly",
		StepID:    "step-1",
	}).RunSprout(context.Background(), "explain the read-only flow")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}
	if output.Output != "read-only result" {
		t.Fatalf("RunSprout output = %q, want read-only result", output.Output)
	}
	if output.Outcome != SproutOutcomeComplete {
		t.Fatalf("RunSprout outcome = %q, want %q (read-only runs cannot measure changes)", output.Outcome, SproutOutcomeComplete)
	}

	if !containsString(capturedExtraEnv, "TENDRIL_READONLY=true") {
		t.Fatalf("expected TENDRIL_READONLY=true to be passed to the container, got %#v", capturedExtraEnv)
	}
	if !strings.Contains(capturedRepoMap, "# Repo Map") {
		t.Fatalf("expected repo map plasmid content, got %q", capturedRepoMap)
	}
}

type stubToolSession struct{}

func (s *stubToolSession) ListAvailableTools(ctx context.Context) ([]ToolDefinition, error) {
	return nil, nil
}

func (s *stubToolSession) Call(ctx context.Context, call ToolCall) (ToolResponse, error) {
	return ToolResponse{}, nil
}

func (s *stubToolSession) Close() error {
	return nil
}

func (s *stubToolSession) Logs() string {
	return ""
}

type stubSproutRunner struct {
	result sproutResult
}

func (s *stubSproutRunner) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	return s.result, nil
}

func prepareSubstrateConfigRepo(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	if _, err := runGitCommand(context.Background(), root, "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	cwd := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(cwd, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir cwd .tendril: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	return root, cwd
}

func chdirToTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	return dir
}

func writeSubstratesYAML(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	trimmed := strings.TrimSpace(content)
	if trimmed != "" {
		trimmed += "\n"
	}
	if err := os.WriteFile(path, []byte(trimmed), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestSubstrateCredentialSchemaParsing pins the RFC slice-1 schema:
// back-compatible scalar auth, mapping auth (ssh/none), signing, checkout, and
// reusable credential profiles.
func TestSubstrateCredentialSchemaParsing(t *testing.T) {
	cwd := chdirToTempDir(t)
	writeSubstratesYAML(t, filepath.Join(cwd, "substrates.yaml"), `
credentials:
  work:
    auth:
      method: pat
      env: GITHUB_TOKEN_WORK
    sign:
      method: ssh
      key: ~/.ssh/id_work

substrates:
  legacy:
    url: https://example.com/legacy.git
    auth: GITHUB_TOKEN
  overssh:
    url: git@example.com:org/overssh.git
    auth:
      method: ssh
      key: ~/.ssh/id_ot
    sign:
      method: gpg
      key: ABCD1234
    checkout:
      mode: managed
  public:
    url: https://example.com/public.git
    auth:
      method: none
  profiled:
    url: https://example.com/profiled.git
    profile: work
`)

	config, err := LoadSubstratesConfig("")
	if err != nil {
		t.Fatalf("LoadSubstratesConfig failed: %v", err)
	}
	if config == nil {
		t.Fatalf("expected config, got nil")
	}

	// Back-compat: a bare scalar decodes to method "pat" with the env name.
	legacy := config.Substrates["legacy"]
	if legacy.Auth.Method != "pat" || legacy.Auth.Env != "GITHUB_TOKEN" {
		t.Fatalf("legacy scalar auth = %+v, want {pat GITHUB_TOKEN}", legacy.Auth)
	}

	// Mapping form: ssh method + key, plus signing and checkout.
	overssh := config.Substrates["overssh"]
	if overssh.Auth.Method != "ssh" || overssh.Auth.Key != "~/.ssh/id_ot" {
		t.Fatalf("overssh auth = %+v, want {ssh ~/.ssh/id_ot}", overssh.Auth)
	}
	if overssh.Sign.Method != "gpg" || overssh.Sign.Key != "ABCD1234" {
		t.Fatalf("overssh sign = %+v, want {gpg ABCD1234}", overssh.Sign)
	}
	if overssh.Checkout.Mode != "managed" {
		t.Fatalf("overssh checkout mode = %q, want managed", overssh.Checkout.Mode)
	}

	if config.Substrates["public"].Auth.Method != "none" {
		t.Fatalf("public auth method = %q, want none", config.Substrates["public"].Auth.Method)
	}

	// Profiles parse and normalize.
	if config.Substrates["profiled"].Profile != "work" {
		t.Fatalf("profiled.Profile = %q, want work", config.Substrates["profiled"].Profile)
	}
	work, ok := config.Credentials["work"]
	if !ok {
		t.Fatalf("expected credential profile %q", "work")
	}
	if work.Auth.Env != "GITHUB_TOKEN_WORK" || work.Sign.Method != "ssh" {
		t.Fatalf("work profile = %+v, want auth.env GITHUB_TOKEN_WORK + sign.method ssh", work)
	}
}
