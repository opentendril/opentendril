# Component: Receptors — the REST + MCP adapter layer that receives external requests and projects Core capabilities onto both surfaces

## Purpose

`cmd/stem/internal/receptors` is the sensory adapter layer of Stem. It receives external requests across REST HTTP and MCP (stdio/JSON-RPC) surfaces and projects transport-free Core capabilities onto both surfaces, driving Conductor for execution. As an aggregator with a fan-out of 5 internal packages, `receptors` holds zero core business or execution logic of its own; instead, it realizes REST/MCP capability parity, translates incoming wire payloads into typed Core inputs, stamps transport-level context (such as origin and stdio session IDs), and enforces per-invocation delegation authorization for Pollinator requests.

## Responsibilities

**Does:**

- **Config & Security Gate (`cmd/stem/internal/receptors/config.go`):** Exposes `ConfigHandler` endpoints for managing hormonal triggers and AI genotypes (`/v1/config/triggers`, `/v1/config/genotypes`), and provides `DelegationGate` and `AccessTokenVerifier` to resolve, prove, and audit Pollen identities (`DelegatedPollen`, `PollenFor`, `Authorize`, `Middleware`), publishing decision outcomes to `eventbus.Bus`.
- **Session Lifecycle Adapter (`cmd/stem/internal/receptors/sessions.go`):** Implements `SessionsHandler` over `core.Core`, mapping canonical `/v1/phytomers` REST endpoints (create, list, get, update, delete, history) and legacy `/v1/sessions` aliases to Core session capabilities, while supporting ungoverned event/run views and async sequence triggers (`runSequenceAsync`).
- **Git Operations Adapter (`cmd/stem/internal/receptors/git.go`):** Implements `GitHandler`, projecting governed git capabilities (`POST /v1/git/commit`, `push`, `pr`, `branch`, `status`, `branches`, `prune`) onto Core git operations with per-invocation delegation checks against operation-classes.
- **Sprout Execution Adapter (`cmd/stem/internal/receptors/sprout.go`):** Implements `SproutHandler`, mapping synchronous `POST /v1/sprouts/grow` and detached 202 Accepted `POST /v1/phytomers/{sessionId}/sprout/grow` (and legacy `/v1/sessions` alias) to Core sprout runs with background goroutine tracking and history logging.
- **Seed Intent Growth Adapter (`cmd/stem/internal/receptors/seed.go`):** Implements `SeedHandler`, mapping synchronous `POST /v1/seeds/grow`, background `POST /v1/seeds/grow/async`, and subject-scoped `GET /v1/seeds/runs/{handle}` collection to Core seed growth with grant-material egress injection.
- **Terrarium Command Adapter (`cmd/stem/internal/receptors/stoma.go`):** Implements `StomaHandler`, mapping `POST /v1/stoma/pass` to Core command execution in network-sealed terraria, placing authorized delegation grant egress allow-lists onto runs.
- **Sequence Orchestration Adapter (`cmd/stem/internal/receptors/sequence.go`):** Implements `SequenceHandler`, mapping `GET /v1/sequences` listing and `POST /v1/sequences/grow` execution to Core sequence orchestration.
- **Genome Management Adapter (`cmd/stem/internal/receptors/genome.go`):** Implements `GenomeHandler`, mapping `GET /v1/genome`, `POST /v1/genome/reduce`, and `POST /v1/genome/evolve` to Core genome operations.
- **Plasmid Rule Adapter (`cmd/stem/internal/receptors/plasmid.go`):** Implements `PlasmidHandler`, mapping `GET /v1/plasmids` and `POST /v1/plasmids/inject` to Core plasmid injection.
- **Mesh Grafting & Trait Adapters (`cmd/stem/internal/receptors/mesh.go`, `cmd/stem/internal/receptors/graft.go`):** Implements `GraftHandler` (`POST /v1/mesh/grafts`, `POST /v1/mesh/promotions`) and `TraitHandler` (`GET /v1/mesh/traits`, `POST /v1/mesh/traits/{id}/accept`, `POST /v1/mesh/traits/{id}/reject`) over Core mesh operations.
- **Pollinator Token Minting Adapter (`cmd/stem/internal/receptors/pollinatortoken.go`):** Implements `PollinatorTokenHandler` (`POST /v1/pollinator/token`), authenticating durable Pollinator refresh roots to mint short-lived Stem-signed access tokens.
- **MCP Protocol Surface (`cmd/stem/internal/receptors/mcp.go`):** Implements `MCPHandler`, serving stdio/JSON-RPC protocol initializations (`initialize`, `resources/list`, `resources/read`, `tools/list`, `tools/call`), dynamically projecting Core capabilities and deprecated legacy tool aliases (`sproutTendril`, `runSequence`, `viewGenome`, `reduceGenome`, `injectPlasmid`, `graftSubstrate`, `promotePR`), maintaining local genotype index files (`index.yaml`), and enforcing bind-time Pollen delegation authorization (`authorizeDelegatedTool`).

