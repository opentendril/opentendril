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
curl -s 127.0.0.1:8080/health
```

A healthy Stem answers with a report naming each check:

```json
{"overall":true,"results":{"api-key":{"healthy":true,"message":"At least one LLM provider is available"},
 "docker-daemon":{"healthy":true},"workspace":{"healthy":true,"message":".tendril workspace is writable"}}}
```

The startup log states the primary HTTP bind posture:

```
Starting Go Stem API on 127.0.0.1:8080 (loopback: durable Pollinator credentials
still accepted on data routes)...
```

Loopback is the default. For a Pollinator on another machine, configure the
Stem's separate HTTPS listener with `TENDRIL_REMOTE_LISTEN_ADDR`,
`TENDRIL_REMOTE_TLS_CERT`, and `TENDRIL_REMOTE_TLS_KEY`. Do not use a
non-loopback plaintext HTTP endpoint for Pollinator traffic. See
[GUIDE-INSTALL.md](./GUIDE-INSTALL.md#remote-pollinator-https-listener) and
[GUIDE-POLLINATOR-INTEGRATION.md](./GUIDE-POLLINATOR-INTEGRATION.md) for the
Stem and Pollinator setup.

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

Before creating the first grant, use the ordinary Substrate lifecycle. These
commands read and write the canonical account-global registry and do not create
delegation authority:

```bash
sudo -u tendril -i tendril substrate add myrepo --repo owner/repo --posture app --app-id 123456 --key /home/tendril/.tendril/app.pem
sudo -u tendril -i tendril substrate list
sudo -u tendril -i tendril substrate get myrepo
sudo -u tendril -i tendril substrate verify myrepo
```

The supported first-use postures are GitHub App and fine-grained PAT plus GPG;
managed checkout remains the governed default. If an empty managed App/API
repository needs a Git base, the Botanist can run
`sudo -u tendril -i tendril git bootstrap --substrate myrepo`, then repeat
`sudo -u tendril -i tendril substrate verify myrepo`.

If yours is not listed, issue one — as the Stem, in its own home:

```bash
sudo -u tendril -i tendril pollinator issue --pollen claude --note "laptop"
```

The secret prints **once** and is never stored; only its digest is kept. It begins
`tendril_refresh_` and is the **durable refresh root** for that Pollinator. Give
it to that Pollinator; do not give the Pollinator the Botanist key.

Then mint a short-lived access token to actually use:

```bash
sudo -u tendril -i tendril pollinator token --pollen claude > ~/.tendril-token
chmod 600 ~/.tendril-token
```

Minting gives the Pollinator a short-lived bearer to use on governed requests.
Tokens last at most 15 minutes; mint another when one expires. Root revocation
blocks the next mint without restarting the Stem, and outstanding tokens remain
bounded by their existing expiry. The Stem reads current grants on each
governed admission.

The redirect keeps the secret out of your terminal history and off your screen.
Use it without printing it:

```bash
-H "Authorization: Bearer $(cat ~/.tendril-token)"
```

## 4. Understand your grant

A credential proves *who* you are. A grant decides *what* you may do. No grant
means every delegated invocation is denied — the secure default.

Create the first grant explicitly through the Stem control plane. Do not edit
`.tendril/grants.yaml` by hand for ordinary first use. Name every operation
class explicitly; there is no hidden or wildcard authority:

```bash
sudo -u tendril -i tendril delegation create \
  --pollen claude \
  --substrate myrepo \
  --operation git.status \
  --operation git.branch.list \
  --operation git.branch \
  --operation git.commit \
  --operation git.push \
  --operation git.pr \
  --operation seed.grow \
  --operation phytomer.continue \
  --operation sprout.watch
```

Grant changes take effect on the next governed admission without restarting the
Stem. Inspect the complete active grant without narrowing the projection to a
single Substrate:

```bash
sudo -u tendril -i tendril delegation grants --pollen claude
```

```text
pollen: claude
  substrates: [myrepo]
  operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr, seed.grow, phytomer.continue, sprout.watch]
