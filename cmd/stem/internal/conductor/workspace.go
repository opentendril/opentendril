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

// workspaceLocks serializes operations that target the same workspace.
//
// Isolation removes subject-versus-pollen corruption; it does not remove one
// pollen issuing two overlapping calls. This is an in-process lock, which
// covers the realistic case: one Stem serving many Pollinators. It
// deliberately does NOT claim to coordinate with a separate process on the same
// directory. Claiming more than it delivers would be worse than the honest
// limitation.
var workspaceLocks sync.Map

// LockWorkspace serializes access to one workspace path and returns the
// release function. Callers defer the release.
func LockWorkspace(path string) func() {
	value, _ := workspaceLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
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
	return ResolveDelegatedWorkspaceWithMode(ctx, substrateName, substratePath, pollen, credential, CreateDelegatedWorkspaceIfMissing)
}

// ResolveDelegatedWorkspaceWithMode returns the subject's private worktree.
// ExistingWorkspaceOnly never creates a directory/worktree or rotates its
// branch; it is the resolution mode for operations that require pre-existing
// state and must have no resolver side effects.
func ResolveDelegatedWorkspaceWithMode(ctx context.Context, substrateName, substratePath, pollen string, credential ResolvedCredential, mode DelegatedWorkspaceMode) (DelegatedWorkspace, error) {
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
		if current, err := runGitCommitCommandFn(ctx, path, "branch", "--show-current"); err == nil {
			workspace.Branch = strings.TrimSpace(current)
		}
		return workspace, nil
	}

	if isGitRepo(path) {
		// Existing-workspace branch inspection can trigger a reset when the
		// owned branch is finished. Serialize it with Git execution, but release
		// before returning because the caller acquires this same lock around its
		// own operation.
		unlockWorkspace := LockWorkspace(path)
		if !isGitRepo(path) {
			unlockWorkspace()
		} else {
			if current, err := runGitCommitCommandFn(ctx, path, "branch", "--show-current"); err == nil {
				workspace.Branch = strings.TrimSpace(current)
			}
			// A workspace whose branch is finished is cycled onto a fresh one, so
			// the next piece of work starts from the current default branch rather
			// than piling onto something already merged. This is the other half of
			// owning a reference: it is reclaimed at the moment its purpose ends,
			// which for a subject's working branch is the moment its work lands.
			if rotated, err := rotateFinishedWorkspaceBranch(ctx, base, path, workspace.Branch, trimmedPollen, credential); err == nil && rotated != "" {
				workspace.Branch = rotated
			}
			unlockWorkspace()
			return workspace, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return DelegatedWorkspace{}, fmt.Errorf("create delegated workspace root: %w", err)
	}

	// Cut from the repository's resolved default branch, never from whatever
	// the substrate checkout happens to be on. The workspace's starting point
	// is as much a thing that must not be assumed as the default branch's name.
	startPoint, err := workspaceStartPoint(ctx, base)
	if err != nil {
		return DelegatedWorkspace{}, err
	}

	branch := ownedWorkspaceBranchName(trimmedPollen)
	if _, err := runGitCommitCommandFn(ctx, base, "worktree", "add", "-b", branch, path, startPoint); err != nil {
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

// rotateFinishedWorkspaceBranch resets a subject's working branch onto the
// current default branch when the old one is finished, meaning it holds
// nothing, or everything it held has merged. It returns the branch name when
// it rotated, and "" when the branch was left alone.
//
// Anything else is left strictly alone: a branch carrying unmerged commits is
// the subject's work in progress, and resetting it would destroy exactly what
// this whole design exists to protect.
func rotateFinishedWorkspaceBranch(ctx context.Context, base, workspacePath, branch, pollen string, credential ResolvedCredential) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch != ownedWorkspaceBranchName(pollen) {
		return "", nil
	}

	var ref OwnedRef
	for _, candidate := range OwnedRefsFor(base) {
		if candidate.Branch == branch {
			ref = candidate
			break
		}
	}
	if ref.Branch == "" {
		return "", nil
	}

	finished := branchHasNoWork(ctx, workspacePath, ref)
	if !finished {
		merged, _ := ownedRefIsMerged(ctx, workspacePath, ref, credential)
		finished = merged
	}
	if !finished {
		return "", nil
	}

	startPoint, err := workspaceStartPoint(ctx, base)
	if err != nil {
		return "", err
	}
	// Already current: rotating would achieve nothing.
	if current, err := runGitCommitCommandFn(ctx, workspacePath, "rev-parse", "HEAD"); err == nil {
		if target, targetErr := runGitCommitCommandFn(ctx, workspacePath, "rev-parse", startPoint); targetErr == nil {
			if strings.TrimSpace(current) == strings.TrimSpace(target) {
				return "", nil
			}
		}
	}

	if _, err := runGitCommitCommandFn(ctx, workspacePath, "checkout", "-B", branch, startPoint); err != nil {
		return "", err
	}
	baseCommit := ""
	if out, revErr := runGitCommitCommandFn(ctx, workspacePath, "rev-parse", "HEAD"); revErr == nil {
		baseCommit = strings.TrimSpace(out)
	}
	_ = RegisterOwnedRef(OwnedRef{
		Repository: base,
		Branch:     branch,
		Purpose:    PurposeDelegatedWorkspace,
		Pollen:     pollen,
		Base:       baseCommit,
	})
	return branch, nil
}

// workspaceStartPoint resolves what a new delegated workspace should be cut
// from: the remote-tracking default branch when there is one (so a Pollinator
// starts from what the remote actually has), then the local default branch,
// then the substrate's head as a last resort.
//
// It returns a resolved COMMIT, not a reference name, and that matters. A
// worktree has its own HEAD, so a name like "HEAD" means one thing in the
// substrate and another inside the workspace. Resolving it here, against the
// substrate, removes the ambiguity before the value travels anywhere.
func workspaceStartPoint(ctx context.Context, base string) (string, error) {
	unlockRemoteRefs, lockErr := lockCommonGitRemoteRefs(ctx, base)
	if lockErr != nil {
		return "", fmt.Errorf("lock repository remote refs: %w", lockErr)
	}
	defer unlockRemoteRefs()

	resolution := ResolveDefaultBranchLocal(ctx, base, "")

	refreshRemoteDefaultBranch(ctx, base, resolution)

	candidates := []string{}
	if resolution.Known() {
		candidates = append(candidates, "origin/"+resolution.Branch, resolution.Branch)
	}
	// The protection floor, for the same reason IsProtected applies it: an
	// undetermined default branch is a real outcome: a clone without an
	// origin/HEAD record resolves to nothing. Without this protection, the next
	// candidate is HEAD, which is whatever the checkout was last left on. A
	// sibling branch still carrying another change's commits is exactly what
	// this start point exists to avoid inheriting.
	for _, floor := range defaultBranchProtectionFloorNames {
		candidates = append(candidates, "origin/"+floor, floor)
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

	// HEAD is the last resort rather than a silent one. A repository with no
	// default branch and no floor name is usually a fresh single-branch one,
	// where HEAD is correct, but it is also how work would be cut from a
	// sibling in-flight branch, so the caller is told which branch it inherited.
	commit, err := runGitCommitCommandFn(ctx, base, "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil && strings.TrimSpace(commit) != "" {
		current := "a detached HEAD"
		if out, branchErr := runGitCommitCommandFn(ctx, base, "branch", "--show-current"); branchErr == nil {
			if trimmed := strings.TrimSpace(out); trimmed != "" {
				current = fmt.Sprintf("branch %q", trimmed)
			}
		}
		fmt.Fprintf(os.Stderr, "⚠️ No default branch could be established for %s, so this workspace starts from %s. Anything already committed there is inherited by the new branch.\n", base, current)
		return strings.TrimSpace(commit), nil
	}

	return "", fmt.Errorf("substrate %q has no commits to start a workspace from", base)
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
func refreshRemoteDefaultBranch(ctx context.Context, base string, resolution DefaultBranchResolution) {
	if !resolution.Known() {
		return
	}
	_, _ = runGitFetchCommandFn(ctx, base, []string{"GIT_TERMINAL_PROMPT=0"}, "fetch", "origin", resolution.Branch)
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
