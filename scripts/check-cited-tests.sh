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

# Writing ABOUT a filename is indistinguishable from citing one: a description
# explaining that a guard wrongly excused `a_b_test.go` contains the same
# characters as a description claiming that file exists. Nothing in the text
# separates the two, so the author declares — the same resolution
# check-test-sleep.sh reached for sleeps and check-test-runtime-floor.sh for
# durations.
#
# The declaration names the specific tests or files rather than exempting the
# description, because a blanket escape would also excuse a genuinely fabricated
# citation standing beside the hypothetical one. Anything NOT named here still
# fails.
#
#   Names hypothetical: a_b_test.go, TestSomethingImagined — probe inputs, not repository files.
readonly HYPOTHETICAL_ACK='^[[:space:]]*Names hypothetical:'

declared=$(grep -E "${HYPOTHETICAL_ACK}" "${body_file}" \
  | grep -oE '\bTest[A-Z][A-Za-z0-9_]*\b|[A-Za-z0-9_/.-]+_test\.go\b' | sort -u || true)

is_declared() {
  printf '%s\n' "${declared}" | grep -qxF "${1}"
}

# Extract test function names (Test[A-Z][A-Za-z0-9_]*)
cited_funcs=$(grep -oE '\bTest[A-Z][A-Za-z0-9_]*\b' "${body_file}" | sort -u || true)

# Extract test file references ([A-Za-z0-9_/.-]+_test\.go)
cited_files=$(grep -oE '[A-Za-z0-9_/.-]+_test\.go\b' "${body_file}" | sort -u || true)

status=0

for func in $cited_funcs; do
  if is_declared "${func}"; then
    continue
  fi
  # Search for the function anywhere in the tree at HEAD or base
  if ! git grep -qE "^func ${func}\b" HEAD -- '*_test.go' \
     && ! git grep -qE "^func ${func}\b" "${base}" -- '*_test.go'; then
    status=1
    echo "::error::The description cites a test function that the change does not add."
    echo "Test function \"${func}\" was not found in any *_test.go file at HEAD or base."
  fi
done

for file in $cited_files; do
  if is_declared "${file}"; then
    continue
  fi
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
  echo "If the name is deliberately hypothetical — a probe input, or an example of"
  echo "what a guard wrongly accepted — declare it by name in the description:"
  echo "  Names hypothetical: a_b_test.go, TestSomethingImagined — probe inputs, not repository files."
  exit 1
fi

echo "✅ All cited test functions and files exist at HEAD."
exit 0
