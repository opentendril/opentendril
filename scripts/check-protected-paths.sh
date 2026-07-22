#!/usr/bin/env bash
# Fails when .github/CODEOWNERS and .github/protected-paths drift apart.
#
# .github/protected-paths is the single definition of the kernel a Sprout must
# not rewrite. Two things enforce it: the Stem refuses to merge a Terrarium
# commit touching a listed path, and GitHub requests code-owner review before
# such a change lands. The second only works if every listed path has an owner,
# and nothing stops somebody adding to one file and forgetting the other.
#
# So this check proves the projection is complete. It does NOT check reviews —
# GitHub does that, better, and re-implementing it here would be a second
# enforcement path to keep correct.
#
# Correspondence rule, deliberately literal so it needs no glob emulation:
# a protected entry "X" must appear in CODEOWNERS as "/X" with at least one
# owner. A directory entry "X/" must appear as "/X/".
#
# Usage: scripts/check-protected-paths.sh
set -euo pipefail

list=".github/protected-paths"
owners=".github/CODEOWNERS"

# Fail closed. A missing definition in THIS repository is a broken control, not
# an absent one — unlike a Substrate belonging to somebody else, where an absent
# list correctly means nothing is declared protected.
if [ ! -r "${list}" ]; then
  echo "::error::${list} is missing or unreadable. The kernel protection has no definition."
  exit 1
fi
if [ ! -r "${owners}" ]; then
  echo "::error::${owners} is missing or unreadable, so no protected path has a reviewer."
  exit 1
fi

missing=""
count=0

while IFS= read -r raw; do
  entry="$(printf '%s' "${raw}" | sed -e 's/[[:space:]]*$//' -e 's/^[[:space:]]*//')"
  [ -z "${entry}" ] && continue
  case "${entry}" in \#*) continue ;; esac

  count=$((count + 1))

  # The CODEOWNERS line must start with the anchored pattern and carry an owner.
  if ! grep -Eq "^/${entry//./\\.}([[:space:]]+@[^[:space:]]+)+[[:space:]]*$" "${owners}"; then
    missing="${missing}  ${entry}"$'\n'
  fi
done < "${list}"

if [ "${count}" -eq 0 ]; then
  echo "::error::${list} declares no protected paths. An empty list is not a control."
  exit 1
fi

if [ -n "${missing}" ]; then
  echo "::error::Protected paths with no code owner — ${owners} has drifted from ${list}."
  printf '%s' "${missing}"
  echo "Add each as: /<path>    @owner"
  exit 1
fi

echo "✅ All ${count} protected path(s) have a code owner."
