# 🌱 OpenTendril

**OpenTendril lets you stop approving every step an LLM takes — it works freely inside a boundary, and everything it did arrives in Git for you to review.**

The Terrarium isolation boundary gives it room to work; Git gives you the veto. Neither half is new. The combination is what makes walking away reasonable.

Most work on controlling these models addresses what they *say*: prompts, filters, evaluations. This addresses what their code *does* once you run it.

---

**Artificial Intelligence does not behave like traditional computing.**

For decades, the software industry has built deterministic systems—rigid state machines where "Tasks," "Scripts," and "Agents" blindly follow instructions and fail if a single parameter deviates. But Large Language Models are not state machines; they are probabilistic, messy, and organic, modeling the very chaotic adaptation seen in natural biology.

Trying to force an organic neural network into rigid, deterministic IT structures leads to fragile execution, context window bloat, and severe security vulnerabilities. 

**OpenTendril abandons traditional software architecture in favor of a Synthetic Biological Taxonomy.** By replicating natural evolutionary processes, we isolate unverified code execution while maintaining a deterministic Go orchestrator.

---

### The Taxonomy (Biological to IT Mapping)
To understand OpenTendril, you must understand its anatomy:

*   **Tendril**: The brand name with no specific meaning or name given to a component.
*   **The Stem**: The deterministic Go routing and lifecycle kernel. It contains and governs the Core capability authority. It is not a reasoning component.
*   **The Mycorrhizae**: The cognitive LLM side.
*   **The Branches**: Capability groups or security zones.
*   **The Sprouts**: Ephemeral execution bodies.
*   **The Terrarium**: The execution and isolation boundary.
*   **The Greenhouse (UI)**: The external observer dashboard. It sits completely outside the biological entity to observe telemetry.
*   **(Prompt Elements)**: We use **Genotypes** (personas), **Plasmids** (modular context blocks), and **Transcripts** (user tasks) to dynamically assemble instructions for the LLM.

REST, MCP, and CLI adapters are architecture surfaces translating transport only.

> 📖 **Read the full philosophy:** Explore the [GLOSSARY.md](GLOSSARY.md) and [SYNTHETIC-TAXONOMY.md](SYNTHETIC-TAXONOMY.md) to understand the concept design.
> 🛠️ **Read the architecture guide:** Check [ARCHITECTURE.md](ARCHITECTURE.md) to understand how these concepts are physically built (Go, Docker, and protocols).
> 🔌 **Review capabilities:** See [CAPABILITIES.md](CAPABILITIES.md) for the exact current capability set.

---

### What does this actually mean for developers?
In standard IT speak: **OpenTendril is a headless, local-first AI coding framework.** 

For operators, the Stem runs host-side, coordinating with any frontend client via the Model Context Protocol (MCP), and executes codebase changes safely.

The Stem coordinates **Sprouts** (ephemeral execution bodies) running inside **Terrariums** (isolation boundaries). Implemented Terrarium providers include Docker and gVisor (container-based), Firecracker (microVM provider), and Host (the explicit isolation escape requiring `TENDRIL_ALLOW_HOST_EXECUTION=true`). `mesh.graft` delegates a Substrate commit through a peer Stem.

---

## 🚀 Installing

**[docs/GUIDE-INSTALL.md](docs/GUIDE-INSTALL.md) is the installation guide**, and the source
of truth for what a sound installation is.

It does not prescribe one procedure. What makes an installation sound is a set of
measurable properties — whether the Stem holds credentials no caller can read,
whether a caller can escalate to it, whether anything else can replace the binary
it runs. The guide states those properties, works through the most hardened
configuration in full, and shows which choices can be made differently without
weakening any of them.

Whichever way you install, this tells you what you actually have:

```bash
tendril hardiness
```

It reports rather than gates. A weak finding may describe a configuration you
chose knowingly — running the Stem as your own user on a laptop is legitimate,
and the report says so plainly instead of pretending otherwise.

Once it is running, [docs/GUIDE-QUICKSTART.md](docs/GUIDE-QUICKSTART.md) covers the first session.

---

## 🏗️ Architecture

```text
 external clients
    │
    ▼
 CLI / REST / MCP adapter (e.g. tendril mcp / tendril serve)
    │
    ▼
 Stem / Core authority
    ├─► Roots ───────► Mycorrhizae (LLM providers)
    └─► Terrarium ───► Sprout ───► Substrate
                                       │
                                       ▼
                              Git-Reviewable Fruit
```

Successful reviewable Sprout work becomes Git-reviewable Fruit. A local RunSprout can internally merge a Terrarium commit back into its source branch, and remote execution may publish its commit, but this internal merge-back or publication is NOT Botanist acceptance. `git.commit`, `git.push`, and `git.pr` are distinct governed capabilities. `git.pr` opens a pull request and does not merge; there is no governed `git.merge`. Final review acceptance and merge remain the Botanist's decision.

---

## 🔌 Client Integrations

OpenTendril acts as a headless backend. You can connect it to your favorite developer tools using either the **Model Context Protocol (MCP)** or its **OpenAI-Compatible API**.

### 1. Claude Code, Claude Desktop & Cursor (via MCP)
`tendril mcp` acts as a governed command surface and stdio bridge. It determines its mode dynamically:
- With a configured credential and another-user governed Stem, it **forwards** traffic securely to that Stem.
- Otherwise, it operates in-process locally or refuses connection if appropriate. See [docs/GUIDE-QUICKSTART.md](docs/GUIDE-QUICKSTART.md) for details.

**Claude Code (CLI) — one command:**
```bash
claude mcp add opentendril -- tendril mcp
```

**Claude Desktop / Cursor** — edit your MCP configuration file:
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

Once connected, the client can drive the Stem's governed capabilities directly.
Governed commands declared by `core.CapabilityNames()` are projected across
CLI, REST, and MCP and mechanically parity-checked.

Explicitly, views (such as `sprout.watch`) and control-plane operations are distinct
and are not part of governed command parity.

See [CAPABILITIES.md](CAPABILITIES.md) for the exact current capability set.

### 2. Aider & CodexCLI (via OpenAI API)
Command-line coding assistants can use Tendril as their backend LLM provider, benefiting from its provider routing.

Make sure `tendril serve` is running, then launch Aider:
```bash
aider --openai-api-base http://localhost:8080/v1 --model openai/tendril
```

### 3. The Greenhouse (Command Center UI)

The **Greenhouse** is a desktop-grade web dashboard for watching every
Sprout the Stem is growing in real time — the visual frontend of the **Tendril
OS**. It is a strictly decoupled React client that talks to the Stem only over
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
[docs/GREENHOUSE.md](docs/GREENHOUSE.md) for how it fits the Tendril OS
architecture and the REST/WebSocket contract it consumes.

---


## 🛠️ Development Commands

```bash
make install          # Build + install tendril binary to ~/.local/bin
make test-stem        # Run Go unit tests
make test-all         # Run all tests

cd ui && npm install  # Install Greenhouse UI deps
npm run dev           # Run the UI dev server (proxies to the Stem)
npm run build         # Type-check + build the static UI bundle to ui/dist/
```
