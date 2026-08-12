# Component: Conductor

## Purpose

`cmd/stem/internal/conductor` is the execution and orchestration package used behind Core ports and adapters. It is responsible for:
- Executing Sequences.
- Growing Sprouts.
- Coordinating Terrarium sessions.
- Resolving Substrates and workspaces.
- Performing state-externalization Git machinery.
- Running selection and verification.
- Staging execution context.
- Recording outcomes and chronicling.

Conductor does **NOT**:
- Own CLI, REST, or MCP transport.
- Own capability governance or parity.
- Own provider/model HTTP implementation in Roots.
- Own Terrarium provider implementations.

Note: Conductor is not the Stem itself; it is an orchestration engine utilized by the Stem.

## Sprout and Terrarium Transport

The host-side Sprout ReAct loop interacts through the Terrarium abstraction. Transport is provider-specific:
- Docker and gVisor support the interactive `stdin`/`stdout` tool-session shape used by the generic `RunSprout` path.
- Firecracker uses Stoma over vsock JSON and does not support interactive `stdin`.
- Provider capability validation rejects requests a provider cannot honor.

## Local Git Workspace Lifecycle

For an ordinary local Git `RunSprout`, the exact ordering is:
1. Resolve Substrate/workspace.
2. If writable and merge-back enabled, resolve the current/default branch.
3. Create `sprout/task-<stepID>` only when the current branch is protected or the default branch. Otherwise, keep the existing non-protected branch.
4. If the active host worktree is dirty, stash tracked and untracked changes using `git stash save -u` with a run-specific name.
5. Attempt shadow worktree creation.
6. On success, execute against the shadow path.
7. Provider resolution and start occurs **after** workspace isolation is established.
8. Teardown removes the shadow worktree and restores the host stash. 
   - Note: Stash-pop recovery handles the specific untracked-file collision case but does not suppress genuine tracked merge conflicts.

### TENDRIL_ALLOW_HOST_WORKSPACE

This applies when a local Git shadow worktree cannot be established:
- **Unset/default**: Fail closed.
- **true**: Permits use of the active host workspace and emits a warning/telemetry.
  - It does **NOT** select the Host Terrarium provider.
  - It is separate from `TENDRIL_ALLOW_HOST_EXECUTION`.

## Non-Git Paths

Terrarium provider isolation and Git shadow-worktree isolation are separate mechanisms. 
- A non-git local path cannot receive shadow-worktree isolation.
- State externalization and change classification are unavailable.
- The current path uses the resolved source path as the Terrarium mount.

## Remote Substrates

- Managed/path checkouts may persist.
- Ephemeral checkout mode uses a fresh directory.
- Remote-cloned substrate paths are Tendril-resolved working copies. They are not the Botanist's active local checkout.

## Provider Selection

Conductor `RunSprout` resolves the Terrarium provider in this order:
1. Explicit `TENDRIL_TERRARIUM_PROVIDER`.
2. Explicit provider from resolved Substrate configuration.
3. Otherwise `gvisor` when `runsc` readiness succeeds.
4. Otherwise `docker`.

Shadow-worktree establishment occurs **before** provider resolution. 

Regarding generic `RunSprout` compatibility: only Docker and gVisor satisfy its complete `TerrariumSpec` and interactive tool session today.

## Secrets and Credentials

Git operations are normally Stem-side. Sealed Sprouts do not receive the Substrate Git token by default. 
- `exposeToken: true` explicitly exposes the resolved Substrate token.
- Provider/model inference credentials may be passed into Terrarium execution as required by the current LLM execution path.

## Fruit and State Externalization

Reviewable Fruit requires completed execution with measured modifications, no run error, not readonly, not investigation-only, and actual changes.
- This results in a Terrarium execution commit.
- `DisableMergeBack` may leave the commit isolated.
- A remote clone may publish its Terrarium commit.
- Local merge-back may internally merge the Terrarium commit into the source branch.
- Internal merge-back is not Botanist acceptance. Final review and acceptance remains human-controlled.

## Chronicler

Adaptation/history-driven genome behavior is separate from Epigenetic Chronicler behavior. 
For reviewable successful Fruit with a diff, the Chronicler consumes the Sprout transcript, diff, and session logs and distills durable learnings.

## Concurrency Constraints

- One managed checkout directory exists per named managed Substrate.
- Refresh/stash/worktree operations are not serialized per managed Substrate.
- Concurrent runs against that same managed checkout can interfere.
- Ephemeral checkout mode uses separate directories.

## Budgets and Detached Work

- The **growth budget** bounds how long the Stem waits.
- A **detached run** may continue on the work context.
- The **reap budget** is the longer backstop.
- **Post-mortem** has its own bounded context.
- A terminal outcome is emitted when work actually ends.
