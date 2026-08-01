#!/usr/bin/env bash
# Fails when a pull request description cites a test function or test file
# that does not exist in the repository at HEAD.
#
# A citation is treated as satisfied if the named test exists anywhere in the
# tree at HEAD or at the base ref, not only if the diff adds it. This prevents false
# positives when a description names a pre-existing test that the change must keep
# passing, a test being modified rather than added, a test being cited as
# absent ("no test covers this yet"), or a deleted test (which exists at base
# but not at HEAD).
#
# The body is read exactly as given, including fenced blocks, because a quoted
# mutation output with a fabricated test name looks exactly like a pasted
# failure and should still be caught.
#
# Invocation:
#   scripts/check-cited-tests.sh <base-ref> --body <file>

set -euo pipefail

if [ $# -lt 3 ] || [ "${2:-}" != "--body" ]; then
  echo "Usage: scripts/check-cited-tests.sh <base-ref> --body <file>" >&2
  exit 1
fi

base="${1}"
body_file="${3}"

if [ ! -f "${body_file}" ]; then
  echo "::error::Body file not found: ${body_file}"
  exit 1
fi

# Extract test function names (Test[A-Z][A-Za-z0-9_]*)
cited_funcs=$(grep -oE '\bTest[A-Z][A-Za-z0-9_]*\b' "${body_file}" | sort -u || true)

# Extract test file references ([A-Za-z0-9_/.-]+_test\.go)
cited_files=$(grep -oE '[A-Za-z0-9_/.-]+_test\.go\b' "${body_file}" | sort -u || true)

status=0

for func in $cited_funcs; do
  # Search for the function anywhere in the tree at HEAD or base
  if ! git grep -qE "^func ${func}\b" HEAD -- '*_test.go' \
     && ! git grep -qE "^func ${func}\b" "${base}" -- '*_test.go'; then
    status=1
    echo "::error::The description cites a test function that the change does not add."
    echo "Test function \"${func}\" was not found in any *_test.go file at HEAD or base."
  fi
done

for file in $cited_files; do
  # Search for files ending with the cited file name at HEAD or base
  if ! git ls-tree -r --name-only HEAD | grep -E "(^|/)${file}$" >/dev/null \
     && ! git ls-tree -r --name-only "${base}" | grep -E "(^|/)${file}$" >/dev/null; then
    status=1
    echo "::error::The description cites a test file that the change does not add."
    echo "Test file \"${file}\" does not exist at HEAD or base."
  fi
done

if [ ${status} -ne 0 ]; then
  echo "If this is a newly written test, ensure it is committed."
  echo "If it is quoted mutation output, ensure the test actually exists."
  exit 1
fi

echo "✅ All cited test functions and files exist at HEAD."
exit 0
