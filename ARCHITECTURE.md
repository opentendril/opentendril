# Tendril Architecture & Scalability Roadmap

This document serves as the target state and architectural blueprint for OpenTendril. As the Tendril kernel self-modifies and grows, this file acts as a reference for how components should evolve.

## 1. Current State (Phase 1)
- **Pattern:** Modular Monolith
- **Language:** Python (FastAPI)
- **Deployment:** Docker Compose (local environment)
- **Data & State:** Internal Postgres (`pgvector`) & Redis containers.

The current setup maximizes developer velocity and allows rapid integration of LLM tooling for the self-building `/edit` cycle.

## 2. Future Scalability Triggers

When Tendril identifies performance bottlenecks or when preparing for Enterprise SaaS deployment, the system is authorized to transition towards the following patterns:

### A. Infrastructure Evolution (SaaS / Enterprise)
- **Container Orchestration:** Migrate from Docker Compose to Kubernetes (Helm charts) for high availability.
- **Externalized Services:** Transition away from internal containers to managed cloud services (e.g., AWS RDS for Postgres, ElastiCache for Redis) to ensure data persistence, backups, and scalability.

### B. Polyglot Microservices
Python will likely remain the orchestrator to leverage AI libraries, but high-throughput components should be abstracted:
- **I/O Bottlenecks:** Memory retrieval (RAG), vector searches, or high-volume logging can be spun out into microservices written in **Go** or **Rust**.
- **Code Execution:** Untrusted code built by Tendril should eventually run in segregated Rust-based WebAssembly (Wasm) or Firecracker microVM sandboxes rather than direct subprocesses.

### C. Headless Kernel & Ecosystem Integrations
As the core orchestrator becomes increasingly robust, Tendril should embrace a "Headless Kernel" pattern:
- **Ecosystem Integration:** Expose an OpenAI-compatible SSE streaming API (`/v1/chat/completions`) that enables external tools like Aider, OpenCode, and Crush to connect directly to Tendril.
- **Web App:** Extract the UI from FastAPI logic into a dedicated **TypeScript + React / Next.js** dashboard for observability and agent configuration rather than purely chat.
- **Local Native:** For local users who want a "1-click" experience outside of Docker, the Python backend and web frontend can be bundled into a desktop application via **Tauri (Rust)** or **Electron**.

## 3. Documentation Governance
To maintain meta-awareness of its own development, Tendril relies on a suite of living documents. Agents working in this repository must reference and update these files:
- **`PROGRESS.md`:** The "Source of Truth" for current active tasks, immediate sprint status, and the recent changelog.
- **`ARCHITECTURE.md`:** (This file) Long-term structural design and scaling pathways.
- **`DECISIONS.md`:** (Strategic Memory) Records the "Why" behind major architectural and brand shifts.
- **`GUARDRAILS.md`:** (Project Constitution) Codifies mandatory naming conventions, brand constraints, and security standards.
- **`FEATURE_REQUESTS.md`:** (Planned) A structured backlog for community or internal requests.

*Note to AI agents modifying this repository: Before decoupling components or shifting infrastructure, refer to this document. Ensure that all major changes are validated via `test_runner.py` before finalizing.*
