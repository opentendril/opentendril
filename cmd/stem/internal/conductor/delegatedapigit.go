package conductor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

	allAdditions, allDeletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, nil)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: enumerate workspace changes: %w", err)
	}
	selectedAdditions, selectedDeletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, execution.Paths)
	if err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: enumerate selected changes: %w", err)
	}
	if len(selectedAdditions) == 0 && len(selectedDeletions) == 0 {
		if len(allAdditions)+len(allDeletions) > 0 {
			recovered, found, recoveryErr := recoverDelegatedAPICommitAtHead(ctx, execution, branch, headOID)
			if recoveryErr != nil {
				return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: verify previously reconciled intent: %w", recoveryErr)
			}
			if found {
				return recovered, nil
			}
		}
		return GitCommitResult{Status: "nothing-to-commit"}, nil
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

	intent, err := validateAPIFruitIntent(owner, repo, branch, headOID, execution.Message, selectedAdditions, selectedDeletions)
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

	if err := reconcileDelegatedAPICommitWorkspace(ctx, execution, intent, token, commitOID, allAdditions, allDeletions, selectedAdditions, selectedDeletions); err != nil {
		return GitCommitResult{}, fmt.Errorf("api-mode delegated commit: remote commit %s exists but local workspace reconciliation failed: %w", commitOID, err)
	}
	return GitCommitResult{Status: "committed", CommitHash: commitOID}, nil
}

func recoverDelegatedAPICommitAtHead(ctx context.Context, execution GitCommitExecution, branch, headOID string) (GitCommitResult, bool, error) {
	message, err := runGitCommitCommandFn(ctx, execution.Workspace, "show", "-s", "--format=%B", "HEAD")
	if err != nil {
		return GitCommitResult{}, false, err
	}
	gotHeadline, gotBody := splitCommitMessage(message)
	wantHeadline, wantBody := splitCommitMessage(execution.Message)
	if gotHeadline != wantHeadline || gotBody != wantBody {
		return GitCommitResult{}, false, nil
	}
	baseOID, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD^^{commit}")
	if err != nil {
		return GitCommitResult{}, false, nil
	}
	baseOID = strings.TrimSpace(baseOID)
	additions, deletions, err := apiCommitFileChangesBetweenCommits(ctx, execution.Workspace, baseOID, headOID)
	if err != nil {
		return GitCommitResult{}, false, err
	}
	if len(additions)+len(deletions) == 0 {
		return GitCommitResult{}, false, nil
	}
	if len(execution.Paths) > 0 {
		args := []string{"diff", "--name-only", "-z", "--no-renames", baseOID, headOID, "--"}
		args = append(args, execution.Paths...)
		selectedPaths, err := runGitCommandRawOutput(ctx, execution.Workspace, args...)
		if err != nil {
			return GitCommitResult{}, false, err
		}
		if !sameAPICommitPathSet(apiCommitChangePaths(additions, deletions), strings.Split(strings.TrimSuffix(selectedPaths, "\x00"), "\x00")) {
			return GitCommitResult{}, false, nil
		}
	}
	originURL, err := runGitCommitCommandFn(ctx, execution.Workspace, "remote", "get-url", "origin")
	if err != nil {
		return GitCommitResult{}, false, err
	}
	originURL = strings.TrimSpace(originURL)
	owner, repo, err := parseOwnerRepo(originURL)
	if err != nil {
		return GitCommitResult{}, false, err
	}
	token, err := githubAppInstallationToken(ctx, execution.Credential.App, originURL)
	if err != nil {
		return GitCommitResult{}, false, fmt.Errorf("GitHub App authentication failed")
	}
	intent, err := validateAPIFruitIntent(owner, repo, branch, baseOID, execution.Message, additions, deletions)
	if err != nil {
		return GitCommitResult{}, false, err
	}
	state, err := reconcileAPIFruit(ctx, intent, token)
	if err != nil {
		return GitCommitResult{}, false, err
	}
	if state.Outcome != apiFruitReconciledExact || state.OID != headOID {
		return GitCommitResult{}, false, nil
	}
	currentBranch, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil {
		return GitCommitResult{}, false, err
	}
	currentHead, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return GitCommitResult{}, false, err
	}
	if strings.TrimSpace(currentBranch) != branch || strings.TrimSpace(currentHead) != headOID {
		return GitCommitResult{}, false, fmt.Errorf("workspace branch or HEAD changed while verifying the existing commit")
	}
	selectedAdditions, selectedDeletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, execution.Paths)
	if err != nil {
		return GitCommitResult{}, false, err
	}
	if len(selectedAdditions)+len(selectedDeletions) != 0 {
		return GitCommitResult{}, false, fmt.Errorf("selected workspace paths changed while verifying the existing commit")
	}
	return GitCommitResult{Status: "committed", CommitHash: headOID}, true, nil
}

