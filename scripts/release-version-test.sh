#!/usr/bin/env bash
# Isolated tests for scripts/release-version.sh.
#
# Fixture repositories and remotes stand in for published tag history so a
# stale or conflicting local tag cannot masquerade as authority. Historical
# cases invoke the script against those fixtures. Caller-repository identity
# is derived from the live VERSION file: current, intended-tag, and check
# must agree with it whether the checkout is aligned to the published
# baseline or prepared one permitted increment ahead. Read-only operations
# must not create tags, push, or otherwise mutate VERSION or Git state.
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

# Snapshot of the caller repository's VERSION bytes and Git identity. A
# VERSION that already differs from HEAD is a legitimate prepared state,
# not a failure; the suite only requires that read-only operations leave
# that state unchanged.
capture_caller_state() {
  printf 'head=%s\n' "$(git -C "${repo_root}" rev-parse HEAD)"
  printf 'version-sha256=%s\n' "$(sha256sum "${repo_root}/VERSION" | awk '{print $1}')"
  printf 'status-porcelain\n'
  git -C "${repo_root}" status --porcelain
  printf 'diff-head\n'
  git -C "${repo_root}" diff HEAD --
  printf 'diff-cached\n'
  git -C "${repo_root}" diff --cached --
  printf 'local-tags\n'
  git -C "${repo_root}" tag --list | LC_ALL=C sort
}

caller_state_before="${tmp_root}/caller-state-before"
caller_state_after="${tmp_root}/caller-state-after"
capture_caller_state >"${caller_state_before}"

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
  pass "release-version.sh has no tag/push/commit/PR/GitHub-release invocation"
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
expect_stdout "check treats a successful empty remote listing as unpublished, not as a query failure" "${unpublished_repo}" "0.2.0" check
expect_failure "bump fails when remote has no published tags" "${unpublished_repo}" bump minor

# --- Configured remote whose URL cannot be queried fails closed. ------------
# Distinct from "zero published tags": origin is configured, but ls-remote
# cannot read it. That must not be mistaken for empty history.

unreachable="$(mktemp -d "${tmp_root}/unreachable.XXXXXX")"
unreachable_repo="$(build_fixture "${unreachable}" "0.2.0" "0.2.0" "0.2.0")"
git -C "${unreachable_repo}" tag v9.9.9
git -C "${unreachable_repo}" remote set-url origin "${unreachable}/missing-remote.git"
printf 'keep-me-too\n' >"${unreachable_repo}/extra.txt"

unreachable_version_before="$(cat "${unreachable_repo}/VERSION")"
unreachable_tags_before="$(git -C "${unreachable_repo}" tag | sort | tr '\n' ' ')"
unreachable_tree_before="$(file_checksums "${unreachable_repo}")"

run_tool_capture "${unreachable_repo}" check
if [ "${status}" -eq 0 ]; then
  fail "check fails when the configured remote cannot be queried" "unexpected success; stdout=$(tr '\n' ' ' <"${stdout_file}")"
elif grep -q "failed to query published tags on origin" "${stderr_file}"; then
  pass "check fails when the configured remote cannot be queried"
else
  fail "check fails when the configured remote cannot be queried" "stderr lacked the query-failure diagnostic: $(tr '\n' ' ' <"${stderr_file}")"
fi
if grep -q "no published release tags" "${stderr_file}"; then
  fail "check must not treat a failed remote query as empty tag history" "stderr: $(tr '\n' ' ' <"${stderr_file}")"
else
  pass "check does not confuse a failed query with empty published history"
fi

run_tool_capture "${unreachable_repo}" bump minor
if [ "${status}" -eq 0 ]; then
  fail "bump minor fails when the configured remote cannot be queried" "unexpected success; stdout=$(tr '\n' ' ' <"${stdout_file}")"
elif grep -q "failed to query published tags on origin" "${stderr_file}"; then
  pass "bump minor fails when the configured remote cannot be queried"
else
  fail "bump minor fails when the configured remote cannot be queried" "stderr lacked the query-failure diagnostic: $(tr '\n' ' ' <"${stderr_file}")"
fi
if grep -q "no published release tags" "${stderr_file}"; then
  fail "bump must not treat a failed remote query as empty tag history" "stderr: $(tr '\n' ' ' <"${stderr_file}")"
else
  pass "bump does not confuse a failed query with empty published history"
