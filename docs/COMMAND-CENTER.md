# OS of OT — Visual Command Center

The **Command Center** is the operator-facing frontend of the "OS of OT"
(Operating System of OpenTendril). Where [ARCHITECTURE.md §5](../ARCHITECTURE.md)
describes the persistent, multi-session Go Stem *daemon* (the unified
`SessionManager`, the `.tendril/history.db` state layer, and the pluggable
EventBus), this document describes the **decoupled web client** that turns that
daemon into a single, living dashboard — and the Stem-side API contracts the
client depends on.

- **Where it lives:** [`ui/`](../ui/) (React 18 + Vite + TypeScript).
- **How to run / build it, the component tree, and the full event → visual
  mapping:** [`ui/README.md`](../ui/README.md).
- **Design intent:** deeply biological, dark-mode-first. The
  Rhizome/Sprout/Tendril taxonomy is the actual visual language — each
  orchestration grows as a plant whose branches, tendril tips, and phenotype
  arenas mutate as EventBus telemetry streams in.

---

## 1. How it fits the OS-of-OT picture

```
   Operator's browser
         │
         │  Command Center (ui/, static React app)
         │    · REST: hydrate cold state on load / reconnect
         │    · WebSocket /ws: live EventBus feed
         ▼
   ┌───────────────────────────────────────────────┐
   │            Unified Go Stem (daemon)            │
   │  SessionManager ── history.db ── EventBus      │
   └───────────────────────────────────────────────┘
```

The client is **strictly decoupled**: it consumes only the documented HTTP +
WebSocket surface and has no knowledge of Go internals. This is what allows the
UI to ship, version, and deploy independently of the Stem binary.

It leans on three Phase 1 backend capabilities:

1. **Unified `SessionManager`** — `GET /v1/sessions` lists every live Tendril
   regardless of which surface (CLI, MCP, REST, WS) sprouted it, so the operator
   sees the whole fleet in one rail.
2. **`history.db` persistence** — the per-session history endpoints let the UI
   re-hydrate its entire state after a browser refresh instead of starting blank.
3. **EventBus over `/ws`** — the live telemetry stream that drives the botanical
   visualization in real time.

---

## 2. REST surface consumed by the Command Center

All endpoints are served by the Go Stem on its API port (default `:8080`) and
authenticated with the operator bearer key. `OPENTENDRIL_API_KEY` (or
`ADMIN_TOKEN`) sets it explicitly; if neither is set, the Stem generates one on
first run and persists it to `.tendril/api-key` (printed once to the log) —
the API is never served unauthenticated. Handlers:
`cmd/stem/internal/api/sessions.go`.

| Method & path | Used for |
| --- | --- |
| `GET /health` | Onboarding reachability check. |
| `GET /v1/sessions` | The session rail; also the onboarding key-validation call. |
| `POST /v1/sessions` | "+ Sprout" — create a new Tendril session. |
| `PATCH /v1/sessions/{id}` | Update a session's preferences (model, genotype, …). |
| `DELETE /v1/sessions/{id}` | Prune a session. |
| `GET /v1/sessions/{id}/history` | Chat log hydration. |
| `GET /v1/sessions/{id}/sprout-runs` | The per-session execution list (drilldown source). |
| `GET /v1/sessions/{id}/events` | Persisted EventBus telemetry for garden re-growth. |
| `POST /v1/chat/completions` | Send a task into a session (sprouts a Tendril run). |

The `…/events` and `…/sprout-runs` endpoints return `501 Not Implemented` when
`OPENTENDRIL_DB_LOGGING=false`, since there is no persistent store to read from.

---

## 3. WebSocket surface — the EventBus gateway (`/ws`)

`/ws` requires the same bearer key as the REST surface. Native
WebSocket clients (e.g. the CLI's gorilla/websocket dialer) send it as an
`Authorization: Bearer <key>` header on the upgrade request; browsers cannot
attach custom headers to a WebSocket handshake, so the Command Center instead
appends it as a `?key=` query parameter (`ui/src/lib/api.ts#websocketUrl`).
Gating is applied identically on both the main API mux (`:8080`) and the
dedicated gateway listener (`:9090`).

Handler: `cmd/stem/internal/gateway/gateway.go`. On connect the client receives
a `{"type":"connected"}` frame, then a live stream of EventBus events. Each
frame is JSON of the shape:

```jsonc
{
  "type": "sprout-matured",          // EventBus event type
  "timestamp": "2026-07-07T…Z",
  "source": "parallel-sprouting",
  "sessionId": "tendril-…",          // omitted when the event carries none
  "data": { "sproutIndex": 2, "branchName": "…" },
  "content": "…"                      // convenience copy of data.token / data.thought
}
```

The registered event types are defined in
`cmd/stem/internal/eventbus/eventbus.go` (`AllEventTypes()`), and the gateway
subscribes a handler for every one of them. The UI's event → botanical-visual
mapping for each type is tabulated in [`ui/README.md`](../ui/README.md).

### 3.1 `?replay=N` — recent-history replay (public contract)

`/ws` accepts an **opt-in** `replay` query parameter:

```
ws://<stem>/ws?replay=100
```

When present, immediately after the `connected` frame the gateway replays up to
`N` events from the bus's in-memory history window (capped at 100, the bus's
`maxHistory`) before the live feed begins. Without the parameter, behavior is
unchanged — no replay.

