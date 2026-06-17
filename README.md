# 🌱 Tendril

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Build Status](https://img.shields.io/badge/build-passing-brightgreen.svg)](#)
[![Stable Release](https://img.shields.io/badge/release-v0.1.0--alpha-blue.svg)](#)
[![Discord](https://img.shields.io/badge/discord-join-7289da.svg?logo=discord&logoColor=white)](https://discord.gg/opentendril)

**Headless AI coding assistant kernel. Point it at any project. Talk to it from any CLI or web app. It reads, edits, and commits safely inside a secure sandbox.**

Your LLM keys. Your codebase. Your machine.

---

## The Vision: A Zero-Friction, Headless Kernel

Tendril is designed as an **Intelligent Backend Protocol** (a Headless Kernel) that seamlessly powers the best developer frontends in the open-source ecosystem. Rather than locking you into a custom editor or a complex chat UI, Tendril runs as a lightweight local daemon that exposes:
1. **OpenAI-Compatible API Endpoint** (`/v1/chat-completions`) — point Aider, Continue, or custom dashboards to it.
2. **Model Context Protocol (MCP) stdio Server** — connect Claude Desktop, ChatGPT CLI, or any MCP client directly to access Tendril's secure sandboxed tools.

---

## Quick Start (Under 2 Minutes)

Get up and running locally with zero configuration friction.

### 1. Run the Installer
Run the single-line installation script to download the Go gateway CLI and boot the backend daemon:
```bash
curl -fsSL https://opentendril.com/install.sh | sh
```

### 2. Choose Your Onboarding Pathway
On the first boot, Tendril will automatically assist you in selecting your runtime mode:
* **Option A: Free Cloud Trial (No Key):** Instantly get started using our hosted gateway (`api.opentendril.com`). Routes requests anonymously to top-tier models (Claude 3.5 Sonnet / Gemini 1.5 Pro) using server-side trial credits. No signup or credit cards required.
* **Option B: Auto-Ollama Detection (Local-First):** If Ollama is running locally on `localhost:11434`, Tendril automatically detects it and boots using your local coding models (e.g. `qwen2.5-coder`).
* **Option C: Custom API Keys:** Provide your own Anthropic, OpenAI, or Google API keys directly in the CLI prompt.

---

## Connectivity & Interfaces

Once the backend is live, you can connect your preferred interface:

| Interface / Client | Connection Type | Command / Setup |
|---|---|---|
| **Claude Desktop** | MCP (stdio) | Add `tendril-cli -mcp` to your `claude_desktop_config.json` |
| **Aider** | OpenAI API | `aider --openai-api-base http://localhost:8080/v1 --model openai/tendril` |
| **LibreChat (Web UI)** | OpenAI API | Point LibreChat's custom endpoints to `http://localhost:8080/v1` |
| **Interactive CLI** | WebSocket | Run `tendril-cli` in your terminal |

---

## How It Works (Decoupled Microservices)

```
                 ┌────────────────────────────────────────────────────────┐
                 │                       CLIENTS                          │
                 │   LibreChat (Web)  │  Cursor / VSCode  │  Claude CLI   │
                 └───────────────────────────┬────────────────────────────┘
                                             │ (MCP over stdio / SSE)
                                             ▼
                 ┌────────────────────────────────────────────────────────┐
                 │                 LIGHTWEIGHT GO GATEWAY                 │
                 │   `tendril-cli -mcp` (Instant boot, proxy routing)     │
                 └──────┬────────────────────┬─────────────────────┬──────┘
                        │                    │                     │
                        ▼                    ▼                     ▼
             ┌─────────────────────┐ ┌──────────────┐   ┌─────────────────────┐
             │    SANDBOX CORE     │ │  MEMORY/RAG  │   │     LLM ROUTER      │
             │   (Python/Docker)   │ │ (SQLite/MCP) │   │ (Ollama/Cloud/vLLM) │
             └─────────────────────┘ └──────────────┘   └─────────────────────┘
```

* **Go Gateway Proxy:** Handles incoming client protocols (stdio, HTTP, WebSocket) and routes them efficiently with sub-millisecond latencies.
* **Sandbox Core:** Executes code edits, syntax validation, compile checks, and git operations inside an isolated container sandbox (standard Docker, gVisor, or Firecracker).
* **Zero-Dependency Fallbacks:** For lightweight local execution on low-spec hardware:
  * Uses **SQLite** instead of Postgres if Postgres is offline.
  * Uses **in-memory arrays** instead of Redis if Redis is offline.
  * Runs **Docker-free** (direct host execution) if containerization is disabled.
* **Pluggable Memory:** Run memory-free for small codebases, use local vector stores, or delegate to external semantic indexers (like `codebase-mcp`).

---

## Self-Building Mode (The Root Agent)

When `TENDRIL_PROJECT_PATH` is not configured, Tendril operates on its own source code (the Root Agent mode). It self-heals and self-modifies through a secure staging pipeline: applies surgical patches $\rightarrow$ compiles $\rightarrow$ runs tests $\rightarrow$ commits to a staging branch $\rightarrow$ creates a GitHub Pull Request for human review.

---

## 💬 Community & Support

* **Discord Server:** Join the [OpenTendril Discord](https://discord.gg/opentendril) to chat with other developers, share custom skills, and get help.
* **GitHub Issues:** For bug reports, feature requests, or RFC design proposals, use our [Issue Templates](.github/ISSUE_TEMPLATE/).
* **Contributing:** Check out [CONTRIBUTING.md](CONTRIBUTING.md) to get started on hacking Tendril.

---

## License

MIT — Build freely. Scale with us.
