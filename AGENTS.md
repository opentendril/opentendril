# OpenTendril Agent Taxonomy & External Builder Operating Model

This document defines how AI agents—both internal background processes and external AI builders (like Antigravity)—interact with and modify the OpenTendril workspace. It acts as the formal operating agreement to maintain trust, safety, and security.

---

## 1. Operating Model with an External Builder

When an external developer AI agent (the "Builder") is paired with OpenTendril, it must strictly follow this 5-stage lifecycle to prevent runtime corruption, directory clutter, or logic drift.

### Stage 1: Research & Survey
* **Rules:** The Builder may only use read-only tools (like `list_dir`, `view_file`, `grep_search`).
* **Restriction:** No source code changes or modifying bash commands are allowed. The goal is to thoroughly inspect project requirements and configurations without affecting workspace state.

### Stage 2: Plan & Approve
* **Rules:** The Builder must construct a detailed implementation plan describing which files will be modified, created, or deleted.
* **Artifact:** Create or update `implementation_plan.md` in the brain folder.
* **Human Checkpoint:** Set `RequestFeedback: true` in the metadata. **The Builder must stop and wait for explicit human approval** before making any edits.

### Stage 3: Execute & Track
* **Rules:** Once approved, the Builder creates `task.md` in the brain folder to track task checklists.
* **Tracking:** Mark active items with `[/]` (in progress) and completed items with `[x]` (completed) before and after editing files. Work must progress through small, atomic changes.

### Stage 4: Local Verification
* **Rules:** The Builder must run the codebase tests locally before pushing any changes.
* **Orchestration:** Run verification using the system `Makefile` (e.g. `make lint` and `make test`).
* **Syntax Checks:** Python files must pass `py_compile` checks. Go files must compile without errors.

### Stage 5: Commit & Staging
* **Rules:** Once verified, the Builder commits code using Conventional Commits.
* **Branching:** The Builder must push changes to a dedicated AI staging branch (e.g., `staging/patch-description-timestamp`).
* **Final Gate:** The Builder opens a GitHub Pull Request. A human reviewer validates the PR and merges it into `main`.

---

## 2. Internal Agent Taxonomy

OpenTendril delegates background operations to specific, bounded agent profiles to enforce system security and scalability.

### A. The Root Agent (Core Orchestrator)
* **Role:** The primary self-healing engine. Debugs and expands OpenTendril's codebase based on user chat commands.
* **Primary Loop:** The "Moat Loop" (`/edit`). Translates chat requests into syntax-tested, automatically committed code.
* **Tool Access:** Bounded strictly to the designated `WORKSPACE_ROOT`. Edits must pass internal compilation and pytest suites before commit.

### B. The Marketing Agent (Zero-Touch Engine)
* **Role:** Monitors the repository for milestones and automatically drafts "Build in Public" documentation, social posts, and logs.
* **Primary Loop:** Triggered by git commits and changes in `PROGRESS.md`.
* **Guardrails:** This agent has **zero push access** to external networks (X/Twitter, LinkedIn, PyPI) without routing its draft payload through the `ApprovalGate` for cryptographic human signing.

### C. The Dreamer Agent (Background Optimizer)
* **Role:** Operates on background cron threads to optimize the vector database, clean memory logs, and summarize conversations.
* **Primary Loop:** Runs hourly interval tasks (`src/dreamer.py`).
* **Guardrails:** Bounded strictly to memory databases (SQLite or vector arrays). Cannot modify source files or execute shell command tools.

---

## 3. Governance and Evolution Roadblocks
As OpenTendril scales, new agent profiles will be loaded dynamically via the `skills/` directory (signed using a cryptographic `SECRET_KEY`). Before any new agent profile is merged, its tool permissions and boundaries must be formally mapped in this document. Under no circumstances may an untrusted agent profile be granted direct shell execution capabilities.
