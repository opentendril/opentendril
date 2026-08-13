# Component: HistoryDB — the Chronicler: a single-file SQLite persistence layer that durably records sessions, chat logs, EventBus telemetry, and Sprout/Seed execution histories.

## Purpose

`cmd/stem/internal/historydb` is the Go Stem's durable state backbone. It persists the unified organism state to a lightweight local SQLite database at `.tendril/history.db` using the CGO-free `modernc.org/sqlite` driver, so the `tendril` binary stays purely portable. Everything the Greenhouse must survive a browser refresh lives here: Tendril sessions and their preferences, unified chat logs, all EventBus telemetry, Sprout execution histories, and bounded `seed.grow` runs.

The whole package is a single file (`historydb.go`). It plays two roles at once through Go's dependency-inversion: it **implements** `session.Store` (the port that `session` defines) so the `SessionManager` can persist without importing SQLite, and it **implements** `eventbus.Sink` so every published event lands in the database on the bus's own goroutine. Setting `TENDRIL_DB_LOGGING=false` bypasses SQLite entirely for high-performance headless runs.

## Responsibilities

**Does:**

- Open (creating if needed) the SQLite database at `.tendril/history.db`, or the `TENDRIL_DB_PATH` override, and initialize the schema idempotently (`Open` / `OpenFromEnv` / `initSchema`).
- Apply durability pragmas on open: `journal_mode = WAL`, `synchronous = NORMAL`, `busy_timeout = 5000` (`initSchema`).
- Persist and resume Tendril sessions with their preferences as upserts, keyed by `sessionId` (`SaveSession` / `LoadSessions` / `DeleteSession`).
- Append and load the unified chat log, newest-N-in-order per session (`AppendMessage` / `LoadMessages`).
- Act as the EventBus persistence sink: every published event is encoded and written to the `events` table (`Consume` / `RecordEvent` / `LoadEvents`).
- Persist Sprout execution history as a lifecycle upsert — once when the Sprout emerges (`running`) and again when it matures or withers — recording the dispatching Pollen and the substrate the work targeted so a read can be scoped to the subject that owns it (`RecordSproutRun` / `LoadSproutRuns`).
- Report the distinct subjects that dispatched the Sprout runs recorded against one phytomer, each paired with the substrate that run targeted, without loading or decrypting any run content (`SproutRunOwners`).
- Persist `seed.grow` bounded-task runs keyed by the durable `handle` a Pollinator collects against, recording the dispatching Pollen so collection can be scoped, plus the reviewable Fruit — status, iterations, branch, diff, logs (`RecordSeedRun` / `GetSeedRun`).
- Honor the persistence toggle: `OpenFromEnv` returns `(nil, nil)` when logging is disabled so callers run fully headless without touching disk (`LoggingEnabled` / `OpenFromEnv`).
- Encrypt payload columns at rest (`messages.content`, `sessions.preferences`, `sproutruns.transcript`/`output`/`error`/`genotype`, `seedruns.goal`/`diff`/`logs`/`error`, `events.data`) via AES-GCM using `heartwood` and the key shared with `rhizome` (`.tendril/rhizome.key` or `OPEN_TENDRIL_INDEX_KEY`). Structural/index columns stay plaintext for queries. `events.data` is redact-then-encrypt (`telemetry.RedactEvent` before sealing). Opt-out is available via `TENDRIL_ENCRYPT_AT_REST`. Note: The Tier-1 auto-key is defense-in-depth, not a boundary against a full disk read (see DESIGN-HEARTWOOD.md).

**Does not:**

- Own any CLI, REST, MCP, or WebSocket wiring — surfaces in `cmd/stem` open the store and attach it; this package is storage only.
- Automatically prune, expire, vacuum, or cap growth by default. Age-based retention exists but is disabled by default (opt-in via `TENDRIL_HISTORY_RETENTION_DAYS`); without it, the `events` table grows with every published event.
- Migrate a schema backwards or across a version it does not recognise. A version row (`schemaMeta`) fails closed against a newer-than-supported version, and `migrateSchema` applies forward steps only.
- Block the orchestrator hot path — as an EventBus sink, persistence failures are counted and logged sparsely, never propagated back to `Publish`.
- Define or resolve session/event semantics — it stores the `session` and `eventbus` types verbatim and translates none of them.