```

Read it as a sentence: *the Pollen `claude` may run these operation classes, on
this Substrate, and nothing else.* Note `git.prune` and `sprout.grow` are
absent — deletion and raw Sprout dispatch are not part of the first-use grant.

Removal is separate and dependency-safe. First narrow or revoke every live
grant that names the Substrate, then run `sudo -u tendril -i tendril substrate remove myrepo` as
the Botanist; the command does not rewrite grants or remove secrets, workspaces,
Fruit, or Git state.

Grants and Core identity stay dotted. The MCP tool name is the lower-camelCase
projection of that identity:

```text
Core / grant:  git.status
MCP tool:      gitStatus
```

## 5. Make your first governed call

The Substrate must already have a Git base — at least one commit on the
required branch. `tendril substrate verify myrepo` confirms
authentication and that Git base without mutating the repository.

```bash
TOKEN=$(cat ~/.tendril-token)
curl -s -X POST 127.0.0.1:8080/v1/git/status \
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
curl -s -X POST 127.0.0.1:8080/v1/seeds/grow \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"substrate":"myrepo","goal":"make the failing tests pass","verify":["go","test","./..."],"detached":true,"idempotencyKey":"seed-open-1"}'
```

Detached opens require a non-empty, Pollen-scoped `idempotencyKey`. Reusing the
same key for the same semantic request returns the existing Seed handle and
Phytomer without replacement work. Reusing it for a different semantic request
is refused; use a new key for a distinct detached Seed.

Canonical `seed.grow` owns detached lifecycle. The Stem returns active identity
immediately:

```json
{"handle":"seed-…","phytomerId":"tendril-…","status":"running"}
```

`POST /v1/seeds/grow/async` remains a compatibility presentation of the same
Core lifecycle.

`handle` is the Fruit-collection identity. `phytomerId` is the
lifecycle/observation identity. `status` starts as `running`. Copy `phytomerId`
from that response. Do not invent an ID, inspect a database, or correlate
identities by hand.

Immediately observe that exact Phytomer. `sprout.watch` authorises this stream;
it does not collect Fruit and does not grant `seed.grow`.

```bash
curl -N \
  127.0.0.1:8080/v1/phytomers/<phytomerId>/watch \
  -H "Authorization: Bearer $TOKEN"
```

The stream is Server-Sent Events. After authenticating it emits the current safe
observation immediately, then follows durable state until the Seed reaches a
terminal state (`satisfied`, `exhausted`, `withered`, or `fruit-publication-failed`),
then closes. Only `satisfied` is success; the other terminal states are
non-successful outcomes. Connecting after a terminal Seed returns that terminal
current state and closes.

The observation names Pollen, Substrate, handle, `phytomerId`, and Seed status.
When continuations exist, it also includes `continuationId`, `sequence`, and
`deliveryState`. When the Stem actually produced Fruit, the same stream includes
`branch` and `commit`. Those fields stay absent until those facts exist. Raw
continued intent is never in this view. `main` is not modified.

After the active `phytomerId` is returned, continue that owned Phytomer:

```bash
curl -s -X POST 127.0.0.1:8080/v1/phytomers/<phytomerId>/continue \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"intent":"keep going on the remaining tests","idempotencyKey":"continue-1"}'
```

Accepted intent is durable before acknowledgment. It applies only to the active
owned Phytomer, does not interrupt or inject into the currently running Sprout,
is delivered at the next permitted cognitive boundary, and does not widen Seed
bounds. The grant is `phytomer.continue` on the Phytomer's Stem-bound Substrate.

When the documented workflow needs the collection view, use the Seed handle
from the dispatch response. Collection is `seed.grow`, scoped to the Pollen
that dispatched it:

```bash
curl -s 127.0.0.1:8080/v1/seeds/runs/<handle> \
  -H "Authorization: Bearer $TOKEN"
