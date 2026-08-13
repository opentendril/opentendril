# Component: Mesh — signed Stem Grafting and trait-envelope primitives for joining a local Stem to a remote Stem.

## Purpose

`cmd/stem/internal/mesh` is the self-contained leaf that implements **Stem Grafting**: Ed25519 key material and short-lived JWT tokens, a WebSocket graft client/server that delegates a local commit patch to a central Stem for governance and push, and signed trait envelopes with an in-memory accept/reject inbox. It owns crypto and graft transport only — adapters and Core project its capabilities onto CLI/REST/MCP.

## Responsibilities

**Does:**

- Generate, persist, and load workspace Ed25519 keypairs under `.tendril/security/` (`auth.go`).
- Issue and verify short-lived EdDSA JWTs for mesh graft authorization (`IssueToken` / `VerifyToken` and workspace helpers).
- Act as graft **client**: when `TENDRIL_GRAFT_URL` and `TENDRIL_GRAFT_TOKEN` are set, collect the latest commit patch and stream a `graft-request` over WebSocket (`client.go`).
- Act as graft **server**: verify the bearer JWT, apply the patch in a detached git worktree terrarium, run `.tendril/mesh-governance.yaml` (or a relative sequence path), stream logs, and push the validated result (`server.go`).
- Define trait payload/envelope types, sign/verify envelope signatures, evaluate `acceptPolicy` (`deny` / `manual` / `allowlist:…`), and keep a process-local trait inbox (`trait.go`).

**Does not:**

- Own CLI, REST, or MCP surface wiring (those live in `cmd/stem` adapters and `internal/core`).
- Authenticate the admin issue-token HTTP route itself — callers wrap `HandleAdminIssueToken` (e.g. Botanist API key middleware in `cmdserve.go`).
- Persist trait inbox state across process restarts, or run inbound trait network transport.
- Implement full peer-to-peer mesh discovery (the as-built model is local Stem → central Stem graft, not a multi-hop mesh).
- Perform substrate name resolution (Core injects that via `MeshOperations.ResolveWorkspace`).

## Public interface

| Symbol | Role |
| --- | --- |
| `KeyPair` | Ed25519 public/private material used for mesh signing. |
| `GenerateKeyPair` / `WriteKeyPair` / `LoadKeyPair` | Create and PEM-store keys under `.tendril/security/`. |
| `LoadPrivateKey` / `LoadPublicKey` | Load one half of the pair (public may be derived from private if the public file is missing). |
| `WorkspaceSecurityDir` / `WorkspaceKeyPaths` | Resolve security directory and key file paths for a workspace. |
| `TokenOptions` / `TokenValidationOptions` / `TokenClaims` | Issue-time options, verify-time expectations, and JWT claim body (`meshScope`, `workspacePath`, standard JWT fields). |
| `IssueToken` / `IssueWorkspaceToken` | Sign a JWT (default TTL 1h; defaults issuer/subject/audience/scope to mesh graft values). |
| `VerifyToken` / `VerifyWorkspaceToken` | Verify EdDSA signature and optional issuer/audience/scope/workspace/time checks. |
| `PublicKeyFingerprint` | Short SHA-256 fingerprint for CLI/allowlist display. |
| `ResolveRepoRoot` | Prefer `git rev-parse --show-toplevel`; fall back to the given path. |
| `Client` / `NewClient` / `NewClientFromEnv` | Local graft client; env constructor returns `nil` if URL or token is unset. |
| `(*Client).DelegatePush` | Send latest commit patch to the central graft endpoint; return accepted commit hash. |
| `(*Client).SetLogWriter` | Redirect streamed graft logs (default stderr). |
| `Server` / `NewServer` | Central-Stem graft server bound to a workspace root. |
| `(*Server).HandleAdminIssueToken` | `POST` handler: issue a signed JWT from JSON body (`ttl`, audience, etc.). |
| `(*Server).HandleGraftWebSocket` | `GET` upgrade: authenticate, run terrarium governance, push branch. |
| `TraitKind` / `TraitPayload` / `TraitOrigin` / `TraitEnvelope` | Signed trait transport types (plasmid / genotype / sequence). |
| `SignTraitEnvelope` / `VerifyTraitEnvelopeSignature` / `TraitEnvelopeSigningPayload` | Envelope crypto over canonical trait+origin+signedAt JSON. |
| `ResolveTraitAcceptPolicy` | Classify ingress by accept policy string. |
| `TraitInbox` / `NewTraitInbox` | In-memory pending/accepted/rejected trait buckets. |
| `(*TraitInbox).Ingest` / `ListPending` / `Accept` / `Reject` | Inbox lifecycle used by governed trait capabilities. |

