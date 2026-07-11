# Enterprise Telemetry & Structured Logging: Resin, Amber, Transporters (Issue #136)

This document specifies the structured-logging contract for the Stem orchestrator: one
canonical event schema, the surfaces that carry it, and how local Resin logs harden into
the Amber archive. It records what is **implemented today** in
`cmd/stem/internal/telemetry/` and `cmd/stem/internal/eventbus/`, and names the remaining
follow-ups explicitly.

---

## 1. The Canonical Event Schema

Every telemetry surface carries the same JSON object — the wire form of
`eventbus.Event` (`cmd/stem/internal/eventbus/eventbus.go`):

```json
{
  "type": "sprout-emerged",
  "timestamp": "2026-07-11T09:15:04.291Z",
  "source": "step-1783569436504490340",
  "sessionId": "sess-abc123",
  "data": { "stepId": "meristem", "sequence": "deploy" }
}
```

Contract rules (per GUARDRAILS/AGENTS casing):

* **Keys are `camelCase`** (`sessionId`, `stepId`) — JSON payload boundary rule.
* **Event types are `kebab-case` domain enums** (`sprout-emerged`, `stream-token`,
  `phenotypic-selection`) — stored-enum rule. The full registry is
  `eventbus.AllEventTypes()`; new event types are added there, once, and every
  telemetry surface picks them up automatically.
* `timestamp` is UTC RFC 3339, stamped by the bus at publish time when the producer
  leaves it zero.
* `source` identifies the producer (a step id, `parallel-sprouting`, a subsystem name).
* `data` is event-specific payload; keys inside it are also `camelCase`.

**This schema is the single source of truth.** Resin lines, Transporter payloads, and
the `/ws` stream are all projections of it; none of them defines its own log format.

## 2. Surfaces

| Surface | Biology | Mechanism | Status |
| :--- | :--- | :--- | :--- |
| Local structured log | **Resin** | `ResinSink` appends one JSON event per line to `.tendril/logs/resin.log` | Shipped (#140) |
| Archived logs | **Amber** | Oversized resin.log gzip-hardens into `.tendril/logs/amber/` | Shipped (this slice) |
| External emitters | **Transporters** | Webhook (#140), Kafka via REST Proxy, Prometheus `/metrics` (#141) | Shipped |
| Live view | — | `/ws` EventBus stream (+ `?replay`) | Shipped; a *view*, outside the capability registry by design |

All surfaces attach to the EventBus as subscribers and must obey the telemetry
invariant: **a telemetry failure never blocks or fails the bus.** Sinks swallow their own
errors; `Emit`/`handle` do in-memory or local-append work only, never remote I/O on the
publish path (the Kafka transporter buffers; Prometheus only updates counters).

## 3. Resin → Amber Hardening (this slice)

Configured in `.tendril/telemetry.yaml`:

```yaml
enabled: true
resin:
  enabled: true
  format: json     # default
  level: info      # default; see §4
  amber:
    enabled: true  # default false — opt-in
    max_size_kb: 1024   # default when enabled
    keep: 5             # archives retained, default
```

Behavior (`telemetry/resin.go`):

1. After each append, if `resin.log` ≥ `max_size_kb`, the sink gzip-compresses it to
   `.tendril/logs/amber/resin-<UTC nanosecond stamp>.log.gz`.
2. The active log is truncated **only after the archive is fully written** — a failed
   compression can never lose events; the worst outcome of any Amber error is an
   oversized active log.
3. Oldest archives beyond `keep` are pruned. Archive names embed a sortable UTC stamp,
   so lexical order is chronological order.
4. Rotation runs under the sink's mutex on the same goroutine as the append —
   synchronous, deterministic, and still local-disk-only.

Amber is **immutable cold storage**: nothing in the Stem reads archives back; they exist
for operators and external log shippers (the issue's recommended enterprise pattern —
FluentBit/Vector tailing resin.log or amber/ — remains fully supported and unchanged).

## 4. Log Levels

`resin.level` is accepted and normalized (`info` default) but **not yet enforced**:
EventBus events are not leveled, so the sink currently records all of them. The intended
mapping when leveling lands (follow-up, not this slice):

* `debug` — everything, including `stream-token` chunks (high volume).
* `info` — lifecycle events (sprout/sequence/health/mesh), excluding per-token streams.
* `error` — only failure-family events (`*-failure`, `*-withered`, `terrarium-oom`,
  `terrarium-timeout`, `api-key-invalid`).

Leveling must be implemented as a *filter in the sink*, never by suppressing bus
publishes — other subscribers (the `/ws` view, Prometheus counters) need the full
stream.

## 5. Explicit Non-Goals & Follow-Ups

* **Ad-hoc `log.Printf` output across the orchestrator is not Resin.** Operational
  stderr logging remains unstructured; migrating high-value call sites to publish
  structured events (rather than reformatting stderr) is the correct alignment path and
  is tracked as follow-up work on #136.
* **Provider token-usage counts:** LLM providers do not currently report
  prompt/completion usage anywhere in the codebase; the Prometheus transporter derives
  token metrics from `stream-token` chunks (#141). If provider usage stats land in
  `roots/llm`, they should be published as a new kebab-case event type and will flow
  through every surface automatically.
* **Datadog/OTel emitters** stay out of the core binary; the webhook transporter plus
  log shippers cover them without adding dependencies.
