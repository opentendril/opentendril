import { useEffect, useRef, useState } from "react";
import { useStem } from "../state/store";
import type { SproutRun } from "../lib/types";

function timeOf(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function ChatPanel() {
  const activeSessionId = useStem((s) => s.activeSessionId);
  const messages = useStem((s) =>
    s.activeSessionId ? (s.messagesBySession[s.activeSessionId] ?? []) : [],
  );
  const runs = useStem((s) =>
    s.activeSessionId ? (s.runsBySession[s.activeSessionId] ?? []) : [],
  );
  const pending = useStem((s) =>
    s.activeSessionId ? Boolean(s.chatPending[s.activeSessionId]) : false,
  );
  const chatError = useStem((s) => s.chatError);
  const sendChat = useStem((s) => s.sendChat);
  const openDrilldown = useStem((s) => s.openDrilldown);

  const [draft, setDraft] = useState("");
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
  }, [messages.length, pending, activeSessionId]);

  function submit() {
    const content = draft.trim();
    if (!content || pending || !activeSessionId) return;
    setDraft("");
    void sendChat(content);
  }

  return (
    <div className="chat-zone">
      <section className="chat glass">
        <div className="chat-head">
          <h2 className="panel-title">Session</h2>
          <span className="mono" style={{ color: "var(--ink-faint)" }}>
            {activeSessionId ?? "none"}
          </span>
        </div>

        <div className="chat-log" ref={logRef}>
          {activeSessionId === null ? (
            <p className="rail-empty">Select or sprout a Tendril to chat.</p>
          ) : messages.length === 0 && !pending ? (
            <p className="rail-empty">
              Quiet soil. Send a task below and watch it grow in the garden.
            </p>
          ) : (
            messages.map((msg, i) => (
              <div className={`msg ${msg.role === "user" ? "user" : "assistant"}`} key={i}>
                <div className="bubble">{msg.content}</div>
                <div className="msg-meta">
                  {msg.role === "user" ? "you" : (msg.model || "tendril")} ·{" "}
                  {timeOf(msg.createdAt)}
                </div>
              </div>
            ))
          )}
          {pending ? (
            <div className="sprouting">
              <span className="frond">🌿</span> Tendril is growing your task…
            </div>
          ) : null}
          {chatError ? <div className="chat-error">{chatError}</div> : null}
        </div>

        <div className="composer">
          <textarea
            value={draft}
            placeholder={
              activeSessionId ? "Describe a task for this Tendril…" : "No session selected"
            }
            disabled={!activeSessionId}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                submit();
              }
            }}
            rows={2}
          />
          <button className="btn" onClick={submit} disabled={!draft.trim() || pending}>
            Sow
          </button>
        </div>
      </section>

      <section className="runs glass">
        <h2 className="panel-title">Sprout runs</h2>
        <div className="runs-list">
          {runs.length === 0 ? (
            <span className="runs-empty">No executions recorded yet.</span>
          ) : (
            runs.map((run: SproutRun) => (
              <button className="run-row" key={run.runId} onClick={() => openDrilldown(run)}>
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
