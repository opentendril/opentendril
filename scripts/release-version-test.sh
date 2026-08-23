#!/usr/bin/env bash
# Isolated tests for scripts/release-version.sh.
#
# Fixture repositories and remotes stand in for published tag history so a
# stale or conflicting local tag cannot masquerade as authority. The script
# under test is invoked against those fixtures, never against the caller's
# checkout, and it must not create tags, push, or otherwise mutate Git state.
#
# Usage: scripts/release-version-test.sh
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tool="${script_dir}/release-version.sh"
repo_root="$(cd "${script_dir}/.." && pwd)"

if [ ! -x "${tool}" ] && [ -f "${tool}" ]; then
  chmod +x "${tool}"
fi
[ -f "${tool}" ] || {
  echo "missing ${tool}" >&2
  exit 1
}

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/release-version-test.XXXXXX")"
empty_gitconfig="${tmp_root}/empty.gitconfig"
: >"${empty_gitconfig}"
export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL="${empty_gitconfig}"
export GIT_TERMINAL_PROMPT=0
export GIT_ASKPASS=true
trap 'rm -rf "${tmp_root}"' EXIT

stdout_file="${tmp_root}/stdout"
stderr_file="${tmp_root}/stderr"
failures=0
passes=0

pass() {
  echo "ok: $1"
  passes=$((passes + 1))
}

fail() {
  echo "FAIL: $1"
  if [ "$#" -gt 1 ]; then
    printf '  %s\n' "$2"
  fi
  failures=$((failures + 1))
}

init_repo() {
  local dir="$1"
  mkdir -p "${dir}"
  git init -b main "${dir}" >/dev/null 2>&1
  git -C "${dir}" config user.name "Release Version Test"
  git -C "${dir}" config user.email "release-version-test@example.com"
  git -C "${dir}" config commit.gpgsign false
  git -C "${dir}" config tag.gpgsign false
}

# Build a bare remote whose published tags are the remaining arguments, then a
# working clone with --no-tags so local tag state starts empty.
#
# Arguments: <work-dir> <committed-version> <working-version> [published-tag-version...]
build_fixture() {
  local work="$1" committed="$2" working="$3"
  shift 3
  local seed="${work}/seed"
  local remote="${work}/remote.git"
  local repo="${work}/repo"
  local tag

  rm -rf "${work}"
  init_repo "${seed}"
  printf '%s\n' "${committed}" >"${seed}/VERSION"
  printf 'keep-me\n' >"${seed}/unrelated.txt"
  mkdir -p "${seed}/nested"
  printf 'nested\n' >"${seed}/nested/file.txt"
  git -C "${seed}" add VERSION unrelated.txt nested/file.txt
  git -C "${seed}" commit -q -m "seed ${committed}"

  for tag in "$@"; do
    git -C "${seed}" tag -a "v${tag}" -m "v${tag}"
  done

  git clone --bare --quiet "${seed}" "${remote}" >/dev/null 2>&1
  git clone --no-tags --quiet "${remote}" "${repo}" >/dev/null 2>&1
  git -C "${repo}" config user.name "Release Version Test"
  git -C "${repo}" config user.email "release-version-test@example.com"
  git -C "${repo}" config commit.gpgsign false
  git -C "${repo}" config tag.gpgsign false
  printf '%s\n' "${working}" >"${repo}/VERSION"
  printf 'untracked\n' >"${repo}/untracked.dat"

  printf '%s\n' "${repo}"
}

run_tool() {
  local repo="$1"
  shift
  (
    cd "${repo}"
    "${tool}" "$@"
  )
}

run_tool_capture() {
  local repo="$1"
  shift
  set +e
  (
    cd "${repo}"
    "${tool}" "$@"
  ) >"${stdout_file}" 2>"${stderr_file}"
  status=$?
  set -e
}

expect_stdout() {
  local name="$1" repo="$2" want="$3"
  shift 3
  run_tool_capture "${repo}" "$@"
  local got
  got="$(cat "${stdout_file}")"
  if [ "${status}" -ne 0 ]; then
    fail "${name}" "exit ${status}; stderr: $(tr '\n' ' ' <"${stderr_file}")"
    return
  fi
  if [ "${got}" != "${want}" ]; then
    fail "${name}" "stdout=${got@Q} want=${want@Q}"
    return
  fi
  pass "${name}"
}

expect_failure() {
  local name="$1" repo="$2"
  shift 2
  run_tool_capture "${repo}" "$@"
  if [ "${status}" -eq 0 ]; then
    fail "${name}" "expected failure; stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return
  fi
  pass "${name}"
}

