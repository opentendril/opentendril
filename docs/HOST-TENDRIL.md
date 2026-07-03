# Host Terrariums & External Integrations

OpenTendril primarily uses **Docker** (or gVisor/Firecracker) to run "Sprouts" in isolated Terrariums. However, sometimes you want a Tendril (the agent logic) to run **directly on the Stem host** so that it can access locally installed CLI tools (like `codex`, `firebase`, or `gh`) using your existing, authenticated credentials.

For this, OpenTendril provides the `host` Terrarium provider.

## Security Warning
> [!WARNING]
> **Use With Caution:** The `host` provider runs processes with the full permissions of the user running the OpenTendril Stem. 
> Only route trusted internal sequences (like delegating prompts to Codex) to a host substrate. Never allow an autonomous agent to execute arbitrary shell commands via the host provider.

## Configuring a Host Substrate

To use the host provider, you must explicitly opt-in by configuring a substrate in your `.tendril/substrates.yaml` file (or globally in `~/.config/opentendril/substrates.yaml`).

```yaml
# substrates.yaml
substrates:
  # 'codex-host' is the name referenced in your sequences
  codex-host:
    provider: host
    path: .
    # The actual command the Stem will invoke on the host. 
    # This must be an OpenTendril worker script that listens 
    # to stdin/stdout (e.g., the standard Python Sprout).
    command: ["python3", "tendrils/python/src/main.py"]
```

## Creating a Delegation Sequence

Once your substrate is configured, you can route tasks to it using a sequence definition. For example, `.tendril/sequences/codex-delegate.yaml`:

```yaml
version: 1.0
name: codex-delegate
description: Passes a prompt to the Codex CLI via a Host Tendril
parameters:
  - name: transcript
    required: true
steps:
  - id: codex-run
    description: "Invoking Codex CLI on the host"
    substrate: codex-host
    execute:
      tool: execCommand
      arguments:
        # The Python Sprout will execute this command natively on your host
        command: 'codex --approval-mode full-auto -q "{{ transcript }}"'
```

Because the Python Sprout is running directly on your machine instead of inside a Docker container, the `execCommand` tool will invoke `codex` directly from your local `$PATH`, perfectly passing through authentication tokens without complex mounting.
