# Design RFC: Hyphae — Ephemeral, Bounded Context Paths

## Status
- [x] proposed
- [ ] approved
- [ ] blocked
- [ ] superseded

## Decision owner
Human Control Plane

---

## Problem statement

When a Phytomer opens and a Sprout is about to grow, the Stem currently makes a
coarse, all-or-nothing decision about what evidence to place in the Sprout's
context window. The Rhizome generates a flat Repo Map from the full Substrate
index and the Conductor stages it as a single Plasmid before each growth. That
Repo Map is structurally complete but contextually blind: it does not know which
symbols, files, tests, prior decisions, or Git state are *relevant* to the
specific Transcript the Sprout is about to grow. The result is one of two
failure modes:

1. **Over-injection**: the Repo Map consumes a large fraction of the available
   context window, crowding out the Transcript, Genotype, and any other Plasmids
   — and the Mycorrhiza receives more structural noise than signal.
2. **Under-injection**: to avoid filling the window, the map is truncated or
   elided, and the Mycorrhiza must reason from insufficient structural evidence,
   increasing hallucination and wrong-file edits.

Neither failure is tolerable for unattended Sprout work against complex
Substrates. The plant needs a way to *select and assemble* evidence that is
bounded, provenance-tracked, and scoped to the Phytomer's concrete task — not
loaded whole from a standing index.

---

## Taxonomy placement

**Classification: a core organ of the Stem**, not a Symbiotic Nodule, not a
Root, and explicitly not a replacement for the Rhizome.

Applying the decision heuristic from `SYNTHETIC-TAXONOMY.md` §5:

- Hyphae perform **local computation on the plant's own material** — they
  traverse the Rhizome's existing SQLite index, read Git state from the local
  clone, and inspect the Substrate's own test and document tree. No external
  network service is called; no provider API is consulted.
- Hyphae do not parse the Substrate themselves. They **select and assemble**
  evidence from what the Rhizome has already indexed. The Rhizome remains the
  parser and index; Hyphae are the selector and assembler.
- The assembled evidence reaches a Phytomer as a set of bounded **Plasmids**
  injected via the existing Xylem path. Hyphae are not a new transport layer;
  they produce output in a form the plant already understands.
- Context-selection policy is governed business logic. Per `AGENTS.md` §6, it
  must live in the Stem's `core` registry, not in a REST handler, MCP adapter,
  or CLI subcommand.

**Why "Hyphae"**: in mycology, hyphae are the filamentous threads that a fungus
extends through a substrate to locate, contact, and extract nutrients — directed
growth toward a resource, not bulk absorption. The metaphor is exact: a Hypha
reaches into the Rhizome and Substrate, locates relevant evidence, and delivers
it in bounded form to the Phytomer. Hyphae are ephemeral (they grow for one
growth cycle and expire); the Rhizome is the persistent underground structure
they extend into.

**Organ placement**: Hyphae logic belongs in `cmd/stem/internal/hyphae` as a
new internal package. The Conductor calls into it (analogous to how it calls
`rhizomefacade.go`) to assemble the context Plasmid set before each growth. The
Stem core governs the boundary: retrieval budget, freshness policy,
provenance, and context-assembly rules are expressed as a governed capability in
the `core` registry.

**What Hyphae are NOT**: Hyphae are not a reasoning component. They do not call
the Mycorrhizal Network. They do not embed text or perform semantic ranking.
They do not store any new persistent state. They are a deterministic selection
and assembly organ that the Stem drives, and the Mycorrhiza receives and reasons
over the output.

---

## Target model

### Hyphae lifecycle

A Hypha is ephemeral. It exists for exactly one context-assembly event: the
interval between the Stem deciding a Phytomer/Sprout requires evidence and the
Stem injecting that evidence as Plasmids into the Terrarium. Six phases govern
the lifecycle:

```
discover → traverse/select → assemble → inject → observe → expire
```

**1. discover**

The Stem identifies the evidence sources available for a given Phytomer and
Substrate. Evidence sources are:

- **Rhizome index**: the existing SQLite FTS5 symbol and file index produced by
  `cmd/stem/internal/rhizome`. Hyphae query this index; they do not re-scan or
  re-parse the Substrate.
- **Git state**: the current branch HEAD, diff from the merge-base against the
  default branch, tracked-but-unstaged changes, and recent commit log (bounded
  by commit count and diff size). Read from the local git clone via
  `os/exec`-driven git commands, not a new git library dependency.
