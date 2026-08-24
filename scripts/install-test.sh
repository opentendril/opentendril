#!/usr/bin/env bash
# Isolated tests for the first-party local installer.
#
# Disposable directories and PATH shims stand in for uname, curl, checksum
# tools, tar, and install/copy. Tests do not reach GitHub and must not mutate
# the host account or repository.
#
# Usage:
#   bash scripts/install-test.sh

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
installer="${repo_root}/install.sh"

if [ ! -f "${installer}" ]; then
  echo "missing ${installer}" >&2
  exit 1
fi

need_host_cmd() {
  local cmd=$1
  if ! type -P "${cmd}" >/dev/null 2>&1; then
    echo "host is missing ${cmd}, required to build installer fixtures" >&2
    exit 1
  fi
}

need_host_cmd tar
need_host_cmd gzip
need_host_cmd sha256sum
need_host_cmd mkdir
need_host_cmd mktemp
need_host_cmd install
need_host_cmd cp
need_host_cmd chmod
need_host_cmd rm
need_host_cmd cat
need_host_cmd tr

real_home="${HOME}"
host_tendril=""
host_tendril_hash=""
if [ -f "${real_home}/.local/bin/tendril" ]; then
  host_tendril="${real_home}/.local/bin/tendril"
  host_tendril_hash="$(sha256sum "${host_tendril}")"
fi

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/install-test.XXXXXX")"
trap 'rm -rf "${tmp_root}"' EXIT

stdout_file=""
stderr_file=""
events_file=""
status=0
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

file_hash() {
  local out
  out="$(sha256sum "$1")"
  printf '%s' "${out%% *}"
}

# --- per-case fixture -------------------------------------------------------

ROOT=""
HOME_DIR=""
TMP_DIR=""
SHIM_DIR=""
FIXTURE_DIR=""
UNAME_S=Linux
UNAME_M=x86_64
WSL_DISTRO=""
PIN_VERSION=""
CHECKSUM_TOOL=sha256sum

write_exec() {
  local path=$1
  cat >"${path}"
  chmod +x "${path}"
}

setup_shims() {
  local real_tar real_install real_cp real_sha256
  real_tar="$(type -P tar)"
  real_install="$(type -P install)"
  real_cp="$(type -P cp)"
  real_sha256="$(type -P sha256sum)"

  ln -s "$(type -P mkdir)" "${SHIM_DIR}/mkdir"
  ln -s "$(type -P mktemp)" "${SHIM_DIR}/mktemp"
  ln -s "$(type -P chmod)" "${SHIM_DIR}/chmod"
  ln -s "$(type -P rm)" "${SHIM_DIR}/rm"
  ln -s "$(type -P cat)" "${SHIM_DIR}/cat"
  ln -s "$(type -P tr)" "${SHIM_DIR}/tr"
  # GNU tar decompresses .gz by execing gzip from PATH.
  ln -s "$(type -P gzip)" "${SHIM_DIR}/gzip"

  write_exec "${SHIM_DIR}/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' "${UNAME_S:-Linux}" ;;
  -m) printf '%s\n' "${UNAME_M:-x86_64}" ;;
  *) printf '%s\n' "${UNAME_S:-Linux}" ;;
esac
EOF

  write_exec "${SHIM_DIR}/curl" <<EOF
#!/bin/sh
out=""
url=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -o|--output)
      out=\$2
      shift 2
      ;;
    --output=*)
      out=\${1#--output=}
      shift
      ;;
    --proto)
      shift 2
      ;;
    --proto=*|-fsSL|-f|-s|-S|-L|--tlsv1.2)
      shift
      ;;
    -*)
      shift
      ;;
    *)
      url=\$1
      shift
      ;;
  esac
done
printf 'CMD curl %s\n' "\$url" >> "${events_file}"
case "\$url" in
  https://github.com/opentendril/opentendril/releases/*) ;;
  *)
    printf 'curl shim: refusing non-release URL: %s\n' "\$url" >&2
    exit 1
    ;;
esac
base=\${url##*/}
if [ "\$base" = checksums.txt ] && [ -f "${ROOT}/fail-checksums-download" ]; then
  exit 1
fi
case "\$base" in
  *.tar.gz)
    if [ -f "${ROOT}/fail-archive-download" ]; then
      exit 1
    fi
    ;;
