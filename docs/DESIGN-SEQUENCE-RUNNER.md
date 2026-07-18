# Vascular Cambium: Parallel Workflows & Failure recovery (Issue)

This plan details the implementation of the **Vascular Cambium** (Sequence Runner) in the Go Stem orchestrator. This allows developers and IDE agents to write structured, parallel-capable task graphs to `.tendril/sequences/<name>.yaml` and run them concurrently using Go goroutines, with advanced failure recovery options.

---

## 1. Architectural Flow & Parallel Concurrency

Instead of running steps strictly in order, the Vascular Cambium parses a **directed acyclic graph (DAG)** of tasks. Steps that have no dependencies (or whose dependencies are already complete) are executed concurrently in separate terrariumed worktrees up to a configurable concurrency limit.

```
                  [ Load Sequence YAML ]
                             │
                             ▼
                  [ Build Dependency DAG ]
                             │
              ┌──────────────┴──────────────┐
              ▼                             ▼
        [ Run Step A ]                [ Run Step B ]
       (Terrarium Worktree)            (Terrarium Worktree)
              │                             │
              ▼                             ▼
        [ Commit A ]                  [ Commit B ]
              │                             │
              └──────────────┬──────────────┘
                             │  (both complete)
                             ▼
                       [ Run Step C ]
```

---

## 2. Updated Sequence YAML Schema

```yaml
name: refactor-auth-module
substrate: core
branch: feature/auth-refactor
concurrencyLimit: 2           # Max parallel sprouts running
onFailure: pause              # "pause", "halt", "retry"
maxRetries: 3                 # Only used if onFailure: retry
steps:
  - id: step-read-code
    status: pending
    transcript: "Read src/auth/ and identify token schema files"

  - id: step-update-docs
    status: pending
    transcript: "Update ARCHITECTURE.md to reflect the new token schema"
    # No dependsOn -> Runs in parallel with step-read-code

  - id: step-apply-changes
    status: pending
    dependsOn: 
      - step-read-code
    transcript: "Apply the token schema changes to each file identified"

  - id: step-run-tests
    status: pending
    dependsOn:
      - step-apply-changes
    transcript: "Run the test suite and fix any failures"
    modelProvider: local
    modelName: llama3.2
    modelBaseURL: http://localhost:11434/v1 # Explicitly target a local LLM endpoint
```

---

## 3. Concurrency & Failure Recovery Logic

### A. Parallel Dispatching (Go Goroutines)
1.  Go Stem builds a dependency resolver.
2.  In a loop, the Vascular Cambium identifies all steps with status `"pending"` whose `dependsOn` steps are `"complete"`.
3.  Dispatches a Go goroutine for each available step up to the `concurrencyLimit`.
4.  Each parallel step runs in a separate, isolated shadow git worktree (e.g. `/tmp/opentendril-terrarium-step-<id>-<random>`).
5.  On completion, the terrarium changes are committed and merged back into the shared branch on the host (Phloem transport), and the step status is updated in the YAML file in-place.

### B. Failure Recovery Options (`onFailure`)
*   `halt`: Immediately stops the sequence. No further steps are dispatched.
*   `retry`: Automatically resets the step status to `"pending"` and retries execution up to `maxRetries` times.
*   `pause` (Interactive Recovery):
    *   The runner pauses and outputs the error.
    *   It prompts the user in the CLI: `⚠️ Step <id> failed. [R]etry after fixing code, or [H]alt?`
    *   If running headlessly (MCP / serve mode), it waits for a resume request endpoint to be hit before retrying.

### C. Detached Asynchronous Execution
To support long-running, overnight workloads using slow local LLMs, sequences can be executed asynchronously.
* Using the CLI flag `--detach` (or `-d`), the sequence is handed off to the Stem daemon via the `POST /v1/sessions/{sessionId}/sequences/run` REST endpoint.
* The daemon runs the sequence in the background, freeing up the user's terminal. Logs and status can be tracked via existing session event APIs.

---

## 4. Side Question: Calling OpenTendril / CodexCLI Integration

### 1. Can CodexCLI call OpenTendril?
**Yes!** OpenTendril is built specifically as a headless backend. We can connect CodexCLI to OpenTendril in two ways:
1.  **Via OpenAI API:** Point CodexCLI's LLM completion URL to the local OpenTendril API port (e.g., `LOCAL_INFERENCE_URL=http://localhost:8080/v1`) using the Bearer Auth we just implemented. CodexCLI's terminal agent will then use OpenTendril to solve coding subtasks.
2.  **Via MCP:** Add OpenTendril's stdio or HTTP MCP server to CodexCLI's config, allowing it to call the `sproutTendril` and `viewGenome` tools natively.

### 2. Can I (Antigravity) call OpenTendril?
**Yes, but we should not do it for local git operations.** 
While the `sproutTendril` tool is registered in my available MCP servers, running git checkout, commit, or push operations *inside* a Sprout container terrarium is less efficient for host management. Sprout terrariumes are meant for untrusted, isolated code editing. For host-side repo operations (like branch creation and pushes), executing commands directly on your host terminal using my approved `run_command` tool is much faster and cleaner.

---

## 5. Proposed Changes

### Component: Go Stem Orchestrator

#### [NEW] [orchestrator/sequence.go](../cmd/stem/internal/orchestrator/sequence.go)
*   Define `Sequence` and `SequenceStep` Go structs.
*   Implement DAG topological traversal and concurrent task scheduler using channels and goroutines.
*   Implement CLI prompt checking for `onFailure: pause` interaction.

#### [NEW] [cmdsequence.go](../cmd/stem/cmdsequence.go)
*   Implement CLI commands: `tendril sequence run <path>` and `tendril sequence list`.
*   Support `--provider`, `--model`, and `--base-url` override flags.
*   Support `--detach` flag for asynchronous execution.

#### [NEW] [internal/api/sessions.go](../cmd/stem/internal/api/sessions.go)
*   Implement `POST /v1/sessions/{sessionId}/sequences/run` to support detached background runs.

#### [MODIFY] [internal/api/mcp.go](../cmd/stem/internal/api/mcp.go)
*   Expose `runSequence` tool in MCP, supporting headless execution of parallel sequence files.

---

## 6. Verification Plan

### Automated Tests
*   **DAG Scheduler Tests:** Verify that steps are executed in correct topological order, and that steps with no dependencies run concurrently.
*   **Failure Pause/Retry Tests:** Simulate step failure with configured `retry` and `pause` modes to verify correct recovery branches.

### Manual Verification
1.  Create a parallel sequence YAML: `step-1` (create file A), `step-2` (create file B), and `step-3` (read A and B, depends on 1 and 2).
2.  Run `tendril sequence run test.yaml` with `concurrencyLimit: 2`.
3.  Verify from logs that `step-1` and `step-2` run concurrently, and `step-3` only starts after both complete.
