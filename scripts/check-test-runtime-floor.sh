#!/usr/bin/env bash
# Fails when a pull request description reports a test run whose stated runtime
# is too short for that run to have happened.
#
# The failure this exists to catch is not dishonesty. It is an author reporting
# a package as passing from an environment where most of it silently skipped.
# Twice in one week the same package was reported at "10.041s" and "15.246s",
# once alongside an explicit assertion that a Docker daemon was available. It
# was not. Measured with a daemon present the same package takes 321s.
#
# What makes this checkable is that some packages have no skips to hide behind.
# Their runtime is bimodal: fast when the dependency is absent and most of the
# suite skips, slow when it is present and the suite actually runs. There is no
# middle. So a reported time below the floor is not a fast machine — it is a
# different run than the one being claimed, and no re-execution is needed to
# know that.
#
# This does not measure anything. It reads the number the author wrote and
# compares it to a floor established by measurement, recorded in the table
# below with the evidence for it.
#
# Reporting a skipped run honestly PASSES. A line naming the package with a
# sub-floor time and an acknowledgement that the suite skipped is an accurate
# report of what happened, which is the whole objective — the guard exists to
# stop a skipped run being described as a passing one, not to stop it being
# described. This follows check-test-sleep.sh: where the distinction cannot be
# made from the artifact, the author declares it.
#
# Scope is deliberately one package. A floor is only admissible here with a
# measurement behind it, and inventing plausible floors for other packages
# would produce a guard that fires on correct work — the failure mode that
# nearly sank check-doc-prose-deletions.sh in review.
#
# Invocation:
#   scripts/check-test-runtime-floor.sh --body <file>

set -euo pipefail

if [ "${1:-}" != "--body" ] || [ -z "${2:-}" ]; then
  echo "Usage: scripts/check-test-runtime-floor.sh --body <file>" >&2
  exit 1
fi

body_file="${2}"

if [ ! -f "${body_file}" ]; then
  echo "::error::Body file not found: ${body_file}"
  exit 1
fi

# Packages whose runtime is bimodal, with the floor separating the two modes.
#
# Format: <package-path-suffix>|<floor-seconds>|<evidence>
#
# The floor sits between the two modes rather than just under the slow one, so
# an unusually quick real run cannot trip it.
readonly FLOORS="cmd/stem/internal/conductor|60|measured 10-15s with no Docker daemon (the suite skips) and 86-515s with one (483 tests, zero skips); the spread with a daemon is image-cache warmth, and even the fastest real run is well clear of the floor"

# A sub-floor time is accepted when the same line says the run skipped. An
# accurate report of a skipped run is the desired behaviour, not a violation.
readonly SKIP_ACK='skip|skipped|skipping|docker (was )?(un)?available|no docker|without docker|daemon (un)?available'

# Writing ABOUT a past runtime claim is indistinguishable from making one: a
# description discussing why "10.041s" was wrong contains the same characters as
# a description asserting it. Nothing in the text separates the two, so the
# author declares — the same resolution check-test-sleep.sh reached for polls
# and dwells.
#
# The declaration names the durations rather than exempting the description,
# because a blanket escape would also excuse a genuine false claim standing
# beside the quotation. Any sub-floor duration NOT named here still fails.
#
#   Runtime quoted: 10.041s, 15.246s — historical claims under discussion.
readonly QUOTE_ACK='^[[:space:]]*Runtime quoted:'

quoted_durations="$(grep -E "${QUOTE_ACK}" "${body_file}" \
  | grep -oE '[0-9]+(\.[0-9]+)?s\b' | sort -u || true)"

status=0

while IFS='|' read -r pkg floor evidence; do
  [ -n "${pkg}" ] || continue

  # Only lines that name the package are considered, so a bare "(0.02s)" from
  # per-test output cannot be mistaken for a claim about the package total.
  while IFS= read -r line; do
    [ -n "${line}" ] || continue

    # Every duration on the line, in Go's own format.
    durations="$(printf '%s' "${line}" | grep -oE '[0-9]+(\.[0-9]+)?s\b' || true)"
    [ -n "${durations}" ] || continue

    if printf '%s' "${line}" | grep -qiE "${SKIP_ACK}"; then
      continue
    fi

    while IFS= read -r duration; do
      [ -n "${duration}" ] || continue
      seconds="${duration%s}"

      # A duration the description declared as quoted is under discussion, not
      # claimed. Matched exactly, so an undeclared number beside it still fails.
      if printf '%s\n' "${quoted_durations}" | grep -qxF "${duration}"; then
        continue
      fi

      # Integer comparison against the floor; the fractional part cannot change
      # the verdict because the two modes are hundreds of seconds apart.
      whole="${seconds%%.*}"
      if [ "${whole}" -lt "${floor}" ]; then
        status=1
        echo "::error::The description reports a test run too short to have happened."
        echo "Package \"${pkg}\" is reported at ${duration}, below the ${floor}s floor."
        echo "  Evidence for the floor: ${evidence}"
        echo "  Claim: ${line}"
      fi
    done <<EOF
${durations}
EOF
  done <<EOF
$(grep -F "${pkg}" "${body_file}" || true)
EOF
done <<EOF
${FLOORS}
EOF

if [ ${status} -ne 0 ]; then
  echo ""
  echo "A run this fast means the suite skipped, whatever the environment appeared to be."
  echo "Either re-run it where the dependency is present and report that time, or say"
  echo "on the same line that the run skipped — an accurate report of a skipped run passes."
  exit 1
fi

echo "✅ No test run reported below its package's measured runtime floor."
exit 0
