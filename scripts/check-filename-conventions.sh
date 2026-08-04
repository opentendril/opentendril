#!/usr/bin/env bash
# Fails when a non-Go path uses an underscore that GUARDRAILS.md does not allow.
#
# GUARDRAILS.md, "Filesystem Naming (The No Underscore Rule)", splits the tree in
# two rather than imposing one casing everywhere:
#
#   * code files (Python, Go) are merged lowercase — no underscores, no hyphens;
#   * directories and non-code files are kebab-case;
#   * Go tests are `*_test.go`, because the Go build system requires it;
#   * externally required canonical names are allowed where a platform discovers
#     a file or directory by its exact spelling.
#
# GO FILES ARE NOT CHECKED HERE. `cmd/stem/filenames_test.go` already owns them,
# and owns them more precisely than this script did: it peels the sanctioned
# suffixes off the end and judges what remains, so `a_b_test.go` fails a rule
# that forbids it. The first version of this script checked only that a name
# ENDED in a sanctioned suffix, which excused every underscore before it — the
# exact defect that test's comment records having already fixed once. This script
# now defers instead of restating the logic: a second copy is a second source of
# truth, and it will drift. It also must not advise on Go names, because the
# correct fix there is merged lowercase while the fix everywhere else is a
# hyphen, and one message cannot serve both.
#
# What was actually missing, and why this exists: nothing guarded the names of
# files that are neither Go nor Python. Eight JSON fixtures arrived underscored
# in one change, past a builder holding the rule and a review that read those
# files byte-for-byte without reading their names. A convention that depends on
# somebody remembering is a guard.
#
# Underscores only. GUARDRAILS also rules on casing, and the tree carries
# PascalCase interface components whose status under the merged-lowercase rule is
# a decision for the Botanist rather than something a guard should settle by
# going red. This follows the taxonomy guard's stated philosophy: add to the
# check when a drift is observed, never in anticipation of one.
#
# Every path COMPONENT is checked, not only the basename, because the rule covers
# directories too.
#
# Invocation:
#   scripts/check-filename-conventions.sh

set -euo pipefail

# Names a platform or tool discovers by exact spelling. Keep this short, and add
# to it only when something genuinely requires the name — never because renaming
# would be inconvenient. GUARDRAILS names this class explicitly.
is_canonical_name() {
  case "${1}" in
    # GitHub locates the community-health file and the issue-template directory
    # by these exact names.
    CODE_OF_CONDUCT.md | ISSUE_TEMPLATE) return 0 ;;
    # npm's fixed directory name. Present only inside a test fixture standing in
    # for a real workspace, where renaming it would stop it representing one.
    node_modules) return 0 ;;
    *) return 1 ;;
  esac
}

violations=""
while IFS= read -r path; do
  case "${path}" in
    *.go) continue ;;
  esac

  # Split on "/" and judge each component, so a directory is covered too.
  IFS='/' read -r -a components <<<"${path}"
  for component in "${components[@]}"; do
    case "${component}" in
      *_*) ;;
      *) continue ;;
    esac
    if is_canonical_name "${component}"; then
      continue
    fi
    violations="${violations}${path}  (component: ${component})"$'\n'
    break
  done
done < <(git ls-files)

if [ -n "${violations}" ]; then
  echo "::error::Underscore in a path. GUARDRAILS.md allows it only where a platform requires the exact name."
  printf '%s' "${violations}" | sed 's/^/  /'
  echo
  echo "Non-code files and directories are kebab-case:  my_fixture.json -> my-fixture.json"
  echo "Python modules are merged lowercase:            llm_router.py   -> llmrouter.py"
  echo
  echo "Go files are not judged here — cmd/stem/filenames_test.go owns them, and"
  echo "requires merged lowercase with a *_test.go or build-constraint suffix."
  echo
  echo "If a new name is genuinely discovered by a platform, add it to"
  echo "is_canonical_name in this script and say which tool requires it."
  exit 1
fi

echo "✅ No underscored non-Go paths outside the canonical-name exemptions."
