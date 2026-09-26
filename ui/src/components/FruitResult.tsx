import { matchingSeedFruit, seedReportsFruitProvenance } from "../lib/fruit";
import type { PhytomerObservation, SeedRun, SeedVerificationDiagnostic } from "../lib/types";
import { useStem } from "../state/store";

function diagnosticsFor(
  run?: SeedRun,
  observation?: PhytomerObservation,
): SeedVerificationDiagnostic[] {
  if (run?.verificationDiagnostics && run.verificationDiagnostics.length > 0) {
    return run.verificationDiagnostics;
  }
  return observation?.verificationDiagnostics ?? [];
}

export function FruitResult({
  run,
  observation,
}: {
  run?: SeedRun;
  observation?: PhytomerObservation;
}) {
  const inventory = useStem((s) => s.fruitInventory);
  const fruitStatus = useStem((s) => s.fruitStatus);
  const fruitError = useStem((s) => s.fruitError);

  const status = run?.status || observation?.status || "";
  const branch = (run?.branch || observation?.branch || "").trim();
  const commit = (run?.commit || observation?.commit || "").trim();
  const hasProvenance = Boolean(branch && commit) || seedReportsFruitProvenance(run);
  const diagnostics = diagnosticsFor(run, observation);
  const verification = diagnostics[diagnostics.length - 1];
  const publication = run?.publicationDiagnostic || observation?.publicationDiagnostic;
  const match = hasProvenance ? matchingSeedFruit(inventory, run) : null;

  return (
    <section className="fruit-result" data-testid="fruit-result">
      <h3>Result</h3>
      <div className="fact-grid">
        <div className="fact">
          <div className="k">Status</div>
          <div className="v" data-testid="fruit-status">
            {status || "unknown"}
          </div>
        </div>
        <div className="fact">
          <div className="k">Verification result</div>
          <div className="v" data-testid="verification-result">
            {verification ? verification.outcome : "No verification recorded"}
          </div>
        </div>
      </div>

      {publication ? (
        <div className="publication-diagnostic" data-testid="publication-diagnostic">
          <h4>Publication diagnostic</h4>
          <div className="fact-grid">
            <div className="fact">
              <div className="k">Failure category</div>
              <div className="v">{publication.failureCategory}</div>
            </div>
            <div className="fact">
              <div className="k">Execution status</div>
              <div className="v">{publication.executionStatus}</div>
            </div>
            <div className="fact">
              <div className="k">Phase</div>
              <div className="v">{publication.phase}</div>
            </div>
            <div className="fact">
              <div className="k">Outcome</div>
              <div className="v">{publication.outcome}</div>
            </div>
            <div className="fact">
              <div className="k">Retry</div>
              <div className="v">{publication.retrySafe ? "retry safe" : "not retry safe"}</div>
            </div>
          </div>
          {publication.message ? <p>{publication.message}</p> : null}
          {publication.requestId ? (
            <p className="technical-id">Request {publication.requestId}</p>
          ) : null}
        </div>
      ) : null}

      {hasProvenance ? (
        <div data-testid="fruit-provenance">
          <div className="fact-grid">
            {run?.fruitRepository ? (
              <div className="fact">
                <div className="k">Repository</div>
                <div className="v" data-testid="fruit-repository" title={run.fruitRepository}>
                  {run.fruitRepository}
                </div>
              </div>
            ) : null}
            <div className="fact">
              <div className="k">Branch</div>
              <div className="v" data-testid="fruit-branch" title={branch}>
                {branch}
              </div>
            </div>
            <div className="fact">
              <div className="k">Commit</div>
              <div className="v" data-testid="fruit-commit" title={commit}>
                {commit}
              </div>
            </div>
            {run?.fruitPublicationState ? (
              <div className="fact">
                <div className="k">Publication state</div>
                <div className="v" data-testid="fruit-publication-state">
                  {run.fruitPublicationState}
                </div>
              </div>
            ) : null}
          </div>
          {fruitStatus === "ready" ? (
            <div className="review-state">
              <div className="k">Review state</div>
              <div data-testid="fruit-review-state">
                {match
                  ? match.reviewState
                  : "Fruit inventory did not return one matching Seed record."}
              </div>
              {match?.unknownReason ? (
                <div data-testid="fruit-unknown-reason">{match.unknownReason}</div>
              ) : null}
              {match?.pullRequest ? (
                <div data-testid="fruit-pull-request">Pull request {match.pullRequest}</div>
              ) : null}
            </div>
          ) : fruitStatus === "error" ? (
            <p className="chat-error" data-testid="fruit-review-state">
              Fruit inventory could not be read{fruitError ? `: ${fruitError}` : "."}
            </p>
          ) : (
            <p data-testid="fruit-review-state">Reading Fruit inventory.</p>
          )}
        </div>
      ) : (
        <p data-testid="fruit-absent">
          Stem did not report Fruit branch and commit provenance.
        </p>
      )}

      {run?.diff ? (
        <details data-testid="seed-diff">
          <summary>Unified diff</summary>
          <pre className="log-block">{run.diff}</pre>
        </details>
      ) : null}
      {run?.logs ? (
        <details data-testid="seed-logs">
          <summary>Seed logs</summary>
          <pre className="log-block">{run.logs}</pre>
        </details>
      ) : null}
    </section>
  );
}
