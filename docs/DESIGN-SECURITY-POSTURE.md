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

Ephemeral Terrariums (backed by Docker, gVisor, Firecracker, or Host) that execute a single Sprout run and then die. They
hold no credentials at all — LLM calls and mesh operations happen on the Stem,
never inside a Terrarium. No GitHub token is injected by default, upholding the
zero-authority guarantee; a substrate must explicitly opt in with `exposeToken: true`
to expose its own resolved token to in-container tooling (see
[Conductor fail-closed](#conductor-fail-closed--identity--isolation) below).
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

## Credential model — two-tier Pollinator access

Pollinator REST access is **two-tier**:

1. **Durable refresh root** — a Pollinator credential (`tendril_refresh_…`) issued by
   `tendril pollinator issue`. Digest-stored, revocable by Pollen, no inherent
   lifetime. Presented **only to mint** (CLI `tendril pollinator token`, or
   `POST /v1/pollinator/token`).
2. **Short-lived access token** — a Stem-signed bearer (`tendril_access_…`) carrying
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

### The three surfaces, and which of them is a boundary

Capability parity means the command line, REST and the Model Context Protocol
expose the same operations (`cmd/stem/parity_test.go`). **It does not mean they
establish identity the same way**, and conflating the two is the mistake this
section exists to prevent.

| Surface | How a Pollen is established | Trust level |
| --- | --- | --- |
| **REST** | issued credential or Stem-signed access token | **proven** — signature or digest verified per request |
| **Model Context Protocol**, forwarding | credential presented to the governed Stem, which derives the Pollen | **proven** — it is the REST path underneath |
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

**The consequence worth carrying forward:** adding identity checks to the
declared paths would defend a boundary that does not exist on the installation
where those paths run, while adding a mode and a refusal path to the
installation that works today. The enforcement belongs at the operating system,
which is where P1 through P5 put it. `tendril hardiness` reports whether that
enforcement is real on a given host, in those words.

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

MCP has no networked ingress. Scoped access tokens are a **REST** surface;
networked MCP is a deferred consumer of the same gate.

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

The credential is a durable root, read from the location defined by
`TENDRIL_POLLINATOR_CREDENTIAL`, `TENDRIL_MCP_CREDENTIAL`, or the default
`~/.config/tendril/pollinators/<pollen>`; access tokens are minted from it on demand because
their ≤15-minute cap is shorter than a working session. `TENDRIL_POLLEN` binds a
Pollen on the in-process path only — where the surface forwards, the presented
credential derives the Pollen and the variable has no effect.

## References

- `cmd/stem/internal/terrarium/docker.go` + `provider_test.go` — worker isolation
  flags and their regression tests.
- `cmd/stem/parity_test.go` — CLI/REST/MCP capability parity enforcement.
- `docs/GREENHOUSE.md` — Tendril OS deployment and auth contract.
- `docs/DESIGN-MESH.md` — mesh token model; the one place delegated,
  short-lived tokens already exist today.
