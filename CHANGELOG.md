# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
where versions are cut.

## [Unreleased]

### Fixed

- **Chat/WS mounted a `/tmp` shadow instead of the managed checkout.** After
  the managed Substrate at `~/.tendril/substrates/<name>` was populated, the
  Greenhouse name-only grow path still created a `/tmp/opentendril-terrarium-*`
  shadow worktree and passed that as the Docker `-v` source. Rootless Docker
  cannot see host `/tmp`, so it created an empty directory and `/app` had no
  `.git` or `docs/`. Chat/WS now bind-mounts the resolved managed checkout
  itself — the same host path CLI `tendril sprout grow --substrate <name>`
  already used. A bare name is refused (it would become an empty Docker
  volume). Stem-home refusal is unchanged.
- **Empty Terrarium workspace on Greenhouse/chat grow.** Chat copies only the
  Phytomer Substrate name onto the orchestrator. A managed checkout directory
  that existed but had no `.git` (failed materialization, leftover
  placeholder) then counted as a local workspace, so clone/fetch was skipped
  and Docker bind-mounted an empty `/app`. An unpopulated managed checkout is
  now treated as absent and cloned from the named Substrate URL before mount,
  matching CLI `tendril sprout grow --substrate <name>`. A missing Substrate
  still fails with the existing required-substrate / workspace-absent
  refusal.
- **Managed clone under a parent git repository.** After the empty-placeholder
  path started cloning, refresh still used `git rev-parse`, which walks
  parents. An empty managed directory under Stem home then looked like a
  repository, refresh ran against the parent, and the Terrarium still mounted
  an empty `/app`. Clone/refresh now requires `.git` in the checkout
  directory itself.
- **Repo-map / Substrate scan under rootless Docker.** A short Greenhouse task
  used the Stem working directory (the principal's home) as the implicit
  Substrate. The Rhizome walk then entered the rootless daemon's containerd
  snapshot tree and withered on `permission denied` before any Mycorrhizal
  request. Unnamed runs now use the unique managed checkout when the implicit
  path is the Stem home; `repoRoot` will not walk out of a managed checkout
  into a parent repository; and the scanner skips container runtime trees and
  unreadable sibling directories instead of failing the whole map.

### Added

- **Provider-auth preflight before emergence.** `RunSprout` (CLI and chat) asks Roots
  for a minimal authenticated provider interaction after the workspace is ready
  and before `sprout-emerged` or Terrarium creation. An HTTP 401/403/407 becomes
  a terminal `provider-auth-rejected` run with a credential-free diagnostic,
  `providerRequestAttempted` reflecting the probe, and zero tool invocations.
  A successful probe continues the existing emerge path. Mid-run auth failures
  still use the existing classification path.
- **Phytomer Substrate preference.** A session can carry a first-class
  `preferences.substrate` (create/PATCH, persisted and merged like
  provider/model/genotype). Greenhouse chat binds and shows that name; the
  chat grow path passes it into the Sprout instead of the Stem working
  directory. An empty Substrate still hits the existing required-substrate
  refusal.
- **Sprout-run observation contract.** Finished Sprout runs persist the
  Conductor outcome plus a Core-owned `failureCategory`, a credential-free
  `providerDiagnostic`, `providerRequestAttempted`, and `toolInvocations`.
  EventBus emits `mycorrhizal-request-begun` when the first Mycorrhizal
  request is issued, and terminal sprout events carry the same structured
  fields. The Greenhouse drill-down renders those facts as the primary
  explanation and does not parse raw error text to decide a category.
- **Two-tier Pollinator REST auth.** A durable Pollinator credential
  (`tendril_root_…`, issued by `tendril pollinator issue`) is the **refresh root**.
  Callers mint short-lived, Stem-signed **access tokens** (`tendril_access_…`, hard-capped
  at ≤15 minutes) from that root and present the token on data routes:
  - `POST /v1/pollinator/token` (durable root in `Authorization: Bearer`)
  - `tendril pollinator token --pollen <name> [--ttl <dur>]`
  Revocation is at the root: revoke the credential and minting stops; outstanding
  tokens age out within the cap. Tokens verify by signature (stateless — no
  per-token store or denylist).

### Changed

- **Greenhouse run review.** Opening a matured or withered Sprout run puts
  structured observation, the task transcript, and tool activity above the
  fold in the garden column. The decorative garden and Event Pulse move aside;
  raw telemetry stays collapsed until opened.
- **Daemon bind host** is selected by `TERROIR_HOST` (see Breaking).
- **Stem bearer-key / daemon gate** env var is `BOTANIST_KEY` (see Breaking).

### Security

- **Loopback-default bind.** Unset `TERROIR_HOST` binds `127.0.0.1`, so the REST
  surface is not reachable off-host unless the operator opts in.
- **Off-host requires access tokens.** When the daemon binds off-host (e.g.
  `TERROIR_HOST=0.0.0.0`), durable Pollinator credentials are **refused** on data
  routes; callers must present a short-lived access token. The mint endpoint still
  accepts the durable root. On loopback, the durable root remains accepted on data
  routes for local convenience.
- **Stateless token verification.** Access tokens are Stem-signed and verified by
  signature against the Stem public key; there is no shared per-token denylist.

### ⚠ BREAKING

These environment-variable and default changes land with the scoped-token uplift.
Operators must adjust before or at upgrade if they rely on the old defaults.

| Change | Old behavior | New behavior | Migration |
| --- | --- | --- | --- |
| **`TERROIR_HOST` bind default** | Daemon bound all interfaces implicitly | Defaults to loopback (`127.0.0.1`) | Set `TERROIR_HOST=0.0.0.0` (or a specific interface) and issue access tokens to keep off-host access |
| **Stem bearer-key env var → `BOTANIST_KEY`** | A generic admin-token env var gated the Stem bearer | Same behavior under the sole name `BOTANIST_KEY` (legacy aliases dropped; no silent fallback) | Rename the former admin-token env var to `BOTANIST_KEY` in your env (and any unit files / secrets managers) |