fi

if [ "$(cat "${unreachable_repo}/VERSION")" = "${unreachable_version_before}" ]; then
  pass "unreachable-remote failure leaves VERSION unchanged"
else
  fail "unreachable-remote failure mutated VERSION" "got=$(cat "${unreachable_repo}/VERSION" | tr '\n' ' ')"
fi

unreachable_tags_after="$(git -C "${unreachable_repo}" tag | sort | tr '\n' ' ')"
if [ "${unreachable_tags_before}" = "${unreachable_tags_after}" ]; then
  pass "unreachable-remote failure does not mutate local tags"
else
  fail "unreachable-remote failure mutated local tags" "before=${unreachable_tags_before@Q} after=${unreachable_tags_after@Q}"
fi

unreachable_tree_after="$(file_checksums "${unreachable_repo}")"
if [ "${unreachable_tree_before}" = "${unreachable_tree_after}" ]; then
  pass "unreachable-remote failure does not mutate other files"
else
  fail "unreachable-remote failure mutated the worktree" "$(comm -3 <(printf '%s\n' "${unreachable_tree_before}") <(printf '%s\n' "${unreachable_tree_after}") | tr '\n' ' ')"
fi

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
expect_failure "intended-tag rejects extra arguments" "${calc_repo}" intended-tag extra
expect_failure "publication-state without a SHA fails" "${calc_repo}" publication-state
expect_failure "publication-state rejects a short SHA" "${calc_repo}" publication-state abcdef
expect_failure "publication-state rejects extra arguments" "${calc_repo}" publication-state "$(git -C "${calc_repo}" rev-parse HEAD)" extra

# --- Publication identity and state machine. --------------------------------

kv() {
  sed -n "s/^${1}=//p" "${stdout_file}" | head -n 1
}

head_sha() {
  git -C "$1" rev-parse HEAD
}

remote_tags_list() {
  local remote_url
  remote_url="$(git -C "$1" remote get-url origin)"
  if [ -d "${remote_url}" ]; then
    git --git-dir="${remote_url}" tag | sort | tr '\n' ' '
  else
    printf '%s\n' "<unreachable>"
  fi
}

expect_publication_state() {
  local name="$1" repo="$2" source="$3" want_state="$4"
  local version_before tags_before remote_tags_before tree_before
  version_before="$(cat "${repo}/VERSION")"
  tags_before="$(git -C "${repo}" tag | sort | tr '\n' ' ')"
  remote_tags_before="$(remote_tags_list "${repo}")"
  tree_before="$(file_checksums "${repo}")"

  run_tool_capture "${repo}" publication-state "${source}"
  if [ "${status}" -ne 0 ]; then
    fail "${name}" "exit ${status}; stderr: $(tr '\n' ' ' <"${stderr_file}")"
    return
  fi
  if [ "$(kv state)" != "${want_state}" ]; then
    fail "${name}" "state=$(kv state) want=${want_state}; stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return
  fi
  if [ "$(kv source-sha)" != "${source}" ]; then
    fail "${name}" "source-sha=$(kv source-sha) want=${source}"
    return
  fi
  if [ "$(cat "${repo}/VERSION")" != "${version_before}" ]; then
    fail "${name}" "publication-state mutated VERSION"
    return
  fi
  if [ "$(git -C "${repo}" tag | sort | tr '\n' ' ')" != "${tags_before}" ]; then
    fail "${name}" "publication-state mutated local tags"
    return
  fi
  if [ "$(remote_tags_list "${repo}")" != "${remote_tags_before}" ]; then
    fail "${name}" "publication-state mutated remote tags"
    return
  fi
  if [ "$(file_checksums "${repo}")" != "${tree_before}" ]; then
    fail "${name}" "publication-state mutated the worktree"
    return
  fi
  pass "${name}"
}

