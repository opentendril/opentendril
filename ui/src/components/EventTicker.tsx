import { useStem } from "../state/store";
import type { StemEvent } from "../lib/types";

const TYPE_COLOR: Record<string, string> = {
  "sprout-emerged": "var(--chlorophyll)",
  "sprout-matured": "var(--bloom)",
  "sprout-withered": "var(--wither)",
  "parallel-sprouting": "var(--sap)",
  "mycelial-merge": "var(--spore)",
  "phenotypic-selection": "var(--bloom)",
  "sequence-complete": "var(--bloom)",
  "sequence-failure": "var(--scorch)",
  "terrarium-oom": "var(--scorch)",
  "terrarium-timeout": "var(--scorch)",
  "api-key-invalid": "var(--scorch)",
  "health-degraded": "var(--scorch)",
  "health-recovered": "var(--chlorophyll)",
  "thought-branch": "var(--spore)",
};

function detailOf(event: StemEvent): string {
  const d = event.data ?? {};
  const parts: string[] = [];
  if (typeof d["phase"] === "string") parts.push(String(d["phase"]));
  if (typeof d["stepId"] === "string") parts.push(String(d["stepId"]));
  if (typeof d["branchName"] === "string") parts.push(String(d["branchName"]));
  if (typeof d["sequence"] === "string") parts.push(`seq=${d["sequence"]}`);
  if (typeof d["bestScore"] === "number") parts.push(`best=${d["bestScore"]}`);
  if (typeof d["alphaScore"] === "number") parts.push(`alpha=${d["alphaScore"]}`);
  if (typeof d["detail"] === "string" && d["detail"]) parts.push(String(d["detail"]));
  if (event.content) parts.push(String(event.content));
  if (parts.length === 0 && event.source) parts.push(event.source);
  return parts.join(" · ").slice(0, 220);
}

export function EventTicker() {
  const ticker = useStem((s) => s.ticker);

  return (
    <section className="ticker glass">
      <h2 className="panel-title">Event pulse</h2>
      <div className="ticker-list">
        {ticker.length === 0 ? (
          <span className="runs-empty">Listening to the EventBus…</span>
        ) : (
          [...ticker].reverse().map((event, i) => (
            <div className="tick" key={`${ticker.length - i}`}>
              <span className="t-time">
                {event.timestamp
                  ? new Date(event.timestamp).toLocaleTimeString([], {
                      hour: "2-digit",
                      minute: "2-digit",
                      second: "2-digit",
                    })
                  : "--:--"}
              </span>
              <span className="t-type" style={{ color: TYPE_COLOR[event.type] ?? "var(--ink-mute)" }}>
                {event.type}
              </span>
              <span className="t-detail">{detailOf(event)}</span>
            </div>
          ))
        )}
      </div>
    </section>
  );
}
