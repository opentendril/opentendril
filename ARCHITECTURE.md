# OpenTendril Architecture

This document is the authoritative description of the current implemented OpenTendril architecture. Repository documentation describes current state only.

## Purpose/Authority

OpenTendril is a governed framework for executing AI-driven code modifications. It delegates authority from a human (**Botanist**) to an external entity (**Pollinator**) via a strictly verified principal (**Pollen**).

The core invariant is that governed capabilities are executed only by the **Stem** (the host control plane), mediated by its Core authority. 

## Anatomy

OpenTendril is built on a strict synthetic biological architecture:
- **Botanist**: The human owner holding absolute authority.
- **Pollinator**: An external client or AI builder initiating requests.
- **Pollen**: The authenticated principal of the Pollinator.
- **Stem**: The host daemon containing the core authority, orchestrating the system, and retaining secrets.
- **Mycorrhizae**: The external cognitive LLM side.
- **Roots**: The external network connectivity (e.g., to LLM providers or Git remotes).
- **Terrarium**: The ephemeral isolation boundary where execution takes place.
- **Sprout**: The ephemeral executing code body inside the Terrarium.
- **Substrate**: The target source repository.
- **Fruit**: The git-reviewable output of a Sprout.
- **Phytomer**: A durable, stateful session orchestrating related Sprouts.
- **Plasmid**: Injectable context and behavioral traits.
- **Sequence**: A directed acyclic graph (DAG) of execution steps.
- **Greenhouse**: The network-facing UI and reverse proxy.

## Governed path

All external requests enter through a transport adapter (CLI, REST, or Model Context Protocol). These adapters strictly translate the transport protocol into internal Go structs.

No business logic resides in the adapters. The adapters dispatch to the **Stem Core** (`cmd/stem/internal/core`), which holds the canonical governed capability registry. Interface parity is mechanically enforced across all adapters via tests. The Core executes the capability and enforces policy. Views and control-plane operations are distinct and not Pollinator-facing governed command capabilities.

## Cognitive path

The **Mycorrhizae** (LLM) is the cognitive engine, running entirely externally. The Stem interacts with the Mycorrhizae via the `roots/llm` package, which acts as the provider connectivity and model routing layer. 

The Stem is a deterministic routing and lifecycle kernel; it is not a reasoning component and does not plan or reason cognitively. The Mycorrhizae receives context via Plasmids (markdown trait injections) and repository maps (structural maps of the Substrate), and returns tool calls or text.

## Execution/Containment

Execution happens strictly inside a **Terrarium**—a network-isolated Docker, gVisor, or Firecracker container. The **Sprout** is the stateless worker executing inside this boundary. 

The Stem provides context and tools to the Sprout over stdin/stdout (`ToolCall` / `ToolResponse`). By default, Terraria fail closed: they cannot reach the host network (`--network none`) or the host workspace. Egress is mediated strictly by the Stem via an allow-listed fetch mechanism. 

The host workspace is shadowed to isolate the Substrate (`sprout` execution), protecting the active workspace from untrusted modifications. Executing directly in the host workspace is fail-closed and must be explicitly opted into via `TENDRIL_ALLOW_HOST_WORKSPACE=true` for deliberate in-place runs, which will bypass isolation and trigger loud audit telemetry.

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

The Stem commits changes on an isolated staging branch (`sprout/task-*`). Work is never merged directly to the default branch (e.g., `main`). A dirty host workspace fails closed, blocking the pipeline instead of silently stashing and restoring state. 

Protected kernel paths enforce a floor of safety, refusing automated merges that modify the Stem's own kernel or governance files. The Botanist reviews the drift and performs the final Gate C merge decision manually.

## Observation/Persistence

The runtime state is persistent. **Phytomers** (sessions) are recorded in an application-level encrypted SQLite database (`history.db`). 

The system publishes lifecycle events (e.g., `sprout-emerged`, `sprout-withered`) over an EventBus.
An Epigenetic Chronicler distills successful runs into Markdown-based rules to adapt the Sprout's behavior for future tasks based on commit history.

Hardiness is an observation command (`tendril hardiness`), not an acceptance gate. It reports the strength of the security boundaries on the host, such as whether a declared Pollen is legitimately bound by the operating system user permissions.

## Trust boundaries

- **Stem (host)**: Full authority. Holds all secrets (API keys, mesh keys, Botanist key, GitHub tokens).
- **Greenhouse**: Delegated authority. An opt-in, containerized reverse proxy holding no credentials of its own, relying entirely on a locally-stored Botanist key in the browser.
- **Terrarium / Sprout**: Zero authority. No GitHub token is injected by default. Operations requiring authorization (like pushing code) are executed on the host by the Stem. A Substrate must explicitly expose credentials with `exposeToken: true` if the Sprout requires access to them.
