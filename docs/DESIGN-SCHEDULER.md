# Component: Scheduler — in-process cron ticker that grows Sequences or ad-hoc Sprouts

## Purpose

`cmd/stem/internal/scheduler` is the self-contained leaf that implements the unattended scheduled growth daemon. It parses standard 5-field cron expressions, loads `.tendril/schedules.yaml`, and evaluates due runs on a 30-second tick loop. It stays decoupled from the rest of the system by injecting a `Firer` seam for actual execution.

## Responsibilities

**Does:**

- Load and parse workspace schedule configurations into enabled entries (`config.go`).
- Parse 5-field cron specs supporting ranges, steps, lists, and standard day-of-month/day-of-week semantics (`cron.go`).
- Evaluate local-timezone cron specs to compute the next valid fire time.
- Run a 30-second ticker loop to check and fire due entries without busy-polling (`scheduler.go`).
- Enforce `overlap: skip` (drop concurrent fires) or `overlap: queue` (coalesce and enqueue one run) policies (`scheduler.go`).
- Retry withered runs up to `Retries` times with a constant 5-second backoff (`scheduler.go`).

**Does not:**

- Depend on conductor or core (it uses an injected `Firer` seam to remain a pure leaf).
- Manage long-running process lifetimes (it is tied to a standard context and stops when cancelled).
- Persist queued or missed runs across restarts (everything pending or in-flight is kept in memory).

## Public interface

| Symbol | Role |
| --- | --- |
| `Scheduler` | In-process ticker loop for evaluating and launching scheduled runs. |
| `New` | Constructor accepting config, an injected firer, and a logger. |
| `Firer` / `FirerFunc` | The dependency-injected seam through which due entries are grown. |
| `Config` | Configuration struct mapping to `.tendril/schedules.yaml`. |
| `Entry` | One scheduled entry specifying a cron and a target Sequence or Sprout. |
| `Start` | Primes first fire times and launches the ticker goroutine loop. |
| `Schedule` | A parsed 5-field cron expression. |
| `Parse` | Parses a cron string into a `Schedule`. |
| `(*Schedule).Next` | Computes the first valid fire time strictly after a given time. |
| `LoadConfig` | Parses scheduler settings from YAML, fast-failing on bad cron expressions. |
| `OverlapSkip` / `OverlapQueue` | String constants for overlap policies. |

Package-level sentinel errors: none exported.

## Dependencies

**Fan-out:** none (leaf). Stdlib + `gopkg.in/yaml.v3` only; no other internal packages.

**Fan-in:**

- **`cmd/stem`** — the daemon entrypoint loads the config and wires the `Firer` implementation into `New`, and calls `Start`. This dependency inversion keeps the scheduler a leaf.

## Limitations

- **Cron dialect limits**: Only standard 5-field specs are supported; alphabetic names like `mon-fri` or `jan` are not supported.
- **No missed-run persistence**: It computes future fire times from startup; if the daemon is down, missed runs are not caught up on restart.
- **Single-process only**: The in-flight latching (`inFlight` / `pending`) is in-memory only; it does not coordinate across multiple daemon instances.
- **Config reload behavior**: There is no hot-reload; the daemon must be restarted to pick up changes to the config file.
- **Timezone handling**: Evaluates cron expressions in the local wall-clock location matching the reference time passed to `Next`.

## Design & rationale

The scheduler separates cron timing logic from execution by injecting a `Firer`. This dependency inversion keeps the component a leaf that never imports core or conductor logic, making it highly testable in isolation. 

The unattended-growth model assumes runs may overlap or fail. Overlap policies (`skip` or `queue`) are protected by an in-memory latch that correctly treats a full retry sequence as a single in-flight run. Instead of complex backoffs, the robustness choices include a simple fixed 5-second pause to recover from transient failures without over-complicating the ticker loop.

To prevent busy-polling, the loop ticks every 30 seconds. A fire only happens strictly after a minute boundary, ensuring runs execute within their target minute without duplicate firings. The parser in `cron.go` handles invalid calendar dates (like February 30) by safely disabling entries that can never fire, avoiding infinite scanning overhead.