expect_publication_failure() {
  local name="$1" repo="$2" source="$3" needle="$4"
  local version_before tags_before remote_tags_before tree_before
  version_before="$(cat "${repo}/VERSION")"
  tags_before="$(git -C "${repo}" tag | sort | tr '\n' ' ')"
  remote_tags_before="$(remote_tags_list "${repo}")"
  tree_before="$(file_checksums "${repo}")"

  run_tool_capture "${repo}" publication-state "${source}"
  if [ "${status}" -eq 0 ]; then
    fail "${name}" "expected failure; stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return
  fi
  if ! grep -F -q "${needle}" "${stderr_file}"; then
    fail "${name}" "stderr lacked ${needle@Q}: $(tr '\n' ' ' <"${stderr_file}")"
    return
  fi
  if [ "$(cat "${repo}/VERSION")" != "${version_before}" ]; then
    fail "${name}" "failed publication-state mutated VERSION"
    return
  fi
  if [ "$(git -C "${repo}" tag | sort | tr '\n' ' ')" != "${tags_before}" ]; then
    fail "${name}" "failed publication-state mutated local tags"
    return
  fi
  if [ "$(remote_tags_list "${repo}")" != "${remote_tags_before}" ]; then
    fail "${name}" "failed publication-state mutated remote tags"
    return
  fi
  if [ "$(file_checksums "${repo}")" != "${tree_before}" ]; then
    fail "${name}" "failed publication-state mutated the worktree"
    return
  fi
  pass "${name}"
}

advance_remote_main() {
  local repo="$1"
  printf 'advance\n' >>"${repo}/unrelated.txt"
  git -C "${repo}" add unrelated.txt
  git -C "${repo}" commit -q -m advance
  git -C "${repo}" push -q origin HEAD:main
}

push_tag_at_other_commit() {
  local repo="$1" version="$2"
  git -C "${repo}" commit --allow-empty -q -m "not-main"
  git -C "${repo}" tag -a "v${version}" HEAD -m "v${version}"
  git -C "${repo}" push -q origin "refs/tags/v${version}"
  git -C "${repo}" reset -q --hard HEAD~1
}

# Caller identity is derived from the live VERSION file. A prepared
# checkout may differ from HEAD by exactly that file; that is not a
# test failure. check is the authority for whether the value is an
# allowed aligned or prepared state.
caller_version="$(cat "${repo_root}/VERSION")"
expect_stdout "current reports the caller repository VERSION" "${repo_root}" "${caller_version}" current
expect_stdout "intended-tag from 0.2.0" "${calc_repo}" "v0.2.0" intended-tag
expect_stdout "intended-tag is v plus the caller repository VERSION" "${repo_root}" "v${caller_version}" intended-tag
expect_stdout "check accepts the caller repository VERSION" "${repo_root}" "${caller_version}" check

malformed_pub="$(mktemp -d "${tmp_root}/malformed-pub.XXXXXX")"
malformed_pub_repo="$(build_fixture "${malformed_pub}" "0.2.0" "0.2.0" "0.2.0")"
malformed_pub_sha="$(head_sha "${malformed_pub_repo}")"
printf 'v0.2.0\n' >"${malformed_pub_repo}/VERSION"
expect_publication_failure "malformed VERSION fails publication-state" \
  "${malformed_pub_repo}" "${malformed_pub_sha}" "not a stable MAJOR.MINOR.PATCH"

first="$(mktemp -d "${tmp_root}/first.XXXXXX")"
first_repo="$(build_fixture "${first}" "0.3.0" "0.3.0" "0.2.0")"
first_sha="$(head_sha "${first_repo}")"
git -C "${first_repo}" tag v9.9.9
expect_publication_state "absent intended tag is first-publication" \
  "${first_repo}" "${first_sha}" "first-publication"
if [ "$(kv intended-tag)" = "v0.3.0" ] && [ "$(kv version)" = "0.3.0" ] && [ -z "$(kv tag-commit)" ]; then
  pass "first-publication reports v0.3.0 and empty tag-commit"
else
  fail "first-publication reports v0.3.0 and empty tag-commit" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
fi

# Local-only intended tag must not look like a retry or a conflict.
local_only="$(mktemp -d "${tmp_root}/local-only.XXXXXX")"
local_only_repo="$(build_fixture "${local_only}" "0.3.0" "0.3.0" "0.2.0")"
local_only_sha="$(head_sha "${local_only_repo}")"
git -C "${local_only_repo}" tag -a v0.3.0 "${local_only_sha}" -m "local-only"
expect_publication_state "local-only intended tag is not remote authority" \
  "${local_only_repo}" "${local_only_sha}" "first-publication"

