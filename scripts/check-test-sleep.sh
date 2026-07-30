#!/usr/bin/env bash
# Fails when a change ADDS an undeclared time.Sleep to a test.
#
# A sleep in a test is one of two things, and they are not equally acceptable:
#
#   1. The interval of a bounded poll that has a deadline, or a deliberate dwell
#      that widens a detection window. Both are fine. Too short only makes an
#      assertion weaker, never wrong.
#   2. A stand-in for synchronisation — "wait a bit for the thing to happen".
#      This is the defect. It makes the assertion able to pass because nothing
#      has happened yet, which is a silent false pass rather than a flake, and
#      a false pass is worse because nobody investigates it.
#
# Telling those apart from the source is not reliably decidable, so this guard
# does not try. It requires the author to say which one it is. That converts a
# judgement made silently into one made on the record — the same trade the
# repository already accepts for banned vocabulary and issue references.
#
# Declare an intentional sleep by putting one of these markers on the sleep's
# own line, or on either of the two lines above it:
#
#   // poll: <why>    the interval of a bounded poll that has a deadline
#   // dwell: <why>   deliberately widening a window; short only weakens it
#
# Why this is enforced rather than remembered: sleep-based synchronisation has
# caused real, repeated damage here. It produced flakes that cost whole review
# cycles to diagnose, and it produced at least one test that passed while
# exercising nothing at all, because the run had not yet reached the state the
# assertion described.
#
# This lints only ADDED lines against a base ref, so the sleeps already in the
# tree never block an unrelated change — it stops new undeclared ones creeping
# in.
#
# Two modes:
#   scripts/check-test-sleep.sh [base-ref]   lint ADDED lines of a diff (default: origin/main)
#   scripts/check-test-sleep.sh --staged     lint ADDED lines in the staged diff
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

# Two lines of leading context, so a marker on the line above the sleep counts
# whether that line is itself new or was already there.
offenders="$(git diff --unified=2 "${diff_args[@]}" -- '*_test.go' \
  ':!scripts/check-test-sleep.sh' \
  | awk '
    # Reset the lookback at every file and hunk boundary: a marker cannot carry
    # across a discontinuity in the diff.
    /^\+\+\+ / { file = substr($0, 7); prev1 = ""; prev2 = ""; next }
    /^@@ /     { prev1 = ""; prev2 = ""; next }
    /^(---|diff --git|index |old mode|new mode|similarity|rename )/ { next }

    {
      marker = "//[[:space:]]*(poll|dwell):"
      line = substr($0, 2)
      added = (substr($0, 1, 1) == "+")

      if (added && line ~ /time\.Sleep\(/) {
        declared = (line ~ marker) || (prev1 ~ marker) || (prev2 ~ marker)
        if (!declared) print file ": " line
      }

      # Deleted lines are not part of the new file, so they must not shadow the
      # real preceding line.
      if (substr($0, 1, 1) != "-") { prev2 = prev1; prev1 = line }
    }
  ' || true)"

if [ -n "${offenders}" ]; then
  echo "::error::A time.Sleep was added to a test without declaring what it is for."
  echo "Put '// poll: <why>' or '// dwell: <why>' on the sleep's line or one of the two above it."
  echo "If it is neither — if it is standing in for synchronisation — the test needs a real"
  echo "synchronisation point instead: wait on a channel, or poll a real condition against a deadline."
  echo "Offending added lines:"
  echo "${offenders}"
  exit 1
fi

echo "✅ No undeclared time.Sleep added to tests."