- **Canonical docs**: files matching a configurable allowlist of canonical
  document paths within the Substrate (e.g. `README.md`, `ARCHITECTURE.md`,
  `GLOSSARY.md`, Substrate-specific configuration files, and files named in the
  genome). Loaded from the local Substrate tree; not fetched remotely.
- **Tests**: test files that are co-located with, or named for, the symbols
  selected in the traverse/select phase. Hyphae do not run tests; they select
  their source as evidence.
- **Prior Fruit**: materialized decision records and epigenetic learnings stored
  in the Taproot or genome that are provably connected to the Phytomer's task.
  Only Fruit that can be linked by a symbol name, file path, or explicitly named
  concept in the Transcript is eligible.
- **Execution evidence**: the Stem's own structured log of prior Sprout
  outcomes, verifier results, and macrophage crash reports for the same
  Substrate, within a freshness window.

Sources are checked against freshness criteria (see **Freshness and
invalidation** below) before any evidence is admitted.

**2. traverse/select**

Given the Transcript for the Phytomer, Hyphae traverse the available evidence
sources to identify relevant evidence. Traversal is deterministic and governed
by explicit selection rules — not a semantic embedding or ranking call.

Selection rules (in priority order):

1. **Direct file/symbol match**: files or symbols whose names appear verbatim in
   the Transcript, or are listed by the active Sequence step as explicit context
   anchors.
2. **Git-diff adjacency**: files modified in the current branch's diff against
   the merge-base. Their Rhizome symbols are included up to the symbol budget.
3. **Import/reference neighbourhood**: for each directly matched symbol, the
   Rhizome index provides its import graph neighbourhood (direct dependencies
   and dependents, one hop). Selected up to the symbol budget.
4. **Test co-location**: test files whose path differs from a selected source
   file only by a conventional suffix (e.g. `_test.go`, `.test.ts`, `test_`
   prefix). Included up to the file budget.
5. **Canonical doc inclusion**: docs in the allowlist whose declared topic (if
   any) intersects the Transcript's known symbols or file paths. Included up to
   the doc-byte budget.
6. **Prior Fruit**: Taproot entries linked by symbol or explicit name to any
   already-selected symbol or file. Included up to the evidence-item budget.
7. **Execution evidence**: verifier/macrophage results for already-selected
   files, within the freshness window. Included up to the evidence-item budget.

A rule yields no output if its budget is exhausted. Rules are applied in order;
a source admitted by an earlier rule is not counted twice against later budgets.

Selection is performed without calling the Mycorrhizal Network. If the Stem
cannot determine relevance from the explicit rules above, the evidence is
excluded — not guessed. Uncertain evidence is not better than no evidence.

**3. assemble**

Selected evidence is assembled into a bounded Plasmid set. Each Plasmid:

- carries a **provenance block** naming the selection rule that admitted it,
  the source type (symbol/file/doc/fruit/execution-evidence), and the
  freshness timestamp of its evidence source;
- is formatted as the Rhizome already formats its existing map output
  (markdown, symbol stubs, line spans) to avoid introducing a new format
  dependency;
- is tagged with its evidence class so the Mycorrhiza can weight it
  appropriately without guessing.

The assembler enforces the aggregate token budget across all assembled Plasmids.
It truncates at natural boundaries (file boundary, symbol boundary, paragraph
boundary) rather than hard-cutting mid-line. The Stem logs the assembled
Plasmid manifest (evidence class, source path, byte size, selection rule,
freshness) at the governed log level before injection.

**4. inject**

The assembled Plasmid set is staged into the Terrarium through the existing
Xylem injection path in `cmd/stem/internal/conductor/docker.go`, replacing the
current coarse Repo Map injection. The Hyphae context Plasmids and the genome
Plasmids (Genotype, epigenetics) are staged as separate Plasmids so that neither
eclipses the other in the context window layout.

The inject phase is the terminal write. No Hypha state is persisted after
injection; the Hyphae object for that growth cycle is released.

**5. observe**

During the Sprout's growth, the Stem records which evidence was injected (the
Plasmid manifest logged in phase 3) and whether the Sprout's actions touched the
corresponding files or symbols. This observation is written to the structured
execution log only — not to the Rhizome, not to the Taproot, and not back into
the Hypha traversal logic. The observation record exists for post-mortem
analysis by the Botanist and for future Acceptance-Criteria validation.

**6. expire**

