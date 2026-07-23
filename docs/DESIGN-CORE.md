# Component: Core — the single transport-free capability service that owns capability registration and the delegation/grant authorization model.

## Purpose

`cmd/stem/internal/core` is the one hub every Stem command routes through. It defines the governed **capability set** once, owns the **delegation/grant** authorization model that decides whether an external Pollinator may run a delegated operation-class, and holds the **two-tier credential** security model (durable Pollinator credential → short-lived Stem-signed access token). It contains no transport: every method is expressible with plain domain types only — "invokable with zero HTTP, CLI or MCP types in scope" (`core.go`). The CLI, REST, and MCP surfaces are thin adapters that project this one service onto their transport; a boundary test (`boundary_test.go`) makes the transport-free property structural, not aspirational.

## Responsibilities

**Does:**

- Declare the canonical capability names — the single source of capability-name truth — and project each as one declarative `Capability` (name, description, JSON-schema input, `Invoke`) through the `Registry` (`registry.go`).
- Expose the `Core` interface and its `Service` implementation: session commands (the only business logic that lives inline) plus the genome/plasmid/mesh/sequence/sprout/stoma/seed/git operation-classes (`core.go`).
- Own the **dependency-injection seam**: for every execution-bearing family the Core defines an `*Operations` port (a struct of function fields) and a `WithX` setter; adapters and the conductor supply the implementations (`sprout.go`, `git.go`, `mesh.go`, `sequence.go`, `stoma.go`, `seed.go`, `plasmid.go`, `genome.go`).
- Define and evaluate the delegation grant model: `DelegationGrant`, `DelegationRequest`, `DelegationDecision`, and `DelegationAuthorizer.Authorize` (`delegation.go`); load grants from the Stem's own control-plane file (`delegationconfig.go`); carry the authorized Pollen through the request context and only the context (`delegationcontext.go`).
- Classify which capabilities are delegated operation-classes that must pass the grant gate before running for a Pollinator (`DelegatedCapabilityNames`, `IsDelegatedCapability` in `registry.go`).
- Own the credential security model: mint/verify short-lived access tokens with the Stem's Ed25519 key (`accesstoken.go`) and issue/resolve/revoke digest-stored Pollinator credentials (`pollinatorcredentials.go`).
- Read pure filesystem state directly (genome view, plasmid list, session reads) without touching an execution port.

**Does not:**

- Translate any transport. It imports no `net/http`, no MCP, no CLI, and no execution internal (orchestrator, terrarium, gateway, mesh, historydb) — enforced by `boundary_test.go`.
- Execute delegated work itself. Terrariums, git operations, mesh graft, and sequence runs happen behind the injected ports; a nil port makes its capability report "not wired" rather than act (e.g. `SproutRun` in `sprout.go`).
- Authenticate callers or gate invocations. Surfaces authenticate, derive the Pollen, call `DelegationAuthorizer.Authorize`, and only then invoke — the Core never reads the Pollen (`delegationcontext.go`).
- Discover grants inside a cloned Substrate, or accept grant/policy/Pollen material from caller-supplied input — the no-self-escalation invariant.
- Persist grants, credentials, or the authorizer's decision surface as mutable runtime state: grants and credentials are file-backed and reloaded, and the authorizer is immutable once constructed.

## Public interface