Package-level sentinel errors: `ErrMissingPrivateKey`, `ErrMissingPublicKey`, `ErrInvalidToken`, `ErrInvalidSignature`, `ErrExpiredToken`, `ErrNotYetValid`, `ErrTraitNotFound`.

## Dependencies

**Fan-out:** none (leaf). Stdlib + `github.com/gorilla/websocket` only; no other OpenTendril internal packages.

**Fan-in:**

- **`cmd/stem`** — registers `/v1/mesh/admin/issue-token` and `/v1/mesh/graft` on the serve mux; CLI `mesh keygen|issue-token` call auth helpers; governed `mesh.graft|promote|trait.*` wire `NewClientFromEnv` and a process-global `TraitInbox`; `plasmid sign` reuses `LoadPrivateKey` as the node signing key.
- **`internal/conductor`** — `meshpush.go` intercepts auto-push when graft env is set (`DelegatePush`); terrarium plasmid staging optionally verifies signatures via `LoadPublicKey`.
- **`internal/core`** — does **not** import `mesh` (boundary test). Declares governed capabilities and injects execution via `MeshOperations` (`DelegatePush`, trait list/accept/reject ports). Keygen/issue-token stay deliberately ungoverned CLI-local.

## Limitations

- **Default token TTL is one hour** (`defaultTokenTTL` in `auth.go`). Issuers may override; there is no refresh or revocation list — compromise window is “until exp.”
- **Graft WebSocket workspace binding** is enforced: `HandleGraftWebSocket` validates the `workspacePath` claim against `s.workspace` via `TokenValidationOptions.ExpectedWorkspace`. Tokens minted for a different workspace are rejected before the upgrade completes. `HandleAdminIssueToken` ignores any caller-supplied `workspacePath` — issued tokens always carry `s.workspace`.
- **WebSocket `CheckOrigin` is fail-closed** — `NewServer` rejects any upgrade request that carries an `Origin` header. `Origin` is a browser-set header that legitimate service-to-service callers (`mesh.Client`, CLI, another Stem) never send; rejecting its presence is stricter than a same-host allowlist and requires no configuration knob. There is no opt-out.
- **`client_test.go` covers `DelegatePush`'s protocol handling and the local git-patch collection helpers; `server_test.go` covers the git-mechanics of applying/committing/pushing a graft (worktree terrarium, patch apply, push to `origin`).** The one piece that stays untested is `runGovernanceSequence`'s actual re-invocation of the `tendril` binary — there is no test seam for redirecting `os.Executable()`, so the real governance-sequence subprocess step is exercised only in production use, not CI.
- **Trait inbox is memory-only** and lives as a package-global in the Stem process adapter (`meshtraits.go`); `Accept`/`Reject` return an `ErrTraitNotFound`-wrapped error when the supplied trait ID is not present in any bucket (pending, accepted, or rejected), including the empty-string case.
- **Trait inbound transport is not wired** — envelopes, policy, and inbox exist, but there is no network ingress hook.
- **Governance sequence must already exist** on the central workspace (default `.tendril/mesh-governance.yaml`); missing sequence fails the graft.
- **Push requires a configured `origin` remote** and material change after governance; empty/no-op validation returns an error rather than a no-op success.
- **Mesh keys double as plasmid signing keys** for callers outside pure grafting — same PEM material under `.tendril/security/`.