**Does not:**

- Declare governed capabilities (Core package `cmd/stem/internal/core` owns capability declaration and definition schemas).
- Implement orchestration, git, or terrarium execution logic (Conductor package `cmd/stem/internal/conductor` and Terrarium package `cmd/stem/internal/terrarium` implement execution).
- Own transport lifecycle or daemon listener binding (entrypoint files `cmd/stem/cmdserve.go` and `cmd/stem/cmdmcp.go` own HTTP server and stdio stream lifecycles).
- Persist sessions or execution history directly (delegates persistence to `cmd/stem/internal/session` and `cmd/stem/internal/historydb`).

## Public interface

| Symbol | Role |
| --- | --- |
| `PollenHeader` | Constant string `X-OpenTendril-Pollen` marking delegated HTTP requests. |
| `PollinatorCredentials` | Slice of `core.PollinatorCredential` issued refresh root credentials. |
| `AccessTokenVerifier` | Interface for verifying Stem-signed short-lived access tokens (`VerifyAccessToken`). |
| `DelegatedPollen` | Resolves presented Pollen identity and returns whether it was proven by signature/credential. |
| `DelegationGate` / `(*DelegationGate).PollenFor` | Evaluates credentials/tokens and authorizer decision for a request (`Authorize`, `Middleware`). |
| `ConfigHandler` / `NewConfigHandler` | REST adapter for trigger/genotype management (`ListTriggers`, `ListGenotypes`, `UploadGenotype`). |
| `Trigger` | JSON DTO for trigger file metadata (`Name`, `Size`). |
| `SessionsHandler` / `NewSessionsHandler` | REST adapter for session lifecycle (`Register`, `Capabilities`). |
| `GitHandler` / `NewGitHandler` | REST adapter for governed git operations (`Register`, `Capabilities`, `WithDelegation`). |
| `SproutHandler` / `NewSproutHandler` | REST adapter for sprout execution (`Register`, `Capabilities`, `WithDelegation`). |
| `SeedHandler` / `NewSeedHandler` | REST adapter for seed growth and fruit collection (`Register`, `Capabilities`, `WithDelegation`, `WithHistory`). |
| `StomaHandler` / `NewStomaHandler` | REST adapter for terrarium command execution (`Register`, `Capabilities`, `WithDelegation`). |
| `SequenceHandler` / `NewSequenceHandler` | REST adapter for sequence orchestration (`Register`, `Capabilities`). |
| `GenomeHandler` / `NewGenomeHandler` | REST adapter for genome view/reduce/evolve (`Register`, `Capabilities`). |
| `PlasmidHandler` / `NewPlasmidHandler` | REST adapter for plasmid list/inject (`Register`, `Capabilities`). |
| `GraftHandler` / `NewGraftHandler` | REST adapter for client-side mesh graft and promotion (`Register`, `Capabilities`). |
| `TraitHandler` / `NewTraitHandler` | REST adapter for mesh trait list/accept/reject (`Register`, `Capabilities`). |
| `PollinatorTokenHandler` / `NewPollinatorTokenHandler` | Self-authenticating REST handler for access-token minting (`HandleMint`, `Register`). |
| `MCPHandler` / `NewMCPHandler` | Stdio/JSON-RPC MCP surface adapter (`HandleMCP`, `ProcessMCPMessage`, `CoreCapabilityNames`, `SetupRoutes`). |
| `(*MCPHandler).WithSessions` / `WithDefaultSession` / `WithCore` / `WithDelegation` | Builder methods binding session state, default stdio session, Core service, and delegation gate. |

The package exports approximately 108 symbols across exported types, constructors, primary methods, struct fields, and constants.

## Dependencies

**Fan-out:** 5 internal packages:

