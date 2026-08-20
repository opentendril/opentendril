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
7. Roots issues a minimal authenticated provider interaction for the selected provider/model. An authentication rejection (HTTP 401/403/407) ends the run as `provider-auth-rejected` with no `sprout-emerged` event and no Terrarium.
8. Provider resolution and start occurs **after** workspace isolation is established, and only after that auth probe succeeds.
9. Teardown removes the shadow worktree and restores the host stash. 
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

- Managed and path checkouts persist but use distinct execution lifecycles.
- Ephemeral checkout mode uses a fresh directory.
- Remote-cloned substrate paths are Tendril-resolved working copies. They are not the Botanist's active local checkout.

## Provider Selection

Conductor `RunSprout` resolves the Terrarium provider in this order:
1. Explicit provider from the resolved Substrate configuration, when present.
2. Otherwise explicit `TENDRIL_TERRARIUM_PROVIDER`.
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
- This results in a Terrarium execution commit on a run-specific branch (`sprout/task-<stepID>`).
- Managed remote publication pushes to that run-specific branch. The source/default branch remains the starting source and is not advanced automatically.
- For local managed execution, committed Fruit remains on the run-specific local branch and is reviewable there.
- `SproutRunReport` carries `FruitBranch` and `FruitCommit` when a managed run creates committed reviewable Fruit, retaining this identity even if remote publication fails.
- Failure isolation is run-scoped: teardown or publication failure for one managed run must not delete or mutate another run's worktree, branch, or Fruit.
- `DisableMergeBack` may leave the commit isolated.
- Local merge-back may internally merge the Terrarium commit into the source branch.
- Internal merge-back is not Botanist acceptance. Final review and acceptance remains human-controlled.

## Chronicler

Adaptation/history-driven genome behavior is separate from Epigenetic Chronicler behavior. 
For reviewable successful Fruit with a diff, the Chronicler consumes the Sprout transcript, diff, and session logs and distills durable learnings.

## Managed Run Workspaces

- The managed checkout is a persistent Tendril-owned base repository / Git backing store, not the writable Terrarium execution workspace.
- Each writable managed Sprout run receives its own linked Git worktree keyed by step/run identity.
- The run workspace lives under the Tendril-owned `~/.tendril/run-workspaces/` (not `/tmp`) and uses `sprout/task-<stepID>`.
- The run workspace starts from an explicitly resolved commit.
- Sprout mutation, measurement, commit, generated execution inputs, and managed Fruit creation operate against the run workspace.
- The persistent managed checkout may still be fetched/refreshed/reset to its configured starting branch. That refresh does not reset an already-running Sprout's work.
- Shared managed-base Git metadata operations (materialization, start-commit resolution, run-worktree allocation) use a short per-managed-base lock.
- The lock is NOT held across Sprout execution, provider calls, Terrarium lifetime, or autonomous file mutation. Execution is not locked or serialized.
- Cleanup is run-specific: the run's linked worktree is removed at actual lifecycle ending. A detached return does not destroy a still-running workspace.
- A run branch with no work may be reclaimed by the owned-ref lifecycle; a branch carrying committed Fruit remains available for review.
- Delegated Git per-Pollen workspaces under `~/.tendril/workspaces/` are a separate mechanism from managed Sprout RunWorkspaces.

## Budgets and Detached Work

- The **growth budget** bounds how long the Stem waits.
- A **detached run** may continue on the work context.
- The **reap budget** is the longer backstop.
- **Post-mortem** has its own bounded context.
- A terminal outcome is emitted when work actually ends.
