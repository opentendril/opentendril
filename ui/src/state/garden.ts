// The living garden model: a pure fold of EventBus events into a botanical
// scene graph. Each orchestration (a sequence name, or a session for direct
// chat runs) grows one Plant; each stepId is a Branch off its stem; each
// parallel sprout is a Tendril tip on that branch; a phenotypic-selection
// sequence grows a selection Arena of competing phenotype pods.
//
// Event → mutation mapping (payload fields match the Go publishers exactly):
//   parallel-sprouting  {phase:"map", sproutCount}        → branch unfurls with N buds
//   sprout-emerged      {sproutIndex, branchName, detail} → tendril tip grows
//   sprout-matured      {sproutIndex, branchName}         → tip blooms
//   sprout-withered     {sproutIndex, branchName, detail} → tip desaturates and droops
//   mycelial-merge      {phase:"reduce"|"complete", maturedCount, witheredCount}
//                                                         → filaments converge, node pulses
//   phenotypic-selection {phase:"start"|"generation"|"evaluated"|"complete", ...}
//                                                         → arena of pods competes; alpha crowns
//   stream-token        (source = stepId)                 → sap shimmer up the active branch
//   thought-branch      {thought}                         → ephemeral leaf whisper
//   sequence-complete / sequence-failure                  → plant fruits / branch scorches

import type { StemEvent } from "../lib/types";

export type SproutStatus = "emerging" | "matured" | "withered";

export interface TendrilTip {
  index: number;
  branchName: string;
  status: SproutStatus;
  detail?: string;
  bornAt: number;
  updatedAt: number;
}

export interface PhenotypePod {
  id: number;
  generation: number;
  status: "growing" | "survived" | "withered" | "alpha";
}

export interface SelectionArena {
  phase: "start" | "generation" | "evaluated" | "complete";
  populationSize: number;
  maxGenerations: number;
  fitnessGoal?: string;
  generation: number;
  pods: PhenotypePod[];
  survivors: number;
  withered: number;
  bestScore?: number;
  alphaScore?: number;
  alphaBranch?: string;
  updatedAt: number;
}

export interface Branch {
  stepId: string;
  order: number;
  bornAt: number;
  updatedAt: number;
  expectedSprouts: number;
  tips: TendrilTip[];
  mergePhase?: "reduce" | "complete";
  maturedCount: number;
  witheredCount: number;
  arena?: SelectionArena;
  failed?: boolean;
}

export interface Whisper {
  id: number;
  text: string;
  at: number;
}

export interface Plant {
  key: string;
  label: string;
  kind: "sequence" | "session";
  bornAt: number;
  lastEventAt: number;
  branches: Branch[];
  streamingStepId?: string;
  streamPulseAt: number;
  whispers: Whisper[];
  complete?: boolean;
  failed?: boolean;
  failureDetail?: string;
}

export interface GardenState {
  plants: Record<string, Plant>;
  whisperSeq: number;
}

export const emptyGarden: GardenState = { plants: {}, whisperSeq: 0 };

const MAX_PLANTS = 8;
const MAX_WHISPERS = 3;

function num(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}
function str(v: unknown): string | undefined {
  return typeof v === "string" && v !== "" ? v : undefined;
}

function plantKeyFor(event: StemEvent): { key: string; kind: Plant["kind"] } | null {
  const seq = str(event.data?.["sequence"]);
  if (seq) return { key: `seq:${seq}`, kind: "sequence" };
  if (event.sessionId) return { key: `ses:${event.sessionId}`, kind: "session" };
  // Direct chat runs: Sprout stream tokens carry only source=stepId. Group them
  // under a shared ambient plant so live activity is never invisible.
  if (event.source && event.type !== "connected") {
    return { key: "ambient", kind: "sequence" };
  }
  return null;
}

function ensurePlant(state: GardenState, event: StemEvent, at: number): Plant | null {
  const id = plantKeyFor(event);
  if (!id) return null;
  let plant = state.plants[id.key];
  if (!plant) {
    const label =
      id.kind === "sequence"
        ? str(event.data?.["sequence"]) ?? "orchestrator"
        : (event.sessionId ?? "session").replace(/^tendril-/, "").slice(0, 10);
    plant = {
      key: id.key,
      label,
      kind: id.kind,
      bornAt: at,
      lastEventAt: at,
      branches: [],
      streamPulseAt: 0,
      whispers: [],
    };
    state.plants[id.key] = plant;
    prunePlants(state);
  }
  plant.lastEventAt = at;
  return plant;
}

