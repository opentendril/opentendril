# Component: Server — an as-built-unwired WebSocket chat gateway (Hub + Handler) that proxies browser chat to the brain over the shared protocol types.

## Purpose

`cmd/stem/internal/server` is the intended **stateful WebSocket chat endpoint** for interactive / Command Center sessions: `ServeWS` upgrades a connection, registers the peer in a `Hub`, receives `protocol.IncomingMessage` frames, proxies each chat turn through `proxy.BrainClient.Chat`, and streams `protocol.OutgoingMessage` frames back to that client. As built, **it is not mounted by any binary** — no package in the repository imports it, and `cmd/stem/cmdserve.go` never references it. The live interactive surfaces today are REST `handleChatCompletions` plus the read-only event feed in `cmd/stem/internal/gateway`. This document describes the package honestly as designed-and-compiled but unwired.

## Responsibilities

**Does:**

- Upgrade an HTTP request to a WebSocket via a package-level `websocket.Upgrader` and `(*Handler).ServeWS` (`handler.go`).
- Assign a client id (`ws-<unix-nano>`), construct a `Client` with a buffered `Send` channel (capacity 64), and `Register` / `Unregister` it on the `Hub` (`handler.go`, `hub.go`).
- Track both a client map (keyed by client id) and a per-`SessionID` multi-device list (`sessions map[string][]*Client` in `hub.go`).
- Emit a first-frame `protocol.TypeConnected` confirmation with `RunID` set to the client id.
- Run unexported read and write pumps: `readPump` decodes JSON into `protocol.IncomingMessage` and dispatches on `protocol.Type*`; `writePump` drains `client.Send` as text frames and sends a server-side WebSocket ping every 30 seconds.
- Enforce a 32KB read limit, a 300-second read deadline, and a pong handler that refreshes that deadline (`readPump`).
- Dispatch known types: `TypePing` → `TypePong`; `TypeMessage` → `handleMessage` (brain proxy + stream frames); `TypeStop` → log only; unknown types → `TypeError`.
- Proxy a chat turn with `Brain.Chat(sessionID, content, provider)`, then emit `TypeStreamStart`, word-split `TypeStreamToken` frames, and a final `TypeStreamEnd` carrying the full response string (`handleMessage`).
- Optionally update `client.SessionID` when an inbound frame supplies a non-empty `sessionId`.
- Drop outbound frames when `Send` is full, logging a warning (`sendJSON`).

**Does not:**

- Get mounted by any HTTP mux or serve path as built — **no fan-in**; the package compiles but is orphaned (verified: no other `.go` file imports `cmd/stem/internal/server`).
- Perform real token streaming from the brain. `handleMessage` blocks on the full `Brain.Chat` string, then splits on whitespace and emits artificial word tokens with a 10ms sleep between them.
- Vary `SessionID` or `Source` at connect time — both are hardcoded (`"default"` and `string(protocol.SourceWeb)` / `"web"`).
- Actually broadcast. Despite the package and `Hub` comments claiming a “WebSocket gateway” that “broadcasts messages,” the `Hub` exposes no broadcast or session-fan-out method; the per-session client list is populated and cleaned up but never used to deliver frames.
- Own authentication, API-key middleware, or the serve lifecycle — those live (for other surfaces) in `cmd/stem/cmdserve.go`.
- Cancel in-flight brain work on `TypeStop` (commented “Future: cancel in-flight LLM request”).
- Expose a Core capability or appear in Core’s forbidden-import boundary list (it is absent because nothing, including Core, imports it).

## Public interface

| Symbol | Role |
| --- | --- |
| `Handler` | Request handler holding `Hub *Hub` and `Brain *proxy.BrainClient`. |
| `(*Handler).ServeWS` | Sole entrypoint: upgrade HTTP → WebSocket, register client, emit `connected`, start read/write pumps. |
| `Hub` | Connection registry: `clients` by id and `sessions` by session id (multi-device list). |
| `NewHub` | Constructor for an empty hub. |
| `(*Hub).Register` / `(*Hub).Unregister` | Add/remove a client; unregister closes `Send` and prunes the session list. |
| `(*Hub).ClientCount` / `(*Hub).SessionCount` | Snapshot counts under a read lock. |
| `Client` | Exported peer record: `Hub`, `ID`, `SessionID`, `Source`, `Send`, `Done`. |

Unexported (not part of the stable surface, listed for audit completeness): package `upgrader`; `(*Handler).readPump`, `writePump`, `handleMessage`, `sendJSON`; package helper `truncate`.

Package-level sentinel errors: **none**.

## Dependencies

**Fan-out:** `protocol` and `proxy` only (plus stdlib and `github.com/gorilla/websocket`). The package owns none of those types:

