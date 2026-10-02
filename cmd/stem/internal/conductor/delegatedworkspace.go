package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DelegatedWorkspaceTarget binds a Botanist workspace operation to one exact
// Pollen and Substrate identity. Repository is the resolved local checkout;
// Substrate is the configured name presented by the Botanist.
type DelegatedWorkspaceTarget struct {
	Pollen           string
	Substrate        string
	Repository       string
	ConfiguredBranch string
	Credential       ResolvedCredential
}

// DelegatedWorkspaceReport is a read-only snapshot. Workspace cleanliness,
// checkout occupancy, and Fruit state remain independent facts.
type DelegatedWorkspaceReport struct {
	Pollen            string
	Substrate         string
	Repository        string
	Path              string
	CurrentBranch     string
	Head              string
	Clean             bool
	CleanKnown        bool
	FruitState        string
	PullRequest       int
	FruitEvidence     string
	WorkspaceVerified bool
	BranchOwned       bool
	UniqueWork        bool
	UniqueWorkKnown   bool
	AutoReclaimable   bool
	Reason            string
}

// DelegatedWorkspaceAbandonment records which local artifacts were removed
// and which branch remains available as reviewable Fruit.
type DelegatedWorkspaceAbandonment struct {
	Report          DelegatedWorkspaceReport
	WorktreeRemoved bool
	BranchDeleted   bool
	BranchPreserved bool
	BranchReason    string
}

// InspectDelegatedWorkspace reports the exact Pollen/Substrate delegated
// worktree without creating, rotating, or reclaiming anything.
func InspectDelegatedWorkspace(ctx context.Context, target DelegatedWorkspaceTarget) (DelegatedWorkspaceReport, error) {
	path, err := delegatedWorkspacePath(target)
	if err != nil {
		return DelegatedWorkspaceReport{}, err
	}
	unlock := LockWorkspace(path)
	defer unlock()
	return inspectDelegatedWorkspaceUnlocked(ctx, target, path)
}

// AbandonDelegatedWorkspace removes only the verified delegated worktree for
// the supplied exact Pollen/Substrate pair. The explicit confirmation is
// required even when the workspace is clean; unowned or unique local branches
// are retained for review.
func AbandonDelegatedWorkspace(ctx context.Context, target DelegatedWorkspaceTarget, confirm bool) (DelegatedWorkspaceAbandonment, error) {
	if !confirm {
		return DelegatedWorkspaceAbandonment{}, fmt.Errorf("delegated workspace abandonment requires explicit confirmation")
	}
	path, err := delegatedWorkspacePath(target)
	if err != nil {
		return DelegatedWorkspaceAbandonment{}, err
	}
	unlock, err := LockWorkspaceContext(ctx, path)
	if err != nil {
		return DelegatedWorkspaceAbandonment{}, fmt.Errorf("lock delegated workspace: %w", err)
	}
	defer unlock()

	// Re-inspection happens under the same process/interprocess guard directly
	// before removal. Any failed identity or worktree check refuses destruction.
	report, err := inspectDelegatedWorkspaceUnlocked(ctx, target, path)
	if err != nil {
		return DelegatedWorkspaceAbandonment{}, err
	}
	if !report.WorkspaceVerified {
		return DelegatedWorkspaceAbandonment{}, fmt.Errorf("delegated workspace ownership could not be verified; nothing was removed")
	}
	return removeDelegatedWorkspace(ctx, target, report, true)
}

func delegatedWorkspacePath(target DelegatedWorkspaceTarget) (string, error) {
	pollen := strings.TrimSpace(target.Pollen)
	substrate := strings.TrimSpace(target.Substrate)
	repository := strings.TrimSpace(target.Repository)
	if pollen == "" || substrate == "" || repository == "" {
		return "", fmt.Errorf("exact Pollen, Substrate, and repository are required")
	}
	if pollen != target.Pollen || substrate != target.Substrate {
		return "", fmt.Errorf("Pollen and Substrate identifiers must be supplied exactly, without surrounding whitespace")
	}
	if sanitizeWorkspaceComponent(pollen) != pollen || sanitizeWorkspaceComponent(substrate) != substrate {
		return "", fmt.Errorf("Pollen and Substrate must be exact workspace path components to prove delegated ownership")
	}
	name := sanitizeWorkspaceComponent(substrate)
	path := filepath.Join(delegatedWorkspaceRoot(), name, sanitizeWorkspaceComponent(pollen))
	return path, nil
}