## Public interface

| Symbol | Role |
| --- | --- |
| `Store` | SQLite-backed history database; implements `session.Store` and `eventbus.Sink`. Fields: `db`, `path`, `eventErrors`. |
| `SproutRun` | One Sprout execution record (runId, session/step/origin, model, genotype, transcript, status, output, error, timestamps). |
| `SeedRun` | One `seed.grow` bounded-task record: handle, dispatching Pollen, substrate, goal, status, iterations, and the Fruit (branch, diff, logs, error, timestamps). |
| `EventRecord` | One persisted EventBus telemetry row (id, sessionId, type, source, data map, createdAt). |
| `EnvDBLogging` / `EnvDBPath` | Env-var constants: persistence toggle and database-path override. |
| `LoggingEnabled` | Reports whether SQLite persistence is on; `false`/`0`/`off`/`no`/`disabled` (case-insensitive) disable it, anything else enables it. |
| `DefaultPath` | Standard location `<root>/.tendril/history.db` (empty root becomes `.`). |
| `OpenFromEnv` | Open honoring the env toggles; returns `(nil, nil)` when logging is disabled. |
| `Open` | Open (creating dirs) at an explicit path; sets `MaxOpenConns(1)` and runs `initSchema`. |
| `Close` / `Path` | Release the handle (nil-safe); report the file location. |
| `SaveSession` / `DeleteSession` / `LoadSessions` | `session.Store` session persistence (upsert / cascade delete of session + messages / newest-active-first load). |
| `AppendMessage` / `LoadMessages` | `session.Store` chat-log persistence (append / newest-N returned oldest-first; default limit 50). |
| `Consume` | `eventbus.Sink` entry point; delegates to `RecordEvent`, counting and log-sampling failures (1 in 100). |
| `RecordEvent` / `LoadEvents` | Write one telemetry event / load recent rows, optionally filtered by session (default limit 100). |
| `RecordSproutRun` / `LoadSproutRuns` | Upsert a Sprout run by `runId` / load recent, optionally by session (default limit 50). Ownership is settled by the first write and never reassigned. |
| `SproutRunOwners` | Distinct `{Pollen, Substrate}` pairings recorded against one phytomer's Sprout runs; an empty result means nothing was ever dispatched into it. |
| `RecordSeedRun` / `GetSeedRun` | Upsert a seed run by `handle` / fetch one by handle (returns `found bool`; a missing handle is not an error). |
| `PruneOlderThan` | Delete rows from messages, events, sproutruns, and seedruns older than a cutoff, then `VACUUM` if any were deleted. |
| `StartRetentionSweep` | Launch a background loop that prunes rows older than `TENDRIL_HISTORY_RETENTION_DAYS`. |

**Sentinel errors:** none. The package exports no error values; validation returns dynamically formatted errors (for example, `RecordSproutRun` requires `runId`, `RecordSeedRun` requires a non-empty `handle`, `GetSeedRun` requires a handle), and all storage failures are wrapped with `fmt.Errorf(... %w ...)`. A not-found `GetSeedRun` is signalled by a `false` boolean rather than a sentinel.

## Dependencies

**Fan-out (4):**

