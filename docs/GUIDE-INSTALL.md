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
| Binary provenance | **compiled from source** / precompiled release / package manager | — |
| Build principal | **a separate build account** / the Stem's own account | — |
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

Two paths resolve against **home** rather than the working directory. Because
this guide sets both to `/home/tendril`, they coincide:

| Path | Holds |
|---|---|
| `/home/tendril/.tendril/substrates/<name>` | the Stem's own clone of each Substrate |
| `/home/tendril/.tendril/workspaces/` | isolated per-Pollen working trees |

---

## The two-working-copy model

A separate principal cannot read your home directory, so it cannot work in your
clone. It gets its own, and **the remote is the only thing the two share**.

```
   your account                              the Stem (tendril)
   ~/…/opentendril                           ~/.tendril/substrates/opentendril
   you edit here                             the Stem clones and edits here
             │                                          │
             │  push / fetch                            │  branch + pull request
             └──────────────►  remote  ◄────────────────┘
                        source of truth
```

* **The Stem makes a change** → it commits in its own clone and opens a pull
  request. You fetch to review.
* **You make a change** → you push. The Stem picks it up on its next run: a
  managed checkout is fetched and hard-reset to the target branch before every
  run.

> [!WARNING]
> **Never hand-edit the Stem's clone.** It is Tendril-owned; the next run resets
> it and discards uncommitted changes without asking. Your clone is the one you
> edit.

The boundary and the workflow are the same mechanism here — nothing needs to be
shared between the accounts, because everything already travels through the
remote.

---

# The procedure

Commands marked **[root]** need `sudo`; the rest run as the named user. **Do the
stages in order** — container access comes first, because a user in a
root-equivalent group makes every later stage cosmetic.

