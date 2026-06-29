# Conductor Parallel Sequence Steps: Speculative Multi-Model Orchestration (Issue #11)

This document details the design and implementation of **Parallel Sequence Step Execution** (Conductor mode) inside the Go Stem orchestrator. This allows Go Stem to execute independent steps concurrently in isolated shadow worktrees, preventing shared-state conflicts.

---

## 1. Botanical Metaphor: Multiple Growing Shoots

In plant biology, a vine dispatches multiple parallel shoots or growing tips in search of support structures. They grow independently in space, and nutrients are coordinated centrally.

In OpenTendril, when a Sequence specifies a `concurrencyLimit > 1`, the Conductor dispatches independent steps (those whose `dependsOn` lists contain only completed steps) in parallel. Each step executes in its own isolated shadow worktree, preventing race conditions or file-writing conflicts on the developer's host substrate.

---

## 2. Proposed Architecture

```
                 ┌─────────────────────────────────┐
                 │        Activate Meristem        │
                 └────────────────┬────────────────┘
                                  │
                    ┌─────────────┴─────────────┐
                    ▼                           ▼
            (Shadow Worktree)           (Shadow Worktree)
             ┌─────────────┐             ┌─────────────┐
             │ Step A (run)│             │ Step B (run)│
             └──────┬──────┘             └──────┬──────┘
                    │                           │
                    ▼ (standard merge)          ▼ (standard merge)
             ┌─────────────┐             ┌─────────────┐
             │ Merge host  │             │ Merge host  │
             └─────────────┘             └─────────────┘
```

### A. Isolated Shadow Worktree Executions
*   For each concurrent step, Go Stem checks out a temporary shadow worktree at a unique branch name: `derivedSequenceBranch(seq.Branch, step.ID)`.
*   The orchestrator is initialized with `DisableMergeBack: true` so it edits only its isolated shadow workspace.
*   Once execution finishes, the branch is merged back to the host.

### B. Parallel Step Merging & Conflict Gating
*   When a concurrent step completes successfully, its shadow branch is merged back to the host branch using `git merge --no-ff -m "merge message" branchName`.
*   Because other parallel steps might have already merged their branches, a standard `--no-ff` merge is used to let Git auto-merge non-conflicting changes.
*   If a merge conflict occurs, the merge is aborted via `git merge --abort` and the step returns a conflict compilation error, triggering the normal debugger/retry/pause logic.

---

## 3. Proposed Changes

### Component: Go Stem Orchestrator

#### [MODIFY] [orchestrator/sequence.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/sequence.go)
*   Add `DependsOnLegacy []string `yaml:"depends_on,omitempty"`` to `SequenceStep` struct to support both `dependsOn` and `depends_on` syntax formats.
*   In `normalizeSequence`, copy `DependsOnLegacy` to `DependsOn` if the latter is empty.
*   In `defaultSequenceStepRunner`, check if `seq.ConcurrencyLimit > 1`. If so, delegate to `runParallelSequenceStep`.
*   Implement `runParallelSequenceStep`:
    *   Creates a shadow worktree and injects the mycorrhizal cache.
    *   Runs the sprout with `DisableMergeBack: true`.
    *   Invokes `mergeConductorBranchToHostFn` to merge the branch back.
*   Implement `mergeConductorBranchToHost` using `git merge --no-ff`, aborting via `git merge --abort` if it fails.

---

## 4. Verification Plan

### Automated Tests
*   **TestConductorStepParsing:** Asserts both `dependsOn` and `depends_on` parse correctly and normalize to `DependsOn`.
*   **TestConductorParallelStepExecution:** Mocks parallel steps running concurrently, verifying branch names are resolved correctly and shadow worktrees are created/cleaned up.
*   **TestConductorMergeConflictHandling:** Mocks a failed standard merge, verifying the merge is aborted and returns a clean error.

### Manual Verification
1.  Define a sequence with `concurrencyLimit: 2` and two independent steps (no `depends_on`).
2.  Verify that both steps launch concurrently in separate sandboxes.
3.  Verify that both branches are merged cleanly back to the main branch upon completion.
