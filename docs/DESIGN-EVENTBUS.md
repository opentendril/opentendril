# Component: EventBus — in-process pub/sub spine for organism signalling (health, sequence, sprout lifecycle, stream/thought, tools, hormonal/rhizome placeholders, and sink fan-out to telemetry, history, and the gateway).

## Purpose

`cmd/stem/internal/eventbus` is the pure-leaf package that carries the plant's **signalling**. Producers publish typed `Event` values onto a process-local `Bus`; typed `Subscribe` handlers and type-agnostic `Sink` pumps deliver them to live observers (`/ws`), durable history, Resin logs, and remote telemetry transporters. It owns only the contract (`EventType` constants + `AllEventTypes`), the in-memory bus, and delivery mechanics — not persistence, not WebSocket framing, and not what any producer puts in `Data`.

## Responsibilities

**Does:**

- Define the cross-package **event-name contract**: kebab-case `EventType` string constants and `AllEventTypes()` for broad subscriptions (`eventbus.go`).
- Construct a process-local `Bus` (`New`) that stores typed handler lists, a rolling in-memory history window (`maxHistory` = 100), and a list of sink pumps.
- Deliver events to **type-matched** handlers via `Subscribe` + `Publish` (handlers run synchronously on the publisher's goroutine).
- Deliver **every** event to attached `Sink` implementations asynchronously on a dedicated buffered channel per sink (`AttachSink`, default buffer 1024), with drop-on-full so a slow sink cannot block `Publish`.
- Stamp zero timestamps with UTC now on publish; retain the last `N` events via `History`.
- Drain sink pumps and refuse new sink attachment after `Shutdown` (idempotent; publish after shutdown still updates history and runs handlers, but does not send to sinks).

**Does not:**

- Persist events (history.db is a sink owned by `historydb`; Resin is owned by `telemetry`).
- Own WebSocket, Prometheus, Kafka, Redis, or webhook transport (gateway + telemetry attach as consumers).
- Unsubscribe handlers, rate-limit, or provide at-least-once / durable delivery.
- Validate event `Data` schemas or enforce which package may publish which type.
- Cross process or network boundaries by itself — it is strictly in-process.

## Public interface

| Symbol | Role |
| --- | --- |
| `EventType` | String enum for event names. **Wire values are kebab-case** (domain enums per GUARDRAILS / AGENTS.md), e.g. `sprout-emerged`. |
| **Health** | `EventHealthCheck` (`health-check`), `EventHealthDegraded` (`health-degraded`), `EventHealthRecovered` (`health-recovered`). |
| **Terrarium / infra** | `EventTerrariumOOM` (`terrarium-oom`), `EventTerrariumTimeout` (`terrarium-timeout`). |
| **Sequence** | `EventSequenceFailure` (`sequence-failure`), `EventSequenceComplete` (`sequence-complete`). |
| **Stream / thought** | `EventStreamToken` (`stream-token`), `EventThoughtBranch` (`thought-branch`). |
| **Tool / transcript** | `EventToolInvoked` (`tool-invoked`), `EventSproutTranscript` (`sprout-transcript`; historical persisted name `Pollinator-transcript` may still exist in old history rows). |
| **Sprout lifecycle** | `EventSproutEmerged` (`sprout-emerged`), `EventSproutMatured` (`sprout-matured`), `EventSproutWithered` (`sprout-withered`). |
| **Parallel / GA / mesh-ish** | `EventParallelSprouting` (`parallel-sprouting`), `EventMycelialMerge` (`mycelial-merge`), `EventPhenotypicSelection` (`phenotypic-selection`). |
| **Delegation audit** | `EventDelegationAuthorized` (`delegation-authorized`), `EventDelegationDenied` (`delegation-denied`). |
| `AllEventTypes` | Returns every registered type in declaration order — used by gateway `/ws`, Prometheus pre-registration, and broad telemetry subscriptions. **Must be kept in lockstep** with the const block. |
| `Event` | Payload: `Type`, `Timestamp`, `Source`, optional `SessionID` (`json:"sessionId"`), optional `Data` map. |
| `Handler` | `func(Event)` — typed subscription callback. |
| `Sink` | `Consume(Event)` — type-agnostic consumer; intended for persistence and remote transporters. |
| `Bus` | In-process bus: handlers map, history slice, sink pumps, closed flag. |
| `New` | Construct an empty bus. |
| `(*Bus).Subscribe` | Append a handler for one `EventType` (no remove/unsubscribe API). Nil bus/handler is a no-op. |
| `(*Bus).Publish` | Append to history (cap 100), invoke matching handlers synchronously, non-blocking fan-out to sink pumps. |
| `(*Bus).AttachSink` | Register a sink pump (default buffer 1024; `buffer <= 0` → default). Drop-on-full per sink. No-op if bus closed or args nil. |
| `(*Bus).Shutdown` | Close all sink channels, wait for pumps to drain, mark closed. Idempotent. |
| `(*Bus).History` | Copy of the last `n` retained events (capped by the rolling window). |

Package-level sentinel errors: **none**.

## Dependencies

**Fan-out:** none. Stdlib only (`sync`, `time`). Pure leaf; no other OpenTendril packages.

**Fan-in:**

- **`cmd/stem`** — constructs the process bus for `serve`, MCP, health CLI, sprout/sequence ops, and chat-completions streaming; attaches `historydb` as a sink and remote transporters; publishes `stream-token` from the chat path; shuts the bus down on process exit paths (`cmdserve.go`, `cmdmcp.go`, `cmdsprout.go`, `clidelegation.go`, …).
- **`internal/conductor`** — primary producer: sequence complete/failure + terrarium OOM/timeout; sprout emerged/matured/withered; stream-token, thought-branch, tool-invoked, sprout-transcript; parallel-sprouting / mycelial-merge; phenotypic-selection (`sequence.go`, `sprout.go`, `outcome.go`, `parallelsprouting.go`, `selection.go`).
- **`internal/gateway`** — consumer only: `Subscribe`s to every `AllEventTypes()` entry per WebSocket client and optionally replays `History` via `?replay=N` (`gateway.go`).
- **`internal/healthmon`** — publishes `health-check` every interval and `health-degraded` when overall health is false (`monitor.go`). Does not publish `health-recovered`.
- **`internal/historydb`** — sink: `Store.Consume` persists every event into SQLite (`historydb.go`).
- **`internal/receptors`** — publishes sequence complete/failure from session sequence runs; `DelegationGate.audit` publishes `delegation-authorized` / `delegation-denied` (`sessions.go`, `config.go`). Threads the ambient bus into sprout handlers for lifecycle visibility.
- **`internal/telemetry`** — attaches transporters as sinks (`AttachTransporter`); Resin log sink attaches via `AttachSink` (`resin.go`, `transporter.go`); Prometheus counters pre-register from `AllEventTypes()`.

## Limitations

- **Handlers are synchronous on the publish path.** A slow `Subscribe` handler (e.g. a gateway handler that does work beyond a non-blocking channel send) **blocks the publisher** until it returns. Sinks are the non-blocking path; all durable/slow consumers use them.
- **Sinks are lossy by design.** When a sink buffer is full, `Publish` drops that event for that sink only (`select` / `default`). Default buffer is 1024. Sink buffer overflows emit an edge-triggered log (once when dropping starts, once when the sink catches up), and a running drop counter is accessible via `SinkDroppedCount`.
- **Subscribers must manage their lifecycle.** `Subscribe` returns an unsubscribe closure. Broad consumers like the gateway use these closures to deregister their handlers on client disconnect. Sinks, however, cannot be detached once attached; they run until `Shutdown`.
- **In-memory history only (last 100).** `History` is a process-local ring for `?replay` and tests. Durable retention is entirely sink-side (history.db / Resin / remote). Events published with no attached sink and no live subscriber are gone after the window slides.
- **No ordering guarantees across concurrent publishers** beyond mutex-protected history append and sequential handler invocation per single `Publish` call. Concurrent `Publish` calls interleave freely.
- **Publish after `Shutdown` still mutates history and runs handlers** but skips sinks. Callers that only attach sinks and shut down early will miss post-shutdown publishes for those sinks; handlers remain active forever.
- **`health-recovered` has no in-repo publisher today.** It appears in `AllEventTypes()` (so gateway/telemetry listen for it) but healthmon never publishes it — tracked separately by issue #481. (Four sibling reserved-but-unused types — `api-key-invalid`, `hormonal-trigger`, `rhizome-update`, `xylem-transport` — were pruned entirely rather than left as dead contract entries; see issue #439.)
- **`AllEventTypes` is a manual registry.** Adding a const without updating the slice silently omits the type from broad subscriptions (gateway, Resin, Prometheus pre-reg). Tests cover sink delivery of the listed set, not compile-time exhaustiveness against the const block.
- **`Event` data is untyped `map[string]interface{}`.** Schema discipline lives at each publisher; consumers must type-assert. Observation payloads on `tool-invoked` are truncated by the Conductor, not by the bus.
- **No package-level sentinel errors** for closed bus, drop, or invalid type — nil bus methods are silent no-ops.

## Design & rationale

**Why an in-process bus.** The Stem is one OS process. Signalling between Conductor (producers), healthmon, receptors, and multiple fan-out surfaces (WebSocket gateway, SQLite history, Resin file, remote transporters) needs a zero-dependency spine that does not import adapters or I/O stacks. Keeping `eventbus` fan-out 0 preserves the modularity baseline in the Architecture Audit: receptors/conductor aggregate; the bus stays a pure leaf. Network and disk I/O attach as sinks (or, for historical Resin, as handlers) without the bus knowing about them.

**Event-taxonomy contract.** Event names are a **cross-package, often persisted** contract: history.db rows store the string type; `/ws` frames forward it; Prometheus labels use it; the Command Center UI maps types to visual weather/tendrils. That is why constants live here rather than in each producer: one registry, kebab-case wire values (`sprout-emerged`, `phenotypic-selection`) matching AGENTS.md domain-enum rules, camelCase only on JSON fields of the envelope (`sessionId`). `AllEventTypes()` exists so broad consumers do not hard-code partial lists and drift when types are added — provided the slice is updated with the const.

**Two delivery lanes.** Typed `Subscribe` is the simple in-process observer API reserved for fast, non-blocking handlers — the gateway live feed handler body is just a non-blocking `select { case client.send <- payload: default: }` and never does I/O. `Sink` is the pluggable transport boundary designed so **slow or disconnected sinks never block Publish** — telemetry is lossy on purpose so the orchestrator hot path stays free. All durable or potentially slow consumers (historydb, Resin, and remote transporters) attach as sinks via `AttachSink`; the blocking `Subscribe` lane is reserved for in-process observers whose handlers are guaranteed fast.

**No network bus.** Multi-Stem or multi-process event distribution is out of scope for this leaf; remote Redis/Kafka/WebSocket transporters in `telemetry` are the deliberate exit ramps from the process-local spine.

There is no legacy plan-doc for this package: the as-built surface is the source of truth (`eventbus.go` + `eventbus_test.go` + `sinks_test.go`).
