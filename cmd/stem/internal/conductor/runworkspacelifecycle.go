package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const runWorkspaceAllocationFileName = "run-workspace-allocations.json"

type RunWorkspaceAllocationState string

const (
	RunWorkspaceAllocationPending   RunWorkspaceAllocationState = "pending"
	RunWorkspaceAllocationFinalized RunWorkspaceAllocationState = "finalized"
)

// RunWorkspaceMetadata records the explicit relation and caller facts known
// when a new RunWorkspace is allocated. SproutRunID is deliberately separate
// from the allocation RunID.
type RunWorkspaceMetadata struct {
	SproutRunID string
	Substrate   string
	PhytomerID  string
	Pollen      string
}

// RunWorkspaceAllocation is the durable identity of one concrete allocation.
// AllocationRunID names this filesystem/Git allocation. SproutRunID is an
// independently recorded HistoryDB key and is never reconstructed later.
type RunWorkspaceAllocation struct {
	AllocationRunID         string                      `json:"allocationRunId"`
	SproutRunID             string                      `json:"sproutRunId,omitempty"`
	StepID                  string                      `json:"stepId"`
	Repository              string                      `json:"repository"`
	Path                    string                      `json:"path"`
	Branch                  string                      `json:"branch"`
	BaseCommit              string                      `json:"baseCommit"`
	Substrate               string                      `json:"substrate,omitempty"`
	PhytomerID              string                      `json:"phytomerId,omitempty"`
	Pollen                  string                      `json:"pollen,omitempty"`
	CreatedAt               time.Time                   `json:"createdAt"`
	State                   RunWorkspaceAllocationState `json:"state"`
	WorkspaceRemovalPending bool                        `json:"workspaceRemovalPending,omitempty"`
	WorkspaceRemoved        bool                        `json:"workspaceRemoved,omitempty"`
	Historical              bool                        `json:"historical,omitempty"`
	OwnedRefRunID           string                      `json:"ownedRefRunId,omitempty"`
}

type runWorkspaceAllocationLedger struct {
	Version     int                      `json:"version"`
	Allocations []RunWorkspaceAllocation `json:"allocations"`
}

var runWorkspaceAllocationsMu sync.Mutex

func runWorkspaceAllocationsPath() string {
	return filepath.Join(expandHome("~/.tendril"), runWorkspaceAllocationFileName)
}

func loadRunWorkspaceAllocationsLocked() ([]RunWorkspaceAllocation, error) {
	data, err := os.ReadFile(runWorkspaceAllocationsPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read RunWorkspace allocation ledger: %w", err)
	}
	var ledger runWorkspaceAllocationLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, fmt.Errorf("decode RunWorkspace allocation ledger: %w", err)
	}
	if ledger.Version != 1 || ledger.Allocations == nil {
		return nil, fmt.Errorf("RunWorkspace allocation ledger has an unsupported or malformed format")
	}
	seenIDs := make(map[string]struct{}, len(ledger.Allocations))
	seenBranches := make(map[string]struct{}, len(ledger.Allocations))
	for _, allocation := range ledger.Allocations {
		if strings.TrimSpace(allocation.AllocationRunID) == "" ||
			strings.TrimSpace(allocation.StepID) == "" ||
			strings.TrimSpace(allocation.Repository) == "" ||
			strings.TrimSpace(allocation.Path) == "" ||
			strings.TrimSpace(allocation.Branch) == "" ||
			strings.TrimSpace(allocation.BaseCommit) == "" ||
			allocation.CreatedAt.IsZero() ||
			(allocation.State != RunWorkspaceAllocationPending && allocation.State != RunWorkspaceAllocationFinalized) ||
			allocation.Historical {
			return nil, fmt.Errorf("RunWorkspace allocation ledger contains an incomplete allocation")
		}
		if _, exists := seenIDs[allocation.AllocationRunID]; exists {
			return nil, fmt.Errorf("RunWorkspace allocation ledger repeats allocation RunID %q", allocation.AllocationRunID)
		}
		seenIDs[allocation.AllocationRunID] = struct{}{}
		branchKey := allocation.Repository + "\x00" + allocation.Branch
		if _, exists := seenBranches[branchKey]; exists {
			return nil, fmt.Errorf("RunWorkspace allocation ledger repeats repository branch %q", allocation.Branch)
		}
		seenBranches[branchKey] = struct{}{}
	}
	return ledger.Allocations, nil
}

