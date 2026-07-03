# Host Terrariums & External Integrations

OpenTendril primarily uses **Docker** (or gVisor/Firecracker) to run "Sprouts" in isolated Terrariums. However, sometimes you want a Tendril (the agent logic) to run **directly on the Stem host** so that it can access locally installed CLI tools (like `codex`, `firebase`, or `gh`) using your existing, authenticated credentials.

For this, OpenTendril provides the `host` Terrarium provider.

## Security Posture: Default Deny

> [!CAUTION]
> **The host provider is disabled by default.** Even if you configure a substrate using `provider: host` in `substrates.yaml`, the OpenTendril Stem will **refuse to start the Terrarium** and emit a fatal error unless the following environment variable is explicitly set:
>
> ```
> TENDRIL_ALLOW_HOST_EXECUTION=true
> ```
>
> This is a deliberate **default-deny** security gate at the engine level. It cannot be overridden by configuration alone — it requires a runtime decision by the operator.

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
