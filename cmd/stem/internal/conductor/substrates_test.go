package conductor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
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

func TestResolveSubstrateExecutionPlanUsesUniqueManagedCheckoutFromStemHome(t *testing.T) {
	stemHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stemHome, ".local", "share", "docker"), 0o755); err != nil {
		t.Fatalf("mkdir docker data-root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(stemHome, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir control plane: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stemHome, ".tendril", "api-key"), []byte("test-key\n"), 0o600); err != nil {
		t.Fatalf("write api-key: %v", err)
	}

	managedRoot := filepath.Join(stemHome, ".tendril", "substrates")
	checkout := filepath.Join(managedRoot, "opentendril")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir managed checkout: %v", err)
	}
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if _, err := runGitCommand(ctx, checkout, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)
	t.Setenv("HOME", stemHome)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(stemHome); err != nil {
		t.Fatalf("chdir stem home: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	snapshotWork := filepath.Join(stemHome, ".local", "share", "docker", "containerd", "daemon",
		"io.containerd.snapshotter.v1.overlayfs", "snapshots", "10", "work", "work")
	if err := os.MkdirAll(snapshotWork, 0o755); err != nil {
		t.Fatalf("mkdir snapshot work: %v", err)
	}
	if err := os.Chmod(snapshotWork, 0); err != nil {
		t.Fatalf("chmod snapshot work: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(snapshotWork, 0o755) })

	config := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"opentendril": {
				URL:      "https://example.com/opentendril.git",
				Branch:   "main",
				Checkout: CheckoutSpec{Mode: "managed"},
			},
		},
	}

	plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{}, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan: %v", err)
	}
	if !plan.named || plan.name != "opentendril" {
		t.Fatalf("plan name = %q named=%v, want the unique managed Substrate", plan.name, plan.named)
	}
	if plan.hostPath != checkout {
		t.Fatalf("hostPath = %q, want the managed checkout %q (not the Stem home)", plan.hostPath, checkout)
	}
}

// Chat/Greenhouse copies only the Substrate name onto the orchestrator. An
// empty managed directory must not look like a ready workspace or the
// Terrarium bind-mounts it as an empty /app. CLI sets SubstrateURL and
// therefore remotes; the name-only path has to reach the same clone.
func TestResolveSubstrateExecutionPlanClonesEmptyManagedCheckoutWithoutExplicitURL(t *testing.T) {
	managedRoot := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)
	placeholder := filepath.Join(managedRoot, "opentendril")
	if err := os.MkdirAll(placeholder, 0o755); err != nil {
		t.Fatalf("mkdir placeholder: %v", err)
	}

	config := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"opentendril": {
				URL:      "https://example.com/opentendril.git",
				Branch:   "main",
				Checkout: CheckoutSpec{Mode: "managed"},
			},
		},
	}

	plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "opentendril"}, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan: %v", err)
	}
	if !plan.remoteClone {
		t.Fatal("empty managed checkout stayed local; chat would mount an empty /app")
	}
	if plan.cloneURL != "https://example.com/opentendril.git" {
		t.Fatalf("cloneURL = %q, want the named Substrate URL", plan.cloneURL)
	}
	if plan.hostPath != placeholder {
		t.Fatalf("hostPath = %q, want the intended managed checkout %q", plan.hostPath, placeholder)
	}
}

func TestResolveSubstrateExecutionPlanEmptyManagedWithoutURLStillAbsent(t *testing.T) {
	managedRoot := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)
	if err := os.MkdirAll(filepath.Join(managedRoot, "missing"), 0o755); err != nil {
		t.Fatalf("mkdir placeholder: %v", err)
	}

	_, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "missing"}, &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"missing": {Checkout: CheckoutSpec{Mode: "managed"}},
		},
	})
	if !errors.Is(err, ErrWorkspaceAbsent) {
		t.Fatalf("err = %v, want ErrWorkspaceAbsent", err)
	}
}

