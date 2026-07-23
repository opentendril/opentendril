# Host Terrariums & External Integrations

OpenTendril primarily uses **Docker** (or gVisor/Firecracker) to run "Sprouts" in isolated Terrariums. However, sometimes you want a Tendril (the Sprout logic) to run **directly on the Stem host** so that it can access locally installed CLI tools (like `codex`, `firebase`, or `gh`) using your existing, authenticated credentials.

For this, OpenTendril provides the `host` Terrarium provider.

> 📐 **Architecture diagrams:** See [ARCHITECTURE-TAXONOMY.md](./ARCHITECTURE-TAXONOMY.md) for full visual diagrams of the Terrarium Provider hierarchy and security trust boundaries.

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
    command: ["python3", "sprouts/python/src/main.py"]
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

---

## Trust Boundary: What Actually Gates Host Execution

Host execution is gated by **one** control, and it is worth being exact about
which, because a wrong belief here is more dangerous than no belief.

**The runtime environment gate.** The Stem refuses to start a host Terrarium
unless `TENDRIL_ALLOW_HOST_EXECUTION=true` is present in its own runtime
environment. Configuration alone can never enable host execution; the decision
has to be made by an operator, outside any file a Sprout can reach.

What decides *which* substrates run that way is ordinary configuration. So the
question that matters is **who can write that configuration** — never where it
sits. A path confers no privilege by being in one directory rather than another.

Two properties make the answer safe on a sound installation:

* The Stem resolves `substrates.yaml` relative to **its own working directory**,
  not to the workspace a Sprout is editing. A Sprout works in a managed checkout;
  the Stem reads its own control plane. The two are different directories.
* That control plane belongs to the Stem's own principal, so no account hosting
  a Pollinator can write it — which is exactly what
  [GUIDE-INSTALL.md](./GUIDE-INSTALL.md) means by requiring that the Stem's working
  directory is not a repository checkout.

`tendril hardiness` measures this. It reports when substrate configuration is
writable by another principal, and escalates that to a weak finding when host
execution is also indicated:

```console
⚠️  Substrate configuration is writable by others AND host execution is enabled
```

## Relationship to a governed installation

Running a Tendril directly on the Stem host means the Sprout executes with the
Stem's own credentials and reach. On an installation where the Stem runs as its
own principal precisely so that no caller can read what it holds, a host
substrate hands a Sprout exactly what that separation was built to withhold.

That is not a reason to avoid the host provider — it is the reason it is
default-deny. Use it where a locally authenticated command-line tool is the whole
point, keep the substrate declaration somewhere a Sprout cannot reach, and treat
enabling it as a decision about that specific Terroir rather than a default.

Installing and hardening a Ramet is covered in [GUIDE-INSTALL.md](./GUIDE-INSTALL.md), which
is the source of truth for what a sound installation is.
