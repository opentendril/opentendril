#!/usr/bin/env bash
# Resolves the code a brief cites, against the branch a builder will start from.
#
# A brief points at code by path and line. Both go stale, and they go stale
# silently: across one batch, cited line numbers were wrong on at least four
# occasions because one file was touched by nine separate merges and a brief
# written against one state was handed over against another. The cost is not the
# wrong number — it is a whole build-and-review cycle spent editing the wrong
# place with complete confidence.
#
# This is a pre-flight, run against the brief before it is handed over, not a
# gate on a pull request. There is no dispatch path inside the organism to hook
# it into: briefs are written here and executed elsewhere.
#
# Two things are decidable and both are checked:
#   - the cited file exists at the reference
#   - the cited line is inside that file
#
# A line that has MOVED but is still in range cannot be detected from a number
# alone, so every resolvable citation is printed with the line it now lands on.
# Reading five of those is quick; discovering the same drift after a build is not.
# This is why addressing by symbol and content excerpt is the durable form — a
# line number is a hint.
#
# The default reference is origin/main because that is what a builder branches
# from. Checking a brief against the working tree would pass on local edits the
# builder will never see, which is the failure this exists to prevent.
#
# Invocation:
#   scripts/check-prompt-anchors.sh <brief-file> [ref]

set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: scripts/check-prompt-anchors.sh <brief-file> [ref]" >&2
  exit 1
fi

brief="${1}"
ref="${2:-origin/main}"

if [ ! -f "${brief}" ]; then
  echo "::error::Brief not found: ${brief}"
  exit 1
fi

if ! git rev-parse --verify --quiet "${ref}" >/dev/null; then
  echo "::error::Reference ${ref} does not resolve. Fetch it first — a brief checked against a stale reference proves nothing."
  exit 1
fi

# A citation is a backticked path, optionally with :line or :start-end.
#
# A slash or a line suffix is required. Without that floor a bare "main.go" used
# illustratively in prose reads as a citation, and a check that fires on prose is
# one nobody reads.
citations="$(grep -oE '`[A-Za-z0-9_.-]*[A-Za-z0-9_/.-]+\.[A-Za-z]+(:[0-9]+(-[0-9]+)?)?`' "${brief}" \
  | tr -d '`' \
  | grep -E '/|:[0-9]' \
  | sort -u || true)"

if [ -z "${citations}" ]; then
  echo "ℹ️  No file citations found in ${brief}."
  exit 0
fi

status=0
resolved=0

for citation in ${citations}; do
  path="${citation%%:*}"
  lines="${citation#"${path}"}"
  lines="${lines#:}"

  if ! content="$(git show "${ref}:${path}" 2>/dev/null)"; then
    status=1
    echo "::error::${citation} — no such file at ${ref}."
    continue
  fi

  total="$(printf '%s\n' "${content}" | wc -l)"

  if [ -z "${lines}" ]; then
    resolved=$((resolved + 1))
    echo "✅ ${path} (${total} lines)"
    continue
  fi

  first="${lines%%-*}"
  last="${lines##*-}"

  if [ "${first}" -gt "${total}" ] || [ "${last}" -gt "${total}" ]; then
    status=1
    echo "::error::${citation} — the file has ${total} lines at ${ref}, so this citation points past its end."
    continue
  fi

  resolved=$((resolved + 1))
  # What the number actually lands on now. The brief's claim about it is the
  # thing being checked, and only a person can do that.
  echo "✅ ${citation} →"
  printf '%s\n' "${content}" | sed -n "${first},${last}p" | head -5 | sed 's/^/       /'
  if [ "$((last - first))" -ge 5 ]; then
    echo "       … $((last - first + 1 - 5)) more line(s)"
  fi
done

echo ""
if [ ${status} -ne 0 ]; then
  echo "A citation does not resolve at ${ref}. Fix the brief before handing it over —"
  echo "a builder given a stale anchor edits the wrong place confidently."
  exit 1
fi

echo "✅ ${resolved} citation(s) resolve at ${ref}. Confirm each excerpt above says what the brief claims."
exit 0
