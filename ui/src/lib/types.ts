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
  substrate?: string;
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

export interface UsageComponent {
  requestsMade: boolean;
  promptTokens?: number;
  completionTokens?: number;
  totalTokens?: number;
  costAmount?: string;
  costUnit?: string;
  costProvenance?: string;
  provider?: string;
  model?: string;
}

export interface SproutRunUsage {
  execution?: UsageComponent;
  postRun?: UsageComponent;
}

export interface ProviderDiagnostic {
  statusCode?: number;
  message?: string;
  provider?: string;
}

export type FailureCategory =
  | "provider-auth-rejected"
  | "provider-request-rejected"
  | "no-engagement"
  | "terrarium-runtime"
  | "execution-failed"
  | "matured"
  | string;

export type FailureStage =
  | "substrate-resolution"
  | "workspace-preparation"
  | "task-context-preparation"
  | "provider-resolution"
  | "provider-preflight"
  | "terrarium-preparation"
  | "sprout-execution"
  | "post-run"
  | "fruit-publication"
  | "unknown";

export type DiagnosticCode =
  | "substrate-not-found"
  | "substrate-access-denied"
  | "substrate-invalid"
  | "workspace-preparation-failed"
  | "task-context-unavailable"
  | "provider-unresolved"
  | "provider-preflight-rejected"
  | "terrarium-preparation-failed"
  | "terrarium-start-failed"
  | "terrarium-oom"
  | "sprout-execution-failed"
  | "post-run-failed"
  | "fruit-publication-failed";

export interface SproutRun {
  runId: string;
  sessionId?: string;
  stepId?: string;
  origin?: string;
  pollen?: string;
  substrate?: string;
  provider?: string;
  model?: string;
  genotype?: string;
  transcript?: string;
  status: "running" | "matured" | "withered" | string;
  output?: string;
  error?: string;
  startedAt: string;
  finishedAt?: string;
  usage?: SproutRunUsage;
  outcome?: string;
  failureCategory?: FailureCategory;
  failureStage?: FailureStage;
  diagnosticCode?: DiagnosticCode;
  providerDiagnostic?: ProviderDiagnostic;
  providerRequestAttempted?: boolean;
  toolInvocations?: number;
  // Recorded provider that created this Sprout's Terrarium. Absent when the
  // durable observation did not contain the fact.
  terrariumProvider?: string;
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
  | "tool-invoked"
  | "sprout-emerged"
  | "sprout-matured"
  | "sprout-withered"
  | "mycorrhizal-request-begun"
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

// Stem seed.grow request. verify is argv and is never a shell command line.
// Zero or omitted bounds mean the Stem default. The Stem remains authoritative
// for the maximums.
export interface SeedGrowRequest {
  substrate: string;
  goal: string;
  verify: string[];
  maxIterations?: number;
  timeoutSeconds?: number;
  origin: string;
  detached: true;
  idempotencyKey: string;
}

// Detached POST /v1/seeds/grow success body.
export interface SeedDispatchResult {
  handle: string;
  phytomerId: string;
  status: string;
}

export interface SeedPublicationDiagnostic {
  failureCategory: string;
  executionStatus: string;
  phase: string;
  outcome: string;
  retrySafe: boolean;
  message: string;
  requestId?: string;
}

export interface SeedVerificationDiagnostic {
  iteration: number;
  outcome: string;
  exitCode?: number;
  timedOut: boolean;
  message?: string;
}

// GET /v1/seeds/runs/{handle}. Goal on this record is the durable task label.
export interface SeedRun {
  handle: string;
  pollen?: string;
  phytomerId?: string;
  substrate?: string;
  goal?: string;
  status: string;
  iterations: number;
  branch?: string;
  commit?: string;
  fruitRepository?: string;
  fruitPublicationState?: string;
  fruitCreatedAt?: string;
  diff?: string;
  logs?: string;
  error?: string;
  publicationDiagnostic?: SeedPublicationDiagnostic;
  verificationDiagnostics?: SeedVerificationDiagnostic[];
  startedAt?: string;
  finishedAt?: string;
}

export interface SproutObservation {
  runId?: string;
  status?: string;
  provider?: string;
  model?: string;
  outcome?: string;
  failureCategory?: string;
  failureStage?: string;
  diagnosticCode?: string;
  providerDiagnostic?: ProviderDiagnostic;
  providerRequestAttempted?: boolean;
  toolInvocations?: number;
  terrariumProvider?: string;
}

export interface ContinuationObservation {
  continuationId: string;
  sequence: number;
  deliveryState: string;
}

// SSE event: observation payload from GET /v1/phytomers/{id}/watch.
export interface PhytomerObservation {
  pollen?: string;
  substrate?: string;
  handle?: string;
  phytomerId?: string;
  status?: string;
  iterations: number;
  branch?: string;
  commit?: string;
  publicationDiagnostic?: SeedPublicationDiagnostic;
  verificationDiagnostics?: SeedVerificationDiagnostic[];
  sprouts?: SproutObservation[];
  continuations?: ContinuationObservation[];
}

export interface ContinuationRequest {
  intent: string;
  idempotencyKey: string;
  sessionId: string;
}

export interface ContinuationResult {
  continuationId: string;
  sessionId: string;
  sequence: number;
  deliveryState: string;
  idempotencyKey: string;
  acceptedAt: string;
}

export interface FruitInventoryItem {
  producerKind: string;
  producerIdentity: string;
  phytomerId?: string;
  substrate?: string;
  repository: string;
  branch: string;
  commit: string;
  publicationState?: string;
  createdAt?: string;
  startedAt?: string;
  finishedAt?: string;
  reviewState: string;
  unknownReason?: string;
  pullRequest?: number;
}

export interface FruitReviewPressure {
  outstanding: number;
  unknown: number;
  closedUnmerged: number;
  merged: number;
  total: number;
}

export interface FruitInventory {
  items: FruitInventoryItem[];
  counts: FruitReviewPressure;
}
