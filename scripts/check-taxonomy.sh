#!/usr/bin/env bash
# Fails when standard IT vocabulary is used inside the organism.
#
# OpenTendril's taxonomy is not decoration. Every term has ONE job, and the
# canonical block the Stem injects (.tendril/genome/taxonomy-canonical.md) is
# explicit: "Use exactly the vocabulary below — each term has ONE meaning — for
# all internal architecture and code." SYNTHETIC-TAXONOMY.md names the only
# sanctioned exception: standard phrasing survives at the deliberate external
# boundary — the AGENTS.md builder-instructions file (a cross-tool standard
# *filename*) and the Meristem / intent-translation layer, whose job is exactly
# to translate IT jargon into botanical instructions.
#
# Why this is enforced rather than remembered: it was written down in three
# places, restated repeatedly by the Botanist, and drifted anyway across more
# than ten merged pull requests. "Agent" spread through code comments and
# documentation in the delegated-git subsystem — the one place the word does
# real damage, because GLOSSARY.md defines a Sprout as a worker container
# *(not "agent")* and maps "agent" to **Sprout (body) + Mycorrhizae (mind)**,
# deliberately decomposing it. Using one word for a Sprout and for an external
# mind holding a Pollen collapses a distinction with opposite
# security postures: a Sprout is network-sealed, a delegated external mind is
# not.
#
# A convention that depends on somebody remembering is a guard. This makes the
# repository reject the drift instead.
#
# This scans the WHOLE TREE, not just a diff. It began diff-based, because a
# backlog of pre-existing drift should not block unrelated work. That backlog has
# since been cleared, so the check was tightened: the vocabulary cannot return to
# a file that was never touched by the change being reviewed, and a rename that
# reintroduces the word anywhere fails immediately rather than at some later
# review.
#
# Discussing a banned term is not using it. Write it in code formatting — `agent`
# — when the term itself is the subject of the sentence; inline code and fenced
# blocks are stripped before checking, so a citation reads correctly and passes.
#
# Two modes:
#   scripts/check-taxonomy.sh [base-ref]        lint ADDED lines of a diff (default: origin/main)
#   scripts/check-taxonomy.sh --text FILE       lint a block of prose in full
#
# The --text mode exists because the drift that prompted this guard lived mostly
# in PULL REQUEST DESCRIPTIONS, which no diff-based check can see. The whole
# point of the guard is that the artifact is checked rather than the author
# trusted; prose written about the organism is an artifact too.
set -euo pipefail

mode="diff"
if [ "${1:-}" = "--text" ]; then
  mode="text"
  text_file="${2:?--text requires a file}"
else
  base="${1:-origin/main}"
fi

# Banned term -> what to use instead. Kept deliberately short: these are the
# terms that have actually drifted, not every word standard IT owns. Add to it
# when a new drift is observed, not in anticipation of one.
declare -A replacements=(
  ["agent"]="a Pollinator (an external requester holding a Pollen), a Sprout (Tendril's own sealed worker), or the Mycorrhizal Network (the LLM) — say which"
  ["sandbox"]="Terrarium"
  ["worker container"]="Sprout"
)

# Where the taxonomy is binding: everything except the sanctioned boundary and
# the documents whose job is to define or translate the words being ruled out.
targets=('.')

# The sanctioned boundary and unavoidable literals:
#   AGENTS.md            - cross-tool standard filename, named in the taxonomy
#   stoma         - an existing directory and binary name (a path, not prose)
#   INTENT-TRANSLATION   - the Meristem layer, whose job IS the translation
#   *_test.go            - fixtures reproduce real payloads and branch names
#   this script          - it necessarily names the banned terms
#   AGENTS.md            - cross-tool standard filename, named in the taxonomy
#   GLOSSARY / SYNTHETIC-TAXONOMY / taxonomy-canonical
#                        - they must name a term in order to define it
#   INTENT-TRANSLATION   - the Meristem layer, whose job IS the translation
#   HISTORY.md           - a record of what the project was called at the time;
#                          rewriting it would falsify the record, the same
#                          reasoning that left the closed issues alone
#   .mailmap             - must contain the historical author name verbatim in
#                          order to map it away
#   this script          - it necessarily names the terms it rules out
#
# Tests are deliberately NOT excluded: fixture names are where vocabulary
# quietly returns.
exclude_paths=(
  ':!**/AGENTS.md'
  ':!GLOSSARY.md'
  ':!SYNTHETIC-TAXONOMY.md'
  ':!**/taxonomy-canonical.md'
  ':!INTENT-TRANSLATION.md'
  ':!HISTORY.md'
  ':!.mailmap'
  ':!scripts/check-taxonomy.sh'
  ':!.claude/**'
)

if [ "${mode}" = "text" ]; then
  # Strip fenced code blocks and inline code before checking: a transcript of a
  # command that actually ran is evidence, and rewriting it would be falsifying
  # the record rather than correcting terminology.
  added="$(sed '/^```/,/^```/d' "${text_file}" | sed 's/`[^`]*`//g')"
else
  added="$(git grep -nIiE "$(IFS='|'; echo "\\b(${!replacements[*]})s?\\b")" \
      -- "${targets[@]}" "${exclude_paths[@]}" 2>/dev/null || true)"
fi

status=0
for term in "${!replacements[@]}"; do
  # Word-boundary match, case-insensitive, plural tolerated. Lines naming the
  # boundary file itself, or the stoma path, are not drift.
  hits="$(printf '%s\n' "${added}" | grep -IiE "\\b${term}s?\\b" \
      | grep -viE 'AGENTS\.md|AGENTS casing|Agent Development Kit|SequentialAgent|ParallelAgent|multi-agent (graph|reasoning)|workflow agents|Open Policy Agent|"Agents" blindly|run an agent tool' || true)"
  if [ -n "${hits}" ]; then
    status=1
    echo "::error::Standard IT term \"${term}\" added inside the organism."
    echo "Use instead: ${replacements[$term]}"
    echo "${hits}"
    echo
  fi
done

if [ "${status}" -ne 0 ]; then
  echo "The taxonomy is authoritative for internal code and architecture documentation."
  echo "Standard phrasing belongs only at the external boundary (AGENTS.md, the Meristem"
  echo "/ intent-translation layer). See GLOSSARY.md, SYNTHETIC-TAXONOMY.md, and"
  echo ".tendril/genome/taxonomy-canonical.md."
  exit 1
fi

if [ "${mode}" = "text" ]; then
  echo "✅ No standard IT vocabulary in the supplied text."
else
  echo "✅ No standard IT vocabulary added inside the organism."
fi
