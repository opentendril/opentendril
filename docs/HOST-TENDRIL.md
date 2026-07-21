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

## Trust Boundary: Why Workspace Config Cannot Escalate to Host

A key architectural principle is that **workspace-resident Sprouts cannot grant themselves host execution privileges**, even if they can write to `.tendril/` files in the workspace.

The Stem enforces this with a two-layer gate:

1. **Field stripping at parse time:** When `substrates.yaml` is loaded from a workspace path (`./.tendril/substrates.yaml` or `./substrates.yaml`), the Stem's `resolveSubstrateExecutionPlan` function strips the `provider` and `command` fields before they reach the Terrarium factory. Any `provider: host` declared in a workspace config silently falls back to `provider: docker`.

2. **System Config path required:** For `provider: host` to be honoured, the `substrates.yaml` must be loaded from a **System Config path** (`~/.opentendril/substrates.yaml` or `/etc/opentendril/substrates.yaml`). These directories are **never mounted** into any Terrarium container, making them physically inaccessible to any Sprout.

3. **Runtime environment gate:** Even if both conditions above are met, the Stem refuses to start a host Terrarium unless `TENDRIL_ALLOW_HOST_EXECUTION=true` is present in the Stem's runtime environment — a decision that must be made by a human operator.

This means the attack chain `a Sprout writes substrates.yaml → poisons provider: host → escalates to host execution` is blocked at every layer.


---

## Running the Stem as its own principal (a worked setup)

The reasoning is in `SECURITY.md`; this is the shape of it on a Linux Terroir.
Check the result with `tendril hardiness`, which is the point of that command.

```bash
# 1. An unprivileged user that owns nothing but its own control plane.
sudo useradd --system --create-home --shell /usr/sbin/nologin tendril
sudo install -d -o tendril -g tendril -m 700 /home/tendril/.tendril

# 2. Rootless container access FOR THAT USER — not the docker group.
sudo loginctl enable-linger tendril
sudo -u tendril -H dockerd-rootless-setuptool.sh install
# DOCKER_HOST then points at that user's own socket:
#   unix:///run/user/$(id -u tendril)/docker.sock
```

A service unit, with the credential path and the rootless socket in its
environment rather than in a shell profile:

```ini
[Unit]
Description=OpenTendril Stem
After=network-online.target

[Service]
User=tendril
Group=tendril
WorkingDirectory=/home/tendril
Environment=DOCKER_HOST=unix:///run/user/998/docker.sock
Environment=OPENTENDRIL_API_KEY_FILE=/home/tendril/.tendril/api-key
ExecStart=/usr/local/bin/tendril serve
Restart=on-failure

# The Stem should not be able to reach what it does not need.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/home/tendril
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes

[Install]
WantedBy=multi-user.target
```

Two things this does **not** do, stated so they are not assumed:

* **It does not stop you.** The Botanist administering the machine can always
  become the Stem's user. The boundary is against the accounts that host
  Pollinators, so keep those separate from the one you administer with — and do
  not grant that account passwordless `sudo` to the Stem's user, remembering that
  a cached `sudo` timestamp counts as passwordless for anything running as you in
  that window.
* **It does not narrow the network surface.** The Representational State Transfer
  surface binds all interfaces. On a Ramet reachable from a network, put it behind
  something that terminates and restricts, and issue a Pollinator credential per
  caller so revocation is per-caller rather than a shared secret rotation.
