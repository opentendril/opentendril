import { useEffect } from "react";
import { useStem } from "../state/store";
import { useConnection } from "../state/connection";
import { SessionRail } from "./SessionRail";
import { GardenCanvas } from "./Garden/GardenCanvas";
import { EventTicker } from "./EventTicker";
import { ChatPanel } from "./ChatPanel";
import { DrilldownDrawer } from "./DrilldownDrawer";
import { TendrilMark } from "./TendrilMark";

export function CommandCenter() {
  const boot = useStem((s) => s.boot);
  const shutdown = useStem((s) => s.shutdown);
  const wsStatus = useStem((s) => s.wsStatus);
  const hydration = useStem((s) => s.hydration);
  const hydrationError = useStem((s) => s.hydrationError);
  const drilldown = useStem((s) => s.drilldown);
  const operatorName = useConnection((s) => s.operatorName);
  const resetConnection = useConnection((s) => s.reset);

  useEffect(() => {
    boot();
    return shutdown;
  }, [boot, shutdown]);

  const connLabel =
    wsStatus === "open"
      ? "EventBus live"
      : wsStatus === "connecting"
        ? "Reaching Stem…"
        : "EventBus dormant — reconnecting";

  return (
    <div className="shell">
      <div className="brand glass">
        <TendrilMark className="brand-mark" />
        <span className="brand-name">
          Open<em>Tendril</em>
        </span>
      </div>

      <header className="topbar glass">
        <span className="conn-badge">
          <span className={`conn-dot ${wsStatus}`} />
          {connLabel}
          {hydration === "error" && hydrationError ? (
            <span style={{ color: "var(--scorch)" }}>
              · hydration failed: {hydrationError}
            </span>
          ) : null}
        </span>
        <span className="conn-badge">
          {operatorName ? <span>{operatorName}</span> : null}
          <button
            className="btn-ghost"
            onClick={() => {
              shutdown();
              resetConnection();
            }}
            title="Forget this Stem and return to onboarding"
          >
            Uproot
          </button>
        </span>
      </header>

      <SessionRail />

      <main className="garden-zone">
        <div style={{ position: "relative", display: "contents" }}>
          <GardenCanvas />
        </div>
        <EventTicker />
      </main>

      <ChatPanel />

      {hydration === "hydrating" ? (
        <div className="hydrating-veil">
          <span className="frond" style={{ display: "inline-block" }}>
            🌱
          </span>
          re-growing state from history…
        </div>
      ) : null}

      {drilldown ? <DrilldownDrawer /> : null}
    </div>
  );
}
