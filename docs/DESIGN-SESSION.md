# Component: Session — unified session manager for OpenTendril (Phytomer lifecycle).

## Purpose

`cmd/stem/internal/session/` implements the unified session manager for OpenTendril. A Phytomer is the canonical name for one logical interaction thread — what the external surfaces present as a "session" — bound to a unique session ID. Every interface surface (CLI chat, MCP server, REST endpoints, WebSocket gateway) resolves its traffic through this single Manager, allowing concurrent conversations to coexist and maintain their own preferences.

## Responsibilities

**Does:**

- Maintain in-memory active Phytomer sessions and their unified chat logs (a bounded idle-TTL cache when a `Store` is attached).
- Mint unique Phytomer session identifiers (`NewID`) and validate externally supplied IDs (`ValidID`).
- Layer session-specific overrides (Preferences) on top of global defaults.
- Persist Phytomer sessions and messages to a database if a `Store` is provided.
- Recover previously persisted sessions across restarts via `NewManager`, and lazily rehydrate individual idle-evicted sessions on `Get` / `GetOrInitiate` via `Store.LoadSession`.
- Provide concurrency-safe access to sessions and messages across multiple interface surfaces.
- Optionally sweep idle entries out of the in-memory map on a daemon-tied background loop (`StartIdleEviction`), never deleting from the store.

**Does not:**

- Implement the persistence layer itself (the `Store` interface is injected, e.g., by `internal/historydb`).
- Enforce token/cost accounting or rate limits.
- Permanently delete idle sessions from durable storage (that remains `Prune`'s explicit, user-triggered job).

## Public interface

| Symbol | Role |
| --- | --- |
| `Manager` | Coordinates live Phytomer sessions across surfaces; with a store, `m.sessions` is a bounded cache over the durable record. |
| `NewManager` | Creates a Manager, resuming previously persisted sessions when a store is attached. |
| `Store` | Interface to persist Phytomer sessions and their chat logs (`SaveSession`, `DeleteSession`, `LoadSessions`, `LoadSession`, `AppendMessage`, `LoadMessages`). |
| `Phytomer` | One stateful interaction thread, including ID, origin, timestamps, and preferences. |
| `Message` | One unified chat-log entry bound to a Phytomer. |
| `Preferences` | Per-Phytomer overrides (provider, model, genotype, epigenetic genome, extras). |
| `NewID` | Mints a unique Phytomer session identifier. |
| `ValidID` | Reports whether an externally supplied session ID is acceptable. |
| `EnvSessionTTL` | Env var name (`TENDRIL_SESSION_TTL`) for the idle-cache TTL duration string. |
| `(*Manager).Initiate` | Creates and persists a new Phytomer session. |
| `(*Manager).Get` | Returns a snapshot of a session; store-backed Managers rehydrate from `LoadSession` on cache miss. |
| `(*Manager).GetOrInitiate` | Resolves an existing session (including store-backed resume) or creates one (adopting well-formed IDs). |
| `(*Manager).List` | Returns all sessions, most recently active first; store-backed Managers source from `LoadSessions`. |
| `(*Manager).UpdatePreferences` | Merges preference overrides into a session and persists the update. |
| `(*Manager).Touch` | Refreshes a session's activity timestamp. |
| `(*Manager).RecordMessage` | Appends a message to a session's log, buffering in memory and persisting. |
| `(*Manager).History` | Returns a session's most recent messages, preferring the persistent store. |
| `(*Manager).Prune` | Removes a session and its persisted state (memory + store). |
| `(*Manager).StartIdleEviction` | Daemon-only background sweep: drops idle entries from the in-memory cache when a store is attached; no-op otherwise. |

Package-level sentinel errors: None declared at the package level (errors are returned inline via `fmt.Errorf`).

## Dependencies

**Fan-out:** none (leaf).

**Fan-in:**

- **`cmd/stem`** — wires the Manager into the CLI, REST, and MCP surfaces; `cmdserve` alone calls `StartIdleEviction`.
- **`internal/core`** — uses sessions to bind execution context and preferences to sprouts.
- **`internal/historydb`** — implements the `Store` interface for SQLite persistence (dependency inversion).
- **`internal/receptors`** — interacts with sessions for incoming WebSocket and REST traffic.

## Limitations

- **In-memory vs persisted state:** The in-memory per-session message buffer is hardcoded to 200 messages (`memoryHistoryCap`) when no persistent store is attached. If a store is attached, it prefers loading messages from the store.
- **Idle-session cache eviction:** When a `Store` is attached, `StartIdleEviction` periodically (every 5 minutes) drops entries from `m.sessions` whose `LastActiveAt` is older than `TENDRIL_SESSION_TTL` (Go duration string, default `24h`; invalid values fall back to the default with a logged warning). Eviction removes from memory only — never from the store. `Get` / `GetOrInitiate` rehydrate via `LoadSession` on a cache miss; `List` sources from `LoadSessions` so idle-but-persisted sessions remain visible. No-store Managers (ephemeral CLI one-shots) never evict, because there is nowhere to reload from. Only the long-lived daemon (`cmdserve`) starts the sweep.
- **Concurrency:** `Manager` uses a single global `sync.RWMutex` to protect the map of sessions, which could become a contention point under heavy concurrent load.
- **Preference-merge edge cases:** `Preferences.Merge` copies map values for `Extras` but zero-value overrides (e.g. an empty string for Model) are ignored, meaning it cannot "clear" an existing preference, only overwrite it or inherit.
- **ID validation:** `ValidID` enforces an alphanumeric start and limited special characters (`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`). `NewID` mints random 12-byte hex IDs, or falls back to a Unix timestamp nano if crypto/rand fails, making collisions practically impossible. `Initiate` guards against one anyway: it regenerates the candidate ID under the same lock up to `maxSessionIDCollisionRetries` times rather than overwriting an existing session, and fails closed with an error if retries are exhausted.

## Design & rationale

The Phytomer model provides a unified context for interaction, regardless of the entry point (CLI, REST, MCP). The `Store` interface keeps `session.go` a fan-out 0 leaf, inverting the dependency so `internal/historydb` can provide SQLite persistence without creating a cyclic dependency. `NewID` creates unique IDs prefixed with `tendril-` using cryptographic randomness, falling back to Unix timestamps if the random reader fails. With a store attached, durability is the source of truth and the in-memory map is a resume-friendly cache; without a store, the map remains the sole authority for the process lifetime.