file_checksums() {
  local repo="$1"
  (
    cd "${repo}"
    find . -path './.git' -prune -o -type f -print | sed 's|^\./||' | sort | while IFS= read -r path; do
      # One checksum line per path so a later diff names the file.
      printf '%s  %s\n' "$(sha256sum "${path}" | awk '{print $1}')" "${path}"
    done
  ) | sort
}

# --- Static contract: the tool must not mutate Git state. -------------------

git_invocations="$(grep -vE '^[[:space:]]*#' "${tool}" | grep -oE 'git[[:space:]]+[A-Za-z][A-Za-z0-9_-]*' | sort -u || true)"
want_git_invocations="$(printf '%s\n' 'git ls-remote' 'git remote' 'git rev-parse')"
if [ "${git_invocations}" = "${want_git_invocations}" ]; then
  pass "release-version.sh only consults git rev-parse/remote/ls-remote"
else
  fail "release-version.sh git surface changed" "got: $(echo "${git_invocations}" | tr '\n' ' ')"
fi

if grep -vE '^[[:space:]]*#' "${tool}" | grep -Eq 'git[[:space:]]+(tag|push|commit|merge|reset|rebase|fetch|pull|branch|update-ref)|gh[[:space:]]+(pr|release)|git[[:space:]]+push'; then
  fail "release-version.sh contains a mutating git/gh invocation"
else
  pass "release-version.sh has no tag/push/commit/PR/publication invocation"
fi

# --- Makefile no longer owns a numeric version. -----------------------------

if grep -Eq '^[[:space:]]*STEM_VERSION[[:space:]]*:?=[[:space:]]*[0-9]' "${repo_root}/Makefile"; then
  fail "Makefile still declares an independent numeric STEM_VERSION"
else
  pass "Makefile does not declare an independent numeric STEM_VERSION"
fi

if grep -Eq '^[[:space:]]*STEM_VERSION[[:space:]]*:?=.*VERSION' "${repo_root}/Makefile"; then
  pass "Makefile derives STEM_VERSION from VERSION"
else
  fail "Makefile does not derive STEM_VERSION from VERSION"
fi

# --- Valid current / next / calculation. ------------------------------------

calc="$(mktemp -d "${tmp_root}/calc.XXXXXX")"
calc_repo="$(build_fixture "${calc}" "0.2.0" "0.2.0" "0.2.0")"

expect_stdout "current reports the VERSION file" "${calc_repo}" "0.2.0" current
expect_stdout "next patch from 0.2.0" "${calc_repo}" "0.2.1" next patch
expect_stdout "next minor from 0.2.0" "${calc_repo}" "0.3.0" next minor
expect_stdout "next major from 0.2.0" "${calc_repo}" "1.0.0" next major
expect_stdout "check accepts VERSION equal to published baseline" "${calc_repo}" "0.2.0" check

before_version="$(cat "${calc_repo}/VERSION")"
run_tool "${calc_repo}" next minor >/dev/null
after_version="$(cat "${calc_repo}/VERSION")"
if [ "${before_version}" = "0.2.0" ] && [ "${after_version}" = "0.2.0" ]; then
  pass "next minor does not modify VERSION"
else
  fail "next minor modified VERSION" "before=${before_version@Q} after=${after_version@Q}"
fi

# Wider calculation coverage against an isolated VERSION, still using a remote
# so check/bump remain exercisable in later cases.
calc2="$(mktemp -d "${tmp_root}/calc2.XXXXXX")"
calc2_repo="$(build_fixture "${calc2}" "0.2.9" "0.2.9" "0.2.9")"
expect_stdout "next patch from 0.2.9 is 0.2.10" "${calc2_repo}" "0.2.10" next patch

calc3="$(mktemp -d "${tmp_root}/calc3.XXXXXX")"
calc3_repo="$(build_fixture "${calc3}" "0.9.0" "0.9.0" "0.9.0")"
expect_stdout "next minor from 0.9.0 is 0.10.0" "${calc3_repo}" "0.10.0" next minor

calc4="$(mktemp -d "${tmp_root}/calc4.XXXXXX")"
calc4_repo="$(build_fixture "${calc4}" "9.1.2" "9.1.2" "9.1.2")"
expect_stdout "next major from 9.1.2 is 10.0.0" "${calc4_repo}" "10.0.0" next major

# --- Malformed VERSION must fail closed. ------------------------------------