When the Phytomer's Sprout matures or withers, the Hyphae context for that
growth cycle is expired. Expiration means: the in-memory Plasmid manifest is
discarded; any per-Hypha transient file reads (Git diff output, canonical doc
slices) are released. No Hyphae state survives a Phytomer boundary.

---

### Per-Phytomer budgets

Budgets are enforced by the Stem and are not negotiable by the Mycorrhiza or by
Sequence YAML. All budgets are configurable via environment variables so an
operator can tune them to the Terroir's model capacity without changing code.
Default values are conservative for unattended operation on standard-tier models.

| Budget axis | What it caps | Default |
|---|---|---|
| **Aggregate token budget** | Total estimated tokens across all assembled Hyphae Plasmids (exclusive of Genotype and Transcript Plasmids) | Configurable; default is a fraction of the model's known context window, leaving headroom for Genotype, Transcript, and reply |
| **File count** | Number of distinct source/test files admitted across all selection rules | Configurable; default small enough to prevent full-tree loading |
| **Symbol count** | Number of Rhizome symbol stubs included | Configurable; default tuned to the flat Repo Map's existing practical symbol count |
| **Evidence item count** | Number of prior-Fruit entries and execution-evidence items admitted | Configurable; small default because these carry dense prose |
| **Doc-byte budget** | Aggregate byte size of canonical-doc slices | Configurable; prevents a large ARCHITECTURE.md from consuming the window |
| **Git diff line budget** | Number of diff lines admitted from Git state | Configurable; prevents a large refactor diff from crowding out symbol evidence |

If a configured budget is zero for an evidence class, that class is skipped in
traversal without error. The Stem logs each class's admitted byte/count against
its budget in the manifest.

Budget enforcement is a Stem responsibility, not a Rhizome responsibility and
not a Mycorrhiza responsibility.

---

### Freshness and invalidation

Evidence is considered fresh against the Git ref of the Substrate at the moment
the Hypha is invoked. Staleness criteria:

- **Rhizome index freshness**: the Rhizome records a content SHA-256 hash per
  file at indexing time. A file's symbols are stale if the hash in the index
  does not match the current working-tree hash of that file. Stale symbols are
  excluded from selection; their file is included in the Git-diff adjacency set
  instead if it appears in the current diff. The Stem logs stale-symbol
  exclusions in the Plasmid manifest.
- **Git diff freshness**: Git diff is read at Hypha invocation time against the
  local clone's current HEAD. There is no TTL; the diff is always fresh at
  invocation because it is computed from the live index, not cached.
- **Canonical doc freshness**: a canonical doc is stale if its mtime is older
  than the last Rhizome scan for its file. Stale docs are excluded. The Stem
  logs the exclusion reason.
- **Prior Fruit freshness**: a Taproot or epigenetic entry is admitted only if
  its recorded merge-commit SHA is reachable in the current Substrate's history.
  An entry whose merge commit is not in the local clone's reachable history (e.g.
  because it was recorded against a different branch or a squash-merged history)
  is excluded.
- **Execution evidence freshness**: verifier and macrophage results are admitted
  only within a configurable time window (default: recent run history, bounded
  by run count, not by wall time, to avoid coupling evidence quality to the
  speed of the SDLC).

Invalidation is stateless from Hyphae's perspective: each invocation re-checks
freshness from first principles. There is no cache of "what was fresh last
time." The cost of a freshness check must be bounded and local (no network, no
new subprocess beyond a single `git show` or hash comparison).

---

### Provenance

Every Plasmid assembled by Hyphae carries a provenance block. The provenance
block is a structured comment at the head of the Plasmid content:

```
<!-- hyphae-provenance:
  rule: <selection rule name, e.g. "direct-file-match">
  source-type: <symbol | file | doc | fruit | execution-evidence>
  source-path: <relative path within the Substrate>
  freshness-ref: <git commit SHA or content hash>
  budget-axis: <which budget this evidence was admitted under>
  admitted-bytes: <byte count of this evidence item>
-->
```

The Stem logs the provenance blocks for all admitted evidence in the structured
execution log before injection. Provenance is append-only: once written to the
log, it is never modified or removed.

The purpose of provenance is auditability, not reasoning. The Mycorrhiza may
read provenance blocks to understand why evidence was included, but the Stem
never asks the Mycorrhizal Network to validate or rank evidence using provenance.
Provenance is a Stem-generated record, not an LLM-generated claim.

---

