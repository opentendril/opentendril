#!/usr/bin/env bash
# Regressions for scripts/check-no-em-dash.sh.
#
# Fixtures are disposable repositories. They prove added lines fail, historical
# lines do not, and the Source Hygiene job is wired into the aggregate gate.
#
# Usage: scripts/check-no-em-dash-test.sh
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
tool="${script_dir}/check-no-em-dash.sh"
workflow="${repo_root}/.github/workflows/check-source-hygiene.yml"

[ -f "${tool}" ] || {
  echo "missing ${tool}" >&2
  exit 1
}

# U+2014 as UTF-8. The source of this test must not contain the character.
em_dash="$(printf '\342\200\224')"

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/check-no-em-dash-test.XXXXXX")"
trap 'rm -rf "${tmp_root}"' EXIT

passes=0
failures=0
repo_n=0
repo=""
out=""
status=0

ok() {
  echo "ok: $1"
  passes=$((passes + 1))
}

fail() {
  echo "FAIL: $1"
  if [ "$#" -gt 1 ]; then
    printf '%s\n' "$2"
  fi
  failures=$((failures + 1))
}

make_repo() {
  repo_n=$((repo_n + 1))
  repo="${tmp_root}/repo-${repo_n}"
  git init -q -b main "${repo}"
  git -C "${repo}" config user.name "Hygiene Test"
  git -C "${repo}" config user.email "hygiene-test@example.com"
  git -C "${repo}" config commit.gpgsign false
}

commit_repo() {
  local msg="$1"
  shift
  git -C "${repo}" add -- "$@"
  git -C "${repo}" -c commit.gpgsign=false commit -q -m "${msg}"
}

run_guard() {
  set +e
  out="$(cd "${repo}" && bash "${tool}" "$@")"
  status=$?
  set -e
}

expect_fail() {
  local name="$1"
  local needle="$2"
  if [ "${status}" -eq 0 ]; then
    fail "${name} (expected failure, got success)" "${out}"
    return
  fi
  if ! printf '%s\n' "${out}" | grep -qF -- "${needle}"; then
    fail "${name} (failed, missing ${needle})" "${out}"
    return
  fi
  ok "${name}"
}

expect_pass() {
  local name="$1"
  if [ "${status}" -ne 0 ]; then
    fail "${name} (expected success, got ${status})" "${out}"
    return
  fi
  if ! printf '%s\n' "${out}" | grep -qF "No new em dash characters in added lines."; then
    fail "${name} (succeeded without the pass message)" "${out}"
    return
  fi
  ok "${name}"
}

# 1. An added line containing U+2014 fails, and the file is left untouched.
make_repo
printf 'alpha\n' > "${repo}/notes.txt"
commit_repo "base" notes.txt
printf 'alpha\nbeta EMDASHCASE1 %s gamma\n' "${em_dash}" > "${repo}/notes.txt"
commit_repo "add em dash" notes.txt
before="$(sha256sum "${repo}/notes.txt")"
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_fail "added line containing U+2014 fails" "notes.txt:2:"
after="$(sha256sum "${repo}/notes.txt")"
if [ "${before}" = "${after}" ]; then
  ok "failing run does not rewrite the file"
else
  fail "failing run does not rewrite the file" "before ${before} after ${after}"
fi

# 2. An added Markdown line containing U+2014 fails.
make_repo
mkdir -p "${repo}/docs"
printf '# Page\n\nHello.\n' > "${repo}/docs/page.md"
commit_repo "base" docs/page.md
printf '# Page\n\nHello.\n\nA clause EMDASHCASE2 %s another.\n' "${em_dash}" > "${repo}/docs/page.md"
commit_repo "add markdown em dash" docs/page.md
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_fail "added Markdown line containing U+2014 fails" "docs/page.md:5:"

# 3. An added source-code comment containing U+2014 fails.
make_repo
printf 'package sample\n\nfunc F() {}\n' > "${repo}/sample.go"
commit_repo "base" sample.go
printf 'package sample\n\n// Keep the window open EMDASHCASE3 %s then close it.\nfunc F() {}\n' "${em_dash}" > "${repo}/sample.go"
commit_repo "add comment em dash" sample.go
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_fail "added source comment containing U+2014 fails" "sample.go:3:"

# 4. An added user-visible string containing U+2014 fails.
make_repo
mkdir -p "${repo}/ui"
printf 'export function Banner() {\n  return <p>Welcome</p>;\n}\n' > "${repo}/ui/Banner.tsx"
commit_repo "base" ui/Banner.tsx
printf 'export function Banner() {\n  return <p>Welcome EMDASHCASE4 %s back</p>;\n}\n' "${em_dash}" > "${repo}/ui/Banner.tsx"
commit_repo "add visible string em dash" ui/Banner.tsx
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_fail "added user-visible string containing U+2014 fails" "ui/Banner.tsx:2:"

