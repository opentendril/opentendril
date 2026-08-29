# OpenTendril Capability & Authorization Model (CAPABILITIES)

This document describes the current implemented capability architecture: where
capability authority lives, how governed commands are projected across transport
surfaces, how delegated authorization works, and how capability authority relates
to execution isolation.

---

## 1. Core Registry Is the Governed Command Authority

Governed command capabilities are declared in the Stem Core registry
(`cmd/stem/internal/core/registry.go`). `core.CapabilityNames()` returns the
canonical set - the single source of truth for every governed command the system
offers.

A Core **Capability** is a transport-free declaration:

```go
type Capability struct {
    Name        string
    Description string
    InputSchema map[string]any                                      // JSON-Schema
    Invoke      func(ctx context.Context, input map[string]any) (any, error)
}
```

The `Invoke` signature carries zero transport types - no `net/http`, no MCP
message types, no CLI flag structs. That is the litmus test for the Core
boundary, enforced by `TestCoreHasNoTransportOrExecutionImports` in
`cmd/stem/internal/core/boundary_test.go`, which fails the build if Core imports
any transport (`net/http`, `receptors`) or execution (`conductor`, `terrarium`,
`gateway`, `mesh`, `historydb`) package.

### Architecture boundary

| Layer | Responsibility |
|---|---|
| **Core** (`cmd/stem/internal/core/`) | Governed capability declarations, delegation authorization, business logic. Transport-free. |
| **REST / MCP / CLI adapters** | Translate transport ↔ Core. Decode requests, encode responses, map errors. No business logic. |
| **Conductor / Terrarium** | Execution orchestration and isolation. The Core is structurally forbidden from importing them. |

The Stem is a deterministic routing/lifecycle kernel and governed capability
registry. LLM cognition belongs to the Mycorrhizal Network. The Stem does not
perform reasoning.

---

## 2. Governed Commands, Views, and Control-Plane Operations

Not every Stem operation is a governed command. The system distinguishes three
categories:

### Governed commands

Capabilities returned by `core.CapabilityNames()`. Every governed command:

- is declared once in the Core registry,
- is projected onto all three transport surfaces (REST, MCP, CLI),
- is subject to interface-parity enforcement (§3).

### Views

Real operation-classes that are deliberately outside the governed command
registry. A view does not map to a command every surface must project, but it is
nonetheless a recognized operation-class: a delegation grant naming it
authorizes observation without authorizing execution.

Current view:

- **`sprout.watch`** - authorizes watching a Phytomer's run records, persisted
  events, live stream, and the headless current-state watch at
  `GET /v1/phytomers/{phytomerId}/watch`. Defined as `CapSproutWatch` in the
  registry constants, with a doc comment stating it is deliberately absent from
  `CapabilityNames()` and the parity registry. Because it is not in
  `CapabilityNames()`, the parity test never evaluates it. Its own
  authorization semantics are exercised in `watch_test.go`, which verifies
  that a delegated caller must hold a `sprout.watch` grant for every Substrate
  targeted by a Phytomer's runs. A Seed-owned Phytomer is observable under this
  same rule even before the first Sprout exists. The watch view emits current
  safe state immediately as Server-Sent Events and follows that Phytomer until
  the associated Seed is terminal; it does not accept Fruit and does not grant
  `seed.grow`. There is no `seed.watch` operation-class.

### Control-plane operations

Operations deliberately excluded from the governed command registry because
exposing them on Pollinator-facing surfaces would create privilege escalation.
They are not Pollinator-facing governed command capabilities, are excluded from
`CapabilityNames()` and command parity, and their management surface varies by
operation: some are local CLI commands (e.g. `tendril setup`, `tendril serve`),
while others are exposed through dedicated Botanist-authenticated REST routes
(e.g. pending delegation confirmation at `GET /v1/delegation/pending`,
`POST /v1/delegation/pending/{id}/approve`,
`POST /v1/delegation/pending/{id}/deny`).

The parity test `TestControlPlaneCapabilitiesExcluded` asserts that no
capability name in `CapabilityNames()` carries a control-plane prefix. The
current deny-list prefixes are:

| Prefix | Examples |
|---|---|
| `setup.` | First-time Stem setup |
| `init.` | Workspace initialization |
| `serve.` | Starting the Stem server |
| `pollinator.` | Pollinator credential management (create, list, revoke) |
| `delegation.` | Grant management (grant, revoke, list) |
| `hardiness.` | Deployment posture reporting |
| `git.setup.` | Git credential configuration |
| `mcp.` | MCP tool management (install, list, remove) |

Additionally, CLI-local commands such as `mesh keygen`, `mesh issue-token`, and
`tendril sequence dynamic` remain outside the governed registry because they
mint cryptographic material, manage local mesh keys, or provide CLI sugar that
synthesizes a file and invokes a governed capability.

