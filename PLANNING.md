# OpenTendril SDLC & Planning Workflow

This document codifies the step-by-step Software Development Lifecycle (SDLC) process for implementing features, fixing bugs, or refactoring components in OpenTendril. This process applies to human developers and external AI builders (like Antigravity) alike.

---

## The 4-Phase Delivery Lifecycle

```
 ┌──────────────────────┐      ┌──────────────────────┐
 │   1. PLANNING MODE   │─────►│  2. EXECUTION MODE   │
 │   Draft design and   │      │  Work through checks │
 │   seek human signoff │      │  in task.md          │
 └──────────────────────┘      └──────────┬───────────┘
                                          │
 ┌──────────────────────┐      ┌──────────▼───────────┐
 │    4. DELIVERY       │◄─────│   3. VERIFICATION    │
 │   Push to staging/   │      │  Run make check-all  │
 │   and open GitHub PR │      │  and write walkthrough│
 └──────────────────────┘      └──────────────────────┘
```

---

## Phase 1: Planning Mode
Before modifying a single file, the builder enters Planning Mode.
1. **Research:** Survey the codebase and dependencies using read-only search tools.
2. **Draft Plan:** Create `implementation_plan.md` in the local conversation data folder. The plan must specify:
   * Key goals.
   * Casing/naming guardrail validations.
   * Specific file modifications, creations, and deletions using `[MODIFY]`, `[NEW]`, and `[DELETE]`.
   * The verification plan (how correctness will be tested).
3. **Seek Approval:** The plan must include open design questions or issues that require feedback. Set `RequestFeedback: true` in the plan's metadata. **Halt execution and wait for explicit human approval.**

---

## Phase 2: Execution Mode
Once the plan is approved, execution begins.
1. **Initialize Checklist:** Create `task.md` in the local conversation folder, listing all tasks, sub-tasks, and testing milestones.
2. **Incremental Edits:** Edit files in small, logical phases:
   * Before working on a task, mark it as in progress `[/]` in `task.md`.
   * After completing a task, mark it as completed `[x]`.
   * Commit intermediate edits locally if working on complex files to maintain backup recovery points.

---

## Phase 3: Verification Mode
All modifications must be validated before they leave the local workspace.
1. **Syntax Checks:** Compile python files (`python -m py_compile`) and check Go compilation.
2. **Test Execution:** Run `make check-all` from the root `Makefile` to lint all code and run the complete test suite.
3. **Draft Walkthrough:** Create `walkthrough.md` in the conversation data folder summarizing the changes, the tests executed, and visual/log proof of execution.

---

## Phase 4: Delivery & Push to Main
We never push directly to the default `main` branch.
1. **Checkout Staging Branch:** Create a staging branch prefixed with `staging/` (e.g. `staging/add-mcp-gateway-125022`).
2. **Push to Remote:** Push the staged branch to the GitHub repository.
3. **Open Pull Request:** Open a pull request against `main`. 
4. **Human Review & Merge:** The human developer reviews the code diff and verification walkthrough. Once approved, the human merges the PR using the Squash and Merge strategy.