malformed_cases=(
  "v0.2.0"
  "0.2"
  "0.2.0.1"
  "0.2.0-rc.1"
  "0.2.0+build.1"
  "01.2.0"
  "0.02.0"
  "0.2.00"
  "0..1"
  ".2.0"
  "0.2."
  " 0.2.0"
  "0.2.0 "
  "latest"
  "0.2.0a"
  $'0.2.0\n1.0.0'
)

malformed_id=0
for value in "${malformed_cases[@]}"; do
  malformed_id=$((malformed_id + 1))
  work="$(mktemp -d "${tmp_root}/bad-${malformed_id}.XXXXXX")"
  repo="$(build_fixture "${work}" "0.2.0" "0.2.0" "0.2.0")"
  printf '%s' "${value}" >"${repo}/VERSION"
  if [[ "${value}" != *$'\n'* ]]; then
    # Keep a single-line malformed file as written; some cases include spaces.
    :
  fi
  expect_failure "malformed current fails (${malformed_id})" "${repo}" current
  expect_failure "malformed next fails (${malformed_id})" "${repo}" next minor
  expect_failure "malformed bump fails (${malformed_id})" "${repo}" bump minor
  expect_failure "malformed check fails (${malformed_id})" "${repo}" check
done

empty_work="$(mktemp -d "${tmp_root}/empty.XXXXXX")"
empty_repo="$(build_fixture "${empty_work}" "0.2.0" "0.2.0" "0.2.0")"
: >"${empty_repo}/VERSION"
expect_failure "empty VERSION fails current" "${empty_repo}" current

missing_work="$(mktemp -d "${tmp_root}/missing.XXXXXX")"
missing_repo="$(build_fixture "${missing_work}" "0.2.0" "0.2.0" "0.2.0")"
rm -f "${missing_repo}/VERSION"
expect_failure "missing VERSION fails current" "${missing_repo}" current

# --- Local tags must not override remote published history. -----------------

stale="$(mktemp -d "${tmp_root}/stale.XXXXXX")"
stale_repo="$(build_fixture "${stale}" "0.2.0" "0.2.0" "0.1.0" "0.2.0")"
# Conflicting/stale local tags: a missing fetch of v0.2.0, plus a higher local-only tag.
git -C "${stale_repo}" tag v0.1.0
git -C "${stale_repo}" tag v9.9.9
expect_stdout "current ignores local tags" "${stale_repo}" "0.2.0" current
expect_stdout "next minor ignores local tags" "${stale_repo}" "0.3.0" next minor
expect_stdout "check uses remote tags, not local v9.9.9" "${stale_repo}" "0.2.0" check

before_tags="$(git -C "${stale_repo}" tag | sort | tr '\n' ' ')"
before_remote="$(git --git-dir="${stale}/remote.git" tag | sort | tr '\n' ' ')"
before_tree="$(file_checksums "${stale_repo}")"
run_tool_capture "${stale_repo}" bump minor
if [ "${status}" -ne 0 ]; then
  fail "bump minor with conflicting local tags" "exit ${status}; stderr: $(tr '\n' ' ' <"${stderr_file}")"
else
  got="$(cat "${stdout_file}")"
  if [ "${got}" != "0.3.0" ]; then
    fail "bump minor with conflicting local tags wrote ${got@Q}" "want 0.3.0"
  else
    pass "bump minor uses remote baseline despite local v9.9.9"
  fi
fi

after_tags="$(git -C "${stale_repo}" tag | sort | tr '\n' ' ')"
after_remote="$(git --git-dir="${stale}/remote.git" tag | sort | tr '\n' ' ')"
if [ "${before_tags}" = "${after_tags}" ]; then
  pass "bump does not create or delete local tags"
else
  fail "bump mutated local tags" "before=${before_tags@Q} after=${after_tags@Q}"
fi
if [ "${before_remote}" = "${after_remote}" ]; then
  pass "bump does not publish or alter remote tags"
else
  fail "bump mutated remote tags" "before=${before_remote@Q} after=${after_remote@Q}"
fi

after_tree="$(file_checksums "${stale_repo}")"
tree_diff="$(comm -3 <(printf '%s\n' "${before_tree}") <(printf '%s\n' "${after_tree}") || true)"
changed_paths="$(printf '%s\n' "${tree_diff}" | awk '{print $NF}' | sed '/^$/d' | sort -u | tr '\n' ' ')"
if [ "${changed_paths}" = "VERSION " ] || [ "${changed_paths}" = "VERSION" ]; then
  pass "bump with conflicting local tags changes only VERSION"
else
  fail "bump changed unexpected paths" "paths=${changed_paths@Q}"
