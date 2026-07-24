# Component: Protocol — the Unified Message Object and WebSocket wire types every chat channel normalizes into.

## Purpose

`cmd/stem/internal/protocol` is a dependency-free leaf that declares the canonical message shapes exchanged between clients and the Stem, and the internal representation those channels normalize into before reaching the Brain. It defines three concerns in one file (`message.go`): the client-facing WebSocket envelopes (`IncomingMessage` / `OutgoingMessage`) with their string message-`type` constants, a `Source` enum naming every channel a message can originate from (Web UI, CLI, SaaS, Slack, Discord, Telegram, API), and the channel-agnostic `UnifiedMessage` / `UnifiedResponse` pair intended as the single interior format so the Brain never has to know which surface a request arrived on. It is pure data and JSON tags — no behavior, no transport, no validation.

## Responsibilities

**Does:**

- Define the WebSocket request envelope `IncomingMessage` (`type` / `content` / `provider` / `sessionId` / `runId`) that clients send (`message.go`).
- Define the WebSocket response envelope `OutgoingMessage` (`type` / `content` / `runId` / `error`) that the Stem streams back.
- Enumerate the message-`type` string constants for both directions (`TypeMessage`, `TypeStop`, `TypePing`, `TypePong`, `TypeStreamStart`, `TypeStreamToken`, `TypeStreamEnd`, `TypeError`, `TypeConnected`).
- Define the `Source` type and its channel constants (`SourceWeb`, `SourceCLI`, `SourceSaaS`, `SourceSlack`, `SourceDiscord`, `SourceTelegram`, `SourceAPI`).
- Define the normalized interior types `UnifiedMessage` (identity, source, channel/thread/user context, content, session, free-form metadata, timestamp) and `UnifiedResponse` (`replyTo` / `content` / `runId`).
- Fix the JSON wire contract for all of the above via struct tags (camelCase field names, explicit `omitempty` on optional fields).

**Does not:**

- Serialize, deserialize, or transport anything — the fan-in package owns `encoding/json` and the WebSocket read/write loop.
- Validate field values, enforce that a `Type` string matches a known constant, or reject an unknown `Source` — these are plain strings with no constructor or guard.
- Version the schema or negotiate capabilities — no protocol-version field exists on any message.
- Populate or consume `UnifiedMessage` / `UnifiedResponse` itself — those types are declared here but not yet referenced anywhere else in the tree.
- Own any channel adapter (Slack/Discord/CLI/SaaS wiring lives outside this leaf).

## Public interface

| Symbol | Role |
| --- | --- |
| `Source` | String type naming the channel a message originated from. |
| `SourceWeb` / `SourceCLI` / `SourceSaaS` / `SourceSlack` / `SourceDiscord` / `SourceTelegram` / `SourceAPI` | The enumerated channel values a `Source` may hold. |
| `IncomingMessage` | Client → Stem WebSocket request envelope (`type`, `content`, `provider`, `sessionId`, `runId`). |
| `OutgoingMessage` | Stem → client WebSocket response envelope (`type`, `content`, `runId`, `error`). |
| `TypeMessage` / `TypeStop` / `TypePing` / `TypePong` / `TypeStreamStart` / `TypeStreamToken` / `TypeStreamEnd` / `TypeError` / `TypeConnected` | Untyped string constants for the `Type` field in both directions. |
| `UnifiedMessage` | Channel-agnostic normalized request for the Brain: id, source, channel/thread/user context, content, provider, session, `metadata`, timestamp. |
| `UnifiedResponse` | Brain reply before platform-specific formatting (`replyTo`, `content`, `runId`). |

Package-level sentinel errors: none — the package exports data types only.

## Dependencies

**Fan-out:** none (leaf). Standard library `time` only (for `UnifiedMessage.Timestamp`); no other OpenTendril internal packages and no third-party imports.

**Fan-in:**

