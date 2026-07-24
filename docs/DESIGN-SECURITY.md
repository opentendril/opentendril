# Component: Security — the pre-run policy gate.

## Purpose

`cmd/stem/internal/security` is the self-contained leaf that implements the pre-run policy gate. It implements Hormonal Triggers — a blocked run never grows. The gate evaluates trigger scripts in a directory before a run proceeds.

## Responsibilities

**Does:**
- Read the specified triggers directory for executable scripts (`trigger.go`).
- Filter scripts by executable permissions (owner, group, or others).
- Serialize the `TriggerPayload` containing the genotype and transcript into a temporary JSON file.
- Execute each valid script, passing the JSON payload file path as an argument.
- Fail closed by blocking the run if any script returns a non-zero exit code, returning the script's stderr as the error message.

**Does not:**
- Run scripts that are not marked as executable, or scripts named `README.md`.
- Capture script stdout (it is explicitly discarded).
- Provide a persistent state or history of executed triggers.

## Public interface

| Symbol | Role |
| --- | --- |
| `TriggerPayload` | Data structure containing the `Genotype` and `Transcript` strings. |
| `EvaluateTriggers` | Executes all trigger scripts in a directory and returns an error on the first non-zero exit code. |

## Dependencies

**Fan-out:** none (leaf). Uses only standard library packages.

**Fan-in:**
- **`cmd/stem`** — uses the gate by calling `EvaluateTriggers` prior to execution, honoring the design that a blocked run never grows.

## Limitations

- **Execution environment:** Triggers run isolated inside an `alpine:3.20` Terrarium (NetworkMode: None, read-only mount, non-root, resource-capped, 30s timeout) and must be POSIX-sh compliant.
- **Gate mode and failure-mode:** Controlled via `TENDRIL_TRIGGERS_MODE`. Under `enforce` (the default), the gate fails closed (blocks) if a script returns a non-zero exit code, or if the triggers directory is missing or unreadable. `disabled` allows all sprouts. Any invalid value falls back to `enforce`.
- **Host execution override:** By setting `TENDRIL_ALLOW_HOST_EXECUTION=true`, operators can explicitly bypass isolation and execute triggers directly on the host.
- **Ordering:** Scripts are evaluated in the order returned by `os.ReadDir` (lexicographical order), meaning naming conventions dictate the execution sequence.

## Design & rationale

The component strictly implements the Hormonal-Trigger "a blocked run never grows" model. By intercepting execution at `cmd/stem`, it ensures that policy enforcement is an insurmountable gate. Triggers are implemented as standalone executable scripts rather than in-memory functions, allowing operators to write policies in any language by placing an executable file in the configured directory.

(Note: a separate `DESIGN-SECURITY-POSTURE.md` already exists as an architecture doc — this new doc is the component doc for the package; keep them distinct.)