# 5. An unchanged historical em dash does not fail.
make_repo
printf 'Already here %s stay.\n' "${em_dash}" > "${repo}/old.md"
commit_repo "historical" old.md
printf 'a normal hyphen - line\n' > "${repo}/other.txt"
commit_repo "unrelated addition" other.txt
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_pass "unchanged historical em dash does not fail"
if printf '%s\n' "${out}" | grep -qF "old.md"; then
  fail "unchanged historical file was reported" "${out}"
else
  ok "unchanged historical file is not reported"
fi

# 6. A removed historical em dash does not fail.
make_repo
printf 'Historical %s sentence.\nkeep\n' "${em_dash}" > "${repo}/old.md"
commit_repo "historical" old.md
printf 'keep\n' > "${repo}/old.md"
commit_repo "remove historical" old.md
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_pass "removed historical em dash does not fail"

# 7. Ordinary ASCII hyphens do not fail.
make_repo
printf 'start\n' > "${repo}/hyphens.md"
commit_repo "base" hyphens.md
printf 'start\nhyphen -\ndouble --\n---\nrange 1-2\npre-commit em-dash wording\n' > "${repo}/hyphens.md"
commit_repo "add hyphens" hyphens.md
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_pass "ordinary ASCII hyphens do not fail"

# Other tracked text, not only Markdown and source comments.
make_repo
mkdir -p "${repo}/sprouts/example"
printf 'FROM scratch\n' > "${repo}/sprouts/example/Dockerfile"
commit_repo "base" sprouts/example/Dockerfile
printf 'FROM scratch\n# keep EMDASHCASEDOCKER %s this\n' "${em_dash}" > "${repo}/sprouts/example/Dockerfile"
commit_repo "add dockerfile em dash" sprouts/example/Dockerfile
run_guard "$(git -C "${repo}" rev-parse HEAD~1)"
expect_fail "added Dockerfile line containing U+2014 fails" "sprouts/example/Dockerfile:2:"

# Staged mode, the pre-commit path.
make_repo
printf 'historical %s line\nplain\n' "${em_dash}" > "${repo}/staged.txt"
commit_repo "historical" staged.txt
printf 'historical %s line\nplain updated\n' "${em_dash}" > "${repo}/staged.txt"
git -C "${repo}" add -- staged.txt
run_guard --staged
expect_pass "staged edit beside a historical em dash does not fail"
printf 'brand new EMDASHCASESTAGED %s\n' "${em_dash}" > "${repo}/fresh.txt"
git -C "${repo}" add -- fresh.txt
run_guard --staged
expect_fail "staged added line containing U+2014 fails" "fresh.txt:1:"

# 8. The new job is part of the aggregate Source Hygiene Gate.
job_ids="$(awk '
  /^jobs:/ { in_jobs = 1; next }
  in_jobs && /^[^ ]/ { in_jobs = 0 }
  in_jobs && /^  [a-z][a-z0-9_-]*:[[:space:]]*$/ { gsub(/[ :]/, ""); print }
' "${workflow}")"
needs="$(awk '
  $0 == "  hygiene-gate:" { in_gate = 1; next }
  in_gate && /^  [a-z]/ { in_gate = 0 }
  in_gate && /^    needs:/ { in_needs = 1; next }
  in_needs && /^      - / { sub(/^      - /, ""); print; next }
  in_needs && !/^      - / { in_needs = 0 }
' "${workflow}")"

if printf '%s\n' "${job_ids}" | grep -qxF "no-em-dash"; then
  ok "workflow defines the no-em-dash job"
else
  fail "workflow defines the no-em-dash job" "${job_ids}"
fi

if printf '%s\n' "${needs}" | grep -qxF "no-em-dash"; then
  ok "no-em-dash is included in hygiene-gate.needs"
else
  fail "no-em-dash is included in hygiene-gate.needs" "${needs}"
fi

set +e
gate_out="$(cd "${repo_root}" && bash scripts/check-hygiene-gate-complete.sh)"
gate_status=$?
set -e
if [ "${gate_status}" -eq 0 ]; then
  ok "aggregate Source Hygiene Gate completeness guard passes"
else
  fail "aggregate Source Hygiene Gate completeness guard passes" "${gate_out}"
fi

if grep -qF 'bash scripts/check-no-em-dash-test.sh' "${workflow}" \
  && grep -qF 'bash scripts/check-no-em-dash.sh ' "${workflow}"; then
  ok "workflow runs the em dash guard and its regressions"
else
  fail "workflow runs the em dash guard and its regressions"
fi

if grep -qF 'bash scripts/check-no-em-dash.sh origin/main' "${repo_root}/Makefile"; then
  ok "make hygiene runs the em dash guard"
else
  fail "make hygiene runs the em dash guard"
fi

if grep -qF 'bash scripts/check-no-em-dash.sh --staged' "${repo_root}/.githooks/pre-commit"; then
  ok "pre-commit runs the em dash guard on the staged diff"
else
  fail "pre-commit runs the em dash guard on the staged diff"
fi

echo
if [ "${failures}" -gt 0 ]; then
  echo "${failures} em dash guard test(s) failed, ${passes} passed."
  exit 1
fi

echo "All ${passes} em dash guard tests passed."
exit 0
