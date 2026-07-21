#!/usr/bin/env bash
# Fails if a change ADDS a hard-coded default-branch assumption to a git
# execution path.
#
# Why this exists: assuming a repository's default branch is called "main" (or
# "master", or whatever branch happens to be checked out) is the single most
# expensive recurring failure in this codebase's git story — it is paid for
# later in rebases, merge repair, and commits reversed off the default branch
# onto a feature branch. The rule was decided, written down, and then silently
# violated anyway in a second place, because nothing checked. Documentation
# records intent and tests record behaviour; only a guard like this one records
# an invariant in a way that survives a contributor who never read the issue.
#
# The rule: a git execution path must RESOLVE the default branch (see
# conductor.ResolveDefaultBranch / ResolveDefaultBranchLocal) or report it as
# unknown. It must never compare against, or fall back to, a literal branch
# name.
#
# Usage: scripts/check-default-branch-assumptions.sh [base-ref]   (default: origin/main)
set -euo pipefail

base="${1:-origin/main}"

# Files that make up the git execution paths this rule governs.
paths=(
  'cmd/stem/internal/conductor/*.go'
  'cmd/stem/internal/core/git*.go'
  'cmd/stem/cmdgit*.go'
  'cmd/stem/internal/receptors/git*.go'
)

# Forbidden: a quoted "main"/"master"/"trunk"/"develop" used as a branch value
# — compared against, assigned, or defaulted to. Matching the quoted literal
# keeps this narrow: prose, identifiers, and paths that merely contain the word
# are not branch assumptions.
pattern='"(main|master|trunk|develop)"'

# The protection floor in defaultbranch.go is the ONE legitimate use: there the
# well-known names are not "the default branch" but the minimum set treated as
# protected when the real default branch cannot be determined. It is excluded
# by name so the single exception stays visible and reviewable, rather than
# blending into the noise.
offenders="$(git diff "${base}...HEAD" -- "${paths[@]}" \
    ':!*_test.go' \
    ':!cmd/stem/internal/conductor/defaultbranch.go' \
    | grep -E '^\+' | grep -Ev '^\+\+\+' \
    | grep -E "${pattern}" || true)"

if [ -n "${offenders}" ]; then
  echo "::error::A hard-coded default-branch name was added to a git execution path."
  echo "Resolve the default branch instead — conductor.ResolveDefaultBranchLocal(...) for a"
  echo "protection decision, conductor.ResolveDefaultBranch(...) when the answer becomes part"
  echo "of a durable artifact. An undetermined answer applies the protection floor; it never"
  echo "falls back to a guessed name."
  echo "Offending added lines:"
  echo "${offenders}"
  exit 1
fi

echo "✅ No hard-coded default-branch assumptions added to git execution paths."
