// Resilient WebSocket client for the Stem EventBus gateway (/ws).
// Reconnects with capped exponential backoff and reports lifecycle status so
// the store can re-hydrate from REST before splicing the live feed back in.

import type { StemEvent } from "./types";

export type WsStatus = "connecting" | "open" | "closed";

export interface StemSocketCallbacks {
  onEvent: (event: StemEvent) => void;
  onStatus: (status: WsStatus) => void;
}

export class StemSocket {
  private ws: WebSocket | null = null;
  private url: string;
  private callbacks: StemSocketCallbacks;
  private attempts = 0;
  private timer: number | null = null;
  private stopped = false;

  constructor(url: string, callbacks: StemSocketCallbacks) {
    this.url = url;
    this.callbacks = callbacks;
  }

  connect() {
    this.stopped = false;
    this.open();
  }

  close() {
    this.stopped = true;
    if (this.timer !== null) window.clearTimeout(this.timer);
    this.ws?.close();
    this.ws = null;
  }

  private open() {
    if (this.stopped) return;
    this.callbacks.onStatus("connecting");
    const ws = new WebSocket(this.url);
    this.ws = ws;

    ws.onopen = () => {
      this.attempts = 0;
      this.callbacks.onStatus("open");
    };

    ws.onmessage = (msg) => {
      try {
        const event = JSON.parse(msg.data as string) as StemEvent;
        if (event && typeof event.type === "string") {
          this.callbacks.onEvent(event);
        }
      } catch {
        // Non-JSON frame; the gateway only sends JSON, ignore.
      }
    };

    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.callbacks.onStatus("closed");
      this.scheduleReconnect();
    };

    ws.onerror = () => {
      ws.close();
    };
  }

  private scheduleReconnect() {
    if (this.stopped) return;
    const delay = Math.min(30_000, 500 * 2 ** this.attempts);
    this.attempts += 1;
    this.timer = window.setTimeout(() => this.open(), delay);
  }
}
