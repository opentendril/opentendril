# OpenTendril Agent Governance & Operating Model (AGENTS)

This document defines the "Operating Constitution" for all AI agents—both internal background processes and external AI builders (like Antigravity)—interacting with the OpenTendril workspace. It enforces safety, determinism, and prevent workspace conflicts.

---

## 1. The Supreme Directive: Biological Taxonomy Enforcement

OpenTendril is built on a strict synthetic biological architecture. Any external builder agent (the Mycorrhizae/LLM) that interacts with, self-evolves, or generates code/documentation for OpenTendril **MUST NOT** corrupt the framework with standard industry IT jargon.

*   **No "Agents" or "Workflows"**: You must use **Tendrils**, **Sprouts**, and **Sequences**.
*   **No "Sandboxes" or "Tools"**: You must use **Terrariums** and **Plasmids**.
*   **No "Brains"**: The Stem is a vascular routing system, not a brain. 

**Self-Evolution Rule**: If the AI attempts to self-improve, refactor, or generate new architecture, it is absolutely forbidden from reverting to disorganized industry standards. All new concepts must be mapped to their biological equivalent using `GLOSSARY.md` and `SYNTHETIC-TAXONOMY.md` as the ultimate source of truth.

**Placement, not just naming**: Enforcement extends beyond vocabulary. **Every architectural decision must classify the capability against the taxonomy** — *which organ does it belong to, and why?* — before a design is proposed. Use the decision heuristic in `SYNTHETIC-TAXONOMY.md` §5 ("How to classify a new capability"): local computation on the plant's own code is a **core organ** (the Rhizome parses the Substrate; a parser is never a Nodule), an external network *service* is a **Symbiotic Nodule**, a worked-on repo is a **Substrate**, our own outward connectivity is a **Root**. Every Design RFC must state this in its "Taxonomy placement" section. Misplacing a capability (e.g. bloating the Stem with what should be a Nodule, or exiling a core organ to a service) is as much a violation as using banned jargon.

---

## 2. Builder Authority & PR Discipline

Any external builder agent or process must operate under strict boundary constraints:
* **No Merge Authority:** Builders do not own merge authority. A builder must never merge a PR or enable auto-merge **on its own initiative**. The sole exception is an **explicit, per-PR human instruction** to merge a specific named PR; a blanket or standing "you may merge" does not qualify, and the builder must never enable auto-merge. Absent such a direct instruction, the human merges at Gate C.
* **Scope Discipline:** Keep Pull Requests small, isolated, and single-purpose (one task/issue per PR).
* **Minimal Diffs:** Avoid drive-by or speculative refactors. Stick strictly to the approved plan.
* **No Direct Push to Main:** Never commit or push directly to the `main` branch. All changes must go through a staging branch.
* **Branch Cleanup:** Builders must never delete remote branches or close/reopen PRs unless explicitly instructed by the human.
* **Self-contained source (no GitHub refs in files):** Never write a GitHub issue/PR number into a repo file — no `(#NNN)`, `issue #NNN`, `PR #NNN`, or GitHub issue/pull URLs in code comments, Dockerfiles, requirements, or docs. Put that context in the commit message and PR description instead. See GUARDRAILS.md → Documentation Governance; enforced by CI (`scripts/check-no-issue-refs.sh`).

---

## 3. The 3-Gate Execution Lifecycle

To prevent "agent runaways" and maintain absolute system security, all non-trivial features or refactors must route through a structured lifecycle. The strictness of this process is governed by the active `TENDRIL_SDLC_PROFILE` configuration (Solo, Collaborative, or Enterprise, as defined in `PLANNING.md`).

In **Enterprise Mode** (`enterprise`), the full 3-gate lifecycle is strictly enforced:


```
 ┌──────────────────────┐      ┌──────────────────────┐      ┌──────────────────────┐
 │   1. DESIGN RFC      │─────►│ 2. IMPL PLAN SPEC     │─────►│     3. BUILD PR      │
 │  Define what to edit │      │ Delta, file citations│      │ Local checks, draft  │
 └──────────┬───────────┘      └──────────┬───────────┘      └──────────┬───────────┘
            │                             │                             │
            ▼ [HUMAN GATE A]              ▼ [HUMAN GATE B]              ▼ [HUMAN GATE C]
     comment "approved"             comment "approved,             merge PR and close
                                    build slice N"                 staging branch
```

### Gate A: Design RFC Approval
1. **Design RFC:** The Thinker reads the codebase and drafts a Design RFC issue using `.github/ISSUE_TEMPLATE/design-rfc.md`. It defines target behaviors and system invariants.
2. **[Human Gate A]:** The human reviews the Design RFC. If approved, the human comments `approved` on the issue.