Prerequisites: Go 1.24+ (on the build account only), Docker, Git, and an LLM —
local [Ollama](https://ollama.ai) by default, or a cloud provider key.

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

```bash
# [root] prerequisites
apt-get update && apt-get install -y uidmap slirp4netns dbus-user-session
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
> (*all free*) — this guide instantiates *compiled from source on a separate
> build account, installed into the Stem's own home*.

What P5 requires is only that no Pollinator-hosting account can write the binary
or any directory on the path used to reach it. A binary owned by `tendril` inside
a home that other accounts cannot traverse satisfies that; so does a root-owned
binary in a system location. Neither is more secure than the other, and the
choice is logistics — see Variations.

Build on an account that has the toolchain. The Stem's account needs no compiler
and no source tree:

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

Repeat this stage whenever you rebuild — it is how a change developed in your
clone reaches the running Stem.

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
tendril init
```

The wizard is Ollama-first: it detects a local instance and defaults to fully
local inference. Decline and it walks you through a cloud provider and its key.

**Check:** `grep DEFAULT_LLM_PROVIDER /home/tendril/.env` prints your choice.

---

## Stage 5 — Install the credential and configure the connection

> **Serves P3.** The Substrate's `checkout` mode is *constrained by P1* —
> `mode: path` cannot work once the Stem cannot read your home.

Generate a **fresh** private key for your GitHub App. Never carry forward one
that has lived in an account hosting Pollinators: changing a file's owner does
not change who has already copied it.

```bash
# [root] install the downloaded key as the Stem's, then destroy your copy
install -o tendril -g tendril -m 600 ~/Downloads/<app>.private-key.pem \
  /home/tendril/.tendril/app.pem
shred -u ~/Downloads/<app>.private-key.pem
```

**Check:** `sudo -u tendril test -r /home/tendril/.tendril/app.pem && echo ok`
prints `ok`, while `cat /home/tendril/.tendril/app.pem` as your own account is
denied.

Then write the connection and the grant together:

```bash
# as tendril, in /home/tendril
tendril git setup \
  --substrate myrepo \
  --repo myorg/myrepo \
  --posture app \
  --app-id <id> \
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
    operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr]
    substrates: [myrepo]
    # egress: [github.com]              # optional: hosts the Stem may fetch for this pollen
    # expires: 2027-01-01               # optional: RFC 3339 timestamp or YYYY-MM-DD
    # confirmAbove: { impact: high }    # optional: escalate high-impact operations to a human
```

**Check:** `tendril git setup --substrate myrepo --repo myorg/myrepo --verify`
reports the connection ready.

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
so a leaked store is not a leaked credential. It begins `tendril_`, which makes it
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
To expose the REST surface off-host, set `TERROIR_HOST=0.0.0.0` (or a specific
interface) in the unit's environment — and once off-host, Pollinator data routes
require short-lived access tokens (see Stage 6).

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

> [!CAUTION]
> **Do not use `tendril mcp` here.** That subcommand starts an **in-process Stem
> as whoever runs it**, reading `./.tendril` from that caller's working
> directory. It does not connect to the Stem running as `tendril`. Pointing a
> Pollinator at it puts a second, ungoverned Stem on your own account.

A credential-bearing Pollinator reaches the Stem over the Representational State
Transfer surface. It is admitted only on routes that consult the delegation
authorizer per invocation, with the Substrate in hand; every other route —
including the Model Context Protocol endpoint at `POST /v1` — refuses it by
default rather than running the request as ordinary traffic:

```console
$ curl -X POST localhost:8080/v1 -H "Authorization: Bearer tendril_…" …
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

```bash
curl -X POST http://localhost:8080/v1/git/status \
  -H "Authorization: Bearer tendril_…" \
  -H "Content-Type: application/json" \
  -d '{"substrate":"myrepo"}'
```

**Check:** call a route your grant does not cover and confirm the refusal names
the reason:

```console
$ curl -X POST localhost:8080/v1/git/prune -H "Authorization: Bearer tendril_…" …
HTTP/1.1 403 Forbidden
delegation denied: no active grant covers Pollen "claude",
operation-class "git.prune", substrate "myrepo"
```

The Pollen in that message was derived from the credential, not claimed by the
caller. That is the boundary working.

> [!NOTE]
> **A Model Context Protocol client has no per-Pollinator path in this
> configuration.** The stdio transport runs the Stem as the caller, and the
> hypertext endpoint refuses credential-bearing callers. Such a client can only
> reach this Stem using the Botanist's own bearer key, which is full access and
> defeats per-Pollinator identity. If your Pollinator speaks only that protocol,
> see the single-principal configuration under Variations and accept that
> delegation there is advisory.

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

```bash
# as your own account, from your own home — the caller's view
cd ~ && tendril hardiness
```

This run must report **no readable credential files**. The check opens each
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

## Variations

Each entry changes only the steps named. Choose an axis value once and keep it
consistent — these are global choices, not per-step ones.

### Precompiled release instead of a source build

*Changes Stage 3. Invariants unaffected.*

Download the release artifact rather than building it, verify its checksum, then
install it exactly as Stage 3 does. The build account and its toolchain become
unnecessary. Updating means fetching a new artifact rather than rebuilding.

### The Stem builds its own binary

*Changes Stage 3. Invariants unaffected.*

Give the `tendril` account a Go toolchain and its own source clone — kept
separate from any managed checkout, which is reset on every run. It then builds
and installs into its own `~/.local/bin` with no cross-account handoff. The cost
is a compiler and a build path inside the account you are hardening.

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
minutes and everything works, including `tendril mcp` over stdio — which is the
one thing the governed configuration cannot offer a Model Context Protocol
client.

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
  (`TERROIR_HOST` unset → `127.0.0.1`). Setting `TERROIR_HOST=0.0.0.0` (or another
  non-loopback address) makes the daemon reachable off-host; at that point
  durable Pollinator credentials are refused on data routes and callers must
  present short-lived access tokens, but you should still put a network-facing
  Ramet behind something that terminates TLS and restricts who can reach the
  mint and data ports. Per-Pollinator roots already make revocation per-caller
  rather than a shared-secret rotation.

---

## Related documents

* [GUIDE-GIT-CONNECTION.md](./GUIDE-GIT-CONNECTION.md) — connection postures,
  signing, and what each grant operation-class permits.
* [GUIDE-HOST-TENDRIL.md](./GUIDE-HOST-TENDRIL.md) — running a Tendril directly on the Stem
  host to reach locally authenticated command-line tools.
* [GUIDE-POLLINATOR-INTEGRATION.md](./GUIDE-POLLINATOR-INTEGRATION.md) — client
  configuration per Pollinator.
