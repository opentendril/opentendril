// Run review panel: structured observation first, then the task transcript
// and tool activity. Raw Event Pulse / terrarium output stays collapsed.

import { useEffect } from "react";
import {
  diagnosticLine,
  filesModifiedFromEvents,
  fruitLabel,
  observationLead,
  providerRequestLabel,
  resolvedProvider,
  toolActivityFromEvents,
  toolInvocationCount,
} from "../lib/observation";
import { useStem } from "../state/store";

function fmt(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function durationOf(start: string, end?: string): string {
  if (!end) return "still growing";
  const ms = Date.parse(end) - Date.parse(start);
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  return s < 60 ? `${s.toFixed(1)}s` : `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}

export function DrilldownDrawer() {
  const drilldown = useStem((s) => s.drilldown);
  const closeDrilldown = useStem((s) => s.closeDrilldown);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeDrilldown();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [closeDrilldown]);

  if (!drilldown) return null;
  const { run, events } = drilldown;

  const fitnessEvents = events.filter((e) => e.type === "phenotypic-selection");
  const bestScore = fitnessEvents
    .map((e) => e.data?.["bestScore"])
    .filter((v): v is number => typeof v === "number")
    .at(-1);
  const alphaScore = fitnessEvents
    .map((e) => e.data?.["alphaScore"])
    .filter((v): v is number => typeof v === "number")
    .at(-1);

  const tools = toolActivityFromEvents(events);
  const filesModified = filesModifiedFromEvents(events);
  const fruit = fruitLabel(run, filesModified);
  const diagnostic = diagnosticLine(run);
  const toolCount = toolInvocationCount(run);

  return (
    <section className="run-review glass" role="dialog" aria-label="Sprout run detail">
      <div className="drawer-head">
        <div className="drawer-title">
          <span className={`status-chip ${run.status}`}>{run.status}</span>
          <h2 title={run.runId}>{run.runId}</h2>
        </div>
        <button className="btn-ghost" onClick={closeDrilldown}>
          ✕ close
        </button>
      </div>

      <div className="drawer-body">
        <div className="drawer-section" data-testid="run-observation">
          <h3>Observation</h3>
          <p className="observation-lead">{observationLead(run)}</p>
          <div className="fact-grid">
            <div className="fact">
              <div className="k">Outcome</div>
              <div className="v">{run.outcome || "—"}</div>
            </div>
            <div className="fact">
              <div className="k">Provider</div>
              <div className="v">{resolvedProvider(run) || "—"}</div>
            </div>
            <div className="fact">
              <div className="k">Model</div>
              <div className="v">{run.model || "inherited"}</div>
            </div>
            <div className="fact">
              <div className="k">Provider request</div>
              <div className="v">{providerRequestLabel(run)}</div>
            </div>
            <div className="fact">
              <div className="k">Tools</div>
              <div className="v">{toolCount}</div>
            </div>
            <div className="fact">
              <div className="k">Duration</div>
              <div className="v">{durationOf(run.startedAt, run.finishedAt)}</div>
            </div>
            {fruit ? (
              <div className="fact">
                <div className="k">Fruit</div>
                <div className="v" title={fruit}>
                  {fruit}
                </div>
              </div>
            ) : null}
            <div className="fact">
              <div className="k">Genotype</div>
              <div className="v">{run.genotype || "default"}</div>
            </div>
            <div className="fact">
              <div className="k">Origin</div>
              <div className="v">{run.origin || "—"}</div>
            </div>
            <div className="fact">
              <div className="k">Step</div>
              <div className="v" title={run.stepId}>
                {run.stepId || "—"}
              </div>
            </div>
            <div className="fact">
              <div className="k">Started</div>
              <div className="v">{fmt(run.startedAt)}</div>
            </div>
            {typeof bestScore === "number" ? (
              <div className="fact">
                <div className="k">Best fitness</div>
                <div className="v gold">{bestScore}</div>
              </div>
            ) : null}
            {typeof alphaScore === "number" ? (
              <div className="fact">
                <div className="k">Alpha fitness</div>
                <div className="v gold">{alphaScore}</div>
              </div>
            ) : null}
          </div>
          {diagnostic ? (
            <pre className="log-block diagnostic">{diagnostic}</pre>
          ) : null}
        </div>

        <div className="drawer-section">
          <h3>Task transcript</h3>
          <pre className="log-block transcript">{run.transcript || "(empty)"}</pre>
        </div>

        <div className="drawer-section" data-testid="run-tool-activity">
          <h3>Tool activity</h3>
          {tools.length > 0 ? (
            <ul className="tool-list">
              {tools.map((tool, i) => (
                <li className="tool-row" key={`${tool.name}-${i}`}>
                  <span className="tool-name">{tool.name}</span>
                  <span className={`tool-status ${tool.status}`}>{tool.status}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="runs-empty">
              {toolCount === 0
                ? "No tool invocations recorded."
                : `${toolCount} invocation${toolCount === 1 ? "" : "s"} recorded; per-tool events are not in this session.`}
            </p>
          )}
        </div>

        <details className="run-telemetry" data-testid="run-telemetry">
          <summary>Raw Event Pulse and telemetry</summary>
          {run.error ? (
            <div className="drawer-section">
              <h3>Raw error (secondary)</h3>
              <pre className="log-block scorched">{run.error}</pre>
            </div>
          ) : null}
          <div className="drawer-section">
            <h3>Raw terrarium output</h3>
            <pre className="log-block">
              {run.output ||
                (run.status === "running" ? "(still growing…)" : "(no output captured)")}
            </pre>
          </div>
          <div className="drawer-section">
            <h3>Related telemetry ({events.length})</h3>
            {events.length === 0 ? (
              <span className="runs-empty">
                No persisted events share this run's step id.
              </span>
            ) : (
              events.map((e) => (
                <div className="event-row" key={e.id}>
                  <span className="e-type">{e.type}</span>
                  <span className="e-data">{e.data ? JSON.stringify(e.data) : ""}</span>
                </div>
              ))
            )}
          </div>
        </details>
      </div>
    </section>
  );
}
