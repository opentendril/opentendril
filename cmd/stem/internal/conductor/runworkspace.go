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
	"time"
)

type runWorkspaceMetadataContextKey struct{}

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
	// SproutRunID is the explicit HistoryDB key associated with this allocation.
	// It is intentionally distinct from RunID, which is the allocation identity.
	SproutRunID string
}

// CreateRunWorkspaceWithMetadata records the available lifecycle relation
// before Git creates the linked worktree. Callers that do not have a durable
// Sprout history row may keep using CreateRunWorkspace; their relation remains
// unknown rather than being inferred from StepID.
func CreateRunWorkspaceWithMetadata(ctx context.Context, repository, stepID, startRevision string, metadata RunWorkspaceMetadata) (RunWorkspace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, runWorkspaceMetadataContextKey{}, metadata)
	return createRunWorkspaceFn(ctx, repository, stepID, startRevision)
}

// ReconcilePublishedFruit synchronizes the local Tendril-owned run workspace to
// match the exact remote commit published via the GitHub API.
//
// Before any destructive operation the following are verified:
//   - The RunWorkspace carries a complete identity (Repository, Path, Branch,
//     BaseCommit, RunID all non-empty).
//   - The owned-reference registry records an entry for this Branch whose
//     Purpose is PurposeSproutIsolation, BaseCommit matches, and RunID matches.
//   - Path is the registered linked worktree for Branch in git-worktree(1)'s
//     --porcelain listing.
//
// After fetching the remote Fruit branch by name (GitHub does not advertise
// arbitrary OIDs), the fetched tip is resolved locally and compared to the
// GitHub-returned OID. If they do not match exactly, the workspace is left
// untouched and an error is returned, so the caller can inspect the workspace
// and no data is silently discarded.
//
// git reset --hard resets tracked files and the index to match the target
// commit. It does not remove untracked files; those remain for inspection.
func (rw *RunWorkspace) ReconcilePublishedFruit(ctx context.Context, oid string) error {
	// 1. Validate complete RunWorkspace identity up-front; any empty field is a
	//    programming error or a zero-value struct accidentally reaching this path.
	repo := strings.TrimSpace(rw.Repository)
	path := strings.TrimSpace(rw.Path)
	branch := strings.TrimSpace(rw.Branch)
	baseCommit := strings.TrimSpace(rw.BaseCommit)
	runID := strings.TrimSpace(rw.RunID)
	targetOID := strings.TrimSpace(oid)
	if repo == "" || path == "" || branch == "" || baseCommit == "" || runID == "" {
		return fmt.Errorf("reconcile: RunWorkspace identity is incomplete (repository, path, branch, base commit, and run ID are all required)")
	}
	if targetOID == "" {
		return fmt.Errorf("reconcile: published OID is required")
	}

	// 2. Require the owned-reference registry to record a PurposeSproutIsolation
	//    entry for this Branch whose BaseCommit and RunID both match the caller's
	//    handle. This prevents a stale or misidentified RunWorkspace from
	//    overwriting a different run's state.
	owned, ownedOK := runWorkspaceOwnedRef(repo, branch, baseCommit)
	if !ownedOK {
		return fmt.Errorf("reconcile: branch %q is not a recorded Tendril-owned Sprout isolation branch for base commit %s", branch, baseCommit)
	}
	if owned.RunID != runID {
		return fmt.Errorf("reconcile: branch %q is owned by run %q, not %q", branch, owned.RunID, runID)
	}

	// 3. Require Path to be the registered linked worktree for Branch. This
	//    prevents reset --hard from targeting the wrong filesystem tree.
	registered, err := runWorkspaceWorktreeMatches(ctx, repo, path, branch)
	if err != nil {
		return fmt.Errorf("reconcile: verify linked worktree: %w", err)
	}
	if !registered {
		return fmt.Errorf("reconcile: path %q is not the registered linked worktree for branch %q", path, branch)
	}
	unlockRemoteRefs, lockErr := lockCommonGitRemoteRefs(ctx, repo)
	if lockErr != nil {
		return fmt.Errorf("reconcile: repository remote refs are unavailable")
	}
	defer unlockRemoteRefs()

	// 4. Fetch the run-specific Fruit branch from origin. Fetching by branch
	//    name is required because GitHub does not advertise arbitrary OIDs.
	if _, err := runGitCommand(ctx, repo, "fetch", "origin", branch); err != nil {
		return fmt.Errorf("reconcile: fetch origin/%s: %w", branch, err)
	}

	// 5. Resolve the fetched tip locally and compare to the GitHub-returned OID
	//    before any destructive operation. A mismatch means GitHub advertised a
	//    different commit than the mutation returned, leaving the workspace clean.
	fetchedOID, err := runGitCommand(ctx, repo, "rev-parse", "--verify", "--end-of-options", "origin/"+branch+"^{commit}")
	if err != nil {
		return fmt.Errorf("reconcile: resolve fetched tip of origin/%s: %w", branch, err)
	}
	fetchedOID = strings.TrimSpace(fetchedOID)
	if fetchedOID != targetOID {
		return fmt.Errorf("reconcile: fetched tip of origin/%s is %s but GitHub returned %s. workspace left untouched", branch, fetchedOID, targetOID)
	}

	// 6. All checks passed: reset tracked files and index to match the published
	//    commit. Untracked files are not removed by reset --hard and remain for
	//    inspection if cleanup fails.
	if _, err := runGitCommand(ctx, path, "reset", "--hard", targetOID); err != nil {
		return fmt.Errorf("reconcile: reset workspace to %s: %w", targetOID, err)
	}
	return nil
}

