# Tendril — Project Progress & State

This file is the "Source of Truth" for all AI agents working on Tendril. **Update this file after every major milestone or configuration change.**

---

## 🚀 Current Milestone: Phase 3 — Enterprise Memory & Testing
**Goal:** Enhance the Dreamer loop, persist RAG memory securely for multi-tenant scalability, and integrate the TestRunner for automated CI-level feedback during self-edits.

| Workstream | Lead Agent | Status | Notes |
|---|---|---|---|
| **Kernel Core** | Antigravity | 🟢 Active | Phase 1 & 2 complete. Starting Phase 3 (Memory / Testing). |
| **Branding** | Antigravity | 🟢 Complete | Name/Logo/Colors locked in. UI updated. |
| **Marketing** | Antigravity | 🟡 Waitlist | Waitlist API live. Awaiting external landing page design. |

---

## 🛠️ Active Tasks

- [x] **Phase 1 & 2:** Chronicler, `/status`, Unified Credits architecture, and Waitlist API.
- [x] **Feature:** Secure TestRunner Integration (Automated quality gates for `/edit`).
- [x] **Feature:** Sandbox Isolation via HTTP Relay (inspired by OpenClaw, improved security).
- [x] **Feature:** Multi-tenant RAG (Session-isolated vector search + cookie-based sessions).
- [x] **Feature:** The "Dreamer" Loop — state tracking, UI widget, manual trigger, API visibility.

---

## 📈 Recent Pulse (Changelog)

- **2026-04-09:** Three core improvements: Event Bus (structured observability w/ Redis), Model Failover (exponential backoff across 4 providers), Surgical Patcher (multi-file patches w/ validation).
- **2026-04-09:** Frontend extracted from main.py into static files (index.html, styles.css, app.js). main.py reduced from 1237 to 644 lines.
- **2026-04-09:** Fixed Claude integration: LLM Router now uses `ChatAnthropic` for Claude (was silently broken using `ChatOpenAI`). Added `langchain-anthropic` dependency.
- **2026-04-09:** Dreamer Loop visibility: DreamerState tracker, sidebar widget with HTMX polling, /dreamer/status API, manual trigger button.
- **2026-04-09:** Multi-tenant memory isolation: cookie-based sessions, filtered RAG retrieval, per-user conversation history.
- **2026-04-09:** Implemented Sandbox Isolation: `Dockerfile.sandbox`, `sandboxserver.py` HTTP relay, internal-only Docker network. TestRunner now routes through isolated container.
- **2026-04-09:** Implemented SDLC Pipeline: Ruff linting + py_compile + pytest with auto-rollback on failure.
- **2026-04-09:** Code review of OpenClaw architecture. Documented learnings in `DECISIONS.md` (Decision #10).
- **2026-04-08:** Refactored project to "No Underscore" naming convention and merged Python modules.
- **2026-04-08:** Integrated "Direction B" logo and "Root Agent" brand with Lobster Red accents.
- **2026-04-08:** Secured `opentendril` namespace on PyPI and NPM.
- **2026-04-08:** Implemented Unified Credit System and Chronicler Service
- **2026-04-08:** Documented future scalability paths and meta-awareness files in `ARCHITECTURE.md`.
- **2026-04-08:** Completed Phase 0: Security fixes, XSS hardening, and async scheduler.
- **2026-04-08:** Initial Master Roadmap established in Conversation `ab7ff...`
- **2026-04-08:** Created `PROGRESS.md` for cross-conversation coordination.

---

## 🛑 Blockers & Open Questions

1. **Claude Key:** Confirmed provided.
