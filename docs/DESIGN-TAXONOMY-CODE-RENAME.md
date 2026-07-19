# Design: Taxonomy Code-Rename Manifest

Implements the code side of the taxonomy reconciliation (see
[SYNTHETIC-TAXONOMY.md](../SYNTHETIC-TAXONOMY.md) / [GLOSSARY.md](../GLOSSARY.md)):
**"Tendril" is the concept/brand only; the ephemeral worker is a Sprout.**

This manifest is grounded in an audit of the current tree. It classifies every
`Tendril` identifier and every `run` verb into **rename** vs **keep**, and stages
the work so the safe, internal changes land first and the breaking public-surface
changes are deliberate and optional.

> **Status (refreshed against `main`):** Slices **1 & 2 have landed** in #241
> (`refactor(conductor): rename worker Tendril identifiers to Sprout`). Slices 3,
> 3b, and 4 remain open. All paths below are re-grounded on the post-restructure
> tree: the Go kernel lives under `cmd/stem/`, and the command files were merged
> to lowercase (`cmd-sprout.go` → `cmdsprout.go`) in #272.

> **Guardrails (all slices):** after each slice, `go build ./...` +
> `go test ./...` in `cmd/stem` must pass, including `TestInterfaceParityCoverage`
> and the `internal/core` boundary tests. Branch + Draft PR; human merges.

---

## Slice 1 — Safe internal renames (worker `Tendril` → `Sprout`) ✅ DONE — #241

No public surface (no MCP tool names, no CLI tokens, no capability strings, no
on-disk config). Pure Go identifiers + their tests. Landed in #241.

| Was | Now | Location |
|---|---|---|
| `RunTendril` (method) | `RunSprout` | `cmd/stem/internal/conductor/docker.go:116` |
| `tendrilRunner` (interface) | `sproutRunner` | `cmd/stem/internal/conductor/docker.go:55` |
| `tendrilExecutionStatus` (type) | `sproutExecutionStatus` | `cmd/stem/internal/conductor/docker.go:987` |
| `newTendrilExecutionID` | `newSproutExecutionID` | `cmd/stem/internal/conductor/docker.go:995` |
| `writeTendrilStatus` / `loadTendrilStatus` | `writeSproutStatus` / `loadSproutStatus` | `cmd/stem/internal/conductor/docker.go:1140,1123` |
| `buildTendrilCommitMessage` | `buildSproutCommitMessage` | `cmd/stem/internal/conductor/docker.go` |
| `summarizeTendrilPrompt` / `summarizeTendrilFailureError` | `summarizeSprout…` | `cmd/stem/internal/conductor/docker.go` |
| `stubTendrilRunner` + the `…Tendril…` tests | `stubSproutRunner` + `…Sprout…` | `cmd/stem/internal/conductor/*_test.go` |

Risk was **low** (compiler catches every miss; no behavior change). Verified
still-clean on `main`: no worker-`Tendril` identifiers remain in `conductor/`.

---

## Slice 2 — Runtime strings & the Go Sprout binary ✅ DONE — #241

| Was | Now | Location |
|---|---|---|
| branch prefix `tendril/task-%s` | `sprout/task-%s` | `cmd/stem/internal/conductor/docker.go:249` |
| binary `tendril-go` | `sprout-go` | `sprouts/go/Dockerfile:8,14,18` (build `-o`, `COPY`, `ENTRYPOINT`) |
| comment refs to `tendril-go` | `sprout-go` / `opentendril-go` | `sprouts/go-fuzz/Dockerfile`, `cmd/stem/internal/conductor/macrophage.go` |

**Kept image names** `opentendril-go:latest` / `opentendril-go-fuzz:latest` —
`opentendril` is the brand, `go` is the language; not worker-`Tendril` usages.

---

## KEEP — do NOT rename (brand / config / data)

These use "tendril" in the **brand or config** sense and must stay.

| Identifier / name | Why keep |
|---|---|
| `tendril` CLI command; `~/.tendril`, `./.tendril/`; `TENDRIL_*` env vars | Brand/command; the dotdir matches the command (correct convention). |
| `opentendril-*:latest` images | Brand + language, not the worker. |
| `ConfiguratorTendril`, `NewConfiguratorTendril` | Generate `.tendril` **config** (`cmd/stem/internal/configurator/tendril.go:16,21`). |
| The `.tendril/config.yaml` loader + path resolution | Load brand config (identifiers were refactored post-manifest; still brand-config, still keep). |
| `TendrilDir` | Path to the `.tendril` config dir (`cmd/stem/internal/receptors/config.go:146`). |
| `TendrilMemory` / `weaviateMemoryClass` (`cmd/stem/internal/rhizome/weaviate.go:17`) | A stored-data schema class; renaming = a data migration. Defer to a separate schema task if ever wanted. |

---

## Slice 3 — `run` → `grow` verb (BREAKING; do deliberately or defer)

This touches the **parity-enforced public surface** (MCP + REST + CLI must move
together, or `TestInterfaceParityCoverage` fails) and **external clients** (Claude
Code, scripts) call these names. Recommend an **alias/deprecation window** rather
than a hard cut.

| Surface | Current | New |
|---|---|---|
| Capability constant | `CapSequenceRun = "sequence.run"` | `"sequence.grow"` (`cmd/stem/internal/core/registry.go:32`) |
| Capability constant | `CapSproutRun = "sprout.run"` | `"sprout.grow"` (`cmd/stem/internal/core/registry.go:33`) |
| MCP tool name | `runSequence` | `growSequence` (`cmd/stem/internal/receptors/mcp.go:405` def, `:875` handler) |
| MCP tool name | `sproutTendril` | `growSprout` (`cmd/stem/internal/receptors/mcp.go:419` def, `:925` handler) |
| CLI token | `tendril sprout run` | `tendril sprout grow` (`cmd/stem/cmdsprout.go:309`) |
| CLI token | `tendril sequence run` | `tendril sequence grow` (`cmd/stem/cmdsequence.go:40,412`) |

