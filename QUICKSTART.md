# OpenTendril Quick Start — "Hello World" in 5 Minutes

This guide gets you from zero to a verified, working Go Stem orchestrator.
By the end, you will have confirmed the system is live, compiled the Go binary, and verified execution commands.

---

## Prerequisites

| Requirement | Check |
|---|---|
| Go 1.24+ | `go version` |
| Docker | `docker --version` |
| Git | `git --version` |
| An LLM | Local [Ollama](https://ollama.ai) (default) — or an Anthropic/OpenAI API key |

---

## Step 0 — GitHub Security & SDLC (Crucial)

OpenTendril operates on a strict **Zero-Trust SDLC architecture**. It must *never* be allowed to push code directly to `main` without human review.

1. **Protect your Main Branch:** In your repository settings, enforce a branch protection rule on `main` that requires a Pull Request before merging.
2. **Create a Fine-Grained PAT:** Generate a Personal Access Token that is strictly scoped to your repository with only **Read/Write** permissions for:
   - Code/Contents
   - Pull Requests
   - Issues
3. **Configure the Environment:** Export this token as `GITHUB_TOKEN` — it is the single canonical PAT variable across OpenTendril. The recommended way is direnv, which sources it from `gh` automatically:

   ```bash
   cp .envrc.example .envrc
   direnv allow
   ```

   Terrariums push branches over HTTPS, so the token must be present in the Stem's process environment (`gh`'s keyring alone is not enough). Alternatively, set `GITHUB_TOKEN=...` in your `.env` file.

---

## Step 1 — Clone and Compile

Clone the repository and build the Go Stem orchestrator binary:

```bash
git clone https://github.com/opentendril/opentendril.git
cd opentendril
make install
```

This compiles the code and installs the `tendril` binary to `~/.local/bin` (make sure this is in your system `$PATH`).

Verify installation:
```bash
tendril --help
```

---

## Step 2 — Run Onboarding Wizard

Run the interactive setup wizard:

```bash
tendril init
```

The wizard is **Ollama-first**: it detects a running Ollama instance at `localhost:11434`, lists your pulled models, and defaults the Stem to fully local, private inference (`DEFAULT_LLM_PROVIDER=local`). If you decline or have no Ollama, it walks you through a cloud provider (Anthropic, OpenAI, xAI, Google) and its API key instead. It also offers to capture a `GITHUB_TOKEN` and scaffold a minimal `substrates.yaml`.

The wizard writes your configuration to a `.env` file in the root directory, updating existing keys in place — re-running it never duplicates keys.

---

## Step 3 — Boot the Orchestrator API

Start the Go Stem background orchestrator API server natively from the repository:

```bash
make up
```

This runs `go run ./cmd/stem serve` on the Stem API port (`http://localhost:8080`).
The remaining compose services are optional: use `docker compose --profile ui up`
to launch the UI, or `make test-stem` to run the Go test container.

Expected output:
```
🌱 Starting OpenTendril Go Stem Server on :8080...
⚙️ Loaded substrates config. Named substrates: core
🧠 Brain client initialized (provider: local)
```

The orchestrator API is now running locally.

---

## Step 4 — Verification Pathways

Choose **any one** of these paths to interact with the running kernel:

---

### Option A — Stdio MCP Server (Claude Desktop / IDE)

OpenTendril integrates natively with Claude Desktop or Cursor/VS Code as an MCP server. Add this config to your MCP settings file:

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril",
      "args": ["mcp"]
    }
  }
}
```

Restart your IDE or Claude Desktop. The OpenTendril tools (`sproutTendril`, `createGenotype`) will now be active in your client.

---

### Option B — Interactive Terminal Chat

You can chat with OpenTendril directly in your terminal using the WebSocket CLI wrapper:

```bash
tendril chat
```

Expected interface:
```
🌱 OpenTendril CLI Chat
Connecting to ws://localhost:8080/ws...
✅ Connected (session: cli-default, provider: default)

you ›
```

Type your first instruction:
```
you › read cmd/main.go and describe what it does
```

---

### Option C — Curl Command Execution

Verify the server REST endpoint directly using curl:

```bash
curl -s -X POST http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "What files are in this project?"}'
```

---

### Option D — Session Management CLI

Manage Tendril sessions directly from the terminal. These commands project the same Core capabilities as the MCP and REST surfaces:

```bash
# Create a new session
tendril phytomer create --provider anthropic --model claude-sonnet-4-20250514

# List active sessions
tendril phytomer list

# View session history
tendril phytomer history <session-id> --limit 20

# Delete a session
tendril phytomer delete <session-id>
```

---

## Step 5 — Multi-Repository & Substrate Config

If your workspace spans more than one repository, add a `substrates.yaml` file to your project root. OpenTendril looks for it in these locations:

1. `./substrates.yaml`
2. `./.tendril/substrates.yaml`
3. Repository root of the current workspace

Example:

```yaml
substrates:
  core:
    path: /home/user/dev/opentendril/opentendril
    url: https://github.com/opentendril/opentendril.git
    branch: main
    auth: GITHUB_TOKEN
    readonly: false

  my-api:
    url: https://github.com/myorg/api.git
    branch: develop
    auth: GITHUB_TOKEN
    readonly: true
```

*   **Usage:** Pass the named key (e.g. `core` or `my-api`) as the `substrate` argument to `sproutTendril`.
*   **Dynamic Clones:** If `path` is omitted or does not exist, Go Stem automatically clones the `url` to a temporary directory under `/tmp`, runs the task, pushes the resulting branch to GitHub, and cleans up.
*   **Gating:** `readonly: true` ensures edits are kept inside the terrarium and never merged back to the host branch.

---

## Step 6 — Production Service Installation

For a persistent background deployment, install OpenTendril as a systemd service. The service runs as your user (not root) so it can access the host Docker socket and your local `.env` configuration.

Make the installer executable:

```bash
chmod +x install.sh
```

Run the installer from the repository root:

```bash
sudo ./install.sh
```

The script compiles `tendril`, installs it to `/usr/local/bin/tendril`, registers `opentendril.service`, and starts the orchestrator automatically.

View service logs:

```bash
journalctl -u opentendril -f
```

Check service status:

```bash
systemctl status opentendril
```

---

## Running the Test Suite

To verify the orchestrator internals and API routing:

```bash
cd cmd/stem
go test -v ./...
```

All tests (API, Docker terrarium stashing, sequence conductors, and coordinator routing) should build and pass cleanly.