func saveRunWorkspaceAllocationsLocked(allocations []RunWorkspaceAllocation) error {
	path := runWorkspaceAllocationsPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create RunWorkspace allocation ledger directory: %w", err)
	}
	sort.Slice(allocations, func(i, j int) bool {
		if !allocations[i].CreatedAt.Equal(allocations[j].CreatedAt) {
			return allocations[i].CreatedAt.Before(allocations[j].CreatedAt)
		}
		return allocations[i].AllocationRunID < allocations[j].AllocationRunID
	})
	data, err := json.MarshalIndent(runWorkspaceAllocationLedger{Version: 1, Allocations: allocations}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode RunWorkspace allocation ledger: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".run-workspace-allocations-*")
	if err != nil {
		return fmt.Errorf("create RunWorkspace allocation ledger temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure RunWorkspace allocation ledger temporary file: %w", err)
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write RunWorkspace allocation ledger: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync RunWorkspace allocation ledger: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close RunWorkspace allocation ledger: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace RunWorkspace allocation ledger: %w", err)
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func ReserveRunWorkspaceAllocation(allocation RunWorkspaceAllocation) error {
	if strings.TrimSpace(allocation.AllocationRunID) == "" ||
		allocation.AllocationRunID != strings.TrimSpace(allocation.AllocationRunID) ||
		strings.TrimSpace(allocation.StepID) == "" ||
		strings.TrimSpace(allocation.Repository) == "" ||
		strings.TrimSpace(allocation.Path) == "" ||
		strings.TrimSpace(allocation.Branch) == "" ||
		strings.TrimSpace(allocation.BaseCommit) == "" {
		return fmt.Errorf("RunWorkspace allocation reservation requires exact identity fields")
	}
	if allocation.State != RunWorkspaceAllocationPending || allocation.Historical {
		return fmt.Errorf("RunWorkspace allocation reservation must be pending and current")
	}
	allocation.Repository = filepath.Clean(allocation.Repository)
	allocation.Path = filepath.Clean(allocation.Path)
	if allocation.CreatedAt.IsZero() {
		allocation.CreatedAt = time.Now().UTC()
	}
	runWorkspaceAllocationsMu.Lock()
	defer runWorkspaceAllocationsMu.Unlock()
	allocations, err := loadRunWorkspaceAllocationsLocked()
	if err != nil {
		return err
	}
	for _, existing := range allocations {
		if existing.AllocationRunID == allocation.AllocationRunID {
			return fmt.Errorf("allocation RunID %q already exists", allocation.AllocationRunID)
		}
		if filepath.Clean(existing.Repository) == allocation.Repository &&
			(existing.Branch == allocation.Branch || filepath.Clean(existing.Path) == allocation.Path) {
			return fmt.Errorf("RunWorkspace allocation %q is still retained for this branch or path", existing.AllocationRunID)
		}
	}
	return saveRunWorkspaceAllocationsLocked(append(allocations, allocation))
}

func finalizeRunWorkspaceAllocation(expected RunWorkspaceAllocation) error {
	runWorkspaceAllocationsMu.Lock()
	defer runWorkspaceAllocationsMu.Unlock()
	allocations, err := loadRunWorkspaceAllocationsLocked()
	if err != nil {
		return err
	}
	for index := range allocations {
		if allocations[index].AllocationRunID != expected.AllocationRunID {
			continue
		}
		if !sameRunWorkspaceAllocation(allocations[index], expected) || allocations[index].State != RunWorkspaceAllocationPending {
			return fmt.Errorf("RunWorkspace allocation changed before finalization")
		}
		allocations[index].State = RunWorkspaceAllocationFinalized
		return saveRunWorkspaceAllocationsLocked(allocations)
	}
	return fmt.Errorf("pending RunWorkspace allocation is absent")
}

func markRunWorkspaceRemoved(expected RunWorkspaceAllocation) error {
	runWorkspaceAllocationsMu.Lock()
	defer runWorkspaceAllocationsMu.Unlock()
	allocations, err := loadRunWorkspaceAllocationsLocked()
	if err != nil {
		return err
	}
	for index := range allocations {
		if allocations[index].AllocationRunID != expected.AllocationRunID {
			continue
		}
		if !sameRunWorkspaceAllocation(allocations[index], expected) ||
			(allocations[index].State != RunWorkspaceAllocationPending && allocations[index].State != RunWorkspaceAllocationFinalized) {
			return fmt.Errorf("RunWorkspace allocation changed before teardown state update")
		}
		allocations[index].WorkspaceRemovalPending = false
		allocations[index].WorkspaceRemoved = true
		return saveRunWorkspaceAllocationsLocked(allocations)
	}
	return fmt.Errorf("RunWorkspace allocation is absent before teardown completion")
}

func markRunWorkspaceRemovalPending(expected RunWorkspaceAllocation) error {
	runWorkspaceAllocationsMu.Lock()
	defer runWorkspaceAllocationsMu.Unlock()
	allocations, err := loadRunWorkspaceAllocationsLocked()
	if err != nil {
		return err
	}
	for index := range allocations {
		if allocations[index].AllocationRunID != expected.AllocationRunID {
			continue
		}
		if !sameRunWorkspaceAllocation(allocations[index], expected) ||
			(allocations[index].State != RunWorkspaceAllocationPending && allocations[index].State != RunWorkspaceAllocationFinalized) {
			return fmt.Errorf("RunWorkspace allocation changed before teardown intent")
		}
		allocations[index].WorkspaceRemovalPending = true
		return saveRunWorkspaceAllocationsLocked(allocations)
	}
	return fmt.Errorf("RunWorkspace allocation is absent before teardown intent")
}

func RetireRunWorkspaceAllocation(expected RunWorkspaceAllocation) error {
	runWorkspaceAllocationsMu.Lock()
	defer runWorkspaceAllocationsMu.Unlock()
	allocations, err := loadRunWorkspaceAllocationsLocked()
	if err != nil {
		return err
	}
	for index := range allocations {
		if allocations[index].AllocationRunID != expected.AllocationRunID {
			continue
		}
		if !sameRunWorkspaceAllocation(allocations[index], expected) {
			return fmt.Errorf("RunWorkspace allocation changed before retirement")
		}
		allocations = append(allocations[:index], allocations[index+1:]...)
		return saveRunWorkspaceAllocationsLocked(allocations)
	}
	return fmt.Errorf("RunWorkspace allocation %q is absent", expected.AllocationRunID)
}

func sameRunWorkspaceAllocation(first, second RunWorkspaceAllocation) bool {
	return first.AllocationRunID == second.AllocationRunID &&
		first.SproutRunID == second.SproutRunID &&
		first.StepID == second.StepID &&
		filepath.Clean(first.Repository) == filepath.Clean(second.Repository) &&
		filepath.Clean(first.Path) == filepath.Clean(second.Path) &&
		first.Branch == second.Branch &&
		first.BaseCommit == second.BaseCommit &&
		first.Substrate == second.Substrate &&
		first.PhytomerID == second.PhytomerID &&
		first.Pollen == second.Pollen &&
		first.CreatedAt.Equal(second.CreatedAt) &&
		first.Historical == second.Historical &&
		first.OwnedRefRunID == second.OwnedRefRunID
}

func LoadRunWorkspaceAllocations() ([]RunWorkspaceAllocation, error) {
	runWorkspaceAllocationsMu.Lock()
	allocations, err := loadRunWorkspaceAllocationsLocked()
	runWorkspaceAllocationsMu.Unlock()
	if err != nil {
		return nil, err
	}

	// Pre-ledger owned Sprout isolation refs remain visible as incomplete
	// historical state. Their old RunID is reported separately and is never
	// promoted to allocation identity or joined to HistoryDB.
	ownedRefsMu.Lock()
	refs, ownedState, ownedErr := loadOwnedRefsStrictLocked()
	ownedRefsMu.Unlock()
	if ownedErr == nil && ownedState != OwnedRefEvidenceMissing {
		known := make(map[string]struct{}, len(allocations))
		for _, allocation := range allocations {
			known[filepath.Clean(allocation.Repository)+"\x00"+allocation.Branch] = struct{}{}
		}
		for _, ref := range refs {
			if ref.Purpose != PurposeSproutIsolation || ref.Pending {
				continue
			}
			key := filepath.Clean(ref.Repository) + "\x00" + ref.Branch
			if _, exists := known[key]; exists {
				continue
			}
			known[key] = struct{}{}
			allocations = append(allocations, RunWorkspaceAllocation{
				Repository: filepath.Clean(ref.Repository), Branch: ref.Branch,
				BaseCommit: ref.Base, CreatedAt: ref.CreatedAt,
				State: RunWorkspaceAllocationFinalized, Historical: true,
				OwnedRefRunID: ref.RunID,
			})
		}
	}
	sort.Slice(allocations, func(i, j int) bool {
		if !allocations[i].CreatedAt.Equal(allocations[j].CreatedAt) {
			return allocations[i].CreatedAt.Before(allocations[j].CreatedAt)
		}
		return allocations[i].AllocationRunID < allocations[j].AllocationRunID
	})
	return allocations, nil
}

func runWorkspaceAllocationByID(allocationRunID string) (RunWorkspaceAllocation, error) {
	allocationRunID = strings.TrimSpace(allocationRunID)
	if allocationRunID == "" || allocationRunID != strings.TrimSpace(allocationRunID) {
		return RunWorkspaceAllocation{}, fmt.Errorf("exact allocation RunID is required")
	}
	runWorkspaceAllocationsMu.Lock()
	defer runWorkspaceAllocationsMu.Unlock()
	allocations, err := loadRunWorkspaceAllocationsLocked()
	if err != nil {
		return RunWorkspaceAllocation{}, err
	}
	for _, allocation := range allocations {
		if allocation.AllocationRunID == allocationRunID {
			return allocation, nil
		}
	}
	return RunWorkspaceAllocation{}, fmt.Errorf("RunWorkspace allocation %q was not found", allocationRunID)
}

func retireRunWorkspaceAllocationForHandle(workspace RunWorkspace) error {
	allocations, err := LoadRunWorkspaceAllocations()
	if err != nil {
		return err
	}
	for _, allocation := range allocations {
		if allocation.AllocationRunID != workspace.RunID {
			continue
		}
		if !runWorkspaceHandleMatchesAllocation(workspace, allocation) {
			return fmt.Errorf("RunWorkspace allocation changed before immediate cleanup retirement")
		}
		return RetireRunWorkspaceAllocation(allocation)
	}
	// Older allocations created before the durable ledger remain governed by
	// their exact owned-reference lifecycle and have no ledger row to retire.
	return nil
}

func markRunWorkspaceRemovedForHandle(workspace RunWorkspace) error {
	allocations, err := LoadRunWorkspaceAllocations()
	if err != nil {
		return err
	}
	for _, allocation := range allocations {
		if allocation.AllocationRunID != workspace.RunID {
			continue
		}
		if allocation.State != RunWorkspaceAllocationFinalized || !runWorkspaceHandleMatchesAllocation(workspace, allocation) {
			return fmt.Errorf("RunWorkspace allocation changed before immediate teardown recording")
		}
		if allocation.WorkspaceRemoved {
			return nil
		}
		return markRunWorkspaceRemoved(allocation)
	}
	// A RunWorkspace allocated before the durable ledger existed remains
	// governed by its exact owned-reference identity and has no row to update.
	return nil
}

func markRunWorkspaceRemovalPendingForHandle(workspace RunWorkspace) error {
	allocations, err := LoadRunWorkspaceAllocations()
	if err != nil {
		return err
	}
	for _, allocation := range allocations {
		if allocation.AllocationRunID != workspace.RunID {
			continue
		}
		if allocation.State != RunWorkspaceAllocationFinalized || !runWorkspaceHandleMatchesAllocation(workspace, allocation) {
			return fmt.Errorf("RunWorkspace allocation changed before immediate teardown intent")
		}
		if allocation.WorkspaceRemoved || allocation.WorkspaceRemovalPending {
			return nil
		}
		return markRunWorkspaceRemovalPending(allocation)
	}
	// A RunWorkspace allocated before the durable ledger existed remains
	// governed by its exact owned-reference identity and has no row to update.
	return nil
}

func runWorkspaceHandleMatchesAllocation(workspace RunWorkspace, allocation RunWorkspaceAllocation) bool {
	return !allocation.Historical && allocation.AllocationRunID == workspace.RunID &&
		filepath.Clean(allocation.Repository) == filepath.Clean(workspace.Repository) &&
		filepath.Clean(allocation.Path) == filepath.Clean(workspace.Path) &&
		allocation.Branch == workspace.Branch && allocation.BaseCommit == workspace.BaseCommit &&
		(workspace.StepID == "" || allocation.StepID == workspace.StepID) &&
		(workspace.SproutRunID == "" || allocation.SproutRunID == workspace.SproutRunID)
}

type RunWorkspacePathState string

const (
	RunWorkspacePathPresent RunWorkspacePathState = "present"
	RunWorkspacePathMissing RunWorkspacePathState = "missing"
	RunWorkspacePathInvalid RunWorkspacePathState = "invalid"
	RunWorkspacePathUnknown RunWorkspacePathState = "unknown"
)

type RunWorkspaceWorktreeState string

const (
	RunWorkspaceWorktreeMatched      RunWorkspaceWorktreeState = "matched"
	RunWorkspaceWorktreeAbsent       RunWorkspaceWorktreeState = "absent"
	RunWorkspaceWorktreeInconsistent RunWorkspaceWorktreeState = "inconsistent"
	RunWorkspaceWorktreeUnknown      RunWorkspaceWorktreeState = "unknown"
)

// RunWorkspaceSnapshot contains physical facts only. Core joins it with the
// explicitly linked Sprout lifecycle evidence and makes every safety decision.
type RunWorkspaceSnapshot struct {
	Allocation         RunWorkspaceAllocation
	Ownership          OwnedRefEvidenceState
	OwnershipPending   bool
	OwnershipError     string
	PathState          RunWorkspacePathState
	PathContained      bool
	WorktreeState      RunWorkspaceWorktreeState
	CurrentBranch      string
	Head               string
	HeadKnown          bool
	BranchKnown        bool
	BranchExists       bool
	BranchHead         string
	UniqueCommitsKnown bool
	UniqueCommits      int
	BaseAncestorKnown  bool
	BaseIsAncestor     bool
	CleanKnown         bool
	Clean              bool
	InspectionError    string
}

// RunWorkspaceMutationResult records physical cleanup while leaving lifecycle
// classification to Core.
type RunWorkspaceMutationResult struct {
	WorkspaceRemoved bool
	BranchDeleted    bool
	BranchPreserved  bool
	BranchReason     string
}

func InspectRunWorkspace(ctx context.Context, allocationRunID string) (RunWorkspaceSnapshot, error) {
	allocation, err := runWorkspaceAllocationByID(allocationRunID)
	if err != nil {
		return RunWorkspaceSnapshot{}, err
	}
	return InspectRunWorkspaceAllocation(ctx, allocation)
}

func InspectRunWorkspaceAllocation(ctx context.Context, expected RunWorkspaceAllocation) (RunWorkspaceSnapshot, error) {
	if expected.Historical || strings.TrimSpace(expected.AllocationRunID) == "" {
		return RunWorkspaceSnapshot{Allocation: expected, InspectionError: "historical allocation identity is incomplete"}, nil
	}
	unlock := lockRunWorkspaceGit(expected.Repository)
	defer unlock()
	current, err := runWorkspaceAllocationByID(expected.AllocationRunID)
	if err != nil {
		return RunWorkspaceSnapshot{Allocation: expected, InspectionError: err.Error()}, nil
	}
	if !sameRunWorkspaceAllocation(current, expected) {
		return RunWorkspaceSnapshot{Allocation: expected, InspectionError: "allocation identity changed before inspection"}, nil
	}
	return inspectRunWorkspaceUnlocked(ctx, current), nil
}

func inspectRunWorkspaceUnlocked(ctx context.Context, allocation RunWorkspaceAllocation) RunWorkspaceSnapshot {
	snapshot := RunWorkspaceSnapshot{
		Allocation: allocation, PathState: RunWorkspacePathUnknown,
		WorktreeState: RunWorkspaceWorktreeUnknown,
	}
	owned := ReadOwnedRefEvidence(allocation.Repository, allocation.Branch, allocation.BaseCommit, allocation.AllocationRunID)
	snapshot.Ownership = owned.State
	snapshot.OwnershipPending = owned.Pending
	if owned.Err != nil {
		snapshot.OwnershipError = owned.Err.Error()
	}

	repository, err := filepath.EvalSymlinks(allocation.Repository)
	if err != nil {
		snapshot.InspectionError = "repository path could not be resolved"
		return snapshot
	}
	root, err := resolvedRunWorkspaceRoot()
	if err != nil {
		snapshot.InspectionError = "RunWorkspace root could not be resolved"
		return snapshot
	}
	path := filepath.Clean(allocation.Path)
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		snapshot.InspectionError = "RunWorkspace path could not be resolved"
		return snapshot
	}
	resolvedPath, resolveErr := resolveRunWorkspacePath(absolutePath)
	if resolveErr != nil {
		snapshot.InspectionError = "RunWorkspace path could not be resolved"
		return snapshot
	}
	relative, relErr := filepath.Rel(root, resolvedPath)
	snapshot.PathContained = relErr == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
	if !snapshot.PathContained {
		snapshot.PathState = RunWorkspacePathInvalid
		snapshot.InspectionError = "RunWorkspace path is outside the verified Tendril root"
		return snapshot
	}

	info, statErr := os.Lstat(absolutePath)
	switch {
	case statErr == nil && info.Mode()&os.ModeSymlink != 0:
		snapshot.PathState = RunWorkspacePathInvalid
		snapshot.InspectionError = "RunWorkspace path is a symbolic link"
		return snapshot
	case statErr == nil && !info.IsDir():
		snapshot.PathState = RunWorkspacePathInvalid
		snapshot.InspectionError = "RunWorkspace path is not a directory"
		return snapshot
	case statErr == nil:
		snapshot.PathState = RunWorkspacePathPresent
	case os.IsNotExist(statErr):
		snapshot.PathState = RunWorkspacePathMissing
	case statErr != nil:
		snapshot.InspectionError = "RunWorkspace path could not be inspected"
		return snapshot
	}

	worktreeState, err := runWorkspaceRegistrationState(ctx, repository, absolutePath, allocation.Branch)
	if err != nil {
		snapshot.InspectionError = "Git worktree registrations could not be inspected"
		return snapshot
	}
	snapshot.WorktreeState = worktreeState

	branchHead, branchErr := runGitCommand(ctx, repository, "for-each-ref", "--format=%(objectname)", "refs/heads/"+allocation.Branch)
	if branchErr == nil {
		snapshot.BranchKnown = true
		snapshot.BranchHead = strings.TrimSpace(branchHead)
		snapshot.BranchExists = snapshot.BranchHead != ""
	}
	if snapshot.BranchExists {
		baseAncestor, mergeBaseErr := runGitCommand(ctx, repository, "merge-base", allocation.BaseCommit, "refs/heads/"+allocation.Branch)
		if mergeBaseErr == nil {
			snapshot.BaseAncestorKnown = true
			snapshot.BaseIsAncestor = strings.TrimSpace(baseAncestor) == allocation.BaseCommit
			if snapshot.BaseIsAncestor {
				count, countErr := runGitCommand(ctx, repository, "rev-list", "--count", allocation.BaseCommit+"..refs/heads/"+allocation.Branch)
				if countErr == nil {
					if parsed, parseErr := strconv.Atoi(strings.TrimSpace(count)); parseErr == nil && parsed >= 0 {
						snapshot.UniqueCommitsKnown = true
						snapshot.UniqueCommits = parsed
					}
				}
			}
		}
	}

	if snapshot.PathState == RunWorkspacePathMissing {
		if (allocation.WorkspaceRemoved || allocation.WorkspaceRemovalPending) && snapshot.WorktreeState == RunWorkspaceWorktreeAbsent {
			snapshot.CleanKnown = true
			snapshot.Clean = true
		}
		return snapshot
	}
	if snapshot.PathState != RunWorkspacePathPresent || snapshot.WorktreeState != RunWorkspaceWorktreeMatched {
		return snapshot
	}
	topLevel, topErr := runGitCommand(ctx, absolutePath, "rev-parse", "--show-toplevel")
	if topErr != nil || filepath.Clean(strings.TrimSpace(topLevel)) != filepath.Clean(absolutePath) {
		snapshot.WorktreeState = RunWorkspaceWorktreeInconsistent
		snapshot.InspectionError = "RunWorkspace path does not resolve to its exact Git worktree"
		return snapshot
	}
	branch, branchErr := runGitCommand(ctx, absolutePath, "branch", "--show-current")
	if branchErr != nil {
		snapshot.WorktreeState = RunWorkspaceWorktreeUnknown
		return snapshot
	}
	snapshot.CurrentBranch = strings.TrimSpace(branch)
	head, headErr := runGitCommand(ctx, absolutePath, "rev-parse", "--verify", "HEAD")
	if headErr == nil {
		snapshot.Head = strings.TrimSpace(head)
		snapshot.HeadKnown = snapshot.Head != "" && snapshot.Head == snapshot.BranchHead
	}
	if snapshot.CurrentBranch != allocation.Branch || !snapshot.HeadKnown {
		snapshot.WorktreeState = RunWorkspaceWorktreeInconsistent
		return snapshot
	}
	status, statusErr := runGitCommandRawOutput(ctx, absolutePath, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if statusErr == nil {
		snapshot.CleanKnown = true
		snapshot.Clean = len(status) == 0
	}
	return snapshot
}

func runWorkspaceRegistrationState(ctx context.Context, repository, path, branch string) (RunWorkspaceWorktreeState, error) {
	listing, err := runGitCommand(ctx, repository, "worktree", "list", "--porcelain")
	if err != nil {
		return RunWorkspaceWorktreeUnknown, err
	}
	wantedPath := filepath.Clean(path)
	wantedBranch := "refs/heads/" + branch
	pathFound := false
	branchFound := false
	for _, block := range strings.Split(listing, "\n\n") {
		var listedPath, listedBranch string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				listedPath = filepath.Clean(strings.TrimSpace(strings.TrimPrefix(line, "worktree ")))
			case strings.HasPrefix(line, "branch "):
				listedBranch = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			}
		}
		if listedPath == wantedPath {
			pathFound = true
			if listedBranch == wantedBranch {
				return RunWorkspaceWorktreeMatched, nil
			}
		}
		if listedBranch == wantedBranch {
			branchFound = true
		}
	}
	if pathFound || branchFound {
		return RunWorkspaceWorktreeInconsistent, nil
	}
	return RunWorkspaceWorktreeAbsent, nil
}

