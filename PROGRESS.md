# Tendril — Project Progress & State

This file is the "Source of Truth" for the Tendril development sprint. **Stable v0.1.0 landed on 2026-04-10.**

---

## 🚀 Current Milestone: Phase 1 — Stable Kernel (v0.1.0) 🟢 COMPLETE
**Goal:** Enable a robust "External Project Mode" where Tendril can be pointed at any codebase to read, understand, edit, and commit changes with high reliability.

| Workstream | Status | Notes |
|---|---|---|
| **External Project Mode** | 🟢 Complete | Verified on `jurnx` (1,300+ files). Read/Write/Commit works. |
| **Go CLI v0.1.0** | 🟢 Complete | Cross-compiled binaries (linux/darwin) available in release. |
| **Go Gateway** | 🟢 Complete | WebSocket transport for low-latency agent interaction. |
| **Developer Experience** | 🟢 Complete | One-command setup (`docker compose up`) + README guide. |

---

## 🛠️ Sprint Deliverables (Day 1-2)

- [x] **Dynamic System Prompt**: Tendril now surveys external workspaces instead of assuming it's editing itself.
- [x] **Zero-Restriction Mode**: `PROTECTED_FILES` and `SDLC` gates are context-aware (disabled for external projects).
- [x] **Universal File Support**: Expanded editor to support `.go`, `.rs`, `.java`, `.rb`, `.c`, etc.
- [x] **Git safe.directory**: Fixed volume mount ownership issues in Docker.
- [x] **Sandbox Volume Fix**: Resolved OCI runtime mount errors for read-only workspaces.
- [x] **Automated Releases**: GitHub Actions workflow for cross-platform CLI binaries.

---

## 📈 Pulse (Latest Logs)

- **2026-04-11 (Day 2.5):** 🚢 **SHIP-IT:** Tagged `v0.1.0`. README rewritten as a minimal setup guide. Makefile added for CLI builds.
- **2026-04-10 (Day 1):** 🧪 **PROOF OF LIFE:** Successfully pointed Tendril at `jurnx-med-dev`. Tendril listed 1,354 files, identified the tech stack (Go/React/Firestore), and committed a verified change to `README.md`.
- **2026-04-09:** 🎯 **STRATEGIC PIVOT:** Paused Evolution 2 (Multi-agent/Credits/Marketplace) to focus on Evolution 1 (The Stable Kernel). Hardened the `/edit` loop and extracted transport to Go.

---

## 🛑 Blockers & Open Questions

1. **User Feedback:** Awaiting first external developer reports on the v0.1.0 CLI.
2. **Demo Content:** Need to capture high-quality terminal recording for the public launch.

---

## 🔮 Next: Phase 2 — UX & Refinement
- [ ] `tendril init` wizard for interactive configuration.
- [ ] Replan/Retry loop for failed tool calls.
- [ ] Improved context window management for large codebases.
- [ ] Human-in-the-loop diff review via CLI.

## 2026-04-22 — Patcher Bug Hunt & External Project Validation

### What happened
End-to-end tested external project mode (`TENDRIL_PROJECT_PATH` + `TENDRIL_WORKSPACE_ROOT=/workspace`).
- ✅ `list_project_files`, `read_file`, `write_file` all work on mounted external directories
- ✅ File writes persist to host filesystem via Docker volume mount
- ✅ `write_file` was confirmed as the correct primary edit mechanism

### Bug found & fixed: `parse_patch` infinite loop
`apply_code_patch` appeared to "hang" when the LLM (Grok) included bare trailing context lines in its patch output — the standard docstring-insertion format. Root cause was an infinite loop in `parse_patch()`'s UPDATE_FILE_MARKER while block: when `i` was never advanced past unrecognised trailing lines.

**Fix**: `i_before_hunk` guard — if the inner hunk parser consumes nothing, skip the unrecognised line with `i += 1`.

### Tests
- 29/29 patcher tests pass (0.05s)
- 3 regression tests added to `TestRegressionPatterns` pinning the exact Grok patch format

### Commits
- `87915b9` — fix(core): apply_code_patch 30s timeout + PROTECTED_FILES + system prompt
- `8c8d851` — fix(patcher): eliminate infinite loop on trailing non-prefixed lines