esac
src="${FIXTURE_DIR}/\$base"
if [ ! -f "\$src" ]; then
  printf 'curl shim: missing fixture %s\n' "\$src" >&2
  exit 1
fi
if [ -z "\$out" ]; then
  printf 'curl shim: no -o path\n' >&2
  exit 1
fi
"${real_cp}" "\$src" "\$out"
EOF

  write_exec "${SHIM_DIR}/tar" <<EOF
#!/bin/sh
printf 'CMD tar %s\n' "\$*" >> "${events_file}"
exec "${real_tar}" "\$@"
EOF

  write_exec "${SHIM_DIR}/install" <<EOF
#!/bin/sh
printf 'CMD install %s\n' "\$*" >> "${events_file}"
exec "${real_install}" "\$@"
EOF

  write_exec "${SHIM_DIR}/cp" <<EOF
#!/bin/sh
printf 'CMD cp %s\n' "\$*" >> "${events_file}"
exec "${real_cp}" "\$@"
EOF

  if [ "${CHECKSUM_TOOL}" = sha256sum ]; then
    write_exec "${SHIM_DIR}/sha256sum" <<EOF
#!/bin/sh
printf 'CMD sha256sum %s\n' "\$*" >> "${events_file}"
exec "${real_sha256}" "\$@"
EOF
  else
    write_exec "${SHIM_DIR}/shasum" <<EOF
#!/bin/sh
printf 'CMD shasum %s\n' "\$*" >> "${events_file}"
if [ "\$1" = "-a" ] && [ "\$2" = "256" ]; then
  shift 2
  exec "${real_sha256}" "\$@"
fi
exit 1
EOF
  fi
}

write_default_checksums() {
  local hash=$1
  cat >"${FIXTURE_DIR}/checksums.txt" <<EOF
${hash}  opentendril-linux-amd64.tar.gz
${hash}  opentendril-linux-arm64.tar.gz
${hash}  opentendril-darwin-amd64.tar.gz
${hash}  opentendril-darwin-arm64.tar.gz
EOF
}

setup_default_fixtures() {
  local staging="${ROOT}/staging"
  mkdir -p "${staging}"
  printf 'tendril-payload\n' >"${staging}/tendril"
  printf 'mcp-payload\n' >"${staging}/tendril-mcp"
  chmod 0755 "${staging}/tendril" "${staging}/tendril-mcp"
  tar -C "${staging}" -czf "${FIXTURE_DIR}/bundle.tar.gz" tendril tendril-mcp
  local hash
  hash="$(file_hash "${FIXTURE_DIR}/bundle.tar.gz")"
  for name in \
    opentendril-linux-amd64.tar.gz \
    opentendril-linux-arm64.tar.gz \
    opentendril-darwin-amd64.tar.gz \
    opentendril-darwin-arm64.tar.gz
  do
    cp "${FIXTURE_DIR}/bundle.tar.gz" "${FIXTURE_DIR}/${name}"
  done
  write_default_checksums "${hash}"
}

new_case() {
  ROOT="$(mktemp -d "${tmp_root}/case.XXXXXX")"
  HOME_DIR="${ROOT}/home"
  TMP_DIR="${ROOT}/tmp"
  SHIM_DIR="${ROOT}/shim"
  FIXTURE_DIR="${ROOT}/fixtures"
  stdout_file="${ROOT}/stdout"
  stderr_file="${ROOT}/stderr"
  events_file="${ROOT}/events"
  mkdir -p "${HOME_DIR}" "${TMP_DIR}" "${SHIM_DIR}" "${FIXTURE_DIR}"
  : >"${events_file}"
  printf 'SENTINEL-BASHRC\n' >"${HOME_DIR}/.bashrc"
  UNAME_S=Linux
  UNAME_M=x86_64
  WSL_DISTRO=""
  PIN_VERSION=""
  CHECKSUM_TOOL=sha256sum
  setup_default_fixtures
}

