import { useEffect, useRef, useState } from "react";
import { useStem } from "../state/store";
import type { SproutRun } from "../lib/types";
import { ActiveSeedWork } from "./ActiveSeedWork";
import { NewWorkForm } from "./NewWorkForm";

function timeOf(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function ChatPanel() {
  const activeSessionId = useStem((s) => s.activeSessionId);
  const seedRun = useStem((s) =>
    s.activeSessionId ? s.seedRunByPhytomer[s.activeSessionId] : undefined,
  );
  const observation = useStem((s) =>
    s.activeSessionId ? s.observationByPhytomer[s.activeSessionId] : undefined,
  );
  const watch = useStem((s) =>
    s.activeSessionId ? s.watchByPhytomer[s.activeSessionId] : undefined,
  );
  const dispatch = useStem((s) => s.seedDispatch);
  const dispatchReady = useStem((s) => s.dispatchReady);
  const messages = useStem((s) =>
    s.activeSessionId ? (s.messagesBySession[s.activeSessionId] ?? []) : [],
  );
  const runs = useStem((s) =>
    s.activeSessionId ? (s.runsBySession[s.activeSessionId] ?? []) : [],
  );
  const openDrilldown = useStem((s) => s.openDrilldown);
  const selectedRunId = useStem((s) => s.drilldown?.run.runId);

  const [composingNew, setComposingNew] = useState(false);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setComposingNew(false);
  }, [activeSessionId]);

  const seedOwned = Boolean(seedRun?.handle || observation?.handle);
  const watchPhase = watch?.phase ?? "idle";
  const checking =
    Boolean(activeSessionId) &&
    !seedOwned &&
    (watchPhase === "connecting" || watchPhase === "idle");
  const watchFailed = watchPhase === "error";
  // A watch error is not the historical/non-Seed path. Only a 404 reaches not-seed.
  const showForm =
    dispatchReady &&
    (dispatch.phase === "ambiguous" ||
      dispatch.phase === "pending" ||
      (composingNew && (seedOwned || !watchFailed)) ||
      (!seedOwned && !checking && !watchFailed));

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
  }, [messages.length, runs.length, activeSessionId, showForm, seedOwned]);

  return (
    <div className="chat-zone">
      <section
        className="chat glass"
        aria-label="Seed workbench"
        data-testid="seed-workbench"
        data-watch-phase={watchPhase}
      >
        <div className="chat-head">
          <h2 className="panel-title">Workbench</h2>
          <span className="mono">{activeSessionId ?? "none"}</span>
        </div>

        <div className="chat-log" ref={logRef}>
          {(seedOwned || checking) && dispatch.phase !== "ambiguous" && !composingNew ? (
            <div className="workbench-actions">
              <button type="button" className="btn-ghost" onClick={() => setComposingNew(true)}>
                New work
              </button>
            </div>
          ) : null}
          {composingNew && seedOwned ? (
            <div className="workbench-actions">
              <button type="button" className="btn-ghost" onClick={() => setComposingNew(false)}>
                Back to active Seed
              </button>
            </div>
          ) : null}

          {showForm ? <NewWorkForm /> : null}
          {!showForm && checking ? (
            <p data-testid="seed-watch-pending">Checking whether this Phytomer is Seed-owned.</p>
          ) : null}
          {!showForm && seedOwned ? <ActiveSeedWork /> : null}
          {watchFailed && watch?.error ? (
            <p className="chat-error" data-testid="watch-error" role="alert">
              {watch.error}
            </p>
          ) : null}

          {messages.length > 0 ? (
            <details className="historical-messages" data-testid="historical-messages">
              <summary>Historical messages</summary>
              {messages.map((msg, index) => (
                <div className={`msg ${msg.role === "user" ? "user" : "assistant"}`} key={index}>
                  <div className="bubble">{msg.content}</div>
                  <div className="msg-meta">
                    {msg.role === "user" ? "you" : msg.model || "tendril"} · {timeOf(msg.createdAt)}
                  </div>
                </div>
              ))}
            </details>
          ) : null}
        </div>
      </section>

      <section className="runs glass">
        <h2 className="panel-title">Sprout runs</h2>
        <div className="runs-list">
          {runs.length === 0 ? (
            <span className="runs-empty">No executions recorded yet.</span>
          ) : (
            runs.map((run: SproutRun) => (
              <button
                className={`run-row${selectedRunId === run.runId ? " active" : ""}`}
                key={run.runId}
                onClick={() => openDrilldown(run)}
              >
                <span className={`run-dot ${run.status}`} />
                <span className="run-task" title={run.transcript}>
                  {run.transcript || run.runId}
                </span>
                <span className="run-time">{timeOf(run.startedAt)}</span>
              </button>
            ))
          )}
        </div>
      </section>
    </div>
  );
}
