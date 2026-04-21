# 🌱 Tendril

**Self-hosted AI coding assistant. Point it at any project. Talk to it. It reads, edits, and commits.**

Your LLM keys. Your codebase. Your machine.

---

## Quick Start

> **[→ Full setup guide with Hello World verification: QUICKSTART.md](./QUICKSTART.md)**

```bash
git clone https://github.com/opentendril/core.git
cd core && cp .env.example .env   # Add your LLM API key
docker compose up --build
```

Verify it's live:
```bash
curl -s http://localhost:8080/health | python3 -m json.tool
# → {"status": "healthy", ...}
```

Then connect — pick your preferred interface:

| Interface | Command |
|---|---|
| **curl** | `curl -X POST http://localhost:8080/v1/chat -H 'Content-Type: application/json' -d '{"session_id":"test","message":"What files are in this project?"}'` |
| **CLI** | `cd cli && go build -o tendril-cli . && ./tendril-cli` |
| **Web UI** | Open `http://localhost:8080/chat` |

See [QUICKSTART.md](./QUICKSTART.md) for full setup, troubleshooting, and first-edit walkthrough.

---

## Ecosystem Integrations (The Headless Kernel)

Tendril is designed as an **Intelligent Backend Protocol** (a Headless Kernel) that seamlessly powers the best Terminal UIs and agents in the open-source ecosystem. 

Because Tendril exposes a native **OpenAI-Compatible API Endpoint** (`/v1/chat/completions`), you can point your favorite third-party tool at your local Tendril Brain. Tendril will execute the edits autonomously in its sandbox, while streaming a continuous live narrative of its thoughts and actions back to your TUI of choice!

### Supported Frontends

#### 1. Aider
The gold standard for Git-native code editing.
```bash
export OPENAI_API_BASE="http://localhost:8080/v1"
export OPENAI_API_KEY="tendril"
aider --model openai/tendril
```

#### 2. Crush
For a glamorous, blazing fast Terminal UI experience.
```bash
export OPENAI_API_BASE="http://localhost:8080/v1"
export OPENAI_API_KEY="tendril"
crush --model openai/tendril
```

#### 3. OpenCode
For Neovim-centric, highly customizable agenting. Point OpenCode's custom provider configuration to `http://localhost:8080/v1`.

---

## How It Works

```
┌──────────────┐     ┌──────────────────┐     ┌──────────────────┐
│  CLI (Go)    │────►│  Gateway (Go)    │────►│  Brain (Python)  │
│  WebSocket   │ ws  │  :9090/ws        │ HTTP│  :8080/v1/chat   │
└──────────────┘     └──────────────────┘     └──────┬───────────┘
                                                     │ LangChain Tools:
                                                     ├─ read_file
                                                     ├─ write_file
                                                     ├─ apply_code_patch
                                                     ├─ search_project
                                                     ├─ list_project_files
                                                     ├─ run_bash_command
                                                     ├─ git_commit
                                                     └─ git_status
```

- **Your project** is mounted read-write at `/workspace` inside the container
- **The LLM** (Claude, Grok, GPT-4, or local via vLLM) processes your request
- **Tools** execute against your mounted codebase — file reads, writes, git operations
- **Everything stays local** — no code leaves your machine except to the LLM API

## Supported LLM Providers

| Provider | Env Var | Best For |
|----------|---------|----------|
| **Anthropic** (Claude) | `ANTHROPIC_API_KEY` | Code editing, tool use — recommended |
| **xAI** (Grok) | `GROK_API_KEY` | Fast, good value |
| **OpenAI** (GPT-4) | `OPENAI_API_KEY` | General purpose |
| **Google** (Gemini) | `GOOGLE_API_KEY` | Multimodal |
| **Local** (vLLM) | Uncomment in `docker-compose.yml` | Air-gapped, GPU required |

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/health` | System health + loaded providers |
| `POST` | `/v1/chat` | Send a message (JSON API) |
| `POST` | `/edit` | Self-building: edit files via LLM |
| `GET`  | `/chat` | Web chat UI |
| `GET`  | `/api/providers` | List available LLM providers |
| `GET`  | `/health/providers` | Provider health + cooldown status |

## GPU Inference (Optional)

If you have an NVIDIA GPU, uncomment the `inference` service in `docker-compose.yml` to run models locally via vLLM. No API keys needed.

## Self-Building Mode

When `TENDRIL_PROJECT_PATH` is not set, Tendril operates on its own source code — the "Root Agent" mode. It can modify itself through a staged edit pipeline with syntax validation, branch-per-change, and PR creation.

## License

MIT — Build freely. Scale with us.