func inspectDelegatedWorkspaceUnlocked(ctx context.Context, target DelegatedWorkspaceTarget, path string) (DelegatedWorkspaceReport, error) {
	report := DelegatedWorkspaceReport{
		Pollen: target.Pollen, Substrate: target.Substrate,
		Repository: filepath.Clean(target.Repository), Path: path,
		FruitState: FruitStateUnverified,
	}
	canonicalRepository, err := filepath.EvalSymlinks(target.Repository)
	if err != nil {
		return report, fmt.Errorf("resolve Substrate checkout: %w", err)
	}
	canonicalPath, err := verifyDelegatedWorktreePath(ctx, canonicalRepository, path)
	if err != nil {
		return report, err
	}
	branch, err := runGitCommitCommandFn(ctx, canonicalPath, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) == "" {
		return report, fmt.Errorf("delegated workspace has no verifiable current branch")
	}
	head, err := runGitCommitCommandFn(ctx, canonicalPath, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(head) == "" {
		return report, fmt.Errorf("delegated workspace HEAD could not be verified")
	}
	report.CurrentBranch = strings.TrimSpace(branch)
	report.Head = strings.TrimSpace(head)
	report.WorkspaceVerified = true

	status, statusErr := runGitCommandRawOutput(ctx, canonicalPath, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if statusErr == nil {
		report.CleanKnown = true
		report.Clean = len(status) == 0
	}

	owned, ownedFound := delegatedOwnedRef(target.Repository, report.CurrentBranch, target.Pollen)
	report.BranchOwned = ownedFound
	if ownedFound && strings.TrimSpace(owned.Base) != "" {
		report.UniqueWorkKnown = true
		report.UniqueWork = !branchHasNoWork(ctx, canonicalRepository, owned)
	}

	branches, branchErr := RunGitBranchList(ctx, GitBranchListExecution{
		Workspace: canonicalRepository, ConfiguredBranch: target.ConfiguredBranch,
		Credential: target.Credential,
	})
	if branchErr == nil {
		for _, item := range branches.Branches {
			if item.Name == report.CurrentBranch && item.Head == report.Head {
				report.FruitState = item.FruitState
				report.PullRequest = item.PullRequest
				report.FruitEvidence = item.Reason
				break
			}
		}
	}

	noUniqueWork := report.UniqueWorkKnown && !report.UniqueWork
	reviewableFruit := report.FruitState == FruitStateOpen || report.FruitState == FruitStateClosedUnmerged
	noWorkReclaimable := noUniqueWork && !(ownedFound && owned.RetainEmpty)
	report.AutoReclaimable = report.CleanKnown && report.Clean && !reviewableFruit && (noWorkReclaimable || report.FruitState == FruitStateMerged)
	switch {
	case !report.CleanKnown:
		report.Reason = "workspace cleanliness could not be established; it is retained"
	case !report.Clean:
		report.Reason = "workspace contains tracked, untracked, ignored, or submodule changes; it is retained"
	case report.AutoReclaimable && noUniqueWork:
		report.Reason = "workspace is clean and its owned branch has no commits beyond the recorded base"
	case report.AutoReclaimable && report.FruitState == FruitStateMerged:
		report.Reason = "workspace is clean and exact branch-tip Fruit is verified merged"
	case report.FruitState == FruitStateOpen:
		report.Reason = "branch Fruit has an open pull request; it is retained"
	case report.FruitState == FruitStateClosedUnmerged:
		report.Reason = "branch Fruit has a closed-unmerged pull request; it is retained"
	case report.FruitState == FruitStateUnverified:
		report.Reason = "branch Fruit or PR state is unverified; " + report.FruitEvidence + "; it is retained"
	case !report.UniqueWorkKnown:
		report.Reason = "no exact delegated OwnedRef base proves the branch has no unique commits; it is retained"
	case noUniqueWork && ownedFound && owned.RetainEmpty:
		report.Reason = "branch was explicitly created for this Pollen and is retained for continued work"
	default:
		report.Reason = "branch contains unique committed Fruit; it is retained"
	}
	return report, nil
}

func verifyDelegatedWorktreePath(ctx context.Context, repository, path string) (string, error) {
	root := delegatedWorkspaceRoot()
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("delegated workspace root is unavailable; nothing was changed")
	}
	name := filepath.Base(filepath.Dir(path))
	parent := filepath.Join(root, name)
	parentInfo, parentErr := os.Lstat(parent)
	info, statErr := os.Lstat(path)
	if parentErr != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("exact delegated workspace path is absent or is not a real directory; nothing was changed")
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve delegated workspace path: %w", err)
	}
	relative, err := filepath.Rel(rootResolved, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("delegated workspace path is outside its verified root; nothing was changed")
	}
	top, err := runGitCommitCommandFn(ctx, resolvedPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("delegated workspace is not a Git worktree; nothing was changed")
	}
	resolvedTop, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil || filepath.Clean(resolvedTop) != filepath.Clean(resolvedPath) {
		return "", fmt.Errorf("delegated workspace Git root does not match its exact target path; nothing was changed")
	}
	baseCommon, err := runGitCommitCommandFn(ctx, repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("Substrate Git identity could not be verified; nothing was changed")
	}
	workCommon, err := runGitCommitCommandFn(ctx, resolvedPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("delegated Git identity could not be verified; nothing was changed")
	}
	baseCommonPath, baseErr := filepath.EvalSymlinks(strings.TrimSpace(baseCommon))
	workCommonPath, workErr := filepath.EvalSymlinks(strings.TrimSpace(workCommon))
	if baseErr != nil || workErr != nil || filepath.Clean(baseCommonPath) != filepath.Clean(workCommonPath) {
		return "", fmt.Errorf("delegated workspace belongs to a different Substrate; nothing was changed")
	}
	listing, err := runGitCommandRawOutput(ctx, repository, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", fmt.Errorf("Git worktree membership could not be verified; nothing was changed")
	}
	registered := false
	for _, field := range strings.Split(listing, "\x00") {
		if strings.HasPrefix(field, "worktree ") && filepath.Clean(strings.TrimPrefix(field, "worktree ")) == filepath.Clean(resolvedPath) {
			registered = true
			break
		}
	}
	if !registered {
		return "", fmt.Errorf("target path is not registered as a worktree of the exact Substrate; nothing was changed")
	}
	return resolvedPath, nil
}