func TestResolveSubstrateExecutionPlanPopulatedManagedCheckoutStaysLocal(t *testing.T) {
	managedRoot := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)
	checkout := filepath.Join(managedRoot, "opentendril")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if _, err := runGitCommand(ctx, checkout, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "opentendril"}, &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"opentendril": {
				URL:      "https://example.com/opentendril.git",
				Branch:   "main",
				Checkout: CheckoutSpec{Mode: "managed"},
			},
		},
	})
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan: %v", err)
	}
	if plan.remoteClone {
		t.Fatal("a populated managed checkout should stay local when the orchestrator has no explicit URL")
	}
	if plan.hostPath != checkout {
		t.Fatalf("hostPath = %q, want %q", plan.hostPath, checkout)
	}
}

// Chat-shaped grow (Substrate name only, no SubstrateURL) must clone an empty
// managed placeholder before the Terrarium mount so /app is the repository.
func TestRunSproutChatPathPopulatesEmptyManagedCheckout(t *testing.T) {
	src := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
	} {
		if _, err := runGitCommand(ctx, src, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "docs", "TERRARIUM.md"), []byte("terrarium notes\n"), 0o644); err != nil {
		t.Fatalf("write TERRARIUM.md: %v", err)
	}
	if _, err := runGitCommand(ctx, src, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, src, "commit", "-q", "-m", "init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	managedRoot := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)
	placeholder := filepath.Join(managedRoot, "opentendril")
	if err := os.MkdirAll(placeholder, 0o755); err != nil {
		t.Fatalf("mkdir placeholder: %v", err)
	}

	cwd := chdirToTempDir(t)
	writeSubstratesYAML(t, filepath.Join(cwd, "substrates.yaml"), fmt.Sprintf(`
substrates:
  opentendril:
    url: %s
    branch: main
    checkout:
      mode: managed
`, src))

	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")

	var mounted string
	originalPreflight := runSproutPreflightChecksFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsure := ensureSproutImageFn
	originalStart := startTerrariumSessionFn
	originalNewSprout := newSproutFn
	originalStash := stashHostWorkspaceFn
	originalCollect := collectStageableFilesFn
	originalDiff := collectGitDiffFn
	originalCommit := commitTerrariumExecutionFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsure
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNewSprout
		stashHostWorkspaceFn = originalStash
		collectStageableFilesFn = originalCollect
		collectGitDiffFn = originalDiff
		commitTerrariumExecutionFn = originalCommit
		mergeTerrariumCommitFn = originalMerge
	})
	runSproutPreflightChecksFn = func(context.Context) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		mounted = mountPath
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		return &stubSproutRunner{result: sproutResult{Response: "edited TERRARIUM.md"}}, nil
	}
	stashHostWorkspaceFn = func(context.Context, string, string) (bool, error) { return false, nil }
	collectStageableFilesFn = func(context.Context, string, ...string) ([]string, error) { return nil, nil }
	collectGitDiffFn = func(context.Context, string) (string, error) { return "", nil }
	commitTerrariumExecutionFn = func(context.Context, string, string, string, sproutExecutionStatus, string, ResolvedCredential) (string, error) {
		return "", nil
	}
	mergeTerrariumCommitFn = func(context.Context, string, string) error { return nil }

	report, err := (&DockerOrchestrator{
		Substrate:        "opentendril",
		StepID:           "step-chat-managed",
		DisableMergeBack: true,
	}).RunSprout(context.Background(), "edit docs/TERRARIUM.md")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Output != "edited TERRARIUM.md" {
		t.Fatalf("output = %q", report.Output)
	}
	if mounted == "" {
		t.Fatal("Terrarium was not given a mount path")
	}
	body, err := os.ReadFile(filepath.Join(mounted, "docs", "TERRARIUM.md"))
	if err != nil {
		t.Fatalf("mounted workspace missing docs/TERRARIUM.md: %v (mount=%q)", err, mounted)
	}
	if string(body) != "terrarium notes\n" {
		t.Fatalf("TERRARIUM.md = %q", body)
	}
	if _, err := os.Stat(filepath.Join(mounted, ".git")); err != nil {
		t.Fatalf("mounted workspace is not a git checkout: %v", err)
	}
}