retry="$(mktemp -d "${tmp_root}/retry.XXXXXX")"
retry_repo="$(build_fixture "${retry}" "0.3.0" "0.3.0" "0.2.0" "0.3.0")"
retry_sha="$(head_sha "${retry_repo}")"
git -C "${retry_repo}" commit --allow-empty -q -m "local-other"
git -C "${retry_repo}" tag -a v0.3.0 HEAD -m "local-other"
git -C "${retry_repo}" tag v9.9.9
git -C "${retry_repo}" reset -q --hard "${retry_sha}"
expect_publication_state "intended tag at exact source commit is safe-retry" \
  "${retry_repo}" "${retry_sha}" "safe-retry"
if [ "$(kv tag-commit)" = "${retry_sha}" ]; then
  pass "safe-retry reports the remote tag commit"
else
  fail "safe-retry reports the remote tag commit" "tag-commit=$(kv tag-commit) want=${retry_sha}"
fi

conflict="$(mktemp -d "${tmp_root}/conflict.XXXXXX")"
conflict_repo="$(build_fixture "${conflict}" "0.3.0" "0.3.0" "0.2.0")"
conflict_sha="$(head_sha "${conflict_repo}")"
push_tag_at_other_commit "${conflict_repo}" "0.3.0"
expect_publication_failure "intended tag at another commit fails closed" \
  "${conflict_repo}" "${conflict_sha}" "refusing to move or overwrite it"

stale="$(mktemp -d "${tmp_root}/stale-main.XXXXXX")"
stale_repo="$(build_fixture "${stale}" "0.3.0" "0.3.0" "0.2.0")"
stale_sha="$(head_sha "${stale_repo}")"
advance_remote_main "${stale_repo}"
expect_publication_failure "stale publication source fails closed" \
  "${stale_repo}" "${stale_sha}" "refusing to publish a stale commit"

nonmono="$(mktemp -d "${tmp_root}/nonmono.XXXXXX")"
nonmono_repo="$(build_fixture "${nonmono}" "0.4.0" "0.4.0" "0.2.0")"
nonmono_sha="$(head_sha "${nonmono_repo}")"
expect_publication_failure "non-monotonic VERSION fails publication-state" \
  "${nonmono_repo}" "${nonmono_sha}" "not a single patch, minor, or major increment"

unpub="$(mktemp -d "${tmp_root}/unpub.XXXXXX")"
unpub_repo="$(build_fixture "${unpub}" "0.2.0" "0.2.0")"
unpub_sha="$(head_sha "${unpub_repo}")"
expect_publication_state "absent history is first-publication of VERSION" \
  "${unpub_repo}" "${unpub_sha}" "first-publication"

unreachable_pub="$(mktemp -d "${tmp_root}/unreachable-pub.XXXXXX")"
unreachable_pub_repo="$(build_fixture "${unreachable_pub}" "0.3.0" "0.3.0" "0.2.0")"
unreachable_pub_sha="$(head_sha "${unreachable_pub_repo}")"
git -C "${unreachable_pub_repo}" tag v9.9.9
git -C "${unreachable_pub_repo}" remote set-url origin "${unreachable_pub}/missing-remote.git"
expect_publication_failure "authoritative remote-query failure fails publication-state" \
  "${unreachable_pub_repo}" "${unreachable_pub_sha}" "failed to query"
if grep -q "no published release tags" "${stderr_file}"; then
  fail "publication-state must not treat a failed remote query as empty tag history" \
    "stderr: $(tr '\n' ' ' <"${stderr_file}")"
else
  pass "publication-state does not confuse a failed query with empty published history"
fi
if grep -q "refusing to publish a stale commit" "${stderr_file}"; then
  fail "publication-state must not treat a failed main query as stale-main" \
    "stderr: $(tr '\n' ' ' <"${stderr_file}")"
else
  pass "publication-state does not confuse a failed query with stale-main"
fi

# --- Workflow contracts (static; do not dispatch or tag). -------------------

ci_yml="${repo_root}/.github/workflows/ci.yml"
release_yml="${repo_root}/.github/workflows/release.yml"
docker_yml="${repo_root}/.github/workflows/docker-publish.yml"

on_block() {
  awk '
    /^on:/ {p=1; next}
    p && /^[a-zA-Z]/ {exit}
    p {print}
  ' "$1"
}

ci_on="$(on_block "${ci_yml}")"
if printf '%s\n' "${ci_on}" | grep -q 'workflow_dispatch:' &&
  printf '%s\n' "${ci_on}" | grep -q 'workflow_call:' &&
  printf '%s\n' "${ci_on}" | grep -q 'pull_request:' &&
  printf '%s\n' "${ci_on}" | grep -q 'push:'; then
  pass "ci.yml keeps dispatch, pull_request, push, and adds workflow_call"
