import { useState } from "react";
import { useStem } from "../state/store";
import type { Session } from "../lib/types";

function relativeTime(iso: string): string {
  const delta = Date.now() - Date.parse(iso);
  if (!Number.isFinite(delta)) return "";
  const mins = Math.floor(delta / 60_000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function shortId(session: Session): string {
  return session.sessionId.replace(/^tendril-/, "");
}

export function SessionRail() {
  const sessions = useStem((s) => s.sessions);
  const activeSessionId = useStem((s) => s.activeSessionId);
  const runsBySession = useStem((s) => s.runsBySession);
  const selectedRunId = useStem((s) => s.drilldown?.run.runId);
  const selectedRunSessionId = useStem((s) => s.drilldown?.run.sessionId);
  const selectSession = useStem((s) => s.selectSession);
  const createSession = useStem((s) => s.createSession);
  const openDrilldown = useStem((s) => s.openDrilldown);
  const [creating, setCreating] = useState(false);

  return (
    <aside className="rail glass">
      <div className="rail-head">
        <h2 className="panel-title">Tendrils</h2>
        <button
          className="btn"
          disabled={creating}
          onClick={async () => {
            setCreating(true);
            try {
              await createSession();
            } finally {
              setCreating(false);
            }
          }}
        >
          {creating ? "Sprouting…" : "+ Sprout"}
        </button>
      </div>

      <div className="rail-list">
        {sessions.length === 0 ? (
          <p className="rail-empty">
            No Tendrils yet. Sprout one to open a session, or let the CLI /
            MCP surfaces grow their own — they will appear here.
          </p>
        ) : (
          sessions.map((session) => (
            <div
              key={session.sessionId}
              className="session-group"
              role="group"
              aria-label={`Phytomer ${shortId(session)}`}
            >
              <button
                className={`session-card ${session.sessionId === activeSessionId ? "active" : ""}`}
                onClick={() => selectSession(session.sessionId)}
              >
                <span className="sid" title={session.sessionId}>
                  {shortId(session)}
                </span>
                <span className="meta">
                  <span className="origin-chip">{session.origin}</span>
                  <span>{relativeTime(session.lastActiveAt)}</span>
                </span>
                <span
                  className={`substrate-chip ${session.preferences?.substrate ? "set" : "unset"}`}
                  title={
                    session.preferences?.substrate
                      ? `Substrate ${session.preferences.substrate}`
                      : "No Substrate bound"
                  }
                >
                  {session.preferences?.substrate || "no substrate"}
                </span>
              </button>
              {(runsBySession[session.sessionId] ?? []).length > 0 ? (
                <div className="session-runs" aria-label={`Sprout runs for ${shortId(session)}`}>
                  {(runsBySession[session.sessionId] ?? []).map((run) => {
                    const target = {
                      ...run,
                      sessionId: run.sessionId ?? session.sessionId,
                    };
                    return (
                      <button
                        className={`session-run-row${selectedRunId === run.runId && selectedRunSessionId === session.sessionId ? " active" : ""}`}
                        key={run.runId}
                        aria-label={`Open Sprout run ${run.transcript || run.runId} in Phytomer ${shortId(session)}`}
                        onClick={() => openDrilldown(target)}
                      >
                        <span className={`session-run-dot ${run.status}`} />
                        <span className="session-run-task" title={run.transcript}>
                          {run.transcript || run.runId}
                        </span>
                        <span className="session-run-status">{run.status}</span>
                      </button>
                    );
                  })}
                </div>
              ) : null}
            </div>
          ))
        )}
      </div>
    </aside>
  );
}
