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
# The fix routes each operation through its approved delegated-workspace
# resolver. The normal resolver returns a per-Pollinator worktree for a
# delegated call and the operator's own checkout for a direct one; git.apply
# uses its existing-only mode. That routing is easy to bypass by accident: a new operation
# that resolves the substrate's path itself looks perfectly reasonable in
# review and reintroduces the corruption. So it is checked rather than
# remembered — the same reasoning as the other guards in this directory.
#
# The rule: in the delegated git adapter, every conductor execution's Workspace
# field must be the resolved workspace path. git.apply uses a stricter
# existing-workspace-only resolver, so this guard verifies that path separately
# instead of treating it as an exception.
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

# Check each operation closure independently: exactly one conductor execution,
# exactly one approved resolver call, and a workspace.Path field. Status also
# returns the workspace path as safe metadata, so its closure has two such
# fields; one belongs to the execution and both must still use the resolved
# path (the global offender check above proves the latter).
operations=0
for operation in Apply Commit Push PullRequest Status BranchList Prune Branch; do
  operation_block="$(awk -v operation="${operation}" '
    BEGIN { start = "^[[:space:]]*" operation ": func\\(" }
    $0 ~ start { in_operation = 1 }
    in_operation && $0 ~ "^[[:space:]]*[A-Z][A-Za-z]+: func\\(" && $0 !~ start { exit }
    in_operation { print }
  ' "${adapter}")"
  if [ -z "${operation_block}" ]; then
    echo "::error::Delegated Git operation ${operation} is missing from ${adapter}."
    exit 1
  fi
  operation_executions="$(grep -cE 'conductor\.RunGit[A-Za-z]+\(' <<<"${operation_block}" || true)"
  resolver_calls="$(grep -cE '^[[:space:]]*workspace, substrateSpec, err := resolve(GitWorkspace|ExistingGitApplyWorkspace)\(ctx,' <<<"${operation_block}" || true)"
  workspace_fields="$(grep -cE '^[[:space:]]*Workspace:' <<<"${operation_block}" || true)"
  expected_workspace_fields=1
  if [ "${operation}" = "Status" ]; then
    expected_workspace_fields=2
  fi
  if [ "${operation_executions}" -ne 1 ] || [ "${resolver_calls}" -ne 1 ] || [ "${workspace_fields}" -ne "${expected_workspace_fields}" ]; then
    echo "::error::Delegated Git operation ${operation} must have one execution, one workspace resolution, and the expected resolved workspace field(s)."
    exit 1
  fi

  expected_resolver='resolveGitWorkspace(ctx,'
  if [ "${operation}" = "Apply" ]; then
    expected_resolver='resolveExistingGitApplyWorkspace(ctx, spec.Substrate, substratesConfig)'
  fi
  if ! grep -Fq "${expected_resolver}" <<<"${operation_block}"; then
    echo "::error::Delegated Git operation ${operation} does not use its approved workspace resolver."
    exit 1
  fi
  operations=$((operations + 1))
done

execution_count="$(grep -cE 'conductor\.RunGit[A-Za-z]+\(' "${adapter}" || true)"
if [ "${execution_count}" -ne "${operations}" ]; then
  echo "::error::Found ${execution_count} conductor Git execution(s) but checked ${operations} delegated operation closure(s)."
  exit 1
fi

apply_resolver="$(awk '
  /^func resolveExistingGitApplyWorkspace\(/ { in_resolver = 1 }
  /^func resolveGitWorkspace\(/ { if (in_resolver) exit }
  in_resolver { print }
' "${adapter}")"
if ! grep -Fq 'conductor.ResolveDelegatedWorkspaceWithMode(' <<<"${apply_resolver}" \
    || ! grep -Fq 'conductor.ExistingDelegatedWorkspaceOnly,' <<<"${apply_resolver}"; then
  echo "::error::git.apply must use ResolveDelegatedWorkspaceWithMode(..., ExistingDelegatedWorkspaceOnly)."
  exit 1
fi

echo "✅ All ${execution_count} delegated Git execution(s) use an approved per-Pollinator workspace resolver."
echo "✅ git.apply is verified to use ExistingDelegatedWorkspaceOnly."