## Invariants and non-negotiables

- The Rhizome remains the only persistent code/AST/symbol index. Hyphae do not
  write to any index, any database, or any file in the Substrate.
- Hyphae do not call the Mycorrhizal Network at any point in their lifecycle.
  Context selection is fully deterministic and local.
- Evidence not admitted by an explicit selection rule is excluded. Hyphae do not
  guess, interpolate, or semantically rank.
- Every assembled Plasmid carries a provenance block. A Plasmid without
  provenance must not be injected.
- Aggregate Hyphae token budget is enforced before injection. Injection of
  evidence that would breach the aggregate budget must fail closed (truncate,
  then log), not silently overflow.
- Hyphae state does not cross a Phytomer boundary. Expire is not optional.
- All budget defaults and freshness windows are configurable by environment
  variable. No budget is compiled as a hard constant.
- Context-assembly policy belongs in the Stem's `core` registry. No
  context-selection logic may live in a REST handler, MCP adapter, or CLI
  subcommand.
- The Stem, not the Mycorrhizal Network, governs what evidence is selected,
  assembled, and injected. The Mycorrhiza reasons over the assembled context; it
  does not determine or modify it.

---

## What must FAIL after this change (negative requirements)

- A Sprout must not receive a Hyphae Plasmid whose aggregate token estimate
  exceeds the configured Hyphae token budget. If the budget would be exceeded,
  the Stem must truncate to budget, log the truncation in the manifest, and
  proceed — not inject beyond budget and not abort the growth.
- A Hyphae traversal must not call any external network endpoint. Any code path
  that attempts an outbound HTTP or TCP connection during Hyphae traversal is a
  bug; the Stem must fail closed if such a connection is detected or attempted.
- A stale symbol (index hash ≠ working-tree hash) must not be admitted as
  evidence without logging the staleness exclusion. Silent admission of stale
  evidence is a correctness failure.
- Hyphae must not write to the Rhizome index, the Taproot, the Substrate tree,
  or any file inside the Terrarium. Any write path from Hyphae to a persistent
  store is forbidden.
- Hyphae must not load an entire file into context if only a subset of its
  symbols is admitted. Whole-file loading that bypasses the symbol-count budget
  is a budget-enforcement failure.
- Hyphae must not admit evidence from a source that is outside the configured
  Substrate's root. Cross-Substrate evidence injection without explicit operator
  opt-in is forbidden.
- A Mycorrhizal Network call inside any Hyphae lifecycle phase (discover,
  traverse/select, assemble, inject, observe, expire) is always forbidden.
- Context assembly policy must not live in a REST/MCP/CLI adapter. If a
  test can reach Hyphae selection logic through a REST handler without going
  through `core`, that is a boundary violation.

---

## Non-goals

This RFC explicitly does not authorize or address:

- **Replacing the Rhizome.** The Rhizome is the persistent code/AST/symbol
  index. Hyphae depend on and query the Rhizome; they do not supersede it,
  modify its schema, or provide an alternative parsing path.
- **Redesigning the Taproot.** Prior Fruit is admitted from the Taproot as an
  evidence source, but this RFC does not change the Taproot's schema, lifecycle,
  or storage. Any Taproot evolution is governed by its own RFC.
- **Fruit lifecycle work.** How PRs are opened, reviewed, merged, or tracked as
  Fruit is outside the scope of Hyphae. Hyphae may *read* prior Fruit as
  evidence; it does not change how Fruit is produced.
- **Whole-repository context loading.** Loading the entire Substrate into a
  Sprout's context window is explicitly rejected. The Repo Map's flat, unguided
  form is what Hyphae replace; a whole-Substrate load would be a regression.
- **Semantic embedding or vector ranking.** Dense embedding of symbols, semantic
  similarity ranking, or vector database queries are not part of this design.
  Selection is rule-based and deterministic.
- **Cross-Phytomer context accumulation.** Hyphae do not maintain state across
  Phytomers. Cross-session memory is the Taproot's domain.
- **Dynamic context requests from the Mycorrhiza.** The Mycorrhiza cannot
  request additional context from Hyphae after injection. If the Sprout needs
  more evidence, it uses its existing tool calls (e.g. `readFile`) within its
  Terrarium. Hyphae inject once, at growth time.
- **Implementation code.** This RFC does not authorize, propose, or contain Go
  implementation. It governs design intent only.

---

## Lane impact

