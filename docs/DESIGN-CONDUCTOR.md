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

### Task-context preparation

After workspace resolution and the current Rhizome refresh, and before cognition, the Conductor assembles a bounded task-specific Plasmid against the execution workspace. Selection is deterministic and bounded across explicit anchors, refreshed Rhizome structural evidence, current Git-state paths, associated tests and documentation, and source-local project memory. Managed RunWorkspaces remain distinct from their backing source repositories: source identity is used for source-local memory, while each growth reads the workspace and revision it will execute.

Each growth refreshes task context against its own workspace and starting revision. The generated context is runtime preparation state, not Sprout-attributed Fruit. The Conductor publishes safe bounded provenance through `task-context-assembled`, never raw evidence. Preparation does not change the Terrarium, provider, tool, credential, network/egress, Git publication, commit, or default/protected-branch authority.

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

## Failure provenance

When a Sprout withers at a deterministic execution boundary, `SproutRunReport`
and its terminal event carry the Core-owned `FailureStage` and optional
`DiagnosticCode`. The Conductor sets the primary typed provenance once, so a
later teardown failure does not replace the causal stage. Substrate access
codes come from typed filesystem error identity; stage and code are never
derived from error text. Core applies the closed-vocabulary validation and
uses `unknown` only when a newly completed withered run has no valid specific
stage.

Regarding generic `RunSprout` compatibility: only Docker and gVisor satisfy its complete `TerrariumSpec` and interactive tool session today.

## Secrets and Credentials

Git operations are normally Stem-side. Sealed Sprouts do not receive the Substrate Git token by default. 
- `exposeToken: true` explicitly exposes the resolved Substrate token.
- Normal Terrarium environments are explicit-only. Provider/model inference credentials remain Stem-side and are not implicitly injected into a normal Terrarium.
- Repository or working-directory `.env` files and `TENDRIL_ENV_FILE` are not implicitly inherited by a normal Terrarium.
- `TENDRIL_READONLY=true` may be explicitly supplied for requested read-only execution.
- A Substrate's resolved Git credential is supplied only when that Substrate explicitly configures `exposeToken: true`; `exposeToken: false` remains credential-free.

## Fruit and State Externalization

Reviewable Fruit requires completed execution with measured modifications, no run error, not readonly, not investigation-only, and actual changes.
- Reviewable Fruit results in a Terrarium execution commit.
- For writable managed runs, that commit is created on the run-specific `sprout/task-<stepID>` branch.
- Managed remote publication targets that run-specific branch.
- Managed local Fruit remains on that run-specific local branch.
- The configured source/default branch is the managed run's starting source and is not advanced by managed Fruit publication.
- `SproutRunReport.FruitBranch` and `FruitCommit` describe every actual reviewable Sprout commit, retaining this exact identity even if publication fails.
- Non-managed local Fruit records the review/source branch; non-managed remote Fruit records the exact branch passed to publication.
- `FruitPublicationState` is closed to `local-only`, `published`, and `publication-failed`.
- `FruitRepository` is captured from the execution repository's Git evidence, using canonical GitHub identity, normalized other-remote identity, or the canonical repository-root path for local-only repositories. `FruitWorkspace` is private local verification evidence and is not exposed to Pollinators.
- Ordinary non-managed local merge-back and non-managed remote publication retain their existing execution semantics while now carrying this provenance.
- Failure isolation is run-scoped: teardown or publication failure for one managed run must not delete or mutate another run's worktree, branch, or Fruit.
- `DisableMergeBack` may leave the commit isolated.
- Internal merge-back is not Botanist acceptance. Final review and acceptance remains human-controlled.

### Fruit review evidence

The Conductor supplies read-only factual evidence for the Stem's Fruit
inventory. GitHub review evidence combines:

- the exact commit-to-pull-request lookup, which remains valid for squash-merge
  workflows;
- the historical branch-head lookup, retaining each pull request's head SHA so
  a reused branch name cannot establish a relationship for another Fruit
  commit; and
- the exact remote branch ref lookup, which is positive only when the ref's
  object SHA equals the persisted Fruit commit.

Local-only and publication-failed Fruit are checked only in their persisted
private workspace, against the exact persisted commit. The inventory starts
from `HistoryDB.ListFruitClaims`; it does not enumerate branches or create a
second Fruit registry. Repository identity is matched before current
Substrate credentials are used, and a repointed Substrate is not queried for
historical Fruit. These reads do not mutate Git, remove refs, or change any
execution or publication authority. Existing `git.branch.list` and
`git.prune` retain their conservative lifecycle rules.

The resulting Botanist observation is surfaced by the Stem's local
`tendril fruit list` and `tendril fruit list --json` commands and by private
`GET /v1/fruit`. Both adapters call Core's `ObserveFruitInventory` and preserve
its item order, review states, unknown reasons, pull-request metadata, and
review-pressure counts. The route is Botanist-only; the public Pollinator REST
and MCP projections do not expose this inventory. Observation reports evidence
unavailability and never accepts, merges, deletes, blocks, or mutates Fruit.

## Chronicler

The bounded Mycorrhizal Chronicler consumes the transcript, measured diff, and session logs from a reviewable successful Fruit. It retains concise repository-specific learnings as individual source-local Rhizome proposals with `origin=mycorrhizal`, `authority=none`, and `status=proposed`. It does not write the legacy Epigenetic genome, grant authority, or make a proposal eligible for Sprout context.

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
