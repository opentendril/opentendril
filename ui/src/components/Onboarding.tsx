// Welcome flow: a non-technical operator points the Command Center at a Stem
// and pastes the operator key — no .env editing. The key is verified live
// against /health (no auth) and /v1/sessions (bearer-authenticated) before we
// let them in, then persisted to localStorage.

import { useState } from "react";
import { stemApi, StemApiError } from "../lib/api";
import { useConnection } from "../state/connection";
import { TendrilMark } from "./TendrilMark";

type Status =
  | { kind: "idle" }
  | { kind: "busy"; text: string }
  | { kind: "err"; text: string }
  | { kind: "ok"; text: string };

export function Onboarding() {
  const configure = useConnection((s) => s.configure);
  const [operatorName, setOperatorName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [status, setStatus] = useState<Status>({ kind: "idle" });

  async function takeRoot() {
    const conn = { baseUrl: baseUrl.trim().replace(/\/+$/, ""), apiKey: apiKey.trim() };
    setStatus({ kind: "busy", text: "Reaching the Stem…" });
    try {
      await stemApi.health(conn);
    } catch (err) {
      setStatus({
        kind: "err",
        text:
          err instanceof StemApiError && err.status === 503
            ? "The Stem answered but reports degraded health — you can still connect after checking it."
            : "No Stem answered at that address. Is `tendril serve` running?",
      });
      if (!(err instanceof StemApiError)) return;
      if (err.status !== 503) return;
    }

    setStatus({ kind: "busy", text: "Verifying operator key…" });
    try {
      await stemApi.listSessions(conn);
    } catch (err) {
      if (err instanceof StemApiError && err.status === 401) {
        setStatus({
          kind: "err",
          text: "The Stem rejected that key. Paste the BOTANIST_KEY value your administrator gave you.",
        });
        return;
      }
      setStatus({ kind: "err", text: `Could not list sessions: ${String(err)}` });
      return;
    }

    setStatus({ kind: "ok", text: "Rooted. Entering the garden…" });
    window.setTimeout(
      () => configure({ baseUrl: conn.baseUrl, apiKey: conn.apiKey, operatorName: operatorName.trim() }),
      450,
    );
  }

  const busy = status.kind === "busy";

  return (
    <div className="onboard-stage">
      <form
        className="onboard glass"
        onSubmit={(e) => {
          e.preventDefault();
          void takeRoot();
        }}
      >
        <TendrilMark className="onboard-mark" />
        <h1>
          Welcome to the <em>OpenTendril</em> Command Center
        </h1>
        <p className="lede">
          One living view of every Tendril your Stem is growing. Tell the
          garden where its roots are — you will only do this once.
        </p>

        <div className="field">
          <label htmlFor="ob-name">Your name (optional)</label>
          <input
            id="ob-name"
            value={operatorName}
            onChange={(e) => setOperatorName(e.target.value)}
            placeholder="Operator"
            autoComplete="name"
          />
        </div>

        <div className="field">
          <label htmlFor="ob-url">Stem address</label>
          <input
            id="ob-url"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="Leave empty for this origin (recommended)"
            spellCheck={false}
          />
          <span className="hint">
            Empty means the Stem that serves this page (or the dev proxy).
            Only set a full URL like http://stem.local:8080 if the Stem lives
            elsewhere and allows cross-origin requests.
          </span>
        </div>

        <div className="field">
          <label htmlFor="ob-key">Botanist key</label>
          <input
            id="ob-key"
            type="password"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder="BOTANIST_KEY"
            spellCheck={false}
            autoComplete="off"
          />
          <span className="hint">
            Stored only in this browser. Leave empty if your Stem runs without
            authentication.
          </span>
        </div>

        <div
          className={`onboard-status ${status.kind === "err" ? "err" : status.kind === "ok" ? "ok" : status.kind === "busy" ? "busy" : ""}`}
        >
          {status.kind !== "idle" ? status.text : ""}
        </div>

        <button className="btn-primary" type="submit" disabled={busy}>
          {busy ? "Taking root…" : "Take root"}
        </button>
      </form>
    </div>
  );
}
