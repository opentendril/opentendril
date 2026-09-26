// Central command-center store.
//
// Hydration contract (the "no flash of empty" rule): on boot or reconnect we
// (1) open the WebSocket immediately and buffer whatever it emits,
// (2) hydrate cold state from REST (/v1/sessions + per-session history),
// (3) replay persisted event records into the garden, then flush the live
//     buffer on top, then switch to pass-through live mode.
// Previously rendered state is never cleared while this runs — the swap is a
// merge, so a refresh mid-orchestration re-grows the garden from history and
// picks up the live feed without a visible seam.

import { create } from "zustand";
import { currentConnection } from "./connection";
import {
  stemApi,
  websocketUrl,
  StemApiError,
  NotSeedWatchError,
  isAbortError,
} from "../lib/api";
import { newIdempotencyKey } from "../lib/idempotency";
import { seedStatusIsTerminal } from "../lib/seed";
import { StemSocket, type WsStatus } from "../lib/ws";
import {
  applyGardenEvent,
  emptyGarden,
  type GardenState,
} from "./garden";
import type {
  ChatMessage,
  ContinuationRequest,
  EventRecord,
  FruitInventory,
  PhytomerObservation,
  Preferences,
  SeedGrowRequest,
  SeedRun,
  Session,
  SproutRun,
  StemEvent,
} from "../lib/types";

const TICKER_LIMIT = 60;
const ACTIVE_SESSION_KEY = "opentendril.activeSession";
const RECENT_SESSION_LIMIT = 10;
const MAX_BACKGROUND_RUN_REQUESTS = 3;
const RUN_REFRESH_DEBOUNCE_MS = 250;
const SESSION_REFRESH_DEBOUNCE_MS = 750;

export type Hydration = "idle" | "hydrating" | "ready" | "error";
export type EvidenceState = "loading" | "ready" | "error";

export interface DrilldownTarget {
  run: SproutRun;
  events: EventRecord[];
  evidenceState: EvidenceState;
  evidenceError?: string;
}

export type SeedDispatchPhase = "idle" | "pending" | "ambiguous" | "rejected";

export interface SeedDispatchState {
  phase: SeedDispatchPhase;
  request?: SeedGrowRequest;
  message?: string;
}

export type PhytomerWatchPhase =
  | "idle"
  | "connecting"
  | "open"
  | "terminal"
  | "not-seed"
  | "error";

export interface PhytomerWatchState {
  phase: PhytomerWatchPhase;
  error?: string;
}

export type ContinuationPhase = "idle" | "pending" | "rejected" | "ambiguous";

export interface ContinuationState {
  phase: ContinuationPhase;
  phytomerId?: string;
  intent?: string;
  idempotencyKey?: string;
  message?: string;
}

export type FruitLoadStatus = "idle" | "loading" | "ready" | "error";

export interface SeedWorkInput {
  substrate: string;
  goal: string;
  verify: string[];
  maxIterations?: number;
  timeoutSeconds?: number;
}

interface StemStore {
  wsStatus: WsStatus;
  hydration: Hydration;
  hydrationError: string | null;

  sessions: Session[];
  activeSessionId: string | null;
  messagesBySession: Record<string, ChatMessage[]>;
  runsBySession: Record<string, SproutRun[]>;
  eventsBySession: Record<string, EventRecord[]>;
  eventsStatusBySession: Record<string, EvidenceState>;
  garden: GardenState;
  ticker: StemEvent[];
  drilldown: DrilldownTarget | null;
  configuredSubstrates: string[];
  seedDispatch: SeedDispatchState;
  seedRunByPhytomer: Record<string, SeedRun>;
  seedCollectErrorByPhytomer: Record<string, string>;
  knownSeedHandleByPhytomer: Record<string, string>;
  observationByPhytomer: Record<string, PhytomerObservation>;
  watchByPhytomer: Record<string, PhytomerWatchState>;
  continuation: ContinuationState;
  fruitInventory: FruitInventory | null;
  fruitStatus: FruitLoadStatus;
  fruitError: string | null;

