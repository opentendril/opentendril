#!/usr/bin/env bash
# Fails when a source-hygiene job is not wired into the gate that makes it binding.
#
# The problem this removes is a chore that silently degrades. Every guard in the
# hygiene workflow is its own job, and a job only blocks a merge if its name is a
# required status check in the branch-protection ruleset. Adding a guard therefore
# needed a separate, manual ruleset edit by someone with administrative access —
# and when that step was missed the guard still ran, still went red, and still let
# the pull request merge. Six guards were in exactly that state.
#
# The fix is one aggregate job that depends on all of them, with only that one
# name required. This script closes the remaining hole in that arrangement: an
# aggregate is only as complete as its dependency list, so a new guard that nobody
# adds to it is a new guard that does not gate. Here that is a failure rather than
# an oversight, and it fails in the same pull request that introduces the guard.
#
# Invocation:
#   scripts/check-hygiene-gate-complete.sh [workflow-file]

set -euo pipefail

workflow="${1:-.github/workflows/check-source-hygiene.yml}"

# The aggregate's own job id. It cannot depend on itself.
readonly GATE_JOB="hygiene-gate"

if [ ! -f "${workflow}" ]; then
  echo "::error::Workflow not found: ${workflow}"
  exit 1
fi

# Top-level job identifiers: two-space indent, bare key, directly under `jobs:`.
jobs="$(awk '
  /^jobs:/ { in_jobs = 1; next }
  in_jobs && /^[^ ]/ { in_jobs = 0 }
  in_jobs && /^  [a-z][a-z0-9_-]*:[[:space:]]*$/ { gsub(/[ :]/, ""); print }
' "${workflow}")"

# The gate's needs: list — the block of `- item` lines following `needs:` inside
# the gate job, stopping at the next key at the same indent.
needs="$(awk -v gate="  ${GATE_JOB}:" '
  $0 == gate { in_gate = 1; next }
  in_gate && /^  [a-z][a-z0-9_-]*:[[:space:]]*$/ { in_gate = 0 }
  in_gate && /^    needs:[[:space:]]*$/ { in_needs = 1; next }
  in_needs && /^      - / { sub(/^      - /, ""); print; next }
  in_needs && !/^      - / { in_needs = 0 }
' "${workflow}")"

if [ -z "${needs}" ]; then
  echo "::error::The ${GATE_JOB} job declares no dependencies in ${workflow}."
  echo "Every hygiene job must be listed under its needs:, or the gate makes nothing binding."
  exit 1
fi

status=0

for job in ${jobs}; do
  [ "${job}" = "${GATE_JOB}" ] && continue
  if ! printf '%s\n' "${needs}" | grep -qxF "${job}"; then
    status=1
    echo "::error::Job \"${job}\" is not a dependency of ${GATE_JOB}."
    echo "It runs, but nothing stops a pull request merging while it is red."
  fi
done

# A dependency naming a job that no longer exists makes the workflow invalid, so
# it is reported here rather than at the first pull request that trips over it.
for need in ${needs}; do
  if ! printf '%s\n' "${jobs}" | grep -qxF "${need}"; then
    status=1
    echo "::error::${GATE_JOB} depends on \"${need}\", which is not a job in ${workflow}."
  fi
done

if [ ${status} -ne 0 ]; then
  echo ""
  echo "Add the job to the ${GATE_JOB} needs: list. Only that one name is a required"
  echo "status check, so this is what makes a new guard actually block a merge."
  exit 1
fi

echo "✅ Every source-hygiene job is wired into ${GATE_JOB}."
exit 0
