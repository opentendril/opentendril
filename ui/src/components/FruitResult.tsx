import { matchingSeedFruit } from "../lib/fruit";
import type { PhytomerObservation, SeedRun } from "../lib/types";
import { useStem } from "../state/store";

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

  const status = observation?.status || run?.status || "";
  const executionOutcome = observation?.executionOutcome ?? run?.executionOutcome ?? "";
  const verificationOutcome = observation?.verificationOutcome ?? run?.verificationOutcome ?? "";
  const match = matchingSeedFruit(inventory, run);
  const branch = (observation?.branch || run?.branch || match?.branch || "").trim();
  const commit = (observation?.commit || run?.commit || match?.commit || "").trim();
  const hasProvenance = Boolean(branch && commit);
  const repository = (run?.fruitRepository || match?.repository || "").trim();
  const publication = observation?.publicationDiagnostic ?? run?.publicationDiagnostic;

  return (
    <section className="fruit-result" data-testid="fruit-result">
      <h3>Reported facts</h3>
      <div className="fact-grid">
        <div className="fact">
          <div className="k">Seed lifecycle status</div>
          <div className="v" data-testid="fruit-status">
            {status || "unknown"}
          </div>
        </div>
        <div className="fact">
          <div className="k">Execution outcome</div>
          <div className="v" data-testid="fruit-execution-outcome">
            {executionOutcome || "Unknown (historical or not reported)"}
          </div>
        </div>
        <div className="fact">
          <div className="k">Verification outcome</div>
          <div className="v" data-testid="verification-result">
            {verificationOutcome || "Unknown (historical or not reported)"}
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
            {repository ? (
              <div className="fact">
                <div className="k">Repository</div>
                <div className="v" data-testid="fruit-repository" title={repository}>
                  {repository}
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
          Stem did not report Fruit provenance.
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
