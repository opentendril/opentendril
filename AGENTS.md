# OpenTendril Agent Governance & Operating Model (AGENTS)

This document defines the "Operating Constitution" for all AI agents—both internal background processes and external AI builders (like Antigravity)—interacting with the OpenTendril workspace. It enforces safety, determinism, and prevent workspace conflicts.

---

## 1. Builder Authority & PR Discipline

Any external builder agent (e.g. Antigravity) must operate under strict boundary constraints:
* **No Merge Authority:** Builders do not own merge authority. A builder must never merge a PR or enable auto-merge.
* **Scope Discipline:** Keep Pull Requests small, isolated, and single-purpose (one task/issue per PR).
* **Minimal Diffs:** Avoid drive-by or speculative refactors. Stick strictly to the approved plan.
* **No Direct Push to Main:** Never commit or push directly to the `main` branch. All changes must go through a staging branch.
* **Branch Cleanup:** Builders must never delete remote branches or close/reopen PRs unless explicitly instructed by the human.

---

## 2. The 3-Gate Execution Lifecycle

To prevent "agent runaways" and maintain absolute system security, all non-trivial features or refactors must route through a 3-gate lifecycle.

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
1. **Design RFC:** The Architect Agent reads the codebase and drafts a Design RFC issue using `.github/ISSUE_TEMPLATE/design-rfc.md`. It defines target behaviors and system invariants.
2. **[Human Gate A]:** The human reviews the Design RFC. If approved, the human comments `approved` on the issue.

### Gate B: Implementation Plan Approval
3. **Implementation Plan:** The Architect Agent drafts an Implementation Plan issue using `.github/ISSUE_TEMPLATE/implementation-plan.md`. This plan contains:
   * Current state with exact file and line citations (`path/to/file:line-range`).
   * Proposed code modifications (the Delta).
   * Specific task slices (isolated implementation steps).
   * Links to the approved Design RFC.
4. **[Human Gate B]:** The human reviews the plan. If approved, the human triggers execution by commenting `approved, build slice N`. A blanket "approved" is not an execution trigger.

### Gate C: Merge Decision
5. **Build & Test:** The Builder Agent checks out a staging branch (`staging/ai-*`), implements the approved slice, runs local verification tests (`make check-all`), and opens a Draft PR linked to the issue.
6. **Drift Review:** The Architect Agent reads the Draft PR, compares the diff to the approved plan, and posts a structured drift review comment classifying any deviations as `P0` (must fix), `P1` (should fix), or `P2` (consider).
7. **[Human Gate C]:** The human reads the drift review, checks CI status, and manually merges the PR.

---

## 3. Git Preflight Checklist (Conflict Avoidance)

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

---

## 4. Casing & Boundary Mapping Rules

To eliminate case mismatch bugs across Go, Python, and JSON boundaries:

* **Internal Python Norms:** Inside Python files, use standard PEP 8 `snake_case` for variables, functions, and methods.
* **Internal Go/JS Norms:** Inside Go, JS, and TS files, use standard `camelCase` (or `PascalCase` for public Go symbols). **No `snake_case` is permitted.**
* **External Contracts Boundary:** All externally visible identifiers crossing service boundaries must use **kebab-case** (hyphens instead of underscores):
  * JSON request/response payload keys (Go/TS must map internal identifiers to kebab-case JSON tags).
  * HTTP API endpoint paths.
  * Stored database keys and domain enums.
* **Filesystem separators:** No underscores are allowed in filenames across the entire filesystem, with the single exception of Go test files (`*_test.go`) and platform build suffixes (e.g. `*_linux.go`).

---

## 5. Internal Agent Taxonomy

OpenTendril runs specific background processes restricted to distinct scopes:

### A. The Root Agent (FastAPI `/edit`)
* **Scope:** Self-building. Applies approved edits and patches.
* **Sandbox:** Bound strictly to `/workspace`. Edits must pass internal syntax compilation checks before git commit.

### B. The Marketing Agent (Headless Cron)
* **Scope:** Monitors progress and drafts public changelogs.
* **Sandbox:** Read-only log access. **Zero push permissions** to external networks (X, LinkedIn) without human cryptographic approval.

### C. The Dreamer Agent (Background Optimizer)
* **Scope:** Cleans conversation databases and refines semantic search indexes.
* **Sandbox:** Access restricted to SQLite/vector database files. Cannot write source files or run shell tests.
