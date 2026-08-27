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

No business logic resides in the adapters. The adapters dispatch to the **Stem Core** (`cmd/stem/internal/core`), which holds the canonical governed capability registry. MCP transport identifiers are adapter projections of those canonical Core names. Interface parity is mechanically enforced across all adapters via tests. The Core executes the capability and enforces policy. Views and control-plane operations are distinct and not Pollinator-facing governed command capabilities.

## Cognitive path

The **Mycorrhizae** (LLM) is the cognitive engine, running entirely externally. The Stem interacts with the Mycorrhizae via the `roots/llm` package, which acts as the provider connectivity and model routing layer. 

The Stem is a deterministic routing and lifecycle kernel; it is not a reasoning component and does not plan or reason cognitively. The Mycorrhizae receives context via Plasmids (injected context or capability payloads) and repository maps (structural maps of the Substrate), and returns tool calls or text.

## Execution/Containment

Normal **Sprout** execution is bounded by a **Terrarium**. Implemented isolated providers include Docker, gVisor, and Firecracker. Docker and gVisor use container isolation, while Firecracker uses a microVM. The Host provider is an explicit escape that bypasses Terrarium isolation and requires `TENDRIL_ALLOW_HOST_EXECUTION=true`.

The Stem communicates with execution through the Terrarium provider/Stoma boundary; the concrete transport is provider-specific (e.g., stdin/stdout for some containers, vsock JSON for Firecracker). By default, isolated Terraria fail closed: they cannot reach the host network or the host workspace. Egress is mediated strictly by the Stem via an allow-listed fetch mechanism.

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

Protected kernel paths enforce a floor of safety, refusing automated merges that modify the Stem's own kernel or governance files. The Botanist always retains the final Gate C merge decision.

**Managed App+API Substrates.** When a managed Substrate is configured with `commit: api`, the Stem creates the commit server-side via the GitHub GraphQL/REST API using the GitHub App credential. This eliminates the need for a local signing key; GitHub supplies the commit identity and the `Verified` badge. The `commit: api` mode is only valid with GitHub App authentication (`auth.method: app`). Any other auth method is refused at setup-verify time and fails before a Seed grows.

`tendril git setup --verify` for managed App/API Substrates additionally confirms that the GitHub App installation holds repository **contents write** permission, which is required to create the Fruit ref and commit. This check is strictly read-only (no branch, commit, push, or pull request is created). A missing or read-only contents permission fails immediately with an actionable message that names the required permission and the installation settings path. The write-permission check is only performed for managed Substrates with `commit: api`; path and ephemeral checkouts are unaffected.

## Observation/Persistence

Runtime state is persisted by default when history logging is enabled. **Phytomers** (sessions) are recorded in `.tendril/history.db`. SQLite persistence is enabled by default (controlled via `TENDRIL_DB_LOGGING`). Heartwood provides application-level encryption for persisted payload values; encrypted writes are default when the cipher resolves and can be disabled with `TENDRIL_ENCRYPT_AT_REST`.

The system publishes lifecycle events (e.g., `sprout-emerged`, `sprout-withered`) over an EventBus.

Every `seed.grow` establishes exactly one canonical Phytomer for that Seed growth. The Seed handle is the durable Fruit-collection identity; the Phytomer ID is the lifecycle/observation identity. Sprouts grown while satisfying that Seed are attributed to that Phytomer. Ownership (Pollen, Substrate, Phytomer) is recorded at dispatch so `sprout.watch` can authorize observation before the first Sprout exists.

`GET /v1/phytomers/{phytomerId}/watch` is the headless Server-Sent Events view of that current state. It is authorized by `sprout.watch` using the same ownership rule as the Phytomer's events and live stream. After authenticating, it emits the current safe observation immediately, then follows durable state changes until the associated Seed reaches `satisfied`, `exhausted`, or `withered`, then closes. Connecting after a terminal Seed returns that terminal current state and closes. The Stem Core owns the safe current-state projection. REST authenticates, authorizes, and frames Server-Sent Events. The projection fails closed if any Sprout's Pollen or Substrate disagrees with the Seed; it does not release a mixed-ownership current state. The projection reports Pollen, Substrate, Seed handle, Phytomer ID, Seed status, iteration progress, actual Sprout lifecycle, provider/model, `providerRequestAttempted`, `toolInvocations`, structured `failureCategory`, a safe `providerDiagnostic`, and real Fruit branch and commit when those facts exist. It does not invent Fruit, expose raw Seed error text, raw model reasoning, or credentials, accept Fruit, or grant execution. Seed collection remains `seed.grow`. `seed.grow` does not imply `sprout.watch`, and `sprout.watch` does not imply `seed.grow`.

For reviewable successful Fruit with a diff, an Epigenetic Chronicler consumes the Sprout transcript, diff, and session logs to distill durable learnings, appending them to epigenetic genome material.

Hardiness reports deployment/Terroir posture—whether conditions permit the delegation boundary to be enforced rather than merely recorded. It evaluates principal separation, credential exclusivity, executable protection, and isolation tier. It does not decide Fruit acceptance.

## Trust boundaries

- **Stem (host)**: Trusted capability authority. Holds the backend credentials and control-plane authority required for governed operations; normal sealed Sprouts do not receive those credentials.
- **Greenhouse**: Delegated authority. The optional observation UI deployed as an nginx container holds no backend credential itself; the browser presents the Botanist bearer key and the proxy forwards it. The Stem remains the capability authority.
- **Terrarium / Sprout**: Zero authority by default. Normal sealed Sprouts do not receive Stem credentials. Git/network operations requiring Stem authority are normally mediated by the Stem. A Substrate may explicitly inject its resolved GitHub credential into the Sprout by configuring `exposeToken: true`. The Host Terrarium provider is a separate, explicit escape that bypasses isolation.
