# 🌱 Tendril

**Self-hosted AI coding assistant. Point it at any project. Talk to it. It reads, edits, and commits.**

Your LLM keys. Your codebase. Your machine.

---

## Quick Start (5 minutes)

### 1. Clone and configure

```bash
git clone https://github.com/opentendril/core.git
cd core
cp .env.example .env
```

Edit `.env` — add at least one LLM API key:
```bash
# Pick your provider (anthropic recommended for code editing)
ANTHROPIC_API_KEY=sk-ant-...
# or
GROK_API_KEY=xai-...
# or
OPENAI_API_KEY=sk-...

# Required infrastructure passwords
POSTGRES_PASSWORD=pick-something-secure
REDIS_PASSWORD=pick-something-secure
SECRET_KEY=generate-with-openssl-rand-hex-32
SANDBOX_TOKEN=generate-with-openssl-rand-hex-16
```

### 2. Mount your project

In `.env`, point Tendril at your codebase:
```bash
TENDRIL_WORKSPACE_ROOT=/workspace
TENDRIL_PROJECT_PATH=/absolute/path/to/your/project
DEFAULT_LLM_PROVIDER=anthropic
```

### 3. Launch

```bash
docker compose up --build
```

Wait for health checks to pass. Verify:
```bash
curl http://localhost:8080/health
# → {"status":"healthy","version":"0.1.0",...}
```

### 4. Talk to it

**Option A — CLI (recommended)**
```bash
cd cli/
go build -o tendril-cli .
./tendril-cli
```

**Option B — Direct API**
```bash
curl -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"session_id": "my-session", "message": "list all files in the project"}'
```

**Option C — Web UI**

Open http://localhost:8080 in your browser.

### 5. Make your first edit

```
you › add error handling to the main entry point
you › read src/auth.py and add input validation
you › what does this project do? summarize the architecture
you › commit all changes with message "feat: add error handling"
```

Tendril reads your code, generates edits, shows diffs, and commits to git.

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
