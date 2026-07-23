#!/usr/bin/env bash
# Fails when a tracked markdown file contains a relative link whose target
# file does not exist.
#
# Scans every tracked *.md file for markdown links of the form [text](target).
# Skips http(s):, mailto:, and bare fragment anchors (#section). Fragment
# suffixes on file targets (path.md#heading) are stripped before the existence
# check. Targets are resolved relative to the directory of the file that
# contains the link.
#
# Usage: scripts/check-doc-links.sh
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "${root}"

broken=0
report=""

# Extract ](target) groups. Title-bearing links — ](path "title") — keep only
# the path token. Angle-bracket targets ](<path>) are normalized. Inline code
# and fenced blocks are not stripped: a broken link inside a code fence is
# still a link the reader will click if rendered outside a fence, and this
# guard is deliberately simple rather than a full markdown parser.
while IFS= read -r -d '' file; do
  dir="$(dirname "${file}")"

  # -n prefixes each match with line number; -o emits only the match.
  # Match the (target) half of a markdown link.
  while IFS= read -r match; do
    [ -z "${match}" ] && continue
    line="${match%%:*}"
    raw="${match#*:}"
    # raw is like ](target) or ](target "title")
    target="${raw#](}"
    target="${target%)}"
    # Drop optional title: first whitespace-separated token is the path.
    target="${target%%[[:space:]]*}"
    # Strip angle brackets used in some markdown dialects.
    if [[ "${target}" == \<*\> ]]; then
      target="${target:1:${#target}-2}"
    fi
    # Skip empty, external, mailto, and bare-anchor targets.
    [ -z "${target}" ] && continue
    case "${target}" in
      http://*|https://*|mailto:*|//*) continue ;;
      \#*) continue ;;
    esac
    # Strip fragment (#heading) and query (?x) for the existence check.
    path="${target%%#*}"
    path="${path%%\?*}"
    [ -z "${path}" ] && continue

    # Resolve relative to the file's directory (not the repo root).
    if [[ "${path}" == /* ]]; then
      # Repo-root-absolute style is uncommon; treat as relative to root.
      resolved="${root}${path}"
    else
      resolved="${dir}/${path}"
    fi
    # Normalize .. and . components without requiring the path to exist yet.
    if command -v realpath >/dev/null 2>&1; then
      # realpath -m allows missing last component; we check existence ourselves.
      if resolved_norm="$(realpath -m "${resolved}" 2>/dev/null)"; then
        resolved="${resolved_norm}"
      fi
    fi

    if [ ! -e "${resolved}" ]; then
      broken=$((broken + 1))
      report+="  ${file}:${line} -> ${target}"$'\n'
    fi
  done < <(grep -nEo '\]\([^)]+\)' "${file}" 2>/dev/null || true)
done < <(git ls-files -z -- '*.md')

if [ "${broken}" -gt 0 ]; then
  echo "::error::Broken relative markdown links (${broken}):"
  printf '%s' "${report}"
  echo "Fix the link target or restore the missing file."
  exit 1
fi

echo "✅ All relative markdown links in tracked .md files resolve."