**Recommendation:** accept **both** `run` (legacy) and `grow` (new) for a
deprecation window on each surface, log a deprecation notice on the legacy form,
then remove `run` in a later release. This keeps existing MCP clients and scripts
working. Internal Go method `RunSprout` (from Slice 1) may also become `GrowSprout`
in this slice — but that is an *internal* verb change, safe to bundle here.

Risk: **high** (public API / muscle memory). Do only when ready to communicate it.

### Slice 3b — `Session` → `Phytomer`

A session is a **Phytomer** (taxonomy addition, #242) — *not* "a Tendril", which
is what `session.go` currently calls it. This rename splits into two scopes:

**Internal (safe, do now):** fix the mislabel in Go identifiers and docstrings.

| Current | New | Location |
|---|---|---|
| `type Session` | `type Phytomer` | `cmd/stem/internal/session/session.go:87` |
| `type Manager` (session) | `type PhytomerManager` (or `Manager` in a `phytomer` pkg) | `cmd/stem/internal/session/session.go:122` |
| `Manager.Sprout()` / `GetOrSprout()` | `Manager.Bud()` / `GetOrBud()` | `session.go:164,210` — minting a session = **budding a Phytomer** (a Meristem buds it), *not* a Sprout worker |
| `"a Tendril session"` docstrings, `IDPrefix` comment | "a Phytomer" | `session.go`, `cmdserve.go`, `cmdmcp.go` |

**Public surface (BREAKING — decide scope):** `session` is a universally-understood
API term, and our own translation-boundary rule (SYNTHETIC-TAXONOMY §9) permits
standard IT terms *at the external boundary*. So there is a real choice:

| Surface | Keep as boundary term | Or rename (with alias) |
|---|---|---|
| REST | `/v1/sessions`, `/v1/sessions/{id}/events` | `/v1/phytomers/…` |
| Capabilities | `session.create\|list\|get\|update\|delete\|history` | `phytomer.*` (`cmd/stem/internal/core/registry.go`) |
| HTTP header | `X-Tendril-Session` | `X-Phytomer` (`cmd/stem/cmdchat.go:180`, `cmd/stem/cmdserve.go:568,572`) |
| CLI | `tendril session …` | `tendril phytomer …` (`cmd/stem/cmdsession.go`) |

**Recommendation:** do the **internal** rename with Slice 3 (fixes the "a Tendril"
mislabel, no external impact). For the **public** surface, my lean is to **keep
`session` at the boundary** (it is the API lingua franca, like `run` for humans) —
Phytomer stays the internal/canonical concept. If you'd rather botanise the API
too, rename with `session`/`X-Tendril-Session` kept as deprecated **aliases**. The
`X-Tendril-Session` header also still carries the retired "Tendril" worker/brand
mix regardless, so at minimum it should become `X-Phytomer` (or `X-Session`).

Risk: internal = low; public = high (REST/MCP/CLI + header).

---

## Slice 4 — Retire the "OT" abbreviation

The brand is **OpenTendril** (written) / **Tendril** (short); **"OT" is banned**
(it means *Operational Technology* in security/SDLC). Remaining cleanup:

**Code — `OT_*` env vars → `TENDRIL_*`** (also fixes prefix inconsistency with the
existing `TENDRIL_*` / `OPENTENDRIL_*` conventions):

| Current | New | Location | Note |
|---|---|---|---|
| `OT_GIT_TOKEN` | `TENDRIL_GIT_TOKEN` | `cmd/stem/internal/conductor/credentials.go:153` + the credential-helper string (`:165`) + tests | Internal process env set by the Stem; safe, but update the helper string and tests together. |
| `OT_LIVE_APP_ID` | `TENDRIL_LIVE_APP_ID` | `cmd/stem/internal/conductor/githubapp_test.go:229` | Test-only (live GitHub App check); dev sets these manually. |
| `OT_LIVE_APP_KEY` | `TENDRIL_LIVE_APP_KEY` | `githubapp_test.go:230` | |
| `OT_LIVE_APP_REPO` | `TENDRIL_LIVE_APP_REPO` | `githubapp_test.go:231` | |

Risk: **low** (internal/test env); no user-facing config surface. Verify the
credential-helper still serves the token after the rename (there is a live test).

**Docs — remaining `\bOT\b` sweep.** `PROGRESS.md` and `feature-requests.md` have
since been **deleted** ("GitHub is the source of truth"), and
`docs/DESIGN-SECURITY-POSTURE.md` was overhauled OT-free in #243. The two files
that still carry a bare `OT` are **`docs/COMMAND-CENTER.md`** and
**`docs/DESIGN-SUBSTRATE-CONFIG.md`**. Replace with `Tendril` (prose) /
`OpenTendril` (formal), and "OS of OT" → **Tendril OS** (matching
ARCHITECTURE/README).

---

## Suggested order (remaining work)

1. ~~**Slice 1** (safe internal nouns)~~ — **landed in #241.**
2. ~~**Slice 2** (runtime strings + Go binary)~~ — **landed in #241.**
3. **Slice 4** (`OT_*` env vars + remaining doc sweep) — lowest-risk of what's
   left; the env renames are internal/test-only and the doc sweep is two files.
   Good next PR.
4. **Slice 3 / 3b** (verb + breaking surface) — separate, deliberate PR with the
   alias/deprecation strategy, gated on a product decision about the public verb
   and session-boundary change. The *internal* halves (verb on `RunSprout`,
   `Session`→`Phytomer` identifiers) can land ahead of the public cutover.
