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
- Persist trait inbox state across process restarts, or run inbound trait network transport (explicitly deferred in `trait.go` comments).
- Implement full peer-to-peer mesh discovery (the as-built model is local Stem → central Stem graft, not a multi-hop swarm).
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

Package-level sentinel errors: `ErrMissingPrivateKey`, `ErrMissingPublicKey`, `ErrInvalidToken`, `ErrInvalidSignature`, `ErrExpiredToken`, `ErrNotYetValid`.

## Dependencies

**Fan-out:** none (leaf). Stdlib + `github.com/gorilla/websocket` only; no other OpenTendril internal packages.

**Fan-in:**

- **`cmd/stem`** — registers `/v1/mesh/admin/issue-token` and `/v1/mesh/graft` on the serve mux; CLI `mesh keygen|issue-token` call auth helpers; governed `mesh.graft|promote|trait.*` wire `NewClientFromEnv` and a process-global `TraitInbox`; `plasmid sign` reuses `LoadPrivateKey` as the node signing key.
- **`internal/conductor`** — `meshpush.go` intercepts auto-push when graft env is set (`DelegatePush`); terrarium plasmid staging optionally verifies signatures via `LoadPublicKey`.
- **`internal/core`** — does **not** import `mesh` (boundary test). Declares governed capabilities and injects execution via `MeshOperations` (`DelegatePush`, trait list/accept/reject ports). Keygen/issue-token stay deliberately ungoverned CLI-local.

## Limitations

- **Default token TTL is one hour** (`defaultTokenTTL` in `auth.go`). Issuers may override; there is no refresh or revocation list — compromise window is “until exp.”
- **Graft WebSocket verification** checks issuer, audience, and scope defaults but **does not bind `workspacePath`** from claims to the server workspace (`HandleGraftWebSocket`).
- **`IssueToken` never sets `nbf`** even though `VerifyToken` honors it — not-before is unused on the happy path.
- **WebSocket `CheckOrigin` always returns true** — origin restriction is not enforced at the mesh layer.
- **No `client_test.go`** and no integration test for the graft WebSocket path; server tests cover admin token issue only.
- **Trait inbox is memory-only** and lives as a package-global in the Stem process adapter (`meshtraits.go`); Accept/Reject are silent no-ops when the trait id is unknown.
- **Trait inbound transport is not wired** — envelopes, policy, and inbox exist; the comment in `trait.go` states the ingress hook is a later slice.
- **Governance sequence must already exist** on the central workspace (default `.tendril/mesh-governance.yaml`); missing sequence fails the graft.
- **Push requires a configured `origin` remote** and material change after governance; empty/no-op validation returns an error rather than a no-op success.
- **Mesh keys double as plasmid signing keys** for callers outside pure grafting — same PEM material under `.tendril/security/`.

## Design & rationale

Stem Grafting separates **local developer Stems** from a **central Stem** that holds high-privilege git credentials and runs pre-push governance. Local nodes never need durable remote write access: they hold a short-lived JWT signed by the central workspace’s private Ed25519 key and open a WebSocket tunnel (`/v1/mesh/graft`). The client sends only the latest commit’s binary patch plus branch/message metadata; the server applies it in a detached worktree terrarium, runs the mesh governance sequence (re-invoking the same `tendril` binary with graft env vars stripped to avoid recursive delegation), streams stdout/stderr as `graft-log` messages, then pushes `HEAD` to `refs/heads/<branch>` on `origin`.

Token design (from the shipped model, matching the original plan’s crypto choices): compact self-contained JWTs with EdDSA (`EdDSA` / `JWT` header), camelCase claims (`meshScope`, `workspacePath`), and defaults issuer `opentendril-mesh`, subject `stem-graft`, audience/scope `mesh-graft`. Key material lives only on the Stem host under `.tendril/security/` (private `0600`, public `0644`). Layered issuance matches production: offline `tendril mesh keygen` / `issue-token`, plus `POST /v1/mesh/admin/issue-token` for automation when the admin gate allows it.

Trait envelopes extend the same Ed25519 trust root toward **epigenetic trait** exchange (plasmid/genotype/sequence payloads with origin fingerprint and signature). Policy evaluation (`deny` / `manual` / `allowlist`) is implemented; network gossip and durable inbox storage are intentionally not part of this leaf yet. That is a deliberate half-step toward the broader Mycelial Mesh goal, not a claim that P2P trait broadcast is complete.

Where the legacy plan (`DESIGN-STEM-GRAFTING`) and the code diverge, the code is authoritative: client entrypoints are `NewClient` / `DelegatePush` (not `DialGraft`); promote is a Core-governed capability that reuses the same delegated push; governance is a Sequence run, not a free-form “AST Assessor” hard-wire inside mesh.