| Symbol | Role |
| --- | --- |
| `Core` | The capability-service interface every adapter routes through; every method carries plain domain types only. |
| `Service` | The `Core` implementation, holding the session manager and the eight injected `*Operations` ports. |
| `NewService` / `WithGenome` / `WithPlasmid` / `WithMesh` / `WithSequence` / `WithSprout` / `WithStoma` / `WithSeed` / `WithGit` | Construct a `Service` and inject each family's execution port (chainable). |
| `Capability` | One declarative command: `Name`, `Description`, `InputSchema` (plain-map JSON-Schema), and a transport-free `Invoke`. |
| `Capabilities()` / `Invoke(name, input)` | The live registry (one entry per canonical name) and the uniform name-dispatch path the MCP and CLI adapters use. |
| `CapabilityNames()` | The sorted canonical governed name set — the single source of truth the parity tests compare every surface against. |
| `DelegatedCapabilityNames()` / `IsDelegatedCapability(name)` | The subset of operation-classes that must pass the delegation gate before running for a Pollinator. |
| `Cap…` constants (`CapSproutGrow`, `CapStomaPass`, `CapSeedGrow`, `CapGitCommit`, …) | The canonical capability-name identifiers shared verbatim across all three surfaces. |
| `SproutOperations` / `StomaOperations` / `SeedOperations` / `GitOperations` / `SequenceOperations` / `MeshOperations` / `PlasmidOperations` / `GenomeOperations` | The injection ports: structs of function fields the Core defines and adapters/conductor implement. A nil field yields a "not wired" error. |
| `DelegationGrant` | One durable, revocable grant: a Pollen authorized for a bounded set of operation-classes on a bounded set of Substrates, with optional egress allow-list, expiry, and confirm-above-impact bound. |
| `DelegationRequest` / `DelegationDecision` | One invocation to authorize (carries no grant material) and the authorizer's verdict (authorized + a copy of the matching grant, or a transport-neutral denial reason). |
| `DelegationAuthorizer` / `NewDelegationAuthorizer` | Evaluates a request against deep-copied grants; holds no mutable policy surface. |
| `LoadDelegationGrants(tendrilDir)` | Reads `grants.yaml` from the Stem's own control-plane directory; a missing file is the secure default (zero grants). |
| `WithPollen(ctx, pollen)` / `PollenFromContext(ctx)` | Bind the authorized Pollen onto the request context and read it in the port; `""` means "not delegated". |
| `StemSigner` / `LoadOrCreateStemSigner` | Mints and verifies access tokens with the Stem's own Ed25519 key; `Public()` lets a remote verify with no shared state. |
| `MintAccessToken` / `MintFromCredential` / `VerifyAccessToken` / `AccessTokenClaims` / `AccessTokenScope` | The short-lived (≤15 min, hard-capped) signature-verified bearer half of the credential model. |
| `PollinatorCredential` / `IssuePollinatorCredential` / `ResolvePollenFromCredential` / `RevokePollinatorCredentials` / `LoadPollinatorCredentials` | The durable refresh-root half: the credential *is* the Pollen; only a SHA-256 digest is stored; lookup is constant-time. |
| `LooksLikeAccessToken` / `LooksLikePollinatorCredential` | Prefix routing (`tendrilat_` vs `tendril_`) so a surface sends a bearer to the right resolver; the two are mutually exclusive. |

Sentinel errors and secure-default returns: `ErrNotFound` (transport-neutral "session not found", mapped by `mapManagerErr`); an unresolvable credential or unverifiable token resolves to the empty Pollen (deny-closed); an empty or absent grant set denies every delegated invocation.

## Dependencies

**Fan-out:** `internal/session` only. The `Service` embeds a `*session.Manager` and returns `session.Phytomer` / `session.Message` / `session.Preferences` from the session-command methods; execution families that bind a run to a session (e.g. `SproutRun`) read the session's preferences to shape the spec, degrading to a sessionless run rather than refusing when resolution fails. Everything else the Core needs — terrariums, git, mesh, orchestration — arrives through the injected `*Operations` ports, never as an import.

**Fan-in:** `cmd/stem` and `internal/receptors` are the transport adapters that project the Core. `cmd/stem` builds the `Service`, wires each `WithX` port to a conductor-backed implementation, and constructs the `DelegationAuthorizer` from `LoadDelegationGrants`. `internal/receptors` holds the CLI/REST/MCP surface handlers: each delegated route derives the Pollen, calls `DelegationGate.Authorize(core.DelegationRequest{…})`, refuses on denial, then stamps the authorized Pollen onto the context with `core.WithPollen` before invoking (`receptors/git.go`, `receptors/config.go`). This is dependency inversion: the Core declares the `Core` interface and the `*Operations` ports; the outer adapters depend inward on those abstractions, and the Core depends on none of them.

## Limitations

