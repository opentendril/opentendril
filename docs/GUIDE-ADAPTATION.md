# Adaptation Guide

OpenTendril’s adaptation loop is intentionally biological: the isolated worker is the **Sprout**, the repository rules are the **genome**, and retained Mycorrhizal learnings are stored as proposed **Rhizome** knowledge. Proposals are available for Botanist review, but they are not automatically injected as Sprout context.

This guide explains how the Lamarckian-style loop works in practice and how to manage it from the `tendril` CLI.

## What “Lamarckian” Means Here

Traditional automation is purely Darwinian: a task runs, the result disappears, and the next run starts from scratch.

OpenTendril adds a Lamarckian layer:

1. A transcript runs in an isolated sprout.
2. The host orchestrator collects the resulting diff and execution logs.
3. The bounded Mycorrhizal Chronicler distills concise proposal statements.
4. Each retained statement is stored in the canonical source Substrate's local Rhizome with no authority.
5. The Botanist inspects and confirms or rejects each proposal through the existing memory lifecycle.

The result is a repository that can accumulate reusable rules, constraints, and lessons without duplicating them in every prompt. Proposal persistence does not confer authority or automatic Sprout context eligibility.

## Genome Layout

The active genome lives in `.tendril/genome/`.

At startup, Tendril loads genome material according to the following rules:

- Ordinary curated Markdown files (e.g. `README.md`, `naming-conventions.md`, injected plasmids) may be included under the bounded genome token budget.
- The Repo Map (`repomap.md`) is available as an on-disk pointer and is staged by the Conductor before a Sprout grows.
- `memorymap.md` and `epigenetics.md` are **quarantined**: they are neither inlined into Sprout context nor advertised to Sprouts at startup.

Recommended curated files:

- `README.md` for human orientation
- `naming-conventions.md` for repository-wide style rules
- Plasmid files copied in from `.tendril/genotypes/plasmids/`

## CLI Reference

### `tendril genome view`

Lists the Markdown genome files stored in `.tendril/genome/`, with clear separators between files.

This is **not** an exact preview of effective Sprout context: quarantined files (`epigenetics.md`, `memorymap.md`) appear on disk and are shown by this command but receive different execution-time treatment and are excluded from automatic Sprout context injection.

Example:

```bash
tendril genome view
```

### `tendril genome reduce`

Runs the genome reducer against `.tendril/genome/epigenetics.md`.

The reducer:

- compresses verbose notes
- deduplicates repeated guidance
- merges overlapping bullets
- rewrites the file into a concise list of high-level principles

Use it when the epigenetics file starts to grow too large or too specific.

Example:

```bash
tendril genome reduce
```

### `tendril adapt --commits N`

Reviews the last `N` git commits in the current repository and asks the bounded Mycorrhizal Chronicler to extract durable proposal statements from each one.

For each commit, Tendril reads:

- the commit message
- the full `git show --no-color <hash>` output

Those are synthesized into individual source-local Rhizome proposals. The command reports proposal creation and does not mutate the legacy genome file.

Example:

```bash
tendril adapt --commits 1
```

### `tendril plasmid list`

Lists available plasmids as Markdown files.

Tendril first looks in:

```text
.tendril/genotypes/plasmids/
```

If that directory is empty, it falls back to the broader:

```text
.tendril/genotypes/
```

Example:

```bash
tendril plasmid list
```

### `tendril plasmid inject <name>`

Copies a plasmid Markdown file into `.tendril/genome/` so it is automatically loaded in future sprout runs.

The command searches by filename, basename, or relative path inside `.tendril/genotypes/` and `.tendril/genotypes/plasmids/`.

Example:

```bash
tendril plasmid inject radicle
```

After injection, the plasmid becomes part of the active genome seed set.

## Proposal persistence

Post-run extraction and `tendril adapt --commits N` create Rhizome proposals in the canonical source Substrate. A managed candidate workspace is only the source of the completed result and diff; candidate cleanup does not remove the proposal.

The proposal envelope is:

```text
origin = mycorrhizal
authority = none
status = proposed
kind = observation
```

Inspect proposals with the existing memory commands. Use the existing Botanist confirm or reject lifecycle. A confirmed proposal retains its Mycorrhizal origin, receives Botanist authority, and becomes eligible through normal task-context assembly. A rejected proposal remains ineligible. No proposal-specific CLI or automatic promotion path exists.

`TENDRIL_GENOME_AUTO_PUSH` is not an automatic learning path. Setting it does not write proposals to `epigenetics.md`, stage files, create commits, push branches, or establish knowledge.

### `TENDRIL_GENOME_MAX_TOKENS`

This setting is retained for explicit legacy genome tooling. Proposal extraction does not use it and does not reduce or rewrite `epigenetics.md`.

The orchestrator uses a simple approximation of `1 token = 4 characters`.

- Default: `2000`
- Effective character limit: about `8000` characters

The explicit `tendril genome reduce` operation remains available for intentional Botanist curation. Reduction does not change the quarantine status of `epigenetics.md`; the file remains excluded from automatic Sprout context.

Example:

```bash
export TENDRIL_GENOME_MAX_TOKENS=3000
```

## Suggested Workflow

1. Run a task that teaches Tendril something new.
2. Inspect the resulting Rhizome proposals with the existing memory commands.
3. Confirm only proposals that the Botanist accepts as repository knowledge, or reject them to keep them ineligible.
4. Use `tendril genome view` for explicit inspection of preserved legacy genome files.
5. Run `tendril genome reduce` or other explicit genome operations only when intentionally curating legacy genome material.
6. If you maintain reusable context blocks, inject them with `tendril plasmid inject <name>`.

## Practical Notes

- Keep proposal statements short and repository-specific.
- Prefer durable principles over task-specific breadcrumbs.
- Treat plasmids as reusable modules: syntax notes, domain rules, architecture constraints, and workflow recipes.
- Use `tendril adapt --commits N` when you want proposal candidates from prior repository history rather than only from the latest run.
- Treat `.tendril/genome/epigenetics.md` as preserved, quarantined legacy material. It is not the automatic proposal store.
