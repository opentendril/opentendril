// Drill-down drawer: everything the history endpoints know about one Sprout
// execution — the raw terrarium (Docker) output, genotype, model, timings,
// and the fitness telemetry from any phenotypic-selection events that share
// its stepId.

import { useEffect } from "react";
import {
  diagnosticLine,
  failureCategoryLabel,
  providerRequestLabel,
  resolvedProvider,
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

  return (
    <>
      <div className="drawer-scrim" onClick={closeDrilldown} />
      <div className="drawer" role="dialog" aria-label="Sprout run detail">
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
          {run.failureCategory ? (
            <div className="drawer-section">
              <h3>Observation</h3>
              <p className="observation-lead">
                {run.status === "withered" ? "Withered" : run.status} — {failureCategoryLabel(run.failureCategory)}
              </p>
            </div>
          ) : null}

          <div className="fact-grid">
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
              <div className="v">{toolInvocationCount(run)}</div>
            </div>
            <div className="fact">
              <div className="k">Outcome</div>
              <div className="v">{run.outcome || "—"}</div>
            </div>
            {run.failureCategory && run.failureCategory !== "matured" ? (
              <div className="fact">
                <div className="k">Fruit</div>
                <div className="v">none</div>
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
              <div className="v" title={run.stepId}>{run.stepId || "—"}</div>
            </div>
            <div className="fact">
              <div className="k">Started</div>
              <div className="v">{fmt(run.startedAt)}</div>
            </div>
            <div className="fact">
              <div className="k">Duration</div>
              <div className="v">{durationOf(run.startedAt, run.finishedAt)}</div>
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

          {diagnosticLine(run) ? (
            <div className="drawer-section">
              <h3>Diagnostic</h3>
              <pre className="log-block">{diagnosticLine(run)}</pre>
            </div>
          ) : null}

          <div className="drawer-section">
            <h3>Task transcript</h3>
            <pre className="log-block">{run.transcript || "(empty)"}</pre>
          </div>

          {run.error ? (
            <div className="drawer-section">
              <h3>Raw error (secondary)</h3>
              <pre className="log-block scorched">{run.error}</pre>
            </div>
          ) : null}

          <div className="drawer-section">
            <h3>Raw terrarium output</h3>
            <pre className="log-block">
              {run.output || (run.status === "running" ? "(still growing…)" : "(no output captured)")}
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
        </div>
      </div>
    </>
  );
}