---

## 3. Interface Parity

`core.CapabilityNames()` is the canonical governed capability identity. Grant
operation-classes stay those dotted names. REST and CLI continue to use
canonical identity as applicable. The MCP receptor translates transport only:
it publishes one deterministic lower-camelCase primary identifier per Core
capability, and that identifier maps one-to-one back to the canonical name.

Examples:

| Canonical Core / grant operation-class | Primary MCP tool |
|---|---|
| `git.status` | `gitStatus` |
| `sprout.grow` | `sproutGrow` |
| `git.branch.list` | `gitBranchList` |

The governed command capability set is independently checked across four
architectural surfaces - Core, REST, MCP, and CLI - using five independently
derived observations (MCP is checked two ways):

1. **Core registry** - `core.CapabilityNames()`
2. **REST adapter** - capabilities from each receptor handler's `Capabilities()`
   method, reflecting what is actually mounted on the HTTP mux
3. **MCP adapter (declared)** - `mcp.CoreCapabilityNames()`, the canonical set
   the adapter projects
4. **MCP adapter (live)** - primary `tools/list` identifiers resolved through
   the adapter projection table back to `CapabilityNames()`. Compatibility
   aliases are not counted as extra governed capabilities.
5. **CLI adapter** - subcommand names collected from each CLI registration
   function

`TestInterfaceParityCoverage` in `cmd/stem/parity_test.go` asserts REST and CLI
set equality against the canonical names, and MCP complete/unique projection
against that same set. It goes red the moment:

- a governed capability is added to one surface but not the others, or
- a Core capability exists that an adapter failed to project, or
- an adapter exposes a governed capability that Core does not declare, or
- a listed primary MCP identifier does not map back to exactly one canonical
  Core capability.

MCP parity is not literal string equality of tool name and Core name.
Authorization and grant lookup always receive the canonical operation-class
(`git.status`), never the transport identifier (`gitStatus`).

Behavioral parity is also tested: `TestBehavioralParity_*` tests assert that
equivalent REST, MCP, and CLI requests decode to identical typed inputs and call
the same Core methods exactly once, proving adapters carry zero independent
business logic. MCP behavioral arms invoke the primary identifier.

**What parity does not cover.** Parity applies only to governed commands from
`CapabilityNames()`. It does not apply to views (`sprout.watch`), control-plane
operations, or compatibility aliases that may exist on MCP outside the
governed set. Those aliases resolve to existing canonical capabilities and
carry no independent authority.

---

## 4. Delegation Model

A durable `DelegationGrant` authorizes bounded delegated work on behalf of an
external Pollinator, replacing per-command host permission prompts with a
declared boundary the Botanist controls.

### Grant dimensions

A `DelegationGrant` (`cmd/stem/internal/core/delegation.go`) carries:

| Field | Purpose |
|---|---|
| `Pollen` | The trust-root identity (Pollinator, Phytomer, or mesh peer) exercising the grant. |
| `OperationClasses` | Explicit allow-list of delegable operation-class names (e.g. `"sprout.grow"`). Exact match; no wildcards. |
| `Substrates` | Scopes the grant to named Substrates. Exact match; a request naming no Substrate never matches. |
| `Egress` | Hostname allow-list bounding network egress for delegated execution. Empty means deny-all. |
| `Expires` | Expiration timestamp. Zero means the grant does not expire; revocation is removing it from the control-plane config. |
| `ConfirmAboveImpact` | Optional escalation threshold (`"low"`, `"medium"`, `"high"`). Invocations at or above this level escalate to Botanist approval rather than executing immediately. |

### Security semantics

- **Zero grants = deny all.** With no grants configured, the authorizer denies
  every delegated invocation. Non-delegated invocations never consult it.
- **A request cannot carry or widen its own grant.** Grants enter the authorizer
  only at construction, from the Stem's own control plane.
  `DelegationRequest` structurally carries no grant material, so neither a
  caller nor a file inside a cloned Substrate can self-escalate.
- **Grant matching is exact and bounded.** Pollen, operation-class, and
  Substrate must each match a value in the grant. No prefix matching, no
  wildcards.
- **Empty egress means deny-all.** An empty `Egress` list means no network
  egress is permitted for delegated execution under that grant. The list is
  carried on the grant so an authorized decision is complete for downstream
  Terrarium enforcement.
- **Expiry is checked at authorization time.** A grant past its `Expires`
  timestamp is silently skipped.
- **Grants are deep-copied at construction.** Later mutation of the caller's
  slice cannot widen (or narrow) what the authorizer permits.

### Impact confirmation

When a grant's `ConfirmAboveImpact` threshold is set and an invocation's impact
meets or exceeds it, the authorizer escalates:

