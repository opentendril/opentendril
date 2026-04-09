# Tendril — Strategic Decision Log (Kernel Memory)

This document records the "Why" behind the Tendril Kernel's evolution. It is a primary reference for all AI agents (including the Root Agent itself) to maintain strategic alignment.

---

## 🏗️ Architectural Decisions

### 1. Security-First Foundation (2026-04-08)
- **Decision:** All self-building operations must be sandboxed. Restricted files (like `.env`) are explicitly blocked from AI modification.
- **Rationale:** To position Tendril as enterprise-grade, we prioritize system integrity over total autonomy. Security is a marketing differentiator.

### 2. Async-First Scheduling (2026-04-08)
- **Decision:** Migrated from `BackgroundScheduler` to `AsyncIOScheduler`.
- **Rationale:** Aligns with FastAPI's event loop to prevent blocking threads during "Dream" cycles and long-running agent tasks.

### 3. Workspace-Centric Operations (2026-04-08)
- **Decision:** Replaced `SRC_DIR` with `WORKSPACE_ROOT` as the primary editor boundary.
- **Rationale:** Allows Tendril to modify its own configuration, Docker files, and root-level documentation, not just code inside `src/`. Essential for a true "Root Agent."

---

## 🎨 Branding & Identity

### 4. Transition to "The Root Agent" (2026-04-08)
- **Decision:** Rebranded from "Tendril Core" to "The Root Agent."
- **Note:** Positioned as the successor to OpenClaw. The branding uses "Lobster Red" accents (`#ef4444`) as a nod to its legacy.
- **Rationale:** Moves the narrative from a "tool" to a "kernel"—the agent that builds agents.

### 5. Visual Identity Selection (2026-04-08)
- **Decision:** Selected "Direction B" (Abstract Network) as the official logo and core visual motif.
- **Rationale:** Represents the interconnected nature of the orchestration kernel.

### 6. Technical Namespace Strategy (2026-04-08)
- **Decision:** Adopted `opentendril` as the namespace for GitHub, PyPI, and NPM, while keeping the product name as "Tendril."
- **Rationale:** Ensures unique brand ownership and avoids collision with generic "Tendril" software projects.

---

## 💰 Business & SaaS Strategy

### 4. Managed SaaS via Unified Credits (2026-04-08)
- **Decision:** Implementation of a `CreditManager` supporting "Local" vs "SaaS" modes. 
- **Rationale:** Allows for a viral "BYO Keys" local version while providing a friction-less path to paid cloud compute.

---

## 🤖 Autonomous Evolution

### 8. The Chronicler Feature (2026-04-08)
- **Decision:** Tendril is required to document its own progress in `PROGRESS.md` upon every git commit.
- **Rationale:** Transparency for "Build in Public" marketing and to provide a persistent context state for future agent sessions.

---

## 🛠️ Developer Experience (DX)

### 9. The "No Underscore" Convention (2026-04-08)
- **Decision:** Eliminate underscores from all project filenames.
- **Rules:** 
    - Python modules: merged lowercase (`llmrouter.py`).
    - Non-Python files: kebab-case (`docker-compose.yml`).
    - Internal code: snake_case (PEP 8).
- **Rationale:** Differentiates the brand with a clean, opinionated filesystem aesthetic and aligns with modern JS/Go naming patterns.

### 10. Sandbox Isolation via HTTP Relay (2026-04-09)
- **Decision:** All code execution (linting, testing, user commands) runs in an isolated `sandbox` container accessible only via an authenticated HTTP relay. Rejected Docker socket (`docker exec`) approach.
- **Rationale:** Docker socket grants host-level control — the exact vulnerability we're trying to prevent. The HTTP relay adds ~50 lines of code but provides zero-trust isolation: no API keys, no DB access, no internet, hard resource limits. OpenClaw uses `docker exec`; we chose to go further because "if the desire is a secure system, we build the most secure method from day one."
- **Trade-off:** Slightly more complex than `docker exec`, but production-correct from the start. No security shortcuts to migrate away from later.

