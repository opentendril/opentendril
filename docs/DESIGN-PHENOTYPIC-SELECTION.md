# Phenotypic Selection: Speculative Parallel Sprouting & Fitness Testing (Issue)

This plan details the implementation of **Phenotypic Selection** (Natural Selection) in the Go Stem orchestrator. This allows Go Stem to execute concurrent sprouts (phenotypes) tackling the same task under different parameters, and merge only the first candidate that passes compilation and unit testing.

---

## 1. Botanical Metaphor: Natural Selection & Phenotypes

In botany, a plant generates multiple shoots or variations (phenotypes) in response to the environmental conditions. Natural Selection filters these variations, ensuring only the ones with the highest environmental fitness survive and propagate.

In OpenTendril, the **compiler, test suite, and linter** act as the objective environmental constraints. Rather than hoping a single LLM output is correct, Go Stem dispatches multiple parallel sprouts in isolated terrariumes (varying their temperature settings to generate diverse code solutions) and selects the "fittest" candidate that successfully satisfies all test validation suites.

---

## 2. Proposed Architecture

```
                  ┌───────────────────────────────┐
                  │     1. Activate Meristem      │
                  └──────────────┬────────────────┘
                                 │
                   ┌─────────────┴─────────────┐
                   ▼ (Temp: 0.1)               ▼ (Temp: 0.7)
             ┌───────────┐               ┌───────────┐
             │ Phenotype │               │ Phenotype │
             │ Sprout A  │               │ Sprout B  │
             └─────┬─────┘               └─────┬─────┘
                   │                           │
                   ▼ (run verification)        ▼ (run verification)
             ┌───────────┐               ┌───────────┐
             │ Fitness?  │               │ Fitness?  │
             └─────┬─────┘               └─────┬─────┘
                   │ (First success)           │ (Tear down / cancel)
                   ▼                           ▼
            ┌─────────────┐             ┌─────────────┐
            │   Winner!   │             │   Discard   │
            │ Merge host  │             │  Worktree   │
            └─────────────┘             └─────────────┘
```

### A. Speculative Dispatch & Branch Isolation
*   If a sequence step declares `phenotypesCount: N` (where $N > 1$):
    *   Go Stem creates $N$ isolated branch names: `step-branch-phenotype-i` (e.g. `feature/issue-77-step-phenotype-0`).
    *   For each phenotype, Go Stem instantiates a `DockerOrchestrator` with `DisableMergeBack: true` so they execute in parallel without writing to or corrupting the host git workspace.
    *   We scale LLM client temperature dynamically per phenotype index:
        `temp := 0.1 + float64(i)*0.3` (providing a spread from `0.1` to `0.7`).

### B. Environment Fitness Test
*   After the Sprout agent run completes, if a `fitnessTest` command (e.g. `go test ./...` or `npm run lint`) is defined:
    *   Go Stem executes this command inside a sterile container mounting the phenotype's terrarium directory:
        `docker run --rm -v terrarium:/app -w /app imageName sh -c "fitnessTest"`
    *   A phenotype is declared **fit** if and only if both the agent edit loop and the fitness test command exit with code `0`.

### C. Selection & Merge
*   Go Stem runs the parallel sprouts concurrently using a Go goroutine runner.
*   The **first** phenotype to complete with `err == nil` is selected as the winner.
*   **Context Cancellation:** Upon declaring a winner, Go Stem immediately triggers context cancellation to terminate all other active phenotype containers and delete their temporary worktrees.
*   The winner's branch is merged back to the host substrate branch via a non-fast-forward merge, and the host stash is popped.

---

## 3. Proposed Changes

### Component: Go Stem Orchestrator

#### [MODIFY] [orchestrator/sequence.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/sequence.go)
*   Add fields `PhenotypesCount int` and `FitnessTest string` to `SequenceStep` struct.
*   Update `normalizeSequence` to validate that `PhenotypesCount` defaults to `1` if empty.
*   In `defaultSequenceStepRunner`, check if `step.PhenotypesCount > 1`.
    *   If so, dispatch `runPhenotypicSelection(ctx, seq, step, substratePath)`:
        *   Stash the host workspace (if dirty).
        *   Launch $N$ goroutines running `runSequenceSprout` in parallel with `DisableMergeBack: true` and unique branch names.
        *   Vary the temperature of the LLM client: `0.1 + float64(i) * 0.3`.
        *   Wait on a results channel. Once the first `err == nil` result arrives:
            *   Cancel the remaining runs.
            *   Merge the winning branch back to the host.
            *   Pop the host stash.
            *   Return success.
        *   If all runs fail, return the error of the first or last failed phenotype.

#### [MODIFY] [orchestrator/docker.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/docker.go)
*   Add fields `DisableMergeBack bool` and `Temperature float64` to `DockerOrchestrator`.
*   Update `resolveLLMClient()` to set the temperature on the LLM client if `orch.Temperature > 0`.
*   Inside `RunTendril`:
    *   If `d.DisableMergeBack` is true, skip stashing the host workspace at start.
    *   In the post-flight section, if `d.DisableMergeBack` is true, skip host merging and stash popping, but still run `commitTerrariumExecution` and return the commit hash.
*   Implement `SetTemperature(temp float64)` in `llm.Client`.

---

## 4. Open Questions

> [!IMPORTANT]
> 1.  **Concurrent LLM Rate Limits:** If we run 3 or 4 sprouts in parallel, we will hit LLM provider rate limits (tokens per minute / requests per minute) much faster. Should we add a slight staggered delay (e.g. 5 seconds) between launching phenotypes, or should we assume the user has configured adequate rate limits?
> 2.  **Headless vs Interactive:** When running in interactive mode (`tendril chat`), should we display output streams for all running phenotypes simultaneously (e.g. prefixing logs with `[Phenotype-0]`, `[Phenotype-1]`), or only output the winning sprout's final summary?

---

## 5. Verification Plan

### Automated Tests
*   **Speculative Concurrency test:** Verify that launching a step with 2 phenotypes dispatches parallel goroutines, and cancelling the parent context halts the loser.
*   **Fitness Test Command test:** Mock a fitness test run that succeeds for one phenotype and fails for another, asserting the correct winner is selected.

### Manual Verification
1.  Define a sequence step with `phenotypesCount: 3` and `fitnessTest: "go test ./..."`.
2.  Trigger `tendril sequence run`.
3.  Confirm from Docker commands (`docker ps`) that multiple sprouts run concurrently and terminate immediately once a winner compiles and passes the tests.
