#!/usr/bin/env bash
# Fails when a tracked file's name uses an underscore where kebab-case is required.
#
# The rule is not new. `.tendril/genome/naming-conventions.md` states it as an
# absolute: "All files MUST be named using kebab-case. You are strictly
# forbidden from using underscores (_) or camelCase in filenames." It is
# injected into every Sprout that grows in this Substrate, and it is one of the
# five genome files the Botanist maintains by hand.
#
# It drifted anyway. Eight fixture files arrived in one change with underscored
# names, through a builder that had the rule in its own context and a review
# that verified those files byte-for-byte without once reading their names. That
# is the same failure mode the taxonomy guard was written for: a convention
# written down in three places and restated repeatedly still went missing,
# because nothing rejected it. A convention that depends on somebody remembering
# is a guard.
#
# WHY UNDERSCORES ONLY, when the genome also forbids camelCase: the tree already
# carries deliberate PascalCase — the React components under the interface
# surface (ChatPanel.tsx, GardenCanvas.tsx and their siblings), where the
# framework's own convention is that a component file is named for its
# component. Whether the genome rule means to override that is a decision for
# the Botanist, not something a guard should settle by going red. This follows
# the taxonomy guard's stated philosophy: add to the check when a drift is
# observed, never in anticipation of one.
#
# Two exemptions, both cases where the name is not a free choice:
#
#   1. Go source files whose underscore is read by the toolchain. `_test.go` is
#      how the compiler finds tests; `_linux.go`, `_darwin.go` and the rest of
#      the GOOS/GOARCH suffixes are build constraints. Renaming them changes
#      what the toolchain does, so they are not naming decisions at all. An
#      ordinary Go file with an underscore anywhere else is still a failure.
#   2. Filenames the wider ecosystem recognises by their exact spelling, the
#      same class of exception AGENTS.md already occupies in the taxonomy guard.
#      Today that is CODE_OF_CONDUCT.md, which GitHub locates by name.
#
# This scans the whole tracked tree rather than a diff. That is affordable
# because the backlog is empty: after the fixtures above were renamed, the only
# underscored name left in the tree is the exempt one. A diff-based check would
# let the vocabulary return to a file nobody touched.
#
# Invocation:
#   scripts/check-filename-conventions.sh

set -euo pipefail

# Go suffixes the toolchain reads. Anchored to the end of the name so that a
# file merely containing the text (my_test_helper.go) is not excused.
readonly GO_TOOLCHAIN_SUFFIXES='_(test|linux|darwin|windows|freebsd|openbsd|netbsd|js|wasm|android|ios|plan9|solaris|aix|amd64|arm64|arm|386|riscv64|loong64|mips|mips64|mipsle|mips64le|ppc64|ppc64le|s390x|wasip1)\.go$'

# Names the ecosystem locates by exact spelling. Keep this list short and add to
# it only when a tool genuinely requires the name — not because renaming would
# be inconvenient.
readonly ECOSYSTEM_NAMES='^(CODE_OF_CONDUCT\.md)$'

violations=""
while IFS= read -r path; do
  name="${path##*/}"
  case "${name}" in
    *_*) ;;
    *) continue ;;
  esac

  if printf '%s' "${name}" | grep -qE "${GO_TOOLCHAIN_SUFFIXES}"; then
    continue
  fi
  if printf '%s' "${name}" | grep -qE "${ECOSYSTEM_NAMES}"; then
    continue
  fi

  violations="${violations}${path}"$'\n'
done < <(git ls-files)

if [ -n "${violations}" ]; then
  echo "::error::Underscore in a filename. This Substrate names files in kebab-case."
  printf '%s' "${violations}" | sed 's/^/  /'
  echo
  echo "Rename using hyphens: my_fixture.json becomes my-fixture.json."
  echo "The rule is .tendril/genome/naming-conventions.md, which every Sprout is given."
  echo
  echo "Exempt: Go files whose underscore is a toolchain suffix (_test.go, _linux.go),"
  echo "and filenames the ecosystem locates by exact spelling. If a new name genuinely"
  echo "belongs in the second group, add it to this script and say which tool requires it."
  exit 1
fi

echo "✅ No underscored filenames outside the toolchain and ecosystem exemptions."