### 11. Native Provider Adapters in LLM Router (2026-04-09)
- **Decision:** Use provider-native LangChain adapters (`ChatAnthropic` for Claude, `ChatOpenAI` for OpenAI-compatible APIs) instead of routing everything through `ChatOpenAI`.
- **Rationale:** Anthropic's API is NOT OpenAI-compatible — it uses `/messages`, not `/chat/completions`. The original router silently failed for Claude. Each provider type now has its own adapter, selected via a `type` field in the provider config.
- **Impact:** This was a **blocking bug** for the Claude MVP. With this fix, switching `DEFAULT_LLM_PROVIDER=anthropic` in `.env` will route all orchestrator requests through Claude.

### 12. Frontend Extraction — Static Files (2026-04-09)
- **Decision:** Extract all HTML/CSS/JS from `main.py` into `static/index.html`, `static/styles.css`, `static/app.js`. Provider options loaded dynamically via `GET /api/providers` instead of server-side string replacement.
- **Rationale:** `main.py` was 1237 lines with 600 lines of embedded frontend. This made it impossible to iterate on API vs UI independently, and blocked future Go migration. After extraction: `main.py` = 644 lines (pure API).
- **Impact:** Clean API/frontend boundary. Frontend is now a static SPA that can be served by any HTTP server (nginx, Go, CDN).

### 13. Go as Future API Gateway (2026-04-09)
- **Decision:** Target architecture is Go for the API gateway (routing, SSE, auth, static serving) with Python as the LLM worker (LangChain, embeddings). Motivated by speed, energy efficiency, and single-binary deployment.
- **Rationale:** Go idles at ~5MB RAM vs Python's ~50MB+. For a SaaS platform serving many tenants, this is the difference between $50/mo and $500/mo in compute. Python stays for ML-specific code where the ecosystem is unmatched.
- **Status:** Not yet implemented — frontend extraction (Decision #12) is the prerequisite. Migration can happen endpoint-by-endpoint.

### 14. Centralized Event Bus (2026-04-09)
- **Decision:** All subsystems emit structured `TendrilEvent` objects through a centralized `EventBus` singleton. Events are persisted in Redis (24h TTL, 500/session) and queryable via `/events/{session_id}`.
- **Rationale:** OpenClaw scatters `emitAgentEvent()` calls with no persistence or queryability. Tendril's bus provides: structured JSON logs, Redis history, subscriber callbacks for SSE, and run-ID correlation across all events.
- **Impact:** Foundation for failover observability, SDLC tracking, live streaming dashboard, and Dreamer pattern analysis.

### 15. Resilient Model Failover with Exponential Backoff (2026-04-09)
- **Decision:** Replace direct `LLMRouter.get().invoke()` with `ModelFailover.invoke_with_failover()`. Per-provider health tracking with exponential backoff (5s → 10s → 20s → 60s → 300s), cost-aware candidate ordering, and full event bus integration.
- **Rationale:** OpenClaw uses fixed 30s probe intervals with no cost awareness or learning. With 4 active providers, silent failover prevents user-facing errors when any provider is rate-limited, overloaded, or down.
- **Impact:** Users never see "provider unavailable" errors. The system self-heals by routing around failed providers and recovering automatically.

### 16. Surgical Patch Format with SDLC Integration (2026-04-09)
- **Decision:** Add `apply_code_patch` tool using a structured `*** Begin Patch / *** End Patch` format supporting multi-file updates, additions, and deletions. Pre-apply validation ensures paths exist and contexts match.
- **Rationale:** OpenClaw's format lacks validation and SDLC integration. Tendril validates patches before applying, emits events for every operation, and keeps `write_file` as a fallback for simple edits.
- **Impact:** LLM can express surgical, multi-file edits efficiently without outputting entire file contents. 10x token savings on large files.