// Same chat-shaped grow when the empty placeholder sits under a parent git
// repository (Stem home). git-rev-parse would walk up; clone must still
// populate THIS directory so /app is the Substrate, not an empty mount.
func TestRunSproutChatPathPopulatesEmptyManagedCheckoutUnderParentGit(t *testing.T) {
	src := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
	} {
		if _, err := runGitCommand(ctx, src, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "docs", "TERRARIUM.md"), []byte("terrarium notes\n"), 0o644); err != nil {
		t.Fatalf("write TERRARIUM.md: %v", err)
	}
	if _, err := runGitCommand(ctx, src, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, src, "commit", "-q", "-m", "init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	parent := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
		{"commit", "--allow-empty", "-q", "-m", "stem-home"},
	} {
		if _, err := runGitCommand(ctx, parent, args...); err != nil {
			t.Fatalf("parent git %v: %v", args, err)
		}
	}

	managedRoot := filepath.Join(parent, ".tendril", "substrates")
	placeholder := filepath.Join(managedRoot, "opentendril")
	if err := os.MkdirAll(placeholder, 0o755); err != nil {
		t.Fatalf("mkdir placeholder: %v", err)
	}
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)

	cwd := chdirToTempDir(t)
	writeSubstratesYAML(t, filepath.Join(cwd, "substrates.yaml"), fmt.Sprintf(`
substrates:
  opentendril:
    url: %s
    branch: main
    checkout:
      mode: managed
`, src))

	t.Setenv("DEFAULT_LLM_PROVIDER", "google")
	t.Setenv("GOOGLE_API_KEY", "google-key")

	var mounted string
	originalPreflight := runSproutPreflightChecksFn
	originalRepoMap := generateRepoMapFn
	originalMemoryMap := generateMemoryMapFn
	originalEnsure := ensureSproutImageFn
	originalStart := startTerrariumSessionFn
	originalNewSprout := newSproutFn
	originalStash := stashHostWorkspaceFn
	originalCollect := collectStageableFilesFn
	originalDiff := collectGitDiffFn
	originalCommit := commitTerrariumExecutionFn
	originalMerge := mergeTerrariumCommitFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		generateRepoMapFn = originalRepoMap
		generateMemoryMapFn = originalMemoryMap
		ensureSproutImageFn = originalEnsure
		startTerrariumSessionFn = originalStart
		newSproutFn = originalNewSprout
		stashHostWorkspaceFn = originalStash
		collectStageableFilesFn = originalCollect
		collectGitDiffFn = originalDiff
		commitTerrariumExecutionFn = originalCommit
		mergeTerrariumCommitFn = originalMerge
	})
	runSproutPreflightChecksFn = func(context.Context) error { return nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "", nil }
	generateMemoryMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		mounted = mountPath
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		return &stubSproutRunner{result: sproutResult{Response: "edited TERRARIUM.md"}}, nil
	}
	stashHostWorkspaceFn = func(context.Context, string, string) (bool, error) { return false, nil }
	collectStageableFilesFn = func(context.Context, string, ...string) ([]string, error) { return nil, nil }
	collectGitDiffFn = func(context.Context, string) (string, error) { return "", nil }
	commitTerrariumExecutionFn = func(context.Context, string, string, string, sproutExecutionStatus, string, ResolvedCredential) (string, error) {
		return "", nil
	}
	mergeTerrariumCommitFn = func(context.Context, string, string) error { return nil }

	report, err := (&DockerOrchestrator{
		Substrate:        "opentendril",
		StepID:           "step-chat-managed-parent",
		DisableMergeBack: true,
	}).RunSprout(context.Background(), "edit docs/TERRARIUM.md")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if report.Output != "edited TERRARIUM.md" {
		t.Fatalf("output = %q", report.Output)
	}
	if mounted == "" {
		t.Fatal("Terrarium was not given a mount path")
	}
	body, err := os.ReadFile(filepath.Join(mounted, "docs", "TERRARIUM.md"))
	if err != nil {
		t.Fatalf("mounted workspace missing docs/TERRARIUM.md: %v (mount=%q)", err, mounted)
	}
	if string(body) != "terrarium notes\n" {
		t.Fatalf("TERRARIUM.md = %q", body)
	}
	if _, err := os.Stat(filepath.Join(mounted, ".git")); err != nil {
		t.Fatalf("mounted workspace is not a git checkout: %v", err)
	}
}

