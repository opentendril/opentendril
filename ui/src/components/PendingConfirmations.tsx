import { useState } from "react";
import { useStem } from "../state/store";
import type { PendingConfirmation } from "../lib/types";

function absoluteExpiry(iso: string): string {
  const parsed = Date.parse(iso);
  if (!Number.isFinite(parsed)) return iso;
  return new Date(parsed).toLocaleString();
}

// Display only. A local clock reading never removes or resolves the row.
function relativeExpiry(iso: string): string | null {
  const parsed = Date.parse(iso);
  if (!Number.isFinite(parsed)) return null;
  const delta = parsed - Date.now();
  if (delta < 0) return "local clock is past this expiry";
  const minutes = Math.round(delta / 60_000);
  if (minutes < 1) return "under a minute left";
  if (minutes < 60) return `${minutes}m left`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h left`;
  return `${Math.round(hours / 24)}d left`;
}

function PendingRow({ item }: { item: PendingConfirmation }) {
  const action = useStem((s) => s.pendingAction);
  const resolve = useStem((s) => s.resolvePendingConfirmation);
  const busy = action !== null;
  const approving = action?.id === item.id && action.kind === "approve";
  const denying = action?.id === item.id && action.kind === "deny";
  const relative = relativeExpiry(item.expiresAt);

  return (
    <article className="pending-card" data-testid="pending-confirmation">
      <dl className="pending-facts">
        <dt>Pollen</dt>
        <dd data-testid="pending-pollen">{item.pollen}</dd>
        <dt>Operation</dt>
        <dd data-testid="pending-operation">{item.operationClass}</dd>
        <dt>Substrate</dt>
        <dd data-testid="pending-substrate">{item.substrate}</dd>
        <dt>Impact</dt>
        <dd data-testid="pending-impact">{item.impact}</dd>
        <dt>Expires</dt>
        <dd>
          <time dateTime={item.expiresAt} data-testid="pending-expiry">
            {absoluteExpiry(item.expiresAt)}
          </time>
          {relative ? (
            <span className="pending-expiry-relative" data-testid="pending-expiry-relative">
              {relative}
            </span>
          ) : null}
        </dd>
      </dl>
      <details className="pending-technical">
        <summary>Technical details</summary>
        <code data-testid="pending-id">{item.id}</code>
      </details>
      <div className="pending-actions">
        <button
          type="button"
          className="btn"
          aria-label={`Approve pending confirmation ${item.id}`}
          disabled={busy}
          onClick={() => void resolve(item.id, "approve")}
        >
          {approving ? "Recording approval…" : "Approve"}
        </button>
        <button
          type="button"
          className="pending-deny"
          aria-label={`Deny pending confirmation ${item.id}`}
          disabled={busy}
          onClick={() => void resolve(item.id, "deny")}
        >
          {denying ? "Recording denial…" : "Deny"}
        </button>
      </div>
    </article>
  );
}

export function PendingConfirmations() {
  const items = useStem((s) => s.pendingConfirmations);
  const error = useStem((s) => s.pendingError);
  const notice = useStem((s) => s.pendingNotice);
  const [open, setOpen] = useState(false);

  if (items.length === 0 && !error && !notice) return null;

  return (
    <div className="pending-attention" data-testid="pending-surface">
      {items.length > 0 ? (
        <button
          type="button"
          className="pending-toggle"
          data-testid="pending-attention"
          aria-expanded={open}
          aria-controls="pending-confirmation-panel"
          onClick={() => setOpen((current) => !current)}
        >
          Needs attention: {items.length}
        </button>
      ) : null}
      {notice ? (
        <p className="pending-note" data-testid="pending-notice" data-phase={notice.phase}>
          {notice.message}
        </p>
      ) : null}
      {error ? (
        <p className="pending-error" data-testid="pending-error" role="alert">
          {error}
        </p>
      ) : null}
      {open && items.length > 0 ? (
        <div
          className="pending-panel glass"
          id="pending-confirmation-panel"
          role="region"
          aria-label="Pending confirmations"
        >
          {items.map((item) => (
            <PendingRow key={item.id} item={item} />
          ))}
        </div>
      ) : null}
    </div>
  );
}