```

Review the resulting Git Fruit on the reported branch. `main` remains unchanged
until a human merges.

An MCP-speaking Pollinator uses the same authority through `tendril-mcp`. The
tool sequence is:

```text
seedGrow         detached:true + idempotencyKey
sproutWatch      sessionId
phytomerContinue sessionId + intent + idempotencyKey
sproutWatch      sessionId
```

Grant names remain dotted (`seed.grow`, `sprout.watch`, `phytomer.continue`).
Tool names are lower-camel MCP presentation. Each grant is checked
independently. `sproutWatch` is a view, not a governed command.

## Botanist Fruit review inventory

The Botanist can inspect all durable Fruit claims from the Stem account without
enumerating Git branches:

```bash
sudo -u tendril -i tendril fruit list
sudo -u tendril -i tendril fruit list --json
```

The text view shows review pressure and each Fruit's producer, Phytomer when
present, Substrate, repository, branch, commit, publication state, review
state, safe unknown reason, and pull-request number when established. The JSON
view is the deterministic Core `FruitInventory` contract. The same contract is
available to a Botanist through private `GET /v1/fruit`; it is not a public
Pollinator route or an MCP tool/view. Review pressure is observational only:
inventory does not accept, merge, delete, block, or mutate Fruit. If persisted
inventory evidence is unavailable, the commands report that condition instead
of presenting an empty inventory.

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
| **Pollinator credential** (`tendril_refresh_…`) | each caller | token minting; the remote root is not used on governed data routes |

They are separate on purpose. It is why a Pollinator cannot approve its own
pending confirmation.

## 9. Connect over MCP

The supported MCP client on this installation is `tendril-mcp`, not
`tendril mcp`. See [Model Context Protocol over stdio](#model-context-protocol-over-stdio).

---

# Single-user installations

The binary is on your own path and the Stem runs as you. There is no boundary to
cross, so the credential steps above do not apply.

## 1. Confirm the install

```bash
tendril --version
tendril hardiness   # reports; does not gate
```

## 2. Start the Stem

```bash
tendril serve
```

`PORT` controls the bind endpoint and defaults to `8080`. Both the Stem and the
local client read the same environment variable, so they agree without
configuration:

```bash
PORT=18080 tendril serve
```

```bash
PORT=18080 tendril chat -- go test ./...
```

## 3. Configure a Substrate

For ordinary Substrate configuration, prefer the canonical lifecycle:

```bash
tendril substrate add default-workspace --repo owner/repo --posture app --app-id <app-id> --key ~/.tendril/app.pem
tendril substrate list
tendril substrate get default-workspace
tendril substrate verify default-workspace
```

`tendril setup substrate` remains a compatibility bootstrap for installations
that still use the wizard; it is not the preferred lifecycle. Delegation is a
separate explicit `tendril delegation create` step.

## 4. Start direct coding — `tendril chat`

`tendril chat` is the direct local coding path. It is a presentation adapter
over the same Stem-owned Seed lifecycle that governs all coding work.

```
tendril chat [--substrate <name>] [--max-iterations N] [--timeout N] -- <verify argv...>
```

A bare `--` is required. Everything after it is the verification command that
bounds success. The verifier is not invented automatically — you must supply it
explicitly.

**With one configured Substrate**, `--substrate` may be omitted:

```bash
tendril chat -- go test ./...
```

**With multiple configured Substrates**, you must name the one to use:

```bash
tendril chat --substrate myrepo -- go test ./...
```

## 5. What happens when you enter a goal

Enter a non-empty line at the prompt. That line is the coding goal.

The Stem opens a detached Seed in the canonical lifecycle immediately. The terminal prints the Seed handle and Phytomer identity:

```text
Handle:   seed-abc123
Phytomer: tendril-xyz789
Status:   running
```

Safe progress (`PhytomerObservation`) streams while the Seed is active.
The default branch is not modified.

## 6. Continued intent

While the Seed is active, further non-empty input lines continue the same
Phytomer. They do not start a new Seed. Intent is accepted durably before
acknowledgment and delivered at the next permitted cognitive boundary.

## 7. Terminal settlement

When the Seed reaches a terminal state (`satisfied`, `exhausted`, `withered`,
or `fruit-publication-failed`) the terminal reports it. Only `satisfied` is
success; the other terminal states are non-successful outcomes. When Fruit exists:

```
Branch: staging/ai-... Commit: <sha>
```

Review that Fruit on the reported branch. The default branch remains unchanged
until a human merges.

After settlement the prompt is ready for the next goal, which starts a fresh
Seed in the same session.

## 8. Local controls

`exit` or `/exit` closes the terminal session. They are local controls; they do
not affect a Seed that is still active on the Stem.

> [!NOTE]
> `--ws` is not the direct coding path. It is rejected by `tendril chat` at
> parse time. The direct path routes through the Stem-owned Seed/Phytomer
> lifecycle, not a WebSocket channel.

---

## How the direct path relates to the governed lifecycle

`tendril chat` is a Pollinator-facing presentation adapter. It does not own a
separate coding lifecycle.

```text
Direct local developer
    → tendril chat
    → local Stem client projection
    → canonical Seed/Phytomer lifecycle owned by the Stem

