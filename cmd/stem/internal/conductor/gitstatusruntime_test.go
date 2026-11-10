package conductor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Git status is host git. This proves a repository can be inspected when no
// container daemon is reachable and no LLM provider is configured.
func TestGitStatusRunsWithoutDockerOrLLM(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("TENDRIL_ALLOW_HOST_EXECUTION", "")
	t.Setenv("TENDRIL_DEPLOYMENT_POSTURE", "")
	t.Setenv("DOCKER_HOST", filepath.Join(t.TempDir(), "missing.sock"))

	workspace := t.TempDir()
	initCmd := exec.Command("git", "init", workspace)
	initCmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	result, err := RunGitStatus(context.Background(), GitStatusExecution{
		Workspace:                workspace,
		ConfiguredBranch:         "main",
		AllowDefaultBranchCommit: false,
	})
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if result.HasCommits {
		t.Fatal("fresh repository reported commits")
	}
}
