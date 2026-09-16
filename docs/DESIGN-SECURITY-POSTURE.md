# Design: Security Posture & Trust Boundaries

## Principle

The configured remote HTTPS listener is a separate **Pollinator-only public
projection** inside the same Stem process and under the same Core authority.
It is deliberately narrower than the local/private Stem mux: it is not the
Botanist management interface, the Greenhouse interface, or a second Stem.
TLS terminates at the Stem, so the supported trust boundary does not depend on
a reverse proxy. The Greenhouse remains a separate Botanist/operator surface.
This document records the implemented boundary and its regression-tested
properties.

## Trust zones

Three zones, in decreasing authority.

### 1. Stem (host) — full authority

Runs on the host machine. Holds the operator's LLM provider keys, the Botanist
bearer key (`BOTANIST_KEY`, or the auto-generated `.tendril/api-key`), the mesh
`Ed25519` signing key (see `docs/DESIGN-MESH.md`), and `history.db`. It
is the only zone that can mint mesh grafting tokens, grow Terrariums, or make LLM
provider calls. Every other zone reaches capability only by asking the Stem.

### 2. Greenhouse (optional, operator-facing) — delegated authority

An opt-in, containerised reverse proxy + static SPA (see `docs/GREENHOUSE.md`)
for Botanist observation and management. It holds **no secrets of its own** —
the proxy adds no credentials and bypasses nothing
(`ui/nginx/default.conf.template`). The only credential in play is the
operator's bearer key, entered once during onboarding and stored in the
browser's `localStorage`; the container process itself is stateless. It is not
the public Pollinator ingress and is not a public Greenhouse.

### 3. Terrarium Sprouts — zero authority

