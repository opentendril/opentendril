package conductor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// runGitAPICommitReconcileCommandFn keeps the narrowly scoped workspace
// reconciliation injectable without changing the ordinary git command path.
var runGitAPICommitReconcileCommandFn = runGitCommandWithEnv

func runDelegatedAPICommit(ctx context.Context, execution GitCommitExecution) (GitCommitResult, error) {
	branch, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: determine current branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit requires the current feature branch")
	}
	if err := validateDelegatedAPICommitWorkspace(ctx, execution, branch); err != nil {
		return GitCommitResult{}, err
	}

	headOID, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: resolve pre-commit HEAD: %w", err)
	}
	headOID = strings.TrimSpace(headOID)

	additions, deletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, nil)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: enumerate workspace changes: %w", err)
	}
	if len(additions) == 0 && len(deletions) == 0 {
		return GitCommitResult{Status: "nothing-to-commit"}, nil
	}
	selectedAdditions, selectedDeletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, execution.Paths)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: enumerate selected changes: %w", err)
	}
	if !sameAPICommitChanges(additions, deletions, selectedAdditions, selectedDeletions) {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit requires all workspace changes so its successful result can be reconciled cleanly")
	}

	originURL, err := runGitCommitCommandFn(ctx, execution.Workspace, "remote", "get-url", "origin")
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: resolve origin remote: %w", err)
	}
	originURL = strings.TrimSpace(originURL)
	owner, repo, err := parseOwnerRepo(originURL)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: %w", err)
	}
	token, err := githubAppInstallationToken(ctx, execution.Credential.App, originURL)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: GitHub App authentication failed")
	}

	intent, err := validateAPIFruitIntent(owner, repo, branch, headOID, execution.Message, additions, deletions)
	if err != nil {
		return GitCommitResult{}, err
	}
	branchState, err := establishDelegatedAPICommitBranch(ctx, intent, token)
	if err != nil {
		return GitCommitResult{}, err
	}
	commitOID := branchState.OID
	if branchState.Outcome != apiFruitReconciledExact {
		commitOID, err = publishAPIFruitCommit(ctx, token, intent)
		if err != nil {
			return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: %w", err)
		}
	}

	if err := reconcileDelegatedAPICommitWorkspace(ctx, execution, intent, token, commitOID, additions, deletions); err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: remote commit %s exists but local workspace reconciliation failed: %w", commitOID, err)
	}
	return GitCommitResult{Status: "committed", CommitHash: commitOID}, nil
}

func sameAPICommitChanges(wantAdditions []apiCommitFileAddition, wantDeletions []apiCommitFileDeletion, gotAdditions []apiCommitFileAddition, gotDeletions []apiCommitFileDeletion) bool {
	if len(wantAdditions) != len(gotAdditions) || len(wantDeletions) != len(gotDeletions) {
		return false
	}
	for i := range wantAdditions {
		if wantAdditions[i] != gotAdditions[i] {
			return false
		}
	}
	for i := range wantDeletions {
		if wantDeletions[i] != gotDeletions[i] {
			return false
		}
	}
	return true
}

