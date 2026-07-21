#!/usr/bin/env bash
# Fails if a delegated git operation is wired to run in a raw substrate path
# instead of the resolved, per-Pollinator workspace.
#
# Why this exists: before workspace isolation, every delegated operation ran in
# one shared directory per substrate. Two Pollinators granted the same substrate
# corrupted each other silently — the delegated commit stages the whole tree, so
# one Pollinator's uncommitted files were committed by the other, onto the other's
# branch, under the other's identity. That destroyed the attribution the
# delegated commit exists to provide, produced no error, and was reachable with
# the documented setup.
#
# The fix routes every operation through resolveGitWorkspace, which returns a
# per-Pollinator worktree for a delegated call and the operator's own checkout for
# a direct one. That routing is easy to bypass by accident: a new operation
# that resolves the substrate's path itself looks perfectly reasonable in
# review and reintroduces the corruption. So it is checked rather than
# remembered — the same reasoning as the other guards in this directory.
#
# The rule: in the delegated git adapter, every conductor execution's Workspace
# field must be the resolved workspace path.
#
# Usage: scripts/check-delegated-workspace-isolation.sh
set -euo pipefail

adapter="cmd/stem/cmdgit.go"

if [ ! -f "${adapter}" ]; then
  echo "::error::${adapter} not found — this guard is out of date with the tree layout."
  exit 1
fi

# Every "Workspace:" assignment in the adapter must use the resolved workspace.
offenders="$(grep -nE '^[[:space:]]*Workspace:' "${adapter}" \
    | grep -vE 'Workspace:[[:space:]]+workspace\.Path,' || true)"

if [ -n "${offenders}" ]; then
  echo "::error::A delegated git operation is not using the resolved per-Pollinator workspace."
  echo "Route it through resolveGitWorkspace(...) and pass workspace.Path, so two Pollinators on"
  echo "one substrate cannot share a working tree and commit each other's changes."
  echo "Offending lines in ${adapter}:"
  echo "${offenders}"
  exit 1
fi

# The resolver itself must still be the thing every operation calls.
calls="$(grep -c 'resolveGitWorkspace(ctx' "${adapter}" || true)"
executions="$(grep -cE '^[[:space:]]*Workspace:[[:space:]]+workspace\.Path,' "${adapter}" || true)"
if [ "${calls}" -lt "${executions}" ]; then
  echo "::error::Found ${executions} delegated execution(s) but only ${calls} workspace resolution(s)."
  echo "Each delegated git operation must resolve its own workspace."
  exit 1
fi

echo "✅ All ${executions} delegated git operation(s) run in a resolved per-Pollinator workspace."
