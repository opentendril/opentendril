package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	RunWorkspaceHistoryPresent     = "present"
	RunWorkspaceHistoryMissing     = "missing"
	RunWorkspaceHistoryUnavailable = "unavailable"
	RunWorkspaceHistoryUnlinked    = "unlinked"
	RunWorkspaceHistoryMalformed   = "malformed"

	RunWorkspaceOwnershipMatched       = "matched"
	RunWorkspaceOwnershipAbsent        = "absent"
	RunWorkspaceOwnershipMissing       = "missing-registry"
	RunWorkspaceOwnershipUnreadable    = "unreadable-registry"
	RunWorkspaceOwnershipMalformed     = "malformed-registry"
	RunWorkspaceOwnershipContradictory = "contradictory"

	RunWorkspacePathPresent = "present"
	RunWorkspacePathMissing = "missing"
	RunWorkspacePathInvalid = "invalid"
	RunWorkspacePathUnknown = "unknown"

	RunWorkspaceWorktreeMatched      = "matched"
	RunWorkspaceWorktreeAbsent       = "absent"
	RunWorkspaceWorktreeInconsistent = "inconsistent"
	RunWorkspaceWorktreeUnknown      = "unknown"

	// RunWorkspaceExecutionComplete is the only recovery checkpoint state.
	// It is not a Sprout outcome and not a HistoryDB status.
	RunWorkspaceExecutionComplete = "execution-complete"
)

// RunWorkspaceExecutionCheckpoint is the allocation-ledger fact that execution
// and Fruit settlement finished for one exact identity. It carries no outcome,
// history status, or cleanliness claim.
type RunWorkspaceExecutionCheckpoint struct {
	State           string `json:"state,omitempty"`
	AllocationRunID string `json:"allocationRunId,omitempty"`
	SproutRunID     string `json:"sproutRunId,omitempty"`
}

// RunWorkspaceAllocation is a transport-free copy of the Conductor's durable
// allocation identity. AllocationRunID and SproutRunID are distinct fields.
type RunWorkspaceAllocation struct {
	AllocationRunID         string                           `json:"allocationRunId,omitempty"`
	SproutRunID             string                           `json:"sproutRunId,omitempty"`
	StepID                  string                           `json:"stepId,omitempty"`
	Repository              string                           `json:"repository"`
	Path                    string                           `json:"path,omitempty"`
	Branch                  string                           `json:"branch"`
	BaseCommit              string                           `json:"baseCommit,omitempty"`
	Substrate               string                           `json:"substrate,omitempty"`
	PhytomerID              string                           `json:"phytomerId,omitempty"`
	Pollen                  string                           `json:"pollen,omitempty"`
	CreatedAt               time.Time                        `json:"createdAt,omitempty"`
	State                   string                           `json:"state"`
	WorkspaceRemovalPending bool                             `json:"workspaceRemovalPending,omitempty"`
	WorkspaceRemoved        bool                             `json:"workspaceRemoved,omitempty"`
	Historical              bool                             `json:"historical,omitempty"`
	OwnedRefRunID           string                           `json:"ownedRefRunId,omitempty"`
	ExecutionCheckpoint     *RunWorkspaceExecutionCheckpoint `json:"executionCheckpoint,omitempty"`
}

// RunWorkspaceHistoryEvidence is the exact HistoryDB row selected by the
// allocation's recorded SproutRunID. State distinguishes missing evidence from
// unavailable persistence.
type RunWorkspaceHistoryEvidence struct {
	State                 string
	RunID                 string
	SessionID             string
	StepID                string
	Pollen                string
	Substrate             string
	Status                string
	FruitRepository       string
	FruitBranch           string
	FruitCommit           string
	FruitPublicationState string
	FruitCreatedAt        time.Time
	Reason                string
}