- If a `PendingConfirmationStore` is attached, the authorizer creates a pending
  confirmation record (with a TTL) and returns `PendingConfirmation: true` with
  a `ConfirmationID`. The Botanist can approve or deny it. On a subsequent
  invocation with the same (Pollen, operation-class, Substrate) tuple, if an
  approved record exists it is consumed atomically and the invocation proceeds
  against the live grant - not a stale snapshot.
- If no pending store is attached, the invocation is denied with a
  confirmation-required reason.
- An invocation whose impact is undeclared (empty) ranks above every configured
  threshold, ensuring undeclared impact never slips under a confirmation bound.

This is an escalation mechanism, not the normal execution model. The purpose
of delegation is that a Botanist can grant freedom *inside a declared boundary*
rather than approve every action individually.

---

## 5. Delegated Subset

`DelegatedCapabilityNames()` returns the subset of governed commands that
execute work on behalf of an external Pollinator and therefore must pass the
delegation control plane (a grant covering {Pollen, operation-class, Substrate})
before they run on a Pollinator-facing surface.

Not every governed capability is delegated. The current delegated set is:

| Family | Delegated capabilities |
|---|---|
| Genotype | `genotype.create` |
| Sprout | `sprout.grow` |
| Stoma | `stoma.pass` |
| Seed | `seed.grow` |
| Git | `git.commit`, `git.push`, `git.pr`, `git.branch`, `git.status`, `git.branch.list`, `git.prune` |

Capabilities outside this set (such as `phytomer.*`, `genome.*`, `plasmid.*`,
`mesh.*`, `sequence.*`) are governed commands subject to parity but are not
gated by the delegation authorizer on Pollinator-facing surfaces.

`IsDelegatedCapability(name)` reports whether a named capability is in the
delegated set. The surfaces that gate per-invocation consult it.

---

## 6. Current Governed Capability Families

The governed capabilities in `core.CapabilityNames()` are grouped by family.
The registry itself is authoritative when exact command names or schemas are
needed.

### Phytomer

Session lifecycle: create, list, get, update (preference overrides), delete
(prune), and history (recent unified chat log).

### Genome

View the workspace Genome, reduce it (distill), or evolve it.

### Genotype

Create a new Genotype (behavioral persona).

### Plasmid

List available Plasmids or inject one into context.

### Mesh

Substrate grafting across the Mycelial Mesh: `mesh.graft` delegates a Substrate
commit through a peer Stem, `mesh.promote` promotes a branch. The trait
sub-family (`mesh.trait.list`, `mesh.trait.accept`, `mesh.trait.reject`)
manages inbound trait proposals from mesh peers.

### Sequence

List defined Sequences or grow (execute) one.

### Sprout

Grow a Sprout - emerge an ephemeral worker to execute a Transcript.

### Stoma

`stoma.pass` - pass a command through the Terrarium's Stoma (the single
controlled aperture in the isolation wall).

### Seed

Grow a Seed - activate a product-level goal. Each Seed growth has one canonical
Phytomer execution context. The Seed handle and Phytomer ID are distinct:
dispatch and collection use the handle (`seed.grow`); observation of that
growth uses the Phytomer ID under `sprout.watch`. Observation does not execute
the Seed and does not accept Fruit. `seed.grow` and `sprout.watch` remain
separately grantable.

Under the managed GitHub App/API posture, Seed writing iterations are chained
through local `tendril/seed-*` checkpoints: each successful iteration advances
the checkpoint, and the next iteration starts from its tip. These checkpoints
are internal convergence state, not remotely published Fruit; they do not
receive GitHub credentials and use a Tendril-owned local Git identity. The
converged result is published exactly once through the Stem-held managed App/API
path. Only the GitHub-created review branch and returned GitHub commit OID are
reported as Fruit identity. Publication configuration, execution-plan
resolution, or final API publication failure preserves the local Seed state,
returns the error, and reports no Fruit branch or commit. The default branch is
unchanged by this Seed growth.

### Git

The delegated Git family. Each operation-class is separately grantable. Git
execution runs on the Stem (the sole secret-holding zone), never inside a
sealed Sprout - a delegated push is the Stem's mediated egress with the
Substrate's dedicated credential.