**Why it exists:** the per-session `…/events` REST endpoint can only return
events that carry a `sessionId`. Orchestration telemetry emitted by the sequence
runner, parallel-sprouting, and phenotypic-selection is **session-less** (it is
keyed by sequence/step, not session), so a client that refreshes mid-sequence
cannot recover that timeline from REST alone. `?replay=N` lets a reconnecting
client re-grow session-less sequence state from the bus's recent history. The
Command Center requests `replay=100` on every connect.

Replay is lossy by design — it only reaches back as far as the bus's 100-event
in-memory window — and is a best-effort supplement to REST hydration, not a
guaranteed-complete event log. The durable log remains `history.db`.

---

## 4. Stem changes made for the Command Center

The following backend changes shipped alongside the UI. All are **additive and
backward-compatible** — existing CLI, MCP, and Stem-Grafting behavior is
unchanged.

| Change | File | Why | Blast radius |
| --- | --- | --- | --- |
| **`phenotypic-selection` registered as a first-class `EventType`** and added to `AllEventTypes()`. | `internal/eventbus/eventbus.go`, `internal/orchestrator/selection.go` | It was previously published with an ad-hoc `EventType("phenotypic-selection")` that no gateway subscription covered, so Genetic-Algorithm telemetry reached persistence sinks but **never the `/ws` live feed** (and, carrying no `sessionId`, was unreachable over REST too). | The gateway now forwards these events to every WS client, and `history.db` still records them as before. No change to selection logic or the event payload shape. |
| **`/ws?replay=N`** opt-in recent-history replay. | `internal/gateway/gateway.go` | Lets a refreshed/reconnected client recover session-less sequence telemetry (see §3.1). | Only active when the query parameter is supplied; default connections are byte-for-byte unchanged. Bounded by the bus's existing 100-event window. |
| **`GATEWAY_PORT` env var** for the standalone WS listener (default `9090`). | `cmd/stem/cmd-serve.go` | Allows running a second Stem (or the UI's dev Stem) without a port clash on the auxiliary gateway. | Purely additive; unset preserves the previous `:9090` default. |
| **Graceful gateway-bind degradation.** | `cmd/stem/cmd-serve.go` | The standalone `:9090` listener previously `log.Fatalf`'d — a port conflict there killed the **entire API server**. It now logs a warning and continues; the same `/ws` surface is already mounted on the main API mux, so live telemetry still works. | Strictly more robust. The main API `/ws` endpoint is unaffected and remains the primary WebSocket surface. |

---

## 5. Deployment — the containerized UI front

The Command Center ships as a **separate, optional, isolated, containerized
component**: a hardened nginx container (built by
[`ui/Dockerfile`](../ui/Dockerfile), configured by
[`ui/nginx/default.conf.template`](../ui/nginx/default.conf.template)) that
serves the static bundle **and** reverse-proxies the Stem's documented API
surface, giving the browser a single origin. The Stem itself stays **on the
host and headless** — it never serves the UI, and the system is fully
operable with this container absent.

```
   Operator's browser ── single origin, e.g. http://127.0.0.1:4173
         │
         ▼
   ┌───────────────────────────────────────────────┐
   │   ui container (nginx, non-root, read-only)   │   docker compose --profile ui
   │     /            → static ui/dist bundle      │
   │     /health /v1* → host.docker.internal:8080  │
   │     /ws          → host.docker.internal:9090  │   (falls back to :8080)
   └───────────────────────────────────────────────┘
         │  host-gateway
         ▼
   Unified Go Stem (host daemon — headless, unchanged)
```

- **Opt-in:** the `ui` compose service sits behind the `ui` profile and never
  starts unless `--profile ui` is passed. One command brings it up alongside
  the host Stem: `docker compose --profile ui up -d`.
- **Single origin, no CORS:** the browser only ever talks to the container, so
  the Stem needs no CORS headers (adding them was explicitly rejected).
  In development, Vite's proxy plays the same role via `STEM_TARGET`.
- **Auth preserved:** the proxy forwards the operator's bearer key untouched;
  the Stem's `withAPIKeyAuth` remains the sole authority. Only `/health`,
  `/v1*`, and `/ws` are proxied — nothing else on the host is reachable.
- **WebSocket upgrade:** the `/ws` proxy speaks HTTP/1.1 with
  `Upgrade`/`Connection` headers, prefers the dedicated gateway listener
  (`:9090`), and falls back to the main API mux (`:8080`) — mirroring the
  Stem's own graceful gateway-bind degradation (§4).
- **Hardened:** non-root image (`nginx-unprivileged`), read-only root
  filesystem, all capabilities dropped, `no-new-privileges`, loopback-only
  port binding by default. The CSP locks `script-src` to `'self'` (no inline
  scripts, no `eval`) and, since the posture audit, splits `style-src` so
  `<style>` tags/stylesheets are `'self'`-only (`style-src-elem`) while only
  React's inline `style=""` attributes keep `'unsafe-inline'`
  (`style-src-attr`) — an XSS payload can no longer inject an arbitrary
  `<style>` element for CSS-based exfiltration or UI redress.
- **Growth path:** any future server-side layer — BFF, operator auth/SSO,
  enterprise integration, the optional concierge mini-model — grows
  **inside this UI component**, never in the Stem. The Stem's surface stays
  the headless CLI/MCP/OpenAPI capability core.

See [`ui/README.md`](../ui/README.md) for commands, configuration variables,
and the manual static-build alternative.
