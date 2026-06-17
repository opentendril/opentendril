# Contributing to OpenTendril Core

Thank you for your interest in contributing to OpenTendril! We are building a secure, zero-friction, sandboxed execution kernel for AI coding agents, and we welcome contributions from human developers.

---

## 🚀 Quick Local Setup

OpenTendril is a polyglot project combining a **Go Gateway** and a **Python Sandbox Core**.

### Prerequisites:
* **Go:** Version 1.23 or newer.
* **Python:** Version 3.11 or newer.
* **Docker:** Required for running the sandbox container tests locally.

### Installation Steps:
1. **Fork and Clone:**
   ```bash
   git clone https://github.com/your-username/core.git
   cd core
   ```
2. **Build the CLI:**
   ```bash
   make cli
   ```
3. **Set Up Python Virtualenv:**
   ```bash
   python3 -m venv venv
   source venv/bin/activate
   pip install -r requirements.txt
   ```
4. **Verify Your Environment:**
   Run the full lint and test suites to verify your local configuration:
   ```bash
   make check-all
   ```

---

## 🎨 Coding and Naming Standards

To prevent visual noise and scanning friction, we enforce strict language casing boundaries across the filesystem and code. Before writing code, review [GUARDRAILS.md](./GUARDRAILS.md).

### Crucial File Naming Rule:
* **No underscores or hyphens in code filenames.** All Python modules and Go source files must use **merged lowercase** only (e.g., `llmrouter.py`, `mcpserver.go`).
* **The Single Exception:** Go test files must end with `_test.go` (e.g., `mcp_test.go`) as required by the Go toolchain. No other underscores are allowed on the filesystem.
* **Non-code files** (like `docker-compose.yml`) use `kebab-case`.

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
