# Design: Security Posture — Trust Boundaries (OS of OT)

**Issue**: #162
**Status**: Trust-boundary model documented; two of four acceptance criteria
were already satisfied by existing code and are cross-linked below. The
short-lived/delegated-credential criterion remains open — see
[Open gap](#open-gap-short-lived-delegated-credentials).

## Principle

The OS (Command Center) is the optional, potentially multi-user,
network-facing surface, so it must be the **thinnest trusted layer**. It
**delegates and proxies; it does not accumulate** authority or long-lived
secrets. "Less attack surface" is a design rule to enforce, not just an
aspiration — this doc records what is already enforced (with tests) and what
is still owed.

## Trust zones

Three zones, decreasing authority:

### 1. Stem (host) — full authority

Runs on the host machine. Holds the operator's LLM provider keys, the
bearer API key (`OPENTENDRIL_API_KEY`/`ADMIN_TOKEN`, or the auto-generated
`.tendril/api-key` — see #171), the mesh `Ed25519` signing key
(`docs/DESIGN-STEM-GRAFTING.md`), and `history.db`. It is the only zone that
can mint mesh grafting tokens, spawn terrariums, or make LLM provider calls.
Every other zone reaches capability only by asking the Stem.

### 2. OS / Command Center (optional, network-facing) — delegated authority

An opt-in, containerized reverse proxy + static SPA
(`docs/COMMAND-CENTER.md` §5). It holds **no secrets of its own** — the proxy
adds no credentials and bypasses nothing (`ui/nginx/default.conf.template`).
The only credential in play is the operator's bearer key, entered once during
onboarding and stored in the browser's `localStorage`; the container process
itself is stateless. It reaches exactly `/health`, `/v1*`, and `/ws` on the
Stem — nothing else on the host is reachable through it.

### 3. Worker (terrarium sprouts) — zero authority

Ephemeral Docker containers that execute a single Sprout run and then die.
They hold no credentials at all — LLM calls and mesh operations happen on the
Stem, never inside a terrarium. Isolation is enforced at the container
level (`cmd/stem/internal/terrarium/docker.go`):

```
--network none
--cap-drop=ALL
--security-opt=no-new-privileges:true
--pids-limit <default 512, spec-overridable>
--cpus / --memory <spec-overridable>
```

These are asserted by `cmd/stem/internal/terrarium/provider_test.go`
(`TestDockerProviderCreate*`, `Test*ProviderCreateDefaultsToPidsLimit`) —
a regression here fails CI, not just a manual audit.

## Enforcement already in place

Two of #162's four acceptance criteria turn out to already be satisfied by
existing code, not new work:

- **"No command capability beyond the CLI."** #159's
  `TestInterfaceParityCoverage` (`cmd/stem/parity_test.go`) diffs the CLI,
  REST, and MCP capability sets against the canonical `core.CapabilityNames()`
  registry and fails on drift. The OS (REST) surface is mechanically
  constrained to the same governed capability set as the CLI — an attacker
  who fully compromises the OS reaches no command the CLI didn't already
  expose.
- **"Terrarium isolation flags documented and regression-tested."** See
  [Worker](#3-worker-terrarium-sprouts--zero-authority) above — the flags and
  their test coverage already exist; this doc is the missing cross-link.

The remaining two criteria — the credential model, and this trust-boundary
write-up — are addressed here and below.

Separately, the Stem-side auth gaps that motivated re-examining this whole
posture (#171, surfaced during the #170 review) are closed: fail-open API
auth and unauthenticated `/ws` (#172), the legacy unauthenticated
`/dashboard/` file server and a `UI_BIND` non-loopback guardrail (#173), and
CSP `style-src` tightening (#174).

## Open gap: short-lived, delegated credentials

Rule 2 of #162 calls for the OS to carry "delegated, short-lived credentials,
not the keys to everything." Today it does not: the browser holds the exact
same long-lived bearer key the CLI uses (`OPENTENDRIL_API_KEY` or the
generated `.tendril/api-key`). This does **not** violate rule 1 — the key
grants no more capability than a CLI user already has, per the parity test
above — but it does mean a leaked browser key is as durable as a leaked CLI
key, with no expiry and no per-session revocation.

Closing this properly means minting short-lived, OS-scoped tokens (distinct
from the master key) at onboarding, which in turn wants an identity/issuance
story. That's explicitly the enterprise identity/secrets provider decision
tracked in #163 — this doc intentionally does not preempt that design. Once
#163 lands a first provider, revisit this section and #162's remaining
checkbox.

## References

#162 (this workstream) · #159 (interface parity, enforcement) · #171/#172/
#173/#174 (Stem/OS auth hardening) · #163 (enterprise identity/secrets,
blocks the credential-model closure) · `cmd/stem/internal/terrarium/docker.go`
+ `provider_test.go` (worker isolation) · `docs/COMMAND-CENTER.md` (OS
deployment + auth contract) · `docs/DESIGN-STEM-GRAFTING.md` (mesh token
model, the one place delegated short-lived tokens already exist today).
