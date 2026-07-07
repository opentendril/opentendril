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

export type Hydration = "idle" | "hydrating" | "ready" | "error";

export interface DrilldownTarget {
  run: SproutRun;
  events: EventRecord[];
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
  garden: GardenState;
  ticker: StemEvent[];
  chatPending: Record<string, boolean>;
  chatError: string | null;
  drilldown: DrilldownTarget | null;

  boot: () => void;
  shutdown: () => void;
  selectSession: (sessionId: string) => void;
  createSession: (preferences?: Preferences) => Promise<void>;
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
let refreshTimer: number | null = null;

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

    // A session we do not know yet showed activity: refresh the rail soon.
    if (
      event.sessionId &&
      !get().sessions.some((s) => s.sessionId === event.sessionId)
    ) {
      scheduleSessionRefresh();
    }
  }

  function scheduleSessionRefresh() {
    if (refreshTimer !== null) return;
    refreshTimer = window.setTimeout(async () => {
      refreshTimer = null;
      try {
        const { sessions } = await stemApi.listSessions(currentConnection());
        set({ sessions });
      } catch {
        // transient; the next event will retry
      }
    }, 750);
  }

  function onLiveEvent(event: StemEvent) {
    if (liveBuffer) {
      liveBuffer.push(event);
      return;
    }
    foldEvent(event);
  }

  async function hydrateSessionData(sessionId: string) {
    const conn = currentConnection();
    const [messages, runs, events] = await Promise.all([
      stemApi.messages(conn, sessionId).catch(() => ({ messages: [] as ChatMessage[] })),
      stemApi.sproutRuns(conn, sessionId).catch(() => ({ sproutRuns: [] as SproutRun[] })),
      stemApi.events(conn, sessionId).catch(() => ({ events: [] as EventRecord[] })),
    ]);
    set((state) => ({
      messagesBySession: {
        ...state.messagesBySession,
        [sessionId]: messages.messages ?? [],
      },
      runsBySession: {
        ...state.runsBySession,
        [sessionId]: runs.sproutRuns ?? [],
      },
      eventsBySession: {
        ...state.eventsBySession,
        [sessionId]: events.events ?? [],
      },
    }));
    return events.events ?? [];
  }

  async function hydrate() {
    set({ hydration: "hydrating", hydrationError: null });
    liveBuffer = [];
    try {
      const conn = currentConnection();
      const { sessions } = await stemApi.listSessions(conn);

      const stored = window.localStorage.getItem(ACTIVE_SESSION_KEY);
      const active =
        (stored && sessions.some((s) => s.sessionId === stored) && stored) ||
        sessions[0]?.sessionId ||
        null;

      set({ sessions, activeSessionId: active });

      let persistedEvents: EventRecord[] = [];
      if (active) {
        persistedEvents = await hydrateSessionData(active);
      }

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
    garden: emptyGarden,
    ticker: [],
    chatPending: {},
    chatError: null,
    drilldown: null,

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
    },

    selectSession: (sessionId) => {
      window.localStorage.setItem(ACTIVE_SESSION_KEY, sessionId);
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
      }));
      get().selectSession(session.sessionId);
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
      const sessionEvents = run.sessionId
        ? get().eventsBySession[run.sessionId] ?? []
        : [];
      const related = sessionEvents.filter(
        (e) =>
          (run.stepId && e.source === run.stepId) ||
          (run.stepId && e.data?.["stepId"] === run.stepId),
      );
      set({ drilldown: { run, events: related } });
    },

    closeDrilldown: () => set({ drilldown: null }),
  };
});
