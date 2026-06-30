# 🌱 OpenTendril: The Synthetic AI Organism

**Artificial Intelligence does not behave like traditional computing.**

For decades, the software industry has built deterministic systems—rigid state machines where "Tasks," "Scripts," and "Agents" blindly follow instructions and fail if a single parameter deviates. But Large Language Models are not state machines; they are probabilistic, messy, and organic, modeling the very chaotic adaptation seen in natural biology.

Trying to force an organic neural network into rigid, deterministic IT structures leads to fragile execution, context window bloat, and severe security vulnerabilities. 

**OpenTendril abandons traditional software architecture in favor of a Synthetic Biological Taxonomy.** By replicating natural evolutionary processes, we have built a highly robust, secure, and adaptable execution engine.

---

### The Taxonomy (Biological to IT Mapping)
To understand OpenTendril, you must understand its anatomy:

*   **The Genotype (System Prompt / Persona):** The core DNA. We don't use generic system prompts; we inject Genotypes that dictate the fundamental identity and constraints of the AI (e.g. `thinker.json`, `verifier.json`, `debugger.json`).
*   **The Plasmid (Modular Context / Skills):** Modular blocks of context, rules, or Repo Maps (`repomap.md`) injected into the genome on the fly.
*   **The Transcript (Task / User Prompt):** We do not issue rigid "Tasks." We provide Transcripts—fuzzy, contextual instructions that the Genotype must dynamically interpret and execute.
*   **The Sprout (Ephemeral Sandbox):** OpenTendril does not run continuous, stateful background agents. For every step, a new isolated *Sprout* (Docker container) instantly boots, executes local commands, and is destroyed.
*   **The Stem (Go Orchestrator):** The Go-based CLI and API kernel. Just like a physical stem supporting a plant, the Go Stem handles networking, routes LLM completions, and orchestrates sandbox lifecycles.

> 📖 **Read the full philosophy:** Explore the [Synthetic Biological Taxonomy](SYNTHETIC-TAXONOMY.md) to understand the concept design.
> 🛠️ **Read the engineering guide:** Check the [Material & Architecture Guide](TENDRIL-GUIDE.md) to understand how these concepts are physically built (Go, Docker, and protocols).

---

### What does this actually mean for developers?
In standard IT speak: **OpenTendril is a headless, local-first and enterprise-ready AI coding framework.** 

For individuals, it runs entirely on your host machine, coordinating with any frontend client via the Model Context Protocol (MCP), and executes codebase changes safely inside ephemeral language-specific Docker sandboxes.

For high-security and multi-tenant enterprise environments, OpenTendril supports **Stem Grafting** (securely delegating high-privilege operations over WebSockets to a Central Governance Stem) and is designed to support hardware-level sandbox isolation (e.g. Firecracker/gVisor) for zero-trust execution.

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
    Sandbox Provider       ← Extensible Execution Substrate
             │  - Local: Ephemeral Docker Sandbox
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
MCP allows clients to natively access Tendril's secure sandboxed tools.

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
```