- **`internal/eventbus`** — imported for `eventbus.Event` and the `eventbus.Sink` contract. `Store` **implements** `Sink` via `Consume(event)`, so it can be attached to a `Bus` with `AttachSink`; each published event is persisted to the `events` table on the bus's dedicated sink goroutine. Because the bus drops on a full buffer and this sink swallows errors, telemetry persistence is lossy by design and never back-pressures `Publish`.
- **`internal/session`** — imported for `session.Phytomer` and `session.Message`. `Store` **implements** the `session.Store` interface (`SaveSession`, `DeleteSession`, `LoadSessions`, `AppendMessage`, `LoadMessages`). This is the dependency inversion: `session` owns the port and knows nothing of SQLite; `historydb` is the adapter, and a `nil` store keeps the `Manager` fully in-memory.
- **`internal/heartwood`** — imported for the `Cipher` to perform at-rest encryption of payload columns.
- **`internal/telemetry`** — imported for `RedactEvent` and `RedactionDisabled` to apply redact-then-encrypt to `events.data`.

Beyond OpenTendril internals, the only import is the `modernc.org/sqlite` driver (blank-imported) plus the standard library — no CGO.

**Fan-in (3):**

- **`cmd/stem`** — `cmdserve.go` calls `OpenFromEnv`, attaches the store as an EventBus sink (`AttachSink(history, 0)`), and passes it as the `session.Store` into `session.NewManager`; when the open fails or logging is off it degrades to a nil store and logs "Persistent state disabled". `handleChatCompletions` records a `SproutRun`. The CLI entry points (`cmdsession.go`, `cmdsprout.go`, `cmdmcp.go`, `clidelegation.go`) each `OpenFromEnv` for their own read/write access, all nil-tolerant.
- **`internal/receptors`** — the `Seed`, `Sprout`, `Sessions`, and `MCP` handlers hold a `*historydb.Store` (wired via `WithHistory` / constructors) and record run lifecycles (`RecordSeedRun`, `RecordSproutRun`). Every call site is nil-guarded (`if h.history != nil`), so a headless (no-store) deployment simply skips recording.
- **`internal/core`** — does **not** import `historydb`. The only reference is `core/boundary_test.go`, which enumerates `historydb` in `forbiddenCoreImports`: the transport- and execution-free Core must never pull in the persistence internals. The test is the fan-in — it actively keeps the dependency arrow pointing away from Core.

## Limitations

- **`TENDRIL_DB_LOGGING=false` disables all persistence.** `OpenFromEnv` returns `(nil, nil)`, so the `SessionManager` runs in-memory and receptor record calls short-circuit on their nil guard. Nothing is written and nothing survives a restart. This is the intended fail-safe posture: an open failure in `cmdserve.go` is also downgraded to a nil store with a warning rather than aborting startup — the Stem always keeps running, persistence is best-effort.
- **Single-writer concurrency ceiling.** `Open` sets `db.SetMaxOpenConns(1)`; the CGO-free driver serializes access per connection, and WAL plus `busy_timeout = 5000` avoids `SQLITE_BUSY` under the concurrent gateway surfaces. All writes funnel through one connection — adequate for a local Stem, not a high-concurrency multi-writer store.
- **Unbounded growth by default.** By default there is no retention, pruning, expiry, or vacuum. Every published event is appended to `events`, and transcripts/diffs/logs are stored in full. Opt-in age-based retention can be enabled via the `TENDRIL_HISTORY_RETENTION_DAYS` environment variable, which prunes messages, events, sproutruns, and seedruns (deliberately excluding `sessions`). **Security tradeoff:** Enabling retention prunes audit-relevant events (e.g. delegation-authorized/denied, host-execution-activated) along with everything else — there is no per-event-type carve-out. Operators who need long-term audit retention should leave retention disabled or set a suitably long window. Additionally, pruning triggers a `VACUUM` which briefly holds the single DB connection (`SetMaxOpenConns(1)`), momentarily blocking other concurrent history writes on a large database.
- **Encryption scope is payload-only.** Structural and index columns stay plaintext for queries. The Tier-1 auto-key (`.tendril/rhizome.key`) is defense-in-depth, not a boundary against a full `.tendril` read; only `OPEN_TENDRIL_INDEX_KEY` provides a real control (see DESIGN-HEARTWOOD.md). Encryption can be bypassed by setting `TENDRIL_ENCRYPT_AT_REST=false`.
- **Migration steps are hand-written and unversioned individually.** Schema creation is `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`, and `migrateSchema` then runs an ordered list of idempotent forward steps. `ensureColumn` inspects `pragma_table_info` before it alters, because SQLite has no `ADD COLUMN IF NOT EXISTS` and re-running a plain `ALTER` is an error — that one check collapses a fresh database, a pre-versioning database, and a database stamped at an earlier generation onto the same path. The cost is that each step is a hand-written statement rather than a numbered migration file, so the ordering constraint (an index may not name a column an earlier step has yet to add) is enforced by reading rather than by tooling.
- **Lossy telemetry, silent by design.** `Consume` never returns an error; failures increment `eventErrors` and log only on every 100th failure. Combined with the bus dropping events on a full sink buffer, some telemetry can be lost without a hard signal.
- **No exported sentinel errors.** Callers cannot match on typed errors; a missing `seed.grow` handle is disambiguated only by `GetSeedRun`'s `found` boolean.
- **Store/Manager integration is thin on tests.** The package's own round-trip tests exercise each table directly, but the `session.Store`-through-`Manager` path is covered elsewhere only on the nil-store branch (see Findings / open issue reconciliation).

