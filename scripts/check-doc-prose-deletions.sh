#!/usr/bin/env bash
# Fails when a change REMOVES a sentence from the design documentation without
# saying so.
#
# The failure this exists to catch is not deliberate deletion — retiring a
# document or a stale paragraph is ordinary work. It is the sentence that goes
# out as collateral. Replacing a paragraph to add something new quietly takes an
# adjacent, still-accurate sentence with it, and nothing notices: the diff looks
# like one line replaced by one line, the reader checks that the addition is
# right, and the loss is invisible unless someone diffs the prose itself.
#
# That has happened here more than once, in the same shape both times: a design
# document's paragraph was rewritten to record new behaviour, and a sentence
# describing unrelated, still-true behaviour disappeared with it. Both were
# caught by a reviewer reading the removals rather than the additions, which is
# not a habit that can be relied upon.
#
# So this guard reports removed sentences that do not appear in what was added,
# and asks for a sentence of acknowledgement. It does not forbid the deletion.
# Naming what went is the whole point — an author who lists the removals will
# notice the one they did not mean to make.
#
# Rewording had to be filtered out. A first version reported any removed
# sentence absent from the additions, which fired on seven of the last eight
# commits that touched a design document — almost all of them ordinary rewrites.
# A gate that fires on nearly every change trains people to satisfy it without
# reading, which would defeat the entire point. So a removed sentence is treated
# as reworded, not dropped, when most of its words reappear in some added
# sentence. The sentence that went missing for real shares almost nothing with
# what replaced it, which is exactly why it survives the filter.
#
# Sentences shorter than the word floor below are ignored, which keeps code
# blocks, table rows and headings out of the report without needing to parse
# Markdown.
#
# Modes:
#   scripts/check-doc-prose-deletions.sh [base-ref]              report removals (default base: origin/main)
#   scripts/check-doc-prose-deletions.sh [base-ref] --body FILE  pass if FILE acknowledges them
#
# --body exists because the acknowledgement belongs in the pull request
# description, which no diff can see. It follows the pattern the taxonomy guard
# already established for reading the body as an artifact in its own right.
set -euo pipefail

# A removed sentence must have at least this many words to be reported.
readonly WORD_FLOOR=6

# A removed sentence is treated as reworded rather than dropped when at least
# this fraction of its words reappear in a single added sentence.
readonly REWORD_OVERLAP=0.6

# A pull request description acknowledges removals by carrying this marker.
readonly ACK_MARKER='^[[:space:]]*Prose removed:'

base="${1:-origin/main}"
body_file=""
if [ "${2:-}" = "--body" ]; then
  body_file="${3:?--body requires a file}"
fi

# Split a stream of Markdown lines into one sentence per line, normalised so
# that a sentence which merely moved between lines still compares equal.
sentences() {
  sed -E 's/^.//' \
    | sed -E 's/([.!?]) +/\1\n/g' \
    | sed -E 's/[[:space:]]+/ /g; s/^ //; s/ $//' \
    | awk -v floor="${WORD_FLOOR}" 'NF >= floor { print }' \
    | LC_ALL=C sort -u
}

doc_diff="$(git diff "${base}...HEAD" -- 'docs/**.md' 'docs/*.md' || true)"

if [ -z "${doc_diff}" ]; then
  echo "✅ No design documentation prose removed."
  exit 0
fi

removed="$(printf '%s\n' "${doc_diff}" | grep -E '^-' | grep -Ev '^---' | sentences || true)"
added="$(printf '%s\n' "${doc_diff}" | grep -E '^\+' | grep -Ev '^\+\+\+' | sentences || true)"

# Removed sentences that no added sentence substantially reproduces. An exact
# match is a sentence that merely moved; a high-overlap match is a rewrite;
# what is left is a sentence that went and did not come back.
dropped="$(awk -v threshold="${REWORD_OVERLAP}" '
  function normalise(line,   out) {
    out = tolower(line)
    gsub(/[^a-z0-9 ]/, " ", out)
    gsub(/  +/, " ", out)
    return out
  }
  NR == FNR { addedLines[FNR] = normalise($0); addedCount = FNR; next }
  {
    original = $0
    split(normalise(original), words, " ")
    total = 0
    delete want
    for (i in words) if (words[i] != "") { want[words[i]] = 1 }
    for (w in want) total++
    if (total == 0) next

    best = 0
    for (a = 1; a <= addedCount; a++) {
      split(addedLines[a], cand, " ")
      delete have
      for (i in cand) if (cand[i] != "") { have[cand[i]] = 1 }
      shared = 0
      for (w in want) if (w in have) shared++
      ratio = shared / total
      if (ratio > best) best = ratio
    }
    if (best < threshold) print original
  }
' <(printf '%s\n' "${added}") <(printf '%s\n' "${removed}") | awk 'NF')"

if [ -z "${dropped}" ]; then
  echo "✅ No design documentation prose removed."
  exit 0
fi

if [ -n "${body_file}" ] && grep -qE "${ACK_MARKER}" "${body_file}"; then
  echo "✅ Design documentation prose removed, and acknowledged in the description."
  exit 0
fi

echo "::error::A change removed sentences from the design documentation without acknowledging them."
echo "If every removal is intended, add a line to the pull request description that starts with"
echo "'Prose removed:' and says what went and why. If one of these is a sentence you did not mean"
echo "to lose — the usual cause is replacing a whole paragraph — put it back."
echo "Removed and not present in what was added:"
printf '%s\n' "${dropped}" | sed 's/^/  - /'
exit 1