else
  fail "ci.yml keeps dispatch, pull_request, push, and adds workflow_call" "on=${ci_on}"
fi

if grep -q 'force_full_verification:' "${ci_yml}" &&
  grep -q 'inputs.force_full_verification' "${ci_yml}"; then
  pass "ci.yml publication mode can force full native verification"
else
  fail "ci.yml publication mode can force full native verification"
fi

if grep -F 'github.event_name' "${ci_yml}" | grep -q 'pull_request' &&
  grep -A30 'Detect required native checks' "${ci_yml}" | grep -q 'sprout-python=true' &&
  grep -A30 'Detect required native checks' "${ci_yml}" | grep -q 'stem-go=true' &&
  grep -A30 'Detect required native checks' "${ci_yml}" | grep -q 'sprout-typescript=true'; then
  pass "ci.yml ordinary non-PR path still forces complete native checks"
else
  fail "ci.yml ordinary non-PR path still forces complete native checks"
fi

gate_names="$(grep -c 'name: Native PR Gate' "${ci_yml}" || true)"
if [ "${gate_names}" = "1" ]; then
  pass "Native PR Gate remains exactly named Native PR Gate"
else
  fail "Native PR Gate remains exactly named Native PR Gate" "count=${gate_names}"
fi

version_contract_job="$(awk '
  $0 ~ /^  version-contract:/ {p=1}
  p && $0 ~ /^  [a-zA-Z0-9_.-]+:/ && $0 !~ /^  version-contract:/ {exit}
  p {print}
' "${ci_yml}")"
if printf '%s\n' "${version_contract_job}" | grep -qE '^[[:space:]]+if:'; then
  fail "Version Contract remains unconditional" "job has if:"
else
  pass "Version Contract remains unconditional"
fi

if printf '%s\n' "${version_contract_job}" | grep -q 'name: Version Contract'; then
  pass "Version Contract job name is unchanged"
else
  fail "Version Contract job name is unchanged"
fi

release_on="$(on_block "${release_yml}")"
if printf '%s\n' "${release_on}" | grep -q 'workflow_dispatch:' &&
  ! printf '%s\n' "${release_on}" | grep -q 'push:' &&
  ! printf '%s\n' "${release_on}" | grep -q 'tags:' &&
  ! printf '%s\n' "${release_on}" | grep -q "v\\*"; then
  pass "release.yml is explicit workflow_dispatch without a v* push trigger"
else
  fail "release.yml is explicit workflow_dispatch without a v* push trigger" "on=${release_on}"
fi

if grep -q 'uses: ./.github/workflows/ci.yml' "${release_yml}" &&
  grep -q 'force_full_verification: true' "${release_yml}"; then
  pass "release.yml invokes reusable CI with full verification"
else
  fail "release.yml invokes reusable CI with full verification"
fi

if grep -q 'github.ref_name' "${release_yml}"; then
  fail "release identity is derived from VERSION, not github.ref_name"
else
  pass "release identity is derived from VERSION, not github.ref_name"
fi

if grep -q 'release-version.sh publication-state' "${release_yml}" &&
  grep -q 'release-version.sh intended-tag' "${release_yml}" &&
  grep -q 'release-version.sh current' "${release_yml}"; then
  pass "release.yml derives identity through the release-version helper"
else
  fail "release.yml derives identity through the release-version helper"
fi

if grep -q 'name: OpenTendril ${{ needs.bind.outputs.intended-tag }}' "${release_yml}"; then
  pass "GitHub Release title is OpenTendril v<VERSION>"
else
  fail "GitHub Release title is OpenTendril v<VERSION>"
fi

if grep -q 'git tag -a' "${release_yml}" &&
  grep -q 'git push origin "refs/tags/${tag}"' "${release_yml}"; then
  pass "new public tags are annotated and pushed without force"
else
  fail "new public tags are annotated and pushed without force"
fi

if grep -q "steps.pretag.outputs.state == 'first-publication'" "${release_yml}" &&
  grep -q 'safe-retry' "${release_yml}"; then
  pass "tag creation is gated to first-publication; retry requires the existing tag"
else
  fail "tag creation is gated to first-publication; retry requires the existing tag"