run_installer() {
  local stdin_mode=0
  local extra_path=""
  local installer_file="${installer}"
  local cwd="${ROOT}"
  while [ $# -gt 0 ]; do
    case "$1" in
      --stdin)
        stdin_mode=1
        shift
        ;;
      --path)
        extra_path=$2
        shift 2
        ;;
      --installer)
        installer_file=$2
        shift 2
        ;;
      --cwd)
        cwd=$2
        shift 2
        ;;
      --)
        shift
        break
        ;;
      *)
        break
        ;;
    esac
  done

  setup_shims

  local env_args=(
    env -i
    HOME="${HOME_DIR}"
    PATH="${SHIM_DIR}${extra_path:+:${extra_path}}"
    TMPDIR="${TMP_DIR}"
    LC_ALL=C
    UNAME_S="${UNAME_S}"
    UNAME_M="${UNAME_M}"
  )
  if [ -n "${WSL_DISTRO}" ]; then
    env_args+=(WSL_DISTRO_NAME="${WSL_DISTRO}")
  fi
  if [ -n "${PIN_VERSION}" ]; then
    env_args+=(OPENTENDRIL_VERSION="${PIN_VERSION}")
  fi

  set +e
  if [ "${stdin_mode}" -eq 1 ]; then
    (
      cd "${cwd}" || exit 1
      "${env_args[@]}" /bin/sh -s -- "$@" <"${installer_file}"
    ) >"${stdout_file}" 2>"${stderr_file}"
  else
    (
      cd "${cwd}" || exit 1
      "${env_args[@]}" /bin/sh "${installer_file}" "$@"
    ) >"${stdout_file}" 2>"${stderr_file}"
  fi
  status=$?
  set -e
}

dest_path() {
  printf '%s' "${HOME_DIR}/.local/bin/tendril"
}

events_match() {
  grep -Eq "$1" "${events_file}"
}

assert_no_machine_mutation() {
  local name=$1
  if [ -f "${HOME_DIR}/.bashrc" ] && [ "$(cat "${HOME_DIR}/.bashrc")" != "SENTINEL-BASHRC" ]; then
    fail "${name}: mutated .bashrc"
  fi
  if [ -e "${HOME_DIR}/.zshrc" ]; then
    fail "${name}: created .zshrc"
  fi
  if [ -e "${HOME_DIR}/.tendril" ]; then
    fail "${name}: created .tendril"
  fi
  leftover="$(find "${TMP_DIR}" -mindepth 1 -maxdepth 1 -name 'opentendril-install.*' 2>/dev/null || true)"
  if [ -n "${leftover}" ]; then
    fail "${name}: leftover temporary directory: ${leftover}"
  fi
}

assert_success_local() {
  local name=$1
  local archive=$2
  local dest
  dest="$(dest_path)"
  if [ "${status}" -ne 0 ]; then
    fail "${name}" "exit ${status}; stderr=$(tr '\n' ' ' <"${stderr_file}")"
    return
  fi
  if ! events_match "CMD curl https://github.com/opentendril/opentendril/releases/.*/${archive}"; then
    fail "${name}: did not download ${archive}" "events=$(tr '\n' ' ' <"${events_file}")"
    return
  fi
  if events_match 'tendril-mcp'; then
    fail "${name}: events mention tendril-mcp" "events=$(tr '\n' ' ' <"${events_file}")"
    return
  fi
  if ! events_match 'CMD tar .*-xzf .* tendril$'; then
    fail "${name}: tar did not extract only tendril" "events=$(tr '\n' ' ' <"${events_file}")"
    return
  fi
  if [ ! -f "${dest}" ]; then
    fail "${name}: missing ${dest}"
    return
  fi
  if [ -e "${HOME_DIR}/.local/bin/tendril-mcp" ]; then
    fail "${name}: tendril-mcp was installed"
    return
  fi
  if [ "$(cat "${dest}")" != "tendril-payload" ]; then
    fail "${name}: installed bytes are not the tendril member"
    return
  fi
  if ! grep -q "Installed: ${dest}" "${stdout_file}"; then
    fail "${name}: success output missing installed path" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return
  fi
  if ! grep -q 'LOCAL / SINGLE-PRINCIPAL' "${stdout_file}"; then
    fail "${name}: success output missing LOCAL / SINGLE-PRINCIPAL"
    return
  fi
  if ! grep -q 'does NOT establish the governed P1-P5 boundary' "${stdout_file}"; then
    fail "${name}: success output missing P1-P5 disclaimer"
    return
  fi
  if ! grep -q 'tendril hardiness' "${stdout_file}"; then
    fail "${name}: success output missing tendril hardiness"
    return
  fi
  if ! grep -q 'tendril init' "${stdout_file}"; then
    fail "${name}: success output missing tendril init"
    return
  fi
  if ! grep -q 'docs/GUIDE-INSTALL.md' "${stdout_file}"; then
    fail "${name}: success output missing GUIDE-INSTALL.md"
    return
  fi
  if grep -qi 'this is a governed' "${stdout_file}"; then
    fail "${name}: described local install as governed"
    return
  fi
  assert_no_machine_mutation "${name}"
  pass "${name}"
}