- **`cmd/stem/internal/protocol`** — `IncomingMessage` / `OutgoingMessage`, `Type*` constants, and `SourceWeb` (see `docs/DESIGN-PROTOCOL.md`).
- **`cmd/stem/internal/proxy`** — `BrainClient` and `(*BrainClient).Chat` for the full-string chat round-trip (see `docs/DESIGN-PROXY.md`).

**Fan-in:** **none.** No package in the repository imports `cmd/stem/internal/server`. It is absent from `cmd/stem/cmdserve.go` (which mounts `gateway.HandleWebSocket` on `/ws` and REST `handleChatCompletions` on `/v1/chat/completions`). It is also absent from the Core boundary test’s `forbiddenCoreImports` list in `cmd/stem/internal/core/boundary_test.go` — unsurprising only if the package is dead code; surprising under the Architecture Audit coupling check, which expects every transport leaf either to be wired or to be deliberately retired.

**Boundary note (gateway vs server):** `cmd/stem/internal/gateway` is the **live**, read-only EventBus fan-out WebSocket (`HandleWebSocket`, fan-out=1 to `eventbus`, mounted on `/ws`). This package was the intended **interactive, bidirectional chat** WebSocket that speaks the protocol message vocabulary and holds a session-aware `Hub`. As built they share a “WebSocket gateway” naming collision in package comments, but only gateway is on a serve path. See `docs/DESIGN-GATEWAY.md`.

## Limitations

- **Not wired into any binary.** The package is dead as-built: it compiles, but nothing constructs a `Handler` or calls `ServeWS`. Any checklist or narrative that treats it as the live chat surface is wrong; `cmd/stem/cmdserve.go` is authoritative.
- **Fake streaming, not incremental generation.** `handleMessage` waits for the full brain reply, then word-splits with `strings.Fields` and sleeps 10ms per token. Clients see `stream.token` frames, but the latency profile is “wait for full answer, then drip words,” not true token streaming from the model.
- **Hardcoded connect identity.** New clients always start as `SessionID: "default"` and `Source: "web"` (`protocol.SourceWeb` only). Inbound frames may later overwrite `SessionID`; `Source` is never updated from the wire.
- **Multi-device session map is write-only for delivery purposes.** `Register` / `Unregister` maintain `sessions[sessionID][]*Client`, but no path iterates that list to deliver a frame. All outbound traffic is single-client via `sendJSON` → that client’s `Send` channel.
- **`CheckOrigin` always returns true** on the package `upgrader` (comment: allow all origins in dev). Moot while unwired; would be open if mounted without outer policy.
- **`TypeStop` is a no-op** beyond a log line — no cancellation of the blocking `Brain.Chat` call, and `handleMessage` runs synchronously on the read pump so a stop frame cannot interrupt an in-flight turn on the same connection until the turn finishes.
- **Drop-on-full is lossy.** Unlike gateway’s silent drop, `sendJSON` logs when the 64-slot `Send` buffer is full, but the frame is still discarded with no client signal or counter.
- **No tests.** There is no `_test.go` under `cmd/stem/internal/server`.
- **Naming overlap with gateway.** Both this package’s doc comment (`// Package server implements the WebSocket gateway server.`) and `cmd/stem/internal/gateway` describe themselves as the WebSocket gateway. Only gateway is live; the dual branding is a source of audit confusion.

## Design & rationale

Server was designed as the **stateful, per-session interactive chat** WebSocket: a `Hub` that knows clients and multi-device sessions, a protocol vocabulary for chat turns and stream frames, and a thin proxy hop to the external brain via `proxy.BrainClient`. That is a different job from gateway’s **stateless, read-only event fan-out** (subscribe to `eventbus`, push telemetry, discard inbound client messages). The intended split is:

| Concern | Intended owner | As-built owner |
| --- | --- | --- |
| Interactive chat (browser ↔ brain) | `server` (`ServeWS` + protocol + proxy) | REST `handleChatCompletions` in `cmd/stem/cmdserve.go` |
| Live sequence/sprout/stream telemetry | `gateway` | `gateway` (mounted on `/ws`) |

As built, the project adopted REST chat plus the gateway event feed and never mounted `server`. The package remains a designed-but-unadopted path: the session model, protocol dispatch, and brain proxy are present; the serve mux never points at them. Where a checklist or narrative implies this package is live, the wiring in `cmd/stem/cmdserve.go` is authoritative — it is not mounted.

There is no legacy plan-doc that corresponds to this package. This as-built document does not retire any existing `DESIGN-*` file; it records the orphaned leaf for the Architecture Audit so a later wire-or-retire decision can be deliberate rather than accidental.
