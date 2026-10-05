# Greenhouse

The **Greenhouse** (the Command Center UI) is the operator-facing frontend of
OpenTendril. Where
[ARCHITECTURE.md §5](../ARCHITECTURE.md) describes the persistent, multi-session
Go Stem *daemon* (the unified `SessionManager`, the `.tendril/history.db` state
layer, and the pluggable EventBus), this document describes the **decoupled web
client** that turns that daemon into a single, living dashboard, and the
Stem-side API contracts the client depends on.

- **Where it lives:** [`ui/`](../ui/) (React 18 + Vite + TypeScript).
- **How to run / build it, the component tree, and the full event → visual
  mapping:** [`ui/README.md`](../ui/README.md).
- **Design intent:** deeply biological, dark-mode-first. The
  Rhizome/Sprout/Tendril taxonomy is the actual visual language: each
  orchestration grows as a plant whose branches, tendril tips, and phenotype
  arenas mutate as EventBus telemetry streams in.

---

## 1. How it fits the OpenTendril picture

```
   Operator's browser
         │
         │  Greenhouse (ui/, static React app)
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
UI to ship, version, and deploy independently of the Stem binary. The run
drill-down renders the Stem's structured observation fields as the primary
explanation of a run; it does not parse raw error strings to decide a failure
category, failure stage, or diagnostic code. It renders those typed backend
values directly and does not derive a stage or code from raw errors, event text,
or provider messages. Opening a run replaces the garden visualization with
that review: observation facts, the task transcript, and tool activity sit
above the fold. Historical rows without stage or code show neither field.
Raw Event Pulse and terrarium output stay collapsed until opened.

The current Greenhouse uses three Stem capabilities:

1. **Unified `SessionManager`**: `GET /v1/phytomers` lists every live Tendril
   regardless of which surface (CLI, MCP, REST, WS) sprouted it, so the operator
   sees the whole fleet in one rail.
2. **`history.db` persistence**: the per-session history endpoints let the UI
   re-hydrate its entire state after a browser refresh instead of starting blank.
3. **EventBus over `/ws`**: the live telemetry stream that drives the botanical
   visualization in real time.

---

## 2. Installed lifecycle and Botanist-key handoff

Greenhouse is optional and separate from the Stem. The governed installer
places the installed image and `/usr/local/sbin/opentendril-greenhouse` wrapper
without requiring an OpenTendril repository checkout or Compose invocation.
Use explicit administrator authority for lifecycle calls:

```bash
sudo /usr/local/sbin/opentendril-greenhouse start
sudo /usr/local/sbin/opentendril-greenhouse stop
sudo /usr/local/sbin/opentendril-greenhouse restart
sudo /usr/local/sbin/opentendril-greenhouse status
sudo /usr/local/sbin/opentendril-greenhouse check
sudo /usr/local/sbin/opentendril-greenhouse address
```

The normal address is `http://127.0.0.1:4173`. `start` and `restart` require
the Stem Unix socket at `/var/lib/opentendril-transport/stem.sock`. Installed
Greenhouse uses Unix Stem transport only, with no normal TCP fallback. The
browser-facing address binds to loopback only. `stop` affects Greenhouse only
and does not stop the Stem. No Greenhouse-specific sudoers or `NOPASSWD` grant
is installed. Repository Compose instructions below are development/reference
material rather than the installed first-use path.

The Stem always requires a non-empty Botanist key. It resolves an explicit
`BOTANIST_KEY` first, then an existing persisted key, then generates and
persists a key if neither exists. In this governed installation, a generated
or reused persisted key is at `/home/tendril/.tendril/api-key`. An administrator
deliberately obtains that value from the Stem account:

```bash
sudo -u tendril -H cat /home/tendril/.tendril/api-key
```

The Botanist explicitly copies the value into Greenhouse onboarding. If
`BOTANIST_KEY` was explicitly configured, the administrator hands off that
configured value instead of the file. This is the Botanist credential, not a
Pollinator credential. Ordinary filesystem access to `/home/tendril` is not
granted. The key is not mounted into Greenhouse or injected through Docker
environment, and this handoff does not use or introduce a secret API/helper.