### Gate B: Implementation Plan Approval
3. **Implementation Plan:** The Thinker drafts an Implementation Plan issue using `.github/ISSUE_TEMPLATE/implementation-plan.md`. This plan contains:
   * Current state with exact file and line citations (`path/to/file:line-range`).
   * Proposed code modifications (the Delta).
   * Specific task slices (isolated implementation steps).
   * Links to the approved Design RFC.
4. **[Human Gate B]:** The human reviews the plan. If approved, the human triggers execution by commenting `approved, build slice N`. A blanket "approved" is not an execution trigger.

### Gate C: Merge Decision
5. **Build & Test:** The Worker checks out a staging branch (`staging/ai-*`), implements the approved slice, runs local verification tests (`make check-all`), and opens a Draft PR linked to the issue.
6. **Drift Review:** The Thinker reads the Draft PR, compares the diff to the approved plan, and posts a structured drift review comment classifying any deviations as `P0` (must fix), `P1` (should fix), or `P2` (consider).
7. **[Human Gate C]:** The human reads the drift review, checks CI status, and manually merges the PR.

---

## 4. Git Preflight Checklist (Conflict Avoidance)

Before starting work on ANY task, the builder MUST run this sequence to guarantee a clean workspace:

1. Run `git status -sb`. If the worktree is not clean: **STOP** and report the dirty files. Do not stash.
2. Run `git fetch origin --prune` to sync remote references.
3. Switch to `main`: `git switch main` (unless continuing work on an active, clean feature branch).
4. Run `git pull --ff-only origin main`. If this fails (local has diverged): **STOP** and report. Do not rebase or merge.
5. Confirm synchronization: Run `git rev-list --left-right --count origin/main...main`.
   * **Expected result is exactly `0 0`**.
   * If the result is `0 N` (local is ahead of remote): **STOP**. Report the branch status and do not push.
   * If the result is `N 0` or `N M` (diverged): **STOP** and report.

Only after the preflight check returns exactly `0 0` on a clean `main` branch may you create a new feature/staging branch.

### GitHub Auth: `GITHUB_TOKEN` via direnv

