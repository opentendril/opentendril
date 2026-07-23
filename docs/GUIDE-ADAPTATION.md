# Adaptation Guide

OpenTendril’s adaptation loop is intentionally biological: the isolated worker is the **Sprout**, the repository rules are the **genome**, and the learned, durable lessons that survive one run become **epigenetic** context for the next run.

This guide explains how the Lamarckian-style loop works in practice and how to manage it from the `tendril` CLI.

## What “Lamarckian” Means Here

Traditional automation is purely Darwinian: a task runs, the result disappears, and the next run starts from scratch.

OpenTendril adds a Lamarckian layer:

1. A transcript runs in an isolated sprout.
2. The host orchestrator collects the resulting diff and execution logs.
3. The epigenetic chronicler distills durable learnings.
4. Those learnings are written into `.tendril/genome/epigenetics.md`.
5. Future sprouts load the updated genome as part of their system context.

The result is a repository that can remember reusable rules, constraints, and lessons learned without duplicating them in every prompt.

## Genome Layout

The active genome lives in `.tendril/genome/`.

At startup, Tendril concatenates the Markdown files in that directory in alphabetical order and injects the result into the sprout context.

Recommended files:

- `README.md` for human orientation
- `naming-conventions.md` for repository-wide style rules
- `epigenetics.md` for learned, durable rules extracted from successful runs
- Plasmid files copied in from `.tendril/genotypes/plasmids/`

## CLI Reference

### `tendril genome view`

Prints the active genome exactly as Tendril will load it, with clear separators between files.

Use it when you want to inspect the effective context before a run.

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

Reviews the last `N` git commits in the current repository and asks the epigenetic chronicler to extract durable learnings from each one.

For each commit, Tendril reads:

- the commit message
- the full `git show --no-color <hash>` output

Those are synthesized into new genome bullets and appended to `epigenetics.md`.

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

## Automatic Genome Updates

The host orchestrator can write learned rules automatically after successful transcript execution.

### `TENDRIL_GENOME_AUTO_PUSH`

When set to `true`, Tendril will:

1. stage `.tendril/genome/epigenetics.md`
2. create a commit with:

```text
chore(genome): epigenetic transcription update [skip ci]
```

3. push the result to `origin HEAD`

This is useful when you want the genome to persist across fresh checkouts and remote clones.

Example:

```bash
export TENDRIL_GENOME_AUTO_PUSH=true
```

### `TENDRIL_GENOME_MAX_TOKENS`

Controls when the genome reducer should run automatically.

The orchestrator uses a simple approximation of `1 token = 4 characters`.

- Default: `2000`
- Effective character limit: about `8000` characters

If `.tendril/genome/epigenetics.md` grows beyond that size after transcription, Tendril automatically runs the reduction pass.

Example:

```bash
export TENDRIL_GENOME_MAX_TOKENS=3000
```

## Suggested Workflow

1. Run a task that teaches Tendril something new.
2. Let the chronicler append the learning to `epigenetics.md`.
3. Inspect the genome with `tendril genome view`.
4. If the file gets noisy, run `tendril genome reduce`.
5. If you maintain reusable context blocks, inject them with `tendril plasmid inject <name>`.

For a stronger persistence loop, enable `TENDRIL_GENOME_AUTO_PUSH=true` so successful transcriptions are committed and pushed automatically.

## Practical Notes

- Keep epigenetic bullets short and general.
- Prefer durable principles over task-specific breadcrumbs.
- Treat plasmids as reusable modules: syntax notes, domain rules, architecture constraints, and workflow recipes.
- Use `tendril adapt --commits N` when you want Tendril to learn from prior repository history rather than only from the latest run.

