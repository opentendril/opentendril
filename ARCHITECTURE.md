# OpenTendril Architecture

This document is the authoritative description of the current implemented OpenTendril architecture. Repository documentation describes current state only.

## Purpose/Authority

OpenTendril is a governed framework for executing AI-driven code modifications. It delegates authority from a human (**Botanist**) to an external entity (**Pollinator**) via an identity/principal (**Pollen**) that the Pollinator presents and a grant names. Pollen may be cryptographically proven when issued credentials are configured; otherwise, it is declared.

Delegated work is authorized by durable `DelegationGrant`s. A grant names a Pollen and specifies allowed operation classes, exact allowed Substrates, an optional egress allow-list, an optional expiry, and an optional impact-confirmation threshold. Zero grants means all delegated execution is denied. An invocation cannot carry or widen its own grant. Impact confirmation is an escalation threshold, not a requirement for normal uninterrupted delegated operation.

The core invariant is that governed capabilities are executed only by the **Stem** (the host control plane), mediated by its Core authority. 

## Anatomy

OpenTendril is built on a strict synthetic biological architecture:
- **Botanist**: The human owner holding absolute authority.
- **Pollinator**: An external client or AI builder initiating requests.
- **Pollen**: The identity/principal a Pollinator presents and a delegation grant names.
- **Stem**: The host-side deterministic routing/lifecycle kernel and governed Core authority.
- **Mycorrhizae**: The external cognitive LLM side.
- **Roots**: OpenTendril's own provider/model connectivity organ (under `roots/`), managing provider clients, model discovery, and routing to reach the Mycorrhizae.
- **Terrarium**: The ephemeral isolation boundary where execution takes place.
- **Sprout**: The ephemeral executing code body inside the Terrarium.
- **Substrate**: The target source repository.
- **Fruit**: The git-reviewable output of a Sprout.
- **Phytomer**: A persistent interaction/session thread; a bounded execution/session context from which work may emerge.
- **Plasmid**: A modular injected context or capability/tool payload.
- **Sequence**: A directed acyclic graph (DAG) of execution steps.
- **Branch**: A capability group and security zone.
- **Greenhouse**: The Botanist observation and Command Center UI (optionally deployed with a reverse proxy).

## Governed path

All external requests enter through a transport adapter (CLI, REST, or Model Context Protocol). These adapters strictly translate the transport protocol into internal Go structs.

No business logic resides in the adapters. The adapters dispatch to the **Stem Core** (`cmd/stem/internal/core`), which holds the canonical governed capability registry. MCP transport identifiers are adapter projections of those canonical Core names. Interface parity is mechanically enforced across the canonical local adapters by tests; the public Pollinator listener is a deliberately narrower projection. The Core executes the capability and enforces policy. Views and control-plane operations are distinct and not Pollinator-facing governed command capabilities.

### Remote Pollinator ingress

The remote HTTPS listener is a separate Pollinator-only public projection in the
same Stem process and under the same Core authority:

```text
Internet/reachable Pollinator
        |
        | verified HTTPS + Pollinator credentials
        v
separate Pollinator-only public mux
        |
        v
same Stem process / same Core authority
        |
        +-> governed Seed / Phytomer / Sprout lifecycle
        |
        +-> Terrarium
        |
        +-> Git-reviewable Fruit
```

The public projection is not the Botanist management interface, the Greenhouse
interface, the full local/private Stem mux, a second Stem, or a reverse proxy
trust boundary. Botanist configuration, mesh management,
pending-confirmation management, chat, WebSocket, ordinary Phytomer CRUD, and
other local/private surfaces remain private and are not projected publicly.

The public route set is exactly `GET /health`, `POST /v1/pollinator/token`,
`POST /v1`, `POST /v1/seeds/grow`, `POST /v1/seeds/grow/async`,
`GET /v1/seeds/runs/{handle}`,
`POST /v1/phytomers/{sessionId}/continue`, and
`GET /v1/phytomers/{sessionId}/watch`. Legacy `/v1/sessions/...` aliases are
not public. Public MCP accepts initialization, `tools/list`, and `tools/call`
only; repository-backed `resources/list` and `resources/read` are not public.
Its tool list contains primary identifiers for `DelegatedCapabilityNames()`
and the `sproutWatch` view. Compatibility aliases can resolve only to an
already-allowed delegated Core capability. The public projection does not
register the private Botanist route `GET /v1/fruit` and does not expose a Fruit
inventory MCP tool or view.

