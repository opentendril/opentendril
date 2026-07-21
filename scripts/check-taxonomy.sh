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
# mind holding a delegation subject collapses a distinction with opposite
# security postures: a Sprout is network-sealed, a delegated external mind is
# not.
#
# A convention that depends on somebody remembering is a guard. This makes the
# repository reject the drift instead.
#
# Like scripts/check-no-issue-refs.sh, this lints only ADDED lines against a base
# ref. Pre-existing drift never blocks a pull request — clearing it is a separate,
# deliberate decision for the Botanist, not something a hygiene check should force
# through in an unrelated change (it reaches into published documentation and at
# least one user-facing command name). This stops new drift; it does not rewrite
# history.
#
# Usage: scripts/check-taxonomy.sh [base-ref]   (default: origin/main)
set -euo pipefail

base="${1:-origin/main}"

# Banned term -> what to use instead. Kept deliberately short: these are the
# terms that have actually drifted, not every word standard IT owns. Add to it
# when a new drift is observed, not in anticipation of one.
declare -A replacements=(
  ["agent"]="a Pollinator (an external requester holding a delegation subject), a Sprout (Tendril's own sealed worker), or the Mycorrhizal Network (the LLM) — say which"
  ["sandbox"]="Terrarium"
  ["worker container"]="Sprout"
)

# Where the taxonomy is binding: internal code and architecture documentation.
targets=(
  'cmd'
  'roots'
  'docs'
  'ARCHITECTURE.md'
  'CAPABILITIES.md'
  'GUARDRAILS.md'
)

# The sanctioned boundary and unavoidable literals:
#   AGENTS.md            - cross-tool standard filename, named in the taxonomy
#   sprout-agent         - an existing directory and binary name (a path, not prose)
#   INTENT-TRANSLATION   - the Meristem layer, whose job IS the translation
#   *_test.go            - fixtures reproduce real payloads and branch names
#   this script          - it necessarily names the banned terms
exclude_paths=(
  ':!**/AGENTS.md'
  ':!cmd/sprout-agent/**'
  ':!**/*_test.go'
  ':!scripts/check-taxonomy.sh'
  ':!INTENT-TRANSLATION.md'
)

added="$(git diff "${base}...HEAD" -- "${targets[@]}" "${exclude_paths[@]}" \
    | grep -E '^\+' | grep -Ev '^\+\+\+' || true)"

status=0
for term in "${!replacements[@]}"; do
  # Word-boundary match, case-insensitive, plural tolerated. Lines naming the
  # boundary file itself, or the sprout-agent path, are not drift.
  hits="$(printf '%s\n' "${added}" | grep -IiE "\\b${term}s?\\b" \
      | grep -viE 'AGENTS\.md|sprout-agent' || true)"
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

echo "✅ No standard IT vocabulary added inside the organism."
