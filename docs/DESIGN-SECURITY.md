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
- Maintain state or history of executed triggers.
- Run scripts that are not marked as executable, or scripts named `README.md`.
- Capture script stdout (it is explicitly discarded).
- Provide a Sprout or Terrarium for execution; scripts run in the host environment.

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

- **Trust model of trigger scripts:** Scripts are executed directly on the host using `os/exec` without a Sprout or Terrarium. They have the same permissions as the calling `stem` process, which poses egress and injection risks if the triggers directory contains untrusted code.
- **Execution environment:** The scripts are executed natively on the host, not inside an isolated environment. The interpreter is determined by the host OS (e.g., via shebangs).
- **Failure-mode:** The gate fails closed (blocks) if a script returns a non-zero exit code. However, if the triggers directory does not exist, the evaluation returns `nil` and allows execution (fail open on missing directory).
- **Ordering:** Scripts are evaluated in the order returned by `os.ReadDir` (lexicographical order), meaning naming conventions dictate the execution sequence.
- **Missing tests:** There are no tests included for the component (e.g., missing `_test.go`), limiting verification.

## Design & rationale

The component strictly implements the Hormonal-Trigger "a blocked run never grows" model. By intercepting execution at `cmd/stem`, it ensures that policy enforcement is an insurmountable gate. Triggers are implemented as standalone executable scripts rather than in-memory functions, allowing operators to write policies in any language by placing an executable file in the configured directory.

(Note: a separate `DESIGN-SECURITY-POSTURE.md` already exists as an architecture doc — this new doc is the component doc for the package; keep them distinct.)