- **Parity enforces the name set, not semantics.** `CapabilityNames()` is what the parity test (`cmd/stem/parity_test.go`) diffs each surface against; identical input schemas or identical behavior across surfaces are not what the test asserts. `plasmid.sign` and the mesh key-management commands are deliberately outside the governed set (ungoverned, CLI-local).
- **REST does not use the uniform `Invoke` path.** MCP and CLI dispatch by name through `Invoke`; the REST receptors call the typed `Service` methods directly with per-route handlers, so the "one projection path" is uniform for two of the three surfaces.
- **The confirm-above-impact bound denies rather than prompts.** `DelegationGrant.ConfirmAboveImpact` is honored, but no confirmation surface exists yet, so an invocation crossing the bound is refused with a confirmation-required reason (`delegation.go`). Compounding this, no current call site populates `DelegationRequest.Impact`; an undeclared (empty) impact ranks *above* every threshold, so any grant carrying a confirm-above bound denies every invocation it would otherwise match.
- **Grants and the authorizer are effectively immutable at runtime.** `NewDelegationAuthorizer` deep-copies its grants and exposes no mutation; changing policy means reloading `grants.yaml` and constructing a new authorizer. A malformed grants or credentials file is a hard error at load — callers are expected to deny all delegation, never fail open.
- **Grant matching is exact, no wildcards.** A grant names precisely the operation-classes and Substrates it opens; a request naming no Substrate, no operation-class, or no Pollen never matches.
- **Credentials and signing key are single-directory, file-backed.** Both live 0600 in the Stem's own `.tendril` control-plane directory. Access tokens are stateless-verified with no per-token denylist: revocation is at the root (revoke the credential, minting stops, outstanding tokens age out within the ≤15-minute cap). Deleting the signing key rotates it and invalidates every outstanding token at once.
- **Egress allow-list is carried, not enforced here.** A grant's `Egress` and the `StomaPassInput.Egress` field (JSON-invisible, `json:"-"`) transport the allow-list to the execution seam; the actual mediation lives in the conductor/terrarium, outside the Core.
- **In-memory session state via the manager.** Session reads and history come through `session.Manager`; the Core adds no persistence of its own.

## Design & rationale

The Core exists so that a **capability is defined once and projected onto every surface**. The litmus test in `core.go` — a capability belongs here only if it is "invokable with zero HTTP, CLI or MCP types in scope" — is what lets the CLI, REST, and MCP surfaces be thin adapters with no business logic, and it is enforced structurally: `boundary_test.go` fails the build if any Core file imports a transport (`net/http`) or an execution internal (orchestrator, terrarium, gateway, mesh, historydb). The `Registry` turns that single definition into three surfaces mechanically: MCP reads a `Capability`'s name/description/schema to build a tool, the CLI builds a subcommand, and both dispatch through `Invoke`, while `CapabilityNames()` is the one list the parity test measures every surface against. Adding a name there without wiring every surface — or wiring a surface without a name — fails CI, so drift is impossible rather than merely discouraged.

The **Operations injection** pattern resolves the tension between "the Core must own the capability" and "the Core must not link execution." For each execution-bearing family the Core defines a port — a struct of function fields (`SproutOperations.Run`, `GitOperations.Commit`, …) — plus a `WithX` setter; the conductor and adapters supply the implementations at construction. Dependency points inward: the Core declares the abstraction, the outer layers implement it. A nil port degrades to an honest "not wired" error rather than a panic, so a partially-assembled Stem is diagnosable.

The **delegation/grant** model replaces per-command host permission prompts with one scoped, revocable grant covering `{Pollen, operation-class, Substrate}`. Its defining property is **no self-escalation**: grants enter the `DelegationAuthorizer` only at construction, from the Stem's own control plane (`grants.yaml`, loaded from the Stem's own `.tendril` directory and never discovered inside a cloned Substrate); a `DelegationRequest` structurally carries no grant material; and the authorized Pollen travels only through the request context, never a capability-input field a caller could name. Zero grants is the secure default — every delegated invocation denies, and non-delegated invocations never consult the authorizer at all, so a plain human at a terminal is unaffected. The two-tier credential model applies the same deny-closed discipline to authentication: a durable, digest-stored, revocable Pollinator credential (`tendril_…`) is presented only to mint a short-lived, Stem-signed access token (`tendrilat_…`, ≤15 min) whose verification is a stateless signature check — a remote verifier needs only the Stem's public key, and revocation is at the root because a leaked token expires before a denylist would matter. Delegated operation-classes are ordered as a ladder (git status/branch reads below commit/push/PR below the destructive prune), and `DelegatedCapabilityNames()` is the single list the surfaces consult to decide which invocations must clear the grant gate before they run.