// RunWorkspaceEvidence combines durable identity with physical facts. The
// adapter gathers facts; Core alone classifies them and authorizes teardown.
type RunWorkspaceEvidence struct {
	Allocation         RunWorkspaceAllocation
	History            RunWorkspaceHistoryEvidence
	OwnershipState     string
	OwnershipPending   bool
	OwnershipError     string
	PathState          string
	PathContained      bool
	WorktreeState      string
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

// RunWorkspaceMutation reports physical results after Core has authorized one
// exact allocation.
type RunWorkspaceMutation struct {
	WorkspaceRemoved bool
	BranchDeleted    bool
	BranchPreserved  bool
	BranchReason     string
}

// RunWorkspaceReport is the Botanist-facing classification and its exact
// evidence. AutoReconcileable concerns only the disposable workspace; Fruit
// branches remain independently reviewable.
type RunWorkspaceReport struct {
	AllocationRunID         string    `json:"allocationRunId,omitempty"`
	SproutRunID             string    `json:"sproutRunId,omitempty"`
	StepID                  string    `json:"stepId,omitempty"`
	Repository              string    `json:"repository"`
	Path                    string    `json:"path,omitempty"`
	Branch                  string    `json:"branch"`
	BaseCommit              string    `json:"baseCommit,omitempty"`
	Substrate               string    `json:"substrate,omitempty"`
	PhytomerID              string    `json:"phytomerId,omitempty"`
	Pollen                  string    `json:"pollen,omitempty"`
	CreatedAt               time.Time `json:"createdAt,omitempty"`
	AllocationState         string    `json:"allocationState"`
	WorkspaceRemovalPending bool      `json:"workspaceRemovalPending,omitempty"`
	WorkspaceRemoved        bool      `json:"workspaceRemoved,omitempty"`
	Historical              bool      `json:"historical,omitempty"`
	OwnedRefRunID           string    `json:"ownedRefRunId,omitempty"`
	HistoryState            string    `json:"historyState"`
	SproutStatus            string    `json:"sproutStatus,omitempty"`
	OwnershipState          string    `json:"ownershipState"`
	OwnershipPending        bool      `json:"ownershipPending"`
	PathState               string    `json:"pathState"`
	PathContained           bool      `json:"pathContained"`
	WorktreeState           string    `json:"worktreeState"`
	CurrentBranch           string    `json:"currentBranch,omitempty"`
	Head                    string    `json:"head,omitempty"`
	HeadKnown               bool      `json:"headKnown"`
	BranchKnown             bool      `json:"branchKnown"`
	BranchExists            bool      `json:"branchExists"`
	BranchHead              string    `json:"branchHead,omitempty"`
	Clean                   bool      `json:"clean"`
	CleanKnown              bool      `json:"cleanKnown"`
	UniqueCommits           int       `json:"uniqueCommits"`
	UniqueCommitsKnown      bool      `json:"uniqueCommitsKnown"`
	FruitRepository         string    `json:"fruitRepository,omitempty"`
	FruitBranch             string    `json:"fruitBranch,omitempty"`
	FruitCommit             string    `json:"fruitCommit,omitempty"`
	FruitPublicationState   string    `json:"fruitPublicationState,omitempty"`
	FruitCreatedAt          time.Time `json:"fruitCreatedAt,omitempty"`
	AutoReconcileable       bool      `json:"autoReconcileable"`
	Reason                  string    `json:"reason"`
}

type RunWorkspaceInput struct {
	AllocationRunID string `json:"allocationRunId"`
}

type RunWorkspaceAbandonInput struct {
	AllocationRunID string `json:"allocationRunId"`
	Confirm         bool   `json:"confirm"`
}

type RunWorkspaceResult struct {
	Report           RunWorkspaceReport `json:"report"`
	WorkspaceRemoved bool               `json:"workspaceRemoved"`
	BranchDeleted    bool               `json:"branchDeleted"`
	BranchPreserved  bool               `json:"branchPreserved"`
	BranchReason     string             `json:"branchReason,omitempty"`
}

// RunWorkspaceOperations is a transport-free local control-plane port. It is
// intentionally absent from CapabilityNames and Pollinator-facing transports.
type RunWorkspaceOperations struct {
	ListAllocations func(ctx context.Context) ([]RunWorkspaceAllocation, error)
	Inspect         func(ctx context.Context, allocation RunWorkspaceAllocation) (RunWorkspaceEvidence, error)
	Reconcile       func(ctx context.Context, evidence RunWorkspaceEvidence) (RunWorkspaceMutation, error)
	Abandon         func(ctx context.Context, evidence RunWorkspaceEvidence, confirm bool) (RunWorkspaceMutation, error)
}

func (s *Service) WithRunWorkspace(operations RunWorkspaceOperations) *Service {
	s.runWorkspace = operations
	return s
}

func (s *Service) ListRunWorkspaces(ctx context.Context) ([]RunWorkspaceReport, error) {
	if PollenFromContext(ctx) != "" {
		return nil, fmt.Errorf("RunWorkspace listing is Botanist-only")
	}
	allocations, err := s.listRunWorkspaceAllocations(ctx)
	if err != nil {
		return nil, err
	}
	reports := make([]RunWorkspaceReport, 0, len(allocations))
	for _, allocation := range allocations {
		evidence, inspectErr := s.inspectRunWorkspace(ctx, allocation)
		if inspectErr != nil {
			evidence = RunWorkspaceEvidence{
				Allocation: allocation, InspectionError: inspectErr.Error(),
				OwnershipState: RunWorkspaceOwnershipUnreadable,
				PathState:      RunWorkspacePathUnknown,
				WorktreeState:  RunWorkspaceWorktreeUnknown,
			}
		}
		reports = append(reports, ClassifyRunWorkspace(evidence))
	}
	sort.Slice(reports, func(i, j int) bool {
		if !reports[i].CreatedAt.Equal(reports[j].CreatedAt) {
			return reports[i].CreatedAt.Before(reports[j].CreatedAt)
		}
		return reports[i].AllocationRunID < reports[j].AllocationRunID
	})
	return reports, nil
}

func (s *Service) InspectRunWorkspace(ctx context.Context, input RunWorkspaceInput) (RunWorkspaceReport, error) {
	if PollenFromContext(ctx) != "" {
		return RunWorkspaceReport{}, fmt.Errorf("RunWorkspace inspection is Botanist-only")
	}
	evidence, err := s.findAndInspectRunWorkspace(ctx, input.AllocationRunID)
	if err != nil {
		return RunWorkspaceReport{}, err
	}
	return ClassifyRunWorkspace(evidence), nil
}

func (s *Service) ReconcileRunWorkspace(ctx context.Context, input RunWorkspaceInput) (RunWorkspaceResult, error) {
	if PollenFromContext(ctx) != "" {
		return RunWorkspaceResult{}, fmt.Errorf("RunWorkspace reconciliation is Botanist-only")
	}
	evidence, err := s.findAndInspectRunWorkspace(ctx, input.AllocationRunID)
	if err != nil {
		return RunWorkspaceResult{}, err
	}
	report := ClassifyRunWorkspace(evidence)
	result := RunWorkspaceResult{Report: report}
	if !report.AutoReconcileable {
		return result, nil
	}
	if s.runWorkspace.Reconcile == nil {
		return RunWorkspaceResult{}, fmt.Errorf("RunWorkspace reconciliation is not wired")
	}
	mutation, err := s.runWorkspace.Reconcile(ctx, evidence)
	if err != nil {
		return result, err
	}
	result.WorkspaceRemoved = mutation.WorkspaceRemoved
	result.BranchDeleted = mutation.BranchDeleted
	result.BranchPreserved = mutation.BranchPreserved
	result.BranchReason = mutation.BranchReason
	return result, nil
}

func (s *Service) AbandonRunWorkspace(ctx context.Context, input RunWorkspaceAbandonInput) (RunWorkspaceResult, error) {
	if PollenFromContext(ctx) != "" {
		return RunWorkspaceResult{}, fmt.Errorf("RunWorkspace abandonment is Botanist-only")
	}
	if !input.Confirm {
		return RunWorkspaceResult{}, fmt.Errorf("RunWorkspace abandonment requires --confirm")
	}
	evidence, err := s.findAndInspectRunWorkspace(ctx, input.AllocationRunID)
	if err != nil {
		return RunWorkspaceResult{}, err
	}
	report := ClassifyRunWorkspace(evidence)
	allocationPending := evidence.Allocation.State == "pending"
	if evidence.Allocation.Historical || (!allocationPending && evidence.Allocation.State != "finalized") ||
		strings.TrimSpace(evidence.Allocation.AllocationRunID) == "" {
		return RunWorkspaceResult{}, fmt.Errorf("an exact current allocation identity is required for abandonment")
	}
	if report.HistoryState == "contradictory" {
		return RunWorkspaceResult{}, fmt.Errorf("contradictory Sprout history relation prevents RunWorkspace abandonment")
	}
	emptyPendingReservation := allocationPending && evidence.OwnershipState == RunWorkspaceOwnershipAbsent &&
		evidence.PathState == RunWorkspacePathMissing && evidence.WorktreeState == RunWorkspaceWorktreeAbsent &&
		evidence.BranchKnown && !evidence.BranchExists
	allowMissingPath := evidence.Allocation.WorkspaceRemoved || evidence.Allocation.WorkspaceRemovalPending ||
		(allocationPending && evidence.OwnershipState == RunWorkspaceOwnershipMatched) || emptyPendingReservation
	ownershipValid := evidence.OwnershipState == RunWorkspaceOwnershipMatched && (allocationPending || !evidence.OwnershipPending)
	allowMissingBranch := emptyPendingReservation ||
		((evidence.Allocation.WorkspaceRemoved || evidence.Allocation.WorkspaceRemovalPending) && ownershipValid)
	if (!ownershipValid && !emptyPendingReservation) ||
		!evidence.PathContained ||
		(evidence.PathState != RunWorkspacePathPresent && evidence.PathState != RunWorkspacePathMissing) ||
		(evidence.WorktreeState != RunWorkspaceWorktreeMatched && evidence.WorktreeState != RunWorkspaceWorktreeAbsent) ||
		!evidence.BranchKnown ||
		(!evidence.BranchExists && !allowMissingBranch && !(allocationPending && evidence.OwnershipState == RunWorkspaceOwnershipMatched &&
			evidence.PathState == RunWorkspacePathMissing && evidence.WorktreeState == RunWorkspaceWorktreeAbsent)) ||
		(evidence.PathState == RunWorkspacePathPresent && (!evidence.HeadKnown || evidence.CurrentBranch != evidence.Allocation.Branch)) ||
		(evidence.PathState == RunWorkspacePathMissing && (!allowMissingPath || evidence.WorktreeState != RunWorkspaceWorktreeAbsent)) {
		return RunWorkspaceResult{}, fmt.Errorf("exact allocation, ownership, containment, branch, and worktree evidence are required for abandonment")
	}
	if s.runWorkspace.Abandon == nil {
		return RunWorkspaceResult{}, fmt.Errorf("RunWorkspace abandonment is not wired")
	}
	mutation, err := s.runWorkspace.Abandon(ctx, evidence, input.Confirm)
	if err != nil {
		return RunWorkspaceResult{Report: report}, err
	}
	return RunWorkspaceResult{
		Report: report, WorkspaceRemoved: mutation.WorkspaceRemoved,
		BranchDeleted: mutation.BranchDeleted, BranchPreserved: mutation.BranchPreserved,
		BranchReason: mutation.BranchReason,
	}, nil
}

func (s *Service) listRunWorkspaceAllocations(ctx context.Context) ([]RunWorkspaceAllocation, error) {
	if s.runWorkspace.ListAllocations == nil {
		return nil, fmt.Errorf("RunWorkspace listing is not wired")
	}
	return s.runWorkspace.ListAllocations(ctx)
}

func (s *Service) findAndInspectRunWorkspace(ctx context.Context, allocationRunID string) (RunWorkspaceEvidence, error) {
	allocationRunID = strings.TrimSpace(allocationRunID)
	if allocationRunID == "" || allocationRunID != strings.TrimSpace(allocationRunID) {
		return RunWorkspaceEvidence{}, fmt.Errorf("exact allocation RunID is required")
	}
	allocations, err := s.listRunWorkspaceAllocations(ctx)
	if err != nil {
		return RunWorkspaceEvidence{}, err
	}
	var match *RunWorkspaceAllocation
	for index := range allocations {
		if allocations[index].AllocationRunID != allocationRunID {
			continue
		}
		if match != nil {
			return RunWorkspaceEvidence{}, fmt.Errorf("allocation RunID %q is ambiguous", allocationRunID)
		}
		copy := allocations[index]
		match = &copy
	}
	if match == nil {
		return RunWorkspaceEvidence{}, fmt.Errorf("RunWorkspace allocation %q was not found", allocationRunID)
	}
	return s.inspectRunWorkspace(ctx, *match)
}

func (s *Service) inspectRunWorkspace(ctx context.Context, allocation RunWorkspaceAllocation) (RunWorkspaceEvidence, error) {
	if s.runWorkspace.Inspect == nil {
		return RunWorkspaceEvidence{}, fmt.Errorf("RunWorkspace inspection is not wired")
	}
	return s.runWorkspace.Inspect(ctx, allocation)
}

// ClassifyRunWorkspace is the deterministic lifecycle policy shared by
// startup reconciliation and Botanist commands. Automatic recovery requires
// an execution-complete checkpoint plus positively proven ownership,
// containment, worktree, base, and cleanliness evidence. History that is
// still running, missing, or unavailable does not supply or block that proof.
func ClassifyRunWorkspace(evidence RunWorkspaceEvidence) RunWorkspaceReport {
	a := evidence.Allocation
	r := RunWorkspaceReport{
		AllocationRunID: a.AllocationRunID, SproutRunID: a.SproutRunID,
		StepID: a.StepID, Repository: a.Repository, Path: a.Path, Branch: a.Branch,
		BaseCommit: a.BaseCommit, Substrate: a.Substrate, PhytomerID: a.PhytomerID,
		Pollen: a.Pollen, CreatedAt: a.CreatedAt, AllocationState: a.State,
		WorkspaceRemovalPending: a.WorkspaceRemovalPending,
		WorkspaceRemoved:        a.WorkspaceRemoved, Historical: a.Historical,
		OwnedRefRunID: a.OwnedRefRunID, HistoryState: evidence.History.State,
		SproutStatus: evidence.History.Status, OwnershipState: evidence.OwnershipState,
		OwnershipPending: evidence.OwnershipPending,
		PathState:        evidence.PathState, PathContained: evidence.PathContained,
		WorktreeState: evidence.WorktreeState, CurrentBranch: evidence.CurrentBranch,
		Head: evidence.Head, HeadKnown: evidence.HeadKnown, BranchKnown: evidence.BranchKnown,
		BranchExists: evidence.BranchExists, BranchHead: evidence.BranchHead,
		Clean: evidence.Clean, CleanKnown: evidence.CleanKnown,
		UniqueCommits: evidence.UniqueCommits, UniqueCommitsKnown: evidence.UniqueCommitsKnown,
		FruitRepository:       evidence.History.FruitRepository,
		FruitBranch:           evidence.History.FruitBranch,
		FruitCommit:           evidence.History.FruitCommit,
		FruitPublicationState: evidence.History.FruitPublicationState,
		FruitCreatedAt:        evidence.History.FruitCreatedAt,
	}
	if a.Historical || strings.TrimSpace(a.AllocationRunID) == "" {
		r.Reason = "historical Sprout isolation state has no durable allocation relation; it is retained"
		return r
	}
	if a.State != "finalized" {
		r.Reason = "RunWorkspace allocation is pending and cannot be reconciled automatically"
		return r
	}
	if strings.TrimSpace(a.SproutRunID) == "" {
		r.HistoryState = RunWorkspaceHistoryUnlinked
		r.Reason = "allocation has no explicit Sprout history RunID; lifecycle state is unknown and it is retained"
		return r
	}
	if reason, ok := runWorkspaceExecutionCheckpointReason(a); !ok {
		r.Reason = reason
		return r
	}
	switch evidence.History.State {
	case RunWorkspaceHistoryPresent:
		if !runWorkspaceHistoryMatchesAllocation(a, evidence.History) {
			r.HistoryState = "contradictory"
			r.Reason = "Sprout history contradicts the durable allocation relation; it is retained"
			return r
		}
		r.HistoryState = "matched"
	case RunWorkspaceHistoryMalformed:
		r.Reason = "the explicitly linked Sprout history evidence is malformed; it is retained"
		return r
	}
	if evidence.OwnershipState != RunWorkspaceOwnershipMatched {
		switch evidence.OwnershipState {
		case RunWorkspaceOwnershipAbsent:
			r.Reason = "valid owned-reference state has no exact allocation match; destructive recovery is refused"
		case RunWorkspaceOwnershipMissing:
			r.Reason = "owned-reference registry is missing; destructive recovery is refused"
		case RunWorkspaceOwnershipUnreadable:
			r.Reason = "owned-reference registry is unreadable; destructive recovery is refused"
		case RunWorkspaceOwnershipMalformed:
			r.Reason = "owned-reference registry is malformed; destructive recovery is refused"
		default:
			r.Reason = "owned-reference evidence is contradictory; destructive recovery is refused"
		}
		return r
	}
	if evidence.OwnershipPending {
		r.Reason = "owned-reference allocation is still pending and is not automatically reconciled"
		return r
	}
	if evidence.InspectionError != "" {
		r.Reason = evidence.InspectionError + "; the RunWorkspace is retained"
		return r
	}
	if !evidence.PathContained {
		r.Reason = "RunWorkspace path containment is not positively established; it is retained"
		return r
	}
	if evidence.PathState == RunWorkspacePathMissing {
		if !(a.WorkspaceRemoved || a.WorkspaceRemovalPending) || evidence.WorktreeState != RunWorkspaceWorktreeAbsent {
			r.Reason = "RunWorkspace path is missing without a durable teardown record; cleanliness is unknown and it is retained"
			return r
		}
	} else if evidence.PathState != RunWorkspacePathPresent {
		r.Reason = "RunWorkspace path state is unknown or invalid; it is retained"
		return r
	}
	if evidence.WorktreeState != RunWorkspaceWorktreeMatched &&
		!((a.WorkspaceRemoved || a.WorkspaceRemovalPending) && evidence.WorktreeState == RunWorkspaceWorktreeAbsent) {
		r.Reason = "linked-worktree identity is absent or inconsistent; it is retained"
		return r
	}
	if !evidence.BranchKnown || !evidence.BranchExists ||
		(evidence.PathState == RunWorkspacePathPresent && (!evidence.HeadKnown ||
			evidence.CurrentBranch != a.Branch || evidence.Head != evidence.BranchHead)) {
		r.Reason = "branch and linked-worktree identity could not be established exactly; it is retained"
		return r
	}
	if !evidence.CleanKnown || !evidence.Clean {
		if evidence.CleanKnown {
			r.Reason = "RunWorkspace contains uncommitted, untracked, ignored, or submodule changes; it is retained"
		} else {
			r.Reason = "RunWorkspace cleanliness is unknown; it is retained"
		}
		return r
	}
	if !evidence.BaseAncestorKnown || !evidence.BaseIsAncestor || !evidence.UniqueCommitsKnown {
		r.Reason = "base and unique-commit state cannot be proven; the RunWorkspace is retained"
		return r
	}
	if evidence.History.FruitBranch == a.Branch && evidence.History.FruitCommit != "" &&
		evidence.UniqueCommits == 0 {
		r.Reason = "HistoryDB records Fruit on this branch but Git shows no unique commit; contradictory Fruit evidence is retained"
		return r
	}
	r.AutoReconcileable = true
	if evidence.UniqueCommits == 0 {
		r.Reason = "RunWorkspace is clean and terminal, and its owned branch has no commits beyond the recorded base"
	} else {
		r.Reason = "RunWorkspace is clean and terminal; the worktree may be removed while its committed Fruit branch is preserved"
	}
	return r
}

func runWorkspaceExecutionCheckpointReason(allocation RunWorkspaceAllocation) (string, bool) {
	checkpoint := allocation.ExecutionCheckpoint
	if checkpoint == nil {
		return "execution-complete checkpoint is absent; automatic recovery is refused", false
	}
	if checkpoint.State != RunWorkspaceExecutionComplete ||
		checkpoint.AllocationRunID == "" || checkpoint.SproutRunID == "" {
		return "execution-complete checkpoint is incomplete; automatic recovery is refused", false
	}
	if checkpoint.AllocationRunID != allocation.AllocationRunID || checkpoint.SproutRunID != allocation.SproutRunID {
		return "execution-complete checkpoint contradicts the allocation identity; it is retained", false
	}
	return "", true
}

func runWorkspaceHistoryMatchesAllocation(allocation RunWorkspaceAllocation, history RunWorkspaceHistoryEvidence) bool {
	if history.RunID != allocation.SproutRunID || history.StepID != allocation.StepID {
		return false
	}
	if allocation.PhytomerID != "" && history.SessionID != allocation.PhytomerID {
		return false
	}
	if allocation.Substrate != "" && history.Substrate != allocation.Substrate {
		return false
	}
	if allocation.Pollen != "" && history.Pollen != allocation.Pollen {
		return false
	}
	return true
}
