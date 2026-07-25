# Design: Security Posture & Trust Boundaries

## Principle

The **Tendril OS** (Greenhouse) is the optional, potentially multi-user,
network-facing surface, so it must be the **thinnest trusted layer**. It
**delegates and proxies; it does not accumulate** authority or long-lived secrets.
"Less attack surface" is a design rule to enforce, not an aspiration — this
document records what is enforced (with tests) and the one place the model is still
maturing.

## Trust zones

Three zones, in decreasing authority.

### 1. Stem (host) — full authority

Runs on the host machine. Holds the operator's LLM provider keys, the Botanist
bearer key (`BOTANIST_KEY`, or the auto-generated `.tendril/api-key`), the mesh
`Ed25519` signing key (see `docs/DESIGN-MESH.md`), and `history.db`. It
is the only zone that can mint mesh grafting tokens, grow Terrariums, or make LLM
provider calls. Every other zone reaches capability only by asking the Stem.

### 2. Tendril OS / Greenhouse (optional, network-facing) — delegated authority

An opt-in, containerised reverse proxy + static SPA (see `docs/GREENHOUSE.md`).
It holds **no secrets of its own** — the proxy adds no credentials and bypasses
nothing (`ui/nginx/default.conf.template`). The only credential in play is the
operator's bearer key, entered once during onboarding and stored in the browser's
`localStorage`; the container process itself is stateless. It reaches exactly
`/health`, `/v1*`, and `/ws` on the Stem — nothing else on the host is reachable
through it.

### 3. Worker (Terrarium Sprouts) — zero authority