  boot: () => void;
  shutdown: () => void;
  selectSession: (sessionId: string) => void;
  createSession: (preferences?: Preferences) => Promise<void>;
  updatePreferences: (preferences: Preferences) => Promise<void>;
  startSeed: (input: SeedWorkInput) => Promise<void>;
  retrySeedDispatch: () => Promise<void>;
  discardUncertainDispatch: () => void;
  continueSeed: (intent: string) => Promise<void>;
  retryContinuation: () => Promise<void>;
  openDrilldown: (run: SproutRun) => void;
  closeDrilldown: () => void;
}

function recordToStemEvent(record: EventRecord): StemEvent {
  return {
    type: record.type,
    timestamp: record.createdAt,
    source: record.source,
    sessionId: record.sessionId || undefined,
    data: record.data,
  };
}

/** Per-token stream frames are too chatty for the ticker; keep boundaries. */
function tickerWorthy(event: StemEvent): boolean {
  if (event.type === "connected") return false;
  if (event.type === "stream-token") {
    const kind = event.data?.["type"];
    return kind === "stream.start" || kind === "stream.end";
  }
  return true;
}

let socket: StemSocket | null = null;
let liveBuffer: StemEvent[] | null = null; // non-null while hydrating
let sessionRefreshTimer: number | null = null;
let pendingSessionRefreshIds = new Set<string>();
let runRefreshTimers = new Map<string, number>();
let activeRunRequests = 0;
let runRequestQueue: Array<() => void> = [];
let drilldownRequestId = 0;
let watchAbort: AbortController | null = null;
let watchPhytomerId: string | null = null;
let watchInFlight = false;
let fruitRequestId = 0;

const IDLE_DISPATCH: SeedDispatchState = { phase: "idle" };
const IDLE_CONTINUATION: ContinuationState = { phase: "idle" };

function emptyFruitCounts(): FruitInventory["counts"] {
  return {
    outstanding: 0,
    unknown: 0,
    closedUnmerged: 0,
    merged: 0,
    total: 0,
  };
}

function recentSessions(sessions: Session[]): Session[] {
  return [...sessions]
    .sort((a, b) => {
      const aTime = Date.parse(a.lastActiveAt);
      const bTime = Date.parse(b.lastActiveAt);
      const safeATime = Number.isFinite(aTime) ? aTime : Number.NEGATIVE_INFINITY;
      const safeBTime = Number.isFinite(bTime) ? bTime : Number.NEGATIVE_INFINITY;
      if (safeATime !== safeBTime) return safeBTime - safeATime;
      return a.sessionId.localeCompare(b.sessionId);
    })
    .slice(0, RECENT_SESSION_LIMIT);
}

function withRunRequestLimit<T>(request: () => Promise<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const start = () => {
      activeRunRequests += 1;
      void (async () => {
        try {
          resolve(await request());
        } catch (error) {
          reject(error);
        } finally {
          activeRunRequests -= 1;
          runRequestQueue.shift()?.();
        }
      })();
    };

    if (activeRunRequests < MAX_BACKGROUND_RUN_REQUESTS) start();
    else runRequestQueue.push(start);
  });
}

function correlatedEvents(run: SproutRun, events: EventRecord[]): EventRecord[] {
  return events.filter(
    (event) =>
      (run.stepId && event.source === run.stepId) ||
      (run.stepId && event.data?.["stepId"] === run.stepId),
  );
}

function mergeEventRecords(
  persisted: EventRecord[],
  current: EventRecord[],
): EventRecord[] {
  const byId = new Map<number, EventRecord>();
  for (const event of persisted) byId.set(event.id, event);
  for (const event of current) byId.set(event.id, event);
  return [...byId.values()].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
}

function isSproutLifecycleEvent(type: string): boolean {
  return (
    type === "sprout-emerged" ||
    type === "sprout-matured" ||
    type === "sprout-withered"
  );
}