## Design & rationale

**Why SQLite, embedded and CGO-free.** The Stem ships as a single portable binary; the `modernc.org/sqlite` pure-Go driver keeps it that way while still giving durable, queryable, transactional local state. A single file under `.tendril/` is trivial to locate, back up, or delete, and it means the Greenhouse never loses state on a browser refresh. WAL journaling with `synchronous = NORMAL` trades a sliver of crash durability for throughput appropriate to a local developer tool, and the single-connection pool sidesteps the multi-writer contention SQLite handles poorly.

**Why the `session.Store` inversion.** Persistence is a policy the `session` package should not know about. `session` declares the `Store` port and works against it; `historydb` is one adapter that satisfies it. The payoff is concrete: `NewManager(ctx, nil)` yields a fully functional in-memory manager for headless and test runs, and swapping SQLite for another backend touches no `session` code. The same inversion is what lets `TENDRIL_DB_LOGGING=false` collapse cleanly to a nil store rather than threading a "disabled" flag through the manager.

**Why the event-sink model.** Telemetry persistence must never slow the orchestrator. Rather than have producers write to the database, `historydb` attaches to the `eventbus.Bus` as one `Sink` among many (local persistence sits alongside remote Redis/WebSocket transporters). The bus fans each event out to per-sink buffered channels drained on dedicated goroutines; a slow or failing persistence sink drops events for itself only and can never block `Publish`. That is why `Consume` deliberately swallows and merely samples errors — correctness of the hot path is worth more than guaranteed telemetry durability.

**Why upsert-by-key for runs.** Sprout and seed runs are recorded twice — at dispatch (`running`) and at settlement — using `ON CONFLICT ... DO UPDATE`. Keying Sprout runs on `runId` and seed runs on the collectible `handle` makes the record idempotent and lets an async dispatcher update terminal state (the reviewable Fruit) against the same row a Pollinator later collects by handle. Both kinds record the dispatching Pollen so a read stays scoped to its owner.

**Why ownership is settled once.** The dispatching Pollen and substrate are written by whichever call first supplies them and are never reassigned: `pollen = CASE WHEN pollen = '' THEN excluded.pollen ELSE pollen END`. A run that already names a subject keeps it, so no later write can hand a recorded run to a different one, while a row that arrived without a subject can still acquire it. Runs recorded before the columns existed stay unowned rather than being guessed at, which denies a delegated read rather than granting it.

**Why the sidecar was removed.** The redundant plaintext `.tendril/history/<chatID>.json` chat sidecar was removed. Chat runs now persist solely through `RecordSproutRun` and the session store, keeping all historical data unified inside SQLite under the at-rest encryption umbrella.
