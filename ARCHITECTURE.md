# OpenTendril Architecture & Unified Go Stem Orchestrator

This document defines the high-level architecture and data-flow specifications of the OpenTendril framework. 

---

## Guiding Principles

These principles are the acceptance criteria for every architectural decision. A proposed mechanism that violates one is the wrong mechanism.

1.  **Security-first, minimal-config default.** The basic runtime is minimal-config **and** secure by default. Hardening is **opt-out**: the secure posture is what you get with no configuration, and you must actively and knowingly loosen it — never opt in to gain it. Enterprise and scaling options are **opt-in**: added deliberately when needed. Configuration is therefore required to *reduce* security or to *scale up*, never to *become* secure.
2.  **Best-tool-for-the-job configurability.** Where a genuine choice exists (a signing engine, an egress policy, a transport, a review gate), it must be selectable, so a developer can pick the right tool and flex to the effectively unlimited range of developer and enterprise configurations. Every such choice ships with a sensible default plus an override.
3.  **Nothing hard-coded; everything questioned.** Treat each mechanism as a hypothesis, not a given. A default may be obvious, but it remains *a default with an override*, not a baked-in assumption. Deployment location, credential type, and transport must never be assumed.
4.  **Build for the current use-case; architect for the future.** The implementation that lands may cover only today's need. The architecture must not foreclose known future directions (portability, multiple cooperating instances, workload routing). Minimise dependencies so an instance stays cheap to stand up, move, and replicate.

The test for any new mechanism: it must degrade to a simple, secure default **and** be overridable for a use-case not yet imagined. If it can't do both, it is the wrong mechanism.

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
2.  **LLM client management:** Drawing inference through the Roots provider layer (below) — no external proxy services are ever run.
3.  **Genotype & Plasmid resolution:** Auto-indexing system prompts (`index.yaml`) and staging markdown context templates.
4.  **Terrarium Isolation:** Dynamically growing stateless language sprouts and executing task scripts securely.

### B. The Roots (`roots/`) — LLM Provider Layer
*   **Role:** The provider-connectivity pillar. Just as physical roots draw nutrients from the Mycorrhizal Network (the LLM), the Roots package (`roots/llm`) owns every direct LLM concern: provider clients (Anthropic, OpenAI, Grok, local Ollama/vLLM), live model discovery, capability registries, and tier/coordinator routing.
* **Boundary:** The Stem *consumes* the Roots; it contains no provider code of its own (the layer was ported out of `cmd/stem/internal`). Nothing outside the Roots speaks a provider API directly.

### C. Ephemeral Sprout Terrariumes
*   **Role:** Safe, isolated containers running target programming languages (e.g. `opentendril-go`, `opentendril-typescript`). See [Terrarium Terrariuming](docs/terrarium.md) for details on supported isolation tiers (Docker, gVisor, Firecracker).
*   **Responsibilities:**
    *   Boots ephemerally when a Sprout run starts.
    *   Mounts the terrarium Git worktree locally.
    *   Receives structured JSON command payloads from the Go Stem host over persistent input/output pipes.
    *   Runs compilers, linters, and unit test suites inside the isolated container, keeping unverified code execution away from the developer's host machine.

### D. Branches (Capability Groups)
*   **Role:** Logical security zones or capability groupings initialized by the Stem at boot time (e.g., Core Branch, Enterprise Branch).
*   **Responsibilities:** They dictate the blast radius and authorization scope. A Sprout that grows on the Core Branch physically cannot access integrations (Symbiotic Nodules) located on the Enterprise Branch.