External Pollinator
    → REST or MCP
    → same Stem-owned lifecycle and authority
```

The Stem is a deterministic routing and lifecycle kernel; it does not reason.
The terminal does not communicate directly with Sprouts or Terraria.

An external Pollinator uses the same lifecycle through the transport surface:

```text
seedGrow         detached:true + idempotencyKey
sproutWatch      sessionId
phytomerContinue sessionId + intent + idempotencyKey
```

Those are valid lifecycle operations for Pollinators connecting over REST or
MCP. They are not required ceremony for the direct terminal path.



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

Configure a named connection before starting the MCP host:

```bash
tendril-mcp connection set local --endpoint http://127.0.0.1:8080 --credential codex
```

For a Pollinator on another machine, use the Stem's HTTPS DNS name or IP. The
certificate must chain to system roots or a named public trust anchor; ordinary
chain and hostname/IP SAN checks remain enabled:

```bash
tendril-mcp connection set remote \
  --endpoint https://stem.example.net:8443 \
  --credential codex \
  --trust-anchor stem-ca
tendril-mcp diagnose --connection remote
```

The `stem-ca` reference resolves to
`~/.config/tendril/trust-anchors/stem-ca`; it contains only the public CA
certificate. Omit the option for system roots. Remote HTTPS uses TLS identity,
not Unix UID identity. Plain HTTP is accepted only for a literal loopback IP
with the local Unix-owner separation check. Non-loopback plaintext HTTP is
refused before the root is read or sent.

The remote listener is configured on the Stem using
`TENDRIL_REMOTE_LISTEN_ADDR`, `TENDRIL_REMOTE_TLS_CERT`, and
`TENDRIL_REMOTE_TLS_KEY`. All three must be present together; all absent
disables it and incomplete or invalid configuration fails startup closed. TLS
terminates at the Stem with TLS 1.2 minimum. The TLS private key is separate from
Stem access-token signing material. The standalone Gateway is not the supported
remote Pollinator ingress.

The credential reference resolves to
`~/.config/tendril/pollinators/codex`, which must be mode `0600` and owned by
the Pollinator account. The restricted client does not use environment
variables to select its endpoint, credential, or Pollen.

Startup fails closed when:

- no credential is configured;
- the credential file is unsafe;
- the Stem is unavailable;
- a literal-loopback HTTP owner is not established or matches the caller's UID;
- HTTPS certificate trust or hostname/IP SAN verification fails;
- the Stem refuses the root.

For HTTPS, TLS identity replaces Unix UID identity. Only after transport and
readiness checks pass does the client read/present the root; only after minting
does MCP forwarding begin. Diagnostics do not print root or access-token values.

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril-mcp",
      "args": ["--connection", "remote"]
    }
  }
}
```

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
`tendril-mcp`. In-process `tendril mcp` hosts the same Seed/continuation/observation
Core lifecycle as the serving Stem, including restart reconciliation of orphaned
Seed work before it accepts stdio frames. Delegated execution tools still require
a bound `TENDRIL_POLLEN` and the corresponding grants. The operator/no-Pollen
exception applies to the `sproutWatch` observation view only. Single-user
in-process posture does not create the governed OS-principal boundary.

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
