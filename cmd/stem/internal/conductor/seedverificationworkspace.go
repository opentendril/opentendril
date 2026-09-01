package conductor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const seedVerificationWorktreePrefix = "seed-verification-"

func seedVerificationWorkspaceRoot() (string, error) {
	root := strings.TrimSpace(runWorkspaceRoot())
	if root == "" {
		return "", fmt.Errorf("seed verification workspace root is unavailable")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve seed verification workspace root: %w", err)
	}
	resolvedRoot, err := resolveRunWorkspacePath(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve seed verification workspace root: %w", err)
	}
	return resolvedRoot, nil
}

// createSeedVerificationWorktree materializes one detached view of the exact
// Seed branch under the Stem-owned run-workspace root. Unlike the general
// shadow-worktree path, this location is intentionally outside the service's
// private /tmp namespace so a Docker daemon can resolve the bind mount.
func createSeedVerificationWorktree(sourcePath, seedBranch string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("seed verification source repository is required")
	}
	absSourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve seed verification source repository: %w", err)
	}
	sourcePath = absSourcePath
	seedBranch = strings.TrimSpace(seedBranch)
	if seedBranch == "" {
		return "", fmt.Errorf("seed verification branch is required")
	}

	absRoot, err := seedVerificationWorkspaceRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return "", fmt.Errorf("create seed verification workspace root: %w", err)
	}

	runID, err := newRunWorkspaceID()
	if err != nil {
		return "", fmt.Errorf("create seed verification workspace identity: %w", err)
	}
	worktree := filepath.Join(absRoot, seedVerificationWorktreePrefix+runID)
	if !pathIsUnder(worktree, absRoot) || sameFilePath(worktree, sourcePath) || pathIsUnder(worktree, sourcePath) {
		return "", fmt.Errorf("seed verification workspace path %q is outside its owned root", worktree)
	}
	if _, err := os.Lstat(worktree); err == nil {
		return "", fmt.Errorf("seed verification workspace path %q already exists", worktree)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect seed verification workspace path %q: %w", worktree, err)
	}

	unlockGit := lockRunWorkspaceGit(sourcePath)
	defer unlockGit()

	cmd := exec.Command("git", "worktree", "add", "--detach", worktree, seedBranch)
	cmd.Dir = sourcePath
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(worktree)
		return "", fmt.Errorf("git worktree add for Seed verification failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}
	return worktree, nil
}

// removeSeedVerificationWorktree removes the disposable verification view and
// reports cleanup failures instead of silently leaving a Docker-visible tree.
func removeSeedVerificationWorktree(sourcePath, worktree string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if sourcePath == "" || worktree == "." || worktree == string(filepath.Separator) {
		return fmt.Errorf("seed verification worktree cleanup identity is incomplete")
	}
	absSourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		return fmt.Errorf("resolve seed verification source repository: %w", err)
	}
	sourcePath = absSourcePath
	root, err := seedVerificationWorkspaceRoot()
	if err != nil {
		return err
	}
	if !pathIsUnder(worktree, root) || sameFilePath(worktree, root) {
		return fmt.Errorf("seed verification worktree path %q is outside its owned root", worktree)
	}

	unlockGit := lockRunWorkspaceGit(sourcePath)
	defer unlockGit()

	cmd := exec.Command("git", "worktree", "remove", "--force", worktree)
	cmd.Dir = sourcePath
	gitOutput, gitErr := cmd.CombinedOutput()
	removeErr := os.RemoveAll(worktree)
	if gitErr != nil {
		if removeErr != nil {
			return fmt.Errorf("git worktree remove failed: %w, output: %s; remove workspace directory: %v", gitErr, strings.TrimSpace(string(gitOutput)), removeErr)
		}
		return fmt.Errorf("git worktree remove failed: %w, output: %s", gitErr, strings.TrimSpace(string(gitOutput)))
	}
	if removeErr != nil {
		return fmt.Errorf("remove seed verification workspace directory: %w", removeErr)
	}
	if _, err := os.Lstat(worktree); err == nil {
		return fmt.Errorf("seed verification workspace directory %q still exists", worktree)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect removed seed verification workspace %q: %w", worktree, err)
	}
	return nil
}
