# Design: Mycorrhizal Mesh Network (Stem Grafting)

**Issue**: #76
**Status**: Approved

## Goal
Implement the **Mycorrhizal Mesh Network** (Stem Grafting) to allow multiple OpenTendril instances to securely cooperate, separating local developer nodes from central governance nodes. Local nodes delegate high-privilege operations (like pushing to remote, or PR promotion) to the Central Stem over a token-gated Mesh API tunnel. 

## Decisions Made
- **Token Cryptography**: `Ed25519` asymmetric cryptography will be used for signed Grafting Tokens. The Central Stem will hold the private key, and Local Stems will present JWTs signed by this key.
- **Tunnel Protocol**: **WebSockets** (`github.com/gorilla/websocket`) will be used to ensure reliable bidirectional log streaming of the AST Assessor and security checks back to the local Stem during a delegation request.
- **Governance Hooks**: The Central Stem will execute a `.tendril/mesh-governance.yaml` sequence inside an ephemeral sandbox before accepting a push. This enables full pre-push CI or fast security gating.
- **Token Management**: A **Layered Approach** is selected.
    1. Basic CLI tools (`tendril mesh keygen` and `tendril mesh issue-token`) for initial/offline setup.
    2. A secure Admin API endpoint (`POST /v1/mesh/admin/issue-token` secured via `ADMIN_TOKEN`) for enterprise automation and integrations.

## Proposed Changes

### Configuration & CLI
#### `cmd/stem/cmd-mesh.go`
- `runMeshKeygenCmd()`: Generates and saves Ed25519 keypairs.
- `runMeshIssueTokenCmd()`: Issues signed JWT tokens via CLI.

### Mesh Network & API
#### `cmd/stem/internal/mesh/auth.go`
- Ed25519 JWT signing and verification logic.
- Key loading logic from `.tendril/security/`.

#### `cmd/stem/internal/mesh/server.go`
- Admin API handler: `POST /v1/mesh/admin/issue-token` (issues a token over API).
- WebSocket handler: `/v1/mesh/graft`. Authenticates the token, receives the git patch/diff payload, runs the `.tendril/mesh-governance.yaml` sequence, and streams logs back to the client in real-time. If valid, pushes to remote.

#### `cmd/stem/internal/mesh/client.go`
- `DialGraft()`: Establishes a secure WebSocket connection to the Central Stem.
- `DelegatePush()`: Transmits local commits to the Central Stem and listens to the streamed validation logs.

#### `cmd/stem/cmd-serve.go`
- Register the Admin API route `mux.HandleFunc("POST /v1/mesh/admin/issue-token", withAdminAuth(meshServer.HandleAdminIssueToken))`.
- Register the WebSocket route `mux.HandleFunc("/v1/mesh/graft", meshServer.HandleGraftWebSocket)`.

### Orchestrator & Governance Tools
#### `cmd/stem/internal/orchestrator/chronicler.go`
- Update `maybeAutoPushGenome` and sequence execution: If `TENDRIL_GRAFT_URL` is set in the environment, intercept the standard `git push` and instead invoke `mesh.DelegatePush()`.

#### `cmd/stem/internal/api/mcp.go`
- Add `graftSubstrate` and `promotePR` to the MCP schema.

## Verification Plan

### Automated Tests
- Unit tests for Ed25519 key generation and JWT validation in `internal/mesh/auth_test.go`.
- Integration test for `POST /v1/mesh/admin/issue-token`.

### Manual Verification
1. Run `tendril mesh keygen`.
2. Configure local environment variables with `TENDRIL_GRAFT_URL`.
3. Start the Central Stem (`tendril serve`).
4. Issue a token using the Admin API or CLI.
5. Trigger a local code edit and watch the WebSocket logs stream the Central Stem's governance checks.
