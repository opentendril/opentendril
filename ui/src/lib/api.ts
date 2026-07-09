// Thin typed client over the Stem REST surface. All calls attach the operator
// bearer key (OPENTENDRIL_API_KEY on the server side) when one is configured.

import type {
  ChatCompletionResponse,
  ChatMessage,
  EventRecord,
  Preferences,
  Session,
  SproutRun,
} from "./types";

export interface StemConnection {
  /** Empty string means same-origin (dev proxy or Stem-served build). */
  baseUrl: string;
  apiKey: string;
}

export class StemApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
  ) {
    super(message);
  }
}

async function request<T>(
  conn: StemConnection,
  path: string,
  init?: RequestInit,
): Promise<T> {
  const headers: Record<string, string> = {
    ...(init?.headers as Record<string, string> | undefined),
  };
  if (conn.apiKey) headers["Authorization"] = `Bearer ${conn.apiKey}`;
  if (init?.body) headers["Content-Type"] = "application/json";

  const res = await fetch(`${conn.baseUrl}${path}`, { ...init, headers });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new StemApiError(text.trim() || res.statusText, res.status);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const stemApi = {
  health(conn: StemConnection) {
    return request<Record<string, unknown>>(conn, "/health");
  },

  listSessions(conn: StemConnection) {
    return request<{ sessions: Session[] }>(conn, "/v1/sessions");
  },

  createSession(conn: StemConnection, preferences: Preferences = {}) {
    return request<Session>(conn, "/v1/sessions", {
      method: "POST",
      body: JSON.stringify({ origin: "ws", preferences }),
    });
  },

  updatePreferences(
    conn: StemConnection,
    sessionId: string,
    preferences: Preferences,
  ) {
    return request<Session>(conn, `/v1/sessions/${sessionId}`, {
      method: "PATCH",
      body: JSON.stringify({ preferences }),
    });
  },

  deleteSession(conn: StemConnection, sessionId: string) {
    return request<void>(conn, `/v1/sessions/${sessionId}`, {
      method: "DELETE",
    });
  },

  messages(conn: StemConnection, sessionId: string, limit = 200) {
    return request<{ sessionId: string; messages: ChatMessage[] }>(
      conn,
      `/v1/sessions/${sessionId}/history?limit=${limit}`,
    );
  },

  events(conn: StemConnection, sessionId: string, limit = 300) {
    return request<{ sessionId: string; events: EventRecord[] }>(
      conn,
      `/v1/sessions/${sessionId}/events?limit=${limit}`,
    );
  },

  sproutRuns(conn: StemConnection, sessionId: string, limit = 100) {
    return request<{ sessionId: string; sproutRuns: SproutRun[] }>(
      conn,
      `/v1/sessions/${sessionId}/sprout-runs?limit=${limit}`,
    );
  },

  genotypes(conn: StemConnection) {
    return request<{ genotypes: string[] }>(conn, "/v1/config/genotypes");
  },

  chat(conn: StemConnection, sessionId: string, content: string, model?: string) {
    return request<ChatCompletionResponse>(conn, "/v1/chat/completions", {
      method: "POST",
      body: JSON.stringify({
        sessionId,
        model: model ?? "",
        messages: [{ role: "user", content }],
      }),
    });
  },
};

/** Build the /ws URL for a connection (ws:// or wss:// derived from baseUrl).
 *  ?replay=100 asks the gateway to prepend the bus's recent in-memory event
 *  history so a refreshed client re-grows sequence state with no session id.
 *  The bearer key rides as `?key=` too: the Stem now requires auth on /ws
 *  (issue #171 finding 2), and the browser's native WebSocket API cannot set
 *  an Authorization header on the upgrade request. */
export function websocketUrl(conn: StemConnection): string {
  const key = conn.apiKey ? `&key=${encodeURIComponent(conn.apiKey)}` : "";
  if (!conn.baseUrl) {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${window.location.host}/ws?replay=100${key}`;
  }
  return conn.baseUrl.replace(/^http/, "ws") + `/ws?replay=100${key}`;
}
