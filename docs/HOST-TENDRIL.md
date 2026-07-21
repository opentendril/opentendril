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

## Hardened deployment: rootless containers and a Stem of its own

This is the full sequence for the strongest isolation Tendril supports on a Linux
Terroir. Every stage ends with something to check, because a deployment that
*looks* right and is not is worse than one you know is weak — that is what
`tendril hardiness` exists to tell you.

**Do the stages in order.** Container access comes first: a user in the `docker`
group with a rootful daemon is already root-equivalent, so creating a separate
Stem user before fixing that would look like progress and change nothing.

Commands marked **[root]** need `sudo`; the rest run as the named user.

---

### Stage 1 — Rootless containers

Membership of the `docker` group lets a member bind-mount the whole filesystem
into a container and read or write anything **as root**, whatever a file's owner
and mode say. Rootless removes that: the daemon runs as an ordinary user and a
container cannot become root on the host.

```bash
# [root] prerequisites
apt-get update && apt-get install -y uidmap slirp4netns

# as the user that will run the daemon
dockerd-rootless-setuptool.sh install --force   # --force only if a rootful daemon is running
systemctl --user enable --now docker
export DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock
```

```bash
# [root] the daemon must survive logout — required for anything unattended
loginctl enable-linger <user>
```

**Check:** `docker info --format '{{.SecurityOptions}}'` contains `rootless`.

The rootless daemon has its **own image store**, so it starts empty. Tendril
rebuilds Sprout images on demand, so this costs a slow first run rather than any
manual work. To confirm the build path before you depend on it:

```bash
docker build -f sprouts/go/Dockerfile -t opentendril-go:latest .
```

#### What rootless costs, measured

Tendril sets three container limits, and all three are enforced under rootless.
Verified with its own passthrough values:

| Limit | Requested | Applied |
|---|---|---|
| `--cpus 1.0` | 1 core | `cpu.max: 100000 100000` |
| `--memory 2048m` | 2 GiB | `memory.max: 2147483648` |
| `--pids-limit 512` | 512 | `pids.max: 512` |

The daemon warns that `io.max` is unsupported. That is **block-IO throttling,
which Tendril never requests** — there is no `--device-read-bps` or
`--blkio-weight` anywhere in the codebase. Nothing Tendril relies on is silently
dropped. The consequence to be aware of: there is no disk-throughput bound on a
Sprout, as was also true under a rootful daemon. Where that matters, the
Firecracker provider bounds it by construction, and needs only `/dev/kvm`
(group `kvm`, which is not root-equivalent).

Only once the build above succeeds:

```bash
# [root] drop the root-equivalence, then log out and back in
gpasswd -d <user> docker
```

**Check:** `tendril hardiness` reports *"Container access is rootless"*.

---

### Stage 2 — A Stem user of its own

Until this stage the Stem shares a principal with its callers, so a Pollinator can
read the credentials, rewrite `grants.yaml`, and ignore `tendril` entirely.
Delegation is advisory. This is what makes it enforced.

```bash
# [root] an ordinary (not system) user: rootless containers need a real user
#        session, and a UID below 1000 complicates that for no benefit.
adduser --disabled-password --gecos "OpenTendril Stem" tendril
install -d -o tendril -g tendril -m 700 /home/tendril/.tendril

# [root] rootless containers need a subordinate id range; adduser adds this
#        automatically when SUB_UID_MIN/COUNT are set in /etc/login.defs.
grep -E "^tendril:" /etc/subuid /etc/subgid   # must print two lines

# [root] the Stem's daemon and service must survive logout
loginctl enable-linger tendril
```

Then repeat **Stage 1** as `tendril`:

```bash
# [root]
sudo -u tendril -H XDG_RUNTIME_DIR=/run/user/$(id -u tendril) \
  dockerd-rootless-setuptool.sh install
sudo -u tendril -H XDG_RUNTIME_DIR=/run/user/$(id -u tendril) \
  systemctl --user enable --now docker
```

**Check:** `sudo -u tendril -H DOCKER_HOST=unix:///run/user/$(id -u tendril)/docker.sock docker info --format '{{.SecurityOptions}}'` contains `rootless`.

#### Move the credentials to their new owner

The point of the whole exercise is that these become unreadable to the accounts
that host Pollinators.

```bash
# [root]
mv ~<botanist>/.tendril/*.pem /home/tendril/.tendril/
chown -R tendril:tendril /home/tendril/.tendril
chmod 700 /home/tendril/.tendril
chmod 600 /home/tendril/.tendril/*.pem
```

---

### Stage 3 — Run the Stem as a service

```ini
# [root] /etc/systemd/system/tendril.service
[Unit]
Description=OpenTendril Stem
After=network-online.target

[Service]
User=tendril
Group=tendril
WorkingDirectory=/home/tendril
# The rootless socket belongs to the tendril user; linger keeps it present.
Environment=DOCKER_HOST=unix:///run/user/1001/docker.sock
Environment=XDG_RUNTIME_DIR=/run/user/1001
ExecStart=/usr/local/bin/tendril serve
Restart=on-failure

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

Replace `1001` with `id -u tendril`. Then `systemctl daemon-reload && systemctl enable --now tendril`.

---

### Stage 4 — Give each Pollinator a provable identity

```bash
# as tendril
tendril pollinator issue --pollen claude --note "laptop"
```

The secret prints once. The Pollinator presents it as its bearer token, and the
Stem **derives** the Pollen from it — a header claim is ignored for such callers,
and an unknown or revoked credential is refused rather than treated as an
ordinary request. Grants still decide what that identity may do.

Revocation is per Pollinator and immediate:

```bash
tendril pollinator revoke --pollen claude
```

---

### Stage 5 — Administer without undoing it

`sudo -u tendril -i` is the natural way in, with one condition that decides
whether any of the above means anything:

**If the account that hosts Pollinators can `sudo` to `tendril`, there is no
boundary.** A Pollinator running as that account simply becomes the Stem. Two
details make this sharper than it looks:

* `NOPASSWD` hands the Stem's identity to anything running as you.
* `sudo` **caches credentials** for about fifteen minutes by default, so a recent
  authentication counts as passwordless for anything running as you in that
  window.

```sudoers
# [root] visudo — require a password every time, no cached ticket
Defaults:botanist  timestamp_timeout=0
botanist ALL=(tendril) PASSWD: ALL
```

Better still, administer `tendril` from a session that does not host Pollinators.

**Check:** `tendril hardiness` reports no weak conditions.

---

### What this does not do

Two limits, stated so they are not assumed away:

* **It does not stop the Botanist.** Whoever administers the machine can become
  the Stem's user. The boundary is against the accounts that host Pollinators.
* **It does not narrow the network surface.** The Representational State Transfer
  surface binds all interfaces. On a Ramet reachable from a network, put it behind
  something that terminates and restricts, and issue a credential per Pollinator
  so revocation is per-caller rather than a shared-secret rotation.

### A consequence worth planning for

Once the Stem runs as `tendril`, it can no longer read a Botanist's home
directory — so a Substrate configured with `checkout: { mode: path }` pointing at
your own clone **will stop working**. That mode was always "operate in the
Botanist's working copy"; with a separate principal, use `mode: managed`, which
gives the Stem its own clone under its own home. Isolated Pollinator workspaces
move with it, to `/home/tendril/.tendril/workspaces/`.