func apiCommitFileChangesBetweenCommits(ctx context.Context, workspace, baseOID, headOID string) ([]apiCommitFileAddition, []apiCommitFileDeletion, error) {
	raw, err := runGitCommandRawOutput(ctx, workspace, "diff", "--name-status", "-z", "--no-renames", baseOID, headOID)
	if err != nil {
		return nil, nil, err
	}
	entries := strings.Split(raw, "\x00")
	var additions []apiCommitFileAddition
	var deletions []apiCommitFileDeletion
	for index := 0; index+1 < len(entries); index += 2 {
		status := entries[index]
		path := filepath.ToSlash(entries[index+1])
		if status == "" || path == "" {
			continue
		}
		if strings.HasPrefix(status, "D") {
			deletions = append(deletions, apiCommitFileDeletion{Path: path})
			continue
		}
		contents, err := runGitCommandRawOutput(ctx, workspace, "show", headOID+":"+path)
		if err != nil {
			return nil, nil, fmt.Errorf("read committed path %s: %w", path, err)
		}
		additions = append(additions, apiCommitFileAddition{Path: path, Contents: base64.StdEncoding.EncodeToString([]byte(contents))})
	}
	sort.Slice(additions, func(i, j int) bool { return additions[i].Path < additions[j].Path })
	sort.Slice(deletions, func(i, j int) bool { return deletions[i].Path < deletions[j].Path })
	if additions == nil {
		additions = []apiCommitFileAddition{}
	}
	if deletions == nil {
		deletions = []apiCommitFileDeletion{}
	}
	return additions, deletions, nil
}

func apiCommitChangePaths(additions []apiCommitFileAddition, deletions []apiCommitFileDeletion) []string {
	paths := make([]string, 0, len(additions)+len(deletions))
	for _, addition := range additions {
		paths = append(paths, addition.Path)
	}
	for _, deletion := range deletions {
		paths = append(paths, deletion.Path)
	}
	sort.Strings(paths)
	return paths
}

