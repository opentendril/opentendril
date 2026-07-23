# OpenTendril Quick Start — your first session

This covers what to do **once OpenTendril is installed**: confirming the Stem is
live, and the four ways to talk to it.

> [!IMPORTANT]
> **Installation is not here.** It lives in
> **[docs/GUIDE-INSTALL.md](GUIDE-INSTALL.md)**, which is the source of truth for what a
> sound installation is — the properties that decide whether the delegation
> boundary is enforced by the operating system or merely recorded, and the
> configurations that satisfy them.
>
> Install first, then come back. Run `tendril hardiness` at any point to see
> which configuration you are actually running.

---

## Before you start

| Requirement | Check |
|---|---|
| A working install | `tendril --help` |
| Docker | `docker --version` |
| An LLM | Local [Ollama](https://ollama.ai) (default) — or a cloud provider key |

Confirm the Stem is running:

```bash
curl -s localhost:8080/health
```

If it is not, start it the way your installation calls for — a service, or
`tendril serve` from the Stem's working directory. Both are covered in the
installation guide.

---

## Talking to the Stem

Choose **any one** of these to interact with the running kernel:

---

### Option A — Model Context Protocol over stdio (Claude Desktop / IDE)

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

> [!CAUTION]
> `tendril mcp` starts an **in-process Stem as whoever runs it**, reading its
> control plane from that caller's working directory. It does not connect to a
> Stem running as another user. If you installed the Stem under its own
> principal, this bypasses that boundary — see
> [docs/GUIDE-INSTALL.md](GUIDE-INSTALL.md) for the surface a credential-bearing
> Pollinator uses instead.

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

## Running the Test Suite

To verify the orchestrator internals and API routing:

```bash
cd cmd/stem
go test -v ./...
```

All tests (API, Docker terrarium stashing, sequence conductors, and coordinator routing) should build and pass cleanly.
