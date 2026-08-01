#!/usr/bin/env bash
# Fails when a change ADDS a defer to a TestMain that also calls os.Exit,
# or ADDS an os.Exit to a TestMain that already has a defer.
#
# os.Exit terminates the process immediately, bypassing deferred functions.
# If a TestMain creates temporary directories (e.g., for compiled binaries)
# and defers their cleanup, but then calls os.Exit, the cleanup never runs,
# leaking files on disk.
#
# This guard enforces that TestMain functions either do not use defer,
# or do not use os.Exit, requiring explicit cleanup instead.
#
# Two modes:
#   scripts/check-testmain-cleanup.sh [base-ref]   lint ADDED lines of a diff (default: origin/main)
#   scripts/check-testmain-cleanup.sh --staged     lint ADDED lines in the staged diff
set -euo pipefail

if [ "${1:-}" = "--staged" ]; then
  diff_args=(--cached)
else
  base="${1:-origin/main}"
  diff_args=("${base}...HEAD")
fi

# Use -U100000 to include the entire file content in the diff, allowing us to
# track the boundaries of func TestMain and see all its lines, while still
# being able to distinguish which lines were added (+).
offenders="$(git diff -U100000 "${diff_args[@]}" -- '*_test.go' \
  ':!scripts/check-testmain-cleanup.sh' \
  | awk '
    /^\+\+\+ / { file = substr($0, 7); in_testmain = 0; next }
    /^@@ /     { next }
    /^(---|diff --git|index |old mode|new mode|similarity|rename )/ { next }

    {
      line = substr($0, 2)
      added = (substr($0, 1, 1) == "+")
      deleted = (substr($0, 1, 1) == "-")

      if (deleted) {
        next
      }

      # Exact match to avoid false positives in string literals (e.g., macrophage_test.go)
      if (line ~ /^func TestMain\(m \*testing\.M\)/) {
        in_testmain = 1
        has_defer = 0
        has_exit = 0
        added_defer = 0
        added_exit = 0
      }

      if (in_testmain && line ~ /^[[:space:]]*defer /) {
        has_defer = 1
        if (added) added_defer = 1
      }

      if (in_testmain && line ~ /^[[:space:]]*os\.Exit\(/) {
        has_exit = 1
        if (added) added_exit = 1
      }

      if (in_testmain && line ~ /^}/) {
        # Only complain if the combination exists AND one of them was just added
        if (has_defer && has_exit && (added_defer || added_exit)) {
          print file ": func TestMain(m *testing.M)"
        }
        in_testmain = 0
      }
    }
  ' || true)"

if [ -n "${offenders}" ]; then
  echo "::error::A TestMain function combines 'defer' and 'os.Exit', which bypasses cleanup."
  echo "os.Exit terminates the process immediately, so deferred functions are never run."
  echo "Replace 'defer cleanup()' with explicit 'cleanup()' calls before every os.Exit."
  echo "Offending TestMain functions:"
  echo "${offenders}"
  exit 1
fi

echo "✅ No TestMain functions combine defer and os.Exit."