`TENDRIL_REMOTE_LISTEN_ADDR`, `TENDRIL_REMOTE_TLS_CERT`, and
`TENDRIL_REMOTE_TLS_KEY` must be configured together; with all three absent the
listener is disabled, and partial or invalid configuration fails Stem startup
closed. TLS terminates directly at the Stem with TLS 1.2 or later. The TLS
private key is separate from the Stem access-token signing material. Remote
clients use normal certificate-chain and hostname/IP SAN verification with
system roots, optionally augmented by one named public `trustAnchor`. There is
no insecure verification, TOFU, arbitrary certificate acceptance, mTLS
Pollinator identity, SSH/VPN, or tunnel requirement.

The durable Pollinator root is accepted only at the mint route. Public data and
MCP requests use the short-lived Stem-signed access token in the
`Authorization` header; the Botanist bearer is invalid publicly, the durable
root is invalid on ordinary public data/MCP routes, query-string bearer
material has no public authentication effect, and forwarding headers do not
affect Pollen, authority, listener provenance, or limits. Root revocation
affects the next mint, grant removal or narrowing affects the next governed
admission, and already-minted
tokens age out cryptographically within their 15-minute cap.

Public `GET /health` is passive in-memory readiness. It does not run active
health checks, publish health events, inspect repositories, contact providers,
invoke Docker, create Sprouts or Terrariums, or expose the local owner UID.
Local/private `/health` remains active operator health. The public transport
uses the current documented connection, timeout, body, concurrency, and global
token-mint bounds; it has no IP-derived or forwarding-header-derived rate
identity and no global HTTP read/write deadline.

### Direct local coding path

`tendril chat` is an L1 Pollinator-facing presentation adapter. It does not own a separate coding lifecycle.

A first non-empty terminal line is routed to `seed.grow` on the running Stem, with `detached: true`. The Stem owns handle mint, durable opening, and background growth through the canonical Seed lifecycle. The returned Phytomer is the active interaction identity. The terminal is a presentation layer over the Stem-owned lifecycle; it does not communicate directly with Sprouts or Terraria.

Further terminal input during an active Seed reaches `phytomer.continue` on that same Phytomer. Intent is durable before acknowledgment and delivered at the next permitted cognitive boundary. It does not interrupt or inject into a live Sprout. The terminal does not widen Seed bounds.

Observation is the safe `PhytomerObservation` projection (`sprout.watch`). It does not expose raw intent, reasoning, or credentials. Fruit is Git-reviewable output reported on a branch; the default branch remains unchanged until a human merges.

`PORT` controls the local Stem endpoint, defaulting to `8080`. Both the Stem and the local client read the same environment variable; no additional configuration is required for coexistence on an alternate port.

Continued intent follows this implemented path:

```text
Pollinator
-> transport adapter
-> Stem Core authorization
-> durable continuation ledger bound to the Phytomer
-> next permitted Mycorrhizal/Sprout boundary
```

There is no direct Pollinator → Sprout/Terrarium channel. Accepted continued intent is not injected into a live Sprout.

## Cognitive path

The **Mycorrhizae** (LLM) is the cognitive engine, running entirely externally. The Stem interacts with the Mycorrhizae via the `roots/llm` package, which acts as the provider connectivity and model routing layer. 

The Stem is a deterministic routing and lifecycle kernel; it is not a reasoning component and does not plan or reason cognitively. Before each Sprout growth, the Conductor deterministically assembles a bounded task-specific Plasmid from current Substrate evidence after resolving the execution workspace and refreshing the Rhizome index. Evidence may include explicit file anchors, Rhizome structural matches, current Git-state paths, deterministic associated tests and documentation, and source-local project memory. The selection is Stem execution preparation, not Stem reasoning.

The task context is assembled against the actual execution workspace and is refreshed for later Seed iterations as their candidate revision changes. Task context and curated genome content share the existing bounded evidence envelope. Repository evidence is explicitly untrusted and does not widen execution authority. Raw selected evidence is not copied into the observable Sprout transcript; safe selection provenance is published through `task-context-assembled`. The Mycorrhizae receives these Plasmids and repository maps and returns tool calls or text. Continued developer intent reaches the Mycorrhizae only as a delimited section of the next Sprout prompt, composed at a new cognitive boundary from the durable continuation ledger.

