# OpenTendril Quick Start — your first session

This covers what to do **once OpenTendril is installed**: confirming the Stem is
live, obtaining a credential, and making your first governed call.

> [!IMPORTANT]
> **Installation is not here.** If OpenTendril is not installed yet, start with
> **[docs/GUIDE-INSTALL-QUICK.md](GUIDE-INSTALL-QUICK.md)** — the short public
> entrypoint for local/evaluation and governed installation.
> **[docs/GUIDE-INSTALL.md](GUIDE-INSTALL.md)** is the deeper hardened/manual
> reference: the properties that decide whether the delegation boundary is
> enforced by the operating system or merely recorded, and the configurations
> that satisfy them.

---

## Before you start

Both installation shapes need these:

| Requirement | Check |
|---|---|
| Docker | `docker --version` |
| An LLM | Local [Ollama](https://ollama.ai) (default) — or a cloud provider key |

A governed installation runs Docker rootless, under the Stem's own account, so
check it as the Stem: `sudo -u tendril -i docker --version`. The Stem's health
report names both, so step 1 confirms them either way.

**Which runtime actually isolates your code.** Docker is the requirement, but it
is not necessarily what runs a Terrarium. Given no explicit choice, the conductor
prefers **gVisor** when the host supports it, and falls back to Docker's default
runtime otherwise. gVisor is not an alternative to Docker — it is a runtime
*within* it, selected as `--runtime=runsc`, which is why the daemon is required
either way. Ask the daemon what it has:

```bash
sudo -u tendril -i docker info -f '{{.Runtimes.runsc}}'
```

An empty result means gVisor is unavailable and Terraria run under Docker's
default runtime. `TENDRIL_TERRARIUM_PROVIDER` overrides the choice, and an
explicit selection is always honoured rather than second-guessed.

---

## Which installation do you have?

This matters more than anything else on this page, and the two shapes need
different instructions.

```bash
command -v tendril
```

- **Nothing on your path** — you have a **governed** installation. The full
  Stem binary belongs to the Stem's own account, at mode 750, so that you
  cannot run or replace it. That is the design working. A governed
  installation may also have the restricted client on this account:

  ```bash
  command -v tendril-mcp
  ```

  Continue below.
- **A path is printed** — you have a **single-user** installation. Skip to
  [Single-user installations](#single-user-installations).

If you are unsure which you have,
[GUIDE-INSTALL-QUICK.md](GUIDE-INSTALL-QUICK.md) names the two postures;
[GUIDE-INSTALL.md](GUIDE-INSTALL.md) is the detailed reference.

---

# Governed installations

The Stem runs as its own operating-system principal. You talk to it over the
transport surface, holding a credential it issued you.

## 1. Confirm the Stem is running

```bash
systemctl status tendril
curl -s localhost:8080/health
```

A healthy Stem answers with a report naming each check:

```json
{"overall":true,"results":{"api-key":{"healthy":true,"message":"At least one LLM provider is available"},
 "docker-daemon":{"healthy":true},"workspace":{"healthy":true,"message":".tendril workspace is writable"}}}
```

The startup log states the bind and what it means for credentials:

```
Starting Go Stem API on 127.0.0.1:8080 (loopback: durable Pollinator credentials
still accepted on data routes)...
```

Loopback is the default. To reach the Stem from another host, set
`TERROIR_HOST=0.0.0.0`; data routes then refuse durable credentials and require a
short-lived access token, which is what step 3 mints anyway.

## 2. Read what the installation actually is

```bash
sudo -u tendril -i tendril hardiness
```

**This reports and never gates — its exit status is always zero.** The output is
the point, not the exit code:

```
✅  Running as the Stem (tendril), which owns ./.tendril
✅  3 credential file(s) readable — this is the Stem's own material
✅  The Stem's binary: Nothing on its resolution chain is writable by others
✅  The control plane is outside any repository
✅  1 active Pollinator credential(s) — those callers PROVE their Pollen
✅  1 grant(s) configured

HARDY — no weak conditions and nothing unestablished.
```

Hardiness is a control-plane operation. It is not a Pollinator-facing command.
A correctly governed Pollinator-hosting account has no full `tendril` binary, so
it cannot run `tendril hardiness`. If the Stem-side report tells you to run it
again from an account that hosts Pollinators, that sentence does not apply to
this posture.

From the ordinary account, confirm the other side of the boundary with tools
that account already has. Do not copy or chmod the Stem binary.

```bash
command -v tendril
# no result

test -r /home/tendril/.tendril/api-key; echo $?
# non-zero — this account cannot read the Botanist key the Stem holds

test -r /home/tendril/.tendril/pollinators.json; echo $?
# non-zero — this account cannot read the Pollinator credential store
```

Those `test -r` checks do not print file contents. A zero from either means this
account can read Stem control-plane material; that is a weak installation, not
a prompt to install `tendril` here.

> [!NOTE]
> `hardiness` measures the binary **on disk**. If you have installed a new binary
> and not yet restarted the service, the running Stem may be executing a
> different image. Restart before trusting the reading.

## 3. Get a credential

A Pollinator is a caller. Each one holds its own credential and is constrained by
its own grant. See what already exists:

```bash
sudo -u tendril -i tendril pollinator list
```

```
POLLEN  STATUS  ISSUED      DIGEST         NOTE
claude  active  2026-07-22  38c3089267f7…  laptop
```

If yours is not listed, issue one — as the Stem, in its own home:

```bash
sudo -u tendril -i tendril pollinator issue --pollen claude --note "laptop"
```

The secret prints **once** and is never stored; only its digest is kept. It begins
`tendril_refresh_` and is the **durable refresh root** for that Pollinator. Give
it to that Pollinator; do not give the Pollinator the Botanist key.

> [!IMPORTANT]
> **Credentials and grants are read at startup.** One issued while the Stem is
> running is refused with `401` until it restarts. Issue everything first, then
> restart once.

Then mint a short-lived access token to actually use:

```bash
sudo -u tendril -i tendril pollinator token --pollen claude > ~/.tendril-token
chmod 600 ~/.tendril-token
```

Minting is the right habit for two reasons: it works on both loopback and
off-host binds, and it does not require still holding a root that printed once.
Tokens last at most 15 minutes; mint another when one expires. Revoking the root
stops further minting, and outstanding tokens age out.

The redirect keeps the secret out of your terminal history and off your screen.
Use it without printing it:

```bash
-H "Authorization: Bearer $(cat ~/.tendril-token)"
```

## 4. Understand your grant

A credential proves *who* you are. A grant decides *what* you may do. No grant
means every delegated invocation is denied — the secure default.

Inspect the Stem control-plane grant. Do not edit `.tendril/grants.yaml` by hand
for ordinary first use:

```bash
sudo -u tendril -i tendril delegation grants --pollen claude --substrate myrepo
```

After Git setup the grant is Git-only. The Botanist then grants `seed.grow` and
`sprout.watch` explicitly, using the same Pollen and Substrate names:

```bash
sudo -u tendril -i tendril delegation grant \
  --pollen claude \
  --substrate myrepo \
  --operation seed.grow \
  --operation sprout.watch
```

If the Stem is already running, restart it so the new grant is read. Then
inspect again:

```text
pollen: claude
  substrates: [myrepo]
  operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr, seed.grow, sprout.watch]
```

Read it as a sentence: *the Pollen `claude` may run these operation classes, on
this Substrate, and nothing else.* Note `git.prune` and `sprout.grow` are
absent — deletion and raw Sprout dispatch are not part of the first-use grant.

Grants and Core identity stay dotted. The MCP tool name is the lower-camelCase
projection of that identity:

```text
Core / grant:  git.status
MCP tool:      gitStatus
```

## 5. Make your first governed call

```bash
TOKEN=$(cat ~/.tendril-token)
curl -s -X POST localhost:8080/v1/git/status \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"substrate":"myrepo"}'
```

```json
{"branch":"tendril/claude/work","head":"bb63c9f…","defaultBranch":"main",
 "clean":true,"onDefaultBranch":false,"commitAllowed":true,
 "workspace":"/home/tendril/.tendril/workspaces/myrepo/claude",
 "isolated":true,"pollen":"claude"}
```

Three things in that response are worth reading closely:

- **`"pollen":"claude"`** — the Stem *derived* your Pollen from the credential you
  presented. A Pollen claimed in a header is ignored for credential-bearing
  callers, so a caller cannot assert someone else's identity.
- **`"isolated":true`** and the `workspace` path — you get your own worktree,
  under your own Pollen. Two Pollinators never share a tree, so they cannot stage
  each other's files.
- **`"branch":"tendril/claude/work"`** — work you do here happens on a branch the
  Stem owns and can later reclaim. Branches made by hand in a shell are invisible
  to it.

## 6. Hand off a bounded Seed

Use the same Pollinator credential and the same Substrate name. This is the
delegated first task — not the Botanist/operator `tendril seed grow` command,
and not the Botanist key.

```bash
TOKEN=$(cat ~/.tendril-token)
curl -s -X POST localhost:8080/v1/seeds/grow/async \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"substrate":"myrepo","goal":"make the failing tests pass","verify":["go","test","./..."]}'
```

The Stem accepts the dispatch with HTTP 202 and returns three fields:

```json
{"handle":"seed-…","phytomerId":"tendril-…","status":"running"}
```

`handle` is the Fruit-collection identity. `phytomerId` is the
lifecycle/observation identity. `status` starts as `running`. Copy `phytomerId`
from that response. Do not invent an ID, inspect a database, or correlate
identities by hand.

Immediately observe that exact Phytomer. `sprout.watch` authorises this stream;
it does not collect Fruit and does not grant `seed.grow`.

```bash
curl -N \
  localhost:8080/v1/phytomers/<phytomerId>/watch \
  -H "Authorization: Bearer $TOKEN"
```

The stream is Server-Sent Events. After authenticating it emits the current safe
observation immediately, then follows durable state until the Seed is
`satisfied`, `exhausted`, or `withered`, then closes. Connecting after a
terminal Seed returns that terminal current state and closes.

The observation names Pollen, Substrate, handle, `phytomerId`, and Seed status.
When the Stem actually produced Fruit, the same stream includes `branch` and
`commit`. Those fields stay absent until those facts exist. `main` is not
modified.

When the documented workflow needs the collection view, use the Seed handle
from the dispatch response. Collection is `seed.grow`, scoped to the Pollen
that dispatched it:

```bash
curl -s localhost:8080/v1/seeds/runs/<handle> \
  -H "Authorization: Bearer $TOKEN"
```

Review the resulting Git Fruit on the reported branch. `main` remains unchanged
until a human merges.

An MCP-speaking Pollinator uses the same authority through `tendril-mcp`. The
MCP tool name is `seedGrow`; the grant stays `seed.grow`.

## 7. Learn what a refusal looks like

A refusal is not a fault. Knowing the difference between these three saves an
afternoon:

| What you did | Response | Meaning |
|---|---|---|
| Sent no credential | `401` | The route is authenticated. |
| Sent an unrecognised bearer | `401 Unauthorized` | Unknown, revoked, expired or forged — all refused the same way. |
| Asked for something outside your grant | `403` | Authenticated fine; not permitted. |

The `403` names all three things it checked, so you know which to change:

```
delegation denied: no active grant covers Pollen "claude",
operation-class "git.prune", substrate "myrepo"
```

If a Substrate is configured but its managed checkout has not been materialized,
an in-grant call returns `409` — a configuration state you fix on the host, not a
server fault.

## 8. Two credential systems, not one

A frequent confusion, worth stating plainly:

| | Held by | Used for |
|---|---|---|
| **`BOTANIST_KEY`** | the operator | the gate, and management routes such as delegation approvals |
| **Pollinator credential** (`tendril_refresh_…`) | each caller | delegated data routes, constrained by that caller's grant |

They are separate on purpose. It is why a Pollinator cannot approve its own
pending confirmation.

## 9. Connect over MCP

The supported MCP client on this installation is `tendril-mcp`, not
`tendril mcp`. See [Model Context Protocol over stdio](#model-context-protocol-over-stdio).

---

# Single-user installations

The binary is on your own path and the Stem runs as you. There is no boundary to
cross, so the credential steps above do not apply.

```bash
tendril --help          # confirm the install
tendril serve           # start the Stem
curl -s localhost:8080/health
tendril chat            # interactive session
```

`tendril chat` resolves its key from `BOTANIST_KEY`, then from
`./.tendril/api-key` in the working directory.

---

# Model Context Protocol over stdio

## Governed installations

The supported MCP client is `tendril-mcp`. Do not use `tendril mcp` here.

```text
MCP-speaking Pollinator
    -> tendril-mcp stdio client
    -> durable Pollinator root
    -> automatic short-lived access token
    -> separately owned governed Stem
    -> governed capabilities
```

`tendril-mcp` holds that Pollinator's durable root and mints short-lived access
tokens automatically. Authorization and Pollen derivation stay at the governed
Stem. The client cannot construct a Stem and has no in-process mode.

Credential lookup, first match wins:

1. `TENDRIL_POLLINATOR_CREDENTIAL`
2. `TENDRIL_MCP_CREDENTIAL`
3. `~/.config/tendril/pollinators/<TENDRIL_POLLEN>`

Startup fails closed when:

- no credential is configured;
- the credential file is unsafe;
- the Stem is unavailable;
- ownership is not established;
- the answering Stem has the caller's UID;
- the Stem refuses the root.

Only after all of those checks pass does MCP forwarding begin.

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

A granted `git.status` call uses the primary MCP identifier:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gitStatus","arguments":{"substrate":"myrepo"}}}
```

The Stem authorizes and invokes canonical `git.status`. Do not rewrite the
grant to `gitStatus`.

## Single-user installations

`tendril mcp` acts as a governed command surface and stdio bridge. For a
single-user (in-process) installation, it is the natural editor integration.
It determines its mode based on the environment and credentials:

- If a governed other-user Stem is reachable and a durable Pollinator credential is provided, it **forwards** MCP frames to that Stem. Authorization and Pollen derivation happen at the governed Stem; local grants and `TENDRIL_POLLEN` do not govern the forwarded connection.
- If such a Stem is owned by another principal and no credential is available, it **refuses** to start rather than creating a competing local Stem.
- If no governed other-user Stem is reachable, or if the reachable Stem belongs to the caller, or if explicitly forced via `TENDRIL_MCP_IN_PROCESS=1`, it starts an **in-process Stem** reading its control plane from the caller's working directory.

That in-process fallback belongs to `tendril mcp` only. It is not a property of
`tendril-mcp`.

Credential lookup for forwarding mode checks:

1. `TENDRIL_POLLINATOR_CREDENTIAL`
2. `TENDRIL_MCP_CREDENTIAL`
3. `~/.config/tendril/pollinators/<TENDRIL_POLLEN>`

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril",
      "args": ["mcp"]
    }
  }
}
```

For the **in-process MCP path**, bind one Pollen with `TENDRIL_POLLEN`. Unset, every delegated capability is denied.

---

## Where to go next

Every governed command in `core.CapabilityNames()` is projected across the command line, the transport surface (REST), and the Model Context Protocol (MCP) surface alike — parity is mechanically checked. REST and CLI use canonical identity; MCP publishes a lower-camelCase primary identifier that maps one-to-one back to that identity. Views and control-plane operations are distinct and are not part of governed command parity. So `tendril phytomer create|list|get|history` manages sessions from a terminal exactly as the transport routes do.

- **[docs/GUIDE-INSTALL-QUICK.md](GUIDE-INSTALL-QUICK.md)** — short install entrypoint
- **[docs/GUIDE-INSTALL.md](GUIDE-INSTALL.md)** — the five invariants, and which configurations satisfy them
- **[docs/GUIDE-GIT-CONNECTION.md](GUIDE-GIT-CONNECTION.md)** — connecting a Substrate to its forge
- `tendril --help` (or `sudo -u tendril -i tendril --help`) — every command