function prunePlants(state: GardenState) {
  const keys = Object.keys(state.plants);
  if (keys.length <= MAX_PLANTS) return;
  const sorted = keys.sort(
    (a, b) => state.plants[a].lastEventAt - state.plants[b].lastEventAt,
  );
  for (const key of sorted.slice(0, keys.length - MAX_PLANTS)) {
    delete state.plants[key];
  }
}

function ensureBranch(plant: Plant, stepId: string, at: number): Branch {
  let branch = plant.branches.find((b) => b.stepId === stepId);
  if (!branch) {
    branch = {
      stepId,
      order: plant.branches.length,
      bornAt: at,
      updatedAt: at,
      expectedSprouts: 0,
      tips: [],
      maturedCount: 0,
      witheredCount: 0,
    };
    plant.branches.push(branch);
  }
  branch.updatedAt = at;
  return branch;
}

/** stepId for events that only carry source (stream tokens from Sprouts use
 *  source = stepId; sequence events put stepId in data). */
function stepIdOf(event: StemEvent): string {
  return str(event.data?.["stepId"]) ?? str(event.source) ?? "stem";
}

function upsertTip(branch: Branch, event: StemEvent, status: SproutStatus, at: number) {
  const index = num(event.data?.["sproutIndex"]) ?? branch.tips.length;
  const branchName = str(event.data?.["branchName"]) ?? `sprout-${index}`;
  const detail = str(event.data?.["detail"]);
  let tip = branch.tips.find((t) => t.index === index);
  if (!tip) {
    tip = { index, branchName, status, detail, bornAt: at, updatedAt: at };
    branch.tips.push(tip);
    branch.tips.sort((a, b) => a.index - b.index);
  } else {
    tip.status = status;
    tip.branchName = branchName;
    if (detail) tip.detail = detail;
    tip.updatedAt = at;
  }
  if (status === "matured") branch.maturedCount += 1;
  if (status === "withered") branch.witheredCount += 1;
}

function applySelection(branch: Branch, event: StemEvent, at: number) {
  const data = event.data ?? {};
  const phase = (str(data["phase"]) ?? "start") as SelectionArena["phase"];
  let arena = branch.arena;
  if (!arena) {
    arena = {
      phase: "start",
      populationSize: num(data["populationSize"]) ?? 4,
      maxGenerations: num(data["maxGenerations"]) ?? 1,
      generation: 0,
      pods: [],
      survivors: 0,
      withered: 0,
      updatedAt: at,
    };
    branch.arena = arena;
  }
  arena.updatedAt = at;
  arena.phase = phase;

  switch (phase) {
    case "start": {
      arena.populationSize = num(data["populationSize"]) ?? arena.populationSize;
      arena.maxGenerations = num(data["maxGenerations"]) ?? arena.maxGenerations;
      arena.fitnessGoal = str(data["fitnessGoal"]) ?? arena.fitnessGoal;
      break;
    }
    case "generation": {
      const generation = num(data["generation"]) ?? arena.generation;
      const population = num(data["population"]) ?? arena.populationSize;
      arena.generation = generation;
      // A new generation germinates: previous survivors compost, fresh pods grow.
      arena.pods = Array.from({ length: population }, (_, i) => ({
        id: generation * 100 + i,
        generation,
        status: "growing" as const,
      }));
      break;
    }
    case "evaluated": {
      const survivors = num(data["survivors"]) ?? 0;
      const withered = num(data["withered"]) ?? 0;
      arena.survivors = survivors;
      arena.withered = withered;
      arena.bestScore = num(data["bestScore"]) ?? arena.bestScore;
      arena.alphaScore = num(data["alphaScore"]) ?? arena.alphaScore;
      arena.alphaBranch = str(data["alphaBranch"]) ?? arena.alphaBranch;
      // The publisher reports counts, not per-pod verdicts: mark the first
      // `survivors` pods survived (they are sorted fittest-first server-side).
      arena.pods = arena.pods.map((pod, i) => ({
        ...pod,
        status: i < survivors ? "survived" : "withered",
      }));
      break;
    }
    case "complete": {
      arena.alphaScore = num(data["alphaScore"]) ?? arena.alphaScore;
      arena.alphaBranch = str(data["alphaBranch"]) ?? arena.alphaBranch;
      arena.pods = arena.pods.map((pod, i) => ({
        ...pod,
        status: pod.status === "survived" && i === 0 ? "alpha" : pod.status,
      }));
      if (!arena.pods.some((p) => p.status === "alpha") && arena.pods.length > 0) {
        arena.pods[0] = { ...arena.pods[0], status: "alpha" };
      }
      break;
    }
  }
}

