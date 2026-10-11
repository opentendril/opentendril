import { useState } from "react";
import { useStem } from "../state/store";
import type {
  FruitInventoryItem,
  LifecycleDelegatedWorkspace,
  LifecycleInventory,
  LifecycleRunWorkspace,
  LifecycleSourceStatus,
} from "../lib/types";

const sections = [
  { source: "fruit", title: "Fruit", empty: "No Fruit entries were reported." },
  {
    source: "run-workspace",
    title: "RunWorkspaces",
    empty: "No RunWorkspace entries were reported.",
  },
  {
    source: "delegated-workspace",
    title: "Delegated workspaces",
    empty: "No delegated workspace entries were reported.",
  },
] as const;

function sourceStatus(
  inventory: LifecycleInventory | null,
  source: string,
): LifecycleSourceStatus {
  const found = inventory?.sources?.find((item) => item.source === source);
  if (!found || found.state !== "available") {
    return {
      source,
      state: "unavailable",
      reason: found?.reason || "source status was not reported",
      itemCount: 0,
    };
  }
  return found;
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </>
  );
}

function FruitRow({ item }: { item: FruitInventoryItem }) {
  return (
    <article className="lifecycle-row" data-testid="lifecycle-fruit-row">
      <dl className="lifecycle-facts">
        <Fact label="Producer" value={`${item.producerKind} ${item.producerIdentity}`.trim()} />
        <Fact label="Repository" value={item.repository} />
        <Fact label="Branch" value={item.branch} />
        <Fact label="Commit" value={item.commit} />
        <Fact label="Review" value={item.reviewState} />
        {item.unknownReason ? <Fact label="Unknown" value={item.unknownReason} /> : null}
      </dl>
    </article>
  );
}

function RunWorkspaceRow({ item }: { item: LifecycleRunWorkspace }) {
  return (
    <article className="lifecycle-row" data-testid="lifecycle-run-workspace-row">
      <dl className="lifecycle-facts">
        <Fact label="Allocation" value={item.allocationRunId || "unreported"} />
        <Fact label="Repository" value={item.repository} />
        <Fact label="Branch" value={item.branch} />
        <Fact label="Ownership" value={item.ownershipState} />
        <Fact label="Clean" value={item.cleanKnown ? String(item.clean) : "unknown"} />
        <Fact label="Reported reclaimable" value={String(item.autoReconcileable)} />
        <Fact label="Reason" value={item.reason} />
      </dl>
    </article>
  );
}

function DelegatedRow({ item }: { item: LifecycleDelegatedWorkspace }) {
  return (
    <article className="lifecycle-row" data-testid="lifecycle-delegated-row">
      <dl className="lifecycle-facts">
        <Fact label="Pollen" value={item.pollen || "unreported"} />
        <Fact label="Substrate" value={item.substrate || "unreported"} />
        <Fact label="Repository" value={item.repository || "unreported"} />
        <Fact label="Branch" value={item.currentBranch || "unreported"} />
        <Fact label="Verified" value={String(item.workspaceVerified)} />
        <Fact label="Reported reclaimable" value={String(item.autoReclaimable)} />
        <Fact label="Fruit" value={item.fruitState || "unreported"} />
        <Fact label="Reason" value={item.reason} />
      </dl>
    </article>
  );
}

function SectionRows({
  source,
  inventory,
}: {
  source: (typeof sections)[number]["source"];
  inventory: LifecycleInventory | null;
}) {
  const status = sourceStatus(inventory, source);
  if (status.state !== "available") {
    return (
      <p className="lifecycle-unavailable" data-testid={`lifecycle-unavailable-${source}`} role="status">
        {source} evidence is unavailable. {status.reason}
      </p>
    );
  }
  if (source === "fruit") {
    const items = inventory?.fruit;
    if (items == null) {
      return (
        <p className="lifecycle-unavailable" data-testid="lifecycle-unavailable-fruit" role="status">
          fruit evidence is unavailable. entries were not reported
        </p>
      );
    }
    if (items.length === 0) return <p data-testid="lifecycle-empty-fruit">{sections[0].empty}</p>;
    return items.map((item) => (
      <FruitRow key={`${item.producerKind}:${item.producerIdentity}:${item.commit}`} item={item} />
    ));
  }
  if (source === "run-workspace") {
    const items = inventory?.runWorkspaces;
    if (items == null) {
      return (
        <p className="lifecycle-unavailable" data-testid="lifecycle-unavailable-run-workspace" role="status">
          run-workspace evidence is unavailable. entries were not reported
        </p>
      );
    }
    if (items.length === 0) {
      return <p data-testid="lifecycle-empty-run-workspace">{sections[1].empty}</p>;
    }
    return items.map((item) => (
      <RunWorkspaceRow key={item.allocationRunId || item.path || item.branch} item={item} />
    ));
  }
  const items = inventory?.delegatedWorkspaces;
  if (items == null) {
    return (
      <p className="lifecycle-unavailable" data-testid="lifecycle-unavailable-delegated-workspace" role="status">
        delegated-workspace evidence is unavailable. entries were not reported
      </p>
    );
  }
  if (items.length === 0) {
    return <p data-testid="lifecycle-empty-delegated-workspace">{sections[2].empty}</p>;
  }
  return items.map((item) => (
    <DelegatedRow key={`${item.substrate}:${item.pollen}:${item.path}`} item={item} />
  ));
}

export function LifecycleInventory() {
  const inventory = useStem((s) => s.lifecycleInventory);
  const status = useStem((s) => s.lifecycleStatus);
  const error = useStem((s) => s.lifecycleError);
  const refresh = useStem((s) => s.loadLifecycleInventory);
  const [open, setOpen] = useState(false);

  return (
    <div className="lifecycle-attention">
      <button
        type="button"
        className="lifecycle-toggle"
        data-testid="lifecycle-toggle"
        aria-expanded={open}
        aria-controls="lifecycle-inventory-panel"
        onClick={() => setOpen((current) => !current)}
      >
        Retained state
      </button>
      {open ? (
        <div
          className="lifecycle-panel glass"
          id="lifecycle-inventory-panel"
          role="region"
          aria-label="Retained workspaces and Fruit"
          data-testid="lifecycle-panel"
        >
          <button type="button" className="btn-ghost" onClick={() => void refresh()}>
            Refresh retained state
          </button>
          {status === "error" ? (
            <p className="lifecycle-unavailable" data-testid="lifecycle-request-error" role="alert">
              Lifecycle inventory is unavailable. {error}
            </p>
          ) : (
            sections.map((section) => (
              <section key={section.source} aria-label={section.title}>
                <h2>{section.title}</h2>
                <SectionRows source={section.source} inventory={inventory} />
              </section>
            ))
          )}
        </div>
      ) : null}
    </div>
  );
}