Ephemeral Terrariums (backed by isolated providers: Docker, gVisor, or
Firecracker) execute a single Sprout run and then die. (The Host provider
exists separately as an explicit escape that bypasses Terrarium isolation.)
Normal sealed Sprouts receive only explicit execution environment. They do not
implicitly receive the repository or working-directory `.env`,
`TENDRIL_ENV_FILE`, provider API keys, provider-selection or local-inference
variables, Botanist or Pollinator credentials, TLS or signing material,
unrelated Git credentials, or unrelated infrastructure environment. A
requested read-only execution may receive `TENDRIL_READONLY=true`. A
Substrate may explicitly inject its own resolved Git credential only when it
sets `exposeToken: true`; `exposeToken: false` remains credential-free (see
[Conductor fail-closed](#conductor-fail-closed--identity--isolation) below).
Provider interaction, LLM calls, and mesh operations happen on the Stem, never
inside a Terrarium.

For the Docker provider, isolation is enforced at the container level
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
regression fails CI.

The host-side Docker process may retain host settings such as `DOCKER_HOST` and
`PATH` so the Stem can reach Docker. Those settings are not thereby passed as
container environment. Environment values deliberately supplied to a Sprout
can be copied into files, logs, commits, diffs, or other Git-reviewable Fruit.
Fruit is therefore an information channel out of the execution boundary, not
trusted secret storage or an isolation boundary. `--network none` does not make
it safe to inject unrelated Stem secrets into a Terrarium.

## Enforcement

Two properties of this posture are mechanically enforced, not merely documented:

- **Canonical interface parity.** The interface-parity test
  (`TestInterfaceParityCoverage`, `cmd/stem/parity_test.go`) checks the local
  canonical CLI, REST, and MCP adapters against the Core
  `core.CapabilityNames()` registry and fails on drift. The public Pollinator
  listener is a deliberate route and delegated-capability projection of that
  authority; it is not a second registry and does not expose every local
  command, view, or control-plane operation.
- **Terrarium isolation is regression-tested.** The flags in
  [Terrarium Sprouts](#3-terrarium-sprouts--zero-authority) above are covered by the
  provider tests noted there, so a weakening of container isolation breaks the
  build.

## Delegated stoma-pass egress — deny-all default, grant-opened mediation

The stoma-pass operation-class (`stoma.pass`) runs one bounded command
inside the same sealed Terrarium described above. Its egress model maps the
delegation grant's `egress` allow-list onto the existing isolation seams:

- **Deny-all is physical, not policy.** The docker provider pins
  `--network none` for every Terrarium it creates (regression-tested per the
  Terrarium Sprouts section), so the executed command itself can never reach any host —
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

This keeps the sealed-Sprout invariant intact for the `stoma.pass` operation: a
Sprout "cannot reach out on its own; external calls are Stem-mediated."

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

## Conductor fail-closed — identity & isolation

The conductor enforces fail-closed defaults for GitHub identity and shadow-worktree
isolation. Each error message names the exact corrective action.

- **No ambient GitHub identity.** The conductor never reads an ambient host
  `GITHUB_TOKEN`. Substrates declare auth explicitly (`auth: GITHUB_TOKEN`,
  `auth.method: pat+env`, `ssh`, `app`, or `none`). A github.com substrate with no
  declared auth fails closed with an actionable error (`requireGitHubPushAuth` in
  `credentials.go`).

- **Least-privilege Terrarium credential.** A Sprout receives no GitHub token by
  default — the authenticated push runs host-side. A substrate opts in with
  `exposeToken: true` to expose only its own resolved token to in-container tooling;
  the ambient host token is never injected. Short-lived GitHub App installation
  tokens (scoped to the target repo) are recommended over long-lived PATs for Sprout
  work (`githubapp.go`).

- **Isolation fail-closed.** A sequence or single-run step that cannot establish
  shadow-worktree isolation aborts by default with an actionable error. The opt-in
  for a deliberate in-place run is `TENDRIL_ALLOW_HOST_WORKSPACE=true`. Parallel,
  selection, and seed paths are fail-closed (`docker.go`).

## Mesh graft hardening — workspace binding & origin restriction

The `/v1/mesh/graft` WebSocket endpoint enforces fail-closed controls. Both controls are unconditional with no override or
opt-out — cross-workspace tokens and browser-origin callers are never legitimate
for this endpoint.

- **Workspace binding.** `HandleGraftWebSocket` validates `workspacePath` against
  `s.workspace` via `TokenValidationOptions.ExpectedWorkspace`; a mismatched
  claim is rejected with `401 Unauthorized` before the WebSocket upgrade begins.
  `HandleAdminIssueToken` ignores any caller-supplied `workspacePath` — issued
  tokens always carry `s.workspace`, so a caller cannot mint a cross-workspace
  token even with admin access to the issuance endpoint.

- **Origin restriction.** The `websocket.Upgrader`
  in `NewServer` accepts upgrades only when the
  `Origin` header is absent. `Origin` is a browser-controlled header that
  legitimate service-to-service callers (`mesh.Client`, CLI, another Stem) never
  send; this is stricter than a same-host allowlist and requires no configuration
  knob. There is no opt-out: a browser-initiated call to this endpoint is never
  a legitimate use-case by design (the mesh graft endpoint is service-to-service
  only, as documented in `DESIGN-MESH.md`).

## Host execution auditing — loud activation & telemetry

Host execution is an intentional escape hatch that bypasses Terrarium isolation.
It is strictly audited
with both local warnings and verifiable telemetry events, ensuring that bypassing
isolation cannot happen silently.

- **Audited activation.** Host execution is gated by the explicit
  `TENDRIL_ALLOW_HOST_EXECUTION=true` environment variable. When
  enabled, the `host` provider prints
  a loud `stderr` warning (`checkHostExecutionAllowed` in `factory.go`) and
  publishes an `EventHostExecutionActivated` telemetry event. The telemetry
  event is published via the `ActivationObserver` callback pattern, allowing the
  conductor to audit the execution without introducing an eventbus dependency
  into the `terrarium` leaf package.
- **Audited workspace fallback.** The opt-in for an in-place
  shadow-worktree run is `TENDRIL_ALLOW_HOST_WORKSPACE=true`.
  This fallback publishes the exact same
  `EventHostExecutionActivated` telemetry event for consistency across all host
  execution paths.
- **No audit opt-out.** There is no configuration switch to disable the `stderr`
  warning or the telemetry event. The audit trail is the mandatory consequence
  of explicitly opting into host execution; it is not a separate gate.

## Delegation confirm-above — pending confirmation and approval

The `confirmAbove` bound on a delegation grant ensures that high-impact operations require explicit human approval before execution.

- **Pending confirmation state machine.** It creates a pending confirmation (not a denial) when a grant threshold is crossed.
- **Botanist-only approval.** The pending confirmation can only be approved or denied by the Botanist via REST (`/v1/delegation/pending`) or the CLI (`tendril delegation pending`, `tendril delegation approve <id>`). There is no "loosen this" override — the approval surface itself is the mechanism, not a bypass of it.
- **Live-grant re-validation.** A pending approval is validated against the live grant at consumption time, not a stale snapshot. Revoking or narrowing a grant while a confirmation is outstanding takes effect immediately.
- **Single-use and TTL.** An approved confirmation authorizes exactly one matching retry and expires after an hour (1-hour TTL) if unresolved.

## Pollinator credentials and remote HTTPS ingress

The configured remote HTTPS listener is a separate Pollinator-only public route
projection within the same Stem process and shared Core authority. It is not
the Botanist management interface, the Greenhouse interface, the full
local/private Stem mux, a second Stem, or a reverse proxy trust boundary.

Pollinator access has two credential forms:

1. **Durable Pollinator root** — a credential (`tendril_refresh_…`) issued by
   `tendril pollinator issue`, stored by the Stem as a digest, and revocable by
   the Botanist for its Pollen. A remote Pollinator presents it only to
   `POST /v1/pollinator/token`.
2. **Short-lived access token** — a Stem-signed bearer (`tendril_access_…`)
   carrying a Pollen and expiry, hard-capped at 15 minutes. Governed public
   data and MCP requests use this token. Verification is stateless; there is
   no per-token store or denylist.

The public route projection is exactly:

| Route | Public use | Credential |
| --- | --- | --- |
| `GET /health` | Passive in-memory readiness | None |
| `POST /v1/pollinator/token` | Mint a short-lived access token | Durable Pollinator root |
| `POST /v1` | Restricted public MCP | Short-lived access token |
| `POST /v1/seeds/grow` | Synchronous or detached `seed.grow` | Short-lived access token |
| `POST /v1/seeds/grow/async` | Compatibility presentation of detached `seed.grow` | Short-lived access token |
| `GET /v1/seeds/runs/{handle}` | Collect a Seed result | Short-lived access token |
| `POST /v1/phytomers/{sessionId}/continue` | Continue a Seed-owned Phytomer | Short-lived access token |
| `GET /v1/phytomers/{sessionId}/watch` | Observe a Seed-owned Phytomer | Short-lived access token |

Legacy `/v1/sessions/...` aliases are not public. Botanist configuration,
mesh management, pending-confirmation management, chat, WebSocket, Greenhouse,
ordinary Phytomer CRUD, and other local/private surfaces are not projected to
the public listener.

The durable root is invalid on ordinary public data and MCP routes. The
Botanist bearer (`BOTANIST_KEY`, or the generated `.tendril/api-key`) is the
Stem's operator credential for local/private control surfaces and is invalid on
the public Pollinator surface. Public authentication uses the `Authorization`
header. Bearer material in a query string has no public authentication effect.
Pollen, authority, listener provenance, limits, and rate identity do not come
from `Forwarded` or `X-Forwarded-*` metadata. The Stem reads current grants for
each governed admission. Revoking a root prevents the next mint; already-minted
tokens remain valid cryptographic assertions until their expiry, no later than
15 minutes, while current grant removal or narrowing applies to subsequent
admissions.

Public MCP supports protocol initialization plus `tools/list` and `tools/call`.
It does not expose repository-backed `resources/list` or `resources/read`.
`tools/list` exposes the primary MCP identifiers for canonical Core capabilities
in `DelegatedCapabilityNames()` and the existing `sproutWatch` view. Compatibility
aliases are not an independent authority: they may resolve only to an already
allowed delegated Core capability. This public projection does not expose every
capability covered by ordinary local interface parity.

Public `GET /health` is passive in-memory readiness. It does not run `healthmon`
checks, publish health events, inspect repositories, contact providers, invoke
Docker, create Sprouts or Terrariums, or expose the local owner UID. The
local/private `/health` remains the existing active operator-health behavior.

The public transport boundary currently implements these defaults:

| Boundary | Default |
| --- | ---: |
| Simultaneous accepted TCP connections | `128` |
| `ReadHeaderTimeout` | `5s` |
| `IdleTimeout` | `60s` |
| `MaxHeaderBytes` | `32 KiB` |
| Global `ReadTimeout` | unset |
| Global `WriteTimeout` | unset |
| Ordinary public request body maximum | `4 MiB` |
| Token-mint request body maximum | `16 KiB` |
| Simultaneous public request handling | `64` |
| Simultaneous authenticated admissions | `32` |
| Simultaneous long-lived REST observations | `16` |
| Simultaneous token-mint requests | `8` |
| Global token-mint rate | `4 requests/second` |
| Token-mint burst | `8` |

These are public-boundary defaults, not a generic system-wide rate limiter.
Token minting uses one global bucket; it has no IP-derived or forwarding-header-
derived identity. The public listener does not set global HTTP read or write
deadlines.

The remote HTTPS listener is disabled when all three settings are absent:

| Setting | Meaning |
| --- | --- |
| `TENDRIL_REMOTE_LISTEN_ADDR` | Stem HTTPS bind address (`host:port`). |
| `TENDRIL_REMOTE_TLS_CERT` | Stem TLS certificate chain file. |
| `TENDRIL_REMOTE_TLS_KEY` | Separate Stem TLS private-key file. |

All three must be present and valid; incomplete or invalid configuration fails
Stem startup closed. TLS terminates at the Stem and requires TLS 1.2 or later.
The TLS private key is not the Stem access-token signing material.

Remote clients verify the normal certificate chain and hostname/IP SAN using
system certificate roots by default. A Pollinator may configure a named public
`trustAnchor` from its canonical trust-anchor directory; that CA is appended to
system roots and does not disable chain or SAN checks. The client does not use
insecure-skip-verify, TOFU, arbitrary certificate acceptance, an SSH/VPN/tunnel,
or mTLS Pollinator identity. The supported public trust topology terminates
Pollinator TLS directly at the Stem; ordinary networking infrastructure outside
OpenTendril is not part of that trust boundary.

Plain HTTP is supported by the restricted Pollinator client only for a literal
loopback IP and retains local Unix-owner separation. HTTPS uses the verified
Stem TLS identity and does not use Unix UID identity.

When `TENDRIL_LOCAL_SOCKET` is set to an absolute path, the same authenticated
mux is also served on that Unix-domain socket. The local socket is transport
only; reaching it is not authorization. The standalone Gateway remains a
separate listener and is not the supported remote Pollinator ingress.

The public projection shares the Stem Core authority and Seed, Sprout,
Terrarium, and Fruit boundaries. Fruit remains Git-reviewable; it is not secret
storage, and the default branch remains under Botanist control.

### The three surfaces, and which of them is a boundary

Canonical interface parity means the local command line, REST, and Model Context
Protocol adapters expose the same governed operations
(`cmd/stem/parity_test.go`). **It does not mean they establish identity the same
way**, and the public Pollinator projection is deliberately narrower than those
local/canonical surfaces.

| Surface | How a Pollen is established | Trust level |
| --- | --- | --- |
| **REST, remote HTTPS** | root only at the mint route; Stem-signed access token on governed data routes | **proven** — digest or signature verified per request |
| **REST, literal-loopback HTTP** | local root or access-token posture; the restricted client verifies a separate Stem Unix owner | **proven** — digest or signature verified per request |
| **Model Context Protocol**, forwarding | `tendril-mcp` verifies the Stem TLS peer, then uses the Pollinator root only to mint; forwarded frames carry the access token | **proven** — it is the REST path underneath |
| **Model Context Protocol**, in-process | `TENDRIL_POLLEN` | **declared** |
| **Command line** | `TENDRIL_POLLEN` | **declared** |

A **declared** Pollen is an audit control and accident prevention, not a security
boundary — a caller owning its own environment can declare any Pollen it likes.
This is stated at the constructor itself (`cmd/stem/clidelegation.go`) and in the
trust table at `docs/GUIDE-GIT-CONNECTION.md`. It is the **P4** invariant, and
the declared rows above are the paths that deliberately do not meet it.

### Why a declared Pollen is nevertheless sound

Because the paths that accept one are unreachable by any account the boundary
would need to constrain.

On the recommended installation the binary is mode `0750` owned by the Stem and
its home is mode `750`, so no Pollinator-hosting account can execute `tendril` at
all — the reasoning is written out at the install step itself. That is the **P5**
invariant doing the work: the boundary is enforced by the operating system before
any check inside the binary is reached.

So a declared Pollen is only ever accepted where **one principal owns the host**,
and there the caller *is* the Botanist. There is no boundary to cross, and the
declaration buys real value — every delegated operation is authorised against the
grants, audited to `history.db`, and run in that Pollinator's own workspace.

Identity checks are not enforced on the declared paths. The enforcement belongs at the operating system level. `tendril hardiness` reports whether that OS enforcement is correctly configured on a given host.

### A single principal is the assumption, not an accident

Every surface above assumes one Stem per host. Where that assumption breaks —
a governed Stem beside an account that can run its own — the stdio surface
detects it and changes behaviour, described below. The command line does not need
to, because P5 denies it the binary.

Running more than one Stem on a host is possible today by giving each its own
`PORT`, but it is neither discoverable nor enforced, and Stem discovery resolves
a single address per host.

`tendril hardiness` reports a Stem owned by another principal when it finds one,
**below the verdict and without changing it.** The five invariants are properties
of an installation; a second Stem is a property of the host, and grading it would
claim this installation is weaker when it is not. The report is there because the
verdict is otherwise read as a promise about the machine: a caller can hold a
control plane it fully owns without violating P1 through P5 as they are measured,
by simply declining to involve the Stem being measured.

### MCP

MCP is a stdio-facing client protocol, not a separate network listener.
`tendril-mcp` forwards frames to the governed Stem over the configured HTTP
transport. For HTTPS it completes certificate-chain and hostname/IP SAN
verification before it reads or presents the durable root. It then uses the
verified transport for readiness, token minting, and governed frames. A remote
connection uses TLS identity rather than Unix UID identity.

The stdio surface (`tendril mcp`) selects its control plane at startup, because
**personal-stdio is only sound where one principal owns the host.** Where a Stem
owned by another principal is present, a surface building its own control plane
from the caller's working directory would let the caller rewrite the grants that
gate it — the condition `docs/GUIDE-INSTALL.md` forbids. Ownership is established
by comparing the owner published on the Stem's health surface against the
caller's own, so a single-principal installation running its own Stem is
unaffected.

| Condition | Control plane | Pollen |
| --- | --- | --- |
| **No governed Stem** | in-process, from the working directory | bound from `TENDRIL_POLLEN` |
| **Governed Stem, credential configured** | the governed Stem's, reached over loopback | derived there from the presented credential |
| **Governed Stem, no credential** | refuses, naming the command that issues one | — |

The restricted bridge resolves its named credential reference below
`~/.config/tendril/pollinators/`. Connection metadata may name a `trustAnchor`
under `~/.config/tendril/trust-anchors/`; absent that, HTTPS uses system roots.
`TENDRIL_POLLEN` binds a Pollen on the in-process path only — where the surface
forwards, the presented credential derives the Pollen and the variable has no
effect.

## References

- `cmd/stem/internal/terrarium/docker.go` + `provider_test.go` — Terrarium isolation
  flags and their regression tests.
- `cmd/stem/parity_test.go` — CLI/REST/MCP capability parity enforcement.
- `docs/GREENHOUSE.md` — Greenhouse deployment and auth contract.
- `docs/DESIGN-MESH.md` — mesh token model for delegated, short-lived tokens.
