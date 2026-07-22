package conductor

import (
	"context"
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
// visible to the substrate as branches — which is what keeps push, pull requests
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
// covers the realistic case — one Stem serving many Pollinators — and
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

// ResolveDelegatedWorkspace returns the workspace an operation should run in.
//
// With no pollen — a human at a terminal — it returns the substrate's own
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
	if ctx == nil {
		ctx = context.Background()
	}
	base := strings.TrimSpace(substratePath)
	if base == "" {
		return DelegatedWorkspace{}, fmt.Errorf("substrate path is required to resolve a workspace")
	}
	trimmedPollen := strings.TrimSpace(pollen)
	if trimmedPollen == "" {
		return DelegatedWorkspace{Path: base}, nil
	}

	name := sanitizeWorkspaceComponent(substrateName)
	if strings.TrimSpace(substrateName) == "" {
		name = sanitizeWorkspaceComponent(filepath.Base(base))
	}
	path := filepath.Join(delegatedWorkspaceRoot(), name, sanitizeWorkspaceComponent(trimmedPollen))

	workspace := DelegatedWorkspace{Path: path, Pollen: trimmedPollen, Isolated: true}
	if isGitRepo(path) {
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
		return workspace, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return DelegatedWorkspace{}, fmt.Errorf("create delegated workspace root: %w", err)
	}

	// Cut from the repository's resolved default branch, never from whatever
	// the substrate checkout happens to be on — the workspace's starting point
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
// current default branch when the old one is finished — meaning it holds
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
// substrate and another inside the workspace — resolving it here, against the
// substrate, removes the ambiguity before the value travels anywhere.
func workspaceStartPoint(ctx context.Context, base string) (string, error) {
	resolution := ResolveDefaultBranchLocal(ctx, base, "")
	candidates := []string{}
	if resolution.Known() {
		candidates = append(candidates, "origin/"+resolution.Branch, resolution.Branch)
	}
	candidates = append(candidates, "HEAD")

	for _, candidate := range candidates {
		commit, err := runGitCommitCommandFn(ctx, base, "rev-parse", "--verify", "--quiet", candidate)
		if err != nil {
			continue
		}
		if trimmed := strings.TrimSpace(commit); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("substrate %q has no commits to start a workspace from", base)
}

// ownedWorkspaceBranchName builds the branch a Pollinator works on. The shape is
// uniform and machine-generated on purpose: consistent names are what make the
// lifecycle trackable, and they carry the Pollinator so a branch is attributable
// at a glance in any repository listing.
func ownedWorkspaceBranchName(pollen string) string {
	return fmt.Sprintf("tendril/%s/work", sanitizeWorkspaceComponent(pollen))
}
