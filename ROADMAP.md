# Tendril Roadmap & Evolution

*Our vision: Turn your frustrations into skills, automatically.*

Tendril is evolving from a local **Root Ramet** that builds itself, into a globally distributed registry of agentic skills. This roadmap tracks the high-level paradigms driving the project.

## Evolution 1: The Root Ramet (Current State)
**Focus:** Self-healing, self-building, and creating the "Moat."
Tendril points its terrarium at its own source code (`opentendril/opentendril`).
- ✅ **Multi-LLM Routing:** Tiered reasoning (Grok, Gemini, Claude, local models).
- ✅ **Approval Gate:** Human-in-the-loop security for file edits.
- ✅ **The `/edit` Endpoint Loop:** The core moat. Generate → Syntax Check → Auto-Commit → Chronicle.
- 🟡 **The Chronicler:** Marketing-focused logs of what Tendril is building (In Progress).

## Evolution 2: Deployed Ramets (Stable v0.1.0) 🟢
**Focus:** Pointing Tendril at external codebases.
Tendril instances run as sidecars or tools on any codebase (e.g., `jurnx/med-api`).
- ✅ **External Project Mode:** Native mounting of any repo via `TENDRIL_PROJECT_PATH`.
- ✅ **Dynamic Workspace Awareness:** LLM surveys and understands project context on the fly.
- ✅ **Tool Adaptability:** File operations and Git committed directly to the external workspace.
- ✅ **Enterprise Pipelines:** Native "Design → Plan → Pull Request" workflows (Trinity-style Thinker-Worker-Verifier sequence conductor).
- [ ] **Monetization & Credits:** The `credit_manager` enforcing limits on cloud-hosted usage, allowing unlimited local compute.
- ✅ **MCP Server Integration:** Exposing the Headless Kernel via the Model Context Protocol for Claude Desktop and Cursor compatibility.
- ✅ **Shadow Workflows:** Ephemeral git shadow worktrees, pre-flight stashing, and automated validation gates for safe, isolated AI iterations.
- ✅ **Phenotypic Selection:** Speculative parallel execution of sprouts under varying temperatures and plasmids, resolving winner branches via docker fitness check gates.


## Evolution 3: The Automated Hive (Distributed Registry)
**Focus:** Sharing skills learned by deployed Ramets back to the Root.
- [ ] **Skill Abstraction:** The Dreamer detects when a deployed Tendril solves a novel problem (e.g., HL7 parsing).
- [ ] **Automated PRs:** Deployed instances create Pull Requests against the global `opentendril/registry` repository.
- [ ] **Federated Learning:** Tendril instances globally sync approved `.skill.json` files from the registry on startup.
