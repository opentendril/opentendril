import { type FormEvent, useState } from "react";
import type { SeedWorkInput } from "../state/store";
import { useStem } from "../state/store";

function optionalBound(raw: string): number | undefined | "invalid" {
  const trimmed = raw.trim();
  if (!trimmed || trimmed === "0") return undefined;
  if (!/^[0-9]+$/.test(trimmed)) return "invalid";
  const value = Number(trimmed);
  if (!Number.isSafeInteger(value) || value < 0) return "invalid";
  return value;
}

export function NewWorkForm() {
  const substrates = useStem((s) => s.configuredSubstrates);
  const dispatch = useStem((s) => s.seedDispatch);
  const startSeed = useStem((s) => s.startSeed);
  const retrySeedDispatch = useStem((s) => s.retrySeedDispatch);
  const discardUncertainDispatch = useStem((s) => s.discardUncertainDispatch);

  const [substrate, setSubstrate] = useState("");
  const [goal, setGoal] = useState("");
  const [executable, setExecutable] = useState("");
  const [args, setArgs] = useState<string[]>([]);
  const [maxIterations, setMaxIterations] = useState("");
  const [timeoutSeconds, setTimeoutSeconds] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  const pending = dispatch.phase === "pending";

  if (dispatch.phase === "ambiguous" && dispatch.request) {
    const request = dispatch.request;
    return (
      <div className="dispatch-uncertain" data-testid="dispatch-ambiguous" role="alert">
        <p>{dispatch.message}</p>
        <p className="workbench-retained">
          <span className="k">Substrate</span> {request.substrate}
        </p>
        <p className="workbench-retained">
          <span className="k">Task</span> {request.goal}
        </p>
        <div className="workbench-retained">
          <span className="k">Verifier argv</span>
          <ol className="argv-list">
            {request.verify.map((token, index) => (
              <li key={index}>{token}</li>
            ))}
          </ol>
        </div>
        <p className="technical-id">
          Idempotency key{" "}
          <span data-testid="dispatch-idempotency-key">{request.idempotencyKey}</span>
        </p>
        <div className="workbench-actions">
          <button
            type="button"
            className="btn"
            onClick={() => {
              void retrySeedDispatch();
            }}
            disabled={pending}
          >
            Retry dispatch
          </button>
          <button type="button" className="btn-ghost" onClick={discardUncertainDispatch}>
            Discard uncertain dispatch
          </button>
        </div>
      </div>
    );
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (pending || dispatch.phase === "ambiguous") return;
    const task = goal.trim();
    if (!substrate) {
      setFormError("Choose a configured Substrate.");
      return;
    }
    if (!task) {
      setFormError("Describe the task.");
      return;
    }
    if (!executable.trim()) {
      setFormError("Enter the verifier executable.");
      return;
    }
    const iterations = optionalBound(maxIterations);
    const timeout = optionalBound(timeoutSeconds);
    if (iterations === "invalid" || timeout === "invalid") {
      setFormError("Seed bounds must be whole numbers. Zero or empty uses the Stem default.");
      return;
    }
    const verify = [executable, ...args.filter((arg) => arg !== "")];
    const input: SeedWorkInput = { substrate, goal: task, verify };
    if (iterations !== undefined) input.maxIterations = iterations;
    if (timeout !== undefined) input.timeoutSeconds = timeout;
    setFormError(null);
    void startSeed(input);
  }

  return (
    <form className="new-work" onSubmit={submit} data-testid="new-work-form">
      <p className="workbench-intro">
        Start work by dispatching a detached Seed. The Stem creates the canonical
        Phytomer. Greenhouse does not run the verifier.
      </p>

      {dispatch.phase === "rejected" && dispatch.message ? (
        <p className="chat-error" data-testid="dispatch-rejected" role="alert">
          {dispatch.message}
        </p>
      ) : null}
      {formError ? (
        <p className="chat-error" data-testid="new-work-error" role="alert">
          {formError}
        </p>
      ) : null}

      <div className="field">
        <label htmlFor="work-substrate">Substrate</label>
        <select
          id="work-substrate"
          value={substrate}
          disabled={pending || substrates.length === 0}
          onChange={(event) => setSubstrate(event.target.value)}
        >
          <option value="">Select a configured Substrate</option>
          {substrates.map((name) => (
            <option value={name} key={name}>
              {name}
            </option>
          ))}
        </select>
        {substrates.length === 0 ? (
          <span className="hint">No configured Substrate is available.</span>
        ) : (
          <span className="hint">Configured names only. An internal identifier is not required.</span>
        )}
      </div>

      <div className="field">
        <label htmlFor="work-goal">Task</label>
        <textarea
          id="work-goal"
          rows={4}
          value={goal}
          disabled={pending}
          placeholder="What should this Seed accomplish?"
          onChange={(event) => setGoal(event.target.value)}
        />
      </div>

      <fieldset className="verifier-fields" disabled={pending}>
        <legend>Verification</legend>
        <div className="field">
          <label htmlFor="work-executable">Verifier executable</label>
          <input
            id="work-executable"
            value={executable}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setExecutable(event.target.value)}
          />
        </div>
        {args.map((arg, index) => (
          <div className="arg-row" key={index}>
            <div className="field">
              <label htmlFor={`work-arg-${index}`}>Argument {index + 1}</label>
              <input
                id={`work-arg-${index}`}
                value={arg}
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => {
                  const next = [...args];
                  next[index] = event.target.value;
                  setArgs(next);
                }}
              />
            </div>
            <button
              type="button"
              className="btn-ghost"
              aria-label={`Remove argument ${index + 1}`}
              onClick={() => setArgs(args.filter((_, item) => item !== index))}
            >
              Remove
            </button>
          </div>
        ))}
        <button
          type="button"
          className="btn-ghost"
          onClick={() => setArgs([...args, ""])}
        >
          Add argument
        </button>
        <span className="hint">
          Each field is one argv token. Greenhouse does not build a shell command.
        </span>
      </fieldset>

      <details className="seed-bounds" data-testid="seed-bounds">
        <summary>Advanced Seed bounds</summary>
        <div className="field">
          <label htmlFor="work-iterations">Max iterations</label>
          <input
            id="work-iterations"
            inputMode="numeric"
            value={maxIterations}
            disabled={pending}
            onChange={(event) => setMaxIterations(event.target.value)}
          />
        </div>
        <div className="field">
          <label htmlFor="work-timeout">Timeout seconds</label>
          <input
            id="work-timeout"
            inputMode="numeric"
            value={timeoutSeconds}
            disabled={pending}
            onChange={(event) => setTimeoutSeconds(event.target.value)}
          />
        </div>
        <span className="hint">Zero or empty leaves the bound with the Stem.</span>
      </details>

      <div className="workbench-actions">
        <button className="btn" type="submit" disabled={pending}>
          {pending ? "Dispatching Seed…" : "Start work"}
        </button>
      </div>
    </form>
  );
}
