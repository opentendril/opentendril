# Component: Dormancy — evaluating and reporting signs of life for growing Sprouts

## Purpose

`cmd/stem/internal/dormancy` evaluates whether a Sprout that is still growing has stopped showing signs of life (a "dormant growth"). It provides the scratch test logic to accrue suspicion based on a run's own observed cadence, exposing ports for orchestration to inject probes and consume reports, while remaining a strict leaf package that cannot end a run.

## Responsibilities

**Does:**

- **Accrue suspicion from the run's own observed cadence**, never a fixed threshold. It uses the Welford mean and variance plus the widest observed gap. The envelope is `max(widest, mean + 3σ)`, and suspicion is `silence/envelope − 1`, floored at zero.
- Maintain **one duration constant** (`coldStartCadence`), which is consulted only until three gaps have been seen, and never again for that run. Other constants are **dimensionless** (a multiple of the run's own spread, a count of envelopes), meaning the design adapts to the model's current speed rather than relying on brittle time thresholds.
- Treat **every signal as a suppressor only**. A sign of life lowers suspicion; the absence of one is never evidence of death. An identical repeated tool call and a static diff are inert (they neither suppress nor accelerate).
- Expose two function ports (`ScratchProbe` and `DormancyCapture`), decoupled from the orchestrator. These are ports rather than imports because the package is a pure leaf and must not reach back into the orchestrator.
- Increase verbosity (by reporting `sprout-dormant` events) when suspicion rises, ending nothing.

**Does not:**

- **End any run.** The package is structurally prevented from doing so by an import allowlist (`leaf_test.go`), which ensures no process, terrarium, or network handles can be reached. This is the structural guarantee that dormancy increases verbosity and ends nothing.
- Import the orchestrator or any non-leaf OpenTendril packages.
- Decide what constitutes a terminal event.

## Public interface

| Symbol | Role |
| --- | --- |
| `ScratchProbe` | Function port for the orchestrator to inject an active test for signs of life. |
| `DormancyCapture` | Function port for the orchestrator to consume the resulting dormancy evaluation. |

## Dependencies

**Fan-out:**

- **`cmd/stem/internal/eventbus`** — The bus is the one outward reach, used solely to publish the `sprout-dormant` report.

**Fan-in:**

- **`cmd/stem/internal/conductor`** — instantiates and drives the dormancy watcher, providing the `ScratchProbe` and `DormancyCapture` ports.

## Design & rationale

- **Accrued cadence vs threshold:** Distinguishing a stopped growth from a slow one is undecidable. A fixed timeout is always wrong for some model or workload. By using the run's own cadence (`max(widest, mean + 3σ)`), the watcher adapts to the model's current speed. The only time-based constant is the `coldStartCadence`, which bootstraps the watcher until three events establish a real baseline. All other tuning values are dimensionless multipliers of that baseline.
- **Suppressors only:** We can prove a Sprout is alive (it did something), but we cannot prove it is dead (it might just be thinking). Therefore, signals only lower suspicion. Static diffs or identical repeated tool calls provide no new evidence either way, so they are inert.
- **Reporting, not killing:** A spent growth budget or high dormancy suspicion never kills a container. Dormancy only increases verbosity. The structural guarantee that this package cannot kill a run is held by `leaf_test.go`'s import allowlist, which prevents any dependencies that could terminate a process or container.
