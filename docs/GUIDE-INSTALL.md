# Installing OpenTendril

This guide installs the most hardened configuration OpenTendril supports: the
Stem running as its own operating-system principal, holding credentials no
Pollinator can read, on a Linux Terroir.

**It is one worked example, not the definition of a correct installation.** What
makes an installation sound is a set of properties — the *invariants* below — and
several of the choices made here could be made differently without weakening any
of them. Where that is true, the step says so, and the [Variations](#variations)
section shows what changes.

Whether your installation actually holds those properties is not a matter of
having followed the steps. It is measurable:

```bash
tendril hardiness
```

**That command reports; it does not gate.** It tells you what is true about this
Terroir and stops there. A weak finding may describe something you chose
knowingly — running the Stem as yourself on a laptop is a legitimate
configuration, not a failed installation. Hardening is opt-out here, so the
report informs your judgement rather than substituting for it. Its exit status is
always zero.

---

## The invariants

Five properties decide whether delegation is *enforced* by the operating system
or merely *recorded*. Every step below names the one it serves.

| | Invariant | Why it decides the boundary |
|---|---|---|
| **P1** | The Stem's control plane belongs to a principal distinct from every account that hosts Pollinators | If a caller owns the control plane it can rewrite `grants.yaml` and ignore the binary entirely |
| **P2** | No Pollinator-hosting account can *become* that principal | Escalation makes ownership cosmetic — a caller that can `sudo` to the Stem simply is the Stem |
| **P3** | No Pollinator-hosting account can read the Stem's credential material | A caller that can read a credential uses it directly, without asking the Stem and without appearing in the audit lane |
| **P4** | A caller proves its Pollen with an issued credential rather than declaring one | A declared identity constrains only callers who choose to be honest |
| **P5** | No Pollinator-hosting account can write the binary the Stem executes, or anything on its resolution path | Credential ownership is worthless if the binary enforcing it can be replaced before the next start |

Anything else this guide recommends is **preference**, and is labelled as such.

---

## The design space

These axes can be set differently without moving any invariant. This guide
instantiates the value in bold; [Variations](#variations) covers the rest.

| Axis | Options | Constrained by |
|---|---|---|
| Binary provenance | **verified precompiled release** / compiled from source / package manager | — |
| Build principal | **none — the release is obtained, not compiled** / a separate build account / the Stem's own account | — |
| Binary location | **the Stem's own home** / a system location | P5 only |
| Lifecycle | **system service** / user service / foreground | — |
| State directory | **a conventional home** / a service state directory | — |
| Container access | **rootless daemon** / Firecracker / rootful daemon | **P2 — genuinely constrained** |

Container access is the one axis that can destroy an invariant on its own.
Membership of a container-daemon group with a **rootful** daemon is
root-equivalent: a member can bind-mount the whole filesystem and read or write
anything as root, whatever file ownership says. That defeats P1, P2 and P3
together, so a rootful daemon is not a supported variation.

---

## Before you start: the control plane is a working directory

This is the single fact that most often produces an installation that looks right
and is not.

`tendril serve`, `tendril pollinator`, `tendril hardiness` and the Model Context
Protocol surface all resolve the control plane as `./.tendril` — relative to the
**current directory of the process**, not to anyone's home. The environment file
is read the same way: `./.env` and nothing else.

Two consequences to hold on to:

* Every command marked *as tendril* below must be run from `/home/tendril`. Run
  `tendril pollinator issue` from somewhere else and it writes a credential store
  the running Stem will never read — and nothing will tell you.
* **The Stem's working directory must not be a repository checkout.** A checkout
  has its own `.tendril/` holding tracked genome, genotypes and sequences;
  pointing the control plane there would drop the bearer key, the credential
  store and `grants.yaml` into version control, beside files a Sprout can edit.

Three paths resolve against **home** rather than the working directory. Because
this guide sets them all to `/home/tendril`, they coincide:

| Path | Holds |
|---|---|
| `/home/tendril/.tendril/substrates/<name>` | the Stem's own clone (managed base) of each Substrate |
| `/home/tendril/.tendril/run-workspaces/` | run-scoped managed Sprout worktrees |
| `/home/tendril/.tendril/workspaces/` | delegated per-Pollen workspaces |

---

## The two-working-copy model

A separate principal cannot read your home directory, so it cannot work in your
clone. It gets its own, and **the remote is the only thing the two share**.

```text
   your account                              the Stem (tendril)
   ~/…/opentendril                           ~/.tendril/substrates/opentendril (managed base)
   you edit here                                        │
             │                                          ├── run workspace A → Fruit A
             │                                          ├── run workspace B → Fruit B
             │  push / fetch                            │  publish managed Fruit
             └──────────────►  remote  ◄────────────────┘
                        source of truth
```

* **A managed Sprout makes a change** → it uses the Tendril-owned managed base as Git backing state, executes writable work in an independent `~/.tendril/run-workspaces/` worktree, and produces managed Fruit on a `sprout/task-<stepID>` branch.
* **A Pollinator invokes delegated Git capabilities** → it works in a per-Pollen delegated workspace under `~/.tendril/workspaces/`. Operations like `git.commit`, `git.push`, and `git.pr` belong to this delegated Git ladder; they are NOT managed Sprout RunWorkspaces.
* **You make a change** → you work in your own clone and push to the remote. The Stem picks it up on its next run: the managed base is fetched and hard-reset to the target branch.

> [!WARNING]
> **Never hand-edit the Stem's clone.** It is Tendril-owned backing state and may
> be refreshed/reset. That reset applies to the backing checkout and does not
> discard active RunWorkspace state. Your clone is the one you edit.

> [!IMPORTANT]
> **Multiple managed runs may execute concurrently against the same Substrate.**
> - The managed clone is the persistent Tendril-owned base.
> - Each writable managed run gets an independent RunWorkspace.
> - Multiple managed runs may be in execution against the same managed Substrate without sharing a working tree.
> - The shared base is used only for backing Git state and short metadata operations.
> - Each managed run that produces committed reviewable Fruit retains/publishes its own `sprout/task-<stepID>` Fruit.
> - The Botanist reviews each Fruit separately.

The boundary and the workflow are the same mechanism here — nothing needs to be
shared between the accounts, because everything already travels through the
remote.

---

# The procedure

Commands marked **[root]** need `sudo`; the rest run as the named user. **Do the
stages in order** — container access comes first, because a user in a
root-equivalent group makes every later stage cosmetic.

Prerequisites for the normal path:
- A Terrarium provider. This guide instantiates rootless Docker Engine in Stage 2.
- Git, where the Stem clones and manages Substrates.
- An LLM — local [Ollama](https://ollama.ai) by default, or a cloud provider key.
- Access to the target GitHub repository.
- Authority necessary to create and install the GitHub App used by the secure-default path.
- Access to GitHub's web UI from a browser, which may be on a different trusted administrative machine from the headless Stem host.

The normal path obtains a **verified precompiled OpenTendril release**. It does
not clone this repository, does not install Go, and does not install GNU Make.
Those belong only to the [source-build variation](#compile-from-source).

A governed installation that connects an MCP-speaking Pollinator uses two
executables. They are not interchangeable.

| Executable | Role |
|---|---|
| **`tendril`** | The full Stem/operator executable. It may construct and run a Stem. It is owned by the Stem principal and is not on any Pollinator-hosting account's PATH. |
| **`tendril-mcp`** | The restricted MCP client/bridge. The ordinary Pollinator-hosting account may run it. It holds that Pollinator's durable root, mints and caches short-lived access tokens derived from it, holds no Botanist or Stem credential or authority, cannot construct a Stem, authenticates to the separately owned governed Stem, and receives only authority derived from that Stem. |

Stage 3 places `tendril` under the Stem principal. Stage 8 installs `tendril-mcp`
for the ordinary account.

---

## Stage 1 — Create the Stem's principal

> **Serves P1.** Axis: state directory (*free*) — this guide instantiates a
> conventional home at `/home/tendril`.

```bash
# [root] an ordinary user, not a system user: rootless containers need a real
#        user session, and a UID below 1000 complicates that for no benefit.
adduser --disabled-password --gecos "OpenTendril Stem" tendril

# [root] rootless containers need a subordinate id range
grep -E "^tendril:" /etc/subuid /etc/subgid   # must print two lines

# [root] the daemon and the service must survive logout
loginctl enable-linger tendril
```

If the `grep` printed nothing, add the ranges before continuing — rootless Docker
cannot start without them:

```bash
# [root]
usermod --add-subuids 165536-231071 --add-subgids 165536-231071 tendril
```

**Check:** `id tendril` resolves and both subordinate ranges print.

---

## Stage 2 — Rootless containers for that principal

> **Serves P2.** Axis: container access — **constrained**. A rootful daemon is
> not a supported alternative; Firecracker is (see Variations).

A rootless daemon runs as an ordinary user, so a container cannot become root on
the host. This is what stops container access from being an escalation path.

On Ubuntu 24.04 LTS this stage needs **Docker Engine** from
https://download.docker.com/linux/ubuntu: `docker-ce`, `docker-ce-cli`,
`containerd.io`, and `docker-ce-rootless-extras`. `docker-ce-rootless-extras`
is the package that provides `dockerd-rootless-setuptool.sh`.

> [!CAUTION]
> **Do not `apt-get install docker.io`.** Ubuntu's `docker.io` package is not
> the governed path: it does not ship `dockerd-rootless-setuptool.sh`, and it
> starts a rootful daemon. If `docker.io` is already installed, remove it
> before adding Docker's repository — the two stacks conflict.

```bash
# [root] Ubuntu 24.04 LTS — Docker Engine. docker-ce-rootless-extras provides
#        dockerd-rootless-setuptool.sh.
apt-get remove -y docker.io
apt-get update && apt-get install -y ca-certificates curl
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
tee /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras
```

```bash
# [root] rootless prerequisites
apt-get update && apt-get install -y uidmap slirp4netns dbus-user-session
```

If `command -v dockerd-rootless-setuptool.sh` prints nothing, the wrong Docker
package is installed. Ubuntu `docker.io` does not provide the script. Docker
Engine's `docker-ce-rootless-extras` from
https://download.docker.com/linux/ubuntu does.

The `docker-ce` package starts a rootful `docker.service`. If that unit is
present — from this install or from leftover `docker.io` — disable it before
the setuptool:

```bash
# [root] rootful docker.service must be off before the setuptool
systemctl disable --now docker.service docker.socket
```

```bash
# [root] install and start the Stem's own daemon
sudo -u tendril -H XDG_RUNTIME_DIR=/run/user/$(id -u tendril) \
  dockerd-rootless-setuptool.sh install
sudo -u tendril -H XDG_RUNTIME_DIR=/run/user/$(id -u tendril) \
  systemctl --user enable --now docker
```

**Check:**

```bash
sudo -u tendril -H DOCKER_HOST=unix:///run/user/$(id -u tendril)/docker.sock \
  docker info --format '{{.SecurityOptions}}'
```

Output must contain `rootless`.

### What rootless costs

All three container limits Tendril sets are enforced under rootless:

| Limit | Requested | Applied |
|---|---|---|
| `--cpus 1.0` | 1 core | `cpu.max: 100000 100000` |
| `--memory 2048m` | 2 GiB | `memory.max: 2147483648` |
| `--pids-limit 512` | 512 | `pids.max: 512` |

The daemon warns that `io.max` is unsupported. That is block-IO throttling, which
Tendril never requests — there is no `--device-read-bps` or `--blkio-weight`
anywhere in the codebase, so nothing it relies on is silently dropped. The
consequence to know: there is no disk-throughput bound on a Sprout. Where that
matters, the Firecracker provider bounds it by construction and needs only
`/dev/kvm`, whose group is not root-equivalent.

The rootless daemon has its own image store and starts empty; Tendril rebuilds
Sprout images on demand, so this costs a slow first run rather than any work.

---

## Stage 3 — Obtain and place the binary

> **Serves P5.** Axes: binary provenance, build principal and binary location
> (*all free*) — this guide instantiates *a verified precompiled OpenTendril
> release, installed into the Stem's own home*.

What P5 requires is only that no Pollinator-hosting account can write the binary
or any directory on the path used to reach it. A binary owned by `tendril` inside
a home that other accounts cannot traverse satisfies that; so does a root-owned
binary in a system location. Neither is more secure than the other, and the
choice is logistics — see Variations.

The current OpenTendril release is **v0.3.0**. Each platform archive contains two
independent executables, `tendril` and `tendril-mcp`, and nothing else. This
worked path is Linux amd64. The same release also publishes
`opentendril-linux-arm64.tar.gz`, `opentendril-darwin-amd64.tar.gz`, and
`opentendril-darwin-arm64.tar.gz`. Substitute the matching archive name; the
checksum file and verification step are the same.

Obtain the archive and `checksums.txt` from the same release. Verify the
selected archive **before** extracting or installing it. Then install **only**
`tendril` into the Stem principal's executable path. Do not place the full
`tendril` executable on a Pollinator-hosting account's PATH. Stage 8 installs
the restricted `tendril-mcp` client for that account.

```bash
# [root] Linux amd64. Verify the archive before extracting or installing it.
RELEASE=v0.3.0
ARCHIVE=opentendril-linux-amd64.tar.gz
WORKDIR=$(mktemp -d)
cd "$WORKDIR"
curl -fsSL -o "$ARCHIVE" \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/${ARCHIVE}"
curl -fsSL -o checksums.txt \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/checksums.txt"
# Must print: opentendril-linux-amd64.tar.gz: OK
# Stop here if it does not.
grep "${ARCHIVE}$" checksums.txt | sha256sum -c
tar -xzf "$ARCHIVE" tendril
# 0750 and not 0755: no account other than the Stem should run this binary,
# so no account other than the Stem is given the ability.
install -d -o tendril -g tendril -m 750 /home/tendril/.local/bin
install -o tendril -g tendril -m 750 tendril /home/tendril/.local/bin/tendril
cd /
rm -rf "$WORKDIR"
```

Do not copy this binary onto a Pollinator-hosting account's PATH, and do not run
`make install` for that account. That target is the single-user full-binary
install. The ordinary account installs `tendril-mcp` in Stage 8.

```bash
# [root] the home itself must not be traversable by other accounts
chmod 750 /home/tendril
```

**Check**, as the Stem:

```bash
sudo -u tendril -i tendril hardiness
```

Among the findings you should see the executable-integrity line reporting that
nothing on the resolution chain is writable by others.

To replace these executables later, follow [Upgrade](#upgrade). Do not rerun
this stage as a way of reinitializing the Stem.

---

## Stage 4 — Create the control plane

> **Serves P1 and P3.** The directory location within the home is *preference*;
> what matters is that it belongs to the Stem and no caller can read its
> contents.

Everything from here runs **as `tendril`, from `/home/tendril`**.

```bash
# [root]
sudo -u tendril -i
```

```bash
# as tendril
pwd                                     # must print /home/tendril
install -d -m 700 /home/tendril/.tendril
export DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock
```

Create the environment file **before** running the wizard. `tendril init` writes
to `~/.tendril/.env` when no `./.env` exists, and the Stem reads `./.env` — so an
empty file here is what makes the wizard write where the Stem will look:

```bash
# as tendril, in /home/tendril
touch /home/tendril/.env
chmod 600 /home/tendril/.env
ollama pull llama3.2
tendril init
```

The wizard is Ollama-first: it detects a running local instance — including one
with no models pulled yet — and defaults to fully local inference. Decline and
it walks you through a cloud provider and its key.

**Check:** `grep DEFAULT_LLM_PROVIDER /home/tendril/.env` prints your choice.

---

## Stage 5 — Install the credential and configure the connection

> **Serves P3.** The Substrate's `checkout` mode is *constrained by P1* —
> `mode: path` cannot work once the Stem cannot read your home.

Create the GitHub App and download its private key **before** this step.
That procedure is [GUIDE-GIT-CONNECTION.md Path B](./GUIDE-GIT-CONNECTION.md#path-b--github-app).
This stage does not create the App. You must already have the `.pem` file and the
App ID from your browser session.

Because that browser session may be on a different administrative machine, you
must transfer the private key to this headless installation host. **Do not copy
or stage the key through the Pollinator-hosting account** (for example, by using
`scp` into your own home directory or opening a temporary file with your own
user), as doing so exposes the credential to that account.

Use a **fresh** private key for that App. Never carry forward one that has lived
in an account hosting Pollinators: changing a file's owner does not change who
has already copied it.

To keep the credential exclusive to the Stem, write it directly into the
control plane using a privileged command. Paste the private key from your
browser session into this interactive prompt (press `Enter`, then `Ctrl+D` when done):

```bash
# [root] Write the key directly as the Stem principal.
# umask 077 ensures the resulting file is mode 0600.
sudo -u tendril sh -c 'umask 077 && cat > /home/tendril/.tendril/app.pem'
```

*(If you must use a transfer mechanism like `scp`, it must be executed through a
trusted administrative context—such as connecting directly as `root`—that does
not expose the PEM to the Pollinator-hosting account. A shared location like
`/tmp` is not neutral with respect to Unix file ownership.)*

**Check:** `sudo -u tendril test -r /home/tendril/.tendril/app.pem && echo ok`
prints `ok`, while `cat /home/tendril/.tendril/app.pem` as your own account is
denied.

Then write the connection and the grant together. Set `APP_ID` and `REPO` to
the real App ID and `owner/repo` from Path B before running the command. The
shell substitutions refuse to run while either is unset, so the example
identity cannot be executed by accident:

```bash
# as tendril, in /home/tendril
: "${APP_ID:?set APP_ID to the GitHub App ID from Path B}"
: "${REPO:?set REPO to owner/repo the App is installed on}"
tendril git setup \
  --substrate myrepo \
  --repo "$REPO" \
  --posture app \
  --app-id "$APP_ID" \
  --key /home/tendril/.tendril/app.pem \
  --grant-pollen claude
```

`--checkout managed` is the default and the only mode that works under a separate
principal: the Stem cannot read your clone, so `mode: path` pointing at it will
fail. Managed mode gives the Stem its own clone under its own home.

This writes `/home/tendril/substrates.yaml` and
`/home/tendril/.tendril/grants.yaml`. Review the grant — no grant means every
delegated invocation is denied, which is the secure default:

```yaml
# /home/tendril/.tendril/grants.yaml
grants:
  claude:
    # git.prune is deliberately absent: it deletes branches, and every other
    # operation is recoverable. Add it knowingly if you want one tidying up.
    #
    # sprout.watch is the read side: it lets this pollen watch the runs it
    # dispatched — the stored record, the persisted events, and the live
    # stream — and nothing anyone else dispatched. Grant it alongside
    # sprout.grow, or on its own for an observer that may look but not start
    # work. Without it a pollen can begin work it cannot then see.
    operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr, sprout.watch]
    substrates: [myrepo]
    # egress: [github.com]              # optional: hosts the Stem may fetch for this pollen
    # expires: 2027-01-01               # optional: RFC 3339 timestamp or YYYY-MM-DD
    # confirmAbove: { impact: high }    # optional: an invocation crossing this bound returns pending,
    #                                   # not denied. An operator lists/approves/denies via CLI
    #                                   # (`tendril delegation pending`, `tendril delegation approve <id>`).
    #                                   # An approved confirmation is single-use and expires after
    #                                   # an hour if unresolved.
```

**Check:** `tendril git setup --verify --substrate myrepo`
reports the connection ready only after authenticating to the configured
repository. A wrong App ID, unusable private key, missing installation, or
inaccessible repository fails the check.

### Commit signing

*Preference in general — **required** if the target repository demands signed
commits.* Many do, either through a repository ruleset or classic branch
protection, and an unsigned commit is then rejected at push time. Discovering
that during the Stem's first delegated run is a bad way to learn it, so settle it
here.

The two connection postures differ, and this is the practical reason to prefer
the App:

| Posture | Signing | What you configure |
|---|---|---|
| **GitHub App** (`--posture app`) | GitHub signs server-side; commits show **Verified** | nothing — it is automatic |
| Personal Access Token (`--posture pat`) | your own GPG key, signed locally | a dedicated key, and its public half uploaded to GitHub |

With the App posture the Stem commits through the API (`commit: api`), so no key
material exists on the Terroir at all and signing needs no setup. That is the
recommended path and the one this guide instantiates.

If you are using the Personal Access Token posture instead, generate a dedicated
signing key and pass it at setup:

```bash
# as tendril, in /home/tendril
tendril git setup \
  --substrate myrepo --repo myorg/myrepo \
  --posture pat \
  --token-env TENDRIL_GITHUB_PAT \
  --sign-key <gpg-key-id> \
  --identity-name "Tendril Bot" --identity-email "tendril-bot@your-domain"
```

Upload the key's public half to GitHub so its commits verify.
[GUIDE-GIT-CONNECTION.md](./GUIDE-GIT-CONNECTION.md) covers key generation and
both postures in full; it is not repeated here.

**Check:** confirm the target repository's requirement before the first
delegated run, so a rejection is not the way you find out:

```bash
gh api repos/<owner>/<repo>/rulesets --jq '.[].name'
gh api repos/<owner>/<repo>/rulesets/<id> --jq '[.rules[].type]'
```

A `required_signatures` rule means every commit must be signed — including the
Stem's. Note that the classic endpoint
(`repos/<owner>/<repo>/branches/main/protection`) reports nothing when protection
comes from a ruleset, so checking it alone will tell you a repository is
unprotected when it is not.

---

## Stage 6 — Issue a credential per Pollinator

> **Serves P4.**

```bash
# as tendril, in /home/tendril
tendril pollinator issue --pollen claude --note "laptop"
```

The secret prints **once** and is never stored — only its SHA-256 digest is kept,
so a leaked store is not a leaked credential. It begins `tendril_root_`, which makes it
recognisable in a log or a configuration file. That secret is the **durable
refresh root** for this Pollinator.

On a **loopback** bind (the default), the Pollinator may present the durable root
as a bearer token on data routes for local convenience. On an **off-host** bind,
data routes refuse the root — mint a short-lived access token first
(`tendril pollinator token --pollen claude`, or `POST /v1/pollinator/token` with
the root). The Stem **derives** the Pollen from a verified credential or token; a
header claim is ignored for such callers, and an unknown, revoked, expired, or
forged bearer is refused rather than treated as ordinary traffic.

Revocation is at the root and takes effect on the next start: revoke the
credential, minting stops, and outstanding access tokens age out within their
cap (≤15 minutes):

```bash
tendril pollinator revoke --pollen claude
tendril pollinator list
```

> [!IMPORTANT]
> **Credentials and grants are read at startup.** A credential issued while the
> Stem is running is rejected with `401` until it restarts. Issue everything
> first, then start (or restart) the service once.

---

## Stage 7 — Run the Stem as a service

> **Preference.** Axis: lifecycle (*free*). A user service or a foreground
> process satisfies every invariant equally; a system service is chosen here
> because it starts without a login session.

```ini
# [root] /etc/systemd/system/tendril.service
[Unit]
Description=OpenTendril Stem
After=network-online.target

[Service]
User=tendril
Group=tendril
# This is the control plane: .tendril/ and .env resolve against it.
WorkingDirectory=/home/tendril
# The rootless socket belongs to the tendril user; linger keeps it present.
Environment=DOCKER_HOST=unix:///run/user/1001/docker.sock
Environment=XDG_RUNTIME_DIR=/run/user/1001
# Dedicated state directory for the optional local Stem socket.
# Owned by the Stem principal, mode 0755 (not writable by other users).
# Contains only local transport artifacts.
StateDirectory=opentendril-transport
StateDirectoryMode=0755
Environment=TENDRIL_LOCAL_SOCKET=/var/lib/opentendril-transport/stem.sock
ExecStart=/home/tendril/.local/bin/tendril serve
Restart=on-failure

NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
# ProtectHome= is deliberately absent. It empties /home inside the service's
# namespace, and the Stem's binary, control plane and managed checkouts all live
# there — systemd cannot even resolve ExecStart, failing with 203/EXEC.
# ReadWritePaths does not rescue that lookup. Little is lost: the Stem runs as
# its own unprivileged user, so ordinary permissions already keep it out of
# other accounts' homes.
ReadWritePaths=/home/tendril /run/user/1001
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes

[Install]
WantedBy=multi-user.target
```

Replace **both** occurrences of `1001` with `id -u tendril`, then:

```bash
# [root]
systemctl daemon-reload && systemctl enable --now tendril
journalctl -u tendril -f
```

On first start the Stem generates a bearer key at
`/home/tendril/.tendril/api-key`, mode `0600`. That key authenticates **you**,
the Botanist (`BOTANIST_KEY` sets it explicitly when you prefer not to use the
file). It is not what a Pollinator uses.

The daemon binds **loopback by default** (`TERROIR_HOST` unset → `127.0.0.1`).
When `TENDRIL_LOCAL_SOCKET` is unset, no Unix-domain socket is created and TCP
behavior is unchanged. When it is set to an absolute path — the governed unit
sets `/var/lib/opentendril-transport/stem.sock` — the Stem serves the **same
authenticated HTTP mux** on that socket as well as on loopback TCP. The socket
is transport only: connecting to it grants no extra trust. Botanist bearer
checks and Pollinator credential / access-token semantics are unchanged.

`/var/lib/opentendril-transport` is a systemd `StateDirectory` owned by the
Stem principal, mode `0755` (other users cannot replace the socket pathname).
It contains only local transport artifacts: no Botanist key, Pollinator
credentials, grants, provider credentials, or other Stem control-plane state.
The socket file itself is connectable by local clients. It is not under
`/home/tendril`.

The containerized Greenhouse (`docker compose --profile ui up -d`) reaches this
socket through a read-only mount of `/var/lib/opentendril-transport` only. The
browser talks only to the Greenhouse origin (`http://127.0.0.1:4173` by
default). The Stem stays on loopback TCP when `TERROIR_HOST` is unset. See
[GREENHOUSE.md](./GREENHOUSE.md).

To expose the REST surface off-host, set `TERROIR_HOST=0.0.0.0` (or a specific
interface) in the unit's environment — and once off-host, Pollinator data routes
require short-lived access tokens (see Stage 6). Off-host classification follows
the TCP bind only; the local socket does not change it.

**Check:** `curl -s localhost:8080/health` returns a health report.

If the service fails at `203/EXEC` — *"Unable to locate executable"* — a
sandboxing directive is hiding the path rather than the path being wrong. Check
`ProtectHome=` is absent, then `ProtectSystem=`. Confirm the binary is reachable
outside the unit first:

```bash
sudo -u tendril -i test -x /home/tendril/.local/bin/tendril && echo reachable
```

If it starts but cannot reach its container daemon, `ReadWritePaths` is the line
to check: `/run/user/<uid>` must be listed, and `<uid>` must match `id -u tendril`
in all three places it appears.

---

## Stage 8 — Connect the Pollinator

> **Mechanism, not an invariant.** How a Pollinator reaches the Stem is decided
> by the delegation gate rather than by this guide.

The ordinary Pollinator-hosting account does not receive the protected `tendril`
binary. It installs **only** `tendril-mcp` from a verified release archive — the
same bundle Stage 3 used, independently downloaded and checksum-verified on this
account. Do not run `make install-mcp-client` on the normal path.

```bash
# as the ordinary (Pollinator-hosting) account
RELEASE=v0.3.0
ARCHIVE=opentendril-linux-amd64.tar.gz
WORKDIR=$(mktemp -d)
cd "$WORKDIR"
curl -fsSL -o "$ARCHIVE" \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/${ARCHIVE}"
curl -fsSL -o checksums.txt \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/checksums.txt"
# Must print: opentendril-linux-amd64.tar.gz: OK
# Stop here if it does not.
grep "${ARCHIVE}$" checksums.txt | sha256sum -c
tar -xzf "$ARCHIVE" tendril-mcp
install -d -m 755 "$HOME/.local/bin"
install -m 755 tendril-mcp "$HOME/.local/bin/tendril-mcp"
# Do not extract or install tendril. The full Stem executable stays with the Stem principal.
cd /
rm -rf "$WORKDIR"
```

That installs only `~/.local/bin/tendril-mcp` for the invoking account. It does
not install, copy, or expose the full `tendril` Stem binary, and it does not
touch `/home/tendril/.local/bin/tendril`. Ensure `~/.local/bin` is on this
account's PATH.

**Check:**

```bash
command -v tendril
# no result

command -v tendril-mcp
# ~/.local/bin/tendril-mcp
```

> [!CAUTION]
> **Do not use `tendril mcp` here.** That subcommand belongs to the full Stem
> executable. A governed installation does not put that binary on the ordinary
> account, and must not. The supported MCP client for this installation is
> `tendril-mcp`.

`tendril-mcp` is a stdio MCP bridge. An MCP-speaking Pollinator launches it; it
loads that Pollinator's durable root, mints a short-lived access token with the
governed Stem, and forwards MCP frames only after that preflight succeeds.
Authorization and Pollen derivation stay at the Stem. The client cannot
construct a Stem and has no in-process mode.

Credential lookup, first match wins:

1. `TENDRIL_POLLINATOR_CREDENTIAL`
2. `TENDRIL_MCP_CREDENTIAL`
3. `~/.config/tendril/pollinators/<TENDRIL_POLLEN>`

Startup fails closed when there is no credential, the credential file is unsafe,
no Stem answers, ownership is not established, the answering Stem has the
caller's UID, or the Stem refuses the root. Only after all of those checks pass
does MCP forwarding begin.

Place the durable root issued in Stage 6 where that lookup will find it, mode
`0600`, owned by the ordinary account. Then point an MCP-speaking Pollinator at
the client:

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril-mcp",
      "env": {
        "TENDRIL_POLLEN": "<pollen>"
      }
    }
  }
}
```

Name the credential file with `TENDRIL_POLLINATOR_CREDENTIAL` or
`TENDRIL_MCP_CREDENTIAL` when the default path is not the one you want.

A credential-bearing Pollinator can also reach the Stem over the
Representational State Transfer surface. It is admitted only on routes that
consult the delegation authorizer per invocation, with the Substrate in hand;
every other route — including the Model Context Protocol endpoint at `POST /v1`
— refuses a direct credential-bearing caller by default rather than running the
request as ordinary traffic:

```console
$ curl -X POST localhost:8080/v1 -H "Authorization: Bearer tendril_root_…" …
HTTP/1.1 403 Forbidden
delegation denied: this endpoint exposes no delegable operation-class
```

The routes a Pollinator may use, each gated by the matching operation-class:

| Route | Operation-class |
|---|---|
| `POST /v1/git/status` | `git.status` |
| `POST /v1/git/branches` | `git.branch.list` |
| `POST /v1/git/branch` | `git.branch` |
| `POST /v1/git/commit` | `git.commit` |
| `POST /v1/git/push` | `git.push` |
| `POST /v1/git/pr` | `git.pr` |
| `POST /v1/git/prune` | `git.prune` |
| `POST /v1/stoma/pass` | `stoma.pass` |
| `POST /v1/seeds/grow` | `seed.grow` |
| `POST /v1/sprouts/grow` | `sprout.grow` |
| `GET /v1/phytomers/{id}/sprout-runs` | `sprout.watch` |
| `GET /v1/phytomers/{id}/events` | `sprout.watch` |
| `GET /ws?sessionId={id}` | `sprout.watch` |

A `sprout.watch` grant releases only what the Pollen's own runs put there, and
only for the substrates those runs targeted. `sprout-runs` narrows to the
caller's own records. A phytomer's events and its live stream are session-wide
and name no owner individually, so they are released whole or not at all: every
run in the phytomer must be the caller's, and a phytomer nothing was dispatched
into belongs to nobody. A delegated stream must name the phytomer it watches and
receives only that phytomer's events; the Botanist key still opens the
unfiltered feed.

```bash
curl -X POST http://localhost:8080/v1/git/status \
  -H "Authorization: Bearer tendril_root_…" \
  -H "Content-Type: application/json" \
  -d '{"substrate":"myrepo"}'
```

**Check:** call a route your grant does not cover and confirm the refusal names
the reason:

```console
$ curl -X POST localhost:8080/v1/git/prune -H "Authorization: Bearer tendril_root_…" …
HTTP/1.1 403 Forbidden
delegation denied: no active grant covers Pollen "claude",
operation-class "git.prune", substrate "myrepo"
```

The Pollen in that message was derived from the credential, not claimed by the
caller. That is the boundary working.

Direct `POST /v1` remains refused for a credential-bearing caller. The
per-Pollinator MCP path is `tendril-mcp`, which authenticates to the governed
Stem and forwards only after the Stem accepts the root.

### Handing off a bounded Seed

`seed.grow` grows a **Seed** — a bounded intent: a goal, a verify command that
must exit 0, and iteration/time bounds. A Sprout builds toward the goal and the
Stem runs the verify command deterministically in a network-sealed Terrarium;
that exit code — never the Sprout's own claim — is the verdict. The work lands on
a branch for review as **Fruit**; nothing is ever merged.

```bash
# Synchronous — blocks until the Seed settles, then prints the Fruit.
tendril seed grow --substrate myrepo --goal "make the failing tests pass" -- go test ./...

# Asynchronous — hand it to the running daemon and walk away with a handle.
tendril seed grow --substrate myrepo --goal "make the failing tests pass" --async -- go test ./...
#   → Handle: seed-1723488000000000000
tendril seed collect seed-1723488000000000000
```

The daemon routes underneath, each gated by the `seed.grow` operation-class:

| Route | Behaviour |
|---|---|
| `POST /v1/seeds/grow` | grow synchronously; the response body is the Fruit |
| `POST /v1/seeds/grow/async` | dispatch; the response body is a durable `handle` |
| `GET /v1/seeds/runs/{handle}` | collect the Fruit — **only** by the Pollen that dispatched it |

A settled Seed reports `satisfied` (verify passed), `exhausted` (bounds spent), or
`withered` (the Sprout failed). Collection is scoped to the dispatching subject:
one Pollinator can never read another's handle.

---

## Stage 9 — Administer without undoing it

> **Serves P2.**

`sudo -u tendril -i` is the natural way in, with one condition that decides
whether any of the above means anything.

**If an account that hosts Pollinators can `sudo` to `tendril`, there is no
boundary** — a Pollinator running as that account simply becomes the Stem. Two
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

---

## Verify the installation

Run the report from **both** sides. They answer different questions, and only
together do they describe the boundary — so a finding that differs between them
is expected, not a fault.

| Finding | Authoritative side | Why |
|---|---|---|
| Credential exclusivity | **your account** | The question is "can *I* read these?", and only your account can answer it by trying |
| Executable integrity | **the Stem** | A property of the Stem's binary; run as you it measures yours |
| Principal, escalation, host configuration, control-plane reachability | **the Stem** | Properties of the Stem's own environment |

```bash
# as tendril — the Stem's own view
sudo -u tendril -i tendril hardiness
```

Expect no weak findings, ending with: *"This Terroir is hardy: the delegation
boundary is enforced by the operating system."*

A governed Pollinator-hosting account following Stage 8 has `tendril-mcp` and
not `tendril`. That is the intended outcome: `command -v tendril` prints
nothing. The Stem-side report above remains the authoritative P5 reading.

If this account *does* resolve a `tendril` executable — a single-user install,
or a leftover copy — run the caller's-view report from your home:

```bash
# as your own account, from your own home — the caller's view
cd ~ && tendril hardiness
```

That run must report **no readable credential files**. The check opens each
candidate rather than inspecting its mode, because permission can be satisfied
through group membership, and it examines the invoking user's own home as well as
the control plane. If anything ever left credential material in your `~/.tendril`,
this is where it surfaces.

For reference, a **single-principal** installation reports like this — every
finding legible, exit status still zero:

```console
$ tendril hardiness
⚠️  The Stem shares a principal with its callers (dr3w)
     This user owns ./.tendril, so a Pollinator running as this user can
     rewrite grants.yaml, read issued credentials, and bypass the binary entirely.
     Run the Stem as its own operating-system user to make the boundary real.
⚠️  1 credential file(s) are readable by this user
       /home/dr3w/.tendril/app.private-key.pem
     A Pollinator that can read a credential can use it directly, without asking
     the Stem and without appearing in the audit lane.
✅ Nothing on the running binary's resolution chain is writable by others (…/bin/tendril)
ℹ️  No Pollinator credentials issued — every Pollen is DECLARED, not proven
ℹ️  No grants configured — every delegated invocation is denied (secure default)

2 condition(s) mean delegation here is ADVISORY, not enforced.
```

> [!IMPORTANT]
> **Run the report as the Stem for an authoritative P5 reading.** Executable
> integrity is a property of the Stem's own binary; run from your account it
> measures *your* binary, which is a different and less useful question. The two
> runs answer different things and both are correct — see the note below on which
> side is authoritative for which finding.
>
> The Stem records which binary it is running, in
> `/home/tendril/.tendril/stem.json`. That is not for reading across accounts: it
> is because `tendril hardiness` typed at a shell measures the binary *that
> invocation* resolved, which need not be the one `ExecStart` names. With the
> record readable the finding is titled *"The Stem's binary"*; without it, *"This
> invocation's binary"*.

---

## Upgrade

Replacing the installed executables does not reinitialize the Stem. **Do not
rerun `tendril init`.** Existing durable configuration and state remain in
place, including `.env`, `.tendril/`, GitHub App credentials, Pollinator
credentials, grants, Substrate definitions, and other runtime state.

1. Obtain the newer release archive and its `checksums.txt` from the same tag.
2. Verify the archive with SHA-256 **before** replacing anything.
3. Stop the Stem.
4. Replace the protected `tendril` at the Stage 3 path, keeping the same owner
   and mode.
5. If the Pollinator-hosting account has `tendril-mcp`, replace that executable
   the same way as Stage 8.
6. Restart the Stem.
7. Run the existing health and hardiness checks.

Stop the Stem, then replace the protected executable. Substitute the newer
release tag for `RELEASE`.

```bash
# [root] Linux amd64 — substitute the newer release tag.
RELEASE=v0.3.0
ARCHIVE=opentendril-linux-amd64.tar.gz
WORKDIR=$(mktemp -d)
cd "$WORKDIR"
curl -fsSL -o "$ARCHIVE" \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/${ARCHIVE}"
curl -fsSL -o checksums.txt \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/checksums.txt"
# Must print: opentendril-linux-amd64.tar.gz: OK
# Stop here if it does not.
grep "${ARCHIVE}$" checksums.txt | sha256sum -c
tar -xzf "$ARCHIVE" tendril
systemctl stop tendril
install -o tendril -g tendril -m 750 tendril /home/tendril/.local/bin/tendril
cd /
rm -rf "$WORKDIR"
```

If Stage 8 installed `tendril-mcp`, replace it from a separately verified
archive on the Pollinator-hosting account. Do not copy the full `tendril`
executable onto that account's PATH.

```bash
# as the ordinary (Pollinator-hosting) account — same RELEASE as above
RELEASE=v0.3.0
ARCHIVE=opentendril-linux-amd64.tar.gz
WORKDIR=$(mktemp -d)
cd "$WORKDIR"
curl -fsSL -o "$ARCHIVE" \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/${ARCHIVE}"
curl -fsSL -o checksums.txt \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/checksums.txt"
# Must print: opentendril-linux-amd64.tar.gz: OK
# Stop here if it does not.
grep "${ARCHIVE}$" checksums.txt | sha256sum -c
tar -xzf "$ARCHIVE" tendril-mcp
install -m 755 tendril-mcp "$HOME/.local/bin/tendril-mcp"
cd /
rm -rf "$WORKDIR"
```

Then restart the Stem and run the existing checks:

```bash
# [root]
systemctl start tendril
curl -s localhost:8080/health
sudo -u tendril -i tendril hardiness
```

No installer daemon, package manager, or background updater is part of this
path.

---

## Variations

Each entry changes only the steps named. Choose an axis value once and keep it
consistent — these are global choices, not per-step ones.

### Compile from source

*Advanced / developer. Changes Stage 3 and, if used, Stage 8. Invariants
unaffected.*

The normal path uses a verified OpenTendril release. Compiling from source is an
advanced/developer variation. It is not required to install or run the Stem.

This variation depends on:

- Git
- GNU Make
- Go 1.25.0, matching the `go` directive in the repository's `go.mod`. Install
  Go from the [upstream Go installation instructions](https://go.dev/doc/install).

It also requires an OpenTendril source checkout. The Stem's account needs no
compiler and no source tree.

```bash
# as your own (build) account
git clone https://github.com/opentendril/opentendril.git
cd opentendril
make stem                      # builds cmd/stem/tendril with the project's flags
```

```bash
# [root] hand it over. 0750 and not 0755: no account other than the Stem should
#        run this binary, so no account other than the Stem is given the ability.
install -d -o tendril -g tendril -m 750 /home/tendril/.local/bin
install -o tendril -g tendril -m 750 cmd/stem/tendril /home/tendril/.local/bin/tendril
rm cmd/stem/tendril
```

Do not copy this binary onto a Pollinator-hosting account's PATH, and do not run
`make install` for that account. That target is the single-user full-binary
install. For a governed Pollinator on this variation, install only
`tendril-mcp` from the same checkout:

```bash
# as the ordinary (Pollinator-hosting) account, from the source checkout
make install-mcp-client
```

### The Stem builds its own binary

*Changes Stage 3. Invariants unaffected.*

Give the `tendril` account the source-build dependencies above and its own
source clone — kept separate from any managed checkout, which is reset on every
run. It then builds and installs into its own `~/.local/bin` with no
cross-account handoff. The cost is a compiler and a build path inside the
account you are hardening.

### A system binary location

*Changes Stage 3. Satisfies P5 identically.*

Install to `/usr/local/bin/tendril` owned by `root`, mode `0755`, and update
`ExecStart=` in Stage 7. Both accounts then share one binary, which means both
run the same version — convenient if you want that, and a coupling if you do not.
Watch `$PATH` order: a stale copy in `~/.local/bin` shadows `/usr/local/bin`.

### A user service, or no service at all

*Changes Stage 7. Invariants unaffected.*

`systemctl --user` under the `tendril` account works identically given
`loginctl enable-linger`. Running `tendril serve` in the foreground from
`/home/tendril` is also fine — the control plane is the working directory either
way.

### A service state directory instead of a home

*Changes Stages 1, 3, 4 and 7. Invariants unaffected.*

Set the account's home to `/var/lib/tendril` if you prefer the Filesystem
Hierarchy Standard placement for service state. Managed checkouts and Pollinator
workspaces follow the home, so they move with it, and `WorkingDirectory=` must
match. Rootless containers still need a real user session.

### Firecracker instead of rootless containers

*Changes Stage 2. Satisfies P2.*

The Firecracker provider needs only `/dev/kvm`, whose group is not
root-equivalent, and bounds disk throughput by construction — which rootless
Docker does not. Configure the provider instead of installing a rootless daemon.

### A single principal (the Stem runs as you)

*Removes Stages 1, 2 and 9; changes 3 through 7.*

Install into a repository checkout and run `tendril serve` there. Setup takes
minutes and everything works, including `tendril mcp` over stdio. That
in-process MCP path is the single-user client. A governed installation uses
`tendril-mcp` instead and does not put the full binary on the Pollinator
account's PATH.

**This fails P1, P2 and P3.** The Stem's credentials, its grants and its issued
credential store all sit in a directory your own account owns. A Pollinator
running as you can read the git credential and use it directly — without asking
the Stem and without appearing in the audit lane — and can rewrite the grants
meant to constrain it. Grants and audit still record intent and catch accidents.
They do not constrain a caller that chooses otherwise.

That is a reasonable trade when you are the only caller. It stops being one when
you are not. `tendril hardiness` will report it accurately, and that report is
correct rather than a failure.

If you run this way inside a checkout, confirm the control-plane files are
ignored by git before committing anything:

```bash
git check-ignore -v .tendril/api-key .tendril/pollinators.json .tendril/grants.yaml
```

---

## Moving from a single principal to a separate one

Install fresh, then decommission — do not move the old installation.

The configuration has to change anyway (`checkout: mode: path` stops working,
workspaces relocate), and moving a private key does not un-expose it: `mv`
changes who owns a file and says nothing about who has already copied it.

> [!CAUTION]
> **Do not delete a repository's `.tendril/` directory.** In a development
> checkout it holds tracked content — genome, genotypes, sequences — beside
> ignored runtime state. Removing it destroys part of the working tree. The
> user-level `~/.tendril` is the one that goes.

**1. Stop and remove any old service.** An earlier `install.sh` registers
`opentendril.service` running as *your* user; left enabled it competes for port
8080 and can silently start an ungoverned Stem:

```bash
# [root]
systemctl disable --now opentendril
rm /etc/systemd/system/opentendril.service
systemctl daemon-reload
```

**2. Revoke credentials**, so one dies even if a copy survives:

```bash
# from the directory the old Stem actually ran in
tendril pollinator list
tendril pollinator revoke --pollen <each listed pollen>
```

**3. Remove the user-level control plane**, where the private key lives:

```bash
ls -la ~/.tendril/                        # look before deleting
shred -u ~/.tendril/*.pem
rm -rf ~/.tendril
```

**4. Remove old binaries**, so nothing on `$PATH` starts an ungoverned Stem:

```bash
which -a tendril
```

**5. Clear runtime state from the development checkout** — ignored files only,
leaving tracked files intact:

```bash
git status --short --ignored .tendril     # confirm what is ignored
rm -f .tendril/api-key .tendril/pollinators.json .tendril/grants.yaml
git status --short                        # must show no deleted tracked files
```

**6. Rotate the credential.** Regenerate the private key at GitHub and install
the new one per Stage 5.

**Check:** `cd ~ && tendril hardiness` reports no readable credential files.

---

## Day-to-day

**You made a change.** Push it; the Stem picks it up on its next run.

**The Stem made a change.** Review it from your clone:

```bash
git fetch origin
git diff main...origin/<branch>
```

**Inspect what the Stem did** — read-only, as the Stem:

```bash
sudo -u tendril -i
cd ~/.tendril/substrates/myrepo && git log --oneline -5 && git status
```

**After anything that touches the installation**, run `tendril hardiness` as
yourself. It is the fastest way to notice credential material that has drifted
back into your home directory.

---

## What this does not do

* **It does not stop the Botanist.** Whoever administers the machine can become
  the Stem's user. The boundary is against the accounts that host Pollinators.
* **It does not replace network perimeter controls when you opt into exposure.**
  The Representational State Transfer surface binds **loopback by default**
  (`TERROIR_HOST` unset → `127.0.0.1`). An optional Unix-domain socket
  (`TENDRIL_LOCAL_SOCKET`) is local transport for the same authenticated mux;
  it is not a trust boundary and does not change that default. Setting
  `TERROIR_HOST=0.0.0.0` (or another non-loopback address) makes the daemon
  reachable off-host; at that point durable Pollinator credentials are refused
  on data routes and callers must present short-lived access tokens, but you
  should still put a network-facing Ramet behind something that terminates TLS
  and restricts who can reach the mint and data ports. Per-Pollinator roots
  already make revocation per-caller rather than a shared-secret rotation.

---

## Related documents

* [GUIDE-GIT-CONNECTION.md](./GUIDE-GIT-CONNECTION.md) — connection postures,
  signing, and what each grant operation-class permits.
* [GUIDE-HOST-TENDRIL.md](./GUIDE-HOST-TENDRIL.md) — running a Tendril directly on the Stem
  host to reach locally authenticated command-line tools.
* [GUIDE-POLLINATOR-INTEGRATION.md](./GUIDE-POLLINATOR-INTEGRATION.md) — client
  configuration per Pollinator.
