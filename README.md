# 🌱 OpenTendril: The Synthetic AI Organism

**Artificial Intelligence does not behave like traditional computing.**

For decades, the software industry has built deterministic systems—rigid state machines where "Tasks," "Scripts," and "Agents" blindly follow instructions and fail if a single parameter deviates. But Large Language Models are not state machines; they are probabilistic, messy, and organic, modeling the very chaotic adaptation seen in natural biology.

Trying to force an organic neural network into rigid, deterministic IT structures leads to fragile execution, context window bloat, and severe security vulnerabilities. 

**OpenTendril abandons traditional software architecture in favor of a Synthetic Biological Taxonomy.** By replicating natural evolutionary processes, we have built a highly secure, decoupled **microservices architecture** for AI orchestration. The goal of OpenTendril is to completely isolate unverified code execution while maintaining a perfectly lean core orchestrator, utilizing discrete plugins as gateways to remote capabilities.

---

### The Taxonomy (Biological to IT Mapping)
To understand OpenTendril, you must understand its anatomy:

*   **The Stem**: The lean, deterministic Go orchestrator. It handles routing and lifecycle management without any business logic or LLM "smarts".
*   **The Branches**: Capability groups or security zones initialized by the Stem at boot time to organize integrations and limit access scope.
*   **The Tendrils**: Lean microservices (ideally written in Go) grafted onto Branches. They act as simple MCP gateways to remote functions and external enterprise systems.
*   **The Sprouts**: Ephemeral, strictly isolated worker containers that dynamically sprout on a Branch to perform code execution, and are then destroyed.
*   **The Greenhouse (UI)**: The external observer dashboard. It sits completely outside the biological entity to observe telemetry.
*   **(Prompt Elements)**: We use **Genotypes** (personas), **Plasmids** (modular context blocks), and **Transcripts** (user tasks) to dynamically assemble instructions for the LLM.

> 📖 **Read the full philosophy:** Explore the [Synthetic Biological Taxonomy](SYNTHETIC-TAXONOMY.md) to understand the concept design.
> 🛠️ **Read the engineering guide:** Check the [Material & Architecture Guide](TENDRIL-GUIDE.md) to understand how these concepts are physically built (Go, Docker, and protocols).
> 📐 **Visual architecture diagrams:** See [docs/ARCHITECTURE-TAXONOMY.md](docs/ARCHITECTURE-TAXONOMY.md) for Mermaid diagrams of the full execution pipeline, Genotype hierarchy, and security trust boundaries.

---

### What does this actually mean for developers?
In standard IT speak: **OpenTendril is a headless, local-first and enterprise-ready AI coding framework.** 

For individuals, it runs entirely on your host machine, coordinating with any frontend client via the Model Context Protocol (MCP), and executes codebase changes safely inside ephemeral language-specific Docker terrariumes.

For high-security and multi-tenant enterprise environments, OpenTendril supports **Stem Grafting** (securely delegating high-privilege operations over WebSockets to a Central Governance Stem) and is designed to support hardware-level terrarium isolation (e.g. Firecracker/gVisor) for zero-trust execution.

---

## 🚀 Quick Start (Fresh Install)

### Step 1 — Clone and install the CLI

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

### Step 2 — Run the setup wizard

```bash
tendril init
```

This will:
- Auto-detect local [Ollama](https://ollama.ai) models.
- Configure cloud LLM providers (Anthropic, OpenAI, etc.).
- Write your config to `.env`.

### Step 3 — Start the Stem server

In one terminal:
```bash
tendril serve
```

### Step 4 — Chat!

In a second terminal:
```bash
tendril chat
```

---

## 🏗️ Architecture

```
            YOU (Developer)
             │
             ▼
      Client Applications  ← Claude Desktop / Cursor / Web UI
             │  (MCP over stdio / WebSockets)
             ▼
       tendril serve       ← Unified Go Stem (port :8080)
             │  - Dynamic LLM Routing (Coordinator / Worker)
             │  - Substrate & Read-Only configs checked
             │  - Ephemeral git worktree checkout
             │  - Stem Grafting (Delegates high-privilege pushes via WebSockets)
             ▼
    Terrarium Provider       ← Extensible Execution Substrate
             │  - Local: Ephemeral Docker Terrarium
             │  - Enterprise: Firecracker / gVisor MicroVMs (WIP)
             │  - Injects Genotype plasmids & AST Repo Map
             │  - Executes file edits, compilation, and unit tests
             ▼
        Merge Back         ← Stash popped, worktree merged, main updated
```

---

## 🔌 Client Integrations

OpenTendril acts as a headless backend. You can connect it to your favorite developer tools using either the **Model Context Protocol (MCP)** or its **OpenAI-Compatible API**.

### 1. Claude Desktop & Cursor (via MCP)
MCP allows clients to natively access Tendril's secure terrariumed tools.

Edit your MCP configuration file:
- **Claude Desktop (Mac):** `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Claude Desktop (Linux):** `~/.config/Claude/claude_desktop_config.json`

Add the following configuration:
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

### 2. Aider & CodexCLI (via OpenAI API)
Command-line coding assistants can use Tendril as their backend LLM provider, benefiting from its local inference and routing.

Make sure `tendril serve` is running, then launch Aider:
```bash
aider --openai-api-base http://localhost:8080/v1 --model openai/tendril
```

### 3. The Greenhouse (Command Center UI)

The **Command Center** is a desktop-grade web dashboard for watching every
Sprout the Stem is growing in real time — the visual frontend of the "OS of
OT". It is a strictly decoupled React client that talks to the Stem only over
the documented REST + WebSocket surface, and renders live orchestration as a
**living botanical garden**: parallel sprouts emerge and wither, mycelial merges
converge, and phenotypic-selection runs compete for fitness on screen.

It is **optional and containerized** — one command brings it up alongside the
host Stem (no Node/npm required), served from a single origin that
reverse-proxies the Stem's API and `/ws` surfaces:

```bash
docker compose --profile ui up -d               # http://127.0.0.1:4173
```

For UI development, run the Vite dev server instead:

```bash
cd ui
npm install
STEM_TARGET=http://localhost:8080 npm run dev   # http://localhost:5173
```

Operators enter their Stem address and API key in an onboarding screen (no
`.env` editing). See [`ui/README.md`](ui/README.md) for running it, the
component tree, the refresh-resilient hydration flow, and the full
EventBus-event → botanical-visual mapping; and
[docs/COMMAND-CENTER.md](docs/COMMAND-CENTER.md) for how it fits the OS-of-OT
architecture and the REST/WebSocket contract it consumes.

---

## ⚙️ Configuration API

Manage security triggers and genotypes via the REST API:

```bash
# List active security triggers
curl http://localhost:8080/v1/config/triggers

# List AI genotypes
curl http://localhost:8080/v1/config/personas
```

---

## 🛠️ Development Commands

```bash
make install          # Build + install tendril binary
make test-sprout      # Run Go unit tests
make test-all         # Run all tests

cd ui && npm install  # Install Command Center UI deps
npm run dev           # Run the UI dev server (proxies to the Stem)
npm run build         # Type-check + build the static UI bundle to ui/dist/
```
