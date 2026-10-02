package conductor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Per-Pollinator workspace isolation for the delegated git ladder.
//
// Each delegation subject gets a real git worktree, private to that subject. The
// isolation unit is the subject because it is already the unit of authorization
// and is bound at connection time, so no operation needs an extra parameter.
//
// Without isolation, two Pollinators on one substrate corrupt each other: a
// delegated commit stages the whole tree, so one subject's uncommitted files are
// committed by the other, onto the other's branch, under the other's identity.
//
// A worktree shares the repository's object store, so commits are immediately
// visible to the substrate as branches, which is what keeps push, pull requests
// and review working. Git also refuses to check out one branch in two worktrees,
// turning "two Pollinators on one branch" into a refusal rather than corruption.

func delegatedWorkspaceRoot() string {
	return filepath.Join(expandHome("~/.tendril"), "workspaces")
}

// sanitizeWorkspaceComponent makes a substrate or Pollen value safe as a single
// path component. Both are operator-controlled today, but the Pollinator is the
// key an untrusted caller is identified by, so treating either as a raw path
// component would be the kind of assumption this codebase now explicitly
// refuses to make.
func sanitizeWorkspaceComponent(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	cleaned := strings.Trim(b.String(), ".-")
	if cleaned == "" {
		return "unnamed"
	}
	return cleaned
}

// workspaceLocks serializes operations that target the same workspace within
// this Stem process. The process guard extends that exclusion across Stem
// processes on supported platforms.
var workspaceLocks sync.Map

var ErrWorkspaceProcessLockUnavailable = errors.New("cross-process workspace locking is unavailable on this platform")

// lockWorkspaceAcrossProcessesFn is the platform guard seam. Ordinary Git
// calls use it best-effort through LockWorkspacePortable; lifecycle mutation
// requires it directly before destructive change.
var lockWorkspaceAcrossProcessesFn = lockWorkspaceAcrossProcesses

