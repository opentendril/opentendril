# OpenTendril Project Guardrails (The Laws of the Kernel)

All contributors (human and AI) must adhere to these guardrails to maintain the structural integrity, naming consistency, and security profile of OpenTendril.

---

## 🏗️ Naming and Casing Standards

We enforce strict language-based casing boundaries to prevent mixed patterns in code and serialized payloads.

### 1. Filesystem Naming (The "No Underscore" Rule)
* **Python Modules:** Must use **merged lowercase** only. No underscores, no hyphens (e.g., `llmrouter.py`, `skillsmanager.py`).
* **Go Modules & Source Files:** Must use **merged lowercase** only (e.g., `mcpserver.go`, `main.go`).
* **Go Test Files:** Must use **`*_test.go`** (e.g., `mcp_test.go`). **This is the ONLY exception allowed to have underscores in the filename across the entire repository.**
* **Directories & Non-Code Files:** Must use **kebab-case** (e.g., `docker-compose.yml`, `dynamic-skills/`, `assets/`).
* **Zero Underscores elsewhere:** No other files on the filesystem may use `snake_case` or underscores.

> [!NOTE]
> **Cognitive Load & File Scan Philosophy:**
> In polyglot codebases, developers are often forced to constantly context-switch between snake_case filenames (Python), camelCase/PascalCase filenames (TypeScript/React), and flat-lowercase (Go/standard libraries). This fragmentation slows down visual scanning, autocomplete, and fuzzy-searching. 
> OpenTendril eliminates this friction by enforcing a strict filesystem bifurcation:
> 1. **Code Files are Flat Lowercase:** All source code files are merged lowercase (`llmrouter.py`, `mcpserver.go`), meaning you can scan the explorer tree or fuzzy-find files without thinking about punctuation boundaries.
> 2. **Config & Folders are Kebab-case:** Non-code files and directories use hyphens (`docker-compose.yml`, `github-actions/`). This creates an instant visual distinction between the active code logic and the surrounding configuration scaffold.
> 3. **Exceptions:** Go unit tests are named `*_test.go` because the Go build system strictly requires this pattern to discover and execute test suites. No other underscores are allowed.

### 1.1 Documentation Path Portability

Documentation, examples, and design records must not contain paths tied to a
developer's workstation. Do not commit personal home-directory paths such as
`/home/name/...`, `/Users/name/...`, `C:\\Users\\name\\...`, or local
`file://` links. Use repository-relative Markdown links, `/path/to/...`
placeholders, or the standard uppercase `$HOME` variable in shell examples.
The lowercase `$home` form is not a portable default.


### 2. Code Variables, Properties, and Methods
* **Python Code:**
  * Functions, variables, and methods must use **`snake_case`** (e.g., `def search_memory(query: str):`).
  * Class names must use **`PascalCase`** (e.g., `class ToolFactory:`).
  * **Python is the ONLY language where `snake_case` is permitted in code.**
* **Go / JavaScript / TypeScript Code:**
  * Variables, local fields, and methods must use **`camelCase`** (e.g., `var runID string`, `func runMCPServer()`).
  * Exported structs, methods, and types in Go must use **`PascalCase`** (standard Go visibility conventions).
  * **Zero use of `snake_case` is permitted in Go/JS/TS code.**
* **JSON / REST / JSON-RPC Payloads:**
  * All serialized JSON keys in API payloads and JSON-RPC messages must use **`camelCase`** (e.g., `{"protocolVersion": "2024-11-05", "inputSchema": {...}}`).
* **REST HTTP Endpoints:**
  * All URL paths and endpoints must use **`kebab-case`** (e.g., `/api/mcp-tools`, `/v1/chat-completions`).
* **Configuration:**
  * Environment variables and database keys must use **`SCREAMING_SNAKE_CASE`** (e.g., `TERRARIUM_PROVIDER`, `GROK_API_KEY`).

---

## 🔒 Security & Code Write Protection

OpenTendril operates a self-building pipeline. To protect the orchestrator from corrupting its own running process during a session, we define strict write permissions.

### Protected Files (No AI Edits in Session)
The following kernel files must **never** be modified directly via the AI orchestrator's `write_file` or `apply_code_patch` tools during active chat execution:
* `cmd/stem/main.go` — Stem kernel entry point
* `cmd/stem/cmdserve.go` — daemon / gateway surface bootstrap
* `cmd/stem/internal/core/` — the governed capability registry and its boundary/parity tests
* `.github/workflows/` — CI pipelines (the Adaptive Immune System)
* `.github/dependabot.yml` — supply-chain update policy
* `.env` — Environment secrets
* `AGENTS.md`, `GUARDRAILS.md`, `ARCHITECTURE.md`, `SYNTHETIC-TAXONOMY.md`, `CAPABILITIES.md`, `USE-CASES.md` — Governance files

### Staged Modification Pipeline
If these protected files must be edited, they must route through the **`staged_edit`** tool. This tool:
1. Creates a git staging branch (`staging/*`).
2. Applies the surgical code patch.
3. Compiles the syntax and runs automated Docker canary tests.
4. Commits and checks out back to `main`, leaving the staging branch for manual human code review and merge.

---

## 📚 Documentation Governance
* No major design shift, architectural choice, or branding change exists unless recorded where decisions actually live: a Design-RFC issue (label `design-rfc`, per the AGENTS.md 3-gate lifecycle) and/or a `docs/DESIGN-*.md` document.
* **Repo files must be self-contained — no GitHub references in source.** Never bake a GitHub issue/PR number into a repo file (code comments, Dockerfiles, requirements, docs): not `(#NNN)`, not `issue #NNN` / `PR #NNN` / `Design RFC #NNN`, not GitHub issue/pull URLs. That context belongs where decisions live — the **commit message** and the **pull-request description** — because that is what GitHub is for. Describe the *why* in prose instead. Enforced by `scripts/check-no-issue-refs.sh` in CI (diff-based: it blocks *new* references; pre-existing ones are swept as encountered). Legitimate exceptions are test fixtures that simulate real GitHub payloads and styling hex colours, which the check excludes.
* Technical structures are maintained in `ARCHITECTURE.md`.
* The roadmap is maintained in `ROADMAP.md`. **Shipped progress is not a checked-in file** — it lives in the project's pull-request and release history on GitHub (that is what GitHub is for), and the backlog lives in GitHub Issues, not a checked-in list.
