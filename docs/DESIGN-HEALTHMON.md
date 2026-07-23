# Component: HealthMon — periodic Stem self-diagnosis that runs pluggable health checks and reports/publishes their aggregate verdict.

## Purpose

`cmd/stem/internal/healthmon` is the self-contained leaf that answers one question: *is this Stem host presently fit to run work?* It defines a tiny `HealthCheck` abstraction (`monitor.go`), ships a set of concrete environment probes (`checks.go`) — Docker daemon, LLM provider availability, disk space, memory, and workspace writability — and a `Monitor` that runs them, aggregates each `CheckResult` into a single `HealthReport`, and can either return that report synchronously or drive it on an interval and publish the outcome onto the eventbus. It owns diagnosis only: it observes and reports, it never remediates.

## Responsibilities

**Does:**

- Define the `HealthCheck` contract (`Name()` + `Check(ctx) CheckResult`) so probes are pluggable and order-preserving (`monitor.go`).
- Provide `DefaultChecks()` — the shipped probe set: `DockerDaemonCheck`, `APIKeyCheck`, `DiskSpaceCheck`, `MemoryCheck`, `WorkspaceCheck` (`checks.go`).
- Aggregate probe results into a `HealthReport` (`RunOnce`): a UTC timestamp, an `Overall` boolean, and a name-keyed map of every `CheckResult`. `Overall` is false if **any** probe reports `Healthy: false`.
- Optionally run the probe set on a `time.Ticker` interval in a background goroutine (`Start`), publishing after each cycle and stopping on context cancellation.
- Publish `health-check` every cycle and `health-degraded` when the report's `Overall` is false (`runAndPublish` / `publish`).

**Does not:**

- Own CLI or HTTP surface wiring — `cmd/stem` (`cmdhealth.go`, `cmdserve.go`) constructs the monitor, registers `DefaultChecks()`, and renders/serves the report.
- Remediate, retry, or restart anything on a failing probe — a red check only changes what is reported and published.
- Retain any state between cycles: there is no memory of the previous report, so no debounce, no flap suppression, and (as built) **no `health-recovered` transition** — the `health-recovered` event type exists on the eventbus but is never published from this package.
- Configure thresholds or intervals from outside — thresholds are package constants and the interval is a `New` argument (see Limitations).
- Guarantee cross-platform probing — `MemoryCheck` and `DiskSpaceCheck` are Linux-specific (see Limitations).

## Public interface

| Symbol | Role |
| --- | --- |
| `HealthCheck` | Interface every probe implements: `Name() string`, `Check(ctx context.Context) CheckResult`. |
| `CheckResult` | One probe's verdict: `Healthy bool`, `Message string`, `Data map[string]interface{}` (carries an advisory `severity` of `info`/`warning`/`critical`, plus probe-specific fields). |
| `HealthReport` | Aggregate: `Timestamp time.Time` (UTC), `Overall bool`, `Results map[string]CheckResult` keyed by check name. |
| `Monitor` | Holds the eventbus handle, interval, and ordered check slice. |
| `New(bus *eventbus.Bus, interval time.Duration) *Monitor` | Construct; a non-positive interval is coerced to 30s; starts with an empty check set. |
| `(*Monitor).RegisterCheck(HealthCheck)` | Append a probe; nil receiver or nil check is a no-op. |
| `(*Monitor).RunOnce(ctx) HealthReport` | Run every registered probe once, synchronously, and return the aggregate. Nil-safe. |
| `(*Monitor).Start(ctx)` | Spawn a goroutine that runs+publishes immediately, then re-runs every interval until `ctx` is done. |
| `DefaultChecks() []HealthCheck` | The shipped probe set (5 checks). |
| `DockerDaemonCheck` | Runs `docker info`; unhealthy (`critical`) if the command errors. |
| `APIKeyCheck` | Healthy if `llm.AvailableProviders()` yields any local or remote provider; else unhealthy (`critical`). |
| `DiskSpaceCheck` | `statfs` on the working directory; thresholds below. |
| `MemoryCheck` | Reads `MemAvailable` from `/proc/meminfo`; warning below ~500MB. |
| `WorkspaceCheck` | Confirms `.tendril` exists, is a directory, and is writable (creates+removes a temp file). |

Package-level sentinel errors: **none.** Probe failures are conveyed as data (`CheckResult{Healthy: false, Message, Data}`), not as returned Go errors; underlying `error` values are formatted into `Message`.

## Dependencies

**Fan-out:**

- **`cmd/stem/internal/eventbus`** (`monitor.go`) — the publish target. On each `runAndPublish`, the monitor publishes an `eventbus.Event{Type, Source: "healthmon", Data: {"report": report}}`:
  - `health-check` (`EventHealthCheck`) — emitted **every** cycle with the full report.
  - `health-degraded` (`EventHealthDegraded`) — emitted **only** when `report.Overall` is false.
  - `health-recovered` (`EventHealthRecovered`) is defined on the eventbus but **not** emitted here.
  `publish` is a no-op when the bus is nil, so a monitor constructed with `New(nil, …)` runs and reports but publishes nothing.