The canonical GitHub PAT variable is **`GITHUB_TOKEN`** (the Stem also accepts `GITHUB_PERSONAL_ACCESS_TOKEN` as a legacy alternate). The token must be present in the Stem's **process environment** — terrariums push branches over HTTPS, and `gh`'s keyring does not expose its token to child processes. The recommended setup uses [direnv](https://direnv.net/):

```bash
cp .envrc.example .envrc
direnv allow
```

The `.envrc` exports `GITHUB_TOKEN` from `gh auth token` when it is not already set, and loads `.env` on top. `.envrc` is gitignored — never commit it.

---

## 5. Casing & Boundary Mapping Rules

To eliminate case mismatch bugs across Go, Python, and JSON boundaries:

* **Internal Python Norms:** Inside Python files, use standard PEP 8 `snake_case` for variables, functions, and methods.
* **Internal Go/JS Norms:** Inside Go, JS, and TS files, use standard `camelCase` (or `PascalCase` for public Go symbols). **No `snake_case` is permitted.**
* **External Contracts Boundary:**
  * JSON request/response payload keys must use **`camelCase`** (Go/TS must map internal identifiers to camelCase JSON tags, e.g. `json:"sessionId"`). This matches GUARDRAILS.md and CONTRIBUTING.md.
  * HTTP API endpoint paths must use **kebab-case** (e.g. `/api/mcp-tools`).
  * Stored database keys and domain enums must use **kebab-case** (e.g. the EventBus event types `sprout-emerged`, `phenotypic-selection`).
* **Filesystem separators:** No underscores are allowed in filenames across the entire filesystem, with the single exception of Go test files (`*_test.go`) and platform build suffixes (e.g. `*_linux.go`).
* **Frontend exception (`ui/`):** The Command Center UI tree follows standard React/TypeScript convention — `PascalCase.tsx` component files, `camelCase.ts` modules, and `PascalCase/` component-family folders. Builders must **not** rewrite these to merged-lowercase; the casing/boundary rules above apply to Go and Python. JSON keys and API paths in `ui/` still follow the external-contract rules (`camelCase` keys, `kebab-case` paths).

---

## 6. Interface Parity — Adapters Translate Only

The CLI, MCP, and OpenAPI/REST surfaces are **projections of one core capability registry**, not independent implementations. To keep them from silently diverging:

* **One Core owns command authority.** Every governed command capability lives in `cmd/stem/internal/core` as a declarative `Capability` in the registry. A capability MUST be invokable with **zero HTTP, CLI, or MCP types in scope** — that is the litmus test for the boundary.
* **Adapters translate transport ↔ core only.** The REST handlers (`internal/api`), the MCP server (`internal/api/mcp.go`, `cmd-mcp.go`), and the CLI subcommands (`cmd/stem/cmd-*.go`) may decode/encode their transport and map errors, and **nothing else**. No business logic in a handler. Do not reach for orchestrator/terrarium/gateway internals from an adapter path for a governed capability — route it through `core.Core`.
* **Parity is enforced, not disciplinary.** `core.CapabilityNames()` is the canonical set. The parity tests under `cmd/stem` (`TestInterfaceParityCoverage`) assert REST == MCP == CLI == that canonical set and go **red** the moment a capability is added to one surface but not the others. The boundary test (`internal/core/boundary_test.go`) fails if the Core imports a transport or execution internal. Never weaken or skip these to land a change — add the capability to the registry and wire all three surfaces.
* **To add a command capability:** declare it once in the `core` registry (Name, InputSchema, Invoke), then project it onto all three adapters. Adding it to only one surface is a CI failure by design.
* **Views are exempt.** The `/ws` event stream and `?replay` are *views*, not commands, and are deliberately outside the registry and the parity tests. Do not pull them into the capability registry. See `docs/DESIGN-DYNAMIC-ORCHESTRATION.md` for the commands-vs-views distinction.

> **Current scope:** the governed registry covers the session-lifecycle family (`session.create|list|get|update|delete|history`), the genome family (`genome.view|reduce|evolve`), the plasmid family (`plasmid.list|inject`), the substrate-grafting family (`mesh.graft|promote`; `mesh keygen|issue-token` stay deliberately ungoverned CLI-local commands because they mint the workspace's private mesh keys and tokens), the sequence family (`sequence.list|run`; `tendril sequence dynamic` is CLI-local sugar that synthesizes a file and invokes the governed `sequence.run`), and the sprout family (`sprout.run`). All Stem capabilities are now governed in the Core.

---

## 7. Internal Agent Taxonomy

OpenTendril runs specific background processes restricted to distinct scopes:

### A. The Conductor (Orchestration & Planning)
* **Scope:** Dynamic Sequence Manager. Compiles transcripts into Directed Acyclic Graph (DAG) sequences, handles topological sorting, and manages concurrency limits using Go routines.
* **Terrarium:** Runs on the host Stem. It creates terrariumed shadow worktrees, schedules executions, and stages/commits/merges files back cleanly.

### B. The Trinity Roles (Specialized Genotypes)
* **Thinker (Genotype: `thinker.json`):** System architect. Parses the workspace repository maps and designs technical plans and step-by-step implementation logs.
* **Worker (Genotypes: `go-dev`, `typescript-dev`, etc.):** Code editing sprouts. They ingest instructions from the Thinker, write local code edits inside the isolated terrarium, and format files.
* **Verifier (Genotype: `verifier.json`):** Quality assurance. Compiles code, executes unit test runners (`pytest`, `vitest`, `go test`), and validates linter rules.

### C. The Debugger (Auto-Correction Sprout)
* **Scope:** Ephemeral self-repair. Sprouted dynamically by the Conductor when a Verifier — or a Macrophage (below) — step fails.
* **Terrarium:** Ingests the failing step's error output and compiler/test/fuzz-crash logs, applies targeted source patches inside the terrarium, and triggers a retry loop (up to 3 times) to establish dynamic self-healing execution.

### D. The Macrophage (Destructive Fuzz Verification)
* **Scope:** The immune system. A step whose ID contains `macrophage` runs the `macrophage.json` genotype, which reads the recently changed code and writes a native Go fuzz test (`FuzzXxx(f *testing.F)`) targeting the most volatile function it touched — parsers, deserializers, anything shaped by attacker/adversarial input.
* **Terrarium:** After that agent turn, the Conductor deterministically executes the fuzz test (`go test -fuzz=Fuzz -fuzztime=10s`) itself, inside a dedicated Go-toolchain terrarium (`opentendril-go-fuzz:latest`) — network-isolated exactly like every other terrarium, with the host's read-only Go module cache mounted in since this repo doesn't vendor and the terrarium has no network. This is a deliberate, structural check: a crash is decided by exit code and panic/failure-marker detection in Go code (`macrophageFuzzFailed`), never by asking the LLM whether its own fuzz run "passed." A crash is a hard failure that sprouts a recursive Debugger exactly like a Verifier failure does, blocking a clean merge until the Debugger's patch survives re-fuzzing.
