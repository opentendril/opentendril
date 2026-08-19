package conductor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RunWorkspace is the mutable filesystem state for one Sprout run. Its
// identity is the backing repository plus StepID; it deliberately has no
// Pollen field because concurrent runs from one Pollen must remain distinct.
type RunWorkspace struct {
	// Repository is the persistent managed checkout whose Git objects and refs
	// back this linked worktree.
	Repository string
	// Path is the Tendril-owned directory mounted into the run's Terrarium.
	Path string
	// Branch is the run-specific Fruit branch.
	Branch string
	// StepID is the run identity used to derive Branch and Path.
	StepID string
	// BaseCommit is the resolved commit supplied when the workspace was created.
	BaseCommit string
	// RunID distinguishes this allocation from a later run that reuses the same
	// step-scoped branch after this workspace is reclaimed.
	RunID string
}

// runWorkspaceGitLocks covers only Git metadata allocation and removal. The
// key is the canonical managed-base path, so unrelated Substrates do not block
// each other. No lock is held while a Sprout uses its workspace.
var runWorkspaceGitLocks sync.Map

func lockRunWorkspaceGit(repository string) func() {
	value, _ := runWorkspaceGitLocks.LoadOrStore(filepath.Clean(repository), &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// runWorkspaceRoot returns the Tendril-owned root for run worktrees. It uses
// the same Stem state location as the owned-reference registry and is never
// host /tmp.
func runWorkspaceRoot() string {
	return filepath.Join(expandHome("~/.tendril"), "run-workspaces")
}

// CreateRunWorkspace allocates a linked Git worktree for one run. startRevision
// is required and is resolved to a commit before branch/worktree creation; no
// implicit HEAD or default-branch choice is made here.
func CreateRunWorkspace(ctx context.Context, repository, stepID, startRevision string) (RunWorkspace, error) {
	base, err := absoluteRunWorkspaceRepository(ctx, repository)
	if err != nil {
		return RunWorkspace{}, err
	}
	step := strings.TrimSpace(stepID)
	if step == "" {
		return RunWorkspace{}, fmt.Errorf("run workspace step ID is required")
	}
	branch := "sprout/task-" + step
	if _, err := runGitCommand(ctx, base, "check-ref-format", "--branch", branch); err != nil {
		return RunWorkspace{}, fmt.Errorf("invalid run workspace step ID %q: %w", stepID, err)
	}
	revision := strings.TrimSpace(startRevision)
	if revision == "" {
		return RunWorkspace{}, fmt.Errorf("run workspace start revision is required")
	}
	resolvedCommit, err := runGitCommand(ctx, base, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return RunWorkspace{}, fmt.Errorf("resolve run workspace start revision %q: %w", revision, err)
	}
	resolvedCommit = strings.TrimSpace(resolvedCommit)
	if resolvedCommit == "" {
		return RunWorkspace{}, fmt.Errorf("resolve run workspace start revision %q returned no commit", revision)
	}

	root := strings.TrimSpace(runWorkspaceRoot())
	if root == "" {
		return RunWorkspace{}, fmt.Errorf("run workspace root is unavailable")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return RunWorkspace{}, fmt.Errorf("resolve run workspace root: %w", err)
	}
	root, err = resolveRunWorkspacePath(root)
	if err != nil {
		return RunWorkspace{}, fmt.Errorf("resolve run workspace root: %w", err)
	}
	path := runWorkspacePath(root, base, step)
	if sameFilePath(path, base) || pathIsUnder(path, base) {
		return RunWorkspace{}, fmt.Errorf("run workspace path %q is inside the managed repository %q", path, base)
	}

	unlockGit := lockRunWorkspaceGit(base)
	defer unlockGit()

	if _, err := os.Lstat(path); err == nil {
		return RunWorkspace{}, fmt.Errorf("run workspace path %q already exists", path)
	} else if !os.IsNotExist(err) {
		return RunWorkspace{}, fmt.Errorf("inspect run workspace path %q: %w", path, err)
	}

	branchExists := runWorkspaceBranchExists(ctx, base, branch)
	ownedBranch, ownedBranchOK := ownedRefForBranch(base, branch)
	if branchExists {
		if !ownedBranchOK || ownedBranch.Purpose != PurposeSproutIsolation {
			return RunWorkspace{}, fmt.Errorf("run workspace branch %q already exists and is not an owned Sprout isolation branch", branch)
		}
		if outcome, reclaimed := reclaimRunWorkspaceCollision(ctx, base, branch); !reclaimed {
			return RunWorkspace{}, fmt.Errorf("run workspace branch %q already exists and was not reclaimed: %s", branch, outcome)
		}
	} else if ownedBranchOK {
		if ownedBranch.Purpose != PurposeSproutIsolation {
			return RunWorkspace{}, fmt.Errorf("run workspace branch %q has unrelated owned state", branch)
		}
		// The branch disappeared outside the normal lifecycle. Forgetting only
		// this stale registry entry cannot affect a Git ref or another run.
		if err := ForgetOwnedRef(base, branch); err != nil {
			return RunWorkspace{}, fmt.Errorf("forget stale ownership for run workspace branch %q: %w", branch, err)
		}
	}

	runID, err := newRunWorkspaceID()
	if err != nil {
		return RunWorkspace{}, fmt.Errorf("create run workspace identity: %w", err)
	}
	owned := OwnedRef{
		Repository: base,
		Branch:     branch,
		Purpose:    PurposeSproutIsolation,
		Base:       resolvedCommit,
		RunID:      runID,
		Pending:    true,
	}
	// Reserve ownership before Git mutation. Pending protects this allocation
	// window, and rollback/retry reconciles the owned state where it is safe to
	// do so. Cross-process and crash hardening remain outside this slice.
	if err := RegisterOwnedRef(owned); err != nil {
		return RunWorkspace{}, fmt.Errorf("register ownership for run workspace %q: %w", branch, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		_ = forgetRunWorkspaceOwnedRef(base, branch, runID)
		return RunWorkspace{}, fmt.Errorf("create run workspace parent: %w", err)
	}
	if _, err := runGitCommand(ctx, base, "worktree", "add", "-b", branch, path, resolvedCommit); err != nil {
		rollbackErr := rollbackRunWorkspaceAllocation(ctx, owned, path)
		if rollbackErr != nil {
			return RunWorkspace{}, fmt.Errorf("create run workspace %q: %v; rollback also failed: %w", branch, err, rollbackErr)
		}
		return RunWorkspace{}, fmt.Errorf("create run workspace %q: %w", branch, err)
	}
	owned.Pending = false
	if err := RegisterOwnedRef(owned); err != nil {
		rollbackErr := rollbackRunWorkspaceAllocation(ctx, owned, path)
		if rollbackErr != nil {
			return RunWorkspace{}, fmt.Errorf("finalize ownership for run workspace %q: %v; rollback also failed: %w", branch, err, rollbackErr)
		}
		return RunWorkspace{}, fmt.Errorf("finalize ownership for run workspace %q: %w", branch, err)
	}

	return RunWorkspace{
		Repository: base,
		Path:       path,
		Branch:     branch,
		StepID:     step,
		BaseCommit: resolvedCommit,
		RunID:      runID,
	}, nil
}

// Cleanup removes this run's linked worktree and then applies no-work-only
// owned-reference reclamation. A branch with no work is reclaimed; a branch
// carrying committed Fruit remains available for review.
func (workspace RunWorkspace) Cleanup(ctx context.Context, _ ResolvedCredential) error {
	base := filepath.Clean(strings.TrimSpace(workspace.Repository))
	branch := strings.TrimSpace(workspace.Branch)
	path := filepath.Clean(strings.TrimSpace(workspace.Path))
	if base == "." || branch == "" || path == "." || strings.TrimSpace(workspace.BaseCommit) == "" || strings.TrimSpace(workspace.RunID) == "" {
		return fmt.Errorf("run workspace cleanup requires repository, branch, path, base commit, and run ID")
	}

	base, err := absoluteRunWorkspaceRepository(ctx, workspace.Repository)
	if err != nil {
		return err
	}
	unlockGit := lockRunWorkspaceGit(base)
	defer unlockGit()

	owned, ownedOK := runWorkspaceOwnedRef(base, branch, workspace.BaseCommit)
	if ownedOK && owned.RunID != workspace.RunID {
		ownedOK = false
	}
	pathInfo, pathErr := os.Lstat(path)
	pathExists := pathErr == nil
	if pathErr != nil && !os.IsNotExist(pathErr) {
		return fmt.Errorf("inspect run workspace path %q: %w", path, pathErr)
	}
	if !ownedOK {
		branchExists := runWorkspaceBranchExists(ctx, base, branch)
		if !pathExists && !branchExists {
			return nil
		}
		return fmt.Errorf("run workspace %q is not recorded as owned by this run", branch)
	}
	if pathExists && !pathInfo.IsDir() {
		return fmt.Errorf("run workspace path %q is not a directory", path)
	}

	registered, err := runWorkspaceWorktreeMatches(ctx, base, path, branch)
	if err != nil {
		return err
	}
	if pathExists {
		if !registered {
			return fmt.Errorf("run workspace path %q is not the linked worktree for branch %q", path, branch)
		}
		status, err := runGitCommandRawOutput(ctx, path, "status", "--porcelain", "-uall", "-z")
		if err != nil {
			return fmt.Errorf("inspect run workspace changes: %w", err)
		}
		if status != "" {
			return fmt.Errorf("refusing to remove run workspace %q with uncommitted changes", path)
		}
		if _, err := runGitCommand(ctx, base, "worktree", "remove", path); err != nil {
			return fmt.Errorf("remove run workspace %q: %w", path, err)
		}
	} else if registered {
		if _, err := runGitCommand(ctx, base, "worktree", "remove", "--force", path); err != nil {
			return fmt.Errorf("remove stale run workspace metadata %q: %w", path, err)
		}
	}

	if !runWorkspaceBranchExists(ctx, base, branch) {
		_ = ForgetOwnedRef(base, branch)
		return nil
	}
	// Run-workspace teardown is intentionally narrower than general owned-ref
	// reclamation: committed Fruit is the run's output and must remain even if
	// a forge could prove its pull request merged.
	outcome := ReclaimOwnedRefIfNoWork(ctx, base, owned)
	if outcome.Reclaimed {
		return nil
	}
	if strings.HasPrefix(outcome.Reason, "reclamation failed:") || strings.Contains(outcome.Reason, "checked out") {
		return fmt.Errorf("cleanup run workspace branch %q: %s", branch, outcome.Reason)
	}
	return nil
}

func absoluteRunWorkspaceRepository(ctx context.Context, repository string) (string, error) {
	base := strings.TrimSpace(repository)
	if base == "" {
		return "", fmt.Errorf("run workspace repository is required")
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve run workspace repository: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat run workspace repository %q: %w", abs, err)
	}
	if !info.IsDir() || !isGitRepo(abs) {
		return "", fmt.Errorf("run workspace repository %q is not a Git checkout", abs)
	}
	topLevel, err := runGitCommand(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve run workspace Git top-level: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(strings.TrimSpace(topLevel))
	if err != nil {
		return "", fmt.Errorf("resolve run workspace repository symlinks: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func resolveRunWorkspacePath(path string) (string, error) {
	missing := []string{}
	current := filepath.Clean(path)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func runWorkspacePath(root, repository, stepID string) string {
	identity := sha256.Sum256([]byte(repository + "\x00" + stepID))
	return filepath.Join(root, hex.EncodeToString(identity[:16]))
}

func newRunWorkspaceID() (string, error) {
	identity := make([]byte, 16)
	if _, err := rand.Read(identity); err != nil {
		return "", err
	}
	return hex.EncodeToString(identity), nil
}

func runWorkspaceBranchExists(ctx context.Context, repository, branch string) bool {
	_, err := runGitCommand(ctx, repository, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func ownedRefForBranch(repository, branch string) (OwnedRef, bool) {
	for _, ref := range OwnedRefsFor(repository) {
		if ref.Branch == branch {
			return ref, true
		}
	}
	return OwnedRef{}, false
}

func runWorkspaceOwnedRef(repository, branch, base string) (OwnedRef, bool) {
	ref, ok := ownedRefForBranch(repository, branch)
	if ok && ref.Purpose == PurposeSproutIsolation && (strings.TrimSpace(base) == "" || ref.Base == base) {
		return ref, true
	}
	return OwnedRef{}, false
}

func forgetRunWorkspaceOwnedRef(repository, branch, runID string) error {
	ref, ok := runWorkspaceOwnedRef(repository, branch, "")
	if !ok || ref.RunID != runID {
		return fmt.Errorf("run workspace ownership changed before cleanup")
	}
	return ForgetOwnedRef(repository, branch)
}

func rollbackRunWorkspaceAllocation(ctx context.Context, owned OwnedRef, path string) error {
	current, ok := runWorkspaceOwnedRef(owned.Repository, owned.Branch, owned.Base)
	if !ok || current.RunID != owned.RunID {
		return fmt.Errorf("run workspace ownership changed before rollback")
	}

	registered, err := runWorkspaceWorktreeMatches(ctx, owned.Repository, path, owned.Branch)
	if err != nil {
		return err
	}
	if registered {
		status, err := runGitCommandRawOutput(ctx, path, "status", "--porcelain", "-uall", "-z")
		if err != nil {
			return fmt.Errorf("inspect partially allocated run workspace: %w", err)
		}
		if status != "" {
			return fmt.Errorf("partially allocated run workspace has uncommitted changes")
		}
		if _, err := runGitCommand(ctx, owned.Repository, "worktree", "remove", "--force", path); err != nil {
			return fmt.Errorf("remove partially allocated run workspace: %w", err)
		}
	}

	if runWorkspaceBranchExists(ctx, owned.Repository, owned.Branch) {
		outcome := ReclaimOwnedRefIfNoWork(ctx, owned.Repository, owned)
		if !outcome.Reclaimed {
			if outcome.Reason == "carries committed Fruit" {
				owned.Pending = false
				if err := RegisterOwnedRef(owned); err != nil {
					return fmt.Errorf("preserved run workspace branch %q but could not finalize ownership: %w", owned.Branch, err)
				}
			}
			return fmt.Errorf("preserved run workspace branch %q: %s", owned.Branch, outcome.Reason)
		}
	}
	return forgetRunWorkspaceOwnedRef(owned.Repository, owned.Branch, owned.RunID)
}

func reclaimRunWorkspaceCollision(ctx context.Context, repository, branch string) (string, bool) {
	ref, ok := runWorkspaceOwnedRef(repository, branch, "")
	if !ok {
		return "the branch is not an owned Sprout isolation branch", false
	}
	outcome := ReclaimOwnedRefIfNoWork(ctx, repository, ref)
	return outcome.Reason, outcome.Reclaimed
}

func runWorkspaceWorktreeMatches(ctx context.Context, repository, path, branch string) (bool, error) {
	listing, err := runGitCommand(ctx, repository, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("list Git worktrees: %w", err)
	}
	wantedPath := filepath.Clean(path)
	wantedBranch := "refs/heads/" + branch
	for _, block := range strings.Split(listing, "\n\n") {
		var listedPath, listedBranch string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				listedPath = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			case strings.HasPrefix(line, "branch "):
				listedBranch = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			}
		}
		if filepath.Clean(listedPath) == wantedPath && listedBranch == wantedBranch {
			return true, nil
		}
	}
	return false, nil
}
