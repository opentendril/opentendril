import type { FailureCategory, SproutRun } from "./types";

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
