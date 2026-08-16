import type { EventRecord, FailureCategory, SproutRun } from "./types";

// Presentation copy for Core-owned failure categories. The UI never
// derives a category from run.error or other free text.
const FAILURE_CATEGORY_COPY: Record<string, string> = {
  "provider-auth-rejected": "provider authentication rejected",
  "provider-request-rejected": "provider request rejected",
  "no-engagement": "no engagement",
  "terrarium-runtime": "Terrarium runtime",
  "execution-failed": "execution failed",
  matured: "matured",
};

export function failureCategoryLabel(category?: FailureCategory): string {
  if (!category) return "";
  return FAILURE_CATEGORY_COPY[category] ?? category;
}

export function statusLabel(status: string): string {
  if (status === "withered") return "Withered";
  if (status === "matured") return "Matured";
  if (status === "running") return "Running";
  return status || "—";
}

export function observationLead(run: SproutRun): string {
  const status = statusLabel(run.status);
  if (run.failureCategory) {
    return `${status} — ${failureCategoryLabel(run.failureCategory)}`;
  }
  if (run.outcome) return `${status} — ${run.outcome}`;
  return status;
}

export function providerRequestLabel(run: SproutRun): string {
  if (run.providerRequestAttempted) return "attempted";
  if (run.status === "running") return "not yet";
  return "not attempted";
}

export function toolInvocationCount(run: SproutRun): number {
  return typeof run.toolInvocations === "number" ? run.toolInvocations : 0;
}

export function diagnosticLine(run: SproutRun): string {
  const diagnostic = run.providerDiagnostic;
  if (!diagnostic) return "";
  const status =
    typeof diagnostic.statusCode === "number" && diagnostic.statusCode > 0
      ? `HTTP ${diagnostic.statusCode}`
      : "";
  const message = diagnostic.message?.trim() ?? "";
  if (status && message) return `${status} / ${message}`;
  return status || message;
}

export function resolvedProvider(run: SproutRun): string {
  return (
    run.provider ||
    run.providerDiagnostic?.provider ||
    run.usage?.execution?.provider ||
    ""
  );
}

export interface ToolActivityRow {
  name: string;
  status: string;
}

// Per-tool rows come only from tool-invoked EventBus records. The run
// contract carries a count, not names; this never invents a tool from
// run.error or other free text.
export function toolActivityFromEvents(events: EventRecord[]): ToolActivityRow[] {
  const rows: ToolActivityRow[] = [];
  for (const event of events) {
    if (event.type !== "tool-invoked") continue;
    const name = typeof event.data?.["tool"] === "string" ? event.data["tool"] : "";
    const status =
      typeof event.data?.["status"] === "string" ? event.data["status"] : "";
    if (!name && !status) continue;
    rows.push({ name: name || "—", status: status || "unknown" });
  }
  return rows;
}

// filesModified is published on the terminal sprout event, not on the
// durable SproutRun row. Copy it when present; do not infer it.
export function filesModifiedFromEvents(events: EventRecord[]): string[] | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i];
    if (event.type !== "sprout-matured" && event.type !== "sprout-withered") {
      continue;
    }
    const files = event.data?.["filesModified"];
    if (!Array.isArray(files)) continue;
    return files.filter((path): path is string => typeof path === "string");
  }
  return undefined;
}

export function fruitLabel(
  run: SproutRun,
  filesModified?: string[],
): string | undefined {
  if (filesModified && filesModified.length > 0) {
    return filesModified.join(", ");
  }
  if (filesModified && filesModified.length === 0) {
    return "none measured";
  }
  if (run.failureCategory && run.failureCategory !== "matured") {
    return "none";
  }
  return undefined;
}
