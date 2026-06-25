# 🧪 Hormonal Triggers (Pre-Tendril Security)

In botany, a **Hormonal Trigger** occurs at the plant's Meristem (the cellular manufacturing hub). It is the genetic "switch" that evaluates if a dormant bud is allowed to mutate and sprout into a Tendril based on its programming and the environment.

In OpenTendril's Pure Micro-Agent Architecture, this directory serves as your **Outbound Biological Firewall**. It acts as an absolute gatekeeper for the creation of any new sub-agent.

## How It Works

When an orchestrating agent invokes the `sprout_tendril` capability (The Meristem), the framework pauses. It serializes the requested `Genotype` and `Transcript` into a JSON payload and executes every script inside this directory, passing the path to the JSON file as the first argument (`$1`).

- If all scripts exit with `0` (Success): The hormonal levels are correct, and the sub-tendril is allowed to sprout (the Docker container starts).
- If any script exits with `>0` (Failure): The sprout is **blocked**. The bud remains dormant, the sub-tendril is never born, and the script's `stderr` output is fed directly back into the orchestrator's LLM context window so it understands why the biological action failed.

## Usage

Simply drop executable scripts (`.sh`, `.py`, `.js`) into this directory.

**Examples:**
- `restrict-internal-network.sh`: Parses the task prompt for internal IP ranges (`127.0.0.1`, `10.0.0.0/8`) and blocks the sprout if found.
- `enforce-github-read-only.py`: Intercepts attempts to sprout a `github-tendril`. If the task involves "commit" or "push" but the repository is marked as read-only, it aborts the sprout.

> **Important:** Ensure your scripts are marked as executable (`chmod +x script.sh`)!