fi

mutating_force="$(grep -nE 'git[[:space:]]+tag[[:space:]]+(-f|--force)|git[[:space:]]+push.*--force|git[[:space:]]+push[[:space:]]+-f |--force-with-lease|git[[:space:]]+tag[[:space:]]+-d|git[[:space:]]+push.*--delete|git[[:space:]]+update-ref' "${release_yml}" "${tool}" || true)"
if [ -n "${mutating_force}" ]; then
  fail "no force-update or tag-delete path exists" "${mutating_force}"
else
  pass "no force-update or tag-delete path exists"
fi

for archive in \
  opentendril-linux-amd64.tar.gz \
  opentendril-linux-arm64.tar.gz \
  opentendril-darwin-amd64.tar.gz \
  opentendril-darwin-arm64.tar.gz \
  checksums.txt
do
  if grep -q "${archive}" "${release_yml}"; then
    pass "release artifact name ${archive} is preserved"
  else
    fail "release artifact name ${archive} is preserved"
  fi
done

if grep -q 'go build -ldflags="-s -w" -o "${staging}/tendril" ./cmd/stem' "${release_yml}" &&
  grep -q 'go build -ldflags="-s -w" -o "${staging}/tendril-mcp" ./cmd/tendril-mcp' "${release_yml}" &&
  grep -q 'tar -C "${staging}" -czf "dist/opentendril-${os}-${arch}.tar.gz" tendril tendril-mcp' "${release_yml}"; then
  pass "release archives still contain independently built tendril and tendril-mcp"
else
  fail "release archives still contain independently built tendril and tendril-mcp"
fi

if grep -q 'sha256sum' "${release_yml}"; then
  pass "SHA-256 checksum generation is preserved"
else
  fail "SHA-256 checksum generation is preserved"
fi

if git -C "${repo_root}" diff HEAD -- "${release_yml}" | grep -q .; then
  fail "release.yml is unchanged"
else
  pass "release.yml is unchanged"
fi

# Docker publication is downstream of a governed GitHub Release. Prove the
# workflow contract statically and by executing the bind script locally. Do
# not dispatch, tag, log into GHCR, or push an image.

docker_on="$(on_block "${docker_yml}")"
if printf '%s\n' "${docker_on}" | grep -q 'release:' &&
  printf '%s\n' "${docker_on}" | grep -q 'types: \[published\]' &&
  ! printf '%s\n' "${docker_on}" | grep -q 'workflow_dispatch:' &&
  ! printf '%s\n' "${docker_on}" | grep -q 'push:' &&
  ! printf '%s\n' "${docker_on}" | grep -q 'tags:' &&
  ! printf '%s\n' "${docker_on}" | grep -q "v\\*"; then
  pass "docker-publish.yml is triggered only by release published"
else
  fail "docker-publish.yml is triggered only by release published" "on=${docker_on}"
fi

if grep -q 'workflow_dispatch:' "${docker_yml}"; then
  fail "docker-publish.yml has no workflow_dispatch publication path"
else
  pass "docker-publish.yml has no workflow_dispatch publication path"
fi

if grep -F -q 'RELEASE_TAG: ${{ github.event.release.tag_name }}' "${docker_yml}"; then
  pass "docker release identity is github.event.release.tag_name"
else
  fail "docker release identity is github.event.release.tag_name"
fi

if grep -E -q 'github\.(ref_name|sha)|github\.ref[^.]|is_default_branch' "${docker_yml}"; then
  fail "docker identity does not use implicit ref, sha, or is_default_branch" \
    "$(grep -nE 'github\.(ref_name|sha)|github\.ref[^.]|is_default_branch' "${docker_yml}" | tr '\n' ' ')"
else
  pass "docker identity does not use implicit ref, sha, or is_default_branch"
fi

if grep -E -q '(^|[^[:alnum:]_])VERSION([^[:alnum:]_]|$)|release-version\.sh' "${docker_yml}"; then
  fail "docker-publish.yml does not use VERSION as a release-version authority"
else
  pass "docker-publish.yml does not use VERSION as a release-version authority"
fi

if grep -E -q '^[[:space:]]+REGISTRY: ghcr.io[[:space:]]*$' "${docker_yml}"; then
  pass "docker registry remains ghcr.io"
else
  fail "docker registry remains ghcr.io"
fi

