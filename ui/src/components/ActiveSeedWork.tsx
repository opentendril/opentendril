import { type FormEvent, useState } from "react";
import { seedStatusIsTerminal } from "../lib/seed";
import { terrariumProviderFact } from "../lib/terrarium";
import type { SeedVerificationDiagnostic } from "../lib/types";
import { useStem } from "../state/store";
import { FruitResult } from "./FruitResult";

export function ActiveSeedWork() {
  const activeSessionId = useStem((s) => s.activeSessionId);
  const run = useStem((s) =>
    s.activeSessionId ? s.seedRunByPhytomer[s.activeSessionId] : undefined,
  );
  const observation = useStem((s) =>
    s.activeSessionId ? s.observationByPhytomer[s.activeSessionId] : undefined,
  );
  const knownHandle = useStem((s) =>
    s.activeSessionId ? s.knownSeedHandleByPhytomer[s.activeSessionId] : undefined,
  );
  const collectError = useStem((s) =>
    s.activeSessionId ? s.seedCollectErrorByPhytomer[s.activeSessionId] : undefined,
  );
  const watch = useStem((s) =>
    s.activeSessionId ? s.watchByPhytomer[s.activeSessionId] : undefined,
  );
  const continuation = useStem((s) => s.continuation);
  const runs = useStem((s) =>
    s.activeSessionId ? (s.runsBySession[s.activeSessionId] ?? []) : [],
  );
  const drilldownSessionId = useStem((s) => s.drilldown?.run.sessionId);
  const continueSeed = useStem((s) => s.continueSeed);
  const retryContinuation = useStem((s) => s.retryContinuation);

  const [draft, setDraft] = useState("");

  const status = observation?.status || run?.status || "";
  const terminal = seedStatusIsTerminal(status);
  const sprouts = observation?.sprouts ?? [];
  const latest = sprouts.length > 0 ? sprouts[sprouts.length - 1] : undefined;
  const terrarium = latest ? terrariumProviderFact(latest.terrariumProvider) : null;
  const diagnostics: SeedVerificationDiagnostic[] =
    observation?.verificationDiagnostics && observation.verificationDiagnostics.length > 0
      ? observation.verificationDiagnostics
      : (run?.verificationDiagnostics ?? []);
  const model = [latest?.provider, latest?.model].filter(Boolean).join(" / ");
  const iterations = observation ? observation.iterations : (run?.iterations ?? 0);
  const substrate = observation?.substrate || run?.substrate || "";
  const goal = run?.goal?.trim() ?? "";
  const handle = run?.handle || observation?.handle || knownHandle || "";
  const step = runs.find((item) => item.runId === latest?.runId);
  const continuationHere =
    continuation.phytomerId && continuation.phytomerId === activeSessionId
      ? continuation
      : undefined;
  const separateReview = Boolean(
    drilldownSessionId && activeSessionId && drilldownSessionId !== activeSessionId,
  );

  function submitContinuation(event: FormEvent) {
    event.preventDefault();
    if (status !== "running") return;
    if (continuation.phase === "pending" || continuation.phase === "ambiguous") return;
    const intent = draft.trim();
    if (!intent) return;
    void continueSeed(intent).then(() => {
      if (useStem.getState().continuation.phase === "idle") setDraft("");
    });
  }

  return (
    <div className="active-seed" data-testid="active-seed-work">
      {separateReview ? (
        <p className="seed-context" data-testid="seed-work-context">
          Continuation stays with this Seed. The open Sprout run is a separate review.
        </p>
      ) : null}

      <h3 className="workbench-goal" data-testid="seed-goal">
        {goal || (collectError ? "Seed goal unavailable" : "Reading the Seed goal from the Stem")}
      </h3>
      {collectError && !goal ? (
        <p className="chat-error" data-testid="seed-collect-error">
          {collectError}
        </p>
      ) : null}
      {watch?.phase === "error" && watch.error ? (
        <p className="chat-error" data-testid="watch-error">
          {watch.error}
        </p>
      ) : null}

      <div className="fact-grid">
        <div className="fact">
          <div className="k">Substrate</div>
          <div className="v" data-testid="seed-substrate" title={substrate}>
            {substrate || "unknown"}
          </div>
        </div>
        <div className="fact">
          <div className="k">Seed status</div>
          <div className="v" data-testid="seed-status">
            {status || "unknown"}
          </div>
        </div>
        <div className="fact">
          <div className="k">Iterations</div>
          <div className="v" data-testid="seed-iterations">
            {iterations}
          </div>
        </div>
        <div className="fact">
          <div className="k">Latest Sprout</div>
          <div className="v" data-testid="latest-sprout">
            {latest ? latest.status || "recorded" : "No Sprout recorded yet"}
          </div>
        </div>
        {latest ? (
          <div className="fact">
            <div className="k">Terrarium provider</div>
            <div className="v" data-testid="terrarium-provider" title={terrarium?.note || terrarium?.label}>
              <div>{terrarium?.label}</div>
              {terrarium?.note ? (
                <div className="provider-note" data-testid="terrarium-provider-note">
                  {terrarium.note}
                </div>
              ) : null}
            </div>
          </div>
        ) : null}
        {model ? (
          <div className="fact">
            <div className="k">Model</div>
            <div className="v" data-testid="seed-model" title={model}>
              {model}
            </div>
          </div>
        ) : null}
      </div>

      <div data-testid="verification-diagnostics">
        <h4>Verification</h4>
        {diagnostics.length === 0 ? (
          <p className="runs-empty">No verification recorded yet.</p>
        ) : (
          <ul className="verification-list">
            {diagnostics.map((item) => (
              <li key={`${item.iteration}-${item.outcome}`}>
                Iteration {item.iteration}: {item.outcome}
                {item.timedOut ? ", timed out" : ""}
                {typeof item.exitCode === "number" ? `, exit ${item.exitCode}` : ""}
                {item.message ? `. ${item.message}` : ""}
              </li>
            ))}
          </ul>
        )}
      </div>

      {(latest?.failureCategory || latest?.failureStage || latest?.diagnosticCode) ? (
        <div className="fact-grid" data-testid="sprout-failure">
          {latest.failureCategory ? (
            <div className="fact">
              <div className="k">Failure category</div>
              <div className="v">{latest.failureCategory}</div>
            </div>
          ) : null}
          {latest.failureStage ? (
            <div className="fact">
              <div className="k">Failure stage</div>
              <div className="v">{latest.failureStage}</div>
            </div>
          ) : null}
          {latest.diagnosticCode ? (
            <div className="fact">
              <div className="k">Diagnostic code</div>
              <div className="v">{latest.diagnosticCode}</div>
            </div>
          ) : null}
        </div>
      ) : null}

      {continuationHere?.message ? (
        <p className="chat-error" data-testid="continuation-error" role="alert">
          {continuationHere.message}
        </p>
      ) : null}

      {status === "running" ? (
        continuationHere?.phase === "ambiguous" ? (
          <div className="workbench-actions">
            <button
              type="button"
              className="btn"
              onClick={() => {
                void retryContinuation();
              }}
            >
              Retry continuation
            </button>
          </div>
        ) : (
          <form className="continuation-form" data-testid="continuation-form" onSubmit={submitContinuation}>
            <div className="field">
              <label htmlFor="continuation-intent">Continue this Seed</label>
              <textarea
                id="continuation-intent"
                rows={3}
                value={draft}
                disabled={continuation.phase === "pending"}
                placeholder="Further instruction for this same Seed"
                onChange={(event) => setDraft(event.target.value)}
              />
              <span className="hint">This continues the running Seed. It does not start a new one.</span>
            </div>
            <div className="workbench-actions">
              <button className="btn" type="submit" disabled={continuation.phase === "pending" || !draft.trim()}>
                {continuation.phase === "pending" ? "Sending continuation…" : "Continue work"}
              </button>
            </div>
          </form>
        )
      ) : null}

      {terminal ? <FruitResult run={run} observation={observation} /> : null}

      <details data-testid="technical-details">
        <summary>Technical details</summary>
        <dl className="technical-list">
          <div>
            <dt>Seed handle</dt>
            <dd>{handle || "unknown"}</dd>
          </div>
          <div>
            <dt>Phytomer ID</dt>
            <dd>{observation?.phytomerId || run?.phytomerId || activeSessionId || "unknown"}</dd>
          </div>
          <div>
            <dt>Sprout run ID</dt>
            <dd>{latest?.runId || "none"}</dd>
          </div>
          <div>
            <dt>Step ID</dt>
            <dd>{step?.stepId || "none"}</dd>
          </div>
          {latest?.providerDiagnostic ? (
            <div>
              <dt>Provider diagnostic</dt>
              <dd>
                {[
                  typeof latest.providerDiagnostic.statusCode === "number"
                    ? `HTTP ${latest.providerDiagnostic.statusCode}`
                    : "",
                  latest.providerDiagnostic.message || "",
                ]
                  .filter(Boolean)
                  .join(" / ") || "recorded"}
              </dd>
            </div>
          ) : null}
          {run?.error ? (
            <div>
              <dt>Seed error</dt>
              <dd>{run.error}</dd>
            </div>
          ) : null}
        </dl>
        {observation?.continuations && observation.continuations.length > 0 ? (
          <ul className="verification-list">
            {observation.continuations.map((item) => (
              <li key={item.continuationId}>
                Continuation {item.sequence}: {item.deliveryState}
              </li>
            ))}
          </ul>
        ) : null}
        <p className="hint">Raw event evidence stays in the Sprout run review.</p>
      </details>
    </div>
  );
}
