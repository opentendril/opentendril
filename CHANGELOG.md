# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
where versions are cut.

## [Unreleased]

### Added

- **Two-tier Pollinator REST auth.** A durable Pollinator credential
  (`tendril_…`, issued by `tendril pollinator issue`) is the **refresh root**.
  Callers mint short-lived, Stem-signed **access tokens** (`tendrilat_…`, hard-capped
  at ≤15 minutes) from that root and present the token on data routes:
  - `POST /v1/pollinator/token` (durable root in `Authorization: Bearer`)
  - `tendril pollinator token --pollen <name> [--ttl <dur>]`
  Revocation is at the root: revoke the credential and minting stops; outstanding
  tokens age out within the cap. Tokens verify by signature (stateless — no
  per-token store or denylist).

### Changed

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
