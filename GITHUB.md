# OpenTendril GitHub Operating Rules & Repository Configuration

This document defines how GitHub is utilized for OpenTendril's source control, automated integration pipelines, and branch lifecycles.

---

## 1. The Role of GitHub
GitHub serves as the single source of truth and central orchestrator for OpenTendril's software development lifecycle (SDLC). It is responsible for:
* **Coordination:** Issue tracking, feature requests, and milestone tracking.
* **Continuous Integration (CI):** Running code verification pipelines (linters, compilation checks, unit tests) via GitHub Actions.
* **Deployment Gates:** Enforcing human-in-the-loop review before merging code.
* **Distribution:** Storing container images (GitHub Container Registry) and releasing pre-compiled Go CLI binaries.

---

## 2. Branching Strategy

OpenTendril segregates human development, automated AI edits, and production-stable code using a strict branching model:

```
                  ┌───────────────────────────────┐
                  │          main branch          │◀───┐
                  │    (Protected & Stable)       │    │
                  └──────────────┬────────────────┘    │
                                 │                     │
                  ┌──────────────┴────────────────┐    │ Merge PR
                  │                               │    │ (Human Review
                  ▼                               ▼    │  & CI Passed)
       ┌─────────────────────┐         ┌─────────────────────┐ │
       │    staging/ai-*     │         │       feat/*        │─┘
       │  (AI-Generated Edits│         │ (Human-Developed    │
       │   & Sandbox Tested) │         │  Feature Branch)    │
       └─────────────────────┘         └─────────────────────┘
```

1. **`main`:** The default branch. It must remain stable, compile successfully, and pass all tests at all times. Direct pushes to `main` are strictly disabled.
2. **`staging/ai-[patch-description]-[timestamp]`:** Automatically created by AI builders (like Antigravity or OpenTendril's Root Agent) when performing workspace edits. Edits are tested inside the sandbox and committed here.
3. **`feat/[feature-name]` or `fix/[bug-name]`:** Used by human developers when implementing manual changes or architectural migrations.

---

## 3. Repository Configuration & Branch Protection

To prevent accidental corruption of the stable codebase, the `main` branch on GitHub must be configured with the following protection rules:

* **Require a Pull Request Before Merging:** Direct pushes to `main` are blocked. All changes must arrive via a Pull Request.
* **Require Status Checks to Pass:** The PR cannot be merged unless the GitHub Actions CI workflow (`check-all`) compiles successfully and passes all unit tests and linters.
* **Require Human Approval:** For any AI-generated `staging/*` pull request, at least one human developer must review the code diff and manually click "Merge".
* **Require Signed Commits:** GPG/SSH cryptographic signing is recommended for human commits to verify author identity.
* **Require Linear History:** All pull requests should be merged using **Squash and Merge** or **Rebase and Merge** to maintain a clean, readable git commit history.

---

## 4. Conventional Commits Standard

All commits (human and AI-generated) must follow the [Conventional Commits](https://www.conventionalcommits.org/) specification. This enables automated version bumping and progress changelog generation.

### Commit Format:
```
<type>(<scope>): <description>

[optional body]

[optional footer(s)]
```

### Type Definitions:
* **`feat`:** A new feature (e.g., `feat(mcp): add stdio protocol gateway`).
* **`fix`:** A bug fix (e.g., `fix(sandbox): correct host directory mapping error`).
* **`docs`:** Documentation changes only (e.g., `docs(github): define branching model`).
* **`style`:** Code changes that do not affect the meaning of the code (formatting, white-space).
* **`refactor`:** A code change that neither fixes a bug nor adds a feature (e.g. `refactor(db): migrate to sqlite database`).
* **`test`:** Adding missing tests or correcting existing tests.
* **`chore`:** Changes to the build process, auxiliary tools, or library dependencies.

---

## 5. AI Agent Artifact Management

OpenTendril agents (like Codex, Antigravity, Aider, Cline, or local Planners) generate transient markdown artifacts, logs, and plans to coordinate progress.

**CRITICAL RULE:** Transient artifacts must **NEVER** be committed to the repository. 
* All AI tools and agents MUST create and store their workspace files inside a dedicated `.ai/`, `.scratch/`, or `.tendril/scratch/` directory.
* These directories and common artifact names (e.g. `task.md`, `walkthrough.md`, `.aider*`) are ignored in `.gitignore`.
* Instead of committing these files, agents MUST extract their valuable contents and inject them directly into **GitHub Pull Request Descriptions** or **Issue Comments** using MCP GitHub tools.
* This ensures rich historical context remains on GitHub where it belongs, rather than cluttering the git tree.
