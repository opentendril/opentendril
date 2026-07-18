# Contributing to OpenTendril Core

Thank you for your interest in contributing to OpenTendril! We are building a secure, zero-friction, terrariumed execution kernel for AI coding agents, and we welcome contributions from human developers.

---

## 🚀 Quick Local Setup

OpenTendril is a Go Stem orchestrator with optional containerized UI and test services.

### Prerequisites:
* **Go:** Version 1.23 or newer.
* **Python:** Version 3.11 or newer.
* **Docker:** Required only for the optional UI and containerized test targets.
* **Node.js:** Version 18 or newer — only needed to work on the Command Center UI (`ui/`).

### Installation Steps:
1. **Fork and Clone:**
   ```bash
   git clone https://github.com/your-username/opentendril.git
   cd opentendril
   ```
2. **Build the CLI:**
   ```bash
   make stem      # builds cmd/stem/tendril (use 'make install' to put it on your PATH)
   ```
3. **Set Up Python Virtualenv:**
   ```bash
   python3 -m venv venv
   source venv/bin/activate
   pip install -r sprouts/python/requirements.txt
   ```
4. **Verify Your Environment:**
   Run the full lint and test suites to verify your local configuration:
   ```bash
   make check-all
   ```

To start the local orchestrator during development, run `make up`. This launches
`go run ./cmd/stem serve` directly on the Stem API port at `http://localhost:8080`;
it does not start a full Docker Compose stack. The optional UI can be started with
`docker compose --profile ui up`.

### Command Center UI (optional):
If you are working on the visual frontend, install its dependencies and run the
dev server (which proxies to a running `tendril serve`):
```bash
cd ui
npm install
STEM_TARGET=http://localhost:8080 npm run dev   # http://localhost:5173
npm run build                                   # type-check + static build to ui/dist/
```
See [`ui/README.md`](./ui/README.md) for the full UI guide.

---

## 🎨 Coding and Naming Standards

To prevent visual noise and scanning friction, we enforce strict language casing boundaries across the filesystem and code. Before writing code, review [GUARDRAILS.md](./GUARDRAILS.md).

### Crucial File Naming Rule:
* **No underscores or hyphens in code filenames.** All Python modules and Go source files must use **merged lowercase** only (e.g., `llmrouter.py`, `mcpserver.go`).
* **The Single Exception:** Go test files must end with `_test.go` (e.g., `mcp_test.go`) as required by the Go toolchain. No other underscores are allowed on the filesystem.
* **Non-code files** (like `docker-compose.yml`) use `kebab-case`.
* **The `ui/` frontend follows React/TypeScript convention instead**, scoped to that tree: `PascalCase.tsx` for components (`GardenCanvas.tsx`), `camelCase.ts` for modules (`garden.ts`), and `PascalCase/` folders for component families (`Garden/`). Do **not** rename these to merged-lowercase — it is a deliberate, tooling-idiomatic exception. JSON payload keys stay `camelCase` and API paths stay `kebab-case` as everywhere else.

### Code Casing Rules:
* **Python Code:** Use `snake_case` for variables and methods, and `PascalCase` for classes. Python is the *only* language where `snake_case` is allowed.
* **Go Code:** Use `camelCase` for local variables and `PascalCase` for exported names. **Do not use `snake_case` in Go.**
* **JSON Properties:** Must use `camelCase` only.
* **API Endpoints:** Must use `kebab-case` (e.g., `/api/mcp-tools`).

---

## 🔀 Workflow & Pull Requests

OpenTendril is an **AI-SDLC** workspace, meaning you will see issues and draft PRs generated automatically by AI agents (like Antigravity). 

As a human contributor, your workflow is simple and respects the same quality gates:
1. **Create a Branch:** Create a branch from `main` prefixed with `feat/` or `fix/` (e.g., `feat/add-github-provider`).
2. **Implement Changes:** Stay focused on a single issue/purpose. Keep diffs minimal.
3. **Verify Locally:** Before pushing, ensure all tests and linters pass:
   ```bash
   make check-all
   ```
4. **Submit a PR:** Push your branch and open a Pull Request into `main`. The GitHub Actions CI pipeline will automatically run `make check-all` inside the container.
5. **Review:** A maintainer will review your code. Once approved and CI checks pass, it will be merged.
