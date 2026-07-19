# OpenTendril Command Center (`ui/`)

The **Command Center** is the visual operator UI for the Tendril OS — a
single, desktop-grade dashboard an operator leaves open all day to watch every
Tendril the Go Stem is growing. It replaces the fragmented "one CLI terminal
per run" workflow with one living view where live orchestration activity is
legible at a glance and nothing is lost on refresh.

It is a **strictly decoupled client.** It talks to the Stem only over the
documented REST + WebSocket surface (see
[`docs/COMMAND-CENTER.md`](../docs/COMMAND-CENTER.md) for the full contract) and
has zero coupling to Go internals.

---

## Running it

The UI is a static React app built with Vite. It has no server of its own.

### Development

The Stem sets no CORS headers, so in development Vite proxies the REST and
WebSocket surfaces through its own origin. Point `STEM_TARGET` at your running
Stem (defaults to `http://localhost:8080`):

```bash
cd ui
npm install
STEM_TARGET=http://localhost:8080 npm run dev   # http://localhost:5173
```

The proxy (see [`vite.config.ts`](./vite.config.ts)) forwards `/v1`, `/health`,
and `/ws` (with WebSocket upgrade) to the Stem, so the browser makes only
same-origin requests.

### Production — the containerized UI front (recommended)

The supported deployment is the **optional, isolated, containerized UI front**
(issue #160): a hardened nginx container that serves the built bundle **and**
reverse-proxies `/v1`, `/health`, and `/ws` (with WebSocket upgrade) to the
**host** Stem via `host.docker.internal`. The browser sees a **single origin**,
so no CORS configuration exists anywhere and the Stem stays headless (#158).
Docker is already a core dependency (Tendrils sprout into containerized
substrates), so this adds no new requirement — and no local Node/npm is needed;
the image builds the bundle itself in a multi-stage build.

```bash
docker compose --profile ui up -d     # from the repo root
# → http://127.0.0.1:4173
```

The service is **opt-in**: without `--profile ui` it never starts, and the
system is 100% operable without it (CLI / MCP / OpenAPI are capability-parity
peers, #159). Configuration knobs (all optional, via environment):

| Variable | Default | Meaning |
| --- | --- | --- |
| `UI_BIND` | `127.0.0.1` | Host interface to publish on. Loopback-only by default; set `0.0.0.0` for LAN access. |
| `UI_PORT` | `4173` | Host port for the UI front. |
| `STEM_HOST` | `host.docker.internal` | Where the container finds the Stem. |
| `STEM_PORT` | `8080` | The Stem's main API port. |
| `STEM_GATEWAY_PORT` | `9090` | The dedicated `/ws` gateway listener; the proxy falls back to `STEM_PORT` automatically if it is down. |

**Security posture:** the proxy adds no credentials and bypasses nothing — the
operator's `Authorization: Bearer` key passes through untouched and the Stem's
`withAPIKeyAuth` remains the sole authority. Only `/health`, `/v1*`, and `/ws`
are proxied; nothing else on the host is reachable. The container runs as a
non-root user with a read-only root filesystem, all capabilities dropped, and
`no-new-privileges`. Future server-side layers (BFF, auth, enterprise SSO, the
#164 concierge) grow **inside this component** — never in the Stem.

With the container in front, the operator leaves the **Stem address** blank in
onboarding (same origin) and enters only the operator API key.

### Manual static build (alternative)

```bash
npm run build     # type-checks with tsc, emits static assets to ui/dist/
```

`ui/dist/` is a self-contained static bundle. Serve it from the **same origin
as the Stem's API surface** (i.e. behind your own reverse proxy fronting both),
so `/v1`, `/health`, and `/ws` resolve without cross-origin requests — the Stem
sets no CORS headers by design. A full Stem URL in onboarding is only for a
cross-origin Stem that has been configured to allow it.

### Testing

```bash
npm run test:e2e   # builds ui/dist, serves it via `vite preview`, runs Playwright
```

`tests/e2e.spec.ts` is a foundational Playwright suite covering onboarding,
the `/ws` EventBus connection, and session-rail rendering. It mocks the Go
Stem entirely at the network layer — HTTP via `page.route`, `/ws` via
`page.routeWebSocket` — so it needs no Docker, no real Stem, and no LLM
provider, and runs identically in CI. Playwright lives in `devDependencies`
only; `ui/Dockerfile`'s production stage copies just `dist/`, never
`node_modules`, so it never reaches the shipped image.

### Onboarding (no `.env`)

On first load a welcome screen asks a non-technical operator for the Stem
address and the operator API key (`OPENTENDRIL_API_KEY`). The key is validated
live — `/health` confirms the Stem is reachable, `/v1/sessions` confirms the
key is accepted — then persisted to `localStorage`. No `.env` editing, and the
key never leaves the browser. "Uproot" (top-right) clears it and returns to
onboarding. Provider LLM keys remain server-side by design; the UI does not
manage them.

---

## Stack & key decisions

| Choice | Rationale |
| --- | --- |
| **React 18 + Vite + TypeScript** | Vite over Next because this is a pure client of an existing API — there is no SSR surface. TypeScript because every wire shape is mirrored from the Go structs and the compiler enforces the match. |
| **Zustand** for state | The whole app is one WebSocket-driven event fold. A small external store fits that far better than context/reducer prop-plumbing, and lets non-React modules (the socket client) dispatch into state directly. |
| **Hand-written SVG + CSS animation** for the visualization | No charting or animation dependency. The botanical scene is bespoke — a generic chart library cannot express "a withering tendril tip" — and SVG keeps the whole thing declarative, themeable via CSS custom properties, and light (≈58 KB gzipped total bundle). |
| **A pure reducer for the garden** | `applyGardenEvent(state, event)` is a pure fold. Live WebSocket events, replayed persisted history rows, and `/ws?replay=` frames all drive the *identical* code path, which is what makes refresh-resilient hydration possible (see below). |

---

## Component tree

```
App                         gates on stored connection settings
├─ Onboarding               welcome screen; validates /health + /v1/sessions, persists to localStorage
└─ CommandCenter            app shell; boots the store (WS + hydration), renders the grid
   ├─ SessionRail           left: session list from GET /v1/sessions; switch/create Tendrils
   ├─ GardenCanvas          center: the living visualization (the centerpiece)
   │  └─ PlantFigure        one orchestration = one plant (stem, branches, tendril tips)
   │     └─ SelectionArena  a phenotypic-selection step = an arena of competing phenotype pods
   ├─ EventTicker           center-bottom: raw EventBus "pulse" feed
   ├─ ChatPanel             right: per-session chat (/v1/chat/completions) + sprout-runs list
   └─ DrilldownDrawer       overlay: raw terrarium output, genotype, fitness, timings for one run
```

Supporting modules:

- `lib/types.ts` — TypeScript shapes mirrored **field-for-field** from the Go
  structs (`session.go`, `historydb.go`, `gateway.go`, `eventbus.go`). Nothing
  here is invented; every field matches what the backend emits.
- `lib/api.ts` — thin typed REST client; attaches the bearer key; builds the
  `/ws` URL (with the `?replay=100` hydration parameter).
- `lib/ws.ts` — resilient WebSocket client with capped exponential-backoff
  reconnect and lifecycle-status callbacks.
- `state/connection.ts` — onboarding settings, persisted to `localStorage`.
- `state/garden.ts` — the pure garden reducer (the event → visual fold).
- `state/store.ts` — the central store: hydration orchestration, sessions,
  messages, runs, ticker, chat, and drilldown.
- `styles/global.css` — the design system (deep-loam dark theme, glassmorphism,
  the Rhizome/Sprout/Tendril colour language) driven by CSS custom properties.

---

## State-management model

One Zustand store (`state/store.ts`) is the single source of truth. It holds:

- `sessions`, `activeSessionId` — the Tendril rail and current context.
- `messagesBySession`, `runsBySession`, `eventsBySession` — per-session data
  hydrated lazily from REST when a session is selected.
- `garden` — the botanical scene graph, produced *only* by folding events
  through `applyGardenEvent`.
- `ticker` — a bounded rolling window of recent events for the Event Pulse.
- `wsStatus`, `hydration` — connection and hydration lifecycle flags that drive
  the status badge and the "re-growing state" pill.

The garden is deliberately **derived, never hand-mutated**: every visual change
is the result of an event passing through the reducer. This is what lets the
same code render a live feed, a cold history replay, and a reconnect replay
without special cases.

---

## Refresh-resilient hydration flow

The contract is: **on refresh or reconnect, no state is lost and there is no
flash of empty.** The order matters, and it is enforced in `state/store.ts`:

1. **Open the WebSocket first and buffer.** `boot()` connects the socket
   immediately. While hydration is in flight, incoming live events are pushed
   into an in-memory `liveBuffer` instead of being applied — so nothing that
   arrives mid-hydration is dropped.
2. **Hydrate cold state from REST underneath.** `GET /v1/sessions`, then for the
   active session `…/history` (chat), `…/sprout-runs` (executions), and
   `…/events` (persisted telemetry). Previously rendered state is never cleared
   while this runs.
3. **Re-grow the garden from persisted events**, oldest-first, through the same
   `applyGardenEvent` reducer. On a fresh connect the socket also requests
   `/ws?replay=100`, which prepends the bus's recent in-memory history — this
   recovers *session-less* sequence telemetry (parallel-sprouting, mycelial
   merge, phenotypic-selection) that the per-session REST events endpoint cannot
   return because those events carry no session id.
4. **Flush the buffer on top, then go live.** The buffered live events are
   applied over the hydrated state and the store switches to pass-through mode.

The transition is a *merge*, not a reset. A refresh mid-orchestration re-grows
the garden from history and picks the live feed back up with no visible seam;
a small "re-growing state from history" pill is shown instead of a blank shell.
On WebSocket drop, the client reconnects with backoff and re-runs the same
hydrate-then-go-live sequence, so anything that happened while disconnected is
recovered.

---

## EventBus event → botanical-visual mapping

Each orchestration renders as one **plant**. A plant is keyed by sequence name
(`seq:<name>`) when the event carries one, else by session (`ses:<id>`), else by
its source step for agent-only streams (an ambient plant). Each step id is a
**branch** off the stem; each parallel sprout is a **tendril tip** on that
branch; a phenotypic-selection step grows a **selection arena** of competing
pods. Payload field names below match the Go publishers exactly.

| Event (`type`) | Key payload fields | Visual mutation |
| --- | --- | --- |
| `parallel-sprouting` | `phase:"map"`, `sproutCount` | Branch unfurls with `sproutCount` dashed **buds** awaiting sprouts. |
| `sprout-emerged` | `sproutIndex`, `branchName`, `detail` | A **tendril tip** grows in (stroke-dash draw), lit chlorophyll-green. |
| `sprout-matured` | `sproutIndex`, `branchName` | The tip **blooms** — gold bud with a glow. |
| `sprout-withered` | `sproutIndex`, `branchName`, `detail` | The tip **desaturates to umber, droops, and fades** (never removed — the loss stays visible). |
| `mycelial-merge` | `phase:"reduce"`, `maturedCount`, `witheredCount` | Glowing **filaments run from matured tips back into the branch node**, which pulses (consensus being grown). |
| `mycelial-merge` | `phase:"complete"` | The node **consolidates** (spore-blue, steady glow). |
| `phenotypic-selection` | `phase:"start"`, `populationSize`, `maxGenerations`, `fitnessGoal` | A **selection arena** ring appears at the branch tip. |
| `phenotypic-selection` | `phase:"generation"`, `generation`, `population` | A fresh generation of **phenotype pods germinate** around the ring; the ring pulses. |
| `phenotypic-selection` | `phase:"evaluated"`, `survivors`, `withered`, `bestScore`, `alphaScore`, `alphaBranch` | The fittest **`survivors` pods stay lit green; the rest compost** (fall and fade). Best score labelled. |
| `phenotypic-selection` | `phase:"complete"`, `alphaBranch`, `alphaScore` | The **AlphaPhenotype crowns gold** (bright glow) with its fitness score. |
| `stream-token` | `source=stepId`, `data.type` `stream.start`/`stream.end` | A **sap shimmer** flows up the active branch/stem while tokens stream; `stream.end` clears it. |
| `thought-branch` | `thought` | An ephemeral **whisper** tooltip fades in and out over the plant. |
| `sequence-complete` | `sequence` | The plant **fruits** — crown blooms gold, marked matured. |
| `sequence-failure` | `stepId`, `error` | The branch **scorches** red; the plant is marked withered with the failure detail. |
| `health-*`, `terrarium-oom`, `terrarium-timeout`, `api-key-invalid`, `rhizome-update`, `xylem-transport`, `hormonal-trigger` | — | **Ambient weather.** These refresh the plant's liveness and surface in the Event Pulse ticker; they do not grow dedicated structures. |

---

## A note on file casing

The repo's Go/Python rule is "merged lowercase, no underscores or hyphens". The
`ui/` tree instead follows the **standard React/TypeScript convention**:
`PascalCase.tsx` for components (`GardenCanvas.tsx`), `camelCase.ts` for modules
(`garden.ts`), and a `PascalCase/` folder for a component family (`Garden/`).
This is a deliberate, tooling-idiomatic exception scoped to `ui/` — do not
"correct" these filenames to merged-lowercase. JSON payload keys remain
`camelCase` and API paths remain `kebab-case`, matching the repo boundary rules.
```