func validateDelegatedAPICommitWorkspace(ctx context.Context, execution GitCommitExecution, branch string) error {
	pollen := strings.TrimSpace(execution.Pollen)
	if pollen == "" || strings.TrimSpace(execution.Repository) == "" {
		return fmt.Errorf("api-mode delegated commit requires an identified Pollen workspace and Substrate checkout")
	}
	repository, err := absoluteRunWorkspaceRepository(ctx, execution.Repository)
	if err != nil {
		return fmt.Errorf("api-mode delegated commit: resolve Substrate checkout: %w", err)
	}
	workspacePath, err := filepath.Abs(strings.TrimSpace(execution.Workspace))
	if err != nil {
		return fmt.Errorf("api-mode delegated commit: resolve workspace path: %w", err)
	}
	workspacePath, err = resolveRunWorkspacePath(workspacePath)
	if err != nil {
		return fmt.Errorf("api-mode delegated commit: resolve workspace path: %w", err)
	}
	workspaceRoot, err := filepath.Abs(delegatedWorkspaceRoot())
	if err != nil {
		return fmt.Errorf("api-mode delegated commit: resolve delegated workspace root: %w", err)
	}
	workspaceRoot, err = resolveRunWorkspacePath(workspaceRoot)
	if err != nil {
		return fmt.Errorf("api-mode delegated commit: resolve delegated workspace root: %w", err)
	}
	substrateName := strings.TrimSpace(execution.Substrate)
	if substrateName == "" {
		substrateName = filepath.Base(repository)
	}
	expectedPath := filepath.Join(workspaceRoot, sanitizeWorkspaceComponent(substrateName), sanitizeWorkspaceComponent(pollen))
	if workspacePath != expectedPath {
		return fmt.Errorf("api-mode delegated commit refused: workspace is not the isolated workspace for this Pollen and Substrate")
	}
	registered, err := runWorkspaceWorktreeMatches(ctx, repository, workspacePath, branch)
	if err != nil {
		return fmt.Errorf("api-mode delegated commit: verify isolated worktree: %w", err)
	}
	if !registered {
		return fmt.Errorf("api-mode delegated commit refused: current feature branch is not checked out in this Pollen workspace")
	}
	return nil
}

