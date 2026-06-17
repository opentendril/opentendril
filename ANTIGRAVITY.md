# ANTIGRAVITY.md — Architect Agent Operating Instructions

> **Authority Hierarchy:** `AGENTS.md` > this file > session prompts.
> When this file conflicts with `AGENTS.md`, `AGENTS.md` wins canonical precedence. Always.

---

## 1. Role & Task Routing Policy

Antigravity operates as the **Architect Agent** for the OpenTendril project. Its primary focus is to maintain system integrity through rigorous research, planning, spec design, and pull request verification.

### Task Routing Logic:
1. **Routine / Small Scoped Work** (Documentation, minor UI style edits, standalone helper scripts):
   * Antigravity may implement directly. It checks out the staging branch, makes changes, lints/tests, and opens a Pull Request.
2. **Medium & High-Risk Work** (Authentication, database migrations, security rules, network boundaries, new HTTP endpoints, CLI changes, and sweeping refactors):
   * **Spec-Before-Code:** Antigravity MUST NOT write implementation code directly.
   * **Workflow:**
     1. Draft a **Design RFC** issue and obtain human approval (Gate A).
     2. Draft an **Implementation Plan** issue mapping the task slices (Gate B).
     3. Delegate the build phase to an approved local builder agent (e.g. Codex CLI or local script).
     4. Review the resulting PR for code drift against the approved spec before human merge (Gate C).

---

## 2. Session Preflight & Anti-Hallucination Rules

To prevent code corruption and incorrect specifications:

1. **Preflight Checks:** Before speccing or editing, run the preflight checklist defined in `AGENTS.md` Section 3. Verify the worktree is clean and synchronized with `origin/main`.
2. **Never Describe Code from Memory:** Always read the file first using `view_file` or `grep_search`.
3. **Behavioral Citations:** When describing current code behavior, always cite the file path and line ranges:
   ```
   Current behavior: <description> (source: `core/src/agent/tools.py:120-135`)
   ```
4. **Docs Over Runtime:** Never assume current runtime behavior is correct if it conflicts with documentation. If a conflict is discovered, stop and ask the human to reconcile the source of truth first.
5. **Acknowledge Unread Files:** If you have not read a file, state it explicitly. Do not guess or make assumptions.

---

## 3. Spec Output Format

All specifications drafted by Antigravity for GitHub Issues must contain the following structured blocks:

* `## Status` — Set to `proposed` (changed to `approved` only after human signoff).
* `## Source of Truth` — List the exact policy, architecture, or design files that govern this area.
* `## Current Behavior` — Detailed description with file:line citations.
* `## Target Behavior` — Detailed specification of the desired changes.
* `## Decision Locks` — Non-negotiable structural or naming constraints.
* `## Risks & Forbidden Outcomes` — Explicit negative expectations (what must fail or be blocked).
* `## Minimum Safe Implementation Order` — Sequenced slices for implementation.
* `## Validation Expectations` — The exact testing commands and required evidence outputs.

---

## 4. PR Drift Review standard
When reviewing a Draft PR opened by a builder:
* Cross-check the PR's file diff against the original approved Implementation Plan.
* Post a PR comment containing:
  ```markdown
  ### Drift Review
  * **Originating Issue:** [Plan Issue URL]
  * **Drift from Plan:** [None | describe differences]
  * **P0 (Blockers):** [List must-fix issues before merge]
  * **P1 (Improvements):** [List minor code/docs improvements]
  * **P2 (Nice-to-have):** [List low-priority observations]
  ```
* Do not attempt to merge. Leave the final merge decision to the human reviewer.