// ReconcileRunWorkspace removes only a Core-approved clean terminal
// allocation. The full allocation and current physical facts are re-read
// under the repository lock immediately before each mutation.
func ReconcileRunWorkspace(ctx context.Context, allocationRunID string, expected RunWorkspaceSnapshot) (RunWorkspaceMutationResult, error) {
	return mutateRunWorkspace(ctx, allocationRunID, expected, false)
}

// AbandonRunWorkspace removes one exact allocation after Botanist confirmation.
// A branch with unique commits, or whose commit state is unknown, remains.
func AbandonRunWorkspace(ctx context.Context, allocationRunID string, expected RunWorkspaceSnapshot, confirm bool) (RunWorkspaceMutationResult, error) {
	if !confirm {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace abandonment requires explicit confirmation")
	}
	return mutateRunWorkspace(ctx, allocationRunID, expected, true)
}

func mutateRunWorkspace(ctx context.Context, allocationRunID string, expected RunWorkspaceSnapshot, abandon bool) (RunWorkspaceMutationResult, error) {
	allocation, err := runWorkspaceAllocationByID(allocationRunID)
	if err != nil {
		return RunWorkspaceMutationResult{}, err
	}
	if !sameRunWorkspaceAllocation(allocation, expected.Allocation) {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace allocation identity changed before mutation")
	}
	unlock := lockRunWorkspaceGit(allocation.Repository)
	defer unlock()
	allocation, err = runWorkspaceAllocationByID(allocationRunID)
	if err != nil {
		return RunWorkspaceMutationResult{}, err
	}
	if !sameRunWorkspaceAllocation(allocation, expected.Allocation) {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace allocation identity changed before mutation")
	}
	current := inspectRunWorkspaceUnlocked(ctx, allocation)
	if !sameRunWorkspaceSnapshot(expected, current) {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace physical state changed after inspection; inspect it again before mutation")
	}
	emptyPendingReservation := abandon && allocation.State == RunWorkspaceAllocationPending &&
		current.Ownership == OwnedRefEvidenceAbsent && current.PathState == RunWorkspacePathMissing &&
		current.WorktreeState == RunWorkspaceWorktreeAbsent && current.BranchKnown && !current.BranchExists
	pendingExactOwner := abandon && allocation.State == RunWorkspaceAllocationPending &&
		current.Ownership == OwnedRefEvidenceMatched
	if (allocation.State != RunWorkspaceAllocationFinalized && !(abandon && allocation.State == RunWorkspaceAllocationPending)) ||
		(current.Ownership != OwnedRefEvidenceMatched && !emptyPendingReservation) ||
		(allocation.State == RunWorkspaceAllocationFinalized && current.OwnershipPending) ||
		!current.PathContained ||
		current.WorktreeState == RunWorkspaceWorktreeInconsistent ||
		current.WorktreeState == RunWorkspaceWorktreeUnknown ||
		!current.BranchKnown {
		return RunWorkspaceMutationResult{}, fmt.Errorf("exact RunWorkspace identity, ownership, containment, branch, and worktree evidence are required")
	}
	if current.PathState == RunWorkspacePathPresent && current.WorktreeState != RunWorkspaceWorktreeMatched {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace path is not the exact registered linked worktree")
	}
	if current.PathState == RunWorkspacePathMissing &&
		(!(allocation.WorkspaceRemoved || allocation.WorkspaceRemovalPending || pendingExactOwner || emptyPendingReservation) ||
			current.WorktreeState != RunWorkspaceWorktreeAbsent) {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace path is missing without a durable completed-removal record")
	}
	if !current.BranchExists && !(abandon && current.PathState == RunWorkspacePathMissing &&
		current.WorktreeState == RunWorkspaceWorktreeAbsent &&
		((allocation.State == RunWorkspaceAllocationPending && (current.Ownership == OwnedRefEvidenceMatched || emptyPendingReservation)) ||
			((allocation.WorkspaceRemoved || allocation.WorkspaceRemovalPending) && current.Ownership == OwnedRefEvidenceMatched))) {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace branch absence is not covered by a confirmed incomplete teardown")
	}
	if !abandon {
		if (current.PathState == RunWorkspacePathPresent && current.WorktreeState != RunWorkspaceWorktreeMatched) ||
			(current.PathState == RunWorkspacePathMissing &&
				(!(allocation.WorkspaceRemoved || allocation.WorkspaceRemovalPending) || current.WorktreeState != RunWorkspaceWorktreeAbsent)) ||
			(current.PathState != RunWorkspacePathPresent && current.PathState != RunWorkspacePathMissing) ||
			!current.CleanKnown || !current.Clean || !current.UniqueCommitsKnown ||
			!current.BaseAncestorKnown || !current.BaseIsAncestor {
			return RunWorkspaceMutationResult{}, fmt.Errorf("clean linked worktree or durable teardown evidence and exact base/commit facts are required for automatic reconciliation")
		}
	}
	if abandon && current.PathState == RunWorkspacePathPresent && !current.CleanKnown {
		return RunWorkspaceMutationResult{}, fmt.Errorf("RunWorkspace cleanliness is unknown; the current workspace cannot be abandoned")
	}

	result := RunWorkspaceMutationResult{}
	if !allocation.WorkspaceRemoved && !allocation.WorkspaceRemovalPending {
		if err := markRunWorkspaceRemovalPending(allocation); err != nil {
			return result, fmt.Errorf("record RunWorkspace teardown intent: %w", err)
		}
		allocation.WorkspaceRemovalPending = true
	}
	if current.PathState == RunWorkspacePathPresent {
		args := []string{"worktree", "remove"}
		if abandon {
			args = append(args, "--force")
		}
		args = append(args, allocation.Path)
		if _, err := runGitCommand(ctx, allocation.Repository, args...); err != nil {
			return result, fmt.Errorf("remove RunWorkspace %q: %w", allocation.Path, err)
		}
		result.WorkspaceRemoved = true
		if err := markRunWorkspaceRemoved(allocation); err != nil {
			return result, fmt.Errorf("RunWorkspace was removed but its durable teardown state could not be recorded: %w", err)
		}
		allocation.WorkspaceRemovalPending = false
		allocation.WorkspaceRemoved = true
	} else {
		result.WorkspaceRemoved = true
		if !allocation.WorkspaceRemoved {
			if err := markRunWorkspaceRemoved(allocation); err != nil {
				return result, fmt.Errorf("RunWorkspace is absent but its durable teardown state could not be recorded: %w", err)
			}
			allocation.WorkspaceRemovalPending = false
			allocation.WorkspaceRemoved = true
		}
	}

	// Re-read exact physical evidence after worktree removal. The branch is
	// deleted only when zero unique commits are positively established.
	current = inspectRunWorkspaceUnlocked(ctx, allocation)
	if current.PathState != RunWorkspacePathMissing || current.WorktreeState != RunWorkspaceWorktreeAbsent ||
		!current.PathContained || (current.Ownership != OwnedRefEvidenceMatched && !emptyPendingReservation) ||
		!current.BranchKnown {
		return result, fmt.Errorf("RunWorkspace teardown completed but branch ownership or absence could not be re-proved")
	}
	if !current.BranchExists {
		if current.Ownership == OwnedRefEvidenceMatched {
			if err := forgetRunWorkspaceOwnedRefForRollback(OwnedRef{
				Repository: allocation.Repository, Branch: allocation.Branch,
				Purpose: PurposeSproutIsolation, Base: allocation.BaseCommit,
				RunID: allocation.AllocationRunID,
			}); err != nil {
				return result, fmt.Errorf("retire ownership for already-absent RunWorkspace branch: %w", err)
			}
		}
		result.BranchReason = "branch was absent after the exact workspace teardown"
	} else if current.UniqueCommitsKnown && current.UniqueCommits == 0 && current.BaseAncestorKnown && current.BaseIsAncestor {
		deleted, deleteErr := deleteRunWorkspaceBranchExact(ctx, allocation, current.BranchHead)
		if deleteErr != nil {
			return result, deleteErr
		}
		result.BranchDeleted = deleted
		result.BranchReason = "owned branch had no commits beyond its recorded base"
	} else {
		result.BranchPreserved = true
		if current.UniqueCommitsKnown && current.UniqueCommits > 0 {
			result.BranchReason = "branch carries committed Fruit and remains available for review"
		} else {
			result.BranchReason = "branch commit state is not positively known; the branch is retained"
		}
	}
	if err := RetireRunWorkspaceAllocation(allocation); err != nil {
		return result, fmt.Errorf("RunWorkspace was removed but its allocation record could not be retired: %w", err)
	}
	return result, nil
}

