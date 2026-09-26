// Terminal Seed statuses owned by the Stem. Empty or unknown status is not
// terminal.

const TERMINAL_SEED_STATUSES = new Set([
  "satisfied",
  "exhausted",
  "withered",
  "fruit-publication-failed",
]);

export function seedStatusIsTerminal(status?: string): boolean {
  return TERMINAL_SEED_STATUSES.has((status ?? "").trim());
}
