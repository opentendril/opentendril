# Fruit Lifecycle

## Problem

A Sprout can currently leave a remote Git ref even when the run fails, produces no changes, or never meaningfully engages the task. This makes Git review noisy and causes old execution bodies to accumulate as apparent review candidates.

OpenTendril needs Git-reviewable Fruit, not permanent debris from every Sprout attempt.

## Decision

A remote Fruit ref is durable only when all of these are true:

1. the Sprout outcome is matured;
2. the diff is non-empty;
3. the diff is inside the declared Substrate boundary;
4. postflight guardrails pass;
5. the result is intended for Botanist review.

Runs that fail, produce no changes, time out before producing a diff, or return no-engagement are recorded in history but do not remain as remote Fruit refs.

## Lifecycle

### Started

The Stem creates a bounded Phytomer context and starts the Sprout inside a Terrarium.

No durable remote Fruit ref is required at this stage.

### Running

The Sprout may create local changes inside the Terrarium worktree.

Observation events and history records are written as the run progresses.

### Postflight

After execution, the Stem classifies the result:

- `matured` with non-empty diff: candidate Fruit;
- `matured` with empty diff: recorded, not reviewable Fruit;
- `withered`, `no-engagement`, provider error, or timeout before diff: recorded, not reviewable Fruit.

### Published Fruit

Candidate Fruit is pushed only after postflight confirms the diff is bounded and reviewable.

The Botanist reviews this Git output and decides whether to merge, reject, or supersede it.

### Reclaimed Fruit

Non-reviewable refs are deleted or never pushed.

Reviewable refs may be reclaimed after Botanist rejection, supersession, or explicit expiry.

## Sequence aggregation

A Sequence should produce one reviewable Fruit ref for the bounded Phytomer where practical, not one remote ref per Sprout step.

Individual Sprout outcomes remain observable in history. Git review receives the coherent final Fruit.

## Default retention policy

- Failed/no-change/no-engagement runs: no durable remote Fruit ref.
- Matured non-empty runs: keep reviewable Fruit.
- Superseded Fruit: eligible for reclamation.
- Rejected Fruit: eligible for reclamation.
- Stale reviewable Fruit: visible in Greenhouse and eligible for Botanist cleanup.

Suggested defaults:

- warn at 10 open reviewable Fruit refs;
- require explicit Botanist override at 20;
- never silently delete reviewable non-empty Fruit.

## Acceptance criteria

1. A provider billing failure leaves no remote `sprout/task-step-*` ref.
2. A no-change report run leaves no remote `sprout/task-step-*` ref.
3. A matured one-file edit leaves exactly one reviewable Fruit ref.
4. A matured diff outside the declared boundary is rejected and not published.
5. A Sequence with multiple Sprouts can publish one coherent Fruit ref.
6. History still records every Sprout outcome whether or not remote Fruit is published.
7. Greenhouse can distinguish reviewable Fruit from discarded, rejected, and reclaimed outcomes.

## Non-goals

This design does not change Git merge policy.

The Botanist still performs human review and merge. The Stem does not push directly to `main`, auto-merge, or delete non-empty reviewable Fruit without an explicit lifecycle decision.