assert_failure_no_extract() {
  local name=$1
  local dest
  dest="$(dest_path)"
  if [ "${status}" -eq 0 ]; then
    fail "${name}: expected failure" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return
  fi
  if events_match '^CMD tar '; then
    fail "${name}: tar was invoked after failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return
  fi
  if events_match '^CMD install '; then
    fail "${name}: install was invoked after failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return
  fi
  if events_match '^CMD cp '; then
    fail "${name}: cp was invoked after failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return
  fi
  if [ -e "${dest}" ]; then
    fail "${name}: destination was created: ${dest}"
    return
  fi
  if [ -e "${HOME_DIR}/.local/bin/tendril-mcp" ]; then
    fail "${name}: tendril-mcp was installed"
    return
  fi
  assert_no_machine_mutation "${name}"
  pass "${name}"
}

sha_before_tar() {
  local name=$1
  local sha_line tar_line
  sha_line="$(grep -n -E '^CMD (sha256sum|shasum) ' "${events_file}" | head -n1 | cut -d: -f1 || true)"
  tar_line="$(grep -n -E '^CMD tar ' "${events_file}" | head -n1 | cut -d: -f1 || true)"
  install_line="$(grep -n -E '^CMD (install|cp) ' "${events_file}" | head -n1 | cut -d: -f1 || true)"
  if [ -z "${sha_line}" ] || [ -z "${tar_line}" ] || [ -z "${install_line}" ]; then
    fail "${name}: missing sha/tar/install events" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if [ "${sha_line}" -ge "${tar_line}" ]; then
    fail "${name}: verification did not occur before tar" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if [ "${sha_line}" -ge "${install_line}" ] || [ "${tar_line}" -ge "${install_line}" ]; then
    fail "${name}: verification/extract did not occur before install/copy" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  pass "${name}"
  return 0
}

# --- cases ------------------------------------------------------------------

new_case
run_installer
if sha_before_tar "linux amd64 verifies before tar and install"; then
  assert_success_local "linux amd64 selects opentendril-linux-amd64.tar.gz" "opentendril-linux-amd64.tar.gz"
fi
if [ "${status}" -eq 0 ] && [ "$(dest_path)" = "${HOME_DIR}/.local/bin/tendril" ]; then
  pass "destination defaults to ~/.local/bin/tendril"
else
  fail "destination defaults to ~/.local/bin/tendril" "dest=$(dest_path) status=${status}"
fi
if [ "${status}" -eq 0 ] && grep -F "export PATH=\"${HOME_DIR}/.local/bin:\$PATH\"" "${stdout_file}" >/dev/null; then
  pass "PATH-missing diagnostic is actionable"
else
  fail "PATH-missing diagnostic is actionable" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
fi
if [ "${status}" -eq 0 ] && [ ! -e "${HOME_DIR}/.local/bin/tendril-mcp" ]; then
  pass "tendril-mcp is never installed"
else
  fail "tendril-mcp is never installed"
fi
if [ "${status}" -eq 0 ] && [ "$(cat "$(dest_path)")" = "tendril-payload" ]; then
  pass "success installs only tendril"
else
  fail "success installs only tendril"
fi

new_case
UNAME_S=Linux
UNAME_M=aarch64
run_installer
assert_success_local "linux arm64 selects opentendril-linux-arm64.tar.gz" "opentendril-linux-arm64.tar.gz"

new_case
UNAME_S=Darwin
UNAME_M=x86_64
run_installer
assert_success_local "darwin amd64 selects opentendril-darwin-amd64.tar.gz" "opentendril-darwin-amd64.tar.gz"

new_case
UNAME_S=Darwin
UNAME_M=arm64
run_installer
assert_success_local "darwin arm64 selects opentendril-darwin-arm64.tar.gz" "opentendril-darwin-arm64.tar.gz"

new_case
UNAME_S=Linux
UNAME_M=x86_64
WSL_DISTRO=Ubuntu
run_installer
assert_success_local "WSL classifies as Linux local" "opentendril-linux-amd64.tar.gz"
if grep -q 'WSL follows the Linux LOCAL path' "${stdout_file}"; then
  pass "WSL message names the Linux LOCAL path"