## Execution/Containment

Normal **Sprout** execution is bounded by a **Terrarium**. Implemented isolated providers include Docker, gVisor, and Firecracker. Docker and gVisor use container isolation, while Firecracker uses a microVM. The Host provider is an explicit escape that bypasses Terrarium isolation and requires `TENDRIL_ALLOW_HOST_EXECUTION=true`.

The Stem communicates with execution through the Terrarium provider/Stoma boundary; the concrete transport is provider-specific (e.g., stdin/stdout for some containers, vsock JSON for Firecracker). By default, isolated Terraria fail closed: they cannot reach the host network or the host workspace. Egress is mediated strictly by the Stem via an allow-listed fetch mechanism.

Docker Terraria retain the current containment flags `--network none`,
`--cap-drop=ALL`, `--security-opt=no-new-privileges:true`, and a bounded
`--pids-limit` (default `512`, spec-overridable).

Normal Terrarium environment is explicit-only. It does not implicitly inherit a
repository or working-directory `.env`, `TENDRIL_ENV_FILE`, provider API keys,
provider-selection or local-inference variables, Botanist or Pollinator
credentials, TLS or signing material, unrelated Git credentials, or unrelated
infrastructure environment. A requested read-only execution may receive
`TENDRIL_READONLY=true`; a Substrate's resolved Git credential is supplied only
when that Substrate explicitly sets `exposeToken: true`, while
`exposeToken: false` remains credential-free. Host-side Docker settings such as
`DOCKER_HOST` and `PATH` allow the Stem to reach Docker but do not become
container environment. Provider interaction remains Stem-side.

Anything deliberately supplied to a Sprout can be copied into files, logs,
commits, diffs, or other Git-reviewable Fruit. Fruit is an information channel
out of the execution boundary, not trusted secret storage or an isolation
boundary; `--network none` does not make unrelated Stem-secret injection safe.

The Host Terrarium provider is a separate, explicit isolation escape. It requires `TENDRIL_ALLOW_HOST_EXECUTION=true`, runs with full host-user permissions, bypasses Terrarium isolation entirely, and emits a loud audit warning upon activation.

For local Git Sprout runs, the Stem normally creates a shadow worktree before starting the Terrarium, isolating execution from the active source workspace. If shadow-worktree establishment fails, execution fails closed unless `TENDRIL_ALLOW_HOST_WORKSPACE=true` explicitly permits use of the active workspace.

## Sequence

A **Sequence** is a Directed Acyclic Graph (DAG) of execution steps managed by the Conductor. The Conductor schedules these steps with a configurable concurrency limit and a defined failure policy (`halt`, `retry`, `pause`). 

Sequences support:
- **Parallel sprouting**: Map-Reduce pattern across multiple Sprouts.
- **Phenotypic selection**: Speculative execution of multiple temperature-spread Sprouts and generational genetic selection via fitness tests.
- **Recursive Debugger**: Automatically budding self-healing Sprouts when a verification step fails.
- **Macrophage verification**: A post-Sprout deterministic Go fuzz test (`go test -fuzz`) executed inside a dedicated, network-isolated Terrarium to harden parsers and volatile functions.
- **Dynamic Steps**: Meristem-produced dynamic steps appended to the DAG.

## Fruit/Review

Successful Sprout execution produces **Fruit**—a Git-reviewable output.

A commit is produced when a run has completed, has attributed modified files, did not error, is not readonly or investigation-only, and successfully measured its changes.

The Stem may commit changes on an isolated staging branch (`sprout/task-*`). However, if a writable, merge-back enabled local Substrate is already on a non-protected branch, the Stem operates directly on it without unconditionally creating a `sprout/task-*` branch. 

When merge-back is enabled, the Terrarium commit is internally merged back into the source branch. If the Substrate is remote, the commit may be pushed. Internal merge-back is not Botanist acceptance.

The host workspace is not unconditionally failed-closed when dirty. The current `RunSprout` path stashes active, uncommitted Git changes (`git stash save -u`) prior to isolated execution, and teardown restores the stash.