func delegatedOwnedRef(repository, branch, pollen string) (OwnedRef, bool) {
	var match OwnedRef
	for _, ref := range OwnedRefsFor(repository) {
		if ref.Branch != branch {
			continue
		}
		if ref.Purpose == PurposeDelegatedWorkspace && ref.Pollen == pollen && !ref.Pending {
			return ref, true
		}
		return OwnedRef{}, false
	}
	return match, false
}

func removeDelegatedWorkspace(ctx context.Context, target DelegatedWorkspaceTarget, report DelegatedWorkspaceReport, confirmed bool) (DelegatedWorkspaceAbandonment, error) {
	result := DelegatedWorkspaceAbandonment{Report: report}
	args := []string{"worktree", "remove"}
	if confirmed {
		args = append(args, "--force")
	}
	args = append(args, report.Path)
	if _, err := runGitCommitCommandFn(ctx, target.Repository, args...); err != nil {
		return result, fmt.Errorf("remove verified delegated worktree: %w", err)
	}
	result.WorktreeRemoved = true

	owned, ownedFound := delegatedOwnedRef(target.Repository, report.CurrentBranch, target.Pollen)
	if !ownedFound {
		result.BranchPreserved = true
		result.BranchReason = "Stem ownership of this exact repository/branch/delegated-purpose/Pollen tuple is not proven"
		return result, nil
	}
	deletion := ReclaimDelegatedWorkspaceOwnedRef(
		ctx, target.Repository, owned, report.Head, report.FruitState,
		target.ConfiguredBranch, target.Credential,
	)
	if !deletion.Reclaimed {
		result.BranchPreserved = true
		result.BranchReason = deletion.Reason
		return result, nil
	}
	result.BranchDeleted = true
	result.BranchReason = deletion.Reason
	return result, nil
}
