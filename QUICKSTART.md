# OpenTendril Quick Start — "Hello World" in 5 Minutes

This guide gets you from zero to a verified, working Go Stem orchestrator.
By the end, you will have confirmed the system is live, compiled the Go binary, and verified execution commands.

---

## Prerequisites

| Requirement | Check |
|---|---|
| Go 1.22+ | `go version` |
| Docker | `docker --version` |
| Git | `git --version` |
| An LLM API key | Anthropic (Claude) or OpenAI |

---

## Step 0 — GitHub Security & SDLC (Crucial)

OpenTendril operates on a strict **Zero-Trust SDLC architecture**. It must *never* be allowed to push code directly to `main` without human review.

1. **Protect your Main Branch:** In your repository settings, enforce a branch protection rule on `main` that requires a Pull Request before merging.
2. **Create a Fine-Grained PAT:** Generate a Personal Access Token that is strictly scoped to your repository with only **Read/Write** permissions for:
   - Code/Contents
   - Pull Requests
   - Issues
3. **Configure the Environment:** Store this token in your `.env` file as `GITHUB_TOKEN=...` (Git handles this token dynamically during sprout execution).

---

## Step 1 — Clone and Compile

Clone the repository and build the Go Stem orchestrator binary:

```bash
git clone https://github.com/opentendril/core.git
cd core
make install
```

This compiles the code and installs the `tendril` binary directly to your `$GOPATH/bin` (make sure this is in your system `$PATH`).

Verify installation:
```bash
tendril --help
```

---

## Step 2 — Run Onboarding Wizard

Run the interactive setup wizard to configure LLM API keys and model specifications:

```bash
tendril init
```

The wizard will generate a `.env` file containing your configurations in the root directory.

---

## Step 3 — Boot the Orchestrator API

Start the Go Stem background orchestrator API server:

```bash
tendril serve
```

Expected output:
```
🌱 Starting OpenTendril Go Stem Server on :8080...
⚙️ Loaded substrates config. Named substrates: core
🧠 Brain client initialized (provider: anthropic)
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

## Step 5 — Multi-Repository & Substrate Config

If your workspace spans more than one repository, add a `substrates.yaml` file to your project root. OpenTendril looks for it in these locations:

1. `./substrates.yaml`
2. `./.tendril/substrates.yaml`
3. Repository root of the current workspace

Example:

```yaml
substrates:
  core:
    path: /home/user/dev/opentendril/core
    url: https://github.com/opentendril/core.git
    branch: main
    auth: GITHUB_PERSONAL_ACCESS_TOKEN
    readonly: false

  my-api:
    url: https://github.com/myorg/api.git
    branch: develop
    auth: GITHUB_PERSONAL_ACCESS_TOKEN
    readonly: true
```

*   **Usage:** Pass the named key (e.g. `core` or `my-api`) as the `substrate` argument to `sproutTendril`.
*   **Dynamic Clones:** If `path` is omitted or does not exist, Go Stem automatically clones the `url` to a temporary directory under `/tmp`, runs the task, pushes the resulting branch to GitHub, and cleans up.
*   **Gating:** `readonly: true` ensures edits are kept inside the terrarium and never merged back to the host branch.

---

## Running the Test Suite

To verify the orchestrator internals and API routing:

```bash
cd cmd/stem
go test -v ./...
```

All tests (API, Docker terrarium stashing, sequence conductors, and coordinator routing) should build and pass cleanly.
