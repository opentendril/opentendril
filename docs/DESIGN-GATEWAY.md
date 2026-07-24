# Component: Gateway — read-only WebSocket fan-out of EventBus events to live terminal and dashboard clients.

## Purpose

`cmd/stem/internal/gateway` is the read-only push feed that fans EventBus events out to WebSocket clients (terminal UI / web dashboard) so they see live sequence, sprout, stream-token, and thought telemetry. It is a thin transport adapter over the bus — upgrade, subscribe, map, and write — and a deliberate fan-out=1 leaf: its only OpenTendril dependency is `eventbus`.

## Responsibilities

**Does:**

- Upgrade an HTTP request to a WebSocket via a package-level `websocket.Upgrader` and own one buffered outbound channel (`send`, capacity 256) per connection (`gateway.go`).
- Subscribe a single shared handler to every type returned by `eventbus.AllEventTypes()` on that connection's bus (`Subscribe` once per type).
- Emit a first-frame `{"type":"connected"}` handshake so clients know the feed is live before any bus event arrives.
- Map each `eventbus.Event` to a JSON object with `type`, `timestamp`, and `source`; include `sessionId` when non-empty, `data` when non-empty, and a top-level `content` field for `EventStreamToken` (from `data["token"]`) and `EventThoughtBranch` (from `data["thought"]`).
- Support opt-in catch-up via `?replay=N`, replaying the last `N` events from `bus.History` capped at package `maxReplay` (100).
- Keep the connection alive with a server-side write-pump ping every 50 seconds (`writePump`).
- Discard inbound client frames in `readPump` so the feed stays unidirectional (read only to detect disconnect).

**Does not:**

- Own authentication or origin policy. Bearer/`key` query auth is layered in `cmd/stem/cmdserve.go` (`withWebSocketAuth`); `CheckOrigin` inside the package always returns true.
- Accept or interpret inbound client messages as commands — `readPump` discards payload bytes; there is no reverse RPC over `/ws`.
- Own the `GATEWAY_PORT` (default `9090`) server lifecycle or the main API mux mount. Both are constructed in `cmd/stem` (`cmdserve.go`), which also wraps the handler with `delegationGate.Middleware`.
- Persist events, filter by session, rate-limit, or back-pressure publishers. Lossy drop-on-full is local to the connection's `send` channel.
- Expose a Core capability. The gateway is a transport surface only; it is listed among Core's forbidden imports in the boundary test.

## Public interface

| Symbol | Role |
| --- | --- |
| `HandleWebSocket` | Sole public API: `func HandleWebSocket(bus *eventbus.Bus) http.HandlerFunc`. Returns a handler that upgrades, subscribes, optionally replays, and runs read/write pumps until the client disconnects. |
| `Client` | Exported struct for a connected peer (`conn`, buffered `send` channel). Fields and methods are unexported — the type is exported-but-opaque and is not constructed outside the package. |
| `maxReplay` | Unexported const (`100`): hard cap on `?replay=N`. |
| `upgrader` | Unexported package `websocket.Upgrader` with `CheckOrigin` always true. |
| `(*Client).readPump` | Unexported: reads until error, discards inbound messages, closes the connection. |
| `(*Client).writePump` | Unexported: drains `send` as text frames; pings every 50s with a 10s write deadline on ping. |

Package-level sentinel errors: **none**.

## Dependencies

**Fan-out:** `eventbus` only (plus stdlib and `github.com/gorilla/websocket`). The package imports `AllEventTypes`, `(*Bus).Subscribe`, `(*Bus).History`, and the `Event` shape (`Type`, `Timestamp`, `Source`, `SessionID`, `Data`) from `cmd/stem/internal/eventbus`. Fan-out count is deliberately 1.

**Fan-in:**

- **`cmd/stem`** — mounts `/ws` on the main serve mux and again on a dedicated Gateway server (`GATEWAY_PORT`, default `9090`), each behind `withWebSocketAuth` (API key via `Authorization` bearer or `key` query parameter) and `delegationGate.Middleware` (`cmdserve.go`). A bind failure on the dedicated port logs a warning and leaves the main-mux `/ws` available.
- **`internal/core`** — does **not** import `gateway` (boundary test lists it among forbidden Core imports). Gateway exposes no capability to Core; it is a transport surface wired only in `cmd/stem`.

## Limitations

- **Subscription leak on disconnect.** Each connection appends one handler per event type via `Subscribe` and never removes them. There is no `Unsubscribe` on the bus, so closed clients leave dead handlers that still run on every publish and drop into a full/closed send path. Tracked as an existing EventBus-side P0 (do not re-file from this package alone).
- **`CheckOrigin` always returns true.** Origin trust is delegated entirely to outer middleware in `cmdserve.go`; the gateway package itself accepts any Origin header on upgrade.
- **Full `send` channel silently drops events.** The subscribe handler uses `select` with a `default` case: when the 256-slot buffer is full, the payload is discarded with no counter, log, or client signal. Slow consumers lose data without noticing.
- **Weak dead-peer detection.** `writePump` sends Ping frames every 50s but does not register a pong handler or set a read deadline on the connection. `readPump` only exits when `ReadMessage` returns an error; half-open peers can linger until the next write fails.
- **Replayed events look identical to live ones.** `?replay=N` invokes the same handler used for live `Subscribe` delivery; frames carry no replay marker, sequence number, or "historical" flag. Clients cannot tell catch-up from real-time without their own heuristics.
- **`Client` is exported but opaque.** External packages can name the type but cannot construct it or call its methods; the export is vestigial relative to the single-entry public API.

## Design & rationale

The gateway exists so live observers (Command Center UI, terminal clients) share one process-local signalling spine without each attaching their own bus logic. Keeping it a **thin fan-out leaf** preserves the Architecture Audit modularity baseline: producers publish to `eventbus`; durable sinks attach elsewhere; this package only upgrades a socket and pushes JSON. Fan-out=1 (`eventbus` only) means the leaf cannot drag HTTP auth, Core, or Conductor into the transport layer.

**Auth and origin stay outside the package.** Bearer and query-key checks, plus the delegation gate, are Stem serve concerns shared with REST/MCP posture. Browsers cannot set custom headers on the native WebSocket handshake, so `withWebSocketAuth` deliberately accepts a `key` query parameter; that policy belongs next to the rest of serve auth, not inside the upgrade handler. Leaving `CheckOrigin` open inside the package matches that layering — origin policy, if tightened, should land where the mux is composed.

**`?replay=N` exists for session-less telemetry.** Many sequence and organism events never carry a `sessionId`. A refreshed dashboard cannot re-subscribe by session; it needs a short in-memory catch-up window from `bus.History` (itself capped at 100 on the bus, and again at `maxReplay` here) so the UI can re-grow recent state before the live feed continues. Replay is opt-in and best-effort, not a durable log.

There is no legacy plan-doc for this package. The vascular-cambium parallel-step execution it was sometimes conflated with is a Conductor concern, documented in `docs/DESIGN-CONDUCTOR.md`, and is not a gateway design. Where any checklist or narrative claimed otherwise, the shipped code in `gateway.go` / `gateway_test.go` and the serve wiring in `cmdserve.go` are authoritative.