fi

if [ "$(cat "${stale_repo}/VERSION")" != "0.3.0" ]; then
  fail "VERSION after bump is not 0.3.0" "got=$(cat "${stale_repo}/VERSION" | tr '\n' ' ')"
else
  pass "VERSION after bump is 0.3.0"
fi
if [ "$(cat "${stale_repo}/unrelated.txt")" != "keep-me" ]; then
  fail "bump modified unrelated.txt"
else
  pass "unrelated.txt is unchanged after bump"
fi

# --- Already-ahead / unreleased VERSION fails closed. -----------------------

ahead="$(mktemp -d "${tmp_root}/ahead.XXXXXX")"
ahead_repo="$(build_fixture "${ahead}" "0.2.0" "0.3.0" "0.2.0")"
printf '%s\n' "0.3.0" >"${ahead_repo}/VERSION"
ahead_before="$(cat "${ahead_repo}/VERSION")"
expect_stdout "check allows a single prepared minor increment" "${ahead_repo}" "0.3.0" check
expect_failure "bump patch fails when VERSION is already ahead" "${ahead_repo}" bump patch
expect_failure "bump minor fails when VERSION is already ahead" "${ahead_repo}" bump minor
expect_failure "bump major fails when VERSION is already ahead" "${ahead_repo}" bump major
if [ "$(cat "${ahead_repo}/VERSION")" = "${ahead_before}" ]; then
  pass "failed bump leaves VERSION unchanged"
else
  fail "failed bump mutated VERSION" "got=$(cat "${ahead_repo}/VERSION" | tr '\n' ' ')"
fi

unpublished="$(mktemp -d "${tmp_root}/unpublished.XXXXXX")"
unpublished_repo="$(build_fixture "${unpublished}" "0.2.0" "0.2.0")"
expect_failure "bump fails when remote has no published tags" "${unpublished_repo}" bump minor

too_far="$(mktemp -d "${tmp_root}/toofar.XXXXXX")"
too_far_repo="$(build_fixture "${too_far}" "0.2.0" "0.4.0" "0.2.0")"
expect_failure "check fails when VERSION is two minors ahead of published" "${too_far_repo}" check

behind="$(mktemp -d "${tmp_root}/behind.XXXXXX")"
behind_repo="$(build_fixture "${behind}" "0.2.0" "0.1.0" "0.2.0")"
expect_failure "check fails when VERSION is behind published" "${behind_repo}" check
expect_failure "bump fails when VERSION is behind published" "${behind_repo}" bump minor

# --- Only VERSION changes on a successful bump. -----------------------------

only="$(mktemp -d "${tmp_root}/only.XXXXXX")"
only_repo="$(build_fixture "${only}" "1.2.3" "1.2.3" "1.2.3")"
printf 'payload\n' >"${only_repo}/extra.bin"
before_only="$(file_checksums "${only_repo}")"
expect_stdout "bump patch from 1.2.3" "${only_repo}" "1.2.4" bump patch
after_only="$(file_checksums "${only_repo}")"
only_diff="$(comm -3 <(printf '%s\n' "${before_only}") <(printf '%s\n' "${after_only}") || true)"
only_paths="$(printf '%s\n' "${only_diff}" | awk '{print $NF}' | sed '/^$/d' | sort -u | tr '\n' ' ')"
if [ "${only_paths}" = "VERSION " ] || [ "${only_paths}" = "VERSION" ]; then
  pass "successful bump changes only VERSION"
else
  fail "successful bump changed unexpected paths" "paths=${only_paths@Q}"
fi
tracked_change="$(git -C "${only_repo}" diff --name-only)"
staged_change="$(git -C "${only_repo}" diff --cached --name-only)"
if [ "${tracked_change}" = "VERSION" ] && [ -z "${staged_change}" ]; then
  pass "git diff after bump names only unstaged VERSION"
else
  fail "git diff after bump is not only unstaged VERSION" "diff=${tracked_change@Q} staged=${staged_change@Q}"
fi

# --- Argument / kind errors. ------------------------------------------------

expect_failure "unknown command fails" "${calc_repo}" not-a-command
expect_failure "next without a kind fails" "${calc_repo}" next
expect_failure "bump with an invalid kind fails" "${calc_repo}" bump hotfix
expect_failure "current rejects extra arguments" "${calc_repo}" current extra

echo
if [ "${failures}" -gt 0 ]; then
  echo "${failures} release-version test(s) failed, ${passes} passed."
  exit 1
fi
echo "All ${passes} release-version tests passed."
