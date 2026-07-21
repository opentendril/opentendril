#!/usr/bin/env bash
# Fails if a branch deletion can be reached without authoritative merge evidence.
#
# Why this exists: deleting a branch is the only thing the delegated git ladder
# does that can destroy work. Everything else adds, and a mistake is
# recoverable; a branch whose commits exist nowhere else is gone.
#
# The dangerous part is that the OBVIOUS ways to decide "is this branch merged"
# are all wrong. `git branch --merged` misses squash merges entirely — a branch
# merged through a squashing pull request still reports as unmerged, because its
# commits never enter the target's history. `git diff base...branch` fails the
# same way. Both look authoritative in review. A future change that "simplifies"
# the classification to either of them would delete unmerged work while passing
# every local test that does not involve a real forge.
#
# So the rule is structural: deletion of a USER's branches happens in exactly one
# place, and that place is gated on forge evidence. Every other deletion site
# must be named here, so an exception stays visible and reviewable rather than
# blending into the noise — the same treatment the default-branch protection
# floor gets in its own guard.
#
# Usage: scripts/check-branch-deletion-guard.sh
set -euo pipefail

pruner="cmd/stem/internal/conductor/gitbranchlifecycle.go"
gate='if !branch.Deletable || branch.Classification != BranchMerged {'

if [ ! -f "${pruner}" ]; then
  echo "::error::${pruner} not found — this guard is out of date with the tree layout."
  exit 1
fi

# The one reviewed exception: selection.go deletes losing PHENOTYPE branches —
# refs Tendril created itself moments earlier during evolutionary selection, from
# a list it produced. They are never a user's work, so forge verification would
# be meaningless there. Any site beyond these two is a second chance to delete
# someone's work and fails the build.
allowed_other="cmd/stem/internal/conductor/selection.go"

sites="$(grep -rln '"branch", "-D"\|"branch", "-d"' cmd/ --include='*.go' \
    | grep -v '_test\.go$' | sort || true)"
unexpected="$(printf '%s\n' "${sites}" | grep -v "^${pruner}$" | grep -v "^${allowed_other}$" || true)"

if [ -n "${unexpected}" ]; then
  echo "::error::Branch deletion appears somewhere new."
  echo "Deletion of a user's branches belongs in ${pruner}, gated on forge merge evidence."
  echo "Unexpected deletion site(s):"
  echo "${unexpected}"
  exit 1
fi

if ! printf '%s\n' "${sites}" | grep -qx "${pruner}"; then
  echo "::error::${pruner} no longer deletes anything — has the gated pruner been removed or renamed?"
  exit 1
fi

# 2. That one site must be gated on the merged classification.
if ! grep -qF "${gate}" "${pruner}"; then
  echo "::error::The deletion gate is missing from ${pruner}."
  echo "Deletion must be guarded by:"
  echo "  ${gate}"
  echo "Merge state must come from forge evidence — 'git branch --merged' and diff"
  echo "comparisons both report squash-merged branches as unmerged and will delete work."
  exit 1
fi

# 3. The local-only shortcuts must never appear in the lifecycle path.
if grep -nE '"--merged"' "${pruner}"; then
  echo "::error::${pruner} uses git's own --merged check."
  echo "That misses squash merges entirely and will classify merged work as unmerged"
  echo "(and, if the sense is ever inverted, unmerged work as deletable)."
  exit 1
fi

echo "✅ Branch deletion happens in one guarded place, gated on forge merge evidence."
