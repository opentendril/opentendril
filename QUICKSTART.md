# Tendril Quick Start — "Hello World" in 5 Minutes

This guide gets you from zero to a verified, working Tendril kernel.
By the end you will have confirmed the system is live and can respond to a prompt.

---

## Prerequisites

| Requirement | Check |
|---|---|
| Docker + Docker Compose | `docker compose version` |
| Git | `git --version` |
| An LLM API key | Anthropic, xAI (Grok), or OpenAI |
| *(Optional)* Go 1.22+ | Only needed to build the CLI |

---

## Step 1 — Clone and Configure

```bash
git clone https://github.com/opentendril/core.git
cd core
cp .env.example .env
```

Open `.env` and set **at minimum one** LLM key and the three infrastructure secrets:

```bash
# Pick one (Anthropic recommended for code editing)
ANTHROPIC_API_KEY=sk-ant-...
# OR
GROK_API_KEY=xai-...
# OR
OPENAI_API_KEY=sk-...

# Required — generate these now
POSTGRES_PASSWORD=$(openssl rand -hex 16)
REDIS_PASSWORD=$(openssl rand -hex 16)
SECRET_KEY=$(openssl rand -hex 32)
SANDBOX_TOKEN=$(openssl rand -hex 16)
```

> **Self-build mode** (default): Tendril operates on its own source code.
> To point it at your project instead, see [External Project Mode](#external-project-mode) below.

---

## Step 2 — Launch

```bash
docker compose up --build
```

Wait for all services to report healthy. You should see:

```
tendril-1  | INFO     Application startup complete.
gateway-1  | Gateway listening on :9090
```

This usually takes 60–90 seconds on first launch (it downloads the embedding model).

---

## Step 3 — Verify the System is Live

Run this health check:

```bash
curl -s http://localhost:8080/health | python3 -m json.tool
```

Expected response:

```json
{
  "status": "healthy",
  "version": "0.1.0",
  "providers": {
    "anthropic": "available"
  }
}
```

If you see `"status": "healthy"` — **the kernel is running**. ✅

---

## Step 4 — Hello World

Choose **any one** of these three paths. They all hit the same brain.

---

### Option A — curl (fastest, no setup)

```bash
curl -s -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"session_id": "hello-world", "message": "What files are in this project?"}' \
  | python3 -m json.tool
```

You should see a JSON response with a `response` field listing the project files.

---

### Option B — Go CLI (recommended for interactive use)

Build the CLI (requires Go 1.22+):

```bash
cd cli/
go build -o tendril-cli .
cd ..
```

Run it:

```bash
./cli/tendril-cli
```

You should see:

```
🌱 Tendril CLI v0.1.0
Connecting to ws://localhost:9090/ws...
✅ Connected (session: cli-default, provider: default)
Type your message and press Enter. Ctrl+C to exit.

you ›
```

Type your first message:

```
you › What files are in this project?
```

Tendril will stream back a response listing the project files. That's your Hello World. ✅

**CLI commands:**

| Command | Effect |
|---|---|
| `/provider anthropic` | Switch to Claude |
| `/provider grok` | Switch to Grok |
| `/clear` | Clear the screen |
| `/quit` | Exit |
| `Ctrl+C` | Exit |

---

### Option C — Web UI (no install required)

Open your browser to:

```
http://localhost:8080/chat
```

Type `What files are in this project?` in the chat box and press Enter.

---

## Step 5 — Make Your First Edit

Once you've confirmed the system is live, try a real agentic task:

```
you › read cli/main.go and describe what it does
you › add a comment at the top of cli/main.go explaining this is the Tendril CLI entry point
you › commit the change with message "docs: add entry point comment to CLI"
```

Tendril will read the file, generate a diff, apply it, and commit to git.

---

## Troubleshooting

### "Connection refused" on port 8080
```bash
docker compose ps          # Check which services are running
docker compose logs tendril --tail 50  # Check for startup errors
```

### "Connection refused" on port 9090 (CLI)
The gateway service may not have started. Check:
```bash
docker compose logs gateway --tail 20
```

### Health check shows no providers
Your API key is missing or incorrect in `.env`. Verify:
```bash
docker compose exec tendril env | grep -E 'API_KEY|PROVIDER'
```
Then restart: `docker compose restart tendril`

### CLI fails to build
Ensure Go 1.22+ is installed: `go version`

### "All providers in cooldown"
This means all LLM providers returned errors. Check the logs for rate limit or authentication errors:
```bash
docker compose logs tendril | grep -i "error\|cooldown\|api"
```

---

## Third-Party Frontend Compatibility

Tendril exposes an OpenAI-compatible endpoint. Any tool that accepts a custom API base can use it:

```bash
# Aider
export OPENAI_API_BASE="http://localhost:8080/v1"
export OPENAI_API_KEY="tendril"
aider --model openai/tendril

# Crush
export OPENAI_API_BASE="http://localhost:8080/v1"
export OPENAI_API_KEY="tendril"
crush --model openai/tendril
```

---

## External Project Mode

To run Tendril on your own project instead of its own source:

```bash
# In .env:
TENDRIL_WORKSPACE_ROOT=/workspace
TENDRIL_PROJECT_PATH=/absolute/path/to/your-project
```

Restart: `docker compose down && docker compose up`

Tendril will now read, edit, and commit against your project.

---

## Running the Test Suite

To verify the kernel internals are healthy:

```bash
# Inside the repo (no Docker needed)
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt

python3 -m pytest tests/ --ignore=tests/testapi.py -v
# → 108 tests, all passing
```

---

## What's Next

| Task | Command |
|---|---|
| Edit a file | `you › modify src/sample.py to add error handling` |
| View git status | `you › what's the current git status?` |
| Make a safe staged change | `you › use staged_edit to modify [protected file]` |
| Merge a staged branch | `you › merge the staging branch into main` |

For the full architecture overview, see [ARCHITECTURE.md](./ARCHITECTURE.md).
For the self-build pipeline, see [GUARDRAILS.md](./GUARDRAILS.md).
