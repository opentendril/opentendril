import type { FruitInventory, FruitInventoryItem, SeedRun } from "./types";

// Fruit exists for review only when the Stem reported both branch and commit.
export function seedReportsFruitProvenance(run?: SeedRun | null): boolean {
  return Boolean(run?.branch?.trim() && run?.commit?.trim());
}

// Match Seed Fruit by Stem identity. Phytomer id is a consistency check when
// both sides recorded one. Review state is copied from the inventory item.
export function matchingSeedFruit(
  inventory: FruitInventory | null | undefined,
  run?: SeedRun | null,
): FruitInventoryItem | null {
  const handle = run?.handle?.trim() ?? "";
  if (!inventory || !handle) return null;
  const phytomerId = run?.phytomerId?.trim() ?? "";
  const matches = (inventory.items ?? []).filter((item) => {
    if (item.producerKind !== "seed") return false;
    if (item.producerIdentity !== handle) return false;
    if (phytomerId && item.phytomerId && item.phytomerId !== phytomerId) return false;
    return true;
  });
  return matches.length === 1 ? matches[0] : null;
}
