# OpenTendril Architecture & Unified Go Stem Orchestrator

This document defines the high-level architecture and data-flow specifications of the OpenTendril framework. 

---

## 1. The Headless Kernel Split (Brain vs. Hands)

OpenTendril separates high-level cognitive planning (the Brain) from secure file/shell execution (the Hands):

*   **The Brain (Client App):** Developer interfaces (such as Claude Desktop, ChatGPT CLI, Cursor, or VS Code) handle rich system layout reasoning, user prompt processing, and external searches. They communicate with the Go Stem using the **Model Context Protocol (MCP)**.
*   **The Hands (OpenTendril Kernel):** The Go Stem orchestrator runs on the host machine. It receives structured execution commands (e.g. read file, write patch, compile code, run tests) and executes them securely inside sterile terrariumes.

```
┌────────────────────────────────────────────────────────┐
│                       CLIENTS                          │
│   Claude Desktop  │  Cursor / VSCode  │  LibreChat     │
└───────────────────────────┬────────────────────────────┘
                            │ (MCP over stdio / SSE)
                            ▼
┌────────────────────────────────────────────────────────┐
│                UNIFIED GO STEM KERNEL                  │
│   `tendril serve / mcp` (Orchestrator, LLM Routing)   │
└──────┬──────────────────────────────────────────┬──────┘
       │                                          │
       ▼ (Direct LLM Client Calls)                ▼ (JSON payloads over Docker stdin)
┌──────────────────────────────┐          ┌──────────────────────────────┐
│       LLM PROVIDERS          │          │      STATELESS SPROUTS       │
│  Anthropic │ OpenAI │ Local  │          │  (Docker Terrariumes: Go/TS)   │
└──────────────────────────────┘          └──────────────────────────────┘
```

---

## 2. Core Service Anatomy

The system is compiled into a single, unified Go binary (`tendril`) that serves multiple execution interfaces:

### A. The Go Stem Orchestrator (`cmd/stem`)
The single source of truth for execution flow and orchestrator security. It runs on the host machine and is responsible for:
1.  **Protocol Gateways:** Exposing MCP server handlers (stdio/SSE), WebSocket loops for interactive CLI chat, and REST config endpoints.
2.  **LLM client management:** Directly resolving LLM API requests and prompt completions to Anthropic, OpenAI, or local providers (e.g. Ollama/vLLM) without running any external proxy services.
3.  **Genotype & Plasmid resolution:** Auto-indexing system prompts (`index.yaml`) and staging markdown context templates.
4.  **Terrarium Isolation:** Dynamically growing stateless language sprouts and executing task scripts securely.

### B. Ephemeral Sprout Terrariumes
*   **Role:** Safe, isolated containers running target programming languages (e.g. `opentendril-go`, `opentendril-typescript`). See [Terrarium Terrariuming](file:///home/dr3w/GitHub/opentendril/core/docs/terrarium.md) for details on supported isolation tiers (Docker, gVisor, Firecracker).
*   **Responsibilities:**
    *   Boots ephemerally when a Sprout run starts.
    *   Mounts the terrarium Git worktree locally.
    *   Receives structured JSON command payloads from the Go Stem host over persistent input/output pipes.
    *   Runs compilers, linters, and unit test suites inside the isolated container, keeping unverified code execution away from the developer's host machine.

---

## 3. Terrariumed Execution Pipeline (Git-Safe SDLC)

To protect the developer's primary working branch and repository state, Go Stem implements a git-safe terrarium pipeline:

```
                  ┌─────────────────────────────┐
                  │    1. Save Dirty State      │  <-- `git stash -u`
                  └──────────────┬──────────────┘
                                 │
                                 ▼
                  ┌─────────────────────────────┐
                  │    2. Create Terrarium        │  <-- Detached HEAD worktree
                  └──────────────┬──────────────┘
                                 │
                                 ▼
                  ┌─────────────────────────────┐
                  │    3. Sprout Execution      │  <-- Docker mount + command runs
                  └──────────────┬──────────────┘
                                 │
                                 ▼
                  ┌─────────────────────────────┐
                  │   4. Terrarium Verification   │  <-- Tests pass inside sprout
                  └──────────────┬──────────────┘
                                 │
                                 ▼
                  ┌─────────────────────────────┐
                  │    5. Host Merge Back       │  <-- Commit terrarium, ff-merge host,
                  └──────────────┬──────────────┘      teardown worktree, git stash pop
                                 │
                                 ▼
                            (Done / Fail)
```

1.  **Pre-Flight Stash:** If the host repository has uncommitted local files, Go Stem stashes them (`git stash -u`) before checking out.
2.  **Detached Worktree:** Go Stem creates a temporary, detached git worktree in a terrariumed path.
3.  **Staging Plasmids & AST Maps:** Instantiates the Codebase Assessor (Thigmotropism) to generate `repomap.md` and copies genotype plasmids into `.tendril/genome/`.
4.  **Execution & Tests:** Runs the Sprout container. Code edits and tests are run entirely inside this isolated environment.
5.  **Post-Flight Commit & Merge:** If compilation and tests succeed, Go Stem commits the edits inside the terrarium, merges the resulting commit back to the host branch natively, deletes the temporary worktree, and pops the developer's stash to restore local state.
6.  **Read-Only Gating:** If the substrate is marked `readonly: true`, Go Stem skips stashing, blocks commits and merges, and safely discards all terrarium files upon completion.

---

## 4. Dynamic Sequence Conductor (Agent Loops)

Workflow automation is managed by the **Sequence Conductor**:

*   **Directed Acyclic Graph (DAG):** Sequences are compiled into dependency-aware steps (`dependsOn` constraints) with concurrency limitations managed by Go goroutines.
*   **Conductor Step Planning:** Planner sprouts run Coordinator models to analyze code maps and dynamically write/append new steps to the running sequence at execution time.
*   **Trinity Role Delegation:** In complex tasks, the Conductor sprouts three specialized sprouts sequentially:
    1.  **Thinker:** Generates the technical specifications and step-by-step instructions.
    2.  **Worker:** Applies code edits to files.
    3.  **Verifier:** Compiles code and executes unit tests.
*   **Recursive Debugging:** If a verifier step fails, the Conductor intercepts the exit code and dynamically sprouts a **Debugger** step. The debugger patches compile errors recursively (up to 3 times) before the verifier resets.
*   **Phenotypic Selection (Speculative Parallel Execution):** When a step is configured with `phenotypesCount > 1`, Go Stem dispatches multiple parallel sprouts concurrently in separate worktrees under varying LLM temperatures (spread from `0.1` to `0.7`) and plasmid rules. It subjects each phenotype to a sterile container fitness test (e.g. `go test` or linters). The first variant to pass all compilation and unit tests successfully is declared the winner, while the losers are instantly aborted via context cancellation. The winner's branch is merged back to the host substrate.
*   **Parallel Sprouting (Distributed Map-Reduce):** When a step is configured with `parallel: true`, the Meristem coordinator branches the transcript into `sproutCount` (default 5) independent sub-tasks (Map). A fixed pool grows one ephemeral sprout terrarium per sub-task simultaneously, each on an isolated shadow-worktree branch, with real-time status multiplexed onto the EventBus (`sprout-emerged`, `sprout-matured`, `sprout-withered`). A withered sprout — container crash, panic, or LLM timeout — never halts the Stem; surviving branches are grafted back and a final **MycelialMerge** consensus sprout reconciles all results into the host substrate (Reduce).

