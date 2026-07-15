# Design: Security Posture & Trust Boundaries

## Principle

The **Tendril OS** (Command Center) is the optional, potentially multi-user,
network-facing surface, so it must be the **thinnest trusted layer**. It
**delegates and proxies; it does not accumulate** authority or long-lived secrets.
"Less attack surface" is a design rule to enforce, not an aspiration — this
document records what is enforced (with tests) and the one place the model is still
maturing.

## Trust zones

Three zones, in decreasing authority.

### 1. Stem (host) — full authority

Runs on the host machine. Holds the operator's LLM provider keys, the bearer API
key (`OPENTENDRIL_API_KEY` / `ADMIN_TOKEN`, or the auto-generated
`.tendril/api-key`), the mesh `Ed25519` signing key (see
`docs/DESIGN-STEM-GRAFTING.md`), and `history.db`. It is the only zone that can
mint mesh grafting tokens, grow Terrariums, or make LLM provider calls. Every other
zone reaches capability only by asking the Stem.

### 2. Tendril OS / Command Center (optional, network-facing) — delegated authority

An opt-in, containerised reverse proxy + static SPA (see `docs/COMMAND-CENTER.md`).
It holds **no secrets of its own** — the proxy adds no credentials and bypasses
nothing (`ui/nginx/default.conf.template`). The only credential in play is the
operator's bearer key, entered once during onboarding and stored in the browser's
`localStorage`; the container process itself is stateless. It reaches exactly
`/health`, `/v1*`, and `/ws` on the Stem — nothing else on the host is reachable
through it.

### 3. Worker (Terrarium Sprouts) — zero authority

Ephemeral Docker containers that execute a single Sprout run and then die. They
hold no credentials at all — LLM calls and mesh operations happen on the Stem,
never inside a Terrarium. Isolation is enforced at the container level
(`cmd/stem/internal/terrarium/docker.go`):

```
--network none
--cap-drop=ALL
--security-opt=no-new-privileges:true
--pids-limit <default 512, spec-overridable>
--cpus / --memory <spec-overridable>
```

These flags are asserted by `cmd/stem/internal/terrarium/provider_test.go`
(`TestDockerProviderCreate*`, `Test*ProviderCreateDefaultsToPidsLimit`), so a
regression fails CI rather than only a manual audit.

## Enforcement

Two properties of this posture are mechanically enforced, not merely documented:

- **No command capability beyond the CLI.** The interface-parity test
  (`TestInterfaceParityCoverage`, `cmd/stem/parity_test.go`) diffs the CLI, REST,
  and MCP capability sets against the canonical `core.CapabilityNames()` registry
  and fails on drift. The Tendril OS (REST) surface is constrained to the same
  governed capability set as the CLI — an attacker who fully compromises the OS
  reaches no command the CLI did not already expose.
- **Terrarium isolation is regression-tested.** The flags in
  [Worker](#3-worker-terrarium-sprouts--zero-authority) above are covered by the
  provider tests noted there, so a weakening of container isolation breaks the
  build.

## Delegated passthrough egress — deny-all default, grant-opened mediation

The passthrough operation-class (`passthrough.run`) runs one bounded command
inside the same sealed Terrarium described above. Its egress model maps the
delegation grant's `egress` allow-list onto the existing isolation seams:

- **Deny-all is physical, not policy.** The docker provider pins
  `--network none` for every Terrarium it creates (regression-tested per the
  Worker section), so the executed command itself can never reach any host —
  with or without a grant, delegated or not.
- **The allow-list opens Stem-mediated reach only.** The single egress channel
  a passthrough execution has is its optional `fetch` list: URLs the **Stem**
  retrieves on the host, *before* the sealed container runs, delivering the
  payloads read-only under `/tmp/egress` inside the Terrarium
  (`cmd/stem/internal/conductor/passthrough.go`). Every fetch URL must name a
  host on the `EgressPolicy` built from the matching grant's `egress` field —
  exact host or host:port, no wildcards. An empty list (any non-delegated
  invocation, or a grant without egress hosts) denies every fetch: deny-all
  requires zero configuration.
- **No self-escalation.** The allow-list travels on an input field with no
  JSON surface (`core.PassthroughRunInput.Egress`, tagged `json:"-"`): only
  the Stem's own call sites populate it, after the delegation authorizer has
  matched a grant, so no transport caller can widen its own egress.

This keeps the sealed-Sprout invariant intact for the new operation-class: a
worker "cannot reach out on its own; external calls are Stem-mediated."

## Credential model — current state and direction

The Tendril OS today holds the **same long-lived bearer key** the CLI uses
(`OPENTENDRIL_API_KEY`, or the generated `.tendril/api-key`). This does not grant
the OS any capability a CLI user lacks (per the parity test above), so it does not
widen the *capability* surface. It does mean a leaked browser key is as durable as
a leaked CLI key — no expiry, no per-session revocation.

The intended direction is **short-lived, OS-scoped tokens** distinct from the
master key, minted at onboarding. Doing this well requires an identity/issuance
story, which is the natural responsibility of an enterprise identity/secrets
provider. The mesh already demonstrates the pattern in a narrower form: delegated,
short-lived signing tokens (`docs/DESIGN-STEM-GRAFTING.md`). Until an OS-scoped
token issuer exists, the bearer-key model is the deliberate, documented interim.

## References

- `cmd/stem/internal/terrarium/docker.go` + `provider_test.go` — worker isolation
  flags and their regression tests.
- `cmd/stem/parity_test.go` — CLI/REST/MCP capability parity enforcement.
- `docs/COMMAND-CENTER.md` — Tendril OS deployment and auth contract.
- `docs/DESIGN-STEM-GRAFTING.md` — mesh token model; the one place delegated,
  short-lived tokens already exist today.
