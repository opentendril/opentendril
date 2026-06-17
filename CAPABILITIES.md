# OpenTendril Capabilities & Security Model (CAPABILITIES)

OpenTendril is designed to be fully extensible. Developers and AI agents can extend the kernel's capabilities using Tools, Skills, Subagents, and Plugins. This document defines what these components are, why they exist, and how they are dynamically allowed, executed, and sandboxed.

---

## 💡 Plain-English Analogy (For Beginners)

If you are new to agentic coding, terms like "MCP", "Skills", "Tools", and "Plugins" can sound confusing. Here is a simple analogy comparing OpenTendril to a human developer working at a desk:

* **The Agent (The Programmer):** The AI brain itself (the LLM). It doesn't have a body; it can only think, plan, and draft text based on instructions.
* **The Desk (The Context Window):** What the programmer can see at any given moment. To work on your codebase, the programmer needs you to place relevant source files on their desk.
* **The Tools (The Keyboard & Terminal):** The programmer cannot directly modify your host system. Instead, we give them a keyboard with a few specific buttons: `read_file`, `write_file`, and `run_command`. Every action they take must use one of these tools.
* **The Skills (The Reference Manuals):** Prompts or markdown files (like `SKILL.md`) that teach the programmer how to work with a specific framework or tool (e.g., "How to write Firestore database rules"). When you ask the agent to work on a specialized task, the system automatically pulls the matching "Reference Manual" and puts it on their desk.
* **The Plugins (The Toolboxes):** A pre-packaged kit containing both reference manuals (Skills) and specialized tools (like a browser inspector or database connector) that you can hand to the programmer to expand their capabilities.
* **The SDLC Process (The Manager's Sign-off):** The governance rules (defined in `AGENTS.md`) that prevent the programmer from pushing code directly to production. They must write a design draft (Design RFC), get your signature (Gate A), show you exactly what lines of code they will modify (Implementation Plan), get your signature (Gate B), and then open a draft pull request for you to merge (Gate C).

---

## 1. Taxonomy of Capability Extension

```
                      ┌─────────────────────────────────────────┐
                      │                 PLUGIN                  │
                      │  (Bundled grouping of skills & tools)   │
                      └────────────────────┬────────────────────┘
                                           │
                    ┌──────────────────────┴──────────────────────┐
                    ▼                                             ▼
       ┌─────────────────────────┐                   ┌─────────────────────────┐
       │         SKILL           │                   │         TOOL            │
       │   (Markdown prompts,    │                   │   (JSON-RPC interface,  │
       │   RAG resource files)   │                   │    executable logic)    │
       └─────────────────────────┘                   └─────────────────────────┘
```

### A. Tools (The LLM's Hands and Eyes)
* **What:** Executable functions matching the Model Context Protocol (MCP) tool schema.
* **Why:** LLMs are stateless text generators. Tools allow them to interact with filesystems, networks, shell runtimes, and external APIs.
* **How They are Allowed:** Registered dynamically via the Go Gateway and executed inside the Sandbox container (Docker/gVisor).

### B. Skills (Targeted Context Domains)
* **What:** Directories containing standard instruction manuals (`SKILL.md`), structural schema definitions, and helper script workflows.
* **Why:** Avoids polluting the agent's system prompt with domain-specific knowledge (e.g., Xcode configuration, Firebase rules). Skills are injected into the agent's context *only* when the task requires them.
* **How They are Allowed:** Located in `skills/` directories. Scanned automatically by the RAG indexer and activated conditionally based on user intent.

### C. Subagents (Concurrent Delegation)
* **What:** Isolated LLM processes spawned dynamically to solve specific, granular subtasks (e.g., deep research, diff reviews).
* **Why:** Prevents prompt dilution and context pollution in the main orchestrator. Specialized agents perform better than generalists on complex, multi-step code operations.
* **How They are Allowed:** Invoked strictly via the `invoke_subagent` tool. Subagents communicate asynchronously using JSON-RPC messaging and operate on git staging worktrees.

### D. Plugins & Extensions (Modular Integrations)
* **What:** Cohesive packages containing configuration files, custom skills, and schema mappings (e.g., `firebase-plugin`, `chrome-devtools-plugin`).
* **Why:** Provides a modular distribution channel for developers to share entire integration suites.
* **How They are Allowed:** Configured in `tendril/config.json` under `PLUGINS=[...]`. The Go Gateway registers their tool paths during startup.

---

## 2. The Permission & Execution Pipeline (How We Allow Them)

Because executing arbitrary code and external tools can introduce security vulnerabilities, OpenTendril enforces a strict permissions pipeline.

```
┌────────────────────────────────────────────────────────────────────────┐
│                          Go Gateway Handler                            │
│  - Receives MCP / API call request from Client                         │
│  - Evaluates tool signature and permission requirements               │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                  ┌─────────────────┴─────────────────┐
                  ▼                                   ▼
        [ SYSTEM-SAFE TOOL ]                 [ DESTRUCTIVE / WRITE TOOL ]
        (e.g., read_file, list_dir)          (e.g., write_file, run_command)
                  │                                   │
                  ▼                                   ▼
        ┌──────────────────┐               ┌──────────────────────┐
        │  Direct Dispatch │               │  Human Consent Gate  │
        └─────────┬────────┘               └──────────┬───────────┘
                  │                                   │ (Yes / Approve)
                  ▼                                   ▼
┌────────────────────────────────────────────────────────────────────────┐
│                       Sandbox Execution Zone                           │
│  - Dispatches execution inside containerized runner (gVisor/KVM)       │
│  - Limits filesystem mounts strictly to `/workspace`                   │
│  - Blocks or monitors network egress based on tool scopes              │
└────────────────────────────────────────────────────────────────────────┘
```

### A. The Three Permission Gates

1. **System-Safe Tools (Read-Only):**
   * Tools like `read_file`, `list_dir`, or local codebase index searches do not modify system state.
   * **Policy:** Auto-approved. Execute immediately without interrupting the developer.

2. **Destructive / Modification Tools (Write/Execute):**
   * Tools like `write_file`, `run_command`, `git_commit` modify state.
   * **Policy:** Enforces **Explicit Human Consent**. The Go Gateway blocks execution and prompts the user in the UI. Workflows can configure rule-based bypasses (e.g., "Allow `git commit` without asking, but always ask for `run_command`").

3. **Restricted Platform Tools (Sensitive Secrets):**
   * Tools that read environment variables (`.env`), touch keys, or make unverified network requests.
   * **Policy:** Blocked by default. Must be explicitly white-listed in the user's `config.json` with domain constraints (e.g., "Allow network requests only to `api.github.com`").

---

## 3. Sandboxing & Isolation Levels

To guarantee safety, we decouple **what** a tool is allowed to do from **where** it does it. OpenTendril defines three sandbox tiers:

| Tier | Provider | Egress Security | Use Case |
|---|---|---|---|
| **Tier 1: High Compat** | Host OS / Standard Docker | Open | Trusted local plugins, compilers, package managers |
| **Tier 2: User Space Shield** | gVisor (`runsc`) | Restricted | Unverified third-party libraries, test suite execution |
| **Tier 3: Hardware VM** | Firecracker microVM | Isolated | SaaS environments, multi-tenant execution, unverified tools |

---

## 4. Extension Registry Layout

Plugins, Skills, and Tools are located in predictable, structured paths within the workspace to prevent path traversal exploits:

```
opentendril/
├── core/                       # Stateless system engine
└── tendril/                    # User configuration & state
    ├── config.json             # Core configurations & external MCP server pointers
    ├── plugins/                # User-installed plugins
    │   └── firebase/           # The firebase plugin bundle
    │       ├── plugin.json     # Plugin metadata, config, and dependency specifications
    │       ├── skills/         # Prompt instructions for firebase
    │       └── agents/         # Subagent definitions
    └── skills/                 # Custom local user skills (not tied to a plugin)
        └── build-ios/
            └── SKILL.md        # Specialized local prompt instructions
```

* **No Path Traversal:** The Sandbox Core rejects any tool call or skill lookup resolving outside the boundary of `core/` and `tendril/`.