Onboarding rejects empty and whitespace-only input. It first checks reachability
with `/health`, then verifies the non-empty Botanist key with an authenticated
session-list request before entering. An invalid key is rejected. The Stem Unix
socket is transport reachability, not authentication. After successful
onboarding, the browser persists the current connection state, including the
key, in `localStorage` under `opentendril.connection`. REST requests send
`Authorization: Bearer ...`; the browser WebSocket uses the existing
authenticated `?key=` query mechanism because the browser WebSocket API cannot
attach an Authorization header.

The ordered installation-to-Fruit path is in
[GUIDE-QUICKSTART.md](./GUIDE-QUICKSTART.md#first-governed-work-through-greenhouse).

## 3. REST surface consumed by the Greenhouse

The Stem serves `/health` for reachability. Protected API routes are served on
its API port (default `:8080`) and require the Botanist bearer. The Stem always
has a non-empty bearer: explicit `BOTANIST_KEY`, then an existing persisted
key, then a generated and persisted key at `/home/tendril/.tendril/api-key` in
this governed installation. Handlers:
`cmd/stem/internal/api/sessions.go`.

The canonical path is `/v1/phytomers` (a session is a Phytomer). The legacy
`/v1/sessions` path is still mounted as an alias for existing clients; the
`sessionId` field and path parameter are unchanged.

| Method & path | Used for |
| --- | --- |
| `GET /health` | Onboarding reachability check. |
| `GET /v1/phytomers` | Canonical Phytomer list route. |
| `GET /v1/sessions` | Existing session alias used by onboarding to validate the Botanist key and by the current Work rail. |
| `POST /v1/phytomers` | Creates a Phytomer session for other clients. Greenhouse normal work does not call it. |
| `PATCH /v1/phytomers/{id}` | Update a session's preferences (model, genotype, substrate, …). |
| `DELETE /v1/phytomers/{id}` | Prune a session. |
| `GET /v1/phytomers/{id}/history` | Historical message hydration. Greenhouse does not send new work through chat completions. |
| `GET /v1/phytomers/{id}/sprout-runs` | The canonical per-Phytomer Sprout-run list. Each `SproutRun` carries status plus the structured observation fields: `provider`, `model`, `outcome`, `failureCategory`, `failureStage`, `diagnosticCode`, `providerDiagnostic`, `providerRequestAttempted`, `toolInvocations`, `terrariumProvider`, and the existing usage envelope. `terrariumProvider` is the recorded provider that actually created the Sprout's Terrarium. It is absent for historical runs where that fact was not recorded. |
| `GET /v1/phytomers/{id}/events` | Persisted EventBus telemetry for garden re-growth and Sprout-run review evidence. |
| `GET /v1/config/substrates` | Named Substrates from `substrates.yaml`, for the new-work Substrate select. |
| `POST /v1/seeds/grow` | Detached Seed dispatch for normal work. The body carries `substrate`, `goal`, `detached: true`, `origin` `rest`, and one opaque `idempotencyKey`; normal Greenhouse work omits `verify`. A successful response returns `handle`, `phytomerId`, and `status`. Greenhouse does not create a Phytomer before this call. |
| `GET /v1/seeds/runs/{handle}` | Durable Seed record used to reconstruct the task. The recognisable label is the record's `goal`. |
| `GET /v1/phytomers/{id}/watch` | Authenticated Server-Sent Events for one Phytomer. The Botanist bearer stays on the `Authorization` header. Greenhouse applies `observation` frames as current state and surfaces `error` frames. A 404 means the Phytomer is not Seed-owned, and historical Sprout observation stays in place. A response that is not an event stream, and observation data that cannot be read, stay watch errors. The workbench shows that failure. Historical Sprout runs already read from the Stem remain in the run list. |
| `POST /v1/phytomers/{id}/continue` | Continued intent for that same Phytomer while the Seed status is `running`. A Stem rejection stays a rejection. Greenhouse does not start a replacement Seed. |
| `GET /v1/fruit` | Deterministic Botanist Fruit inventory. For Seed Fruit, Greenhouse matches `producerKind` `seed` and `producerIdentity` equal to the Seed handle, and checks `phytomerId` when both sides have one. Review state comes only from that item. |
| `POST /v1/chat/completions` | Still served by the Stem for other clients. The Greenhouse normal-work path does not call it. |
| `GET /v1/delegation/pending` | Open, unexpired pending confirmations. The body is a JSON array. Each item has `id`, `pollen`, `operationClass`, `substrate`, `impact`, `createdAt`, and `expiresAt`. |
| `POST /v1/delegation/pending/{id}/approve` | Records Botanist approval of that confirmation. A success body is `{ "status": "approved", "id" }`. Approval does not resume or execute the delegated operation. |
| `POST /v1/delegation/pending/{id}/deny` | Records Botanist denial of that confirmation. A success body is `{ "status": "denied", "id" }`. |

The `…/events` and `…/sprout-runs` endpoints return `501 Not Implemented` when
`TENDRIL_DB_LOGGING=false`, since there is no persistent store to read from.

The SessionRail provides bounded cross-Phytomer Sprout-run discovery. On boot
and reconnect, Greenhouse fetches canonical run records for up to the 10 most
recently active Phytomers. The active Phytomer still receives normal full
session hydration for its messages, runs, and persisted events; if it is
outside that recent set, its run list is fetched as part of that active-session
hydration. Sprout lifecycle events on `/ws` trigger an authoritative
`/sprout-runs` refresh and serve only as invalidation signals, not as run state.
Selecting a discovered run fetches current persisted `/events` evidence from
the Stem before its review is marked complete. The active Phytomer and workbench
remain unchanged when reviewing a run from another Phytomer. Continuation stays
on the running Seed, not on the Sprout run open for review.

The left rail is headed Work. It lists observed Phytomers and the Sprout runs discovered for them. It does not offer a separate Sprout button, and the Botanist does not create an empty Phytomer before meaningful work. Normal new work starts from the Workbench. An empty rail says that no work has been observed yet, and that new work starts in the Workbench while activity from CLI, MCP, and REST still appears here. The preferred label is the durable Seed goal, then the persisted Sprout transcript, then Phytomer. The opaque Phytomer id stays secondary. Reviewing a historical Sprout does not retarget active Seed continuation.

There is no pending-confirmation EventBus event. While Greenhouse is open it reads `GET /v1/delegation/pending` on one repeating timer, every 5 seconds, and also during initial hydration and EventBus reconnect hydration. Those reads share one in-flight list request. A failed read keeps the previous list and is reported on the attention surface. It does not fail Phytomer, Seed, Fruit, run, Garden, or EventBus hydration, and it does not clear the list. The list is not written to browser storage. When the list is empty and the attention surface has no error, Greenhouse does not show an attention badge.

Approve and Deny call the Botanist routes above through the same bearer as the rest of the client. The row stays visible until a list read that starts after the POST has settled returns a new list. Greenhouse does not remove a row because the POST succeeded, because the browser clock is past `expiresAt`, or because an EventBus frame arrived. `404` and `409` stay unsuccessful: missing, expired, or no longer open, using the Stem response text when it distinguishes those cases. A POST that succeeds while the following list read fails keeps the previous list and says the action response was received but the list could not be reconciled. A later list read may replace that list. It does not turn an uncertain or failed action into approval or denial. Nothing in this surface calls a resume route or starts a Seed or Sprout.

Normal work selects a configured Substrate and accepts a task, then dispatches
detached Seed work without a `verify` field. Greenhouse does not collect a
verifier executable or argv, synthesize a command, or request command
verification. The Stem returns the canonical Phytomer and Greenhouse follows
its current observation. The Botanist uses Botanist authority; no Pollinator
credential or DelegationGrant is required. The Workbench presents Seed lifecycle
status, execution outcome, verification outcome, iteration count, latest Sprout
state, recorded Terrarium provider, per-iteration verification diagnostics,
structured Sprout failures, publication state and diagnostics, and Fruit
provenance and review state as separate facts. `not-requested` is shown as a
verification outcome, not as `passed` or as a judgement about the task. An empty
outcome on a historical record remains unknown. `settled` is presented as a
terminal lifecycle status, not as success or failure, and Mycorrhizal output is
not proof that the task succeeded. The Workbench permits continuation only while
the Seed remains running. It reads deterministic Fruit inventory and shows the
reported repository, branch, commit, and review state when available. If the
Stem reports no Fruit provenance, Greenhouse says so rather than inferring Fruit
from other evidence. Internal identifiers remain in technical details.
Greenhouse does not infer Fruit from branch names or commit text, or merge Fruit
automatically. The default branch remains unchanged until the Botanist separately
reviews and accepts/merges the Fruit.

Normal work is `Substrate + task -> detached Seed -> canonical Phytomer -> current-state watch -> optional continuation -> terminal Seed -> Fruit review`.
The workbench shows the Seed goal, configured Substrate, Seed lifecycle status,
execution outcome, verification outcome, iteration count, latest Sprout state,
the recorded `terrariumProvider` when present, per-iteration verification
diagnostics, and structured failure evidence. Host execution is labeled as
bypassing Terrarium isolation because that is the recorded provider contract.
An absent `terrariumProvider` stays unknown. Internal ids, provider diagnostics,
the unified diff, and Seed logs stay behind technical details.

If Seed dispatch has a transport failure, Greenhouse keeps the original request and idempotency key, refreshes Phytomer state from the Stem, and retries only that same request. A malformed successful response also keeps that request. Greenhouse retains one unresolved retry identity in browser session storage and restores it on boot before a new Seed can be dispatched. A valid success clears it and selects the returned canonical Phytomer; an explicit HTTP rejection clears it too. Another transport failure or malformed success leaves it in place. Reload restores the identity but does not resend the Seed request. Seed lifecycle state still comes from Stem observation and the Seed collection.

---

## 4. WebSocket surface: the EventBus gateway (`/ws`)

`/ws` requires the same bearer key as the REST surface. Native
WebSocket clients (e.g. the CLI's gorilla/websocket dialer) send it as an
`Authorization: Bearer <key>` header on the upgrade request; browsers cannot
attach custom headers to a WebSocket handshake, so the Greenhouse instead
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
  "data": { /* event payload */ }
}
```

The registered event types are defined in
`cmd/stem/internal/eventbus/eventbus.go` (`AllEventTypes()`), and the gateway
subscribes a handler for every one of them. The UI's event → botanical-visual
mapping for each type is tabulated in [`ui/README.md`](../ui/README.md).

### 4.1 `?replay=N`: recent-history replay (public contract)

`/ws` accepts an **opt-in** `replay` query parameter:

```
ws://<stem>/ws?replay=100
```

When present, immediately after the `connected` frame the gateway replays up to
`N` events from the bus's in-memory history window (capped at 100, the bus's
`maxHistory`) before the live feed begins. Without the parameter, behavior is
unchanged: no replay.

**Why it exists:** the per-session `…/events` REST endpoint can only return
events that carry a `sessionId`. Orchestration telemetry emitted by the sequence
runner, parallel-sprouting, and phenotypic-selection is **session-less** (it is
keyed by sequence/step, not session), so a client that refreshes mid-sequence
cannot recover that timeline from REST alone. `?replay=N` lets a reconnecting
client re-grow session-less sequence state from the bus's recent history. The
Greenhouse requests `replay=100` on every connect.

Replay is lossy by design (it only reaches back as far as the bus's 100-event
in-memory window) and is a best-effort supplement to REST hydration, not a
guaranteed-complete event log. The durable log remains `history.db`.

---

## 5. Repository Compose reference deployment

For repository development and reference deployment, the Greenhouse is a
**separate, optional, isolated, containerized
component**: a hardened nginx container (built by
[`ui/Dockerfile`](../ui/Dockerfile), configured by
[`ui/nginx/default.conf.template`](../ui/nginx/default.conf.template)) that
serves the static bundle **and** reverse-proxies the Stem's documented API
surface, giving the browser a single origin. The Stem itself stays **on the
host and headless**: it never serves the UI, and the system is fully
operable with this container absent.

The Compose Unix-transport reference uses the Stem's authenticated Unix-domain socket.
`--profile ui` bind-mounts only `/var/lib/opentendril-transport` into the
container, read-only, and does not create that path if it is missing. The
Greenhouse does not use host networking and does not need
`host.docker.internal`.

```
   Operator's browser ── single origin, e.g. http://127.0.0.1:4173
         │
         ▼
   ┌──────────────────────────────────────────────────────────────────┐
   │   ui container (nginx, non-root, read-only)                      │
   │     /            → static ui/dist bundle                         │
   │     /health /v1* → unix:/var/lib/opentendril-transport/stem.sock  │
   │     /ws          → unix:/var/lib/opentendril-transport/stem.sock  │
   └──────────────────────────────────────────────────────────────────┘
         │  read-only /var/lib/opentendril-transport
         ▼
   Unified Go Stem (host daemon: headless, loopback TCP unchanged)
```

- **Opt-in:** the `ui` compose service sits behind the `ui` profile and never
  starts unless `--profile ui` is passed. One command brings it up alongside
  the host Stem: `docker compose --profile ui up -d`.
- **Local Unix transport:** `/health`, `/v1*`, and `/ws` go through
  `/var/lib/opentendril-transport/stem.sock`: the same authenticated mux the
  Stem already serves on loopback TCP. Socket reachability is not
  authorization.
- **Single origin, no CORS:** the browser only talks to the container, so the
  Stem needs no CORS headers.
  In development, Vite's proxy plays the same role via `STEM_TARGET`.
- **Auth preserved:** the proxy forwards the Botanist bearer untouched; the
  Stem remains the authority. Only `/health`, `/v1*`, and `/ws` are proxied;
  nothing else on the host is reachable. The Greenhouse container does not
  hold the key. The browser stores the configured connection, including the
  key, in `localStorage` and presents it to the Stem.
- **WebSocket upgrade:** the `/ws` proxy speaks HTTP/1.1 with
  `Upgrade`/`Connection` headers against the same Unix socket. Explicit TCP
  mode (`--profile ui-tcp`) still prefers the dedicated gateway listener
  (`:9090`) and falls back to the main API mux (`:8080`), mirroring the
  Stem's own graceful gateway-bind degradation (§4).
- **Hardened:** non-root image (`nginx-unprivileged`), read-only root
  filesystem, all capabilities dropped, `no-new-privileges`, loopback-only
  port binding by default. The only host bind-mount on `--profile ui` is
  `/var/lib/opentendril-transport` (read-only); `--profile ui-tcp` has none.
  `/home/tendril`, Botanist key files, Pollinator credentials,
  grants, the protected `tendril` executable, and the Stem's rootless-Docker
  socket are not mounted. The CSP locks `script-src` to `'self'` (no inline
  scripts, no `eval`) and splits `style-src` so `<style>` tags/stylesheets
  are `'self'`-only (`style-src-elem`) while only React's inline `style=""`
  attributes keep `'unsafe-inline'` (`style-src-attr`): an XSS payload can
  no longer inject an arbitrary `<style>` element for CSS-based
  exfiltration or UI redress.
- **Explicit TCP mode:** `STEM_HOST=stem.example docker compose --profile ui-tcp up -d`
  selects TCP only. That profile pins `STEM_TRANSPORT=tcp`, requires
  `STEM_HOST` at container start, and mounts no
  `/var/lib/opentendril-transport` bind. A missing local socket on
  `--profile ui` is a transport failure; it does not select TCP.
See [`ui/README.md`](../ui/README.md) for commands, configuration variables,
and the manual static-build alternative.