if grep -F -q 'IMAGE_NAME: ${{ github.repository }}' "${docker_yml}"; then
  pass "docker image name remains github.repository"
else
  fail "docker image name remains github.repository"
fi

if grep -F -q 'images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}' "${docker_yml}"; then
  pass "docker metadata images remain registry/image-name"
else
  fail "docker metadata images remain registry/image-name"
fi

if grep -F -q 'type=raw,value=${{ steps.bind.outputs.version }}' "${docker_yml}"; then
  pass "stable docker tag is the bound release SemVer"
else
  fail "stable docker tag is the bound release SemVer"
fi

if grep -F -q 'type=raw,value=latest' "${docker_yml}" &&
  grep -E -q '^[[:space:]]+latest=false[[:space:]]*$' "${docker_yml}"; then
  pass "stable release publishes latest without is_default_branch"
else
  fail "stable release publishes latest without is_default_branch"
fi

if grep -F -q 'ref: refs/tags/${{ steps.bind.outputs.tag }}' "${docker_yml}"; then
  pass "docker checkout is bound to the published Release tag"
else
  fail "docker checkout is bound to the published Release tag"
fi

if grep -q 'docker/build-push-action' "${docker_yml}" &&
  grep -E -q '^[[:space:]]+push: true[[:space:]]*$' "${docker_yml}"; then
  pass "docker push remains the production publication action"
else
  fail "docker push remains the production publication action"
fi

docker_step_line() {
  grep -nE "^[[:space:]]+- name: ${1}$" "${docker_yml}" | head -n 1 | cut -d: -f1
}

bind_line="$(docker_step_line 'Bind published release identity')"
checkout_line="$(docker_step_line 'Checkout published release source')"
login_line="$(docker_step_line 'Log in to the Container registry')"
meta_line="$(docker_step_line 'Extract metadata \(tags, labels\) for Docker')"
push_line="$(docker_step_line 'Build and push Docker image')"
login_uses_line="$(grep -n 'docker/login-action' "${docker_yml}" | head -n 1 | cut -d: -f1)"
push_uses_line="$(grep -n 'docker/build-push-action' "${docker_yml}" | head -n 1 | cut -d: -f1)"

if [ -n "${bind_line}" ] && [ -n "${checkout_line}" ] && [ -n "${login_line}" ] &&
  [ -n "${meta_line}" ] && [ -n "${push_line}" ] &&
  [ "${bind_line}" -lt "${checkout_line}" ] &&
  [ "${checkout_line}" -lt "${login_line}" ] &&
  [ "${login_line}" -lt "${meta_line}" ] &&
  [ "${meta_line}" -lt "${push_line}" ] &&
  [ -n "${login_uses_line}" ] && [ -n "${push_uses_line}" ] &&
  [ "${bind_line}" -lt "${login_uses_line}" ] &&
  [ "${bind_line}" -lt "${push_uses_line}" ]; then
  pass "bind and checkout precede registry login, metadata, and push"
else
  fail "bind and checkout precede registry login, metadata, and push" \
    "bind=${bind_line} checkout=${checkout_line} login=${login_line} meta=${meta_line} push=${push_line}"
fi

extract_docker_bind_script() {
  awk '
    $0 ~ /^[[:space:]]+- name: Bind published release identity[[:space:]]*$/ {step=1}
    step && $0 ~ /^[[:space:]]+- name:/ && $0 !~ /Bind published release identity/ {exit}
    step && $0 ~ /^[[:space:]]+run: \|[[:space:]]*$/ {run=1; next}
    run {
      if ($0 ~ /^          /) {
        print substr($0, 11)
        next
      }
      if ($0 ~ /^[[:space:]]*$/) {
        print
        next
      }
      exit
    }
  ' "${docker_yml}"
}

docker_bind_script="${tmp_root}/docker-bind.sh"
docker_bind_out="${tmp_root}/docker-bind-out"
extract_docker_bind_script >"${docker_bind_script}"
if [ -s "${docker_bind_script}" ] &&
  grep -q 'github.event.release.tag_name' "${docker_yml}" &&
  grep -F -q 'tag="${RELEASE_TAG:-}"' "${docker_bind_script}"; then
  pass "docker bind script is extractable from github.event.release.tag_name"
else
  fail "docker bind script is extractable from github.event.release.tag_name"
fi