func TestResolveSubstrateExecutionPlanRefusesStemHomeWithoutASubstrate(t *testing.T) {
	stemHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stemHome, ".local", "share", "docker"), 0o755); err != nil {
		t.Fatalf("mkdir docker data-root: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(stemHome); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	_, err = resolveSubstrateExecutionPlan(&DockerOrchestrator{}, &SubstratesConfig{})
	if err == nil {
		t.Fatal("expected a refusal when the implicit workspace is the Stem home and no Substrate is configured")
	}
	if !strings.Contains(err.Error(), "substrate is required") {
		t.Fatalf("error = %v, want it to require a Substrate", err)
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
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
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
		return &stubSproutRunner{result: sproutResult{Response: "read-only result", Transcript: "transcript", WroteWorkspace: true}}, nil
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
	pushTerrariumCommitFn = func(ctx context.Context, mountPath, branch string, cred ResolvedCredential, allowDefaultBranchCommit bool, stepID string) error {
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
  github:
    auth:
      method: app
      appId: "4276558"
      privateKeyPath: ~/.tendril/app.pem
    commit: API

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
  apisigned:
    url: https://example.com/apisigned.git
    profile: github
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

	// The commit field parses on both a profile and a substrate, and
	// normalizes (trim + lowercase) exactly like auth.method and sign.method.
	github, ok := config.Credentials["github"]
	if !ok {
		t.Fatalf("expected credential profile %q", "github")
	}
	if github.Commit != "api" {
		t.Fatalf("github profile Commit = %q, want normalized %q", github.Commit, "api")
	}
	if config.Substrates["apisigned"].Profile != "github" {
		t.Fatalf("apisigned.Profile = %q, want github", config.Substrates["apisigned"].Profile)
	}
	if config.Substrates["legacy"].Commit != "" {
		t.Fatalf("legacy.Commit = %q, want empty (defaults to local at resolution)", config.Substrates["legacy"].Commit)
	}
}

// TestSubstrateSpecCommitFieldNormalizes proves an inline substrate-level
// commit field (not just a profile's) is trimmed and lowercased the same way.
func TestSubstrateSpecCommitFieldNormalizes(t *testing.T) {
	cwd := chdirToTempDir(t)
	writeSubstratesYAML(t, filepath.Join(cwd, "substrates.yaml"), `
substrates:
  inline:
    url: https://example.com/inline.git
    auth:
      method: app
      appId: "1"
      privateKeyPath: ~/.tendril/app.pem
    commit: "  API  "
`)

	config, err := LoadSubstratesConfig("")
	if err != nil {
		t.Fatalf("LoadSubstratesConfig failed: %v", err)
	}
	if got := config.Substrates["inline"].Commit; got != "api" {
		t.Fatalf("inline.Commit = %q, want normalized %q", got, "api")
	}
}

// TestRunSproutMissingWorkspaceWinsOverPreflight asserts that substrate/workspace
// resolution errors (like ErrWorkspaceAbsent) take precedence over Terrarium/Docker
// preflight checks so absent workspaces fail with the specific missing-workspace sentinel.
func TestRunSproutMissingWorkspaceWinsOverPreflight(t *testing.T) {
	originalPreflight := runSproutPreflightChecksFn
	t.Cleanup(func() { runSproutPreflightChecksFn = originalPreflight })

	runSproutPreflightChecksFn = func(ctx context.Context) error {
		return fmt.Errorf("❌ Docker daemon is not responding")
	}

	dir := chdirToTempDir(t)
	writeSubstratesYAML(t, filepath.Join(dir, "substrates.yaml"), `
substrates:
  missing_managed:
    checkout:
      mode: managed
`)

	orch := &DockerOrchestrator{
		Substrate: "missing_managed",
	}
	_, err := orch.RunSprout(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrWorkspaceAbsent) {
		t.Fatalf("got unexpected error: %v, want ErrWorkspaceAbsent", err)
	}
}
