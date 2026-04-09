# Tendril Project Guardrails

This document defines the "Laws of the Kernel." All contributors (human and AI) must adhere to these guardrails to maintain the structural integrity and brand identity of the Root Agent.

---

## 🏗️ Naming Conventions

### 1. Filesystem (The "No Underscore" Rule)
- **Python Modules:** Must use **merged lowercase** only. No underscores, no hyphens.
    - ✅ `llmrouter.py`, `skillsmanager.py`
    - ❌ `llm_router.py`, `LlmRouter.py`, `llm-router.py`
- **Directories & Non-Code Files:** Must use **kebab-case**.
    - ✅ `docker-compose.yml`, `dynamic-skills/`, `assets/`
    - ❌ `docker_compose.yml`, `DynamicSkills/`
- **Exceptions:** Reserved Python files like `__init__.py` or third-party config files that require specific naming (e.g. `.env.example`).

### 2. Internal Code
- **General:** Follow **PEP 8** (snake_case) for functions and variables.
- **Classes:** Use **PascalCase**.
    - ✅ `class Orchestrator:`, `class LLMRouter:`

---

## 🎨 Brand Identity

### 3. The Root Agent
- Always refer to the product as **Tendril** and its persona as **The Root Agent**.
- The primary motif is the **Abstract Network** (Direction B).
- **Colors:**
    - Primary Accent: **Bioluminescent Green** (`#10b981`).
    - Heritage Accent: **Lobster Red** (`#ef4444`) used for buttons/critical actions as a nod to OpenClaw.

---

## 🔒 Security & Integrity

### 4. Sandboxing
- Tendril is a **self-building** system. Any code modification via `/edit` must be performed on files within the `WORKSPACE_ROOT`.
- Sensitive files (e.g., `.env`, `data/stubs/`, `venv/`) are **protected**. Do not allow the orchestrator to modify these files without explicit human-in-the-loop approval.

---

## 📚 Documentation Governance

### 5. Meta-Awareness
- No major feature, architectural change, or branding shift exists until it is recorded in `DECISIONS.md`.
- Every significant milestone or session summary must be recorded in `PROGRESS.md`.
- Technical blueprints must be maintained in `ARCHITECTURE.md`.

---

## 🔀 Source Control (SDLC Discipline)

### 6. Commit Cadence
- **Atomic commits** after each logical unit of work. Never batch more than one phase or feature into a single commit.
- **Commit BEFORE testing.** If a live test corrupts files (as happened 2026-04-09 when an LLM `write_file` destroyed `main.py`), the last commit is the recovery point.
- Commit message format: `type(scope): description` using [Conventional Commits](https://www.conventionalcommits.org/).
  - ✅ `feat(failover): add exponential backoff with cost-aware routing`
  - ✅ `fix(llmrouter): correct Claude model names (dots → hyphens)`
  - ❌ `update stuff`
- **Push after every commit session.** Local-only commits are not backups.

### 7. Protected Files
The following files must **never** be modified by the orchestrator's `write_file` tool during a chat session:
- `src/main.py` — Core application server
- `src/tendril.py` — Orchestrator (self-modification = corruption risk)
- `src/config.py` — Configuration
- `.env` — Secrets
- `GUARDRAILS.md`, `DECISIONS.md` — Governance documents

These files should only be modified via the `/edit` endpoint (which has SDLC gates) or by human developers.

### 8. Branch Strategy (Future)
- `main` — Stable, deployable at all times
- `feat/*` — Feature branches for multi-session work
- PRs required for any changes that modify more than 5 files

---

> [!IMPORTANT]
> Failure to follow these guardrails leads to technical debt and brand dilution. If an AI agent (including the Root Agent) detects a violation, it is authorized to pause and request a refactor.
