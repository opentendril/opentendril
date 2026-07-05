# Tendril Feature Requests & Backlog

This document tracks planned improvements and strategic shifts for the OpenTendril kernel.

## 🏗️ Modular Infrastructure (Options for Everything)

To ensure Tendril remains infrastructure-agnostic and scalable, we are moving toward a provider-based architecture for all core services.

### [ARCH] LLM Universal Adapter (OpenRouter)
- **Status:** Backlog ([Issue #7](https://github.com/opentendril/core/issues/7))
- **Goal:** Support any model via `openrouter.ai` to provide maximum reasoning flexibility.
- **Priority:** High (Evolution 1/2)

### [ARCH] Swappable Vector Memory (Pinecone/Weaviate)
- **Status:** Backlog ([Issue #8](https://github.com/opentendril/core/issues/8))
- **Goal:** Enable scaling to multi-tenant SaaS and massive codebases by allowing external managed vector stores.
- **Priority:** Medium (Evolution 2)

### [ARCH] KVStore Abstraction (Redis/Upstash/In-Memory)
- **Status:** Backlog ([Issue #9](https://github.com/opentendril/core/issues/9))
- **Goal:** Decouple from local Redis containers to support serverless deployments and zero-dependency local runs.
- **Priority:** Medium (Evolution 2)

## 🚀 Advanced Evolution & Optimization

### [ARCH] Hardware-Level Isolation (Firecracker/gVisor)
- **Status:** Backlog ([Issue #10](https://github.com/opentendril/core/issues/10))
- **Goal:** Shift from Docker to microVMs for ultra-secure, multi-tenant code execution.
- **Priority:** High (Enterprise)

### [PERF] Speculative Multi-Model Orchestration
- **Status:** Backlog ([Issue #11](https://github.com/opentendril/core/issues/11))
- **Goal:** Use "Judge/Worker" tiers (e.g. Gemini Flash + Claude Opus) to speed up iterations and reduce costs.
- **Priority:** Medium

### [RAG] Project Context Distillation (Context Stubs)
- **Status:** Backlog ([Issue #12](https://github.com/opentendril/core/issues/12))
- **Goal:** Maintain hyper-compressed project maps for better long-context performance.
- **Priority:** High

### [SRE] Self-Healing Infrastructure (Auto-SRE)
- **Status:** Backlog ([Issue #13](https://github.com/opentendril/core/issues/13))
- **Goal:** Autonomously repair environment issues (OOMs, crashes) via the EventBus.
- **Priority:** Low

### [UX] Thought-Stream Visualizer
- **Status:** Backlog ([Issue #14](https://github.com/opentendril/core/issues/14))
- **Goal:** Real-time visual graph of the agent's chain-of-thought for transparency and trust.
- **Priority:** Medium

## 🧠 Cognitive Upgrades (The Orchestrator Evolution)

### [ARCH] Complexity Assessor: Automated Task Triaging
- **Status:** Backlog ([Issue #15](https://github.com/opentendril/core/issues/15))
- **Goal:** Automatically route tasks to the most efficient LLM tier via a "Gating Model."
- **Priority:** High

### [RAG] Infinite Project Memory (Cross-Session RAG)
- **Status:** Backlog ([Issue #16](https://github.com/opentendril/core/issues/16))
- **Goal:** Maintain long-term project evolution context using metadata filtering in external DBs.
- **Priority:** Medium

### [PERF] Native Prompt Caching Integration
- **Status:** Backlog ([Issue #17](https://github.com/opentendril/core/issues/17))
- **Goal:** Leverage Anthropic/Gemini/DeepSeek caching to slash costs and latency in multi-turn sessions.
- **Priority:** High

### [AGENT] Ephemeral Sub-Agent Orchestration
- **Status:** Backlog ([Issue #18](https://github.com/opentendril/core/issues/18))
- **Goal:** Initiate specialized worker agents (Security, Linter, Docs) for parallel, expert execution.
- **Priority:** High

## 🌐 Ecosystem & Workflow Enhancements

### [ARCH] Model Context Protocol (MCP) Server Endpoint
- **Status:** Backlog
- **Goal:** Expose Tendril's toolset via MCP as a **new use-case**. While Tendril maintains its own autonomous "brain", this endpoint allows an external LLM (e.g. Claude Desktop) to delegate tasks to Tendril's secondary brain, turning Tendril into a secure, reliable executor for external workflows.
- **Priority:** High

### [AGENT] Robust Git & "Shadow" Branching Capability
- **Status:** Backlog
- **Goal:** Fix challenges in the self-improvement loop by iterating in an isolated "Shadow Git" state (`.codebase` or isolated branches). OpenTendril will safely do the work locally and automatically commit to a branch, only merging when tests pass, ensuring it never corrupts its own active runtime.
- **Priority:** High

### [RAG] Symbol-Level Local Indexing
- **Status:** Backlog
- **Goal:** Implement FAISS + local embeddings for symbol-level (functions, classes) semantic search to accelerate context retrieval.
- **Priority:** Medium

### [MEMORY] Persistent Cognitive Memory
- **Status:** Backlog
- **Goal:** Categorize learnings into progress, mistakes, and architecture, with pre-action semantic lookups to prevent repeating errors across sessions.
- **Priority:** High

---

## 🤖 Evolution 2 & 3 Items
*Items being tracked in the [ROADMAP.md](./ROADMAP.md).*

- [ ] **Enterprise Pipelines:** Native PR-based workflows.
- [ ] **Skill Abstraction:** Automatic skill detection and registry sync.
- [ ] **Distributed Hive:** Federated learning across nodes.