func lockWorkspaceInProcess(path string) func() {
	value, _ := workspaceLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// LockWorkspace serializes access to one workspace path and returns the
// release function. It uses cross-process exclusion when available and falls
// back to the portable in-process guard when the platform has no process lock.
func LockWorkspace(path string) func() {
	unlock, err := LockWorkspacePortable(context.Background(), path)
	if err == nil {
		return unlock
	}
	return lockWorkspaceInProcess(path)
}

// LockWorkspacePortable is LockWorkspace with a request context. Lack of
// platform process locking falls back to same-process serialization, but
// context cancellation still interrupts a wait instead of starting the Git
// operation after its request has been canceled.
func LockWorkspacePortable(ctx context.Context, path string) (func(), error) {
	unlock, err := LockWorkspaceContext(ctx, path)
	if err == nil {
		return unlock, nil
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !errors.Is(err, ErrWorkspaceProcessLockUnavailable) {
		return nil, err
	}
	return lockWorkspaceInProcess(path), nil
}

// LockWorkspaceContext requires both in-process and cross-process exclusion.
// Lifecycle destruction calls it directly and fails closed on platforms
// without a process guard; ordinary Git calls should use LockWorkspacePortable
// to fall back safely only when that platform capability is unavailable.
func LockWorkspaceContext(ctx context.Context, path string) (func(), error) {
	unlockLocal := lockWorkspaceInProcess(path)
	unlockOS, err := lockWorkspaceAcrossProcessesFn(ctx, path)
	if err != nil {
		unlockLocal()
		return nil, err
	}
	return func() {
		unlockOS()
		unlockLocal()
	}, nil
}

// DelegatedWorkspace describes where an operation will actually run.
type DelegatedWorkspace struct {
	// Path is the directory the operation runs in.
	Path string
	// Repository is the Substrate checkout whose Git metadata this worktree uses.
	Repository string
	// Pollen is the Pollen it belongs to ("" when the operation
	// is not delegated).
	Pollen string
	// Isolated reports whether Path is a per-Pollinator worktree rather than the
	// substrate's own checkout.
	Isolated bool
	// Branch is the owned branch the workspace was placed on ("" for a
	// non-delegated operation, which uses the operator's own checkout).
	Branch string
}

// DelegatedWorkspaceMode controls whether resolution may initialize a
// Pollinator workspace. ExistingWorkspaceOnly is used by capabilities such as
// git.apply whose resolution must not create or rotate a workspace or branch.
type DelegatedWorkspaceMode int

const (
	CreateDelegatedWorkspaceIfMissing DelegatedWorkspaceMode = iota
	ExistingDelegatedWorkspaceOnly
)

var ErrDelegatedWorkspaceAbsent = errors.New("delegated workspace is absent")

// ResolveDelegatedWorkspace returns the workspace an operation should run in.
//
// With no pollen, for a human at a terminal, it returns the substrate's own
// checkout unchanged. With a Pollinator, it returns that subject's private
// worktree, created on first use ON AN OWNED BRANCH cut from the repository's
// resolved default branch.
//
// The branch matters: a delegated workspace is never on the default branch and
// never on no branch at all, so the ladder's branch guards have nothing left to
// catch. They remain as a backstop rather than the mechanism.
//
// The branch is registered as an owned reference at creation, which makes it
// reclaimable rather than litter.

func ResolveDelegatedWorkspace(ctx context.Context, substrateName, substratePath, pollen string, credential ResolvedCredential) (DelegatedWorkspace, error) {
	return ResolveDelegatedWorkspaceWithDefaultBranch(ctx, substrateName, substratePath, pollen, credential, "")
}

// ResolveDelegatedWorkspaceWithDefaultBranch is ResolveDelegatedWorkspace with
// the Substrate's explicitly configured default branch. The branch is passed
// through the same authoritative resolver used by the Botanist lifecycle view.
func ResolveDelegatedWorkspaceWithDefaultBranch(ctx context.Context, substrateName, substratePath, pollen string, credential ResolvedCredential, configuredBranch string) (DelegatedWorkspace, error) {
	return ResolveDelegatedWorkspaceWithModeAndDefaultBranch(ctx, substrateName, substratePath, pollen, credential, CreateDelegatedWorkspaceIfMissing, configuredBranch)
}

// ResolveDelegatedWorkspaceWithMode returns the subject's private worktree.
// ExistingWorkspaceOnly never creates a directory/worktree or rotates its
// branch; it is the resolution mode for operations that require pre-existing
// state and must have no resolver side effects.
func ResolveDelegatedWorkspaceWithMode(ctx context.Context, substrateName, substratePath, pollen string, credential ResolvedCredential, mode DelegatedWorkspaceMode) (DelegatedWorkspace, error) {
	return ResolveDelegatedWorkspaceWithModeAndDefaultBranch(ctx, substrateName, substratePath, pollen, credential, mode, "")
}

// ResolveDelegatedWorkspaceWithModeAndDefaultBranch accepts both the resolver
// mode and configured default-branch evidence. ExistingWorkspaceOnly remains
// side-effect-free.
func ResolveDelegatedWorkspaceWithModeAndDefaultBranch(ctx context.Context, substrateName, substratePath, pollen string, credential ResolvedCredential, mode DelegatedWorkspaceMode, configuredBranch string) (DelegatedWorkspace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if mode != CreateDelegatedWorkspaceIfMissing && mode != ExistingDelegatedWorkspaceOnly {
		return DelegatedWorkspace{}, fmt.Errorf("unknown delegated workspace resolution mode")
	}
	base := strings.TrimSpace(substratePath)
	if base == "" {
		return DelegatedWorkspace{}, fmt.Errorf("substrate path is required to resolve a workspace")
	}
	trimmedPollen := strings.TrimSpace(pollen)
	if trimmedPollen == "" {
		if mode == ExistingDelegatedWorkspaceOnly {
			return DelegatedWorkspace{}, fmt.Errorf("existing delegated workspace requires a Pollen")
		}
		return DelegatedWorkspace{Path: base, Repository: base}, nil
	}

	name := sanitizeWorkspaceComponent(substrateName)
	if strings.TrimSpace(substrateName) == "" {
		name = sanitizeWorkspaceComponent(filepath.Base(base))
	}
	path := filepath.Join(delegatedWorkspaceRoot(), name, sanitizeWorkspaceComponent(trimmedPollen))

	workspace := DelegatedWorkspace{Path: path, Repository: base, Pollen: trimmedPollen, Isolated: true}
	resolvedStartPoint := ""
	if mode == ExistingDelegatedWorkspaceOnly {
		workspaceRoot := delegatedWorkspaceRoot()
		rootResolved, rootErr := filepath.EvalSymlinks(workspaceRoot)
		substrateWorkspace := filepath.Join(workspaceRoot, name)
		substrateInfo, substrateErr := os.Lstat(substrateWorkspace)
		info, statErr := os.Lstat(path)
		resolvedPath, resolvedErr := filepath.EvalSymlinks(path)
		relativePath, relativeErr := filepath.Rel(rootResolved, resolvedPath)
		insideWorkspaceRoot := relativeErr == nil && relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) && !filepath.IsAbs(relativePath)
		if rootErr != nil || substrateErr != nil || substrateInfo.Mode()&os.ModeSymlink != 0 || statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || resolvedErr != nil || !insideWorkspaceRoot || !checkoutHasGitMetadata(path) || !isGitRepo(path) {
			return DelegatedWorkspace{}, fmt.Errorf("%w for Pollen %q on Substrate %q", ErrDelegatedWorkspaceAbsent, trimmedPollen, substrateName)
		}
		unlock := LockWorkspace(path)
		report, inspectErr := inspectDelegatedWorkspaceUnlocked(ctx, DelegatedWorkspaceTarget{
			Pollen: trimmedPollen, Substrate: substrateName, Repository: base,
			ConfiguredBranch: configuredBranch, Credential: credential,
		}, path)
		if inspectErr != nil {
			unlock()
			return DelegatedWorkspace{}, fmt.Errorf("retained delegated workspace could not be safely verified: %w", inspectErr)
		}
		if !report.WorkspaceVerified || !report.BranchOwned {
			unlock()
			return DelegatedWorkspace{}, fmt.Errorf("existing delegated workspace ownership could not be verified for this Pollen")
		}
		workspace.Branch = report.CurrentBranch
		if report.FruitState == FruitStateMerged {
			unlock()
			return DelegatedWorkspace{}, fmt.Errorf("existing delegated workspace contains terminal merged Fruit; recovery is required before git.apply: %s", report.Reason)
		}
		unlock()
		return workspace, nil
	}

	if isGitRepo(path) {
		// Hold only the in-process mutex while inspecting. If this workspace
		// proves reclaimable, the process guard is acquired below and held
		// through removal and replacement creation. Ordinary Git callers use
		// LockWorkspacePortable, which also takes that process guard when supported.
		unlockWorkspace := lockWorkspaceInProcess(path)
		if !isGitRepo(path) {
			unlockWorkspace()
		} else {
			if current, err := runGitCommitCommandFn(ctx, path, "branch", "--show-current"); err == nil {
				workspace.Branch = strings.TrimSpace(current)
			}
			report, inspectErr := inspectDelegatedWorkspaceUnlocked(ctx, DelegatedWorkspaceTarget{
				Pollen: trimmedPollen, Substrate: substrateName, Repository: base,
				ConfiguredBranch: configuredBranch, Credential: credential,
			}, path)
			if inspectErr != nil {
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("retained delegated workspace could not be safely verified: %w", inspectErr)
			}
			if !report.WorkspaceVerified || !report.BranchOwned {
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("retained delegated workspace ownership could not be verified for this Pollen")
			}
			workspace.Branch = report.CurrentBranch
			if report.FruitState == FruitStateMerged && !report.AutoReclaimable {
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("terminal merged workspace residue is not clean and requires Botanist recovery: %s", report.Reason)
			}
			if !report.AutoReclaimable {
				// Dirty, unique, open, closed-unmerged, and unverified work is
				// continuation state for this exact Pollen/Substrate workspace.
				unlockWorkspace()
				return workspace, nil
			}

			// Empty/merged reclamation removes a worktree and may remove its
			// exact owned branch. Keep the portable in-process guard, then add
			// the platform process guard only around that destructive boundary.
			unlockProcess, processErr := lockWorkspaceAcrossProcessesFn(ctx, path)
			if processErr != nil {
				unlockWorkspace()
				if report.FruitState == FruitStateMerged {
					return DelegatedWorkspace{}, fmt.Errorf("terminal merged workspace cannot be reclaimed safely on this platform: %w", processErr)
				}
				// A clean empty workspace contains no unique work. If the
				// platform lacks process locking, keep it rather than make an
				// ordinary Git call fail or remove it without exclusion.
				return workspace, nil
			}

			report, inspectErr = inspectDelegatedWorkspaceUnlocked(ctx, DelegatedWorkspaceTarget{
				Pollen: trimmedPollen, Substrate: substrateName, Repository: base,
				ConfiguredBranch: configuredBranch, Credential: credential,
			}, path)
			if inspectErr != nil {
				unlockProcess()
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("retained delegated workspace could not be safely reverified: %w", inspectErr)
			}
			if !report.WorkspaceVerified || !report.BranchOwned {
				unlockProcess()
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("retained delegated workspace ownership changed before reclamation")
			}
			if report.FruitState == FruitStateMerged && !report.AutoReclaimable {
				unlockProcess()
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("terminal merged workspace residue is not clean and requires Botanist recovery: %s", report.Reason)
			}
			if !report.AutoReclaimable {
				workspace.Branch = report.CurrentBranch
				unlockProcess()
				unlockWorkspace()
				return workspace, nil
			}

			resolvedStartPoint, inspectErr = workspaceStartPointFor(ctx, base, configuredBranch, credential)
			if inspectErr != nil {
				unlockProcess()
				unlockWorkspace()
				return DelegatedWorkspace{}, inspectErr
			}
			removed, removeErr := removeDelegatedWorkspace(ctx, DelegatedWorkspaceTarget{
				Pollen: trimmedPollen, Substrate: substrateName, Repository: base,
				ConfiguredBranch: configuredBranch, Credential: credential,
			}, report, false)
			if removeErr != nil {
				unlockProcess()
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("safely reclaim retained delegated workspace: %w", removeErr)
			}
			if !removed.WorktreeRemoved {
				unlockProcess()
				unlockWorkspace()
				return DelegatedWorkspace{}, fmt.Errorf("retained delegated workspace was not removed")
			}
			unlockExisting := func() {
				unlockProcess()
				unlockWorkspace()
			}
			// Keep both guards through creation so another process cannot
			// claim the exact Pollen path between removal and replacement.
			defer unlockExisting()
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return DelegatedWorkspace{}, fmt.Errorf("create delegated workspace root: %w", err)
	}

	// Cut from the repository's resolved default branch, never from whatever
	// the substrate checkout happens to be on. The workspace's starting point
	// is as much a thing that must not be assumed as the default branch's name.
	var err error
	if resolvedStartPoint == "" {
		resolvedStartPoint, err = workspaceStartPointFor(ctx, base, configuredBranch, credential)
		if err != nil {
			return DelegatedWorkspace{}, err
		}
	}

	branch, err := uniqueOwnedWorkspaceBranchName(ctx, base, trimmedPollen)
	if err != nil {
		return DelegatedWorkspace{}, err
	}
	if _, err := runGitCommitCommandFn(ctx, base, "worktree", "add", "-b", branch, path, resolvedStartPoint); err != nil {
		return DelegatedWorkspace{}, fmt.Errorf("create isolated workspace for pollen %q on substrate %q: %w", trimmedPollen, substrateName, err)
	}
	workspace.Branch = branch

	baseCommit := ""
	if out, revErr := runGitCommitCommandFn(ctx, path, "rev-parse", "HEAD"); revErr == nil {
		baseCommit = strings.TrimSpace(out)
	}
	// Registered at creation: a reference nobody recorded is a reference
	// nobody can ever decide is finished.
	if registerErr := RegisterOwnedRef(OwnedRef{
		Repository: base,
		Branch:     branch,
		Purpose:    PurposeDelegatedWorkspace,
		Pollen:     trimmedPollen,
		Base:       baseCommit,
	}); registerErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Could not record ownership of %s: %v\n", branch, registerErr)
	}

	return workspace, nil
}

// workspaceStartPoint resolves what a new delegated workspace should be cut
// from the resolved default branch. A successful fetch makes origin/<branch>
// the freshest source; when that fetch is unavailable, the local resolved
// default branch is preferred over a potentially stale remote-tracking ref.
//
// It returns a resolved COMMIT, not a reference name, and that matters. A
// worktree has its own HEAD, so a name like "HEAD" means one thing in the
// substrate and another inside the workspace. Resolving it here, against the
// substrate, removes the ambiguity before the value travels anywhere.
func workspaceStartPoint(ctx context.Context, base string) (string, error) {
	return workspaceStartPointFor(ctx, base, "", ResolvedCredential{})
}

func workspaceStartPointFor(ctx context.Context, base, configuredBranch string, credential ResolvedCredential) (string, error) {
	unlockRemoteRefs, lockErr := lockCommonGitRemoteRefs(ctx, base)
	if lockErr != nil {
		return "", fmt.Errorf("lock repository remote refs: %w", lockErr)
	}
	defer unlockRemoteRefs()

	resolution := ResolveDefaultBranch(ctx, base, configuredBranch, credential)

	refreshed := refreshRemoteDefaultBranch(ctx, base, resolution)

	if !resolution.Known() {
		return "", fmt.Errorf("substrate %q default branch could not be resolved; refusing to start from the current checkout", base)
	}
	candidates := []string{resolution.Branch, "origin/" + resolution.Branch}
	if refreshed {
		candidates[0], candidates[1] = candidates[1], candidates[0]
	}

	for _, candidate := range candidates {
		commit, err := runGitCommitCommandFn(ctx, base, "rev-parse", "--verify", "--quiet", candidate)
		if err != nil {
			continue
		}
		if trimmed := strings.TrimSpace(commit); trimmed != "" {
			return trimmed, nil
		}
	}

	return "", fmt.Errorf("resolved default branch %q for Substrate %q has no available commit; refusing to start from the current checkout", resolution.Branch, base)
}

func uniqueOwnedWorkspaceBranchName(ctx context.Context, repository, pollen string) (string, error) {
	base := ownedWorkspaceBranchName(pollen)
	local, err := runGitCommitCommandFn(ctx, repository, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return "", fmt.Errorf("inspect local branches before workspace creation: %w", err)
	}
	remote, err := runGitCommitCommandFn(ctx, repository, "for-each-ref", "--format=%(refname:short)", "refs/remotes")
	if err != nil {
		return "", fmt.Errorf("inspect remote branches before workspace creation: %w", err)
	}
	exists := func(candidate string) bool {
		for _, branch := range strings.Split(local+"\n"+remote, "\n") {
			branch = strings.TrimSpace(branch)
			if branch == candidate || strings.HasSuffix(branch, "/"+candidate) {
				return true
			}
		}
		return false
	}
	if !exists(base) {
		return base, nil
	}
	for suffix := 2; suffix < 10000; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if !exists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a collision-free workspace branch for Pollen %q", pollen)
}

// refreshRemoteDefaultBranch updates the remote-tracking ref the start point is
// cut from, so a workspace begins at the default branch as it is now rather than
// as it was whenever the substrate was last fetched.
//
// Deliberately best-effort and never fatal. Starting from a slightly stale
// default branch produces a clean merge later; starting from a sibling in-flight
// branch does not, and that is the failure this whole function guards. Refusing
// to create a workspace because a fetch could not run would trade the harmless
// problem for a worse one.
//
// GIT_TERMINAL_PROMPT=0 is what makes "best effort" true. Without it a private
// substrate with no usable credential leaves git waiting on a terminal that is
// not there, turning workspace creation into a hang rather than a fast failure.
func refreshRemoteDefaultBranch(ctx context.Context, base string, resolution DefaultBranchResolution) bool {
	if !resolution.Known() {
		return false
	}
	_, err := runGitFetchCommandFn(ctx, base, []string{"GIT_TERMINAL_PROMPT=0"}, "fetch", "origin", resolution.Branch)
	return err == nil
}

// runGitFetchCommandFn is the seam for the start-point refresh, so a test can
// observe whether the fetch was attempted without reaching a network.
var runGitFetchCommandFn = runGitCommandWithEnv

// ownedWorkspaceBranchName builds the branch a Pollinator works on. The shape is
// uniform and machine-generated on purpose: consistent names are what make the
// lifecycle trackable, and they carry the Pollinator so a branch is attributable
// at a glance in any repository listing.
func ownedWorkspaceBranchName(pollen string) string {
	return fmt.Sprintf("tendril/%s/work", sanitizeWorkspaceComponent(pollen))
}