func sameAPICommitPathSet(want, got []string) bool {
	if len(got) == 1 && got[0] == "" {
		got = nil
	}
	if len(want) != len(got) {
		return false
	}
	sort.Strings(got)
	for index := range want {
		if want[index] != got[index] {
			return false
		}
	}
	return true
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
	if !errors.As(createErr, &mutationErr) || (!mutationErr.RequestWritten && !mutationErr.ResponseReceived) {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-creation", apiFruitOutcomePreMutationFailure, false, mutationRequestID(createErr), apiFruitFailureMessage(apiFruitOutcomePreMutationFailure))
	}
	state, err = reconcileAPIFruit(ctx, intent, token)
	if err != nil {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailureWithStatus("reconciliation", apiFruitOutcomeReconciliationFailure, false, mutationErr.RequestID, mutationErr.StatusCode, apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	if state.Outcome == apiFruitReconciledBase || state.Outcome == apiFruitReconciledExact {
		return state, nil
	}
	if state.Outcome != apiFruitReconciledAbsent {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailureWithStatus("target-ref-creation", apiFruitOutcomeTargetRefConflict, false, mutationErr.RequestID, mutationErr.StatusCode, apiFruitFailureMessage(apiFruitOutcomeTargetRefConflict))
	}
	if !ambiguousGitHubMutation(mutationErr) {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailureWithStatus("target-ref-creation", apiFruitOutcomeTargetRefAbsent, false, mutationErr.RequestID, mutationErr.StatusCode, apiFruitFailureMessage(apiFruitOutcomeTargetRefAbsent))
	}

	// One identical create is permitted only after an ambiguous outcome and a
	// read proving that the target remains absent. Explicit 4xx responses are
	// deterministic failures and are never replayed.
	secondErr := githubCreateRef(ctx, intent.Owner, intent.Repo, intent.Branch, intent.BaseCommit, token)
	if secondErr == nil {
		state, err = reconcileAPIFruit(ctx, intent, token)
		if err == nil && (state.Outcome == apiFruitReconciledBase || state.Outcome == apiFruitReconciledExact) {
			return state, nil
		}
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("reconciliation", apiFruitOutcomeReconciliationFailure, false, mutationRequestID(createErr), apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	var secondMutationErr *githubMutationError
	if !errors.As(secondErr, &secondMutationErr) || (!secondMutationErr.RequestWritten && !secondMutationErr.ResponseReceived) {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailure("target-ref-creation", apiFruitOutcomeRetryExhausted, false, mutationRequestID(secondErr), apiFruitFailureMessage(apiFruitOutcomeRetryExhausted))
	}
	state, err = reconcileAPIFruit(ctx, intent, token)
	if err != nil {
		return apiFruitReconciliation{}, newAPIFruitPublicationFailureWithStatus("reconciliation", apiFruitOutcomeReconciliationFailure, false, secondMutationErr.RequestID, secondMutationErr.StatusCode, apiFruitFailureMessage(apiFruitOutcomeReconciliationFailure))
	}
	if state.Outcome == apiFruitReconciledBase || state.Outcome == apiFruitReconciledExact {
		return state, nil
	}
	if explicitGitHub4xx(secondMutationErr) {
		outcome := apiFruitOutcomeTargetRefConflict
		if state.Outcome == apiFruitReconciledAbsent {
			outcome = apiFruitOutcomeTargetRefAbsent
		}
		return apiFruitReconciliation{}, newAPIFruitPublicationFailureWithStatus("target-ref-creation", outcome, false, secondMutationErr.RequestID, secondMutationErr.StatusCode, apiFruitFailureMessage(outcome))
	}
	return apiFruitReconciliation{}, newAPIFruitPublicationFailureWithStatus("target-ref-creation", apiFruitOutcomeRetryExhausted, false, secondMutationErr.RequestID, secondMutationErr.StatusCode, apiFruitFailureMessage(apiFruitOutcomeRetryExhausted))
}

func reconcileDelegatedAPICommitWorkspace(ctx context.Context, execution GitCommitExecution, intent apiFruitPublicationIntent, token, targetOID string, allAdditions []apiCommitFileAddition, allDeletions []apiCommitFileDeletion, selectedAdditions []apiCommitFileAddition, selectedDeletions []apiCommitFileDeletion) error {
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
	if !sameAPICommitChanges(allAdditions, allDeletions, currentAdditions, currentDeletions) {
		return fmt.Errorf("workspace changes differ from the captured pre-commit state")
	}
	beforeSnapshot, err := snapshotDelegatedAPIWorkspaceChanges(execution.Workspace, allAdditions, allDeletions)
	if err != nil {
		return fmt.Errorf("snapshot workspace changes before reconciliation: %w", err)
	}
	expectedRemainingAdditions, expectedRemainingDeletions := withoutSelectedAPICommitChanges(allAdditions, allDeletions, selectedAdditions, selectedDeletions)

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

	// Recheck immediately before moving HEAD. The mixed reset updates HEAD and
	// the index to the authoritative commit while preserving every worktree
	// file, including unselected changes.
	branch, err = runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != intent.Branch {
		return fmt.Errorf("workspace branch changed before authoritative reconciliation")
	}
	head, err = runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(head) != intent.BaseCommit {
		return fmt.Errorf("workspace HEAD changed before authoritative reconciliation")
	}
	currentAdditions, currentDeletions, err = apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, nil)
	if err != nil {
		return fmt.Errorf("reinspect workspace changes before reconciliation: %w", err)
	}
	if !sameAPICommitChanges(allAdditions, allDeletions, currentAdditions, currentDeletions) {
		return fmt.Errorf("workspace changes changed before authoritative reconciliation")
	}
	currentSnapshot, err := snapshotDelegatedAPIWorkspaceChanges(execution.Workspace, allAdditions, allDeletions)
	if err != nil {
		return fmt.Errorf("resnapshot workspace changes before reconciliation: %w", err)
	}
	if !sameDelegatedAPIWorkspaceSnapshot(beforeSnapshot, currentSnapshot) {
		return fmt.Errorf("workspace file contents changed before authoritative reconciliation")
	}

	if _, err := runGitAPICommitReconcileCommandFn(ctx, execution.Workspace, nil, "reset", "--mixed", targetOID); err != nil {
		return fmt.Errorf("move isolated workspace HEAD to authoritative commit while preserving worktree changes: %w", err)
	}
	branch, err = runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != intent.Branch {
		return fmt.Errorf("workspace branch changed during reconciliation")
	}
	head, err = runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(head) != targetOID {
		return fmt.Errorf("workspace HEAD does not match authoritative commit")
	}
	afterSnapshot, err := snapshotDelegatedAPIWorkspaceChanges(execution.Workspace, allAdditions, allDeletions)
	if err != nil {
		return fmt.Errorf("snapshot workspace changes after reconciliation: %w", err)
	}
	if !sameDelegatedAPIWorkspaceSnapshot(beforeSnapshot, afterSnapshot) {
		return fmt.Errorf("workspace file contents changed during authoritative reconciliation")
	}
	remainingAdditions, remainingDeletions, err := apiCommitFileChangesFromWorkspace(ctx, execution.Workspace, nil)
	if err != nil {
		return fmt.Errorf("verify reconciled workspace status: %w", err)
	}
	if !sameAPICommitChanges(expectedRemainingAdditions, expectedRemainingDeletions, remainingAdditions, remainingDeletions) {
		return fmt.Errorf("reconciled workspace does not contain exactly the preserved unselected changes")
	}
	return nil
}

type delegatedAPIWorkspaceFileSnapshot struct {
	mode    os.FileMode
	content []byte
	exists  bool
	link    string
}

func snapshotDelegatedAPIWorkspaceChanges(workspace string, additions []apiCommitFileAddition, deletions []apiCommitFileDeletion) (map[string]delegatedAPIWorkspaceFileSnapshot, error) {
	paths := make(map[string]struct{}, len(additions)+len(deletions))
	for _, addition := range additions {
		paths[addition.Path] = struct{}{}
	}
	for _, deletion := range deletions {
		paths[deletion.Path] = struct{}{}
	}
	snapshot := make(map[string]delegatedAPIWorkspaceFileSnapshot, len(paths))
	for path := range paths {
		fullPath := filepath.Join(workspace, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if os.IsNotExist(err) {
			snapshot[path] = delegatedAPIWorkspaceFileSnapshot{}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}
		entry := delegatedAPIWorkspaceFileSnapshot{exists: true, mode: info.Mode()}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.link, err = os.Readlink(fullPath)
		} else if info.Mode().IsRegular() {
			entry.content, err = os.ReadFile(fullPath)
		} else {
			return nil, fmt.Errorf("unsupported workspace file type for %s", path)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		snapshot[path] = entry
	}
	return snapshot, nil
}

func sameDelegatedAPIWorkspaceSnapshot(left, right map[string]delegatedAPIWorkspaceFileSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for path, want := range left {
		got, ok := right[path]
		if !ok || want.exists != got.exists || want.mode != got.mode || want.link != got.link || !bytes.Equal(want.content, got.content) {
			return false
		}
	}
	return true
}

func withoutSelectedAPICommitChanges(allAdditions []apiCommitFileAddition, allDeletions []apiCommitFileDeletion, selectedAdditions []apiCommitFileAddition, selectedDeletions []apiCommitFileDeletion) ([]apiCommitFileAddition, []apiCommitFileDeletion) {
	selectedPaths := make(map[string]struct{}, len(selectedAdditions)+len(selectedDeletions))
	for _, addition := range selectedAdditions {
		selectedPaths[addition.Path] = struct{}{}
	}
	for _, deletion := range selectedDeletions {
		selectedPaths[deletion.Path] = struct{}{}
	}
	remainingAdditions := make([]apiCommitFileAddition, 0, len(allAdditions))
	for _, addition := range allAdditions {
		if _, selected := selectedPaths[addition.Path]; !selected {
			remainingAdditions = append(remainingAdditions, addition)
		}
	}
	remainingDeletions := make([]apiCommitFileDeletion, 0, len(allDeletions))
	for _, deletion := range allDeletions {
		if _, selected := selectedPaths[deletion.Path]; !selected {
			remainingDeletions = append(remainingDeletions, deletion)
		}
	}
	return remainingAdditions, remainingDeletions
}