- **`cmd/stem/internal/core`** — Projects transport-free capability definitions, input/output types, and signer interfaces.
- **`cmd/stem/internal/eventbus`** — Publishes delegation audit events (`EventDelegationAuthorized`, `EventDelegationDenied`) and sequence execution lifecycle events.
- **`cmd/stem/internal/session`** — Manages session lifecycle state, origin metadata constants (`OriginREST`, `OriginMCP`), and preferences.
- **`cmd/stem/internal/historydb`** — Persists sprout runs, seed runs, and session event records.
- **`cmd/stem/internal/conductor`** — Loads substrate configuration (`SubstratesConfig`), sequence execution options, and definition search paths.

*Boundary Note:* `receptors` is the sensory adapter tier and legitimately imports BOTH `cmd/stem/internal/core` and `cmd/stem/internal/conductor` to project capabilities onto transport surfaces. Inwardly, `cmd/stem/internal/core` must **never** import `receptors` (verified by `cmd/stem/internal/core/boundary_test.go`).

**Fan-in:**

- **`cmd/stem`** — `cmd/stem/cmdserve.go` mounts REST routes and MCP endpoints; `cmd/stem/cmdmcp.go` initializes the stdio MCP handler; `cmd/stem/clidelegation.go` binds CLI delegation gates; `cmd/stem/parity_test.go` verifies REST/MCP/CLI capability parity.
- **`cmd/stem/internal/conductor`** — `cmd/stem/internal/conductor/changescope_test.go` and `cmd/stem/internal/conductor/scopedci_test.go` track `receptors` in scope change and CI dependency trees.

## Limitations

- **Monolithic `mcp.go` size and breadth (`cmd/stem/internal/receptors/mcp.go`):** At 1,279 lines, `cmd/stem/internal/receptors/mcp.go` combines JSON-RPC protocol parsing, tool registration, deprecated tool alias adapters, typed parameter conversions (`callStomaPass`, `callSeedGrow`), and disk-based genotype YAML index generation (`syncGenotypeIndex`, `collectGenotypeIndex`, `writeGenotypeIndex`) into a single file.
- **Surface Asymmetries and Unadvertised Capabilities:**
  - `createGenotype` is exposed as an MCP tool in `cmd/stem/internal/receptors/mcp.go` and as a REST route in `cmd/stem/internal/receptors/config.go`, but is absent from `core.CapabilityNames()`, creating an asymmetry where genotype creation bypasses the governed Core parity registry.
  - Asynchronous sprout (`POST /v1/phytomers/{sessionId}/sprout/grow`) and seed growth collection (`GET /v1/seeds/runs/{handle}`) exist on REST handlers (`SproutHandler`, `SeedHandler`) but have no corresponding MCP tool equivalents.
- **Delegation Gate Posture on Tokens:** `AccessTokenVerifier` and `DelegatedPollen` in `cmd/stem/internal/receptors/config.go` fail closed for unverifiable or missing tokens, but a plain bearer request presenting no Pollen marker header bypasses the delegation gate for ungoverned REST routes.

## Design & rationale

The design of `cmd/stem/internal/receptors` is guided by the core architecture principle **Core-declares-once, receptors-project-to-both-surfaces**. Core capabilities are declared transport-free in `cmd/stem/internal/core`. The `receptors` package acts as the dual-surface projection layer, translating HTTP requests and JSON-RPC messages into typed Core inputs and ensuring complete REST/MCP interface and behavioral parity (enforced by `cmd/stem/parity_test.go`).

The transport surfaces differ in security posture by design:
- **MCP Surface (`cmd/stem/internal/receptors/mcp.go`):** Operates over stdio as a personal, single-tenant process. The Pollen identity is bound at connection initialization time (`WithDelegation(gate, pollen)`). Delegated tools are authorized against this fixed bind-time Pollen without requiring per-invocation credential headers in tool parameters, preventing callers from self-asserting arbitrary identities.
- **REST Surface (`cmd/stem/internal/receptors/config.go`, etc.):** Operates over HTTP as a multi-tenant network daemon. Requests carry short-lived Stem-signed access tokens or refresh root credentials in the `Authorization` header, or a `X-OpenTendril-Pollen` claim header. `DelegationGate` verifies access tokens via `AccessTokenVerifier` and authorizes every delegated operation per-invocation against active delegation grants (`core.DelegationAuthorizer`).

The short-lived token delegation model fits the delivered authentication flow: Pollinators present a durable refresh root credential once to `POST /v1/pollinator/token` (`PollinatorTokenHandler`), receiving a short-lived access token signed by `core.StemSigner`. This access token is presented on subsequent REST calls and verified by `DelegationGate` without exposing durable root secrets across general API routes.
