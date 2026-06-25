# 🌱 OpenTendril

Headless AI coding assistant. Runs locally. Talks to any frontend.

**Prerequisites:** Docker, Go 1.21+, Git — and optionally [Ollama](https://ollama.ai) for free local inference (no API key needed).

> **📖 Before you build:** OpenTendril is built entirely around organic, evolutionary design principles to escape traditional, deterministic IT constraints. Please read [The Tendril Design System](THE-TENDRIL-SYSTEM.md) to understand the architecture and terminology.

---

## 🚀 Quick Start (Fresh Install)

### Step 1 — Clone and install the CLI

```bash
git clone https://github.com/opentendril/core.git
cd core
make install
```

This builds the `tendril` binary and installs it to `~/.local/bin/tendril`.

> Make sure `~/.local/bin` is in your PATH:
> ```bash
> echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc
> ```

### Step 2 — Build the Tendril worker image

```bash
docker compose build tendril
```

This builds the Python AI worker container. Takes ~2 minutes on first run.

### Step 3 — Start the backing services

```bash
docker compose up postgres redis -d
```

Starts the Postgres (memory/vector store) and Redis (event bus) containers.

### Step 4 — Run the setup wizard

```bash
tendril init
```

This will:
- Auto-detect local [Ollama](https://ollama.ai) models (no API key needed!)
- Let you pick a model (auto-selects best `*coder*` model)
- Or configure a cloud provider (Anthropic, OpenAI, Google, etc.)
- Write your config to `.env`

**With Ollama (recommended for local-first):**
```
🌱 Welcome to OpenTendril Setup Wizard!

🔍 Scanning for local LLM providers...
✅ Detected local Ollama with 4 model(s):
  1) llama3.1:8b
  2) qwen3.5:9b
  3) qwen2.5-coder:7b
  4) qwen2.5-coder:14b
Would you like to use Ollama for local, private execution? (y/n)
> y
Auto-selected model: qwen2.5-coder:7b
Press Enter to use it, or type a different model name:

════════════════════════════════════════
  🎉 OpenTendril Setup Complete!
════════════════════════════════════════
  Provider : Ollama (local, private)
  Model    : qwen2.5-coder:7b
  URL      : http://host.docker.internal:11434/v1

  Next steps:
  1. Start the Stem server:   tendril serve
  2. Chat in a new terminal:  tendril chat
════════════════════════════════════════
```

### Step 5 — Start the Stem server

In one terminal:
```bash
tendril serve
```

### Step 6 — Chat!

In a second terminal:
```bash
tendril chat
```

Type your task and press Enter. The AI will respond using your local Ollama model.

---

## 🏗️ Architecture

```
You
 │
 ▼
tendril chat          ← Terminal chat client
 │  HTTP POST /v1/chat/completions
 ▼
tendril serve         ← Go Stem (port :8080)
 │  Hormonal Trigger checks
 │  docker run core-tendril:latest
 ▼
Python Tendril        ← Ephemeral Docker worker
 │  LangChain agent loop
 ▼
Ollama (host)         ← Local LLM inference
 + Postgres + Redis   ← Memory & event bus
```

**Key design principle:** The Python AI worker is an *ephemeral container* — it boots for each task, runs the agent loop, returns the result, and exits. Zero idle cost.

---

## 🔌 Client Integrations

OpenTendril acts as a headless backend. You can connect it to your favorite developer tools using either the **Model Context Protocol (MCP)** or its **OpenAI-Compatible API**.

### 1. Antigravity & Claude Desktop (via MCP)
MCP allows clients to natively access Tendril's secure sandboxed tools.

Edit your MCP configuration file:
- **Antigravity:** `~/.gemini/config/mcp_config.json`
- **Claude Desktop (Mac):** `~/Library/Application Support/Claude/claude_desktop_config.json`

Add the following configuration:
```json
{
  "mcpServers": {
    "opentendril": {
      "command": "/home/<your-username>/.local/bin/tendril",
      "args": ["mcp"]
    }
  }
}
```
*(Tip: Run `tendril init` to see the exact copy-paste block for your system's paths).*

### 2. Aider & CodexCLI (via OpenAI API)
Command-line coding assistants can use Tendril as their backend LLM provider, benefiting from its local inference and routing.

Make sure `tendril serve` is running, then launch Aider:
```bash
aider --openai-api-base http://localhost:8080/v1 --model openai/tendril
```

### 3. VS Code / Cursor (via OpenAI API)
Point any OpenAI-compatible VS Code extension (like Continue.dev) to your local Stem.

In `continue.dev`'s `config.json`:
```json
{
  "models": [
    {
      "title": "OpenTendril",
      "provider": "openai",
      "model": "tendril",
      "apiBase": "http://localhost:8080/v1",
      "apiKey": "sk-local"
    }
  ]
}
```

### 4. LibreChat / AnythingLLM (Web UI)
If you want a ChatGPT-like web interface, run LibreChat and add a custom endpoint:
- **Base URL:** `http://localhost:8080/v1`
- **API Key:** `sk-local`
- **Model:** `tendril`

---

## ⚙️ Configuration API

Manage Hormonal Triggers and AI personas via the REST API (requires `ADMIN_TOKEN` env var if set):

```bash
# List active security triggers
curl http://localhost:8080/v1/config/triggers

# Upload a new trigger script
curl -X POST http://localhost:8080/v1/config/triggers \
  -F "file=@my-trigger.sh"

# List AI personas
curl http://localhost:8080/v1/config/personas
```

---

## 🛠️ Development Commands

```bash
make install          # Build + install tendril binary
make test-core        # Run Python unit tests
make test-sprout      # Run Go unit tests
make test-all         # Run all tests
docker compose build  # Build all Docker images
docker compose up postgres redis -d  # Start backing services only
```
