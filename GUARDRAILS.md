# OpenTendril Project Guardrails (The Laws of the Kernel)

All contributors (human and AI) must adhere to these guardrails to maintain the structural integrity, naming consistency, and security profile of OpenTendril.

---

## 🏗️ Naming and Casing Standards

We enforce strict language-based casing boundaries to prevent mixed patterns in code and serialized payloads.

### 1. Filesystem Naming (The "No Underscore" Rule)
* **Python Modules:** Must use **merged lowercase** only. No underscores, no hyphens (e.g., `llmrouter.py`, `skillsmanager.py`).
* **Go Modules & Source Files:** Must use **merged lowercase** only (e.g., `mcpserver.go`, `main.go`).
* **Go Test Files:** Must use **`*_test.go`** (e.g., `mcp_test.go`).
* **Directories & Non-Code Files:** Must use **kebab-case** (e.g., `docker-compose.yml`, `dynamic-skills/`, `assets/`).
* **Zero Underscores elsewhere:** No other project files may use `snake_case` or underscores, except externally required canonical filenames used for automatic platform discovery, such as GitHub's `CODE_OF_CONDUCT.md`.

> [!NOTE]
> **Cognitive Load & File Scan Philosophy:**
> In polyglot codebases, developers are often forced to constantly context-switch between snake_case filenames (Python), camelCase/PascalCase filenames (TypeScript/React), and flat-lowercase (Go/standard libraries). This fragmentation slows down visual scanning, autocomplete, and fuzzy-searching. 
> OpenTendril eliminates this friction by enforcing a strict filesystem bifurcation:
> 1. **Code Files are Flat Lowercase:** All source code files are merged lowercase (`llmrouter.py`, `mcpserver.go`), meaning you can scan the explorer tree or fuzzy-find files without thinking about punctuation boundaries.
> 2. **Config & Folders are Kebab-case:** Non-code files and directories use hyphens (`docker-compose.yml`, `github-actions/`). This creates an instant visual distinction between the active code logic and the surrounding configuration scaffold.
> 3. **Exceptions:** Go unit tests are named `*_test.go` because the Go build system strictly requires this pattern to discover and execute test suites. Platform-required canonical files, such as GitHub's `CODE_OF_CONDUCT.md`, are also allowed when their exact names are needed for automatic discovery. No general underscore naming is allowed.


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

## 🔒 Kernel Write Protection

OpenTendril builds itself, so a Sprout can be asked to change the orchestrator that is currently running it. Some of those files decide what every later run is permitted to do: the governed capability registry, the continuous integration that enforces these rules, the governance documents, the guard itself. A change to one of them must reach a human before it lands.

### The list lives in one place

The protected paths are defined in **[`.github/protected-paths`](.github/protected-paths)** and nowhere else. This document deliberately does not restate them: a second copy is a second source of truth, and it will drift.

### Editing a protected file is normal

There is no tool you are required to use and no ceremony to perform. Work on a branch and open a pull request, exactly as for anything else. What is not possible is *landing* the change without human review.

That is the whole design. Protection is enforced on the trusted side rather than asked of whoever is editing, because a rule the editing party is asked to honour constrains only a party that chooses to honour it — the same weakness a declared Pollen had before issued credentials replaced it.

### What enforces it

| Layer | Catches | Status |
|---|---|---|
| The Stem's merge-back guard | a Sprout rewriting the kernel through a Terrarium run | **enforced** — refuses the merge, names the path and the rule |
| `CODEOWNERS` | a change reaching the default branch through a pull request | **requests review** — see the caveat below |
| `scripts/check-protected-paths.sh` | the two files above drifting apart | **enforced in CI** |

The merge-back guard reads the list from the checkout as it stands *before* a merge, never from the commit being merged, so a run cannot delete the list in the same change that edits a kernel file.

> [!IMPORTANT]
> **`CODEOWNERS` blocks nothing on its own.** It requests review. Blocking requires branch protection on the default branch with required status checks, and — where there is more than one collaborator — "Require review from Code Owners". Neither is configured today, so the pull-request layer currently *records* intent rather than enforcing it. This is stated rather than glossed because a control that looks like a gate and is not is worse than a documented request.

### Adding a path

Add it to `.github/protected-paths` **and** `.github/CODEOWNERS`. The hygiene job fails if you do only one.

---

## 📚 Documentation Governance
* No major design shift, architectural choice, or branding change exists unless recorded where decisions actually live: a Design-RFC issue (label `design-rfc`, per the AGENTS.md 3-gate lifecycle) and/or a `docs/DESIGN-*.md` document.
* **Repo files must be self-contained — no GitHub references in source.** Never bake a GitHub issue/PR number into a repo file (code comments, Dockerfiles, requirements, docs): not `(#NNN)`, not `issue #NNN` / `PR #NNN` / `Design RFC #NNN`, not GitHub issue/pull URLs. That context belongs where decisions live — the **commit message** and the **pull-request description** — because that is what GitHub is for. Describe the *why* in prose instead. Enforced by `scripts/check-no-issue-refs.sh` in CI (diff-based: it blocks *new* references; pre-existing ones are swept as encountered). Legitimate exceptions are test fixtures that simulate real GitHub payloads and styling hex colours, which the check excludes.
* Technical structures are maintained in `ARCHITECTURE.md`.
* The roadmap is maintained in `ROADMAP.md`. **Shipped progress is not a checked-in file** — it lives in the project's pull-request and release history on GitHub (that is what GitHub is for), and the backlog lives in GitHub Issues, not a checked-in list.