else
  fail "WSL message names the Linux LOCAL path" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
fi

new_case
UNAME_S=FreeBSD
UNAME_M=amd64
run_installer
assert_failure_no_extract "unsupported OS fails"
if grep -q 'unsupported operating system: FreeBSD' "${stderr_file}"; then
  pass "unsupported OS names the rejected system"
else
  fail "unsupported OS names the rejected system" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi
if events_match '^CMD curl '; then
  fail "unsupported OS must not download"
fi

new_case
UNAME_S=Linux
UNAME_M=ppc64le
run_installer
assert_failure_no_extract "unsupported architecture fails"
if grep -q 'unsupported architecture: ppc64le' "${stderr_file}"; then
  pass "unsupported architecture names the rejected arch"
else
  fail "unsupported architecture names the rejected arch" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi
if events_match '^CMD curl '; then
  fail "unsupported architecture must not download"
fi

new_case
hash="$(file_hash "${FIXTURE_DIR}/bundle.tar.gz")"
cat >"${FIXTURE_DIR}/checksums.txt" <<EOF
${hash}  opentendril-linux-arm64.tar.gz
${hash}  opentendril-darwin-amd64.tar.gz
${hash}  opentendril-darwin-arm64.tar.gz
EOF
run_installer
assert_failure_no_extract "missing checksum entry fails"
if grep -q 'no entry for opentendril-linux-amd64.tar.gz' "${stderr_file}"; then
  pass "missing checksum names the archive"
else
  fail "missing checksum names the archive" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_case
hash="$(file_hash "${FIXTURE_DIR}/bundle.tar.gz")"
cat >"${FIXTURE_DIR}/checksums.txt" <<EOF
${hash}  opentendril-linux-amd64.tar.gz
${hash}  opentendril-linux-amd64.tar.gz
${hash}  opentendril-linux-arm64.tar.gz
EOF
run_installer
assert_failure_no_extract "duplicate checksum entry fails"
if grep -q '2 entries for opentendril-linux-amd64.tar.gz' "${stderr_file}"; then
  pass "duplicate checksum names the count"
else
  fail "duplicate checksum names the count" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_case
hash="$(file_hash "${FIXTURE_DIR}/bundle.tar.gz")"
cat >"${FIXTURE_DIR}/checksums.txt" <<EOF
0000000000000000000000000000000000000000000000000000000000000000  opentendril-linux-amd64.tar.gz
${hash}  opentendril-linux-arm64.tar.gz
EOF
run_installer
assert_failure_no_extract "checksum mismatch/corrupt archive fails"
if grep -q 'SHA-256 mismatch' "${stderr_file}"; then
  pass "checksum mismatch is fail-closed"
else
  fail "checksum mismatch is fail-closed" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_case
hash="$(file_hash "${FIXTURE_DIR}/bundle.tar.gz")"
write_default_checksums "${hash}"
printf '\0' >>"${FIXTURE_DIR}/opentendril-linux-amd64.tar.gz"
run_installer
assert_failure_no_extract "corrupted archive fails before tar and install"
if [ ! -e "$(dest_path)" ]; then
  pass "corrupted archive created no tendril destination"
else
  fail "corrupted archive created no tendril destination"
fi

new_case
: >"${ROOT}/fail-archive-download"
run_installer
assert_failure_no_extract "failed archive download fails"
if events_match 'CMD curl .*/checksums.txt'; then
  pass "failed archive download still fetched checksums first"
else
  fail "failed archive download still fetched checksums first" "events=$(tr '\n' ' ' <"${events_file}")"
fi

new_case
: >"${ROOT}/fail-checksums-download"
run_installer
assert_failure_no_extract "failed checksum download fails"
if events_match 'CMD curl .*/opentendril-linux-amd64.tar.gz'; then
  fail "failed checksum download must not fetch the archive"
fi

new_case
run_installer --version v0.3.0
if [ "${status}" -eq 0 ] && events_match 'CMD curl https://github.com/opentendril/opentendril/releases/download/v0.3.0/opentendril-linux-amd64.tar.gz'; then
  if events_match '/releases/latest/'; then
    fail "explicit version pin selects that release rather than latest" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "explicit version pin selects that release rather than latest"
  fi