/** Fold one live (or replayed) event into the garden. Mutates a draft copy. */
export function applyGardenEvent(state: GardenState, event: StemEvent): GardenState {
  const at = event.timestamp ? Date.parse(event.timestamp) : Date.now();
  const next: GardenState = {
    plants: { ...state.plants },
    whisperSeq: state.whisperSeq,
  };

  const existing = plantKeyFor(event);
  if (existing && next.plants[existing.key]) {
    // Deep-ish copy the touched plant only; branches are copied on write below.
    const source = next.plants[existing.key];
    next.plants[existing.key] = {
      ...source,
      branches: source.branches.map((b) => ({
        ...b,
        tips: b.tips.map((t) => ({ ...t })),
        arena: b.arena
          ? { ...b.arena, pods: b.arena.pods.map((p) => ({ ...p })) }
          : undefined,
      })),
      whispers: [...source.whispers],
    };
  }

  const plant = ensurePlant(next, event, at);
  if (!plant) return next;

  switch (event.type) {
    case "parallel-sprouting": {
      const branch = ensureBranch(plant, stepIdOf(event), at);
      branch.expectedSprouts = num(event.data?.["sproutCount"]) ?? 0;
      break;
    }
    case "sprout-emerged": {
      upsertTip(ensureBranch(plant, stepIdOf(event), at), event, "emerging", at);
      break;
    }
    case "sprout-matured": {
      upsertTip(ensureBranch(plant, stepIdOf(event), at), event, "matured", at);
      break;
    }
    case "sprout-withered": {
      upsertTip(ensureBranch(plant, stepIdOf(event), at), event, "withered", at);
      break;
    }
    case "mycelial-merge": {
      const branch = ensureBranch(plant, stepIdOf(event), at);
      const phase = str(event.data?.["phase"]);
      branch.mergePhase = phase === "complete" ? "complete" : "reduce";
      branch.maturedCount =
        num(event.data?.["maturedCount"]) ?? branch.maturedCount;
      branch.witheredCount =
        num(event.data?.["witheredCount"]) ?? branch.witheredCount;
      break;
    }
    case "phenotypic-selection": {
      applySelection(ensureBranch(plant, stepIdOf(event), at), event, at);
      break;
    }
    case "stream-token": {
      plant.streamingStepId = stepIdOf(event);
      plant.streamPulseAt = at;
      const kind = str(event.data?.["type"]);
      if (kind === "stream.end") plant.streamingStepId = undefined;
      break;
    }
    case "thought-branch": {
      const thought = str(event.data?.["thought"]) ?? event.content;
      if (thought) {
        next.whisperSeq += 1;
        plant.whispers = [
          ...plant.whispers.slice(-(MAX_WHISPERS - 1)),
          { id: next.whisperSeq, text: thought.slice(0, 160), at },
        ];
      }
      break;
    }
    case "sequence-complete": {
      plant.complete = true;
      plant.streamingStepId = undefined;
      break;
    }
    case "sequence-failure": {
      const branch = ensureBranch(plant, stepIdOf(event), at);
      branch.failed = true;
      plant.failed = true;
      plant.failureDetail = str(event.data?.["error"]);
      break;
    }
    default:
      // health-*, terrarium-*, rhizome-update, xylem-transport,
      // hormonal-trigger, api-key-invalid: ambient — surfaced in the event
      // pulse ticker, they only refresh the plant's liveness timestamp here.
      break;
  }

  return next;
}
