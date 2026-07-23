# Component: Telemetry — pluggable Transporters and the local Resin sink that carry EventBus activity across the system boundary.

## Purpose

`cmd/stem/internal/telemetry` is the observability layer of the Stem. It turns the in-process EventBus into durable and exportable signal: a local structured trace log (**Resin**, hardening into **Amber** archives), a standard-library Prometheus scrape endpoint, and a set of **Transporters** that push every event to external platforms (Webhook, Redis pub/sub, remote WebSocket, Kafka REST Proxy). It owns configuration parsing (`telemetry.yaml`), the `Transporter` abstraction, and the wiring that attaches each emitter to the bus. It does not produce events — every producer publishes to the EventBus, and telemetry only consumes.

## Responsibilities

**Does:**

- Define the `Transporter` abstraction (`Emit(eventbus.Event) error`) and construct concrete transporters from config (`NewTransporter` dispatch in `transporter.go`).
- Attach a transporter to the bus as an asynchronous, buffered sink (`AttachTransporter` → `bus.AttachSink`), so remote emission runs off the publish hot path.
- Append every event as newline-delimited JSON to the local Resin trace log, and harden an oversized `resin.log` into a gzip archive under `amber/` with bounded retention (`resin.go`).
- Expose EventBus activity as Prometheus text-exposition metrics over a `/metrics` HTTP listener, updating only in-memory counters on `Emit` (`prometheus.go`).
- Pre-register every known `eventbus.EventType` at zero so dashboards see a stable series set from the first scrape.
- Parse `.tendril/telemetry.yaml` into `Config` and normalize Resin/Amber defaults (`config.go`).
- Build remote transporters from the `TENDRIL_REMOTE_SINKS` environment variable, returning malformed entries as errors alongside the valid ones (`remotesinks.go`).

**Does not:**

- Emit or originate events — it is purely a consumer of the EventBus.
- Guarantee delivery. Sink attachment is lossy by design (a full sink buffer drops the event for that sink only); transporter emit failures are counted and log-sampled, never retried durably.
- Buffer or spool remote sinks to disk — Resin is the only persistent record.
- Carry an external Kafka/Redis client dependency: every transporter is stdlib-only except the WebSocket path, which reuses `gorilla/websocket`.
- Redact or filter payloads — whatever a producer puts in an event is what Resin writes and what remote transporters ship.

## Public interface

| Symbol | Role |
| --- | --- |
| `Config` | Root of `.tendril/telemetry.yaml`: `Enabled`, `Resin`, `Transporters`. |
| `ResinConfig` | Local trace-log settings: `Enabled`, `Format`, `Level`, `Amber`. |
| `AmberConfig` | Archive hardening: `Enabled`, `MaxSizeKB`, `Keep`. |
| `TransporterConfig` | One external emitter: `Type`, `Endpoint`, `Port`, `Brokers`, `Channel`, `APIKey`. |
| `LoadConfig` | Read + YAML-decode a config path; applies Resin/Amber defaults via `normalizeConfig`. |
| `Transporter` | Interface — `Emit(eventbus.Event) error`. The pluggable emitter boundary. |
| `NewTransporter` | Factory dispatch on `cfg.Type`: `webhook` / `redis` / `websocket` / `prometheus` / `kafka`. |
| `AttachTransporter` | Wrap a `Transporter` in a `transporterSink` and attach it to the bus as an async sink. |
| `ResinSink` / `InitResinSink` | Local JSON trace sink; `Init` subscribes it to every event type and returns `nil, nil` when disabled. |
| `WebhookTransporter` / `NewWebhookTransporter` | POST JSON event payloads to an HTTP endpoint (Bearer auth optional). |
| `RedisTransporter` / `NewRedisTransporter` | PUBLISH events to a Redis channel over raw RESP, redialing lazily. |
| `RemoteWebSocketTransporter` / `NewRemoteWebSocketTransporter` | Stream events as JSON to a remote WebSocket collector. |
| `PrometheusTransporter` / `NewPrometheusTransporter` | Start a `/metrics` listener; `Emit` only bumps in-memory counters. |
| `(*PrometheusTransporter).Addr` / `.Close` | Report the bound address; shut down the listener and server. |
| `KafkaTransporter` / `NewKafkaTransporter` | Produce events to a Kafka topic via a Kafka REST Proxy (Confluent v2 JSON). |
| `TransportersFromEnv` / `ParseRemoteSink` / `EnvRemoteSinks` | Build transporters from `TENDRIL_REMOTE_SINKS`; supported schemes `redis(s)` / `ws(s)` / `http(s)`. |

**Package-level sentinel errors:** none. Every failure is a wrapped `fmt.Errorf` at the call site (missing endpoint, unknown type, bind failure, non-2xx response) rather than a comparable sentinel.

## Dependencies

**Fan-out — `internal/eventbus` (how it consumes):** Telemetry attaches to the bus two different ways, and the difference matters.