else
  fail "explicit version pin selects that release rather than latest" "status=${status} events=$(tr '\n' ' ' <"${events_file}") stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_case
PIN_VERSION=0.3.1
run_installer
if [ "${status}" -eq 0 ] && events_match 'CMD curl https://github.com/opentendril/opentendril/releases/download/v0.3.1/checksums.txt'; then
  pass "OPENTENDRIL_VERSION pins the release tag"
else
  fail "OPENTENDRIL_VERSION pins the release tag" "status=${status} events=$(tr '\n' ' ' <"${events_file}")"
fi

new_case
PIN_VERSION=v9.9.9
run_installer --version v0.3.0
if [ "${status}" -eq 0 ] && events_match '/releases/download/v0.3.0/'; then
  if events_match '/releases/download/v9.9.9/'; then
    fail "CLI --version overrides OPENTENDRIL_VERSION"
  else
    pass "CLI --version overrides OPENTENDRIL_VERSION"
  fi
else
  fail "CLI --version overrides OPENTENDRIL_VERSION" "status=${status} events=$(tr '\n' ' ' <"${events_file}")"
fi

new_case
run_installer
if [ "${status}" -eq 0 ] && events_match '/releases/latest/download/opentendril-linux-amd64.tar.gz'; then
  pass "unpinned install uses latest GitHub Release"
else
  fail "unpinned install uses latest GitHub Release" "status=${status} events=$(tr '\n' ' ' <"${events_file}")"
fi

new_case
decoy="${ROOT}/decoy"
mkdir -p "${decoy}"
printf 'exit 99\n' >"${decoy}/install.sh"
chmod +x "${decoy}/install.sh"
copied="${ROOT}/not-the-installer-name"
cp "${installer}" "${copied}"
run_installer --stdin --cwd "${decoy}" --installer "${copied}"
if [ "${status}" -eq 0 ] && [ -f "$(dest_path)" ]; then
  if grep -q 'LOCAL / SINGLE-PRINCIPAL' "${stdout_file}"; then
    pass "stdin execution semantics do not depend on installer filename"
  else
    fail "stdin execution semantics do not depend on installer filename" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
  fi
else
  fail "stdin execution semantics do not depend on installer filename" "status=${status} stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_case
CHECKSUM_TOOL=shasum
UNAME_S=Darwin
UNAME_M=arm64
run_installer
if [ "${status}" -eq 0 ] && events_match '^CMD shasum '; then
  if events_match '^CMD sha256sum '; then
    fail "darwin checksum uses shasum when sha256sum is absent"
  else
    assert_success_local "darwin checksum uses shasum -a 256" "opentendril-darwin-arm64.tar.gz"
  fi
else
  fail "darwin checksum uses shasum when sha256sum is absent" "status=${status} events=$(tr '\n' ' ' <"${events_file}") stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_case
run_installer --path "${HOME_DIR}/.local/bin"
if [ "${status}" -eq 0 ] && ! grep -q 'is not on PATH' "${stdout_file}"; then
  pass "PATH-present omits the missing-PATH diagnostic"
else
  fail "PATH-present omits the missing-PATH diagnostic" "status=${status} stdout=$(tr '\n' ' ' <"${stdout_file}")"
fi

new_case
run_installer --governed
assert_failure_no_extract "--governed is reserved and installs nothing"
if grep -q 'reserved' "${stderr_file}" && grep -q 'LOCAL / SINGLE-PRINCIPAL' "${stderr_file}"; then
  pass "--governed refusal names LOCAL / SINGLE-PRINCIPAL"
else
  fail "--governed refusal names LOCAL / SINGLE-PRINCIPAL" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

# --- host isolation ---------------------------------------------------------

if [ -n "${host_tendril}" ]; then
  after_hash="$(sha256sum "${host_tendril}")"
  if [ "${after_hash}" = "${host_tendril_hash}" ]; then
    pass "host tendril binary was not mutated"
  else
    fail "host tendril binary was not mutated"
  fi
else
  if [ -e "${real_home}/.local/bin/tendril" ]; then
    fail "tests created a host tendril binary"
  else
    pass "tests did not create a host tendril binary"
  fi
fi

if [ "${failures}" -gt 0 ]; then
  echo
  echo "${failures} installer test(s) failed; ${passes} passed."
  exit 1
fi

echo
echo "All ${passes} installer tests passed."