Ephemeral Docker containers that execute a single Sprout run and then die. They
hold no credentials at all — LLM calls and mesh operations happen on the Stem,
never inside a Terrarium. No GitHub token is injected by default, upholding the
zero-authority guarantee; a substrate must explicitly opt in with `exposeToken: true`
to expose its own resolved token to in-container tooling (see
[Conductor fail-closed](#conductor-fail-closed--identity--isolation) below).
Isolation is enforced at the container level
(`cmd/stem/internal/terrarium/docker.go`):

```
--network none
--cap-drop=ALL
--security-opt=no-new-privileges:true
--pids-limit <default 512, spec-overridable>
--cpus / --memory <spec-overridable>
```

These flags are asserted by `cmd/stem/internal/terrarium/provider_test.go`
(`TestDockerProviderCreate*`, `Test*ProviderCreateDefaultsToPidsLimit`), so a
regression fails CI rather than only a manual audit.

## Enforcement

Two properties of this posture are mechanically enforced, not merely documented:

- **No command capability beyond the CLI.** The interface-parity test
  (`TestInterfaceParityCoverage`, `cmd/stem/parity_test.go`) diffs the CLI, REST,
  and MCP capability sets against the canonical `core.CapabilityNames()` registry
  and fails on drift. The Tendril OS (REST) surface is constrained to the same
  governed capability set as the CLI — an attacker who fully compromises the OS
  reaches no command the CLI did not already expose.
- **Terrarium isolation is regression-tested.** The flags in
  [Worker](#3-worker-terrarium-sprouts--zero-authority) above are covered by the
  provider tests noted there, so a weakening of container isolation breaks the
  build.

## Delegated stoma-pass egress — deny-all default, grant-opened mediation

The stoma-pass operation-class (`stoma.pass`) runs one bounded command
inside the same sealed Terrarium described above. Its egress model maps the
delegation grant's `egress` allow-list onto the existing isolation seams:

- **Deny-all is physical, not policy.** The docker provider pins
  `--network none` for every Terrarium it creates (regression-tested per the
  Worker section), so the executed command itself can never reach any host —
  with or without a grant, delegated or not.
- **The allow-list opens Stem-mediated reach only.** The single egress channel
  a stoma-pass execution has is its optional `fetch` list: URLs the **Stem**
  retrieves on the host, *before* the sealed container runs, delivering the
  payloads read-only under `/tmp/egress` inside the Terrarium
  (`cmd/stem/internal/conductor/stoma.go`). Every fetch URL must name a
  host on the `EgressPolicy` built from the matching grant's `egress` field —
  exact host or host:port, no wildcards. An empty list (any non-delegated
  invocation, or a grant without egress hosts) denies every fetch: deny-all
  requires zero configuration.
- **No self-escalation.** The allow-list travels on an input field with no
  JSON surface (`core.StomaPassInput.Egress`, tagged `json:"-"`): only
  the Stem's own call sites populate it, after the delegation authorizer has
  matched a grant, so no transport caller can widen its own egress.

This keeps the sealed-Sprout invariant intact for the new operation-class: a
worker "cannot reach out on its own; external calls are Stem-mediated."

## Data-at-rest encryption

OpenTendril applies application-level AES-GCM encryption to sensitive fields before they reach local SQLite storage, ensuring the binary remains CGO-free (no SQLCipher required). 

- **What is encrypted:** `rhizome` SQLite `stubContent` and memory `content`, plus `historydb` payload columns (`messages.content`, `sessions.preferences`, `sproutruns.transcript`/`output`/`error`/`genotype`, `seedruns.goal`/`diff`/`logs`/`error`, `events.data`).
- **What is deliberately not encrypted:** Structural/index columns remain plaintext to allow fast FTS queries. Remote-backend fields (e.g. Pinecone/Weaviate metadata) are sent in the clear, but this is explicitly consent-gated.
- **Two-tier key model:**
  - **Tier-1 (auto-key):** A generated key (`.tendril/rhizome.key`) provides defense-in-depth against casual reads, but is not a boundary against a full directory read (e.g., folder sync or disk backup).
  - **Tier-2 (env key):** The operator-supplied `OPEN_TENDRIL_INDEX_KEY` is never persisted and provides the real control against at-rest compromise.
- **Env knobs:**
  - `OPEN_TENDRIL_INDEX_KEY` to provide the Tier-2 encryption key.
  - `TENDRIL_ENCRYPT_AT_REST` to globally opt out of history database payload encryption.
  - `TENDRIL_MEMORY_REMOTE_CLEARTEXT_ACK` must be explicitly set to acknowledge cleartext egress if selecting a remote memory backend.
- **Explicitly deferred items:** Key rotation is deferred, though the `tnd:atrest:1:<keyID>:` prefix leaves the door open for future support. Active re-encryption or scrubbing verbs do not yet exist — existing plaintext rows are simply read lazily until overwritten.

## Conductor fail-closed — identity & isolation

The conductor enforces fail-closed defaults for GitHub identity and shadow-worktree
isolation. These replaced prior fail-open defaults and constitute breaking changes;
each error message names the exact corrective action.

- **No ambient GitHub identity.** The conductor never reads an ambient host
  `GITHUB_TOKEN`. Substrates declare auth explicitly (`auth: GITHUB_TOKEN`,
  `auth.method: pat+env`, `ssh`, `app`, or `none`). A github.com substrate with no
  declared auth fails closed with an actionable error (`requireGitHubPushAuth` in
  `credentials.go`). There is no compatibility switch that restores the old ambient
  behaviour.

- **Least-privilege Terrarium credential.** A Sprout receives no GitHub token by
  default — the authenticated push runs host-side. A substrate opts in with
  `exposeToken: true` to expose only its own resolved token to in-container tooling;
  the ambient host token is never injected. Short-lived GitHub App installation
  tokens (scoped to the target repo) are recommended over long-lived PATs for Sprout
  work (`githubapp.go`).

- **Isolation fail-closed.** A sequence or single-run step that cannot establish
  shadow-worktree isolation aborts by default with an actionable error. The opt-in
  for a deliberate in-place run is `TENDRIL_ALLOW_HOST_WORKSPACE=true`. Parallel,
  selection, and seed paths were already fail-closed; this extends the guarantee to
  all paths (`docker.go`).

- **Migration note (breaking change).** These defaults flip prior fail-open
  behaviour. Operators migrating from earlier versions will encounter errors on
  first run for any substrate relying on ambient auth or implicit host-workspace
  fallback. Each error names the exact fix (`auth: …`, `TENDRIL_ALLOW_HOST_WORKSPACE`);
  no compatibility switch restores the old ambient behaviour.

## Mesh graft hardening — workspace binding & origin restriction

The `/v1/mesh/graft` WebSocket endpoint previously had two fail-open defaults
that are now fail-closed. Both controls are unconditional with no override or
opt-out — cross-workspace tokens and browser-origin callers are never legitimate
for this endpoint.

- **Workspace binding (was: unbound).** `HandleGraftWebSocket` previously
  verified issuer, audience, and scope but did not bind the `workspacePath` JWT
  claim to the server's own workspace (`server.go`). A token minted for any
  workspace would be accepted. It now validates `workspacePath` against
  `s.workspace` via `TokenValidationOptions.ExpectedWorkspace`; a mismatched
  claim is rejected with `401 Unauthorized` before the WebSocket upgrade begins.
  `HandleAdminIssueToken` ignores any caller-supplied `workspacePath` — issued
  tokens always carry `s.workspace`, so a caller cannot mint a cross-workspace
  token even with admin access to the issuance endpoint.

- **Origin restriction (was: always-true CheckOrigin).** The `websocket.Upgrader`
  in `NewServer` previously set `CheckOrigin: func(r *http.Request) bool { return true }`,
  accepting upgrades from any origin. It now accepts upgrades only when the
  `Origin` header is absent. `Origin` is a browser-controlled header that
  legitimate service-to-service callers (`mesh.Client`, CLI, another Stem) never
  send; this is stricter than a same-host allowlist and requires no configuration
  knob. There is no opt-out: a browser-initiated call to this endpoint is never
  a legitimate use-case by design (the mesh graft endpoint is service-to-service
  only, as documented in `DESIGN-MESH.md`).

## Host execution auditing — loud activation & telemetry

Host execution is an intentional escape hatch that bypasses Terrarium isolation.
Previously, activation of this path was unaudited. It is now strictly audited
with both local warnings and verifiable telemetry events, ensuring that bypassing
isolation cannot happen silently.

- **Audited activation (was: silent).** Host execution is gated by the explicit
  `TENDRIL_ALLOW_HOST_EXECUTION=true` environment variable. Previously, when
  this was enabled, the `host` provider would activate silently. It now prints
  a loud `stderr` warning (`checkHostExecutionAllowed` in `factory.go`) and
  publishes an `EventHostExecutionActivated` telemetry event. The telemetry
  event is published via the `ActivationObserver` callback pattern, allowing the
  conductor to audit the execution without introducing an eventbus dependency
  into the `terrarium` leaf package.
- **Audited workspace fallback (was: silent).** The opt-in for an in-place
  shadow-worktree run is `TENDRIL_ALLOW_HOST_WORKSPACE=true`. Previously, this
  fallback was also unaudited. It has been retrofitted to publish the exact same
  `EventHostExecutionActivated` telemetry event for consistency across all host
  execution paths.
- **No audit opt-out.** There is no configuration switch to disable the `stderr`
  warning or the telemetry event. The audit trail is the mandatory consequence
  of explicitly opting into host execution; it is not a separate gate.

## Delegation confirm-above — pending confirmation and approval

The `confirmAbove` bound on a delegation grant ensures that high-impact operations require explicit human approval before execution.

- **Pending confirmation state machine (was: blanket denial).** Previously, `confirmAbove` denied everything because no impact was wired and there was no confirmation surface. It now creates a pending confirmation (not a denial) when a grant threshold is crossed.
- **Botanist-only approval.** The pending confirmation can only be approved or denied by the Botanist via REST (`/v1/delegation/pending`) or the CLI (`tendril delegation pending`, `tendril delegation approve <id>`). There is no "loosen this" override — the approval surface itself is the mechanism, not a bypass of it.
- **Live-grant re-validation.** A pending approval is validated against the live grant at consumption time, not a stale snapshot. Revoking or narrowing a grant while a confirmation is outstanding takes effect immediately.
- **Single-use and TTL.** An approved confirmation authorizes exactly one matching retry and expires after an hour (1-hour TTL) if unresolved.

## Credential model — two-tier Pollinator access

Pollinator REST access is **two-tier**:

1. **Durable refresh root** — a Pollinator credential (`tendril_…`) issued by
   `tendril pollinator issue`. Digest-stored, revocable by Pollen, no inherent
   lifetime. Presented **only to mint** (CLI `tendril pollinator token`, or
   `POST /v1/pollinator/token`).
2. **Short-lived access token** — a Stem-signed bearer (`tendrilat_…`) carrying
   a Pollen and an expiry, hard-capped at **≤15 minutes**. Surfaces accept it
   per request. Verification is **stateless** (signature against the Stem public
   key); there is no per-token store or denylist.

**Revocation is at the root:** revoke the credential → minting stops → outstanding
tokens age out within the cap. Tokens are not individually revocable.

**Botanist key** (`BOTANIST_KEY`, or the generated `.tendril/api-key`) remains the
Stem's own unscoped bearer for operator/CLI/Greenhouse use. It is not a
Pollinator credential and is not exchanged for access tokens.

### Bind posture (self-declaring exposure)

| Bind | Env | Data routes |
| --- | --- | --- |
| **Loopback (default)** | `TERROIR_HOST` unset → `127.0.0.1` | Durable root credentials still accepted (local personal setups unchanged). |
| **Off-host** | e.g. `TERROIR_HOST=0.0.0.0` | Durable roots **refused** on data routes (401 → mint); access tokens and `BOTANIST_KEY` unchanged. Mint endpoint still accepts the root. |

Exposure is self-declaring: there is no separate “require tokens” flag. Narrowing
the bind is the only opt-out of the hardened posture.

### MCP

MCP stays **personal-stdio** (`tendril serve mcp stdio` + `TENDRIL_POLLEN`). Scoped
access tokens are a **REST** surface; networked MCP is a deferred consumer of the
same gate.

## References

- `cmd/stem/internal/terrarium/docker.go` + `provider_test.go` — worker isolation
  flags and their regression tests.
- `cmd/stem/parity_test.go` — CLI/REST/MCP capability parity enforcement.
- `docs/GREENHOUSE.md` — Tendril OS deployment and auth contract.
- `docs/DESIGN-MESH.md` — mesh token model; the one place delegated,
  short-lived tokens already exist today.