- **Transporters attach as sinks.** `AttachTransporter` calls `bus.AttachSink`, which drains a per-sink buffered channel on a dedicated goroutine. Emission runs off the publish path; when the buffer is full the event is dropped for that sink only, so a slow or disconnected remote can never block the orchestrator. Emit failures increment an atomic counter and are log-sampled (1 in 100).
- **The Resin sink subscribes as a handler.** `InitResinSink` calls `attachHandler`, which `Subscribe`s the sink to every `eventbus.AllEventTypes()`. Handlers run **synchronously on the Publish goroutine** (`Publish` invokes each handler inline before fanning out to sinks). Resin therefore writes on the caller's path, guarded by its own mutex; write and hardening errors are swallowed so telemetry never fails the bus.
- The Prometheus transporter reads `eventbus.AllEventTypes()` to pre-register series and switches on `EventStreamToken` / `EventSproutEmerged` / `EventSproutMatured` / `EventSproutWithered` to derive LLM-token and active-sprout metrics.

**Fan-in — `cmd/stem` (how it wires transporters):** `cmdserve.go` is the sole assembler. On serve it (1) calls `TransportersFromEnv()` and attaches each env-derived remote sink, logging any malformed entries; (2) `LoadConfig(.tendril/telemetry.yaml)`, tolerating an absent file via `errors.Is(err, fs.ErrNotExist)`; (3) if enabled and `Resin.Enabled`, calls `InitResinSink` against `.tendril/logs/resin.log`; (4) for each `cfg.Transporters` entry, calls `NewTransporter` and `AttachTransporter`, continuing past any constructor error. No other package imports telemetry.

## Limitations

- **Delivery is best-effort and lossy.** Sink buffers drop events when full; there is no ack, retry queue, or disk spool for remote transporters. Under bus pressure or a stalled remote, exported telemetry is incomplete by construction. Resin (synchronous handler) is the only path that does not silently drop, but it too swallows write errors.
- **Transporter maturity varies.** Resin and Prometheus are production-grade and directly tested (event counting, all-types pre-registration, LLM token/character counting, sprouts-active gauge, config validation, loopback bind). Webhook, Redis (raw RESP), and remote WebSocket are functional but thinner. **Kafka is a first slice**: it supports only a Kafka REST Proxy endpoint — a brokers-only config is rejected loudly rather than silently dropping telemetry, and native broker wire protocol is explicitly deferred.
- **Config validation is shallow.** `LoadConfig` normalizes Resin/Amber defaults but does not validate transporter entries; a bad `Type`, missing `Endpoint`, or unbindable Prometheus port surfaces only later at `NewTransporter`/attach time, where `cmdserve.go` logs and continues. There is no schema check that a transporter block is coherent before serve.
- **PII / sensitive data flows through unfiltered.** Resin writes the full event JSON to disk, and every attached transporter ships the same payload off-box. `EventStreamToken` events carry raw LLM output token chunks; anything a producer places in `event.Data` (prompts, paths, session IDs) is persisted and exported verbatim. There is no redaction layer. API keys for transporters live in `telemetry.yaml` / `TENDRIL_REMOTE_SINKS` in cleartext.
- **Prometheus bind posture is loopback-by-default (noted).** With `Port` set and no `Endpoint`, `NewPrometheusTransporter` binds `127.0.0.1:<port>` so enabling telemetry never silently exposes operational data on all interfaces — asserted by `TestPrometheusTransporterPortBindsLoopback`. A non-loopback bind is possible only when an operator sets `Endpoint` verbatim (e.g. `0.0.0.0:9091`), an explicit opt-out. The `/metrics` endpoint itself is unauthenticated, so a non-loopback `Endpoint` exposes metrics with no access control.
- **Metrics are process-lifetime, in-memory only.** Counters reset on restart and the active-sprout gauge is clamped at zero (a matured/withered event for a sprout that emerged before the transporter attached cannot underflow, but is also uncounted).
- **Amber hardening can only bound, not guarantee.** Compression and prune errors are swallowed; the worst case is an oversized active `resin.log`, and a rotation only truncates after a fully written archive so events are never lost to a failed compression.

## Design & rationale

**Why a pluggable `Transporter` off the EventBus.** The EventBus is the single spine every producer already publishes to, so observability is a consumption concern, not a cross-cutting instrumentation one. Modeling each external platform as a `Transporter` with one method (`Emit`) and attaching it as an asynchronous bus sink keeps the orchestrator hot path indifferent to how many destinations are configured or whether any of them is healthy — a disconnected Redis or a scraper outage cannot slow event publication. The factory (`NewTransporter`) and env parser (`TransportersFromEnv` / `ParseRemoteSink`) mean new destinations are added by config, not code changes at call sites. The stdlib-only constraint (raw RESP for Redis, HTTP+JSON REST Proxy for Kafka, hand-written Prometheus exposition) is deliberate: the Stem binary stays dependency-light, which is why Kafka rides the REST Proxy rather than a broker client.

**Local Resin versus remote sinks.** Resin exists because remote delivery is lossy and optional, and an operator needs one durable, always-available record on the box. It attaches as a synchronous handler (not an async sink) precisely so it is the authoritative trace — it does not participate in the drop-when-full contract that the remote sinks accept. Amber then bounds that record's disk cost: `resin.log` hardens into timestamped gzip archives past a size threshold, keeping at most `Keep` of them, with UTC-nanosecond archive names so lexical order is chronological. Remote transporters are the fan-out for fleets — pushing the same event stream to a central collector (Redis pub/sub, WebSocket, webhook, or Kafka) — while Resin remains the local ground truth each Stem can always fall back to.
