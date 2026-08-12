# Terrarium Isolation Architecture

This document describes the current implemented state of the Terrarium boundary in OpenTendril.

## Core Taxonomy

- **Terrarium**: An isolated execution boundary.
- **Sprout**: An ephemeral execution body that may execute inside a Terrarium.

A Terrarium provides the isolated environment, but it is not itself a Sprout. The Stem orchestrates the Sprout, which executes within the Terrarium. Do not conflate the Stem, the Sprout, and the Terrarium.

## Provider Registry

OpenTendril implements four Terrarium providers: `docker`, `gvisor`, `firecracker`, and `host`. Note that a provider's implementation of the Terrarium interface is distinct from its compatibility with specific Conductor execution paths.

### Capability Validation

The `NewProvider` factory wraps all providers with capability validation. A requested mount, network mode, or selectable image that a provider does not declare is rejected rather than silently ignored. This is a strict fail-closed contract to ensure requested isolation constraints are actually enforced.

### 1. Docker

- **Provider Name**: `docker`
- **Implementation**: Docker-based.
- **Capabilities**: Supports interactive I/O, image selection, and bind-mount support.
- **Security Posture**: 
  - Conductor's `RunSprout` path requests `NetworkModeNone`.
  - Enforces `CAP_DROP ALL` and `no-new-privileges`.
  - CPU, memory, and PID limits are applied when requested; the current `RunSprout` supplies 1 CPU, 2048 MB, and a PID limit of 512.
- **Workspace**: The workspace mount may be read-only for investigation or read-only execution.

### 2. gVisor

- **Provider Name**: `gvisor`
- **Implementation**: Docker-based provider using `--runtime=runsc`.
- **Capabilities**: Declares the exact same Terrarium capability shape as Docker.
- **Requirements**: Readiness requires `runsc` to be registered in the local Docker daemon.

### 3. Firecracker

- **Provider Name**: `firecracker`
- **Implementation**: Firecracker microVM provider.
- **Capabilities**: Supports `NetworkModeNone` and `CopyIn`/`CopyOut`. It does not support host mounts or interactive `stdin` execution. It uses a fixed configured rootfs rather than `TerrariumSpec.Image`.
- **Communication**: Stoma communication occurs over a Firecracker vsock on port 5000. Requests and responses use newline-delimited JSON over that channel.
- **Requirements**: Requires the `firecracker` binary in `PATH`, `/dev/kvm`, `TENDRIL_FC_KERNEL_PATH`, and `TENDRIL_FC_ROOTFS_PATH`.

### 4. Host

- **Provider Name**: `host`
- **Implementation**: Executes with `os/exec` as the Stem's host user.
- **Capabilities**: Supports host networking and interactive I/O.
- **Security Posture**: Bypasses Terrarium isolation completely. There is no mount sealing or network sealing; the execution runs with full host-user permissions.
- **Requirements**: Disabled by default. Requires `TENDRIL_ALLOW_HOST_EXECUTION=true`. Emits a conspicuous warning and can emit activation telemetry.

**Note**: `TENDRIL_ALLOW_HOST_EXECUTION` (which bypasses the Terrarium execution boundary) is distinct from `TENDRIL_ALLOW_HOST_WORKSPACE` (which bypasses Git shadow-worktree isolation).

## Provider Selection

The low-level factory default for the Terrarium package is Docker when the provider name is empty.

However, the Conductor `RunSprout` execution path resolves the provider using this precedence:
1. An explicit `TENDRIL_TERRARIUM_PROVIDER` environment variable.
2. An explicit provider from the resolved Substrate configuration.
3. Otherwise, `gvisor`, if `runsc` readiness succeeds.
4. Otherwise, `docker`.

An explicit provider selection is strictly honored and never automatically upgraded.

## Generic RunSprout Compatibility

The generic Conductor `RunSprout` path currently constructs a `TerrariumSpec` that requests:
- A selected Sprout image.
- A host workspace mount at `/app`.
- `NetworkModeNone`.
- Interactive tool calls through `Terrarium.Run` with `stdin` payloads.

Current provider compatibility with this specific execution path:
- **Docker**: Compatible.
- **gVisor**: Compatible.
- **Firecracker**: Incompatible. While the provider is implemented, it is rejected by capability validation because it lacks selectable images/mounts and requires an explicit command rather than interactive `stdin`.
- **Host**: Incompatible. The generic `RunSprout` spec is rejected by capability validation because the Host provider does not support the requested mount, selected image, or `NetworkModeNone`.

## Firecracker Bootstrap

To bootstrap the Firecracker environment, use the CLI:

```bash
tendril terrarium init-firecracker
```

This command performs the following steps:
- Creates `.tendril/terrarium` under the current working directory.
- Obtains the pinned kernel artifact and the pinned static BusyBox artifact.
- Verifies SHA-256 checksums for existing and downloaded artifacts.
- Builds `cmd/stoma` for `linux/amd64` with CGO disabled.
- Installs Stoma as `/init`.
- Stages BusyBox and applet symlinks.
- Creates a 100 MiB ext4 rootfs and populates it using `mkfs.ext4 -d`. (This does not require privileged Docker or rootfs loop mounting).

**Prerequisites for bootstrap:**
- `mkfs.ext4` / `e2fsprogs`

**Prerequisites for runtime execution:**
- The `firecracker` binary in `PATH`
- `/dev/kvm` access

Once bootstrapped, configure the environment variables:
```bash
export TENDRIL_TERRARIUM_PROVIDER=firecracker
export TENDRIL_FC_KERNEL_PATH=~/.tendril/terrarium/vmlinux.bin
export TENDRIL_FC_ROOTFS_PATH=~/.tendril/terrarium/rootfs.ext4
```