run_docker_bind() {
  : >"${docker_bind_out}"
  set +e
  RELEASE_TAG="$1" GITHUB_OUTPUT="${docker_bind_out}" bash "${docker_bind_script}" \
    >"${stdout_file}" 2>"${stderr_file}"
  status=$?
  set -e
}

expect_docker_bind_ok() {
  local name="$1" tag="$2" want_version="$3"
  run_docker_bind "${tag}"
  if [ "${status}" -ne 0 ]; then
    fail "${name}" "exit ${status}; stderr: $(tr '\n' ' ' <"${stderr_file}")"
    return
  fi
  if [ "$(sed -n 's/^tag=//p' "${docker_bind_out}" | head -n 1)" != "${tag}" ]; then
    fail "${name}" "tag output=$(tr '\n' ' ' <"${docker_bind_out}") want tag=${tag}"
    return
  fi
  if [ "$(sed -n 's/^version=//p' "${docker_bind_out}" | head -n 1)" != "${want_version}" ]; then
    fail "${name}" "version output=$(tr '\n' ' ' <"${docker_bind_out}") want version=${want_version}"
    return
  fi
  pass "${name}"
}

expect_docker_bind_fail() {
  local name="$1" tag="$2" needle="$3"
  run_docker_bind "${tag}"
  if [ "${status}" -eq 0 ]; then
    fail "${name}" "expected failure; output=$(tr '\n' ' ' <"${docker_bind_out}")"
    return
  fi
  if ! grep -F -q "${needle}" "${stderr_file}"; then
    fail "${name}" "stderr lacked ${needle@Q}: $(tr '\n' ' ' <"${stderr_file}")"
    return
  fi
  if grep -qE '^(tag|version)=' "${docker_bind_out}"; then
    fail "${name}" "failed bind wrote outputs: $(tr '\n' ' ' <"${docker_bind_out}")"
    return
  fi
  pass "${name}"
}

expect_docker_bind_ok "v0.3.0 maps to Docker SemVer 0.3.0" "v0.3.0" "0.3.0"
expect_docker_bind_ok "v1.2.3 maps to Docker SemVer 1.2.3" "v1.2.3" "1.2.3"

expect_docker_bind_fail "absent production release identity fails closed" \
  "" "published release identity is absent"

: >"${docker_bind_out}"
set +e
env -u RELEASE_TAG GITHUB_OUTPUT="${docker_bind_out}" bash "${docker_bind_script}" \
  >"${stdout_file}" 2>"${stderr_file}"
status=$?
set -e
if [ "${status}" -eq 0 ]; then
  fail "unset production release identity fails closed" "unexpected success"
elif ! grep -q "published release identity is absent" "${stderr_file}"; then
  fail "unset production release identity fails closed" \
    "stderr: $(tr '\n' ' ' <"${stderr_file}")"
elif grep -qE '^(tag|version)=' "${docker_bind_out}"; then
  fail "unset production release identity fails closed" \
    "failed bind wrote outputs: $(tr '\n' ' ' <"${docker_bind_out}")"
else
  pass "unset production release identity fails closed"
fi

malformed_release_tags=(
  "0.3.0"
  "v0.3"
  "v0.3.0-rc.1"
  "v0.3.0.1"
  "v0.3.0+build.1"
  "latest"
  "v01.2.0"
  "v0.02.0"
  "v0.3.00"
  "vv0.3.0"
  "refs/tags/v0.3.0"
  " v0.3.0"
  "v0.3.0 "
)
malformed_tag_id=0
for tag in "${malformed_release_tags[@]}"; do
  malformed_tag_id=$((malformed_tag_id + 1))
  expect_docker_bind_fail "malformed production release identity fails closed (${malformed_tag_id})" \
    "${tag}" "not a stable vMAJOR.MINOR.PATCH tag"
done

capture_caller_state >"${caller_state_after}"
if cmp -s "${caller_state_before}" "${caller_state_after}"; then
  pass "read-only release-version operations do not mutate caller VERSION or Git state"
else
  fail "read-only release-version operations do not mutate caller VERSION or Git state" \
    "$(diff -u "${caller_state_before}" "${caller_state_after}" | tr '\n' ' ')"
fi

echo
if [ "${failures}" -gt 0 ]; then
  echo "${failures} release-version test(s) failed, ${passes} passed."
  exit 1
fi
echo "All ${passes} release-version tests passed."