func deleteRunWorkspaceBranchExact(ctx context.Context, allocation RunWorkspaceAllocation, expectedHead string) (bool, error) {
	if strings.TrimSpace(expectedHead) == "" {
		return false, fmt.Errorf("RunWorkspace branch tip is unavailable")
	}
	owned := ReadOwnedRefEvidence(allocation.Repository, allocation.Branch, allocation.BaseCommit, allocation.AllocationRunID)
	if owned.State != OwnedRefEvidenceMatched {
		return false, fmt.Errorf("exact RunWorkspace ownership changed before branch deletion")
	}
	latestHead, err := runGitCommand(ctx, allocation.Repository, "for-each-ref", "--format=%(objectname)", "refs/heads/"+allocation.Branch)
	if err != nil || strings.TrimSpace(latestHead) != expectedHead {
		return false, fmt.Errorf("RunWorkspace branch tip changed before branch deletion")
	}
	count, err := runGitCommand(ctx, allocation.Repository, "rev-list", "--count", allocation.BaseCommit+"..refs/heads/"+allocation.Branch)
	if err != nil || strings.TrimSpace(count) != "0" {
		return false, fmt.Errorf("RunWorkspace branch gained commits before branch deletion")
	}
	worktreePath, err := runGitCommand(ctx, allocation.Repository, "for-each-ref", "--format=%(worktreepath)", "refs/heads/"+allocation.Branch)
	if err != nil || strings.TrimSpace(worktreePath) != "" {
		return false, fmt.Errorf("RunWorkspace branch is checked out and cannot be deleted")
	}
	if current, err := runGitCommand(ctx, allocation.Repository, "branch", "--show-current"); err != nil || strings.TrimSpace(current) == allocation.Branch {
		return false, fmt.Errorf("RunWorkspace branch is checked out in its repository")
	}
	if _, err := runGitCommand(ctx, allocation.Repository, "update-ref", "-d", "refs/heads/"+allocation.Branch, expectedHead); err != nil {
		return false, fmt.Errorf("delete empty RunWorkspace branch: %w", err)
	}
	expected := OwnedRef{
		Repository: allocation.Repository, Branch: allocation.Branch,
		Purpose: PurposeSproutIsolation, Base: allocation.BaseCommit,
		RunID: allocation.AllocationRunID,
	}
	var retirementErr error
	if owned.Pending {
		retirementErr = forgetRunWorkspaceOwnedRefForRollback(expected)
	} else {
		retirementErr = forgetRunWorkspaceOwnedRefExact(expected)
	}
	if retirementErr != nil {
		zeroOID := strings.Repeat("0", len(expectedHead))
		_, restoreErr := runGitCommand(ctx, allocation.Repository, "update-ref", "refs/heads/"+allocation.Branch, expectedHead, zeroOID)
		if restoreErr != nil {
			return false, fmt.Errorf("branch was deleted but exact ownership retirement failed: %v; branch restoration also failed: %w", retirementErr, restoreErr)
		}
		return false, fmt.Errorf("branch ownership retirement failed; the branch was restored: %w", retirementErr)
	}
	return true, nil
}

func sameRunWorkspaceSnapshot(first, second RunWorkspaceSnapshot) bool {
	return sameRunWorkspaceAllocation(first.Allocation, second.Allocation) &&
		first.Allocation.WorkspaceRemovalPending == second.Allocation.WorkspaceRemovalPending &&
		first.Allocation.WorkspaceRemoved == second.Allocation.WorkspaceRemoved &&
		first.Ownership == second.Ownership &&
		first.OwnershipPending == second.OwnershipPending &&
		first.PathState == second.PathState &&
		first.PathContained == second.PathContained &&
		first.WorktreeState == second.WorktreeState &&
		first.CurrentBranch == second.CurrentBranch &&
		first.Head == second.Head &&
		first.HeadKnown == second.HeadKnown &&
		first.BranchKnown == second.BranchKnown &&
		first.BranchExists == second.BranchExists &&
		first.BranchHead == second.BranchHead &&
		first.UniqueCommitsKnown == second.UniqueCommitsKnown &&
		first.UniqueCommits == second.UniqueCommits &&
		first.BaseAncestorKnown == second.BaseAncestorKnown &&
		first.BaseIsAncestor == second.BaseIsAncestor &&
		first.CleanKnown == second.CleanKnown &&
		first.Clean == second.Clean
}
