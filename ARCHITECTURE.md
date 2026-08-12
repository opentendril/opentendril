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
- **Pollen**: The authenticated principal of the Pollinator.
- **Stem**: The host daemon containing the core authority, orchestrating the system, and retaining secrets.
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

No business logic resides in the adapters. The adapters dispatch to the **Stem Core** (`cmd/stem/internal/core`), which holds the canonical governed capability registry. Interface parity is mechanically enforced across all adapters via tests. The Core executes the capability and enforces policy. Views and control-plane operations are distinct and not Pollinator-facing governed command capabilities.

## Cognitive path

The **Mycorrhizae** (LLM) is the cognitive engine, running entirely externally. The Stem interacts with the Mycorrhizae via the `roots/llm` package, which acts as the provider connectivity and model routing layer. 

The Stem is a deterministic routing and lifecycle kernel; it is not a reasoning component and does not plan or reason cognitively. The Mycorrhizae receives context via Plasmids (markdown trait injections) and repository maps (structural maps of the Substrate), and returns tool calls or text.

## Execution/Containment

Execution happens inside a **Terrarium** (by default, a network-isolated Docker or gVisor container) or via the explicitly enabled Host provider. The **Sprout** is the stateless worker executing inside this boundary.

The Stem provides context and tools to the Sprout over stdin/stdout (`ToolCall` / `ToolResponse`). By default, containerized Terraria fail closed: they cannot reach the host network (`--network none`) or the host workspace. Egress is mediated strictly by the Stem via an allow-listed fetch mechanism.

The Host Terrarium provider is a separate, explicit isolation escape. It requires `TENDRIL_ALLOW_HOST_EXECUTION=true`, runs with full host-user permissions, bypasses Terrarium isolation entirely, and emits a loud audit warning upon activation.

For containerized runs, the host workspace is shadowed to isolate the Substrate (`sprout` execution), protecting the active workspace from untrusted modifications. This shadow-worktree establishment fails closed by default. Executing directly in the active host workspace requires a separate explicit opt-in via `TENDRIL_ALLOW_HOST_WORKSPACE=true`.

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

## Observation/Persistence

The runtime state is persistent. **Phytomers** (sessions) are recorded in `.tendril/history.db`. SQLite persistence is enabled by default (controlled via `TENDRIL_DB_LOGGING`). Heartwood provides application-level encryption for persisted payload values; encrypted writes are default when the cipher resolves and can be disabled with `TENDRIL_ENCRYPT_AT_REST`.

The system publishes lifecycle events (e.g., `sprout-emerged`, `sprout-withered`) over an EventBus.
For reviewable successful Fruit with a diff, an Epigenetic Chronicler consumes the Sprout transcript, diff, and session logs to distill durable learnings, appending them to epigenetic genome material.

Hardiness reports deployment/Terroir posture—whether conditions permit the delegation boundary to be enforced rather than merely recorded. It evaluates principal separation, credential exclusivity, executable protection, and isolation tier. It does not decide Fruit acceptance.

## Trust boundaries

- **Stem (host)**: Full authority. Holds all secrets (API keys, mesh keys, Botanist key, GitHub tokens).
- **Greenhouse**: Delegated authority. The optional observation UI deployed as an nginx container holds no backend credential itself; the browser presents the Botanist bearer key and the proxy forwards it. The Stem remains the capability authority.
- **Terrarium / Sprout**: Zero authority by default. Normal sealed Sprouts do not receive Stem credentials. Git/network operations requiring Stem authority are normally mediated by the Stem. A Substrate may explicitly inject its resolved GitHub credential into the Sprout by configuring `exposeToken: true`. The Host Terrarium provider is a separate, explicit escape that bypasses isolation.
