#!/usr/bin/env bash
# Fails if a change ADDS a GitHub issue/PR reference to a repo source file.
#
# Repo files must be self-contained: issue/PR numbers belong in commit messages
# and pull-request descriptions (that is what GitHub is for), not baked into
# code comments, Dockerfiles, requirements, or docs. This lints only ADDED
# lines against a base ref, so pre-existing references never block a PR — it
# just stops new ones creeping in.
#
# Usage:
#   scripts/check-no-issue-refs.sh [base-ref]   lint ADDED lines vs. a base ref (default: origin/main)
#   scripts/check-no-issue-refs.sh --staged      lint ADDED lines in the staged (not-yet-committed) diff
#
# --staged exists for a pre-commit hook: at pre-commit time HEAD is still the
# previous commit, so a base...HEAD diff cannot see what is about to be
# committed. Diffing the index against HEAD does.
set -euo pipefail

if [ "${1:-}" = "--staged" ]; then
  diff_args=(--cached)
else
  base="${1:-origin/main}"
  diff_args=("${base}...HEAD")
fi

# Forbidden forms: parenthetical issue refs "(#123)"; worded refs like
# "issue #123", "PR #123", "Design RFC #123", "RFC #123", "impl plan #123";
# and GitHub issue/pull URLs. Bare "#123" in prose is intentionally NOT matched
# (too many false positives: hex colours, ordinals).
pattern='\(#[0-9]+\)|(^|[^[:alnum:]])(issue|issues|PR|pull request|Design RFC|RFC|impl(ementation)? plan)s?[[:space:]]+#[0-9]+|github\.com/[^ )"]*/(issues|pull)/[0-9]+'

# Excluded: styling/build assets (hex colours), test files (fixtures simulate
# real GitHub payloads), and this script itself (it names the pattern).
offenders="$(git diff "${diff_args[@]}" -- \
    ':!*.css' ':!*.scss' ':!ui/**' ':!static/**' ':!*_test.go' \
    ':!scripts/check-no-issue-refs.sh' \
    | grep -E '^\+' | grep -Ev '^\+\+\+' \
    | grep -EI "${pattern}" || true)"

if [ -n "${offenders}" ]; then
  echo "::error::New GitHub issue/PR references were added to source files."
  echo "Put them in the commit message / PR description instead — repo files must be self-contained."
  echo "Offending added lines:"
  echo "${offenders}"
  exit 1
fi

echo "✅ No new GitHub issue/PR references in source files."
