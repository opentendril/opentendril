# ⚡ Signal Transduction (Local CI & Enforcement)

In botany, **Signal Transduction** is the sensory system that detects a touch and triggers a biological response. 

In OpenTendril, this directory serves as your **Local Continuous Integration (CI)** pipeline. It acts as an absolute gatekeeper for any code an autonomous Sprout tries to commit.

## How It Works

When a Sprout finishes its work and attempts to execute the `git_commit` tool, the orchestrator physically pauses the execution and runs every executable script inside this directory against the Git repository.

- If all scripts exit with `0` (Success): The commit goes through.
- If any script exits with `>0` (Failure): The commit is **blocked**. The Sprout's growth is inhibited, and the `stderr` output of the script is fed directly back into the Sprout's context window so it can fix the code and try again.

## Usage

Simply drop executable scripts (`.sh`, `.py`, `.js`) into this directory.

**Examples:**
- `naming-convention-check.sh`: Scans staged files for underscores and fails if they aren't kebab-case.
- `run-linter.sh`: Runs `eslint` or `flake8`.
- `run-tests.sh`: Runs the local unit test suite (`pytest`, `jest`, `go test`).

> **Important:** Ensure your scripts are marked as executable (`chmod +x script.sh`)!