- **`roots/llm`** (`checks.go`) — `APIKeyCheck` calls `llm.AvailableProviders()` to decide provider availability. (This is a second, probe-local fan-out beyond the eventbus.)

**Fan-in:**

- **`cmd/stem`** — the only consumer, via `newDefaultHealthMonitor(bus, 30s)` (`cmdhealth.go`), which calls `New` and registers every `DefaultChecks()` probe:
  - `tendril health [--json] [--watch]` (`cmdhealth.go`) — constructs a monitor with a live bus and calls `RunOnce` in a loop (a fresh render every 30s under `--watch`), rendering a table or JSON. Nothing subscribes to the bus here.
  - `GET /health` (`handleHealth` in `cmdserve.go`) — constructs a monitor with a **nil** bus and calls `RunOnce` once per request, returning the report as JSON with `503` when `Overall` is false.
  Both production surfaces use `RunOnce`; the interval-driven `Start` path (and thus all eventbus publishing) is exercised only by the package tests as built.

## Limitations

- **`Start` and eventbus publishing are unwired in production.** Both `cmd/stem` surfaces poll `RunOnce` on demand; nothing calls `Monitor.Start`, so the periodic loop, `health-check`/`health-degraded` emission, and any subscriber contract are covered only by `TestStartPublishesEvents`. The "periodic monitor" is, in practice, on-demand.
- **No degraded→recovered model.** The monitor keeps no prior-state, so it cannot detect recovery; `health-recovered` is never published and there is no debounce or hysteresis. A probe that flaps healthy/unhealthy re-emits `health-degraded` on every unhealthy cycle with no dedup.
- **Thresholds are hard-coded package constants** (`checks.go`), not configurable: disk critical `< 100MB` (`Healthy: false`), disk warning `< 1GB` (`Healthy: true`, `severity: warning`), memory warning `< ~500MB` (`Healthy: true`, `severity: warning`). Only the interval is an argument, and a non-positive value is silently coerced to 30s.
- **`severity` is advisory and decoupled from health.** A `warning` result is still `Healthy: true` and does not move `Overall`; only a `false` `Healthy` degrades the report. `severity` surfaces in the CLI icon and in `Data`, nothing more. `Overall` is a hard AND of booleans — a single red probe degrades the whole report regardless of severity weighting.
- **Platform-bound probes.** `DiskSpaceCheck` uses `syscall.Statfs` and `MemoryCheck` parses `/proc/meminfo` — both are Linux-only. `DockerDaemonCheck` shells out to a `docker` binary on `PATH`. `WorkspaceCheck` targets a `.tendril` path relative to the current working directory.
- **No remediation and no alerting escalation.** A failing probe reports and (when wired) publishes; it never restarts a daemon, frees disk, or rotates a key.
- **Probes run sequentially in registration order** within a single `RunOnce`; a slow probe (e.g. `docker info` under a stalled daemon) blocks the whole cycle, bounded only by the caller's context.
- **Interval trade-off.** The 30s default trades detection latency against probe cost (a `docker info` fork and a filesystem write each cycle); faster detection means more churn, and there is no jitter or backoff.

## Design & rationale

Health monitoring is modeled as **pluggable probes → an aggregate report → optional events**, deliberately kept as a leaf so the diagnosis logic has no coupling to how it is surfaced. A probe is the smallest useful unit: a `Name` and a `Check` that returns a `CheckResult`. Because `Check` takes a `context.Context` and returns data rather than an error, probes stay cancellable and uniform, and a probe's own internal failure (e.g. "cannot read /proc/meminfo") is just another unhealthy result rather than an exception that aborts the sweep. `RunOnce` folds the ordered probe set into one `HealthReport`; `Overall` is the logical AND of every probe's `Healthy`, giving callers a single boolean fitness verdict while preserving the per-probe detail and the advisory `severity` for richer display.

The two-tier verdict — a hard `Healthy` boolean plus a soft `severity` string — exists so the report can distinguish "still fit, but watch this" (disk under 1GB, memory under 500MB → `warning`, still healthy) from "not fit" (disk under 100MB, no LLM provider, Docker down → `critical`, unhealthy) without a second scoring axis leaking into the pass/fail signal that `/health` and `Overall` depend on.

Separating `RunOnce` from `Start` lets the same probe set serve pull and push consumers: request-scoped surfaces (`tendril health`, `GET /health`) call `RunOnce` and render synchronously, while a long-running Stem could call `Start` to drive the loop and turn transitions into eventbus signals. As built, the push path is defined and tested but not yet wired into a running Stem, and the `health-degraded`/`health-recovered` pair anticipates a stateful transition model that the current stateless monitor does not implement. The intent is legible in the event vocabulary; the recovery half is a later slice, and this document treats the code as authoritative over that intent.
