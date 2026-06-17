# OpenTendril Local & Remote Testing Guidelines

This document defines the verification standards for OpenTendril. To guarantee stability and prevent regressions, all checks run locally must match the remote CI environment exactly.

---

## 1. The Single Source of Truth: The Makefile
We do not use ad-hoc commands for testing and linting. Every verification command is orchestrated via the root `Makefile` in the `core/` directory. This ensures developers and CI run identical code checks.

| Command | Subsystem | Purpose |
|---|---|---|
| `make lint` | Python / Go | Runs syntax formatting and code style checks (Ruff for Python). |
| `make test` | Python | Executes the Python backend test suite (`pytest tests/`). |
| `make test-go` | Go | Executes the Go CLI/Gateway unit tests. |
| `make check-all` | Full App | Sequence of: clean build $\rightarrow$ code lint $\rightarrow$ all tests. Must pass before any merge. |

---

## 2. Local Pre-Commit Syntax Validation
Before committing Python modules to staging, builders must execute a pre-compile check to guarantee that code contains no syntax errors:
```bash
python -m py_compile path/to/file.py
```
If this command fails, the code is structurally invalid. Under the staging pipeline rules, any change that fails compilation is instantly reverted and the branch deleted.

---

## 3. Sandboxed Test Executions
To prevent tests from mutating the developer's local environment, all test executions of untrusted or AI-generated modules are isolated:
* **The Sandbox HTTP Relay:** Tests are triggered by sending a payload to the sandbox container's HTTP server (`http://sandbox:9999/execute`).
* **Environment Isolation:** The sandbox has **no internet access**, has a hard memory limit of **512MB**, and has a CPU execution limit of **1.0 core**. 
* **State Persistence:** The sandbox mounts the staging path read-only, ensuring that test scripts cannot corrupt the root repository state.

---

## 4. CI Workflow Configuration (GitHub Actions)
Every pull request opened against OpenTendril triggers the `ci.yml` GitHub Actions workflow.

### CI Verification Sequence:
1. **Runner Boot:** Spins up an isolated Linux runner environment.
2. **Environment Setup:** Installs Python 3.11, Go 1.23, and runs Docker daemon services.
3. **Dependency Build:** Restores cache layers, builds the Go CLI, and installs virtualenv packages.
4. **Execution Gate (`check-all`):** Runs `make check-all`.
5. **Report & Blocking:** If any test fails, the status check is marked red and the PR merge button is disabled.
