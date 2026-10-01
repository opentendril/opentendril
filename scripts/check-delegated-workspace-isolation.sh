#!/usr/bin/env bash
# Verifies delegated Git workspace routing and the repository-scoped git.fetch
# exception.
#
# Why this exists: before workspace isolation, every delegated operation ran in
# one shared directory per substrate. Two Pollinators granted the same substrate
# corrupted each other silently. The delegated commit stages the whole tree, so
# one Pollinator's uncommitted files were committed by the other, onto the other's
# branch, under the other's identity. That destroyed the attribution the
# delegated commit exists to provide, produced no error, and was reachable with
# the documented setup.
#
# Ordinary delegated Git operations route through their approved
# delegated-workspace resolver. The normal resolver returns a per-Pollinator
# worktree for a delegated call and the operator's own checkout for a direct
# one; git.apply uses its existing-only mode. git.fetch is intentionally
# repository-scoped: it requires a named configured Substrate and resolves its
# existing configured checkout without creating or rotating a Pollen workspace.
# git.pr.update is also repository-scoped: it addresses one exact PR through
# the named Substrate URL and credential and has no local workspace effects.
# These boundaries are easy to bypass accidentally, so this guard checks them
# rather than relying on review memory.
#
# The rule: every per-Pollen conductor execution uses the resolved workspace
# path; git.apply must use ExistingDelegatedWorkspaceOnly; git.fetch must pass
# the existing configured Substrate checkout as Repository without a delegated
# resolver; and git.pr.update must use only its configured Substrate URL and
# credential.
#
# Usage: scripts/check-delegated-workspace-isolation.sh
set -euo pipefail

adapter="cmd/stem/cmdgit.go"

if [ ! -f "${adapter}" ]; then
  echo "::error::${adapter} not found. This guard is out of date with the tree layout."
  exit 1
fi

# Every Workspace field in the adapter must use the resolved per-Pollen path.
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

# Check each ordinary per-Pollen operation closure independently: exactly one
# conductor execution, exactly one approved resolver call, and a workspace.Path
# field. Status also returns the workspace path as safe metadata, so its
# closure has two such fields; one belongs to the execution and both must still
# use the resolved path (the global offender check above proves the latter).
# Fetch is checked separately below because its approved boundary is the
# configured repository, not a Pollen workspace.
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

fetch_block="$(awk '
  /^[[:space:]]*Fetch: func\(/ { in_operation = 1 }
  in_operation && /^[[:space:]]*[A-Z][A-Za-z]+: func\(/ && $0 !~ /^[[:space:]]*Fetch: func\(/ { exit }
  in_operation { print }
' "${adapter}")"
if [ -z "${fetch_block}" ]; then
  echo "::error::Delegated Git operation Fetch is missing from ${adapter}."
  exit 1
fi
fetch_executions="$(grep -cE 'conductor\.RunGitFetch\(' <<<"${fetch_block}" || true)"
fetch_git_executions="$(grep -cE 'conductor\.RunGit[A-Za-z]+\(' <<<"${fetch_block}" || true)"
if [ "${fetch_executions}" -ne 1 ] || [ "${fetch_git_executions}" -ne 1 ]; then
  echo "::error::git.fetch must contain exactly one conductor.RunGitFetch(...) execution."
  exit 1
fi

if grep -Eq 'resolveGitWorkspace\(|resolveExistingGitApplyWorkspace\(|ResolveDelegatedWorkspace|CreateDelegatedWorkspace|RotateDelegatedWorkspace|rotateDelegatedWorkspace|Pollen|^[[:space:]]*Workspace:' <<<"${fetch_block}"; then
  echo "::error::git.fetch must not resolve, create, or rotate a Pollen delegated workspace."
  exit 1
fi

for required in \
  'name := strings.TrimSpace(spec.Substrate)' \
  'if name == ""' \
  'substrateSpec, configured := conductor.ResolveSubstrate(name, substratesConfig)' \
  'if !configured || substrateSpec == nil' \
  'repository, err := conductor.ResolveSubstrateWorkspace(name, substrateSpec)' \
  'Repository: repository,'; do
  if ! grep -Fq "${required}" <<<"${fetch_block}"; then
    echo "::error::git.fetch is missing the repository-scoped authority check: ${required}"
    exit 1
  fi
done

operations=$((operations + 1))

update_block="$(awk '
  /^[[:space:]]*UpdatePullRequest: func\(/ { in_operation = 1 }
  in_operation && /^[[:space:]]*[A-Z][A-Za-z]+: func\(/ && $0 !~ /^[[:space:]]*UpdatePullRequest: func\(/ { exit }
  in_operation { print }
' "${adapter}")"
if [ -z "${update_block}" ]; then
  echo "::error::Delegated Git operation UpdatePullRequest is missing from ${adapter}."
  exit 1
fi
update_executions="$(grep -cE 'conductor\.RunGitPRUpdate\(' <<<"${update_block}" || true)"
if [ "${update_executions}" -ne 1 ]; then
  echo "::error::git.pr.update must contain exactly one conductor.RunGitPRUpdate(...) execution."
  exit 1
fi
for required in \
  'conductor.ResolveSubstrate(spec.Substrate, substratesConfig)' \
  'conductor.ResolveSubstrateCredential(*substrateSpec, substratesConfig)'; do
  if ! grep -Fq "${required}" <<<"${update_block}"; then
    echo "::error::git.pr.update is missing configured Substrate authority: ${required}"
    exit 1
  fi
done
if ! grep -Eq 'RepositoryURL:[[:space:]]+substrateSpec\.URL,' <<<"${update_block}" \
    || ! grep -Eq 'Credential:[[:space:]]+credential,' <<<"${update_block}"; then
  echo "::error::git.pr.update must pass the configured Substrate URL and resolved credential to the conductor."
  exit 1
fi
if grep -Eq 'resolveGitWorkspace\(|ResolveSubstrateWorkspace\(|ResolveDelegatedWorkspace|CreateDelegatedWorkspace|RotateDelegatedWorkspace|workspace\.Path|^[[:space:]]*Workspace:' <<<"${update_block}"; then
  echo "::error::git.pr.update must not use a workspace or caller-selected repository path."
  exit 1
fi
operations=$((operations + 1))

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

echo "✅ All ${operations} delegated Git execution closures are checked: per-Pollinator workspaces for ordinary operations and configured Substrate scope for git.fetch and git.pr.update."
echo "✅ git.apply is verified to use ExistingDelegatedWorkspaceOnly."
echo "✅ git.fetch is verified to use the existing configured Substrate checkout without a Pollen workspace."
echo "✅ git.pr.update is verified to use only its configured Substrate URL and credential."
