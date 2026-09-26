// Thin typed client over the Stem REST surface. All calls attach the Botanist
// bearer key (BOTANIST_KEY on the Stem) when one is configured.

import { readSSEFrames } from "./sse";
import type {
  ChatCompletionResponse,
  ChatMessage,
  ContinuationRequest,
  ContinuationResult,
  EventRecord,
  FruitInventory,
  PhytomerObservation,
  Preferences,
  SeedDispatchResult,
  SeedGrowRequest,
  SeedRun,
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
    this.name = "StemApiError";
  }
}

// GET /v1/phytomers/{id}/watch answered 404. That response is the authoritative
// signal that the Phytomer is not a Seed-owned work context. A successful
// response that is not an event stream is a watch failure, not this case.
export class NotSeedWatchError extends Error {
  constructor(
    message: string,
    public readonly status: number,
  ) {
    super(message);
    this.name = "NotSeedWatchError";
  }
}

export function isAbortError(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  return (err as { name?: string }).name === "AbortError";
}

function errorFrameMessage(data: string): string {
  const trimmed = data.trim();
  if (!trimmed) return "Phytomer watch reported an error";
  try {
    const parsed = JSON.parse(trimmed) as { error?: unknown; message?: unknown };
    if (typeof parsed.error === "string" && parsed.error.trim()) return parsed.error.trim();
    if (typeof parsed.message === "string" && parsed.message.trim()) return parsed.message.trim();
  } catch {
    // The Stem may send a plain-text error frame.
  }
  return trimmed;
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

  substrates(conn: StemConnection) {
    return request<{ substrates: string[] }>(conn, "/v1/config/substrates");
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

  // Detached canonical Seed growth. The caller supplies the idempotency key.
  growSeed(conn: StemConnection, body: SeedGrowRequest) {
    return request<SeedDispatchResult>(conn, "/v1/seeds/grow", {
      method: "POST",
      body: JSON.stringify(body),
    });
  },

  collectSeed(conn: StemConnection, handle: string) {
    return request<SeedRun>(
      conn,
      `/v1/seeds/runs/${encodeURIComponent(handle)}`,
    );
  },

  continuePhytomer(
    conn: StemConnection,
    phytomerId: string,
    body: ContinuationRequest,
  ) {
    return request<ContinuationResult>(
      conn,
      `/v1/phytomers/${encodeURIComponent(phytomerId)}/continue`,
      {
        method: "POST",
        body: JSON.stringify(body),
      },
    );
  },

  fruitInventory(conn: StemConnection) {
    return request<FruitInventory>(conn, "/v1/fruit");
  },

  // Authenticated SSE. The bearer stays on Authorization and is never placed
  // in the URL. Only observation frames are delivered. error frames reject.
  async watchPhytomer(
    conn: StemConnection,
    phytomerId: string,
    signal: AbortSignal,
    onObservation: (observation: PhytomerObservation) => void,
  ) {
    const headers: Record<string, string> = {};
    if (conn.apiKey) headers.Authorization = `Bearer ${conn.apiKey}`;
    const response = await fetch(
      `${conn.baseUrl}/v1/phytomers/${encodeURIComponent(phytomerId)}/watch`,
      { headers, signal },
    );
    if (response.status === 404) {
      const text = await response.text().catch(() => "");
      throw new NotSeedWatchError(
        text.trim() || "no seed growth is associated with this phytomer",
        response.status,
      );
    }
    if (!response.ok) {
      const text = await response.text().catch(() => "");
      throw new StemApiError(text.trim() || response.statusText, response.status);
    }
    const contentType = response.headers.get("content-type") ?? "";
    if (!contentType.toLowerCase().includes("text/event-stream")) {
      await response.body?.cancel().catch(() => undefined);
      const reported = contentType.split(";")[0]?.trim() || "no content type";
      throw new Error(
        `Phytomer watch failed because the response was ${reported}, not an event stream`,
      );
    }
    if (!response.body) {
      throw new StemApiError("phytomer watch returned an empty body", response.status);
    }

    await readSSEFrames(
      response.body,
      (frame) => {
        if (frame.event === "error") {
          throw new Error(errorFrameMessage(frame.data));
        }
        if (frame.event !== "observation") return;
        let observation: PhytomerObservation;
        try {
          observation = JSON.parse(frame.data) as PhytomerObservation;
        } catch {
          throw new Error("Phytomer watch observation was not valid JSON");
        }
        onObservation(observation);
      },
      signal,
    );
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