- [x] L1 — CLI & Gateway (Go / MCP): governed capability must be projected onto
  all three surfaces per `AGENTS.md` §6.
- [x] L2 — Sprout Orchestration & Tools (Python): Conductor drives Hyphae before
  each growth; injection path is in Conductor's existing `docker.go` / plasmid
  staging.
- [ ] L3 — Terrarium & Execution (Docker / gVisor / Firecracker): no change to
  Terrarium isolation boundary; Hyphae operate host-side before the Terrarium
  starts.
- [x] L4 — System Storage & Config (SQLite / Memory / env): budget and freshness
  parameters are env-var governed; Hyphae read (never write) the Rhizome SQLite
  index.
- [x] Cross-lane (requires Control Plane approval before build starts): this RFC
  touches Rhizome read path, Conductor injection path, and Core registry.

---

## Source of truth references

- `SYNTHETIC-TAXONOMY.md` §5 — capability classification decision heuristic
- `SYNTHETIC-TAXONOMY.md` §3 — spatial anatomy (Xylem, Phloem, Rhizome)
- `GLOSSARY.md` — canonical definitions for Plasmid, Phytomer, Rhizome, Taproot,
  Mycorrhizal Network, Sprout, Terrarium, Fruit
- `AGENTS.md` §1 — taxonomy enforcement and placement rules
- `AGENTS.md` §6 — interface parity; governed capability/business logic in Core
- `docs/DESIGN-RHIZOME.md` — Rhizome responsibilities, public interface,
  limitations (the persistent index Hyphae read from)
- `docs/DESIGN-CONDUCTOR.md` — `rhizomefacade.go` injection path, genomics,
  plasmid staging, sprout lifecycle (the organ Hyphae augment)
- `docs/DESIGN-MYCORRHIZA-TOKEN-GOVERNANCE.md` — output-token ceiling policy
  (governs the Mycorrhizal budget Hyphae context must fit within)
- `docs/DESIGN-CORE.md` — Core registry, capability declaration, interface parity
- `GUARDRAILS.md` — documentation governance, no issue/PR refs in files,
  filesystem naming policy

---

## Acceptance criteria

The following must be true before this RFC is considered implemented:

1. A Hypha lifecycle (discover → expire) executes for each Sprout growth event
   and replaces the current flat Repo Map injection in
   `cmd/stem/internal/conductor/docker.go`.
2. All six evidence classes (Rhizome symbols, Git state, canonical docs, tests,
   prior Fruit, execution evidence) are traversable. Each class can be
   independently disabled by setting its budget to zero.
3. Per-Phytomer budgets (aggregate token, file count, symbol count, evidence
   item count, doc-byte, Git diff line) are enforced before injection. An
   assembled context that exceeds the aggregate token budget is truncated at a
   natural boundary; the truncation is logged.
4. All budget parameters and freshness windows are configurable by environment
   variable. No value is a compiled constant.
5. Every injected Hyphae Plasmid carries a provenance block. The Stem logs the
   full Plasmid manifest (evidence class, source path, byte size, rule,
   freshness ref) before injection.
6. Stale symbols (index hash ≠ working-tree content hash) are excluded and the
   exclusion is logged. No stale symbol is silently admitted.
7. The Hyphae package contains no outbound network calls. A test that mocks
   `net.Dial` must confirm zero dials during any Hyphae lifecycle phase.
8. Hyphae make no writes to the Rhizome index, Taproot, Substrate tree, or
   Terrarium filesystem. The package's write surface is restricted to the
   structured execution log.
9. Context-assembly policy is declared as a governed capability in the `core`
   registry and projected onto all three surfaces (CLI, MCP, REST) per
   `AGENTS.md` §6. The parity test `TestInterfaceParityCoverage` must pass.
10. `boundary_test.go` in `internal/core` continues to pass: no Core import of
    `conductor`, `hyphae`, or any transport type.
11. The observation record (which injected evidence was acted on) is written to
    the structured execution log and is readable by the Botanist without
    additional tooling.
12. A Phytomer expiration clears all in-memory Hyphae state. No Hyphae object
    survives across Phytomer boundaries.
13. `make check-all` passes with the Hyphae package present.
14. The Mycorrhizal Network is never called during any Hyphae lifecycle phase.
    A unit test must assert this by confirming the LLM client is never invoked
    from any function reachable from the Hyphae package.

---

## Gate A approval

<!-- HUMAN: If this design is approved, comment on this issue: approved -->