| Capability | Behavior |
|---|---|
| `git.commit` | Commit the workspace state. Two modes are determined by the Substrate's connection configuration (see below). Both modes use the same default-branch commit guard: commits to the repository's default branch are refused unless the Substrate explicitly sets `protectDefaultBranch: false`. |
| `git.push` | Push `HEAD` to a target branch on the remote (`HEAD:refs/heads/<branch>`). If no explicit branch is supplied, the workspace's current branch is used; if a branch is supplied, `HEAD` is pushed to that named remote branch. Uses the Substrate's credential. |
| `git.pr` | Open a pull request. The base branch is resolved from the repository (never assumed). An existing open PR for the same head is returned rather than duplicated. A head branch that is the default branch is refused. PR creation does not merge. |
| `git.branch` | Create or switch to a feature branch. An existing branch is switched to, never reset. A branch named as the repository's default branch is refused. |
| `git.status` | Read-only report of workspace state: current branch, resolved default branch, uncommitted changes, ahead/behind, and whether a commit would be allowed. |
| `git.branch.list` | Classify local branches against forge evidence (merged, open PR, closed-unmerged, unpushed, etc.). Read-only. |
| `git.prune` | Delete local branches whose PR merged. Reports what would be deleted unless `confirm` is true. Never deletes the current or default branch, a branch with an open or unmerged PR, one the remote has never seen, or one held by another subject's workspace. |

**`git.commit` modes.** The Substrate's connection configuration determines
which mode is used:

- **Local mode** (default) - commits using local Git under the Substrate's
  configured commit name and email. Deny-closed: execution is refused when no
  commit identity is configured, so a delegated commit is always attributable.
  A subsequent `git.push` is required to publish.
- **API mode** (`commit: api`) - requires a GitHub App connection. Creates the
  commit server-side through the GitHub API; GitHub supplies the identity and
  signature. Because the API commit advances the remote branch directly, it
  also publishes the change - a subsequent `git.push` is unnecessary.
  **Managed readiness requirement:** for `checkout.mode=managed`,
  `tendril git setup --verify` additionally confirms that the GitHub App
  installation holds repository **contents write** permission before the first
  Seed run. The check is read-only and creates no branch, commit, push, or pull
  request. A missing write permission fails verify with an actionable diagnosis.
  This additional readiness probe does not apply to path or ephemeral checkouts;
  their setup-verification contract remains credential-only.

`git.push` and `git.pr` are separate operation-classes by design: a Pollen
granted only `git.pr` must never be able to publish a branch as a side effect.
There is no governed `git.merge` capability - merging is a Botanist decision.

> **Implementation note on `git.push`:** `RunGitPush` in the conductor does
> not check whether the target branch is the repository's default branch.
> Default-branch protection is enforced by `git.branch` (which refuses to
> create/switch to the default branch), `git.commit` (which refuses to commit
> on the default branch unless opted out), and `git.pr` (which refuses a head
> branch that is the default branch), but `git.push` itself does not
> independently verify this. The project invariant is no direct default-branch
> push; this gap is reported as out-of-scope implementation drift.

---

## 7. Terrarium Is Execution Boundary, Not Capability Authority

Capability authorization and execution isolation are separate concerns:

- **Capability authorization** is decided by the Core registry and the
  delegation authorizer before execution begins.
- **Execution isolation** is provided by the Terrarium - the filesystem and
  network boundary around a Sprout.

A **Sprout** is an ephemeral, strictly isolated worker that grows a Transcript.
A **Terrarium** is the isolation boundary wrapping it. The Terrarium ensures a
sealed Sprout cannot reach out on its own - external calls are Stem-mediated.

The Stem remains outside the Sprout/Terrarium distinction. It mediates governed
operations, holds credentials, and performs Git and network operations that a
sealed Sprout cannot. The Terrarium's Stoma (the single controlled aperture in
the isolation wall) is where commands enter and results leave.

Authorization decides *whether* an operation proceeds. The Terrarium decides
*where* it executes and *what it can reach*. The grant's egress allow-list
bridges the two: it is an authorization decision (what hosts this delegation
may contact) that the Terrarium enforces (by configuring the Stoma's network
policy).

Terrarium provider specifics are documented in `docs/TERRARIUM.md`.

---

## Invariants

| Concept | Role |
|---|---|
| **Stem** | Deterministic routing/lifecycle kernel and governed Core capability authority. Not reasoning. |
| **Mycorrhizal Network** | The LLM - cognitive side, external to the plant. |
| **Pollinator** | External requester that reaches in and asks for governed work. |
| **Pollen** | The identity a Pollinator presents; the trust-root a grant names. |
| **Sprout** | Ephemeral execution body. |
| **Terrarium** | Isolation boundary around a Sprout. |
| **Substrate** | Target repository. |
| **Fruit** | Git-reviewable output. The Botanist decides acceptance. |
| **Botanist** | The human who decides acceptance. |
| **Core registry** | Authoritative for governed commands. |
| **CLI / MCP / REST** | Transport adapters. No business logic. |
| **Views** | Real operation-classes, distinct from command parity. |
| **Control-plane operations** | Distinct from Pollinator-facing governed commands. |
| **Hardiness** | Reports deployment posture. Not an acceptance decision. |
| **Authorization ≠ containment** | A grant decides whether; a Terrarium decides where. |