### E. Symbiotic Nodules (External Integrations)
*   **Role:** Persistent, modular integration plugins that reach out and attach to external enterprise systems (e.g., GCP, AWS, Datadog) without bloating the lightweight Go Stem binary. Symbiotic Nodules **form symbioses** on specific Branches. *(Formerly "Tendrils"; "Tendril" is now the concept/brand only — see SYNTHETIC-TAXONOMY.md.)*
*   **Architectural Approaches:**
    1.  **Downstream MCP Servers:** The Stem speaks the Model Context Protocol downstream to external Tendril tools (e.g., `opentendril-gcp-mcp`).
    2.  **Core Capability Interfaces:** Conditionally compiled Go interfaces (`internal/core`) tailored for enterprise builds using build tags (`go build -tags enterprise`).

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
* **Symbiotic Immune System (Destructive Fuzzing):** A `macrophage` step's genotype (`macrophage.json`) writes a native Go fuzz test (`FuzzXxx(f *testing.F)`) against the code the Worker just changed. After that agent turn, the Conductor — not the LLM — deterministically runs `go test -fuzz=Fuzz -fuzztime=10s` inside a dedicated, Go-toolchain-enabled, still network-isolated terrarium (`opentendril-go-fuzz:latest`; every other Go sprout keeps the minimal, toolchain-free `opentendril-go:latest`) and inspects the exit code and output for a panic or failing input. A crash is a hard, structural failure — never an LLM judgment call — that sprouts a recursive **Debugger** exactly like a Verifier failure does, so a crashing merge can't go through until the fix survives re-fuzzing.
*   **Phenotypic Selection (Genetic Algorithm):** When a step is configured with a `selection:` block, Go Stem executes a true LLM-driven Genetic Algorithm to mathematically prove the most optimal code (inspired by ELM and DeepMind's FunSearch). The Orchestrator acts as a Generational Engine, managing a population of parallel Docker Sprouts (default 6, capped at 12 to prevent Docker CPU starvation from corrupting benchmark fitness signals). It dynamically cross-breeds populations using **Prompt-Level Inheritance**, where the diffs of surviving parents are sampled back into the next generation's transcripts as "parent genes". By injecting orthogonal structural directives and spreading LLM temperatures (Explore vs Exploit), it forces meaningful diversity. Surviving phenotypes are evaluated against a sterile container fitness test (e.g. `make benchmark` parsing `ns/op`), and the fittest Alpha Phenotype is merged back to the Substrate (via the Phloem).
*   **Parallel Sprouting (Distributed Map-Reduce):** When a step is configured with `parallel: true`, the Meristem coordinator branches the transcript into `sproutCount` (default 5) independent sub-tasks (Map). A fixed pool grows one ephemeral sprout terrarium per sub-task simultaneously, each on an isolated shadow-worktree branch, with real-time status multiplexed onto the EventBus (`sprout-emerged`, `sprout-matured`, `sprout-withered`). A withered sprout — container crash, panic, or LLM timeout — never halts the Stem; surviving branches are merged back and a final **MycelialMerge** consensus sprout reconciles all results into the Substrate (Reduce).
*   **Epigenetic Memory (Self-Learning):** The `tendril adapt` command dynamically mines local git history to build an Epigenetic Memory. It chunks commits at file boundaries, uses the Meristem Map-Reduce engine to extract recurring coding styles, architectural patterns, and aesthetic preferences, and permanently encodes them into `.tendril/genome/epigenetics.md`. This file is then natively inherited by every new Sprout via `buildAgentSystemPrompt`, allowing the Orchestrator to naturally "learn" and adopt the host repository's conventions.

---

## 5. Command Center & Daemon Persistence (the Tendril OS)

To support the **Tendril OS** (the Operating System of OpenTendril) visual Command Center, the Go Stem operates as a persistent, multi-session, remotely monitorable daemon.

*   **Unified Interface Layer (`Core` + `SessionManager`):** The CLI (`tendril phytomer`, `tendril chat`), MCP Server (stdio/SSE), and REST API (`/v1/phytomers`) all route governed phytomer-lifecycle capabilities through a single, transport-free `Core` service (`cmd/stem/internal/core`). The Core owns the declarative capability registry; the three surfaces are thin adapters that translate their transport to and from it. Every interaction binds to a unique `session_id`. This allows for true **Multi-Session Independence**, where concurrent chats can operate on completely different LLM models and Epigenetic Genomes simultaneously without touching global configurations. See `AGENTS.md` §5 for the parity enforcement rules.
*   **Persistent State (`history.db`):** The Go Stem persists its memory locally via a lightweight, CGO-free SQLite database (`.tendril/history.db`) operating in WAL mode. It captures Session metadata, chat logs, EventBus telemetry, and Sprout execution histories. This ensures that the visual Command Center can instantly hydrate its state upon browser refresh. For high-performance headless runs, this SQLite logging can be completely bypassed via `OPENTENDRIL_DB_LOGGING=false`.
*   **Pluggable Remote EventBus (Swarm Monitoring):** The internal EventBus utilizes asynchronous, buffered `Sink` interfaces. In addition to local WebSockets, it supports **Remote Transporters** (such as raw RESP Redis or remote WebSockets via `OPENTENDRIL_REMOTE_SINKS`). This allows a centralized **Tendril OS** to monitor a massive fleet of distributed OpenTendril instances in real-time. Slow remote sinks will silently drop frames rather than blocking the core Orchestrator's execution loop.
*   **Visual Command Center (`ui/`):** A strictly decoupled React frontend consumes the daemon over its documented REST (`/v1/phytomers`, history, sprout-runs, events) and WebSocket (`/ws`) surface — no coupling to Go internals — and renders live orchestration as a **living botanical garden**: each run grows as a plant whose branches, tendril tips, and phenotype-selection arenas mutate as EventBus telemetry streams in. On refresh it hydrates cold state from REST and then hot-swaps to the live feed, so nothing is lost. See **[docs/COMMAND-CENTER.md](docs/COMMAND-CENTER.md)** for the architecture and the REST/WS contract, and **[ui/README.md](ui/README.md)** for the component tree, hydration flow, and event → visual mapping.
*   **Trust boundaries (Stem / OS / worker):** the Command Center is a delegating, secret-free reverse proxy — it holds no capability the CLI lacks and no credential of its own — while the Stem alone holds real authority (LLM keys, the bearer API key, the mesh signing key) and terrarium workers hold none at all. See **[docs/DESIGN-SECURITY-POSTURE.md](docs/DESIGN-SECURITY-POSTURE.md)** for the full model and what's enforced by tests today.

### The `/ws` EventBus Gateway Contract

The Command Center (and any decoupled monitor) subscribes to the live telemetry stream over `/ws`. Two contract points are relevant to clients:

*   **Event registry:** the gateway forwards every type in `eventbus.AllEventTypes()`, including `phenotypic-selection` (Genetic-Algorithm progress). Each frame is `{ type, timestamp, source, sessionId?, data?, content? }`.
*   **`?replay=N` (opt-in recent-history replay):** connecting with `ws://<stem>/ws?replay=N` replays up to `N` events (capped at the bus's 100-event in-memory window) *before* the live feed. This lets a refreshed client recover **session-less** sequence telemetry (parallel-sprouting, mycelial-merge, phenotypic-selection) that the per-session REST `…/events` endpoint cannot return. Omitting the parameter preserves the original live-only behavior. The standalone gateway listener also honors `GATEWAY_PORT` (default `9090`) and degrades gracefully if that port is taken, since the same `/ws` surface is mounted on the main API mux.