// runWorkspaceGitLocks covers only Git metadata allocation and removal. The
// key is the canonical managed-base path, so unrelated Substrates do not block
// each other. No lock is held while a Sprout uses its workspace.
var runWorkspaceGitLocks sync.Map

func runWorkspaceGitMutexFor(repository string) *sync.Mutex {
	key := strings.TrimSpace(repository)
	if absolute, err := filepath.Abs(key); err == nil {
		key = absolute
	}
	if resolved, err := resolveRunWorkspacePath(key); err == nil {
		key = resolved
	}
	value, _ := runWorkspaceGitLocks.LoadOrStore(filepath.Clean(key), &sync.Mutex{})
	return value.(*sync.Mutex)
}

func lockRunWorkspaceGit(repository string) func() {
	mutex := runWorkspaceGitMutexFor(repository)
	mutex.Lock()
	return mutex.Unlock
}

// runWorkspaceRoot returns the Tendril-owned root for run worktrees. It uses
// the same Stem state location as the owned-reference registry and is never
// host /tmp.
func runWorkspaceRoot() string {
	return filepath.Join(expandHome("~/.tendril"), "run-workspaces")
}

// resolvedRunWorkspaceRoot returns the absolute, symlink-resolved Stem-owned
// run-workspace root. Callers must fail closed when this returns an error.
func resolvedRunWorkspaceRoot() (string, error) {
	root := strings.TrimSpace(runWorkspaceRoot())
	if root == "" {
		return "", fmt.Errorf("run workspace root is unavailable")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve run workspace root: %w", err)
	}
	resolvedRoot, err := resolveRunWorkspacePath(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve run workspace root: %w", err)
	}
	if strings.TrimSpace(resolvedRoot) == "" {
		return "", fmt.Errorf("run workspace root is unavailable")
	}
	return resolvedRoot, nil
}