Protected kernel paths enforce a floor of safety, refusing automated merges that modify the Stem's own kernel or governance files. The Botanist always retains the final merge decision.

**Managed App+API Substrates.** When a managed Substrate is configured with `commit: api`, the Stem creates the commit server-side via the GitHub GraphQL/REST API using the GitHub App credential. This eliminates the need for a local signing key; GitHub supplies the commit identity and the `Verified` badge. The `commit: api` mode is only valid with GitHub App authentication (`auth.method: app`). Any other auth method is refused at setup-verify time and fails before a Seed grows.

`tendril git setup --verify` for managed App/API Substrates additionally confirms that the GitHub App installation holds repository **contents write** permission, which is required to create the Fruit ref and commit. This check is strictly read-only (no branch, commit, push, or pull request is created). A missing or read-only contents permission fails immediately with an actionable message that names the required permission and the installation settings path. The write-permission check is only performed for managed Substrates with `commit: api`; path and ephemeral checkouts are unaffected.

An empty managed App/API Substrate has a Botanist-only setup path:
`tendril git bootstrap --substrate <name>`. The Stem authenticates and checks
the empty repository, resolves the configured or repository default branch (or
the Botanist's explicit branch input), shows the one empty-tree root commit,
and requires confirmation. Publication uses an expected-absent target-ref
lease and never overwrites an existing ref. The resulting root commit is setup
state, not Fruit; `git setup --verify` remains the read-only readiness check.

For `seed.grow` on a managed App/API Substrate, each writing iteration runs in a Tendril-owned RunWorkspace. A successful iteration is transferred into a local `tendril/seed-*` checkpoint only after newly introduced paths are valid repository-relative content; internal execution locations (host RunWorkspace paths, Terrarium mounts, pseudo-home expansions) cannot become Fruit. The next iteration starts from that checkpoint. These checkpoints are internal convergence state: they are not remotely published, do not receive GitHub credentials, use a Tendril-owned local Git identity, and are not returned as Botanist-reviewable Fruit. Deterministic verification runs against that accumulated candidate in a network-sealed Terrarium; the Stem records a bounded verification diagnostic per iteration (`passed`, `predicate-failed`, or `infrastructure-failed`, with exit code and timeout). After convergence, the accumulated Seed result is published through the Stem-held managed App/API path. Mutation outcomes are classified from transport facts, including whether the request was written, and an uncertain result is reconciled read-only against the exact intended parent, message, changed paths, and file contents. Only one final identical mutation is permitted after reconciliation proves the target ref is still at the expected base; no third mutation is issued. Only the GitHub-created review branch and authoritative GitHub commit OID are reported as Fruit. If publication configuration, execution-plan resolution, or final API publication fails, the completed Seed execution evidence is preserved, the terminal status is `fruit-publication-failed`, a structured safe diagnostic is recorded, and no Fruit branch or commit is reported. The default branch remains unchanged.

### Botanist Fruit review inventory

Every inventoried Fruit begins with exact durable provenance on its execution
record: producer kind and identity, Phytomer where present, Substrate,
repository, branch, commit, publication state, and production time. The Stem
joins those claims with exact current Git and forge evidence in Core. Core owns
the deterministic review states `outstanding`, `unknown`, `closed-unmerged`,
and `merged`, their safe unknown reasons, stable item ordering, and review
pressure counts.

The Botanist can observe this model with `tendril fruit list` or
`tendril fruit list --json`. The private local Stem mux exposes the same model
at `GET /v1/fruit` through the Botanist authentication lane. These surfaces
render or serialize Core values only; they do not enumerate branches, inspect
forge state independently, or recalculate classification and counts. Review
pressure is observational and does not accept, merge, delete, block, or mutate
Fruit. Persistence or evidence that is unavailable is reported as unavailable,
not as an empty inventory.

## Observation/Persistence

Runtime state is persisted by default when history logging is enabled. **Phytomers** (sessions) are recorded in `.tendril/history.db`. SQLite persistence is enabled by default (controlled via `TENDRIL_DB_LOGGING`). Heartwood provides application-level encryption for persisted payload values; encrypted writes are default when the cipher resolves and can be disabled with `TENDRIL_ENCRYPT_AT_REST`.

The system publishes lifecycle and preparation events (e.g., `sprout-emerged`, `sprout-withered`, `task-context-assembled`) over an EventBus. The task-context event carries only safe bounded selection provenance; raw selected evidence and private reasoning are excluded. HistoryDB persists the sanitized event through the existing EventBus sink. Task context remains runtime preparation state, and Fruit plus protected/default-branch semantics are unchanged.

Every `seed.grow` establishes exactly one canonical Phytomer for that Seed growth. Canonical `seed.grow` accepts `detached: true` and returns active `{ handle, phytomerId, status: "running" }`. Core owns handle mint, durable opening, background growth, same-process accounting-failure quarantine, and the safe accounting-failure report. REST `POST /v1/seeds/grow/async` is compatibility presentation of that same Core lifecycle. The Seed handle is the durable Fruit-collection identity; the Phytomer ID is the lifecycle/observation identity. Sprouts grown while satisfying that Seed are attributed to that Phytomer. Ownership (Pollen, Substrate, Phytomer) is recorded at dispatch so `sprout.watch` can authorize observation before the first Sprout exists.

A long-lived Stem that can accept detached Seeds reconciles orphaned running/settling Seed and unresolved continuation state from a previous process before it accepts Pollinator traffic. Persistence disabled leaves continuation unavailable and is not a memory-only fallback.

`GET /v1/phytomers/{sessionId}/watch` is the headless Server-Sent Events view of that current state. MCP `sproutWatch({ sessionId })` is the headless snapshot of the same Core projection; a Pollinator may call it again to refresh. Both are authorized by `sprout.watch` using the same ownership rule as the Phytomer's events and live stream. After authenticating, REST emits the current safe observation immediately, then follows durable state changes until the associated Seed reaches `satisfied`, `exhausted`, `withered`, or `fruit-publication-failed`, then closes. Connecting after a terminal Seed returns that terminal current state and closes. The Stem Core owns the safe current-state projection. REST authenticates, authorizes, and frames Server-Sent Events. MCP authenticates, authorizes, and returns one snapshot. The projection fails closed if any Sprout's or continuation's Pollen or Substrate disagrees with the Seed; it does not release a mixed-ownership current state. The projection reports Pollen, Substrate, Seed handle, Phytomer ID, Seed status, iteration progress, actual Sprout lifecycle, provider/model, `providerRequestAttempted`, `toolInvocations`, structured `failureCategory`, a safe `providerDiagnostic`, a safe Seed Fruit-publication diagnostic when present, bounded Seed verification diagnostics when present, safe continuation summaries (`continuationId`, `sequence`, `deliveryState`) when they exist, and real Fruit branch and commit when those facts exist. It does not invent Fruit, expose raw Seed error text, raw continued intent, intent digest, idempotency key, raw model reasoning, or credentials, accept Fruit, or grant execution. Seed collection remains `seed.grow`. `seed.grow` does not imply `sprout.watch`, `phytomer.continue` does not imply `sprout.watch`, and `sprout.watch` does not imply either execution grant.

For reviewable successful Fruit with a diff, an Epigenetic Chronicler consumes the Sprout transcript, diff, and session logs to distill durable learnings, appending them to epigenetic genome material.

Hardiness reports deployment/Terroir posture—whether conditions permit the delegation boundary to be enforced rather than merely recorded. It evaluates principal separation, credential exclusivity, executable protection, and isolation tier. It does not decide Fruit acceptance.

## Trust boundaries

- **Stem (host)**: Trusted capability authority. Holds the backend credentials and control-plane authority required for governed operations; normal sealed Sprouts do not receive those credentials.
- **Greenhouse**: Delegated Botanist/operator surface. The optional observation UI deployed as an nginx container holds no backend credential itself; the browser presents the Botanist bearer key and the proxy forwards it. It is not the public Pollinator ingress. The Stem remains the capability authority.
- **Terrarium / Sprout**: Zero authority by default. Normal sealed Sprouts receive only explicit execution environment. Git/network operations requiring Stem authority are normally mediated by the Stem. A Substrate may explicitly inject its resolved GitHub credential into the Sprout by configuring `exposeToken: true`; anything supplied can leave through Git-reviewable Fruit. The Host Terrarium provider is a separate, explicit escape that bypasses isolation.
