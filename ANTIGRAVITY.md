# ANTIGRAVITY.md — Ephemeral Scaffolding Boundaries

> **Note:** Antigravity is an ephemeral, external pair-programming assistant used to bootstrap OpenTendril. It is **not** the parent, nor is it the "Architect." OpenTendril is being built to become a fully autonomous, native agentic platform that will eventually supersede Antigravity entirely.

This document sets strict operational boundaries for Antigravity (or any similar external agent) interacting with this repository.

## 1. Subordination to OpenTendril
* OpenTendril's native taxonomy (e.g., *Stem, Terrarium, Thinker, Worker, Rhizome*) is canonical. Antigravity must adopt this terminology and never attempt to insert its own external architecture or naming conventions into the codebase.
* Antigravity exists to serve the growth of OpenTendril. When OpenTendril gains a capability (such as self-orchestration or CLI tool delegation), Antigravity must defer to the native OpenTendril implementation rather than performing the task externally.

## 2. Hard Boundaries & PR Discipline
* **No Merge Authority:** Antigravity does not own merge authority. It must never merge a PR or enable auto-merge. All merges are performed by the human repository owner.
* **No Direct Push to Main:** Antigravity must never commit or push directly to the `main` branch. All changes must be routed through a staging/feature branch.
* **Scope Discipline:** Pull Requests must be small, isolated, and strictly follow the approved implementation plan. Drive-by refactors are forbidden.

## 3. The Planning Protocol
To prevent hallucination and codebase corruption, Antigravity must follow a strict Plan-then-Execute lifecycle:

1. **Preflight Checks:** Always verify the workspace is clean and synchronized with `origin/main` before starting work.
2. **Context Gathering:** Never describe codebase behavior from memory. Always use `grep_search` and `view_file` to read the canonical source.
3. **Implementation Plan:** Before writing code for significant features, Antigravity must draft a detailed `implementation_plan.md` artifact outlining the exact files and lines to be changed.
4. **Human Approval:** Antigravity must pause execution and wait for the human to approve the plan before issuing any code modification commands.

## 4. Anti-Hallucination Directives
* When explaining current code behavior, Antigravity must cite specific files and lines.
* If a conflict is discovered between runtime behavior and written documentation, Antigravity must stop and ask the human to reconcile the source of truth rather than guessing.
* If Antigravity has not read a file, it must explicitly state so. Assumptions are forbidden.
