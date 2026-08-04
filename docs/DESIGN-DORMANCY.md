# Component: Dormancy — evaluating and reporting signs of life for growing Sprouts

## Purpose

`cmd/stem/internal/dormancy` evaluates whether a Sprout that is still growing has stopped showing signs of life (a "dormant growth"). It accrues suspicion from a run's own observed cadence, publishes a report when that suspicion crosses a reporting level, and retains evidence about the silent run through a caller-supplied capture. Both the active probe and the capture are injected as function ports, so the package stays a strict leaf that cannot end a run.

## Responsibilities

**Does:**

- **Accrue suspicion from the run's own observed cadence**, never a fixed threshold. It uses the Welford mean and variance plus the widest observed gap. The envelope is `max(widest, mean + 3σ)`, and suspicion is `silence/envelope − 1`, floored at zero.
- Maintain **one duration constant** (`coldStartCadence`), which is consulted only until three gaps have been seen, and never again for that run. Other constants are **dimensionless** (a multiple of the run's own spread, a count of envelopes), meaning the design adapts to the model's current speed rather than relying on brittle time thresholds.
- Treat **every signal as a suppressor only**. A sign of life lowers suspicion; the absence of one is never evidence of death. An identical repeated tool call and a static diff are inert (they neither suppress nor accelerate).
- Expose two function ports (`ScratchProbe` and `DormancyCapture`), decoupled from the orchestrator. These are ports rather than imports because the package is a pure leaf and must not reach back into the orchestrator.
- **Increase verbosity when a run goes quiet, and end nothing.** Crossing the reporting level publishes one `sprout-dormant` event per episode of silence, re-armed by the next sign of life — never once per tick.
- **Retain evidence before announcing the silence.** On crossing, `DormancyCapture` is invoked *before* the report is published, because `Publish` runs handlers synchronously on the publishing goroutine: capturing afterwards would guarantee every subscriber saw the report while the evidence did not yet exist. The report waits on the capture, bounded by the capture's own timeout.
- **Publish the report even when the capture fails.** "The capture could not be taken" is itself evidence; losing the report as well would lose both.

**Does not:**

- **End any run.** The package is structurally prevented from doing so by an import allowlist (`leaf_test.go`), which ensures no process, terrarium, or network handles can be reached. This is the structural guarantee that dormancy increases verbosity and ends nothing.
- Import the orchestrator or any non-leaf OpenTendril packages.
- Decide what constitutes a terminal event.

## Public interface

| Symbol | Role |
| --- | --- |
| `RunKey` | Identifies one watched run by step and session — how bus events are correlated to a record. |
| `Config` | Wires a `Watcher`. Every field is optional: with none set it accrues suspicion silently. |
| `Watcher` | Holds per-run cadence records and the accrued suspicion derived from them. |
| `New` | Construct a `Watcher` from a `Config`. |
| `ScratchProbe` | Function port the caller supplies so the workspace can be actively probed for a sign of life. Injected rather than imported, so the package stays a leaf. |
| `DormancyCapture` | Function port the caller supplies to capture and retain evidence about a run that has gone quiet — container stderr, last request and response, Terrarium state, a process listing. Invoked once per report, **before** the report is published. A non-nil error does not suppress the report. |
| `(*Watcher).Subscribe` | Attach to a bus for the run's lifetime; returns the unsubscribe closure used to detach at the end of the run. |
| `(*Watcher).Observe` | Fold one event into the watched run's record — the suppressors arrive here. |
| `(*Watcher).Tick` | Evaluate every watched run at a supplied instant. Tests drive this directly with a synthetic clock. |
| `(*Watcher).Start` | The production loop: `Tick` on the configured interval until stopped. |
| `(*Watcher).Suspicion` | The accrued level for one run at an instant, in units of that run's own envelope. |
| `(*Watcher).Reported` / `(*Watcher).ReportedAny` | How many dormancy reports a run has produced, and whether any watched run has gone dormant. |

## Dependencies

**Fan-out:**

- **`cmd/stem/internal/eventbus`** — The bus is the one outward reach, used solely to publish the `sprout-dormant` report.

**Fan-in:**

- **`cmd/stem/internal/conductor`** — instantiates and drives the dormancy watcher, providing the `ScratchProbe` and `DormancyCapture` ports.

## Design & rationale

- **Accrued cadence vs threshold:** Distinguishing a stopped growth from a slow one is undecidable. A fixed timeout is always wrong for some model or workload. By using the run's own cadence (`max(widest, mean + 3σ)`), the watcher adapts to the model's current speed. The only time-based constant is the `coldStartCadence`, which bootstraps the watcher until three observed **gaps** establish a real baseline — gaps, not events, so four signals are needed before a run is judged on its own pacing. All other tuning values are dimensionless multipliers of that baseline.
- **Suppressors only:** We can prove a Sprout is alive (it did something), but we cannot prove it is dead (it might just be thinking). Therefore, signals only lower suspicion. Static diffs or identical repeated tool calls provide no new evidence either way, so they are inert.
- **Reporting, not killing:** A spent growth budget or high dormancy suspicion never kills a container. Dormancy only increases verbosity. The structural guarantee that this package cannot kill a run is held by `leaf_test.go`'s import allowlist, which prevents any dependencies that could terminate a process or container.
- **Capture before report, not after:** the silent run is the one the least is known about, so a report is worth little without the evidence behind it. Because bus handlers run synchronously on the publishing goroutine, capturing after publishing would guarantee that every subscriber saw the report before the artifact existed. The report therefore waits on the capture. A run already silent for several times its own widest gap is not harmed by a bounded wait, and a report nobody can find evidence for is worth less than a late one.
- **The capture is redacted:** container stderr and a last request are exactly where a credential surfaces, so every captured field passes through the telemetry scrubber before reaching disk. The capture and the telemetry boundary apply the same patterns rather than two copies that can drift.
