#!/usr/bin/env bash
# Fails when a change ADDS a line containing the Unicode em dash (U+2014).
#
# Repository prose, comments, user-visible strings, and documentation use
# ordinary punctuation: commas, colons, semicolons, parentheses, or hyphens.
# A newly added em dash is rejected in every tracked text file the diff
# touches, including Markdown, source, comments, configuration, UI strings,
# Dockerfiles, and examples.
#
# The check compares against a base ref and inspects added lines only. An
# em dash that already exists on the base does not fail a pull request while
# it stays unchanged, and deleting one does not fail the pull request either.
# Nothing in this script rewrites the tree.
#
# Usage:
#   scripts/check-no-em-dash.sh [base-ref]  lint added lines (default: origin/main)
#   scripts/check-no-em-dash.sh --staged    lint added lines in the staged diff
#
# --staged is for the pre-commit hook. At pre-commit time HEAD is still the
# previous commit, so a base...HEAD diff cannot see what is about to be
# committed. Diffing the index against HEAD does, and it still ignores
# unstaged historical lines.
set -euo pipefail

if [ "${1:-}" = "--staged" ]; then
  diff_args=(--cached)
else
  base="${1:-origin/main}"
  diff_args=("${base}...HEAD")
fi

# U+2014 encoded as UTF-8 (e2 80 94). Built at runtime so this file does not
# itself contain the character it rejects.
em_dash="$(printf '\342\200\224')"

# Force the standard a/ b/ prefixes and literal paths. A caller's diff.* config
# (mnemonic prefixes, noprefix, quoted paths) must not hide the file name.
offenders="$(
  git -c diff.mnemonicPrefix=false -c diff.noprefix=false -c core.quotepath=false \
    --no-pager diff --no-color --unified=0 --src-prefix=a/ --dst-prefix=b/ \
    "${diff_args[@]}" \
    | awk -v em="${em_dash}" '
      function dest_path(raw,    path) {
        path = raw
        if (substr(path, 1, 1) == "\"") {
          sub(/^"/, "", path)
          sub(/"$/, "", path)
        }
        sub(/^b\//, "", path)
        return path
      }

      /^\+\+\+ / {
        path = dest_path(substr($0, 5))
        if (path == "/dev/null") {
          file = ""
        } else {
          file = path
        }
        new_line = 0
        next
      }

      # Parse only the hunk range. Trailing context can contain a plus sign,
      # and a greedy match against the whole header would read that instead
      # of the new-file line number.
      /^@@ / {
        new_line = 0
        hdr = $0
        sub(/^@@ /, "", hdr)
        if (match(hdr, /^\-[0-9]+(,[0-9]+)? \+[0-9]+/)) {
          plus = substr(hdr, RSTART, RLENGTH)
          sub(/.*\+/, "", plus)
          new_line = plus + 0
        }
        next
      }

      /^\+/ {
        line = substr($0, 2)
        if (file != "" && new_line >= 1 && index(line, em) > 0) {
          printf "%s:%d: %s\n", file, new_line, line
        }
        if (new_line >= 1) {
          new_line++
        }
        next
      }
    '
)"

if [ -n "${offenders}" ]; then
  echo "::error::New em dash characters were added."
  echo "Repository prose, comments, user-visible strings, and documentation must use ordinary punctuation: commas, colons, semicolons, parentheses, or hyphens."
  echo "The guard does not rewrite files. Remove the character from the added line."
  echo "Offending added lines:"
  printf '%s\n' "${offenders}"
  while IFS= read -r hit; do
    [ -n "${hit}" ] || continue
    file="${hit%%:*}"
    rest="${hit#*:}"
    line="${rest%%:*}"
    printf '::error file=%s,line=%s::added line contains an em dash (U+2014)\n' "${file}" "${line}"
  done <<< "${offenders}"
  exit 1
fi

echo "✅ No new em dash characters in added lines."
exit 0
