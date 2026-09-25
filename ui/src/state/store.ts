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
import { stemApi, websocketUrl, StemApiError } from "../lib/api";
import { StemSocket, type WsStatus } from "../lib/ws";
import {
  applyGardenEvent,
  emptyGarden,
  type GardenState,
} from "./garden";
import type {
  ChatMessage,
  EventRecord,
  Preferences,
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
  chatPending: Record<string, boolean>;
  chatError: string | null;
  drilldown: DrilldownTarget | null;
  configuredSubstrates: string[];

  boot: () => void;
  shutdown: () => void;
  selectSession: (sessionId: string) => void;
  createSession: (preferences?: Preferences) => Promise<void>;
  updatePreferences: (preferences: Preferences) => Promise<void>;
  sendChat: (content: string) => Promise<void>;
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
    chatPending: {},
    chatError: null,
    drilldown: null,
    configuredSubstrates: [],

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
      set({ activeSessionId: sessionId, drilldown: null, chatError: null });
      void hydrateSessionData(sessionId);
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
      try {
        const session = await stemApi.updatePreferences(
          currentConnection(),
          sessionId,
          preferences,
        );
        set((state) => ({
          chatError: null,
          sessions: state.sessions.map((existing) =>
            existing.sessionId === sessionId ? session : existing,
          ),
        }));
      } catch (err) {
        set({
          chatError:
            err instanceof StemApiError
              ? `Could not bind Substrate (${err.status}): ${err.message}`
              : `Could not bind Substrate: ${String(err)}`,
        });
        throw err;
      }
    },

    sendChat: async (content) => {
      const sessionId = get().activeSessionId;
      if (!sessionId || !content.trim()) return;
      const conn = currentConnection();
      const now = new Date().toISOString();

      set((state) => ({
        chatError: null,
        chatPending: { ...state.chatPending, [sessionId]: true },
        messagesBySession: {
          ...state.messagesBySession,
          [sessionId]: [
            ...(state.messagesBySession[sessionId] ?? []),
            { sessionId, role: "user", content, createdAt: now },
          ],
        },
      }));

      try {
        const res = await stemApi.chat(conn, sessionId, content);
        const reply = res.choices?.[0]?.message;
        if (reply) {
          set((state) => ({
            messagesBySession: {
              ...state.messagesBySession,
              [sessionId]: [
                ...(state.messagesBySession[sessionId] ?? []),
                {
                  sessionId,
                  role: reply.role || "assistant",
                  content: reply.content,
                  model: res.model,
                  createdAt: new Date().toISOString(),
                },
              ],
            },
          }));
        }
      } catch (err) {
        set({
          chatError:
            err instanceof StemApiError
              ? `Sprout failed (${err.status}): ${err.message}`
              : `Sprout failed: ${String(err)}`,
        });
      } finally {
        set((state) => ({
          chatPending: { ...state.chatPending, [sessionId]: false },
        }));
        // The run (matured or withered) is now in history — refresh the drawerable list.
        void hydrateSessionData(sessionId);
      }
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