// CreateRunWorkspace allocates a linked Git worktree for one run. startRevision
// is required and is resolved to a commit before branch/worktree creation; no
// implicit HEAD or default-branch choice is made here.
func CreateRunWorkspace(ctx context.Context, repository, stepID, startRevision string) (RunWorkspace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata, _ := ctx.Value(runWorkspaceMetadataContextKey{}).(RunWorkspaceMetadata)
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

	allocations, err := LoadRunWorkspaceAllocations()
	if err != nil {
		return RunWorkspace{}, fmt.Errorf("read RunWorkspace allocations before reuse: %w", err)
	}
	for _, allocation := range allocations {
		if !allocation.Historical && filepath.Clean(allocation.Repository) == filepath.Clean(base) &&
			(allocation.Branch == branch || filepath.Clean(allocation.Path) == filepath.Clean(path)) {
			return RunWorkspace{}, fmt.Errorf("RunWorkspace allocation %q is still retained; reconcile or abandon it before reusing its branch", allocation.AllocationRunID)
		}
	}

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
	allocation := RunWorkspaceAllocation{
		AllocationRunID: runID,
		SproutRunID:     strings.TrimSpace(metadata.SproutRunID),
		StepID:          step,
		Repository:      base,
		Path:            path,
		Branch:          branch,
		BaseCommit:      resolvedCommit,
		Substrate:       strings.TrimSpace(metadata.Substrate),
		PhytomerID:      strings.TrimSpace(metadata.PhytomerID),
		Pollen:          strings.TrimSpace(metadata.Pollen),
		CreatedAt:       time.Now().UTC(),
		State:           RunWorkspaceAllocationPending,
	}
	if err := ReserveRunWorkspaceAllocation(allocation); err != nil {
		return RunWorkspace{}, fmt.Errorf("reserve durable identity for run workspace %q: %w", branch, err)
	}
	// Both durable allocation identity and exact pending ownership precede Git
	// worktree mutation. A failed reservation cannot authorize recovery.
	if err := reserveRunWorkspaceOwnedRef(owned); err != nil {
		_ = RetireRunWorkspaceAllocation(allocation)
		return RunWorkspace{}, fmt.Errorf("reserve ownership for run workspace %q: %w", branch, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		_ = forgetRunWorkspaceOwnedRef(base, branch, resolvedCommit, runID)
		_ = RetireRunWorkspaceAllocation(allocation)
		return RunWorkspace{}, fmt.Errorf("create run workspace parent: %w", err)
	}
	if _, err := runGitCommand(ctx, base, "worktree", "add", "-b", branch, path, resolvedCommit); err != nil {
		rollbackErr := rollbackRunWorkspaceAllocation(ctx, owned, path, allocation)
		if rollbackErr != nil {
			return RunWorkspace{}, fmt.Errorf("create run workspace %q: %v; rollback also failed: %w", branch, err, rollbackErr)
		}
		_ = RetireRunWorkspaceAllocation(allocation)
		return RunWorkspace{}, fmt.Errorf("create run workspace %q: %w", branch, err)
	}
	owned.Pending = false
	if err := finalizeRunWorkspaceOwnedRef(owned); err != nil {
		rollbackErr := rollbackRunWorkspaceAllocation(ctx, owned, path, allocation)
		if rollbackErr != nil {
			return RunWorkspace{}, fmt.Errorf("finalize ownership for run workspace %q: %v; rollback also failed: %w", branch, err, rollbackErr)
		}
		_ = RetireRunWorkspaceAllocation(allocation)
		return RunWorkspace{}, fmt.Errorf("finalize ownership for run workspace %q: %w", branch, err)
	}
	if err := finalizeRunWorkspaceAllocation(allocation); err != nil {
		rollbackErr := rollbackRunWorkspaceAllocation(ctx, owned, path, allocation)
		if rollbackErr != nil {
			return RunWorkspace{}, fmt.Errorf("finalize durable identity for run workspace %q: %v; rollback also failed: %w", branch, err, rollbackErr)
		}
		_ = RetireRunWorkspaceAllocation(allocation)
		return RunWorkspace{}, fmt.Errorf("finalize durable identity for run workspace %q: %w", branch, err)
	}

	return RunWorkspace{
		Repository:  base,
		Path:        path,
		Branch:      branch,
		StepID:      step,
		BaseCommit:  resolvedCommit,
		RunID:       runID,
		SproutRunID: allocation.SproutRunID,
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
			return retireRunWorkspaceAllocationForHandle(workspace)
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
	}
	if err := markRunWorkspaceRemovalPendingForHandle(workspace); err != nil {
		return fmt.Errorf("record RunWorkspace teardown intent: %w", err)
	}
	if pathExists {
		if _, err := runGitCommand(ctx, base, "worktree", "remove", path); err != nil {
			return fmt.Errorf("remove run workspace %q: %w", path, err)
		}
	} else if registered {
		if _, err := runGitCommand(ctx, base, "worktree", "remove", "--force", path); err != nil {
			return fmt.Errorf("remove stale run workspace metadata %q: %w", path, err)
		}
	}
	if err := markRunWorkspaceRemovedForHandle(workspace); err != nil {
		return fmt.Errorf("record completed RunWorkspace teardown: %w", err)
	}

	if !runWorkspaceBranchExists(ctx, base, branch) {
		_ = ForgetOwnedRef(base, branch)
		return retireRunWorkspaceAllocationForHandle(workspace)
	}
	// Run-workspace teardown is intentionally narrower than general owned-ref
	// reclamation: committed Fruit is the run's output and must remain even if
	// a forge could prove its pull request merged.
	outcome := ReclaimOwnedRefIfNoWork(ctx, base, owned)
	if outcome.Reclaimed {
		return retireRunWorkspaceAllocationForHandle(workspace)
	}
	if strings.HasPrefix(outcome.Reason, "reclamation failed:") || strings.Contains(outcome.Reason, "checked out") {
		return fmt.Errorf("cleanup run workspace branch %q: %s", branch, outcome.Reason)
	}
	return retireRunWorkspaceAllocationForHandle(workspace)
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

func forgetRunWorkspaceOwnedRef(repository, branch, base, runID string) error {
	return forgetRunWorkspaceOwnedRefForRollback(OwnedRef{
		Repository: filepath.Clean(repository), Branch: branch, Base: base,
		Purpose: PurposeSproutIsolation, RunID: runID,
	})
}

func rollbackRunWorkspaceAllocation(ctx context.Context, owned OwnedRef, path string, allocation RunWorkspaceAllocation) error {
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
		if err := markRunWorkspaceRemovalPending(allocation); err != nil {
			return fmt.Errorf("record partial RunWorkspace teardown intent: %w", err)
		}
		if _, err := runGitCommand(ctx, owned.Repository, "worktree", "remove", "--force", path); err != nil {
			return fmt.Errorf("remove partially allocated run workspace: %w", err)
		}
	}

	_, pathErr := os.Lstat(path)
	if pathErr != nil && !os.IsNotExist(pathErr) {
		return fmt.Errorf("inspect rolled-back run workspace path: %w", pathErr)
	}
	if pathErr == nil {
		return fmt.Errorf("partially allocated run workspace path remains after rollback")
	}

	if runWorkspaceBranchExists(ctx, owned.Repository, owned.Branch) {
		ownedEvidence := ReadOwnedRefEvidence(owned.Repository, owned.Branch, owned.Base, owned.RunID)
		if ownedEvidence.State != OwnedRefEvidenceMatched {
			if err := finalizeRunWorkspaceOwnedRef(owned); err != nil {
				return fmt.Errorf("finalize exact ownership for rolled-back run workspace: %w", err)
			}
		}
		currentAllocation, err := runWorkspaceAllocationByID(allocation.AllocationRunID)
		if err != nil {
			return fmt.Errorf("load rolled-back RunWorkspace allocation: %w", err)
		}
		if currentAllocation.State == RunWorkspaceAllocationPending {
			if err := finalizeRunWorkspaceAllocation(currentAllocation); err != nil {
				return fmt.Errorf("finalize durable identity for rolled-back RunWorkspace: %w", err)
			}
			currentAllocation.State = RunWorkspaceAllocationFinalized
		}
		if err := markRunWorkspaceRemoved(currentAllocation); err != nil {
			return fmt.Errorf("record removed state for rolled-back RunWorkspace: %w", err)
		}
	}

	if runWorkspaceBranchExists(ctx, owned.Repository, owned.Branch) {
		outcome := ReclaimOwnedRefIfNoWork(ctx, owned.Repository, owned)
		if outcome.Reclaimed {
			return nil
		}
		return fmt.Errorf("preserved run workspace branch %q: %s", owned.Branch, outcome.Reason)
	}
	return forgetRunWorkspaceOwnedRef(owned.Repository, owned.Branch, owned.Base, owned.RunID)
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
