# Component: Proxy

## Purpose

`cmd/stem/internal/proxy` is a thin outbound HTTP client that proxies requests from the Stem to an external reasoning and inference endpoint, typically a Python-based Tendril brain.

## Responsibilities

**Does:**

- Provide a HTTP client (`BrainClient`) to communicate with the external brain endpoint.
- Proxy chat messages via the `Chat` method, marshaling to a format compatible with the brain's Pydantic models.
- Expose a `Health` check method to verify the reachability of the brain service.
- Forward standard JSON-RPC requests to the brain's `/v1` endpoint via `SendMCPRequest`.

**Does not:**

- Implement any authentication or payload signing; requests to the brain are unauthenticated.
- Perform retries on failed requests or network timeouts.
- Maintain any local state or persistent connections (such as WebSockets).

## Public interface

| Symbol | Role |
| --- | --- |
| `BrainClient` | HTTP client wrapper pointing at the brain service `BaseURL`. |
| `NewBrainClient` | Constructor setting the `BaseURL` and configuring a 300-second HTTP timeout. |
| `(*BrainClient).Chat` | Sends a message (`sessionID`, `message`, `provider`) to `/v1/chat` and returns the response string. |
| `(*BrainClient).Health` | Verifies brain reachability via `GET /health`. |
| `(*BrainClient).SendMCPRequest` | Proxies a JSON-RPC request to the brain's `/v1` endpoint with a hard-coded ID. |

Package-level sentinel errors: none (errors are wrapped inline via `fmt.Errorf`).

## Dependencies

**Fan-out:** none (leaf). Uses only Go standard library packages (`bytes`, `encoding/json`, `fmt`, `io`, `log`, `net/http`, `time`).

**Fan-in:**
- **`internal/server`** — wires `NewBrainClient` to forward inference and tool requests to the external brain endpoint, consistent with the sealed-egress posture where the server mediates all outbound communication.

## Limitations

- **Timeouts/Retries**: The `http.Client` uses a hard-coded 300-second (5-minute) timeout. There is no retry logic for transient network failures or HTTP 5xx errors.
- **Error Propagation**: Errors are inline wrapped, meaning callers cannot easily type-assert or check against sentinel errors to distinguish timeouts from bad payloads.
- **Auth**: The client carries no authentication headers or bearer tokens, and requests are not cryptographically signed.
- **Hard-coded assumptions**: The client assumes specific API paths (`/v1/chat`, `/health`, `/v1`) and hard-codes the JSON-RPC `"id": 1` in `SendMCPRequest`.
- **Missing Tests**: There is no `brain_test.go` present to validate the proxy's HTTP interactions.

## Design & rationale

`cmd/stem/internal/proxy` acts as a minimal HTTP translation layer. Rather than embedding complex inference logic in the Go codebase, the architecture delegates these tasks to an external brain endpoint. By mediating these calls through `BrainClient`, the system maintains a sealed-egress posture, ensuring that all interactions with the external endpoint pass through a predictable, centrally controlled Stem proxy rather than being scattered. The client remains deliberately thin, translating internal method calls into simple JSON and JSON-RPC HTTP POSTs.