func establishDelegatedAPICommitBranch(ctx context.Context, intent apiFruitPublicationIntent, token string) (apiFruitReconciliation, error) {
	state, err := reconcileAPIFruit(ctx, intent, token)
	if err != nil {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, "", apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	switch state.Outcome {
	case apiFruitReconciledBase, apiFruitReconciledExact:
		return state, nil
	case apiFruitReconciledUnexpected:
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-check", apiFruitOutcomeTargetRefConflict, false, "", apiFruitFailureMessage(apiFruitOutcomeTargetRefConflict))
	case apiFruitReconciledAbsent:
		// The target feature ref is absent, the only state in which this
		// authorized commit may create it. Its sole permitted value is the
		// exact pre-commit workspace HEAD.
	default:
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-check", apiFruitOutcomeUnexpectedState, false, "", apiFruitFailureMessage(apiFruitOutcomeUnexpectedState))
	}

	createErr := githubCreateRef(ctx, intent.Owner, intent.Repo, intent.Branch, intent.BaseCommit, token)
	if createErr == nil {
		state, err = reconcileAPIFruit(ctx, intent, token)
		if err != nil {
			return apiFruitReconciliation{}, newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, "", apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
		}
		if state.Outcome == apiFruitReconciledBase || state.Outcome == apiFruitReconciledExact {
			return state, nil
		}
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-check", apiFruitOutcomeTargetRefConflict, false, "", apiFruitFailureMessage(apiFruitOutcomeTargetRefConflict))
	}

	var mutationErr *githubMutationError
	if !errors.As(createErr, &mutationErr) || !mutationErr.RequestWritten {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-creation", apiFruitOutcomePreMutationFailure, false, mutationRequestID(createErr), apiFruitFailureMessage(apiFruitOutcomePreMutationFailure))
	}
	state, err = reconcileAPIFruit(ctx, intent, token)
	if err != nil {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, mutationRequestID(createErr), apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	if state.Outcome == apiFruitReconciledBase || state.Outcome == apiFruitReconciledExact {
		return state, nil
	}
	if state.Outcome != apiFruitReconciledAbsent {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-creation", apiFruitOutcomeTargetRefConflict, false, mutationRequestID(createErr), apiFruitFailureMessage(apiFruitOutcomeTargetRefConflict))
	}

	// One identical create is permitted only after a read proved that the
	// target remains absent. The failed mutation is never replayed blindly.
	secondErr := githubCreateRef(ctx, intent.Owner, intent.Repo, intent.Branch, intent.BaseCommit, token)
	if secondErr == nil {
		state, err = reconcileAPIFruit(ctx, intent, token)
		if err == nil && (state.Outcome == apiFruitReconciledBase || state.Outcome == apiFruitReconciledExact) {
			return state, nil
		}
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, mutationRequestID(createErr), apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	var secondMutationErr *githubMutationError
	if !errors.As(secondErr, &secondMutationErr) || !secondMutationErr.RequestWritten {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-creation", apiFruitOutcomeRetryExhausted, false, mutationRequestID(secondErr), apiFruitFailureMessage(apiFruitOutcomeRetryExhausted))
	}
	state, err = reconcileAPIFruit(ctx, intent, token)
	if err == nil && (state.Outcome == apiFruitReconciledBase || state.Outcome == apiFruitReconciledExact) {
		return state, nil
	}
	return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-creation", apiFruitOutcomeRetryExhausted, false, mutationRequestID(secondErr), apiFruitFailureMessage(apiFruitOutcomeRetryExhausted))
}

func reconcileDelegatedAPICommitWorkspace(ctx context.Context, execution GitCommitExecution, intent apiFruitPublicationIntent, token, targetOID string, additions []apiCommitFileAddition, deletions []apiCommitFileDeletion) error {
	branch, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != intent.Branch {
		return fmt.Errorf("workspace branch changed before reconciliation")
	}
	head, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(head) != intent.BaseCommit {
		return fmt.Errorf("workspace HEAD changed before reconciliation")
	}
	currentAdditions, currentDeletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, nil)
	if err != nil {
		return fmt.Errorf("inspect workspace changes: %w", err)
	}
	if !sameAPICommitChanges(additions, deletions, currentAdditions, currentDeletions) {
		return fmt.Errorf("workspace changes differ from the committed intent")
	}

	repository, err := absoluteRunWorkspaceRepository(ctx, execution.Repository)
	if err != nil {
		return fmt.Errorf("resolve Substrate checkout: %w", err)
	}
	unlockRemoteRefs, err := lockCommonGitRemoteRefs(ctx, repository)
	if err != nil {
		return fmt.Errorf("lock Substrate remote refs: %w", err)
	}
	defer unlockRemoteRefs()

	refspec := "refs/heads/" + intent.Branch + ":refs/remotes/origin/" + intent.Branch
	if _, err := runGitAPICommitReconcileCommandFn(ctx, repository, gitTokenCredentialEnv(token), "fetch", "--no-tags", "--no-write-fetch-head", "origin", refspec); err != nil {
		return fmt.Errorf("fetch authoritative feature branch: %w", err)
	}
	fetchedOID, err := runGitAPICommitReconcileCommandFn(ctx, repository, nil, "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+intent.Branch+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve fetched feature branch: %w", err)
	}
	if strings.TrimSpace(fetchedOID) != targetOID {
		return fmt.Errorf("remote feature branch is at %s, not authoritative commit %s", strings.TrimSpace(fetchedOID), targetOID)
	}

	if _, err := runGitAPICommitReconcileCommandFn(ctx, execution.Workspace, nil, "reset", "--hard", targetOID); err != nil {
		return fmt.Errorf("reset isolated workspace to authoritative commit: %w", err)
	}
	branch, err = runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != intent.Branch {
		return fmt.Errorf("workspace branch changed during reconciliation")
	}
	head, err = runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(head) != targetOID {
		return fmt.Errorf("workspace HEAD does not match authoritative commit")
	}
	status, err := runGitCommandRawOutput(ctx, execution.Workspace, "status", "--porcelain", "-uall", "-z")
	if err != nil {
		return fmt.Errorf("verify reconciled workspace status: %w", err)
	}
	if status != "" {
		return fmt.Errorf("workspace is not clean after reconciliation")
	}
	return nil
}
