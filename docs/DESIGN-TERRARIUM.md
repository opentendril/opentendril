# Component: Terrarium — isolated Sprout execution and network sealing

## Purpose

`cmd/stem/internal/terrarium` abstracts isolated execution environments (Docker, Firecracker, gVisor, host) to safely run untrusted Sprout code and restrict network egress.

## Responsibilities

**Does:**

- Provide a unified `TerrariumProvider` factory (`factory.go`) to select the appropriate isolation backend based on declared capabilities and host readiness.
- Enforce capability checks (`validate.go`) so that unsupported spec requests (e.g., mounts in host provider, images in firecracker) are rejected rather than silently ignored.
- Execute commands in a sealed environment (`docker.go`, `firecracker.go`) with default-deny network posture and configurable constraints (CPU, memory, PIDs limit).
- Stage dependencies and retrieve results by copying files into and out of the isolated filesystem (`FilePayload`, `Artifact`).
- Support interactive I/O for payloads by piping stdin/stdout streams (`docker.go`, `host.go`).

**Does not:**

- Own orchestration or lifecycle of a Sprout execution sequence (that lives in `internal/conductor`).
- Manage LLM API keys or provider connections; Sprout isolation means the Stem mediates these external calls.
- Persist execution history or results beyond the ephemeral container lifetime.
- Autonomously fetch remote egress dependencies (Stem resolves fetches first and stages them via `CopyIn`).

## Public interface

| Symbol | Role |
| --- | --- |
| `TerrariumProvider` | Interface to create backend-specific Terrarium instances (`Name`, `Capabilities`, `Create`). |
| `Terrarium` | A live execution environment with methods to `Run`, `CopyIn`, `CopyOut`, `SnapshotLogs`, and `Stop`. |
| `TerrariumSpec` | Configuration for initialization (Image, Command, WorkingDir, Environment, Mounts, Files, CPU/Memory/PIDs limits). |
| `CommandSpec` / `CommandResult` | Describes a command (or interactive payload via `Stdin`) and captures execution output, timing, and exit codes. |
| `NetworkMode` | Network posture configuration (`NetworkModeNone`, `NetworkModeBridge`, `NetworkModeHost`). |
| `MountSpec` | Host-to-guest filesystem mapping with `ReadOnly` enforcement. |
| `Artifact` | Represents a file copied out of the terrarium filesystem after execution. |
| `TerrariumLogs` | Captures `Stdout` and `Stderr` snapshots from the isolated run. |
| `NewProvider` | Factory resolving `docker`, `gvisor`, `firecracker`, or `host` (wrapped in `validatingProvider`). |
| Providers | `NewDockerProvider`, `NewFirecrackerProvider`, `NewGVisorProvider`, `NewHostProvider`. |

Sentinel errors: None explicitly exported; returns wrapped `fmt.Errorf`s.

## Dependencies

**Fan-out:** none (leaf).

**Fan-in:**

- `internal/conductor` — `verifier.go`, `sprout.go`, and `stoma.go` instantiate a terrarium provider to run sealed builds, task iterations, and bounded commands. Conductor pushes files into `/tmp/egress` and evaluates the `CommandResult`.
- `internal/core` — Defines governed capabilities for bounded command passes (`stoma.go`) and Sprout runs (`sprout.go`, `seed.go`) that execute in a "network-sealed terrarium".

## Limitations

- **Provider Parity Gaps**: The four providers do not enforce identical isolation.
  - `DockerProvider` is fully featured (`SupportsMounts`, `SupportsImages`, `NetworkModeBridge` | `NetworkModeNone`).
  - `GVisorProvider` is just `DockerProvider` substituting `--runtime=runsc`.
  - `FirecrackerProvider` does not support interactive I/O via stdin (requires an explicit command array) and only supports `NetworkModeNone`. It relies on an external microVM stoma agent over vsock and ignores the image requested in `TerrariumSpec`, booting a pre-configured rootfs instead.
  - `HostProvider` is dangerously permissive. It bypasses all isolation, ignores mounts and network sealing, and runs with full host-user privileges. It is disabled by default and gated by `TENDRIL_ALLOW_HOST_EXECUTION=true` (`factory.go`).
- **Network Default Posture**: `DockerProvider` enforces `--network none` unless explicitly overridden. `FirecrackerProvider` strictly only supports `none`. The isolation design aims for a sealed box, but `HostProvider` silently permits host network usage.
- **Validation**: Capability enforcement in `validate.go` prevents silent downgrades (e.g., asking for an image in Firecracker, which boots a pre-baked kernel/rootfs), but it relies on provider capability declarations rather than deep structural checks.

## Design & rationale

The `TerrariumProvider` abstraction shields the higher-level Sprout and Stoma mechanisms from the nuances of isolation primitives. The core design is the **sealed-Sprout model**: ephemeral workers cannot reach out on their own. External calls must be Stem-mediated, either by fetching payloads beforehand (`/tmp/egress`) or proxying requests out.

The default-deny posture is mechanically enforced in `docker.go` (`--cap-drop=ALL`, `--security-opt=no-new-privileges:true`, `--network none`). The `host` provider is an intentional escape hatch for local testing, protected by a strict opt-in environment gate (`TENDRIL_ALLOW_HOST_EXECUTION`). `firecracker` provides strong hardware-assisted isolation for multi-tenant setups, but its parity gaps (no interactive I/O, no mounts) restrict which workflows can use it.
