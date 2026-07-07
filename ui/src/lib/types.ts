// Shapes mirrored 1:1 from the Go Stem's documented REST + WebSocket surface.
// Sources of truth:
//   cmd/stem/internal/session/session.go      (Session, Message, Preferences)
//   cmd/stem/internal/historydb/historydb.go  (SproutRun, EventRecord)
//   cmd/stem/internal/gateway/gateway.go      (WebSocket frame)
//   cmd/stem/internal/eventbus/eventbus.go    (event type registry)

export interface Preferences {
  provider?: string;
  model?: string;
  genotype?: string;
  epigeneticGenome?: string;
  extras?: Record<string, string>;
}

export interface Session {
  sessionId: string;
  origin: string;
  createdAt: string;
  lastActiveAt: string;
  preferences: Preferences;
}

export interface ChatMessage {
  sessionId: string;
  role: string;
  content: string;
  model?: string;
  createdAt: string;
}

export interface SproutRun {
  runId: string;
  sessionId?: string;
  stepId?: string;
  origin?: string;
  model?: string;
  genotype?: string;
  transcript?: string;
  status: "running" | "matured" | "withered" | string;
  output?: string;
  error?: string;
  startedAt: string;
  finishedAt?: string;
}

export interface EventRecord {
  id: number;
  sessionId?: string;
  type: string;
  source?: string;
  data?: Record<string, unknown>;
  createdAt: string;
}

/** One frame off /ws (gateway.go builds this map per event). */
export interface StemEvent {
  type: string;
  timestamp?: string;
  source?: string;
  sessionId?: string;
  data?: Record<string, unknown>;
  /** gateway copies data.token / data.thought here for stream-token / thought-branch */
  content?: string;
}

// Registered EventBus types (eventbus.go). phenotypic-selection is emitted by
// selection.go with the same envelope.
export type StemEventType =
  | "health-check"
  | "health-degraded"
  | "health-recovered"
  | "terrarium-oom"
  | "terrarium-timeout"
  | "api-key-invalid"
  | "sequence-failure"
  | "sequence-complete"
  | "stream-token"
  | "thought-branch"
  | "sprout-emerged"
  | "sprout-matured"
  | "sprout-withered"
  | "hormonal-trigger"
  | "rhizome-update"
  | "xylem-transport"
  | "parallel-sprouting"
  | "mycelial-merge"
  | "phenotypic-selection"
  | "connected";

export interface ChatCompletionResponse {
  id: string;
  object: string;
  created: number;
  model: string;
  sessionId?: string;
  choices: Array<{
    index: number;
    message: { role: string; content: string };
    finishReason: string;
  }>;
}

export interface HealthReport {
  [key: string]: unknown;
}