export const useStem = create<StemStore>()((set, get) => {
  function foldEvent(event: StemEvent) {
    set((state) => {
      const next: Partial<StemStore> = {
        garden: applyGardenEvent(state.garden, event),
      };
      if (tickerWorthy(event)) {
        next.ticker = [...state.ticker.slice(-(TICKER_LIMIT - 1)), event];
      }
      if (event.sessionId && state.eventsBySession[event.sessionId]) {
        next.eventsBySession = {
          ...state.eventsBySession,
          [event.sessionId]: [
            ...state.eventsBySession[event.sessionId],
            {
              id: Date.now() + Math.random(),
              sessionId: event.sessionId,
              type: event.type,
              source: event.source,
              data: event.data,
              createdAt: event.timestamp ?? new Date().toISOString(),
            },
          ],
        };
      }
      return next;
    });

    if (!event.sessionId) return;

    const knownSession = get().sessions.some(
      (session) => session.sessionId === event.sessionId,
    );
    if (!knownSession) {
      scheduleSessionRefresh(event.sessionId);
    } else if (isSproutLifecycleEvent(event.type)) {
      scheduleRunRefresh(event.sessionId);
    }
  }

  function scheduleSessionRefresh(sessionId: string) {
    pendingSessionRefreshIds.add(sessionId);
    if (sessionRefreshTimer !== null) return;
    sessionRefreshTimer = window.setTimeout(async () => {
      sessionRefreshTimer = null;
      const refreshIds = [...pendingSessionRefreshIds];
      pendingSessionRefreshIds.clear();
      try {
        const { sessions } = await stemApi.listSessions(currentConnection());
        set({ sessions });
        await Promise.all(refreshIds.map((id) => hydrateSessionRuns(id)));
      } catch {
        // transient; the next event will retry
      }
    }, SESSION_REFRESH_DEBOUNCE_MS);
  }

  function scheduleRunRefresh(sessionId: string) {
    const pending = runRefreshTimers.get(sessionId);
    if (pending !== undefined) window.clearTimeout(pending);
    const timer = window.setTimeout(() => {
      runRefreshTimers.delete(sessionId);
      void hydrateSessionRuns(sessionId);
    }, RUN_REFRESH_DEBOUNCE_MS);
    runRefreshTimers.set(sessionId, timer);
  }

  function onLiveEvent(event: StemEvent) {
    if (liveBuffer) {
      liveBuffer.push(event);
      return;
    }
    foldEvent(event);
  }

  async function fetchSessionRuns(sessionId: string): Promise<SproutRun[]> {
    const response = await withRunRequestLimit(() =>
      stemApi.sproutRuns(currentConnection(), sessionId),
    );
    return (response.sproutRuns ?? []).map((run) => ({
      ...run,
      sessionId: run.sessionId ?? sessionId,
    }));
  }

  async function hydrateSessionRuns(sessionId: string): Promise<void> {
    try {
      const runs = await fetchSessionRuns(sessionId);
      set((state) => ({
        runsBySession: { ...state.runsBySession, [sessionId]: runs },
      }));
    } catch {
      // A later refresh or session selection can retry this observation read.
    }
  }

  async function hydrateRecentRuns(sessions: Session[]): Promise<void> {
    await Promise.all(sessions.map((session) => hydrateSessionRuns(session.sessionId)));
  }

  async function hydrateSessionData(
    sessionId: string,
    includeRuns = true,
  ): Promise<EventRecord[]> {
    const conn = currentConnection();
    set((state) => ({
      eventsStatusBySession: {
        ...state.eventsStatusBySession,
        [sessionId]: "loading",
      },
    }));
    const [messages, runs, events] = await Promise.all([
      stemApi.messages(conn, sessionId).catch(() => ({ messages: [] as ChatMessage[] })),
      includeRuns
        ? fetchSessionRuns(sessionId).catch(() => null)
        : Promise.resolve(null),
      stemApi.events(conn, sessionId).catch(() => null),
    ]);
    const persistedEvents = events?.events ?? [];
    set((state) => {
      const next: Partial<StemStore> = {
        messagesBySession: {
          ...state.messagesBySession,
          [sessionId]: messages.messages ?? [],
        },
        eventsStatusBySession: {
          ...state.eventsStatusBySession,
          [sessionId]: events ? "ready" : "error",
        },
      };
      if (runs) {
        next.runsBySession = { ...state.runsBySession, [sessionId]: runs };
      }
      if (events) {
        next.eventsBySession = {
          ...state.eventsBySession,
          [sessionId]: mergeEventRecords(
            persistedEvents,
            state.eventsBySession[sessionId] ?? [],
          ),
        };
      }
      return next;
    });
    return persistedEvents;
  }

  function stillWatching(phytomerId: string, controller: AbortController): boolean {
    return watchPhytomerId === phytomerId && watchAbort === controller;
  }

  function stopWatch() {
    watchAbort?.abort();
    watchAbort = null;
    watchPhytomerId = null;
    watchInFlight = false;
  }

  async function collectSeedIntoStore(handle: string, phytomerId: string) {
    try {
      const run = await stemApi.collectSeed(currentConnection(), handle);
      const id = run.phytomerId || phytomerId;
      set((state) => {
        const errors = { ...state.seedCollectErrorByPhytomer };
        delete errors[phytomerId];
        delete errors[id];
        return {
          seedRunByPhytomer: { ...state.seedRunByPhytomer, [id]: run },
          seedCollectErrorByPhytomer: errors,
        };
      });
      if (seedStatusIsTerminal(run.status)) void loadFruitInventory();
    } catch (err) {
      const message =
        err instanceof StemApiError ? `${err.status}: ${err.message}` : String(err);
      set((state) => ({
        seedCollectErrorByPhytomer: {
          ...state.seedCollectErrorByPhytomer,
          [phytomerId]: message,
        },
      }));
    }
  }

  async function loadFruitInventory() {
    const requestId = ++fruitRequestId;
    set({ fruitStatus: "loading", fruitError: null });
    try {
      const inventory = await stemApi.fruitInventory(currentConnection());
      if (requestId !== fruitRequestId) return;
      set({
        fruitInventory: {
          items: inventory.items ?? [],
          counts: inventory.counts ?? emptyFruitCounts(),
        },
        fruitStatus: "ready",
        fruitError: null,
      });
    } catch (err) {
      if (requestId !== fruitRequestId) return;
      set({
        fruitStatus: "error",
        fruitError:
          err instanceof StemApiError ? `${err.status}: ${err.message}` : String(err),
      });
    }
  }

  function applyObservation(phytomerId: string, observation: PhytomerObservation) {
    set((state) => ({
      observationByPhytomer: {
        ...state.observationByPhytomer,
        [phytomerId]: observation,
      },
      watchByPhytomer: {
        ...state.watchByPhytomer,
        [phytomerId]: {
          phase: seedStatusIsTerminal(observation.status) ? "terminal" : "open",
        },
      },
    }));
    if (observation.handle) void collectSeedIntoStore(observation.handle, phytomerId);
    if (seedStatusIsTerminal(observation.status)) void loadFruitInventory();
  }

  async function runWatch(phytomerId: string, controller: AbortController) {
    watchInFlight = true;
    let terminal = false;
    try {
      await stemApi.watchPhytomer(
        currentConnection(),
        phytomerId,
        controller.signal,
        (observation) => {
          if (!stillWatching(phytomerId, controller)) return;
          applyObservation(phytomerId, observation);
          if (seedStatusIsTerminal(observation.status)) {
            terminal = true;
            controller.abort();
          }
        },
      );
      if (!stillWatching(phytomerId, controller) || controller.signal.aborted) return;
      if (terminal || seedStatusIsTerminal(get().observationByPhytomer[phytomerId]?.status)) {
        set((state) => ({
          watchByPhytomer: {
            ...state.watchByPhytomer,
            [phytomerId]: { phase: "terminal" },
          },
        }));
      }
    } catch (err) {
      if (!stillWatching(phytomerId, controller) || controller.signal.aborted || isAbortError(err)) {
        return;
      }
      if (err instanceof NotSeedWatchError || (err instanceof StemApiError && err.status === 404)) {
        set((state) => ({
          watchByPhytomer: {
            ...state.watchByPhytomer,
            [phytomerId]: { phase: "not-seed" },
          },
        }));
        return;
      }
      const message = err instanceof StemApiError
        ? `Phytomer watch failed (${err.status}): ${err.message}`
        : err instanceof Error
          ? err.message
          : String(err);
      set((state) => ({
        watchByPhytomer: {
          ...state.watchByPhytomer,
          [phytomerId]: { phase: "error", error: message },
        },
      }));
    } finally {
      if (watchAbort === controller) watchInFlight = false;
    }
  }

  function ensureWatch(phytomerId: string) {
    if (!phytomerId) {
      stopWatch();
      return;
    }
    if (
      watchPhytomerId === phytomerId &&
      watchInFlight &&
      watchAbort &&
      !watchAbort.signal.aborted
    ) {
      return;
    }
    stopWatch();
    const controller = new AbortController();
    watchAbort = controller;
    watchPhytomerId = phytomerId;
    watchInFlight = true;
    set((state) => ({
      watchByPhytomer: {
        ...state.watchByPhytomer,
        [phytomerId]: { phase: "connecting" },
      },
    }));
    void runWatch(phytomerId, controller);
  }

  function seedStatusFor(phytomerId: string): string {
    return (
      get().observationByPhytomer[phytomerId]?.status ||
      get().seedRunByPhytomer[phytomerId]?.status ||
      ""
    );
  }

  function sameAmbiguousRequest(request: SeedGrowRequest): boolean {
    const current = get().seedDispatch;
    return (
      current.phase === "ambiguous" &&
      current.request?.idempotencyKey === request.idempotencyKey
    );
  }

  async function probePhytomer(phytomerId: string): Promise<PhytomerObservation | null> {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 4000);
    let observed: PhytomerObservation | null = null;
    try {
      await stemApi.watchPhytomer(
        currentConnection(),
        phytomerId,
        controller.signal,
        (observation) => {
          observed = observation;
          controller.abort();
        },
      );
    } catch (err) {
      if (observed) return observed;
      if (isAbortError(err) || err instanceof NotSeedWatchError) return null;
      if (err instanceof StemApiError && err.status === 404) return null;
      return null;
    } finally {
      window.clearTimeout(timer);
    }
    return observed;
  }

  async function hydrateAmbiguousDispatch(request: SeedGrowRequest, beforeIds: Set<string>) {
    let sessions: Session[] = [];
    try {
      const listed = await stemApi.listSessions(currentConnection());
      sessions = listed.sessions ?? [];
      set({ sessions });
    } catch {
      return;
    }
    if (!sameAmbiguousRequest(request)) return;

    const matches: string[] = [];
    for (const session of sessions) {
      if (beforeIds.has(session.sessionId)) continue;
      if (!sameAmbiguousRequest(request)) return;
      const observed = await probePhytomer(session.sessionId);
      if (!observed?.handle) continue;
      try {
        const run = await stemApi.collectSeed(currentConnection(), observed.handle);
        const goal = (run.goal ?? "").trim();
        const substrate = (run.substrate ?? "").trim();
        if (goal !== request.goal.trim() || substrate !== request.substrate.trim()) continue;
        const id = run.phytomerId || session.sessionId;
        set((state) => ({
          seedRunByPhytomer: { ...state.seedRunByPhytomer, [id]: run },
          observationByPhytomer: {
            ...state.observationByPhytomer,
            [session.sessionId]: observed,
          },
          knownSeedHandleByPhytomer: {
            ...state.knownSeedHandleByPhytomer,
            [id]: run.handle,
          },
        }));
        matches.push(id);
      } catch {
        // The original idempotency key stays available for an explicit retry.
      }
    }
    if (!sameAmbiguousRequest(request) || matches.length !== 1) return;
    set({ seedDispatch: IDLE_DISPATCH });
    get().selectSession(matches[0]);
  }

  async function markAmbiguousDispatch(
    request: SeedGrowRequest,
    beforeIds: Set<string>,
    message: string,
  ) {
    set({ seedDispatch: { phase: "ambiguous", request, message } });
    await hydrateAmbiguousDispatch(request, beforeIds);
  }

  async function dispatchPrepared(request: SeedGrowRequest, beforeIds: Set<string>) {
    try {
      const result = await stemApi.growSeed(currentConnection(), request);
      if (!result?.handle || !result.phytomerId || !result.status) {
        await markAmbiguousDispatch(
          request,
          beforeIds,
          "The dispatch outcome is uncertain. The Stem response did not include a Seed handle and Phytomer.",
        );
        return;
      }
      set((state) => ({
        seedDispatch: IDLE_DISPATCH,
        knownSeedHandleByPhytomer: {
          ...state.knownSeedHandleByPhytomer,
          [result.phytomerId]: result.handle,
        },
      }));
      try {
        const listed = await stemApi.listSessions(currentConnection());
        set({ sessions: listed.sessions ?? [] });
      } catch {
        // The dispatch identity is already known. A later hydration can refresh the rail.
      }
      void collectSeedIntoStore(result.handle, result.phytomerId);
      get().selectSession(result.phytomerId);
    } catch (err) {
      if (isAbortError(err)) {
        set({ seedDispatch: IDLE_DISPATCH });
        return;
      }
      if (err instanceof StemApiError) {
        set({
          seedDispatch: {
            phase: "rejected",
            message: `Seed dispatch rejected (${err.status}): ${err.message}`,
          },
        });
        return;
      }
      await markAmbiguousDispatch(
        request,
        beforeIds,
        "The dispatch outcome is uncertain. The Stem may have accepted this Seed. Retry uses this same request.",
      );
    }
  }

  async function submitContinuation(intent: string, existingKey: string | null) {
    const phytomerId = get().activeSessionId;
    if (!phytomerId || seedStatusFor(phytomerId) !== "running") return;
    const trimmed = intent.trim();
    if (!trimmed || get().continuation.phase === "pending") return;
    const idempotencyKey = existingKey ?? newIdempotencyKey();
    const request: ContinuationRequest = {
      intent: trimmed,
      idempotencyKey,
      sessionId: phytomerId,
    };
    set({
      continuation: {
        phase: "pending",
        phytomerId,
        intent: trimmed,
        idempotencyKey,
      },
    });
    try {
      await stemApi.continuePhytomer(currentConnection(), phytomerId, request);
      if (get().continuation.idempotencyKey !== idempotencyKey) return;
      set({ continuation: IDLE_CONTINUATION });
    } catch (err) {
      if (isAbortError(err)) return;
      if (get().continuation.idempotencyKey !== idempotencyKey) return;
      if (err instanceof StemApiError) {
        set({
          continuation: {
            phase: "rejected",
            phytomerId,
            message: `Continuation rejected (${err.status}): ${err.message}`,
          },
        });
        return;
      }
      set({
        continuation: {
          phase: "ambiguous",
          phytomerId,
          intent: trimmed,
          idempotencyKey,
          message:
            "The continuation outcome is uncertain. Retry uses this same request.",
        },
      });
    }
  }

  async function hydrate() {
    set({ hydration: "hydrating", hydrationError: null });
    liveBuffer = [];
    try {
      const conn = currentConnection();
      const [{ sessions }, substrates] = await Promise.all([
        stemApi.listSessions(conn),
        stemApi.substrates(conn).catch(() => ({ substrates: [] as string[] })),
      ]);
      set({ configuredSubstrates: substrates.substrates ?? [] });

      const stored = window.localStorage.getItem(ACTIVE_SESSION_KEY);
      const active =
        (stored && sessions.some((s) => s.sessionId === stored) && stored) ||
        sessions[0]?.sessionId ||
        null;

      set({ sessions, activeSessionId: active });

      const recent = recentSessions(sessions);
      const recentIds = new Set(recent.map((session) => session.sessionId));
      let persistedEvents: EventRecord[] = [];
      const activeTask = active
        ? hydrateSessionData(active, !recentIds.has(active)).then((events) => {
            persistedEvents = events;
          })
        : Promise.resolve();
      await Promise.all([activeTask, hydrateRecentRuns(recent)]);

      // Re-grow the garden from persisted telemetry, oldest first, then splice
      // in everything the socket buffered while we were reading history.
      let garden = get().garden;
      if (Object.keys(garden.plants).length === 0) {
        garden = persistedEvents.reduce(
          (acc, record) => applyGardenEvent(acc, recordToStemEvent(record)),
          emptyGarden,
        );
      }
      const buffered = liveBuffer ?? [];
      liveBuffer = null;
      set({ garden, hydration: "ready" });
      buffered.forEach(foldEvent);
      if (active) ensureWatch(active);
      else stopWatch();
    } catch (err) {
      liveBuffer = null;
      set({
        hydration: "error",
        hydrationError:
          err instanceof StemApiError
            ? `${err.status}: ${err.message}`
            : String(err),
      });
    }
  }

  return {
    wsStatus: "closed",
    hydration: "idle",
    hydrationError: null,
    sessions: [],
    activeSessionId: null,
    messagesBySession: {},
    runsBySession: {},
    eventsBySession: {},
    eventsStatusBySession: {},
    garden: emptyGarden,
    ticker: [],
    drilldown: null,
    configuredSubstrates: [],
    seedDispatch: IDLE_DISPATCH,
    seedRunByPhytomer: {},
    seedCollectErrorByPhytomer: {},
    knownSeedHandleByPhytomer: {},
    observationByPhytomer: {},
    watchByPhytomer: {},
    continuation: IDLE_CONTINUATION,
    fruitInventory: null,
    fruitStatus: "idle",
    fruitError: null,

    boot: () => {
      socket?.close();
      socket = new StemSocket(websocketUrl(currentConnection()), {
        onEvent: onLiveEvent,
        onStatus: (status) => {
          const previous = get().wsStatus;
          set({ wsStatus: status });
          // Fresh connection (initial or after a drop): re-hydrate so nothing
          // that happened while we were away is missing, then go live.
          if (status === "open" && previous !== "open") {
            void hydrate();
          }
        },
      });
      socket.connect();
      // If the socket cannot open (Stem down), still try REST once so the
      // operator sees history rather than a blank shell.
      window.setTimeout(() => {
        if (get().hydration === "idle") void hydrate();
      }, 2500);
    },

    shutdown: () => {
      stopWatch();
      socket?.close();
      socket = null;
      liveBuffer = null;
      if (sessionRefreshTimer !== null) {
        window.clearTimeout(sessionRefreshTimer);
        sessionRefreshTimer = null;
      }
      pendingSessionRefreshIds.clear();
      for (const timer of runRefreshTimers.values()) window.clearTimeout(timer);
      runRefreshTimers.clear();
    },

    selectSession: (sessionId) => {
      window.localStorage.setItem(ACTIVE_SESSION_KEY, sessionId);
      drilldownRequestId += 1;
      set({ activeSessionId: sessionId, drilldown: null });
      void hydrateSessionData(sessionId);
      ensureWatch(sessionId);
    },

    createSession: async (preferences = {}) => {
      const session = await stemApi.createSession(currentConnection(), preferences);
      set((state) => ({
        sessions: [session, ...state.sessions],
        messagesBySession: {
          ...state.messagesBySession,
          [session.sessionId]: [],
        },
        runsBySession: { ...state.runsBySession, [session.sessionId]: [] },
        eventsBySession: { ...state.eventsBySession, [session.sessionId]: [] },
        eventsStatusBySession: {
          ...state.eventsStatusBySession,
          [session.sessionId]: "ready",
        },
      }));
      get().selectSession(session.sessionId);
    },

    updatePreferences: async (preferences) => {
      const sessionId = get().activeSessionId;
      if (!sessionId) return;
      const session = await stemApi.updatePreferences(
        currentConnection(),
        sessionId,
        preferences,
      );
      set((state) => ({
        sessions: state.sessions.map((existing) =>
          existing.sessionId === sessionId ? session : existing,
        ),
      }));
    },

    startSeed: async (input) => {
      const phase = get().seedDispatch.phase;
      if (phase === "pending" || phase === "ambiguous") return;
      const request: SeedGrowRequest = {
        substrate: input.substrate,
        goal: input.goal,
        verify: [...input.verify],
        origin: "rest",
        detached: true,
        idempotencyKey: newIdempotencyKey(),
      };
      if (input.maxIterations !== undefined) request.maxIterations = input.maxIterations;
      if (input.timeoutSeconds !== undefined) request.timeoutSeconds = input.timeoutSeconds;
      const beforeIds = new Set(get().sessions.map((session) => session.sessionId));
      set({ seedDispatch: { phase: "pending", request } });
      await dispatchPrepared(request, beforeIds);
    },

    retrySeedDispatch: async () => {
      const current = get().seedDispatch;
      if (current.phase !== "ambiguous" || !current.request) return;
      const beforeIds = new Set(get().sessions.map((session) => session.sessionId));
      set({ seedDispatch: { phase: "pending", request: current.request } });
      await dispatchPrepared(current.request, beforeIds);
    },

    discardUncertainDispatch: () => {
      if (get().seedDispatch.phase !== "ambiguous") return;
      set({ seedDispatch: IDLE_DISPATCH });
    },

    continueSeed: async (intent) => {
      await submitContinuation(intent, null);
    },

    retryContinuation: async () => {
      const current = get().continuation;
      if (
        current.phase !== "ambiguous" ||
        !current.intent ||
        !current.idempotencyKey ||
        !current.phytomerId
      ) {
        return;
      }
      if (get().activeSessionId !== current.phytomerId) return;
      await submitContinuation(current.intent, current.idempotencyKey);
    },

    openDrilldown: (run) => {
      const requestId = ++drilldownRequestId;
      const sessionId = run.sessionId;
      if (!sessionId) {
        set({ drilldown: { run, events: [], evidenceState: "ready" } });
        return;
      }

      set((current) => ({
        eventsBySession:
          sessionId in current.eventsBySession
            ? current.eventsBySession
            : { ...current.eventsBySession, [sessionId]: [] },
        eventsStatusBySession: {
          ...current.eventsStatusBySession,
          [sessionId]: "loading",
        },
        drilldown: { run, events: [], evidenceState: "loading" },
      }));

      void (async () => {
        try {
          const response = await stemApi.events(currentConnection(), sessionId);
          const persistedEvents = response.events ?? [];
          set((current) => {
            const events = mergeEventRecords(
              persistedEvents,
              current.eventsBySession[sessionId] ?? [],
            );
            return {
              eventsBySession: { ...current.eventsBySession, [sessionId]: events },
              eventsStatusBySession: {
                ...current.eventsStatusBySession,
                [sessionId]: "ready",
              },
              drilldown:
                drilldownRequestId === requestId && current.drilldown
                  ? {
                      ...current.drilldown,
                      events: correlatedEvents(run, persistedEvents),
                      evidenceState: "ready",
                      evidenceError: undefined,
                    }
                  : current.drilldown,
            };
          });
        } catch {
          set((current) => ({
            eventsStatusBySession: {
              ...current.eventsStatusBySession,
              [sessionId]: "error",
            },
            drilldown:
              drilldownRequestId === requestId && current.drilldown
                ? {
                    ...current.drilldown,
                    evidenceState: "error",
                    evidenceError: "The Stem could not provide persisted events.",
                  }
                : current.drilldown,
          }));
        }
      })();
    },

    closeDrilldown: () => {
      drilldownRequestId += 1;
      set({ drilldown: null });
    },
  };
});