- **`cmd/stem/internal/server`** — `handler.go` is the only consumer. It decodes each inbound frame into `protocol.IncomingMessage`, switches on the `protocol.Type*` constants (`TypePing`/`TypeMessage`/`TypeStop`), and replies by JSON-encoding `protocol.OutgoingMessage` values tagged with `TypeConnected` / `TypePong` / `TypeStreamStart` / `TypeStreamToken` / `TypeStreamEnd` / `TypeError`. It stamps a new client's origin with `string(protocol.SourceWeb)`. It does **not** touch `UnifiedMessage` or `UnifiedResponse`.

## Limitations

- **No schema versioning.** No message carries a protocol- or schema-version field, so the WebSocket contract cannot be evolved with negotiation — a client and Stem on different shapes fail silently or on decode rather than by explicit mismatch.
- **`UnifiedMessage` / `UnifiedResponse` are declared but unused.** As built, no package produces or consumes them; the "normalize every channel into one interior format" intent stated in the package comment is not yet realized. The live path is `IncomingMessage` → handler → Brain, bypassing the unified layer entirely.
- **The `Source` enum is largely aspirational.** Seven channels are named, but only `SourceWeb` is referenced in the tree, and it is set as a hardcoded constant, never selected from an actual channel. There is also no `SourceMCP` even though the package comment scopes surfaces broadly and MCP is a planned surface.
- **Message `Type` is an unvalidated free string.** The `Type*` values are plain untyped `string` constants, not a defined type, so there is no compile-time exhaustiveness and no runtime rejection of an unknown `type`; the handler's default branch is the only backstop.
- **No constructors, no validation.** Any struct can be built with empty required fields; `IncomingMessage.Content` and `UnifiedMessage.Content` have no non-empty guarantee, and nothing enforces that `Provider` names a real provider.
- **JSON contract rigidity is implicit and untested.** Client compatibility rests entirely on the camelCase struct tags (`sessionId`, `runId`, `channelId`, `threadId`, `userId`, `userName`, `replyTo`). A tag rename is a silent breaking change to every WebSocket client.
- **Inconsistent optionality across the two hops.** `IncomingMessage.SessionID` is `omitempty` (optional on the wire), while `UnifiedMessage.SessionID`, `.Content`, `.ID`, and `.Timestamp` are required (no `omitempty`) — the normalize step that would reconcile that gap is the code that does not exist yet.
- **`Metadata map[string]any` is an untyped escape hatch** on `UnifiedMessage` with no schema, deferring any structured per-channel context to convention.
- **No tests.** There is no `_test.go` in the package; the JSON tag contract, the constant values, and `Source` casing are entirely uncovered, so drift is caught only downstream (if at all).

## Design & rationale

The package exists so that one message vocabulary is shared across every interface surface instead of each channel inventing its own DTOs. Splitting the wire envelopes (`IncomingMessage` / `OutgoingMessage`) from the interior form (`UnifiedMessage` / `UnifiedResponse`) is a deliberate seam: the client-facing types are shaped by the WebSocket streaming protocol (discrete `stream.start` / `stream.token` / `stream.end` frames correlated by `runId`), while the interior types are shaped by what the Brain needs (stable `id`, originating `Source`, channel/thread/user provenance, and a `metadata` bag). A channel adapter's job is then narrowly defined — translate its native payload into a `UnifiedMessage` and render a `UnifiedResponse` back — so the Brain stays channel-agnostic and a new surface (Slack, Discord, SaaS) is additive rather than a fork of the request path.

Keeping the component a pure-data leaf with zero fan-out is what lets it be the shared reference point: because it imports nothing internal, every surface and the server can depend on it without creating a cycle, and the types stay a contract rather than a behavior. The honest as-built caveat is that only the WebSocket half of that design is wired. The `Source` enum and the unified interior pair are the declared target shape of the normalization layer; the code that populates them is future work, and this document treats the code — a single live Web WebSocket path — as authoritative over the broader intent the package comment describes.
