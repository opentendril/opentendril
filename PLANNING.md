# OpenTendril SDLC & Planning Workflow

This document codifies the Software Development Lifecycle (SDLC) process for implementing features, fixing bugs, or refactoring components in OpenTendril. To accommodate different project scales, speed requirements, and security compliance levels, OpenTendril supports three configurable **SDLC Profiles**.

---

## 🛠️ Configurable SDLC Profiles

The active workflow profile is set in the kernel configuration (e.g., `TENDRIL_SDLC_PROFILE=solo|collaborative|enterprise`).

### 1. Solo Mode (`solo` / Fast-Track)
* **Best for:** Prototyping, hobby projects, solo developers, and low-risk bug fixes or documentation updates.
* **Friction Level:** Zero-friction.
* **Process:**
  1. **Direct Edit:** The Pollinator analyzes requirements and edits the files directly in the workspace without drafting plans.
  2. **Verification:** The Pollinator compiles files and runs local tests (`make check-all`).
  3. **Auto-Commit:** The Pollinator commits the changes directly to the current active branch and pushes to the remote repository.

### 2. Collaborative Mode (`collaborative` / Standard OS)
* **Best for:** Open-source contributions, shared repositories, and standard feature development.
* **Friction Level:** Balanced (staged reviews).
* **Process:**
  1. **Implementation Plan:** The Pollinator drafts a lightweight implementation plan and waits for a single user approval.
  2. **Branch Isolation:** The Pollinator checks out a `staging/ai-*` branch to do the work.
  3. **Draft PR:** The Pollinator opens a Pull Request on GitHub and waits for the human to review and merge it.

### 3. Enterprise Mode (`enterprise` / Compliance)
* **Best for:** Production codebases, security-critical paths, corporate systems, and audited workflows.
* **Friction Level:** High (strict safety first).
* **Process:**
  1. **Gate A (Design RFC):** The Pollinator drafts a formal Design RFC to align on architecture and data models before any code is written.
  2. **Gate B (Detailed Plan):** The Pollinator drafts a comprehensive Implementation Plan citing line ranges of files to be changed. The user must approve each task slice before execution.
  3. **Terrariumed Staging:** Code is modified inside isolated terrariumes, running automated pre-commit hook checks and linting pipelines.
  4. **Gate C (Drift Review & PR):** The Pollinator generates a Draft PR and runs an automated Drift Review to classify any deviations from the plan before human merge.

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
