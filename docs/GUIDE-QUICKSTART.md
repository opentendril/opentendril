# OpenTendril Quick Start - your first session

This covers what to do **once OpenTendril is installed**: use the Greenhouse as
the ordinary governed first-use path, observe one task, and review its Git
Fruit.

> [!IMPORTANT]
> **Installation is not here.** If OpenTendril is not installed yet, start with
> **[docs/GUIDE-INSTALL-QUICK.md](GUIDE-INSTALL-QUICK.md)** - the short public
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
| An LLM | Local [Ollama](https://ollama.ai) (default) - or a cloud provider key |

A governed installation runs Docker rootless, under the Stem's own account, so
check it as the Stem: `sudo -u tendril -i docker --version`. The Stem's health
report names both, so step 1 confirms them either way.

**Which runtime actually isolates your code.** Docker is the requirement, but it
is not necessarily what runs a Terrarium. Given no explicit choice, the conductor
prefers **gVisor** when the host supports it, and falls back to Docker's default
runtime otherwise. gVisor is not an alternative to Docker - it is a runtime
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

If you followed the governed installation in
[Quick install](GUIDE-INSTALL-QUICK.md), continue below. For a local/evaluation
installation, skip to [Single-user installations](#single-user-installations).

If you are unsure which you have,
[GUIDE-INSTALL-QUICK.md](GUIDE-INSTALL-QUICK.md) names the two postures;
[GUIDE-INSTALL.md](GUIDE-INSTALL.md) is the detailed reference.

---

# Governed installations

The Stem runs as its own operating-system principal. The ordinary governed
first-use path is through Greenhouse, using the Botanist key deliberately
provided by an administrator. It does not use a Pollinator credential or a
DelegationGrant.

## First governed work through Greenhouse

Follow these steps in order. The governed host setup is in
[Quick install](GUIDE-INSTALL-QUICK.md); the complete hardened procedure is in
[GUIDE-INSTALL.md](GUIDE-INSTALL.md).

1. **Install OpenTendril and complete Stem readiness.** The governed installer
   establishes the Stem's protected control plane, rootless container runtime,
   installed service, and optional Greenhouse image. The install guide's
   readiness checks report the installed Stem posture. The installer leaves
   the Stem stopped until configuration is complete.

2. **Configure the LLM provider.** Run `tendril init` as the Stem and choose a
   supported cloud provider and credential, or configure local Ollama. Provider
   credentials belong to the Stem and are not Botanist or Pollinator
   credentials. The guided provider setup is in
   [Quick install](GUIDE-INSTALL-QUICK.md#governed).

3. **Configure Git identity and connection, then add the Substrate.** Follow
   Stage 5 of [GUIDE-INSTALL.md](GUIDE-INSTALL.md) for the supported GitHub App
   or fine-grained PAT posture and the named Substrate configuration. The
   managed checkout is owned by the Stem.

4. **Verify the Substrate.** As the Stem, verify the configured repository
   connection and its usable Git base:

   ```bash
   sudo -u tendril -H /home/tendril/.local/bin/tendril substrate verify myrepo
   ```

   Replace `myrepo` with the configured Substrate name. If the repository has
   no Git base, use the supported bootstrap instructions in the install guide,
   then verify it again.

5. **Start the configured Stem and check readiness.**

   ```bash
   sudo systemctl enable --now tendril
   curl --fail --silent http://127.0.0.1:8080/health
   ```

6. **Start the installed Greenhouse.** Greenhouse is optional and separate from
   the Stem. The installed path needs no repository checkout or Compose
   invocation:

   ```bash
   sudo /usr/local/sbin/opentendril-greenhouse start
   ```

   The wrapper's supported lifecycle commands are:

   ```bash
   sudo /usr/local/sbin/opentendril-greenhouse start
   sudo /usr/local/sbin/opentendril-greenhouse stop
   sudo /usr/local/sbin/opentendril-greenhouse restart
   sudo /usr/local/sbin/opentendril-greenhouse status
   sudo /usr/local/sbin/opentendril-greenhouse check
   sudo /usr/local/sbin/opentendril-greenhouse address
   ```

   `start` and `restart` require the Stem Unix socket. Installed Greenhouse
   uses Unix Stem transport only, with no normal TCP fallback. Its browser
   address is loopback-only. `stop` affects Greenhouse only and does not stop
   the Stem. Lifecycle calls require explicit administrator authority; no
   Greenhouse-specific sudoers or `NOPASSWD` grant is installed.

7. **Obtain the fixed local address.**

   ```bash
   sudo /usr/local/sbin/opentendril-greenhouse address
   ```

   The normal address is `http://127.0.0.1:4173`.

8. **Deliberately obtain the protected Botanist key.** The Stem always requires
   a non-empty Botanist key. Its resolution order is explicit `BOTANIST_KEY`,
   an existing persisted key, then a generated and persisted key. When the
   Stem generated or reused its key in this governed installation, an
   administrator can display it with:

   ```bash
   sudo -u tendril -H cat /home/tendril/.tendril/api-key
   ```

   If `BOTANIST_KEY` was explicitly configured, the administrator hands off
   that configured value instead. This is the Botanist credential, not a
   Pollinator credential. Ordinary filesystem access to `/home/tendril` is not
   granted.

9. **Open Greenhouse and authenticate.** Open `http://127.0.0.1:4173`, leave
   the Stem address empty for the normal same-origin path, and explicitly paste
   the Botanist key into the required onboarding field. Greenhouse rejects an
   empty or whitespace-only key and verifies it live against the Stem before
   entering. Invalid keys are rejected. Socket reachability is transport, not
   authentication.

   The browser persists the current connection state, including the key, in
   `localStorage`. REST calls send `Authorization: Bearer ...`; the browser
   WebSocket uses the existing authenticated query mechanism. The key is not
   mounted into Greenhouse or injected through Docker environment, and this
   handoff does not use or introduce a secret API/helper.

10. **Select the configured Substrate** in the Greenhouse Workbench. The
    configured name is enough; you do not need internal identifiers.

11. **Enter a meaningful task or goal.** Describe the change or outcome you
    want the Seed to achieve.

12. **Enter verifier argv.** Set the executable and each argument separately.
    For example, `go`, `test`, and `./...` are three argv entries. Greenhouse
    does not assemble or run a shell command in the browser.

13. **Start governed work** with the Workbench's **Start work** button. The
    Stem dispatches detached Seed work and Greenhouse follows the canonical
    work context returned by the Stem. The Botanist already holds Botanist
    authority for this lane. First Greenhouse work requires no Pollinator,
    Pollen identity, refresh root, access token, token file, DelegationGrant,
    raw REST orchestration, or MCP.

14. **Observe the work.** Greenhouse presents current Seed and Phytomer state,
    Sprout activity, verification progress, and structured failures. While
    supported and still running, use the Workbench continuation control to
    provide more intent for that same work. Watch for its terminal state.

15. **Review the resulting Fruit.** Greenhouse reads the deterministic Fruit
    inventory and shows the reported repository, branch, commit, and review
    state when the Stem supplies that provenance. It does not run the verifier
    in the browser or infer Fruit from branch names or commit text. If the Stem
    reports no provenance, the UI does not claim Fruit.

16. **Keep the default branch unchanged** until the Botanist separately reviews
    and accepts/merges the reported Fruit. Greenhouse does not merge Fruit
    automatically.

## Advanced: Pollinator and direct transport integrations

The following sections document separate delegated and direct transport
interfaces. They are not prerequisites for the Botanist's Greenhouse path.
Use them when setting up a Pollinator or another integration that needs its own
Pollen identity, credential, grant, REST, or MCP access.

### Pollinator credential setup

For delegated clients, configure and verify the Substrate as the Stem, then
issue a credential for each Pollinator:

```bash
sudo -u tendril -i tendril substrate add myrepo --repo owner/repo --posture app --app-id 123456 --key /home/tendril/.tendril/app.pem
sudo -u tendril -i tendril substrate list
sudo -u tendril -i tendril substrate get myrepo
sudo -u tendril -i tendril substrate verify myrepo
sudo -u tendril -i tendril pollinator list
sudo -u tendril -i tendril pollinator issue --pollen claude --note "laptop"
```

The issued secret is the Pollinator's durable refresh root. It prints once and
is not stored; give it to that Pollinator, not the Botanist key. Mint a
short-lived access token for governed data routes:

```bash
sudo -u tendril -i tendril pollinator token --pollen claude > ~/.tendril-token
chmod 600 ~/.tendril-token
```

Tokens last at most 15 minutes. Send the access token, not the durable root, on
delegated data requests:

```bash
-H "Authorization: Bearer $(cat ~/.tendril-token)"
```

### 1. Understand the Pollinator credential and grant

A credential proves *who* you are. A grant decides *what* you may do. No grant
means every delegated invocation is denied - the secure default.

Create the grant explicitly through the Stem control plane. Do not edit
`.tendril/grants.yaml` by hand for delegated integration setup. Name every operation
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
absent - deletion and raw Sprout dispatch are not part of the first-use grant.

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

### 2. Make a delegated governed call

The Substrate must already have a Git base - at least one commit on the
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

- **`"pollen":"claude"`** - the Stem *derived* your Pollen from the credential you
  presented. A Pollen claimed in a header is ignored for credential-bearing
  callers, so a caller cannot assert someone else's identity.
- **`"isolated":true`** and the `workspace` path - you get your own worktree,
  under your own Pollen. Two Pollinators never share a tree, so they cannot stage
  each other's files.
- **`"branch":"tendril/claude/work"`** - work you do here happens on a branch the
  Stem owns and can later reclaim. Branches made by hand in a shell are invisible
  to it.

### 3. Hand off a bounded Seed

Use the same Pollinator credential and the same Substrate name. This is the
delegated first task - not the Botanist/operator `tendril seed grow` command,
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

### Botanist Fruit review inventory

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

### 4. Learn what a refusal looks like

A refusal is not a fault. Knowing the difference between these three saves an
afternoon:

| What you did | Response | Meaning |
|---|---|---|
| Sent no credential | `401` | The route is authenticated. |
| Sent an unrecognised bearer | `401 Unauthorized` | Unknown, revoked, expired or forged - all refused the same way. |
| Asked for something outside your grant | `403` | Authenticated fine; not permitted. |

The `403` names all three things it checked, so you know which to change:

```
delegation denied: no active grant covers Pollen "claude",
operation-class "git.prune", substrate "myrepo"
```

If a Substrate is configured but its managed checkout has not been materialized,
an in-grant call returns `409` - a configuration state you fix on the host, not a
server fault.

### 5. Two credential systems, not one

A frequent confusion, worth stating plainly:

| | Held by | Used for |
|---|---|---|
| **`BOTANIST_KEY`** | the operator | the gate, and management routes such as delegation approvals |
| **Pollinator credential** (`tendril_refresh_…`) | each caller | token minting; the remote root is not used on governed data routes |

They are separate on purpose. It is why a Pollinator cannot approve its own
pending confirmation.

### 6. Connect over MCP

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

## 4. Start direct coding - `tendril chat`

`tendril chat` is the direct local coding path. It is a presentation adapter
over the same Stem-owned Seed lifecycle that governs all coding work.

```
tendril chat [--substrate <name>] [--max-iterations N] [--timeout N] -- <verify argv...>
```

A bare `--` is required. Everything after it is the verification command that
bounds success. The verifier is not invented automatically - you must supply it
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

Every governed command in `core.CapabilityNames()` is projected across the command line, the transport surface (REST), and the Model Context Protocol (MCP) surface alike - parity is mechanically checked. REST and CLI use canonical identity; MCP publishes a lower-camelCase primary identifier that maps one-to-one back to that identity. Views and control-plane operations are distinct and are not part of governed command parity. So `tendril phytomer create|list|get|history` manages sessions from a terminal exactly as the transport routes do.

- **[docs/GUIDE-INSTALL-QUICK.md](GUIDE-INSTALL-QUICK.md)** - short install entrypoint
- **[docs/GUIDE-INSTALL.md](GUIDE-INSTALL.md)** - the five invariants, and which configurations satisfy them
- **[docs/GUIDE-GIT-CONNECTION.md](GUIDE-GIT-CONNECTION.md)** - connecting a Substrate to its forge
- `tendril --help` (or `sudo -u tendril -i tendril --help`) - every command
