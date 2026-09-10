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
need_host_cmd id
need_host_cmd stat
need_host_cmd grep
need_host_cmd touch
need_host_cmd cmp

real_home="${HOME}"
host_tendril=""
host_tendril_hash=""
if [ -f "${real_home}/.local/bin/tendril" ]; then
  host_tendril="${real_home}/.local/bin/tendril"
  host_tendril_hash="$(sha256sum "${host_tendril}")"
fi

host_unit_path="/etc/systemd/system/tendril.service"
host_sudoers_path="/etc/sudoers.d/opentendril-p2"
host_unit_before="absent"
host_sudoers_before="absent"
if [ -e "${host_unit_path}" ]; then
  host_unit_before="$(stat -c '%s %Y' "${host_unit_path}" 2>/dev/null || echo present)"
fi
if [ -e "${host_sudoers_path}" ]; then
  host_sudoers_before="$(stat -c '%s %Y' "${host_sudoers_path}" 2>/dev/null || echo present)"
fi
host_tendril_home_before="$(ls -la /home/tendril 2>/dev/null || echo '__missing__')"

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
  rm -f "${path}"
  cat >"${path}"
  chmod +x "${path}"
}

setup_shims() {
  local real_tar real_install real_cp real_sha256
  real_tar="$(type -P tar)"
  real_install="$(type -P install)"
  real_cp="$(type -P cp)"
  real_sha256="$(type -P sha256sum)"

  ln -sfn "$(type -P mkdir)" "${SHIM_DIR}/mkdir"
  ln -sfn "$(type -P mktemp)" "${SHIM_DIR}/mktemp"
  ln -sfn "$(type -P chmod)" "${SHIM_DIR}/chmod"
  ln -sfn "$(type -P rm)" "${SHIM_DIR}/rm"
  ln -sfn "$(type -P cat)" "${SHIM_DIR}/cat"
  ln -sfn "$(type -P tr)" "${SHIM_DIR}/tr"
  ln -sfn "$(type -P id)" "${SHIM_DIR}/id"
  ln -sfn "$(type -P grep)" "${SHIM_DIR}/grep"
  ln -sfn "$(type -P touch)" "${SHIM_DIR}/touch"
  # GNU tar decompresses .gz by execing gzip from PATH.
  ln -sfn "$(type -P gzip)" "${SHIM_DIR}/gzip"

  write_exec "${SHIM_DIR}/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' "${UNAME_S:-Linux}" ;;
  -m) printf '%s\n' "${UNAME_M:-x86_64}" ;;
  -r) printf '%s\n' "${UNAME_R:-6.8.0-generic}" ;;
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
  cat >"${staging}/tendril" <<'EOF'
#!/bin/sh
if [ "${1:-}" = --version ]; then
  printf 'tendril %s\n' "${FIXTURE_VERSION:-0.3.13}"
else
  printf 'tendril-payload\n'
fi
EOF
  cat >"${staging}/tendril-mcp" <<'EOF'
#!/bin/sh
if [ "${1:-}" = --version ]; then
  printf 'tendril-mcp %s\n' "${FIXTURE_VERSION:-0.3.13}"
else
  printf 'mcp-payload\n'
fi
EOF
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
  local fixture_version="${PIN_VERSION#v}"
  local scan_arg scan_value
  local scan_args=("$@")
  local scan_index
  for ((scan_index = 0; scan_index < ${#scan_args[@]}; scan_index++)); do
    scan_arg="${scan_args[scan_index]}"
    case "${scan_arg}" in
      --version=*)
        fixture_version="${scan_arg#--version=}"
        ;;
      --version)
        if [ $((scan_index + 1)) -lt ${#scan_args[@]} ]; then
          scan_value="${scan_args[scan_index + 1]}"
          fixture_version="${scan_value#v}"
          scan_index=$((scan_index + 1))
        fi
        ;;
    esac
  done
  if [ -z "${fixture_version}" ]; then
    fixture_version=0.3.13
  fi
  if [ -f "${ROOT}/fail-version" ]; then
    fixture_version=9.9.9
  fi
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
    FIXTURE_VERSION="${fixture_version}"
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
  if ! "${dest}" --version | grep -Eq '^tendril [0-9]+\.[0-9]+\.[0-9]+$'; then
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
  if ! grep -Fq 'https://github.com/opentendril/opentendril/blob/main/docs/GUIDE-INSTALL.md' "${stdout_file}"; then
    fail "${name}: success output missing public GUIDE-INSTALL.md URL" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return
  fi
  if ! grep -Fq 'https://github.com/opentendril/opentendril/blob/main/docs/GUIDE-QUICKSTART.md' "${stdout_file}"; then
    fail "${name}: success output missing public GUIDE-QUICKSTART.md URL" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
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
if [ "${status}" -eq 0 ] && "$(dest_path)" --version | grep -Eq '^tendril [0-9]+\.[0-9]+\.[0-9]+$'; then
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
touch "${ROOT}/fail-version"
run_installer --version v0.3.0
if [ "${status}" -ne 0 ] && grep -q "reports 'tendril 9.9.9', expected 'tendril 0.3.0'" "${stderr_file}"; then
  if [ -e "$(dest_path)" ]; then
    fail "mismatched staged binary version is not installed"
  else
    pass "mismatched staged binary version fails closed"
  fi
else
  fail "mismatched staged binary version fails closed" "status=${status} stderr=$(tr '\n' ' ' <"${stderr_file}")"
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
assert_failure_no_extract "--governed requires root"
if grep -q 'requires root' "${stderr_file}"; then
  pass "--governed without root names the uid 0 requirement"
else
  fail "--governed without root names the uid 0 requirement" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi
if events_match '^CMD (apt-get |adduser |usermod |loginctl |systemctl )'; then
  fail "--governed without root must not mutate the host" "events=$(tr '\n' ' ' <"${events_file}")"
fi

new_case
run_installer --governed --governed-upgrade --pollinator-user alice
assert_failure_no_extract "--governed and --governed-upgrade are mutually exclusive"
if grep -q 'mutually exclusive' "${stderr_file}"; then
  pass "--governed and --governed-upgrade are mutually exclusive"
else
  fail "--governed and --governed-upgrade are mutually exclusive" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

# --- governed helpers -------------------------------------------------------

HOSTFS=""
GOVERNED_SUDO_USER=""

write_os_release() {
  local id=$1
  local version=$2
  mkdir -p "${HOSTFS}/etc"
  cat >"${HOSTFS}/etc/os-release" <<EOF
PRETTY_NAME="${id} ${version}"
NAME="${id}"
ID=${id}
VERSION_ID="${version}"
EOF
}

setup_governed_host() {
  HOSTFS="${ROOT}/hostfs"
  mkdir -p \
    "${HOSTFS}/etc/apt/keyrings" \
    "${HOSTFS}/etc/apt/sources.list.d" \
    "${HOSTFS}/etc/systemd/system" \
    "${HOSTFS}/etc/sudoers.d" \
    "${HOSTFS}/home/alice" \
    "${HOSTFS}/root" \
    "${HOSTFS}/run/user" \
    "${HOSTFS}/var/lib" \
    "${HOSTFS}/proc/sys/kernel" \
    "${HOSTFS}/sys/module/nf_tables" \
    "${HOSTFS}/lib/modules/6.8.0-generic" \
    "${ROOT}/state/pkg" \
    "${ROOT}/state/active" \
    "${ROOT}/state/enabled" \
    "${ROOT}/state/masked" \
    "${ROOT}/meta/owners"
  printf '0\n' >"${ROOT}/euid"
  printf '6.8.0-generic\n' >"${HOSTFS}/proc/sys/kernel/osrelease"
  printf 'nf_tables 217088 0 - Live 0x0000000000000000\n' >"${HOSTFS}/proc/modules"
  : >"${HOSTFS}/lib/modules/6.8.0-generic/modules.builtin"
  write_os_release ubuntu 24.04
  cat >"${HOSTFS}/etc/passwd" <<'EOF'
root:x:0:0:root:/root:/bin/bash
alice:x:1000:1000:Alice:/home/alice:/bin/bash
EOF
  cat >"${HOSTFS}/etc/shadow" <<'EOF'
root:*:19600:0:99999:7:::
alice:!:19600:0:99999:7:::
EOF
  cat >"${HOSTFS}/etc/group" <<'EOF'
root:x:0:
alice:x:1000:
EOF
  cat >"${HOSTFS}/etc/subuid" <<'EOF'
alice:100000:65536
EOF
  cat >"${HOSTFS}/etc/subgid" <<'EOF'
alice:100000:65536
EOF
  mkdir -p "${ROOT}/meta/owners"
  printf 'alice\n' >"${ROOT}/meta/owners/%home%alice"
  printf 'dummy-docker-gpg\n' >"${FIXTURE_DIR}/docker-gpg"
  cat >"${ROOT}/state/sudo-l" <<'EOF'
User alice may run the following commands on this host:
    (ALL : ALL) ALL
EOF
  GOVERNED_SUDO_USER=""
}

new_governed_case() {
  new_case
  setup_governed_host
}

setup_governed_shims() {
  local real_stat real_mkdir real_chmod real_rm real_cat real_install real_cp real_touch real_cmp
  setup_shims
  real_stat="$(type -P stat)"
  real_mkdir="$(type -P mkdir)"
  real_chmod="$(type -P chmod)"
  real_rm="$(type -P rm)"
  real_cat="$(type -P cat)"
  real_install="$(type -P install)"
  real_cp="$(type -P cp)"
  real_touch="$(type -P touch)"
  real_cmp="$(type -P cmp)"
  ln -sf "$(type -P true)" "${SHIM_DIR}/true"
  ln -sf "$(type -P false)" "${SHIM_DIR}/false"

  cat >"${SHIM_DIR}/hostpath.lib" <<EOF
ROOT="${ROOT}"
HOSTFS="${HOSTFS}"
EVENTS="${events_file}"
hostpath() {
  p=\$1
  case "\$p" in
    /usr/*|/etc/*|/home/*|/run/*|/var/*|/proc/sys/kernel/osrelease|/proc/modules|/sys/module/*|/lib/modules/*)
      printf '%s%s' "${HOSTFS}" "\$p"
      ;;
    *)
      printf '%s' "\$p"
      ;;
  esac
}
owner_key() {
  printf '%s' "\$1" | tr '/' '%'
}
set_owner() {
  p=\$1
  u=\$2
  ${real_mkdir} -p "${ROOT}/meta/owners"
  printf '%s\\n' "\$u" > "${ROOT}/meta/owners/\$(owner_key "\$p")"
}
get_owner() {
  p=\$1
  f="${ROOT}/meta/owners/\$(owner_key "\$p")"
  if [ -f "\$f" ]; then
    ${real_cat} "\$f"
  else
    printf 'root\\n'
  fi
}
set_group() {
  p=\$1
  g=\$2
  ${real_mkdir} -p "${ROOT}/meta/groups"
  printf '%s\\n' "\$g" > "${ROOT}/meta/groups/\$(owner_key "\$p")"
}
get_group() {
  p=\$1
  f="${ROOT}/meta/groups/\$(owner_key "\$p")"
  if [ -f "\$f" ]; then
    ${real_cat} "\$f"
  else
    get_owner "\$p"
  fi
}
logcmd() {
  printf 'CMD %s\\n' "\$*" >> "${events_file}"
}
EOF
  chmod 0644 "${SHIM_DIR}/hostpath.lib"

  write_exec "${SHIM_DIR}/uname" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
case "\$1" in
  -s) printf '%s\\n' "${UNAME_S:-Linux}" ;;
  -m) printf '%s\\n' "${UNAME_M:-x86_64}" ;;
  -r) ${real_cat} "${HOSTFS}/proc/sys/kernel/osrelease" ;;
  *) printf '%s\\n' "${UNAME_S:-Linux}" ;;
esac
EOF

  write_exec "${SHIM_DIR}/id" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd id "\$*"
uid_only=0
groups=0
gid_name=0
user=""
for a in "\$@"; do
  case "\$a" in
    -u) uid_only=1 ;;
    -Gn) groups=1 ;;
    -gn) gid_name=1 ;;
    -G) groups=1 ;;
    -g) gid_name=1 ;;
    -n) ;;
    -*) ;;
    *) user=\$a ;;
  esac
done
if [ "\$uid_only" -eq 1 ]; then
  if [ -z "\$user" ]; then
    ${real_cat} "${ROOT}/euid"
    exit 0
  fi
  line=\$(grep "^\$user:" "${HOSTFS}/etc/passwd") || exit 1
  IFS=: read -r _n _x uid _rest <<LINE
\$line
LINE
  printf '%s\\n' "\$uid"
  exit 0
fi
if [ "\$groups" -eq 1 ]; then
  if [ -f "${ROOT}/state/groups-\$user" ]; then
    ${real_cat} "${ROOT}/state/groups-\$user"
    exit 0
  fi
  printf '%s\\n' "\$user"
  exit 0
fi
if [ "\$gid_name" -eq 1 ]; then
  printf '%s\\n' "\$user"
  exit 0
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/getent" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd getent "\$*"
db=\$1
key=\$2
case "\$db" in
  passwd)
    grep "^\$key:" "${HOSTFS}/etc/passwd" && exit 0
    exit 2
    ;;
  shadow)
    grep "^\$key:" "${HOSTFS}/etc/shadow" && exit 0
    exit 2
    ;;
  *)
    exit 1
    ;;
esac
EOF

  write_exec "${SHIM_DIR}/cmp" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd cmp "\$*"
args=""
for a in "\$@"; do
  case "\$a" in
    -*) args="\$args \$a" ;;
    *) args="\$args \$(hostpath "\$a")" ;;
  esac
done
# shellcheck disable=SC2086
exec ${real_cmp} \$args
EOF
  write_exec "${SHIM_DIR}/cat" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
if [ \$# -eq 0 ]; then
  exec ${real_cat}
fi
logcmd cat "\$*"
for a in "\$@"; do
  ${real_cat} "\$(hostpath "\$a")" || exit 1
done
EOF

  write_exec "${SHIM_DIR}/stat" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd stat "\$*"
fmt=""
if [ "\$1" = "-c" ]; then
  fmt=\$2
  shift 2
fi
path=\$1
rpath=\$(hostpath "\$path")
if [ -n "\$fmt" ]; then
  case "\$fmt" in
    %F)
      if [ -d "\$rpath" ]; then
        printf 'directory\\n'
      elif [ -f "\$rpath" ]; then
        printf 'regular file\\n'
      elif [ -S "\$rpath" ]; then
        printf 'socket\\n'
      else
        exec ${real_stat} -c '%F' "\$rpath"
      fi
      ;;
    %U)
      get_owner "\$path"
      ;;
    %G)
      get_group "\$path"
      ;;
    %a)
      ${real_stat} -c '%a' "\$rpath"
      ;;
    *)
      exec ${real_stat} -c "\$fmt" "\$rpath"
      ;;
  esac
  exit 0
fi
exec ${real_stat} "\$rpath"
EOF

  write_exec "${SHIM_DIR}/mkdir" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd mkdir "\$*"
args=""
for a in "\$@"; do
  case "\$a" in
    -*) args="\$args \$a" ;;
    *) args="\$args \$(hostpath "\$a")" ;;
  esac
done
# shellcheck disable=SC2086
exec ${real_mkdir} \$args
EOF

  write_exec "${SHIM_DIR}/chmod" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd chmod "\$*"
mode=""
targets=""
for a in "\$@"; do
  case "\$a" in
    -*) ;;
    *)
      if [ -z "\$mode" ]; then
        mode=\$a
      else
        targets="\$targets \$(hostpath "\$a")"
      fi
      ;;
  esac
done
# shellcheck disable=SC2086
exec ${real_chmod} "\$mode" \$targets
EOF

  write_exec "${SHIM_DIR}/rm" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd rm "\$*"
args=""
for a in "\$@"; do
  case "\$a" in
    -*) args="\$args \$a" ;;
    *) args="\$args \$(hostpath "\$a")" ;;
  esac
done
# shellcheck disable=SC2086
exec ${real_rm} \$args
EOF

  write_exec "${SHIM_DIR}/cp" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd cp "\$*"
first_file=""
for a in "\$@"; do
  case "\$a" in
    -*) ;;
    *)
      if [ -z "\$first_file" ]; then
        first_file=\$a
      fi
      ;;
  esac
done
if [ -f "${ROOT}/state/fail-rollback-cp" ]; then
  case "\$first_file" in
    */rollback/*)
      printf 'cp shim: simulated rollback copy failure\\n' >&2
      exit 1
      ;;
  esac
fi
args=""
for a in "\$@"; do
  case "\$a" in
    -*) args="\$args \$a" ;;
    *) args="\$args \$(hostpath "\$a")" ;;
  esac
done
# shellcheck disable=SC2086
exec ${real_cp} \$args
EOF

  write_exec "${SHIM_DIR}/install" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd install "\$*"
directory=0
mode=0755
owner=""
group=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -d) directory=1; shift ;;
    -m) mode=\$2; shift 2 ;;
    -o) owner=\$2; shift 2 ;;
    -g) group=\$2; shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
if [ "\$directory" -eq 1 ]; then
  for dest in "\$@"; do
    rdest=\$(hostpath "\$dest")
    ${real_mkdir} -p "\$rdest"
    ${real_chmod} "\$mode" "\$rdest" || true
    if [ -n "\$owner" ]; then
      set_owner "\$dest" "\$owner"
    fi
    if [ -n "\$group" ]; then
      set_group "\$dest" "\$group"
    fi
  done
  exit 0
fi
if [ \$# -lt 2 ]; then
  exit 1
fi
src=\$1
dest=\$2
if [ -f "${ROOT}/state/fail-install-unit" ] && [ "\$dest" = "/usr/local/lib/systemd/system/tendril.service" ]; then
  printf 'install shim: simulated unit install failure\\n' >&2
  exit 1
fi
if [ -f "${ROOT}/state/fail-install-tendril" ] && [ "\$dest" = "/home/tendril/.local/bin/tendril" ]; then
  printf 'install shim: simulated Stem binary install failure\\n' >&2
  exit 1
fi
if [ -f "${ROOT}/state/fail-install-mcp" ]; then
  case "\$dest" in
    */tendril-mcp)
      printf 'install shim: simulated MCP install failure\\n' >&2
      exit 1
      ;;
  esac
fi
rdest=\$(hostpath "\$dest")
parent=\${rdest%/*}
${real_mkdir} -p "\$parent"
${real_install} -m "\$mode" "\$src" "\$rdest"
if [ -n "\$owner" ]; then
  set_owner "\$dest" "\$owner"
fi
if [ -n "\$group" ]; then
  set_group "\$dest" "\$group"
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/curl" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
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
printf 'CMD curl %s\\n' "\$url" >> "${events_file}"
case "\$url" in
  https://github.com/opentendril/opentendril/releases/*) ;;
  https://download.docker.com/linux/ubuntu/gpg) ;;
  *)
    printf 'curl shim: refusing URL: %s\\n' "\$url" >&2
    exit 1
    ;;
esac
if [ "\$url" = "https://download.docker.com/linux/ubuntu/gpg" ]; then
  src="${FIXTURE_DIR}/docker-gpg"
else
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
fi
if [ ! -f "\$src" ]; then
  printf 'curl shim: missing fixture %s\\n' "\$src" >&2
  exit 1
fi
if [ -z "\$out" ]; then
  printf 'curl shim: no -o path\\n' >&2
  exit 1
fi
${real_cp} "\$src" "\$out"
EOF

  write_exec "${SHIM_DIR}/dpkg" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd dpkg "\$*"
if [ "\$1" = "--print-architecture" ]; then
  printf 'amd64\\n'
  exit 0
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/dpkg-query" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd dpkg-query "\$*"
pkg=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -W|--show) shift ;;
    -f|--showformat) shift 2 ;;
    -f*) shift ;;
    *) pkg=\$1; shift ;;
  esac
done
if [ -n "\$pkg" ] && [ -f "${ROOT}/state/pkg/\$pkg" ]; then
  printf 'install ok installed\\n'
  exit 0
fi
exit 1
EOF

  write_exec "${SHIM_DIR}/apt-get" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd apt-get "\$*"
apt_call_number=0
if [ -f "${ROOT}/state/apt-call-count" ]; then
  apt_call_number=\$(${real_cat} "${ROOT}/state/apt-call-count")
fi
apt_call_number=\$((apt_call_number + 1))
printf '%s\n' "\$apt_call_number" >"${ROOT}/state/apt-call-count"
apt_lock=0
apt_lock_message=canonical
if [ -f "${ROOT}/state/apt-lock-alternate" ] && [ "\$apt_call_number" -eq 1 ]; then
  apt_lock=1
  apt_lock_message=alternate
fi
if [ -f "${ROOT}/state/apt-lock-docker-drift" ] && [ "\$1" = install ]; then
  case " \$* " in
    *" docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras "*)
      if [ ! -f "${ROOT}/state/apt-lock-docker-drift-injected" ]; then
        ${real_touch} "${ROOT}/state/apt-lock-docker-drift-waiting"
        apt_lock=1
      fi
      ;;
  esac
fi
if [ -f "${ROOT}/state/apt-lock-always" ]; then
  apt_lock=1
elif [ -f "${ROOT}/state/apt-lock-calls" ]; then
  lock_calls=\$(${real_cat} "${ROOT}/state/apt-lock-calls")
  case " \$lock_calls " in
    *" \$apt_call_number "*) apt_lock=1 ;;
  esac
fi
if [ "\$apt_lock" -eq 1 ]; then
  if [ "\$apt_lock_message" = alternate ]; then
    printf 'E: Unable to lock the administration directory (/var/lib/dpkg/), is another process using it?\n' >&2
  else
    case "\$1" in
      update)
        printf 'E: Could not get lock /var/lib/apt/lists/lock. It is held by process 1234 (apt-get)\n' >&2
        ;;
      install)
        printf 'E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 1234 (apt-get)\n' >&2
        ;;
    esac
  fi
  exit 100
fi
if [ -f "${ROOT}/state/apt-fail" ]; then
  printf 'E: Failed to fetch package indexes from the configured mirrors\n' >&2
  exit 1
fi
if [ -f "${ROOT}/state/apt-lock-open-fail" ]; then
  printf 'E: Could not get lock /var/lib/apt/lists/lock: Permission denied\n' >&2
  exit 100
fi
if [ "\$1" = update ]; then
  exit 0
fi
if [ "\$1" = install ]; then
  shift
  ${real_mkdir} -p "${ROOT}/state/pkg"
  for p in "\$@"; do
    case "\$p" in
      -*) ;;
      *) ${real_touch} "${ROOT}/state/pkg/\$p" ;;
    esac
  done
  exit 0
fi
if [ "\$1" = remove ]; then
  printf 'apt-get remove is forbidden in governed tests\\n' >&2
  exit 1
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/sleep" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd sleep "\$*"
if [ -f "${ROOT}/state/apt-lock-docker-drift-waiting" ] \
  && [ ! -f "${ROOT}/state/apt-lock-docker-drift-injected" ]; then
  ${real_mkdir} -p "${ROOT}/state/pkg"
  ${real_touch} "${ROOT}/state/pkg/docker.io"
  ${real_touch} "${ROOT}/state/apt-lock-docker-drift-injected"
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/systemctl" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd systemctl "\$*"
user_mode=0
now=0
action=""
units=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    --user) user_mode=1; shift ;;
    --now) now=1; shift ;;
    --quiet|-q) shift ;;
    is-active|is-enabled|mask|unmask|disable|enable|start|stop|daemon-reload|show)
      action=\$1
      shift
      ;;
    --property=*)
      shift
      ;;
    *)
      units="\$units \$1"
      shift
      ;;
  esac
done
norm() {
  u=\$1
  case "\$u" in
    *.service|*.socket) printf '%s\\n' "\$u" ;;
    docker) printf 'docker.service\\n' ;;
    *) printf '%s.service\\n' "\$u" ;;
  esac
}
set -- \$units
case "\$action" in
  is-active)
    u=\$(norm "\$1")
    if [ "\$user_mode" -eq 0 ] && [ -f "${ROOT}/state/active/\$u" ]; then
      printf 'active\\n'
      exit 0
    fi
    printf 'inactive\\n'
    exit 3
    ;;
  is-enabled)
    u=\$(norm "\$1")
    if [ "\$user_mode" -eq 0 ] && [ -f "${ROOT}/state/masked/\$u" ]; then
      printf 'masked\\n'
      exit 1
    fi
    if [ "\$user_mode" -eq 0 ] && [ -f "${ROOT}/state/enabled/\$u" ]; then
      printf 'enabled\\n'
      exit 0
    fi
    printf 'disabled\\n'
    exit 1
    ;;
  mask)
    if [ -f "${ROOT}/state/mask-fail" ]; then
      printf 'Failed to mask unit\\n' >&2
      exit 1
    fi
    for u in "\$@"; do
      n=\$(norm "\$u")
      ${real_touch} "${ROOT}/state/masked/\$n"
      ${real_rm} -f "${ROOT}/state/active/\$n" "${ROOT}/state/enabled/\$n"
    done
    exit 0
    ;;
  unmask)
    for u in "\$@"; do
      n=\$(norm "\$u")
      ${real_rm} -f "${ROOT}/state/masked/\$n"
    done
    exit 0
    ;;
  disable)
    for u in "\$@"; do
      n=\$(norm "\$u")
      ${real_rm} -f "${ROOT}/state/enabled/\$n"
      if [ "\$now" -eq 1 ]; then
        ${real_rm} -f "${ROOT}/state/active/\$n"
      fi
    done
    exit 0
    ;;
  enable)
    if [ "\$user_mode" -eq 1 ]; then
      ${real_touch} "${ROOT}/state/user-docker-enabled"
      exit 0
    fi
    for u in "\$@"; do
      n=\$(norm "\$u")
      ${real_touch} "${ROOT}/state/enabled/\$n"
      if [ "\$now" -eq 1 ]; then
        ${real_touch} "${ROOT}/state/active/\$n"
      fi
    done
    exit 0
    ;;
  start)
    if [ "\$user_mode" -eq 1 ]; then
      ${real_touch} "${ROOT}/state/user-docker-started"
      exit 0
    fi
    for u in "\$@"; do
      n=\$(norm "\$u")
      if [ "\$n" = tendril.service ]; then
        if [ -f "${ROOT}/state/fail-start-tendril-once" ]; then
          ${real_rm} -f "${ROOT}/state/fail-start-tendril-once"
          printf 'Failed to start tendril.service\\n' >&2
          exit 1
        fi
        if [ -f "${ROOT}/state/fail-start-tendril" ]; then
          printf 'Failed to start tendril.service\\n' >&2
          exit 1
        fi
      fi
      ${real_touch} "${ROOT}/state/active/\$n"
    done
    exit 0
    ;;
  stop)
    for u in "\$@"; do
      n=\$(norm "\$u")
      ${real_rm} -f "${ROOT}/state/active/\$n"
    done
    exit 0
    ;;
  daemon-reload)
    if [ -f "${ROOT}/state/fail-daemon-reload-once" ]; then
      ${real_rm} -f "${ROOT}/state/fail-daemon-reload-once"
      printf 'Failed to reload daemon\\n' >&2
      exit 1
    fi
    ${real_touch} "${ROOT}/state/daemon-reload"
    exit 0
    ;;
  show)
    u=\$(norm "\$1")
    if [ -f "${ROOT}/state/systemctl-show-\$u" ]; then
      ${real_cat} "${ROOT}/state/systemctl-show-\$u"
      exit 0
    fi
    etc="${HOSTFS}/etc/systemd/system/\$u"
    vendor="${HOSTFS}/usr/local/lib/systemd/system/\$u"
    if [ -f "\$etc" ]; then
      base="\$etc"
    elif [ -f "\$vendor" ]; then
      base="\$vendor"
    else
      exit 1
    fi
    keydir="${ROOT}/state/showparse-\$u"
    ${real_rm} -rf "\$keydir"
    ${real_mkdir} -p "\$keydir"
    parse_unit() {
      while IFS= read -r line || [ -n "\$line" ]; do
        case "\$line" in
          \[*) ;;
          \#*) ;;
          '') ;;
          *=*)
            k=\${line%%=*}
            v=\${line#*=}
            case "\$k" in
              Environment)
                printf '%s\\n' "\$v" >> "\$keydir/Environment.list"
                ;;
              ReadWritePaths)
                printf '%s\\n' "\$v" >> "\$keydir/ReadWritePaths.list"
                ;;
              *)
                printf '%s\\n' "\$v" > "\$keydir/\$k"
                ;;
            esac
            ;;
        esac
      done < "\$1"
    }
    parse_unit "\$base"
    dropdir="${HOSTFS}/etc/systemd/system/\${u}.d"
    if [ -d "\$dropdir" ]; then
      for f in "\$dropdir"/*.conf; do
        if [ -f "\$f" ]; then
          parse_unit "\$f"
        fi
      done
    fi
    for k in User Group WorkingDirectory StateDirectory StateDirectoryMode NoNewPrivileges PrivateTmp ProtectSystem ProtectKernelTunables ProtectControlGroups RestrictSUIDSGID; do
      if [ -f "\$keydir/\$k" ]; then
        printf '%s=%s\\n' "\$k" "\$(${real_cat} "\$keydir/\$k")"
      fi
    done
    if [ -f "\$keydir/ExecStart" ]; then
      es=\$(${real_cat} "\$keydir/ExecStart")
      case "\$es" in
        \{*)
          printf 'ExecStart=%s\\n' "\$es"
          ;;
        *)
          path=\${es%% *}
          printf 'ExecStart={ path=%s ; argv[]=%s ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\\n' "\$path" "\$es"
          ;;
      esac
    fi
    if [ -f "\$keydir/Environment.list" ]; then
      while IFS= read -r ev || [ -n "\$ev" ]; do
        [ -n "\$ev" ] || continue
        printf 'Environment=%s\\n' "\$ev"
      done < "\$keydir/Environment.list"
    fi
    if [ -f "\$keydir/ReadWritePaths.list" ]; then
      rwp=""
      while IFS= read -r p || [ -n "\$p" ]; do
        [ -n "\$p" ] || continue
        if [ -z "\$rwp" ]; then
          rwp=\$p
        else
          rwp="\$rwp \$p"
        fi
      done < "\$keydir/ReadWritePaths.list"
      printf 'ReadWritePaths=%s\\n' "\$rwp"
    fi
    exit 0
    ;;
esac
exit 0
EOF

  write_exec "${SHIM_DIR}/adduser" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd adduser "\$*"
user=""
for a in "\$@"; do
  case "\$a" in
    -*) ;;
    *) user=\$a ;;
  esac
done
if [ -z "\$user" ]; then
  exit 1
fi
if grep -q "^\$user:" "${HOSTFS}/etc/passwd"; then
  exit 1
fi
printf '%s:x:2001:2001:OpenTendril Stem:/home/%s:/bin/bash\\n' "\$user" "\$user" >> "${HOSTFS}/etc/passwd"
printf '%s:!:19600:0:99999:7:::\\n' "\$user" >> "${HOSTFS}/etc/shadow"
printf '%s:x:2001:\\n' "\$user" >> "${HOSTFS}/etc/group"
${real_mkdir} -p "${HOSTFS}/home/\$user"
set_owner "/home/\$user" "\$user"
if [ ! -f "${ROOT}/state/adduser-no-subid" ]; then
  printf '%s:165536:65536\\n' "\$user" >> "${HOSTFS}/etc/subuid"
  printf '%s:165536:65536\\n' "\$user" >> "${HOSTFS}/etc/subgid"
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/usermod" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd usermod "\$*"
user=""
for a in "\$@"; do
  case "\$a" in
    --add-subuids|--add-subgids) shift ;;
    -*) ;;
    *) user=\$a ;;
  esac
done
if [ -z "\$user" ]; then
  user=\${$#}
fi
if ! grep -q "^\$user:" "${HOSTFS}/etc/subuid"; then
  printf '%s:165536:65536\\n' "\$user" >> "${HOSTFS}/etc/subuid"
fi
if ! grep -q "^\$user:" "${HOSTFS}/etc/subgid"; then
  printf '%s:165536:65536\\n' "\$user" >> "${HOSTFS}/etc/subgid"
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/loginctl" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd loginctl "\$*"
if [ "\$1" = enable-linger ]; then
  ${real_touch} "${ROOT}/state/linger-\$2"
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/docker" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd docker "\$*"
case "\${DOCKER_HOST:-}" in
  unix:///run/user/*/docker.sock)
    if [ -f "${ROOT}/state/docker-tendril-info-fail" ]; then
      exit 1
    fi
    if [ -f "${ROOT}/state/docker-tendril-info" ]; then
      ${real_cat} "${ROOT}/state/docker-tendril-info"
      exit 0
    fi
    if [ -f "${ROOT}/state/docker-rootless-ready" ]; then
      printf '[name=seccomp,name=rootless,name=cgroupns]\\n'
      exit 0
    fi
    exit 1
    ;;
esac
if [ -f "${ROOT}/state/docker-info-fail" ]; then
  exit 1
fi
if [ -f "${ROOT}/state/docker-info" ]; then
  ${real_cat} "${ROOT}/state/docker-info"
  exit 0
fi
if [ -f "${ROOT}/state/docker-rootless-ready" ]; then
  printf '[name=seccomp,name=rootless,name=cgroupns]\\n'
  exit 0
fi
exit 1
EOF

  write_exec "${SHIM_DIR}/iptables" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd iptables "\$*"
if [ "\$1" = --version ]; then
  if [ -f "${ROOT}/state/iptables-version" ]; then
    ${real_cat} "${ROOT}/state/iptables-version"
    exit 0
  fi
  printf 'iptables v1.8.10 (nf_tables)\\n'
  exit 0
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/modprobe" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd modprobe "\$*"
mod=\$1
if [ -z "\$mod" ]; then
  exit 1
fi
if [ -f "${ROOT}/state/modprobe-fail" ]; then
  printf 'modprobe: FATAL: Module %s not found.\\n' "\$mod" >&2
  exit 1
fi
if [ -f "${ROOT}/state/modprobe-unverified" ]; then
  exit 0
fi
mods=\$(hostpath /proc/modules)
${real_mkdir} -p "\$(dirname "\$mods")"
if [ ! -f "\$mods" ]; then
  : >"\$mods"
fi
if ! grep -q "^\$mod " "\$mods" 2>/dev/null; then
  printf '%s 0 0 - Live 0x0\\n' "\$mod" >>"\$mods"
fi
${real_mkdir} -p "\$(hostpath /sys/module/\$mod)"
exit 0
EOF

  write_exec "${SHIM_DIR}/dockerd-rootless-setuptool.sh" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd dockerd-rootless-setuptool.sh "\$*"
for a in "\$@"; do
  case "\$a" in
    --skip-iptables)
      printf 'test forbidden: --skip-iptables\\n' >&2
      exit 1
      ;;
  esac
done
if [ -f "${ROOT}/state/docker-setup-fail" ]; then
  exit 1
fi
if [ "\$1" = install ]; then
  ${real_mkdir} -p "${HOSTFS}/run/user/2001"
  ${real_touch} "${ROOT}/state/docker-rootless-ready"
  exit 0
fi
exit 0
EOF

  write_exec "${SHIM_DIR}/sudo" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd sudo "\$*"
user=""
nonint=0
list=0
kill_ts=0
while [ \$# -gt 0 ]; do
  case "\$1" in
    -u) user=\$2; shift 2 ;;
    -U) shift 2 ;;
    -H|-n)
      if [ "\$1" = -n ]; then
        nonint=1
      fi
      shift
      ;;
    -l) list=1; shift ;;
    -K) kill_ts=1; shift ;;
    --) shift; break ;;
    *=*) export "\$1"; shift ;;
    -*) shift ;;
    *) break ;;
  esac
done
if [ "\$kill_ts" -eq 1 ]; then
  ${real_touch} "${ROOT}/state/sudo-k-\${user:-self}"
  exit 0
fi
if [ "\$list" -eq 1 ]; then
  if [ -f "${ROOT}/state/sudo-l-fail" ]; then
    printf 'sudo: unable to initialize policy plugin\\n' >&2
    exit 1
  fi
  ${real_cat} "${ROOT}/state/sudo-l"
  exit 0
fi
if [ "\$nonint" -eq 1 ] && [ "\$user" = tendril ]; then
  if [ -f "${ROOT}/state/passwordless-tendril" ]; then
    exec "\$@"
  fi
  printf 'sudo: a password is required\\n' >&2
  exit 1
fi
if [ \$# -eq 0 ]; then
  exit 0
fi
exec "\$@"
EOF

  write_exec "${SHIM_DIR}/visudo" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd visudo "\$*"
if [ "\$1" = -c ] && [ "\$2" = -f ]; then
  if [ ! -f "\$3" ]; then
    exit 1
  fi
  grep timestamp_timeout "\$3" >/dev/null || exit 1
  exit 0
fi
exit 0
EOF
}

run_governed_installer() {
  : >"${events_file}"
  setup_governed_shims
  local fixture_version="${PIN_VERSION#v}"
  local scan_arg scan_value scan_index
  local scan_args=("$@")
  for ((scan_index = 0; scan_index < ${#scan_args[@]}; scan_index++)); do
    scan_arg="${scan_args[scan_index]}"
    case "${scan_arg}" in
      --version=*)
        fixture_version="${scan_arg#--version=}"
        ;;
      --version)
        if [ $((scan_index + 1)) -lt ${#scan_args[@]} ]; then
          scan_value="${scan_args[scan_index + 1]}"
          fixture_version="${scan_value#v}"
          scan_index=$((scan_index + 1))
        fi
        ;;
    esac
  done
  if [ -z "${fixture_version}" ]; then
    fixture_version=0.3.13
  fi
  if [ -f "${ROOT}/fail-version" ]; then
    fixture_version=9.9.9
  fi
  local env_args=(
    env -i
    HOME="${HOSTFS}/root"
    PATH="${SHIM_DIR}"
    TMPDIR="${TMP_DIR}"
    LC_ALL=C
    FIXTURE_VERSION="${fixture_version}"
    UNAME_S="${UNAME_S}"
    UNAME_M="${UNAME_M}"
  )
  if [ -n "${WSL_DISTRO}" ]; then
    env_args+=(WSL_DISTRO_NAME="${WSL_DISTRO}")
  fi
  if [ -n "${PIN_VERSION}" ]; then
    env_args+=(OPENTENDRIL_VERSION="${PIN_VERSION}")
  fi
  if [ -n "${GOVERNED_SUDO_USER}" ]; then
    env_args+=(SUDO_USER="${GOVERNED_SUDO_USER}")
  fi
  set +e
  (
    cd "${ROOT}" || exit 1
    "${env_args[@]}" /bin/sh "${installer}" --governed "$@"
  ) >"${stdout_file}" 2>"${stderr_file}"
  status=$?
  set -e
}

run_governed_upgrade_installer() {
  : >"${events_file}"
  setup_governed_shims
  local fixture_version="${PIN_VERSION#v}"
  local scan_arg scan_value scan_index
  local scan_args=("$@")
  for ((scan_index = 0; scan_index < ${#scan_args[@]}; scan_index++)); do
    scan_arg="${scan_args[scan_index]}"
    case "${scan_arg}" in
      --version=*)
        fixture_version="${scan_arg#--version=}"
        ;;
      --version)
        if [ $((scan_index + 1)) -lt ${#scan_args[@]} ]; then
          scan_value="${scan_args[scan_index + 1]}"
          fixture_version="${scan_value#v}"
          scan_index=$((scan_index + 1))
        fi
        ;;
    esac
  done
  if [ -z "${fixture_version}" ]; then
    fixture_version=0.3.13
  fi
  if [ -f "${ROOT}/fail-version" ]; then
    fixture_version=9.9.9
  fi
  local env_args=(
    env -i
    HOME="${HOSTFS}/root"
    PATH="${SHIM_DIR}"
    TMPDIR="${TMP_DIR}"
    LC_ALL=C
    FIXTURE_VERSION="${fixture_version}"
    UNAME_S="${UNAME_S}"
    UNAME_M="${UNAME_M}"
  )
  if [ -n "${WSL_DISTRO}" ]; then
    env_args+=(WSL_DISTRO_NAME="${WSL_DISTRO}")
  fi
  if [ -n "${PIN_VERSION}" ]; then
    env_args+=(OPENTENDRIL_VERSION="${PIN_VERSION}")
  fi
  if [ -n "${GOVERNED_SUDO_USER}" ]; then
    env_args+=(SUDO_USER="${GOVERNED_SUDO_USER}")
  fi
  set +e
  (
    cd "${ROOT}" || exit 1
    "${env_args[@]}" /bin/sh "${installer}" --governed-upgrade "$@"
  ) >"${stdout_file}" 2>"${stderr_file}"
  status=$?
  set -e
}

write_tendril_unit_file() {
  local path=$1
  local uid=${2:-2001}
  mkdir -p "$(dirname "${path}")"
  cat >"${path}" <<EOF
[Unit]
Description=OpenTendril Stem
After=network-online.target

[Service]
User=tendril
Group=tendril
WorkingDirectory=/home/tendril
Environment=DOCKER_HOST=unix:///run/user/${uid}/docker.sock
Environment=XDG_RUNTIME_DIR=/run/user/${uid}
StateDirectory=opentendril-transport
StateDirectoryMode=0755
Environment=TENDRIL_LOCAL_SOCKET=/var/lib/opentendril-transport/stem.sock
ExecStart=/home/tendril/.local/bin/tendril serve
Restart=on-failure

NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/home/tendril /run/user/${uid}
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes

[Install]
WantedBy=multi-user.target
EOF
}

write_floor_systemctl_show() {
  local uid=${1:-2001}
  mkdir -p "${ROOT}/state"
  cat >"${ROOT}/state/systemctl-show-tendril.service" <<EOF
User=tendril
Group=tendril
WorkingDirectory=/home/tendril
ExecStart={ path=/home/tendril/.local/bin/tendril ; argv[]=/home/tendril/.local/bin/tendril serve ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }
Environment=DOCKER_HOST=unix:///run/user/${uid}/docker.sock XDG_RUNTIME_DIR=/run/user/${uid} TENDRIL_LOCAL_SOCKET=/var/lib/opentendril-transport/stem.sock
StateDirectory=opentendril-transport
StateDirectoryMode=0755
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
ReadWritePaths=/home/tendril /run/user/${uid}
EOF
}

place_old_stem_binary() {
  mkdir -p "${HOSTFS}/home/tendril/.local/bin" "${ROOT}/meta/owners"
  printf '#!/bin/sh\nprintf "tendril old\\n"\n' >"${HOSTFS}/home/tendril/.local/bin/tendril"
  chmod 0750 "${HOSTFS}/home/tendril/.local/bin/tendril"
  printf 'tendril\n' >"${ROOT}/meta/owners/%home%tendril%.local%bin%tendril"
}

place_old_mcp() {
  local user=${1:-alice}
  mkdir -p "${HOSTFS}/home/${user}/.local/bin" "${ROOT}/meta/owners"
  printf '#!/bin/sh\nprintf "tendril-mcp old\\n"\n' >"${HOSTFS}/home/${user}/.local/bin/tendril-mcp"
  chmod 0755 "${HOSTFS}/home/${user}/.local/bin/tendril-mcp"
  printf '%s\n' "${user}" >"${ROOT}/meta/owners/%home%${user}%.local%bin%tendril-mcp"
}

seed_durable_stem_state() {
  mkdir -p \
    "${HOSTFS}/home/tendril/.tendril/substrates/myrepo" \
    "${HOSTFS}/home/tendril/.tendril/workspaces" \
    "${HOSTFS}/home/tendril/.tendril/run-workspaces" \
    "${ROOT}/meta/owners"
  printf 'SENTINEL-ENV\n' >"${HOSTFS}/home/tendril/.env"
  printf 'SENTINEL-DOT\n' >"${HOSTFS}/home/tendril/.tendril/marker"
  printf 'SENTINEL-KEY\n' >"${HOSTFS}/home/tendril/.tendril/app.pem"
  printf 'SENTINEL-GRANT\n' >"${HOSTFS}/home/tendril/.tendril/grants.yaml"
  printf 'SENTINEL-CRED\n' >"${HOSTFS}/home/tendril/.tendril/credentials"
  printf 'SENTINEL-SUB\n' >"${HOSTFS}/home/tendril/substrates.yaml"
  printf 'SENTINEL-REPO\n' >"${HOSTFS}/home/tendril/.tendril/substrates/myrepo/HEAD"
  printf 'SENTINEL-WS\n' >"${HOSTFS}/home/tendril/.tendril/workspaces/ws1"
  printf 'SENTINEL-RW\n' >"${HOSTFS}/home/tendril/.tendril/run-workspaces/rw1"
  printf 'tendril\n' >"${ROOT}/meta/owners/%home%tendril"
  printf 'tendril\n' >"${ROOT}/meta/owners/%home%tendril%.env"
  printf 'tendril\n' >"${ROOT}/meta/owners/%home%tendril%.tendril"
}

assert_durable_untouched() {
  local name=$1
  if [ "$(cat "${HOSTFS}/home/tendril/.env")" = "SENTINEL-ENV" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.tendril/marker")" = "SENTINEL-DOT" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.tendril/app.pem")" = "SENTINEL-KEY" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.tendril/grants.yaml")" = "SENTINEL-GRANT" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.tendril/credentials")" = "SENTINEL-CRED" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/substrates.yaml")" = "SENTINEL-SUB" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.tendril/substrates/myrepo/HEAD")" = "SENTINEL-REPO" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.tendril/workspaces/ws1")" = "SENTINEL-WS" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.tendril/run-workspaces/rw1")" = "SENTINEL-RW" ]; then
    pass "${name}"
    return 0
  fi
  fail "${name}"
  return 1
}

assert_no_upgrade_bootstrap() {
  local name=$1
  if grep -Eq '^CMD (apt-get |adduser |usermod |loginctl |dockerd-rootless|visudo )' "${events_file}"; then
    fail "${name}: performed governed host-bootstrap operations" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if grep -Eq 'tendril init' "${events_file}"; then
    fail "${name}: ran tendril init" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if grep -Eq '^CMD systemctl (mask |unmask )docker' "${events_file}"; then
    fail "${name}: mutated Docker units" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  return 0
}

prepare_upgrade_rootless_runtime() {
  mkdir -p "${HOSTFS}/run/user/2001" "${ROOT}/meta/owners"
  printf 'tendril\n' >"${ROOT}/meta/owners/%run%user%2001"
  touch "${ROOT}/state/docker-rootless-ready"
}

prepare_upgrade_host() {
  preseed_tendril_user with-subid
  place_old_stem_binary
  seed_durable_stem_state
  write_tendril_unit_file "${HOSTFS}/usr/local/lib/systemd/system/tendril.service" 2001
  write_floor_systemctl_show 2001
  prepare_upgrade_rootless_runtime
}

assert_no_sudo_timestamp_mutation() {
  local name=$1
  if grep -Eq '^CMD sudo( .*)? -[Kk]( |$)' "${events_file}"; then
    fail "${name}: invalidated a sudo timestamp or credential cache" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if find "${ROOT}/state" -maxdepth 1 -name 'sudo-k-*' | grep -q .; then
    fail "${name}: sudo timestamp-kill marker was created"
    return 1
  fi
  return 0
}

assert_upgrade_preflight_unmutated() {
  local name=$1
  if ! assert_no_host_write "${name}"; then
    return 1
  fi
  if ! assert_no_upgrade_bootstrap "${name}"; then
    return 1
  fi
  if ! assert_no_sudo_timestamp_mutation "${name}"; then
    return 1
  fi
  if grep -q 'Attempting rollback' "${stderr_file}"; then
    fail "${name}: attempted rollback after a preflight failure"
    return 1
  fi
  if events_match '^CMD tar '; then
    fail "${name}: extracted the archive after a preflight failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  return 0
}

assert_no_host_mutation() {
  local name=$1
  if grep -Eq '^CMD (apt-get |adduser |usermod |loginctl |dockerd-rootless|visudo |systemctl )' "${events_file}"; then
    fail "${name}: privileged command ran after a pre-mutation failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if grep -Eq '^CMD install .*(/home/tendril|/etc/systemd|/etc/sudoers|/etc/apt)' "${events_file}"; then
    fail "${name}: install mutated host paths after a pre-mutation failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if [ -e "${HOSTFS}/home/tendril/.local/bin/tendril" ] || [ -e "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ]; then
    fail "${name}: placed executables after a pre-mutation failure"
    return 1
  fi
  return 0
}

assert_no_host_write() {
  local name=$1
  if grep -Eq '^CMD (apt-get |adduser |usermod |loginctl |dockerd-rootless|visudo )' "${events_file}"; then
    fail "${name}: write command ran after a closed preflight failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if grep -Eq '^CMD systemctl (mask |unmask |disable|enable |start |stop |daemon-reload)' "${events_file}"; then
    fail "${name}: systemctl write ran after a closed preflight failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if grep -Eq '^CMD systemctl --user ' "${events_file}"; then
    fail "${name}: user systemctl ran after a closed preflight failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if grep -Eq '^CMD install .*(/home/tendril|/etc/systemd|/etc/sudoers|/etc/apt)' "${events_file}"; then
    fail "${name}: install mutated host paths after a closed preflight failure" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  return 0
}

assert_governed_failure() {
  local name=$1
  if [ "${status}" -eq 0 ]; then
    fail "${name}: expected failure" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return 1
  fi
  if grep -q '^Posture:     GOVERNED$' "${stdout_file}" || grep -q '^Posture:   GOVERNED$' "${stdout_file}"; then
    fail "${name}: reported GOVERNED success" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return 1
  fi
  return 0
}

assert_no_apt_recovery_abuse() {
  local name=$1
  if grep -Eq 'rm .*(/var/lib/apt|/var/lib/dpkg).*lock|(^|[[:space:]])(kill|pkill|fuser)([[:space:]]|$)' "${installer}" \
    || grep -Eq '^CMD rm .*(/var/lib/apt|/var/lib/dpkg).*lock|^CMD (kill|pkill|fuser) ' "${events_file}"; then
    fail "${name}: recovery deleted a lock file or terminated a process" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  return 0
}

preseed_tendril_user() {
  if ! grep -q '^tendril:' "${HOSTFS}/etc/passwd"; then
    printf 'tendril:x:2001:2001:OpenTendril Stem:/home/tendril:/bin/bash\n' >>"${HOSTFS}/etc/passwd"
    printf 'tendril:!:19600:0:99999:7:::\n' >>"${HOSTFS}/etc/shadow"
    printf 'tendril:x:2001:\n' >>"${HOSTFS}/etc/group"
  fi
  mkdir -p "${HOSTFS}/home/tendril"
  mkdir -p "${ROOT}/meta/owners"
  printf 'tendril\n' >"${ROOT}/meta/owners/%home%tendril"
  if [ "${1:-}" = with-subid ]; then
    if ! grep -q '^tendril:' "${HOSTFS}/etc/subuid"; then
      printf 'tendril:165536:65536\n' >>"${HOSTFS}/etc/subuid"
      printf 'tendril:165536:65536\n' >>"${HOSTFS}/etc/subgid"
    fi
  fi
}

mark_docker_engine_packages() {
  touch \
    "${ROOT}/state/pkg/docker-ce" \
    "${ROOT}/state/pkg/docker-ce-cli" \
    "${ROOT}/state/pkg/containerd.io" \
    "${ROOT}/state/pkg/docker-ce-rootless-extras"
}

clear_loaded_netfilter() {
  mkdir -p "${HOSTFS}/lib/modules/6.8.0-generic"
  : >"${HOSTFS}/proc/modules"
  rm -rf "${HOSTFS}/sys/module/nf_tables" "${HOSTFS}/sys/module/ip_tables"
  : >"${HOSTFS}/lib/modules/6.8.0-generic/modules.builtin"
}

assert_no_post_netfilter_mutation() {
  local name=$1
  if events_match '^CMD adduser '; then
    fail "${name}: adduser ran"
    return 1
  fi
  if events_match 'CMD apt-get install .*docker-ce'; then
    fail "${name}: docker-ce was installed" "events=$(tr '\n' ' ' <"${events_file}")"
    return 1
  fi
  if events_match '^CMD dockerd-rootless'; then
    fail "${name}: rootless setup ran"
    return 1
  fi
  if [ -e "${HOSTFS}/home/tendril/.local/bin/tendril" ] || [ -e "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ]; then
    fail "${name}: binaries were placed"
    return 1
  fi
  if [ -e "${HOSTFS}/etc/systemd/system/tendril.service" ] \
    || [ -e "${HOSTFS}/usr/local/lib/systemd/system/tendril.service" ]; then
    fail "${name}: unit was installed"
    return 1
  fi
  if grep -q 'Posture:     GOVERNED' "${stdout_file}"; then
    fail "${name}: reported GOVERNED success"
    return 1
  fi
  if grep -q -- '--skip-iptables' "${events_file}" "${stderr_file}" "${stdout_file}"; then
    fail "${name}: mentioned or used --skip-iptables"
    return 1
  fi
  return 0
}

# --- governed cases ---------------------------------------------------------

new_governed_case
printf '1000\n' >"${ROOT}/euid"
run_governed_installer --pollinator-user alice
if assert_governed_failure "governed requires root (shimmed uid)"; then
  if grep -q 'requires root' "${stderr_file}"; then
    if assert_no_host_mutation "governed requires root (shimmed uid)"; then
      pass "governed requires root (shimmed uid)"
    fi
  else
    fail "governed requires root (shimmed uid): message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
run_governed_installer
if assert_governed_failure "direct root without Pollinator identity fails"; then
  if grep -q -- '--pollinator-user' "${stderr_file}"; then
    if assert_no_host_mutation "direct root without Pollinator identity fails"; then
      pass "direct root without Pollinator identity fails"
    fi
  else
    fail "direct root without Pollinator identity fails: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
run_governed_installer --pollinator-user root
if assert_governed_failure "root Pollinator rejected"; then
  if grep -qi 'cannot be root\|uid 0\|--pollinator-user' "${stderr_file}"; then
    if assert_no_host_mutation "root Pollinator rejected"; then
      pass "root Pollinator rejected"
    fi
  else
    fail "root Pollinator rejected: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
run_governed_installer --pollinator-user tendril
if assert_governed_failure "tendril Pollinator rejected"; then
  if grep -q 'cannot be tendril' "${stderr_file}"; then
    if assert_no_host_mutation "tendril Pollinator rejected"; then
      pass "tendril Pollinator rejected"
    fi
  else
    fail "tendril Pollinator rejected: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
run_governed_installer --pollinator-user nosuch
if assert_governed_failure "unknown Pollinator rejected"; then
  if grep -q "does not exist" "${stderr_file}"; then
    if assert_no_host_mutation "unknown Pollinator rejected"; then
      pass "unknown Pollinator rejected"
    fi
  else
    fail "unknown Pollinator rejected: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
WSL_DISTRO=Ubuntu
run_governed_installer --pollinator-user alice
if assert_governed_failure "WSL governed rejected before mutation"; then
  if grep -q 'does not support WSL' "${stderr_file}"; then
    if assert_no_host_mutation "WSL governed rejected before mutation"; then
      pass "WSL governed rejected before mutation"
    fi
  else
    fail "WSL governed rejected before mutation: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
UNAME_S=Darwin
UNAME_M=arm64
run_governed_installer --pollinator-user alice
if assert_governed_failure "Darwin governed rejected before mutation"; then
  if grep -qi 'Darwin\|requires Linux' "${stderr_file}"; then
    if assert_no_host_mutation "Darwin governed rejected before mutation"; then
      pass "Darwin governed rejected before mutation"
    fi
  else
    fail "Darwin governed rejected before mutation: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
write_os_release debian 12
run_governed_installer --pollinator-user alice
if assert_governed_failure "non-Ubuntu Linux governed rejected"; then
  if grep -q 'requires Ubuntu' "${stderr_file}"; then
    if assert_no_host_mutation "non-Ubuntu Linux governed rejected"; then
      pass "non-Ubuntu Linux governed rejected"
    fi
  else
    fail "non-Ubuntu Linux governed rejected: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
write_os_release ubuntu 22.04
run_governed_installer --pollinator-user alice
if assert_governed_failure "wrong Ubuntu release rejected"; then
  if grep -q '24.04' "${stderr_file}"; then
    if assert_no_host_mutation "wrong Ubuntu release rejected"; then
      pass "wrong Ubuntu release rejected"
    fi
  else
    fail "wrong Ubuntu release rejected: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
write_os_release ubuntu 26.04
run_governed_installer --pollinator-user alice
if assert_governed_failure "Ubuntu 26.04 fresh governed rejected before mutation"; then
  if grep -q '24.04' "${stderr_file}" && grep -q '26.04' "${stderr_file}"; then
    if assert_no_host_mutation "Ubuntu 26.04 fresh governed rejected before mutation"; then
      pass "Ubuntu 26.04 fresh governed rejected before mutation"
    fi
  else
    fail "Ubuntu 26.04 fresh governed rejected before mutation: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
UNAME_M=aarch64
run_governed_installer --pollinator-user alice
if assert_governed_failure "governed arm64 rejected"; then
  if grep -q 'amd64' "${stderr_file}"; then
    if assert_no_host_mutation "governed arm64 rejected"; then
      pass "governed arm64 rejected"
    fi
  else
    fail "governed arm64 rejected: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/active/docker.service"
run_governed_installer --pollinator-user alice
if assert_governed_failure "pre-existing active rootful docker.service fails before mutation"; then
  if grep -q 'docker.service is active' "${stderr_file}"; then
    if assert_no_host_write "pre-existing active rootful docker.service fails before mutation"; then
      pass "pre-existing active rootful docker.service fails before mutation"
    fi
  else
    fail "pre-existing active rootful docker.service fails before mutation: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/active/docker.socket"
run_governed_installer --pollinator-user alice
if assert_governed_failure "pre-existing rootful docker.socket fails before mutation"; then
  if grep -q 'docker.socket is active' "${stderr_file}"; then
    if assert_no_host_write "pre-existing rootful docker.socket fails before mutation"; then
      pass "pre-existing rootful docker.socket fails before mutation"
    fi
  else
    fail "pre-existing rootful docker.socket fails before mutation: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/pkg/docker.io"
run_governed_installer --pollinator-user alice
if assert_governed_failure "unsafe docker.io posture fails closed"; then
  if grep -q 'docker.io' "${stderr_file}"; then
    if assert_no_host_write "unsafe docker.io posture fails closed"; then
      pass "unsafe docker.io posture fails closed"
    fi
  else
    fail "unsafe docker.io posture fails closed: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
printf '[name=seccomp]\n' >"${ROOT}/state/docker-info"
run_governed_installer --pollinator-user alice
if assert_governed_failure "usable rootful Docker CLI fails closed"; then
  if grep -qi 'rootful' "${stderr_file}"; then
    if assert_no_host_write "usable rootful Docker CLI fails closed"; then
      pass "usable rootful Docker CLI fails closed"
    fi
  else
    fail "usable rootful Docker CLI fails closed: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/enabled/docker.service"
run_governed_installer --pollinator-user alice
if assert_governed_failure "enabled rootful docker.service fails closed"; then
  if grep -qi 'enabled' "${stderr_file}"; then
    if assert_no_host_write "enabled rootful docker.service fails closed"; then
      pass "enabled rootful docker.service fails closed"
    fi
  else
    fail "enabled rootful docker.service fails closed: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
printf 'tendril:x:2001:2001:OpenTendril Stem:/opt/tendril:/bin/bash\n' >>"${HOSTFS}/etc/passwd"
printf 'tendril:!:19600:0:99999:7:::\n' >>"${HOSTFS}/etc/shadow"
run_governed_installer --pollinator-user alice
if assert_governed_failure "conflicting tendril home fails rather than rewrite"; then
  if grep -q '/opt/tendril' "${stderr_file}"; then
    if ! events_match '^CMD adduser '; then
      if ! events_match '^CMD usermod '; then
        pass "conflicting tendril home fails rather than rewrite"
      else
        fail "conflicting tendril home fails rather than rewrite: usermod ran"
      fi
    else
      fail "conflicting tendril home fails rather than rewrite: adduser ran"
    fi
  else
    fail "conflicting tendril home fails rather than rewrite: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
printf 'tendril:x:110:110:OpenTendril Stem:/home/tendril:/usr/sbin/nologin\n' >>"${HOSTFS}/etc/passwd"
printf 'tendril:!:19600:0:99999:7:::\n' >>"${HOSTFS}/etc/shadow"
run_governed_installer --pollinator-user alice
if assert_governed_failure "conflicting tendril system uid fails rather than rewrite"; then
  if grep -q 'system account' "${stderr_file}"; then
    if ! events_match '^CMD adduser '; then
      pass "conflicting tendril system uid fails rather than rewrite"
    else
      fail "conflicting tendril system uid fails rather than rewrite: adduser ran"
    fi
  else
    fail "conflicting tendril system uid fails rather than rewrite: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
hash="$(file_hash "${FIXTURE_DIR}/bundle.tar.gz")"
cat >"${FIXTURE_DIR}/checksums.txt" <<EOF
0000000000000000000000000000000000000000000000000000000000000000  opentendril-linux-amd64.tar.gz
${hash}  opentendril-linux-arm64.tar.gz
EOF
run_governed_installer --pollinator-user alice
if assert_governed_failure "checksum failure prevents executable placement"; then
  if grep -q 'SHA-256 mismatch' "${stderr_file}"; then
    if [ ! -e "${HOSTFS}/home/tendril/.local/bin/tendril" ] && [ ! -e "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ]; then
      if ! events_match '^CMD tar '; then
        pass "checksum failure prevents executable placement"
      else
        fail "checksum failure prevents executable placement: tar ran"
      fi
    else
      fail "checksum failure prevents executable placement: binaries exist"
    fi
  else
    fail "checksum failure prevents executable placement: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/mask-fail"
run_governed_installer --pollinator-user alice
if assert_governed_failure "systemctl mask failure fails before Docker Engine packages"; then
  if events_match 'CMD apt-get install .*docker-ce'; then
    fail "systemctl mask failure fails before Docker Engine packages: docker-ce was installed" "events=$(tr '\n' ' ' <"${events_file}")"
  elif events_match '^CMD adduser '; then
    fail "systemctl mask failure fails before Docker Engine packages: adduser ran" "events=$(tr '\n' ' ' <"${events_file}")"
  elif events_match '^CMD dockerd-rootless'; then
    fail "systemctl mask failure fails before Docker Engine packages: rootless setup ran"
  elif events_match '^CMD visudo '; then
    fail "systemctl mask failure fails before Docker Engine packages: visudo ran"
  elif [ -e "${HOSTFS}/home/tendril/.local/bin/tendril" ] || [ -e "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ]; then
    fail "systemctl mask failure fails before Docker Engine packages: binaries were placed"
  elif [ -e "${HOSTFS}/etc/systemd/system/tendril.service" ]; then
    fail "systemctl mask failure fails before Docker Engine packages: unit was installed"
  elif grep -q 'Posture:     GOVERNED' "${stdout_file}"; then
    fail "systemctl mask failure fails before Docker Engine packages: reported GOVERNED success"
  else
    pass "systemctl mask failure fails before Docker Engine packages"
  fi
fi

new_governed_case
clear_loaded_netfilter
touch "${ROOT}/state/modprobe-fail"
run_governed_installer --pollinator-user alice
if assert_governed_failure "nft missing module and failed modprobe fails closed"; then
  if grep -qi 'failed to load kernel module nf_tables' "${stderr_file}"; then
    if assert_no_post_netfilter_mutation "nft missing module and failed modprobe fails closed"; then
      if events_match '^CMD modprobe nf_tables$'; then
        pass "nft missing module and failed modprobe fails closed"
      else
        fail "nft missing module and failed modprobe fails closed: did not invoke modprobe nf_tables" "events=$(tr '\n' ' ' <"${events_file}")"
      fi
    fi
  else
    fail "nft missing module and failed modprobe fails closed: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
clear_loaded_netfilter
touch "${ROOT}/state/modprobe-unverified"
run_governed_installer --pollinator-user alice
if assert_governed_failure "modprobe success without observable module fails closed"; then
  if grep -qi 'returned success but the module is not loaded' "${stderr_file}"; then
    if assert_no_post_netfilter_mutation "modprobe success without observable module fails closed"; then
      if events_match '^CMD modprobe nf_tables$'; then
        pass "modprobe success without observable module fails closed"
      else
        fail "modprobe success without observable module fails closed: did not invoke modprobe nf_tables" "events=$(tr '\n' ' ' <"${events_file}")"
      fi
    fi
  else
    fail "modprobe success without observable module fails closed: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/docker-setup-fail"
run_governed_installer --pollinator-user alice
if assert_governed_failure "Docker setup failure cannot leave rootful daemon active"; then
  if [ ! -f "${ROOT}/state/active/docker.service" ] && [ ! -f "${ROOT}/state/active/docker.socket" ]; then
    if events_match '^CMD systemctl mask docker.service'; then
      if ! grep -Eq '^CMD systemctl (enable |start )docker\.service' "${events_file}"; then
        pass "Docker setup failure cannot leave rootful daemon active"
      else
        fail "Docker setup failure cannot leave rootful daemon active: started/enabled rootful" "events=$(tr '\n' ' ' <"${events_file}")"
      fi
    else
      fail "Docker setup failure cannot leave rootful daemon active: did not mask" "events=$(tr '\n' ' ' <"${events_file}")"
    fi
  else
    fail "Docker setup failure cannot leave rootful daemon active: unit left active"
  fi
fi

# --- success path -----------------------------------------------------------

assert_governed_success_core() {
  local name=$1
  if [ "${status}" -ne 0 ]; then
    fail "${name}" "exit ${status}; stderr=$(tr '\n' ' ' <"${stderr_file}")"
    return 1
  fi
  if ! grep -q 'Posture:     GOVERNED' "${stdout_file}"; then
    fail "${name}: success output missing GOVERNED" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
    return 1
  fi
  if grep -q 'LOCAL / SINGLE-PRINCIPAL' "${stdout_file}"; then
    fail "${name}: governed success described as LOCAL"
    return 1
  fi
  return 0
}

# --- APT lock handling ------------------------------------------------------

new_governed_case
printf '1 3 5 7\n' >"${ROOT}/state/apt-lock-calls"
run_governed_installer --pollinator-user alice
if assert_governed_success_core "transient APT locks retry every governed operation"; then
  apt_events="$(grep '^CMD apt-get ' "${events_file}")"
  expected_apt_events='CMD apt-get update -o DPkg::Lock::Timeout=0
CMD apt-get update -o DPkg::Lock::Timeout=0
CMD apt-get install -y ca-certificates curl git uidmap slirp4netns dbus-user-session kmod iptables -o DPkg::Lock::Timeout=0
CMD apt-get install -y ca-certificates curl git uidmap slirp4netns dbus-user-session kmod iptables -o DPkg::Lock::Timeout=0
CMD apt-get update -o DPkg::Lock::Timeout=0
CMD apt-get update -o DPkg::Lock::Timeout=0
CMD apt-get install -y docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras -o DPkg::Lock::Timeout=0
CMD apt-get install -y docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras -o DPkg::Lock::Timeout=0'
  apt_calls="$(grep -c '^CMD apt-get ' "${events_file}" || true)"
  waiting_messages="$(grep -c 'another package-manager operation is active' "${stderr_file}" || true)"
  sleep_calls="$(grep -c '^CMD sleep 2$' "${events_file}" || true)"
  if [ "${apt_events}" = "${expected_apt_events}" ] \
    && [ "${apt_calls}" -eq 8 ] \
    && [ "${waiting_messages}" -eq 4 ] \
    && [ "${sleep_calls}" -eq 4 ]; then
    pass "transient APT locks retry every governed operation"
  else
    fail "transient APT locks retry every governed operation" "apt_calls=${apt_calls} waiting_messages=${waiting_messages} sleep_calls=${sleep_calls} events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if assert_no_apt_recovery_abuse "transient APT locks retry every governed operation"; then
    pass "transient APT locks do not delete locks or terminate processes"
  fi
fi

new_governed_case
touch "${ROOT}/state/apt-lock-always"
run_governed_installer --pollinator-user alice
if assert_governed_failure "persistent APT lock contention is bounded"; then
  apt_calls="$(grep -c '^CMD apt-get ' "${events_file}" || true)"
  sleep_calls="$(grep -c '^CMD sleep 2$' "${events_file}" || true)"
  if [ "${apt_calls}" -eq 31 ] && [ "${sleep_calls}" -eq 30 ]; then
    pass "persistent APT lock contention is bounded"
  else
    fail "persistent APT lock contention is bounded" "apt_calls=${apt_calls} sleep_calls=${sleep_calls} events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if grep -q 'package manager remained busy' "${stderr_file}" \
    && grep -q 'after waiting 60 seconds' "${stderr_file}" \
    && grep -q 'rerun the governed installer' "${stderr_file}" \
    && ! grep -Eq 'delete|remove.*lock' "${stderr_file}"; then
    pass "persistent APT lock contention has an actionable diagnosis"
  else
    fail "persistent APT lock contention has an actionable diagnosis" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
  if assert_no_apt_recovery_abuse "persistent APT lock contention is bounded"; then
    pass "persistent APT locks do not trigger unsafe recovery"
  fi
fi

new_governed_case
touch "${ROOT}/state/apt-fail"
run_governed_installer --pollinator-user alice
if assert_governed_failure "ordinary APT failure is not retried"; then
  apt_calls="$(grep -c '^CMD apt-get ' "${events_file}" || true)"
  sleep_calls="$(grep -c '^CMD sleep 2$' "${events_file}" || true)"
  if [ "${apt_calls}" -eq 1 ] && [ "${sleep_calls}" -eq 0 ] \
    && grep -q 'Failed to fetch package indexes' "${stderr_file}" \
    && ! grep -q 'another package-manager operation is active' "${stderr_file}"; then
    pass "ordinary APT failure is not retried"
  else
    fail "ordinary APT failure is not retried" "apt_calls=${apt_calls} sleep_calls=${sleep_calls} stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
  if assert_no_apt_recovery_abuse "ordinary APT failure is not retried"; then
    pass "ordinary APT failure does not trigger unsafe recovery"
  fi
fi

new_governed_case
touch "${ROOT}/state/apt-lock-open-fail"
run_governed_installer --pollinator-user alice
if assert_governed_failure "non-contention lock failure is not retried"; then
  apt_calls="$(grep -c '^CMD apt-get ' "${events_file}" || true)"
  sleep_calls="$(grep -c '^CMD sleep 2$' "${events_file}" || true)"
  if [ "${apt_calls}" -eq 1 ] && [ "${sleep_calls}" -eq 0 ] \
    && grep -q 'Permission denied' "${stderr_file}" \
    && ! grep -q 'another package-manager operation is active' "${stderr_file}"; then
    pass "non-contention lock failure is not retried"
  else
    fail "non-contention lock failure is not retried" "apt_calls=${apt_calls} sleep_calls=${sleep_calls} stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
  if assert_no_apt_recovery_abuse "non-contention lock failure is not retried"; then
    pass "non-contention lock failure does not trigger unsafe recovery"
  fi
fi

new_governed_case
touch "${ROOT}/state/apt-lock-alternate"
run_governed_installer --pollinator-user alice
if assert_governed_success_core "alternate APT lock wording is retried"; then
  apt_calls="$(grep -c '^CMD apt-get ' "${events_file}" || true)"
  sleep_calls="$(grep -c '^CMD sleep 2$' "${events_file}" || true)"
  if [ "${apt_calls}" -eq 5 ] && [ "${sleep_calls}" -eq 1 ]; then
    pass "alternate APT lock wording is retried"
  else
    fail "alternate APT lock wording is retried" "apt_calls=${apt_calls} sleep_calls=${sleep_calls} events=$(tr '\n' ' ' <"${events_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/apt-lock-docker-drift"
run_governed_installer --pollinator-user alice
if assert_governed_failure "Docker posture is rechecked after an APT wait"; then
  docker_install_calls="$(grep -c '^CMD apt-get install .*docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras' "${events_file}" || true)"
  sleep_calls="$(grep -c '^CMD sleep 2$' "${events_file}" || true)"
  if grep -q 'package docker.io is installed' "${stderr_file}" \
    && grep -q 'another package-manager operation is active' "${stderr_file}" \
    && [ "${docker_install_calls}" -eq 1 ] \
    && [ "${sleep_calls}" -eq 1 ]; then
    pass "Docker posture is rechecked after an APT wait"
  else
    fail "Docker posture is rechecked after an APT wait" "docker_install_calls=${docker_install_calls} sleep_calls=${sleep_calls} stderr=$(tr '\n' ' ' <"${stderr_file}") events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if assert_no_apt_recovery_abuse "Docker posture is rechecked after an APT wait"; then
    pass "APT wait does not weaken Docker safety checks"
  fi
fi

new_governed_case
run_governed_installer --pollinator-user alice --version v0.3.0
if assert_governed_success_core "clean Ubuntu governed bootstrap"; then
  pass "explicit --pollinator-user defines the Pollinator account"
  if events_match 'CMD curl https://github.com/opentendril/opentendril/releases/download/v0.3.0/opentendril-linux-amd64.tar.gz'; then
    pass "governed --version composes with release pinning"
  else
    fail "governed --version composes with release pinning" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if events_match '^CMD adduser .* tendril$'; then
    pass "clean host creates tendril"
  else
    fail "clean host creates tendril" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if grep -q '^tendril:x:2001:2001:' "${HOSTFS}/etc/passwd" && grep -q '^tendril:!' "${HOSTFS}/etc/shadow"; then
    pass "tendril has intended home/login posture"
  else
    fail "tendril has intended home/login posture" "passwd=$(tr '\n' ' ' <"${HOSTFS}/etc/passwd")"
  fi
  if grep -q '^tendril:' "${HOSTFS}/etc/subuid" && grep -q '^tendril:' "${HOSTFS}/etc/subgid" && [ -f "${ROOT}/state/linger-tendril" ]; then
    pass "subordinate UID/GID and linger setup occur"
  else
    fail "subordinate UID/GID and linger setup occur"
  fi
  if events_match '^CMD dockerd-rootless-setuptool.sh install'; then
    pass "tendril rootless setup occurs"
  else
    fail "tendril rootless setup occurs" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if [ ! -f "${ROOT}/state/active/docker.service" ] && [ ! -f "${ROOT}/state/active/docker.socket" ]; then
    pass "rootful units are not left active after successful bootstrap"
  else
    fail "rootful units are not left active after successful bootstrap"
  fi
  if grep -Eq '^CMD systemctl (enable |start )docker\.service' "${events_file}"; then
    fail "success enabled or started rootful docker.service" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "success does not enable or start rootful docker.service"
  fi
  stem="${HOSTFS}/home/tendril/.local/bin/tendril"
  mcp="${HOSTFS}/home/alice/.local/bin/tendril-mcp"
  pollinator_tendril="${HOSTFS}/home/alice/.local/bin/tendril"
  if [ -f "${stem}" ] && "${stem}" --version | grep -Eq '^tendril [0-9]+\.[0-9]+\.[0-9]+$'; then
    pass "Stem gets only protected tendril"
  else
    fail "Stem gets only protected tendril"
  fi
  if [ -f "${mcp}" ] && "${mcp}" --version | grep -Eq '^tendril-mcp [0-9]+\.[0-9]+\.[0-9]+$'; then
    pass "Pollinator gets only tendril-mcp"
  else
    fail "Pollinator gets only tendril-mcp"
  fi
  if [ ! -e "${pollinator_tendril}" ]; then
    pass "full tendril never reaches Pollinator destination"
  else
    fail "full tendril never reaches Pollinator destination"
  fi
  if events_match '^CMD install -o tendril -g tendril -m 0750 .* /home/tendril/.local/bin/tendril$'; then
    pass "Stem ownership/mode are requested correctly"
  else
    fail "Stem ownership/mode are requested correctly" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if events_match '^CMD install -o alice -g alice -m 0755 .* /home/alice/.local/bin/tendril-mcp$'; then
    pass "Pollinator ownership/mode are requested correctly"
  else
    fail "Pollinator ownership/mode are requested correctly" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if events_match '^CMD sudo .* -K' || events_match '^CMD sudo -u alice sudo -K'; then
    pass "cached sudo state is invalidated"
  else
    # the logged command is the full argv
    if grep -Eq '^CMD sudo .* -K' "${events_file}"; then
      pass "cached sudo state is invalidated"
    else
      fail "cached sudo state is invalidated" "events=$(tr '\n' ' ' <"${events_file}")"
    fi
  fi
  if grep -Eq '^CMD sudo .* -n -u tendril true' "${events_file}"; then
    pass "accepted posture proves sudo -n escalation does not work"
  else
    fail "accepted posture proves sudo -n escalation does not work" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  unit="${HOSTFS}/usr/local/lib/systemd/system/tendril.service"
  if [ -f "${unit}" ]; then
    if grep -q 'WorkingDirectory=/home/tendril' "${unit}" \
      && grep -q 'ExecStart=/home/tendril/.local/bin/tendril serve' "${unit}" \
      && grep -q 'DOCKER_HOST=unix:///run/user/2001/docker.sock' "${unit}" \
      && grep -q 'XDG_RUNTIME_DIR=/run/user/2001' "${unit}" \
      && grep -q 'TENDRIL_LOCAL_SOCKET=/var/lib/opentendril-transport/stem.sock' "${unit}" \
      && grep -q 'StateDirectory=opentendril-transport' "${unit}" \
      && grep -q 'User=tendril' "${unit}" \
      && grep -q 'Group=tendril' "${unit}"; then
      if grep -q '/run/user/1001' "${unit}"; then
        fail "generated unit hard-codes uid 1001"
      else
        pass "generated unit uses actual tendril UID and protected ExecStart"
      fi
    else
      fail "generated unit uses actual tendril UID and protected ExecStart" "unit=$(tr '\n' ' ' <"${unit}")"
    fi
  else
    fail "generated unit uses actual tendril UID and protected ExecStart: missing unit"
  fi
  if [ -e "${HOSTFS}/etc/systemd/system/tendril.service" ]; then
    fail "fresh install created an OpenTendril base unit under /etc/systemd/system"
  else
    pass "fresh install does not create an OpenTendril base unit under /etc/systemd/system"
  fi
  if grep -Eq '^CMD systemctl (enable |start )tendril' "${events_file}"; then
    fail "unit is started or enabled before configuration" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "unit is not started before configuration"
  fi
  if grep -q 'systemctl enable --now tendril' "${stdout_file}" \
    && grep -q 'cd /home/tendril' "${stdout_file}" \
    && grep -q 'Interactive login as tendril is not required' "${stdout_file}"; then
    pass "next-steps print Stem commands from /home/tendril without auto-init"
  else
    fail "next-steps print Stem commands from /home/tendril without auto-init" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
  fi
  mask_line="$(grep -n '^CMD systemctl mask docker.service' "${events_file}" | head -n1 | cut -d: -f1 || true)"
  apt_line="$(grep -n '^CMD apt-get install .*docker-ce' "${events_file}" | head -n1 | cut -d: -f1 || true)"
  if [ -n "${mask_line}" ] && [ -n "${apt_line}" ] && [ "${mask_line}" -lt "${apt_line}" ]; then
    pass "rootful units are masked before Docker Engine packages"
  else
    fail "rootful units are masked before Docker Engine packages" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if events_match 'CMD curl https://download.docker.com/linux/ubuntu/gpg'; then
    pass "Docker bootstrap uses the official Docker GPG URL"
  else
    fail "Docker bootstrap uses the official Docker GPG URL" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if [ -f "${HOSTFS}/home/tendril/.env" ] && [ -d "${HOSTFS}/home/tendril/.tendril" ]; then
    pass "control-plane scaffolding is created when absent"
  else
    fail "control-plane scaffolding is created when absent"
  fi
  if events_match 'CMD apt-get install .*kmod' && events_match 'CMD apt-get install .*iptables'; then
    pass "kmod and iptables are installed as netfilter prerequisites"
  else
    fail "kmod and iptables are installed as netfilter prerequisites" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if grep -q -- '--skip-iptables' "${events_file}"; then
    fail "clean bootstrap used --skip-iptables" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "clean bootstrap does not pass --skip-iptables"
  fi
fi

# --- netfilter prerequisite -------------------------------------------------

new_governed_case
clear_loaded_netfilter
run_governed_installer --pollinator-user alice
if assert_governed_success_core "nft missing module is loaded via modprobe"; then
  if events_match '^CMD modprobe nf_tables$'; then
    if grep -q '^nf_tables ' "${HOSTFS}/proc/modules" && [ -d "${HOSTFS}/sys/module/nf_tables" ]; then
      pass "nft missing module is loaded via modprobe"
    else
      fail "nft missing module is loaded via modprobe: module state not observed after load" "modules=$(tr '\n' ' ' <"${HOSTFS}/proc/modules")"
    fi
  else
    fail "nft missing module is loaded via modprobe: did not invoke modprobe nf_tables" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if grep -q -- '--skip-iptables' "${events_file}"; then
    fail "nft missing-module path used --skip-iptables"
  fi
fi

new_governed_case
run_governed_installer --pollinator-user alice
if assert_governed_success_core "already-loaded nf_tables is accepted"; then
  if events_match '^CMD modprobe '; then
    fail "already-loaded nf_tables is accepted: unnecessary modprobe" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "already-loaded nf_tables is accepted"
  fi
fi

new_governed_case
clear_loaded_netfilter
printf 'kernel/net/netfilter/nf_tables.ko\n' >"${HOSTFS}/lib/modules/6.8.0-generic/modules.builtin"
run_governed_installer --pollinator-user alice
if assert_governed_success_core "builtin nf_tables is accepted without modprobe"; then
  if events_match '^CMD modprobe '; then
    fail "builtin nf_tables is accepted without modprobe: modprobe ran" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "builtin nf_tables is accepted without modprobe"
  fi
fi

new_governed_case
clear_loaded_netfilter
printf 'iptables v1.8.10 (legacy)\n' >"${ROOT}/state/iptables-version"
run_governed_installer --pollinator-user alice
if assert_governed_success_core "legacy iptables selects ip_tables"; then
  if events_match '^CMD modprobe ip_tables$' && ! events_match '^CMD modprobe nf_tables$'; then
    pass "legacy iptables selects ip_tables"
  else
    fail "legacy iptables selects ip_tables" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
fi

if grep -q -- '--skip-iptables' "${installer}"; then
  fail "installer source never uses --skip-iptables"
else
  pass "installer source never uses --skip-iptables"
fi

upgrade_privilege_preflight="$(awk '/^inspect_pollinator_privilege_readonly\(\)/,/^}$/' "${installer}")"
if [ -z "${upgrade_privilege_preflight}" ]; then
  fail "governed-upgrade privilege preflight is defined"
elif printf '%s\n' "${upgrade_privilege_preflight}" | grep -Eq -- '(^|[[:space:]])-[Kk]([[:space:]]|$)'; then
  fail "governed-upgrade privilege preflight does not use sudo -K/-k" "preflight=$(printf '%s' "${upgrade_privilege_preflight}" | tr '\n' ' ')"
elif ! printf '%s\n' "${upgrade_privilege_preflight}" | grep -q -- 'sudo -l -U'; then
  fail "governed-upgrade privilege preflight keeps read-only sudo -l -U"
elif ! printf '%s\n' "${upgrade_privilege_preflight}" | grep -q -- 'sudo -n -u'; then
  fail "governed-upgrade privilege preflight keeps the sudo -n probe"
else
  pass "governed-upgrade privilege preflight is observational"
fi

new_governed_case
GOVERNED_SUDO_USER=alice
run_governed_installer
if assert_governed_success_core "SUDO_USER resolves a valid Pollinator"; then
  if [ -f "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ]; then
    pass "SUDO_USER resolves a valid Pollinator"
  else
    fail "SUDO_USER resolves a valid Pollinator: missing tendril-mcp"
  fi
fi

new_governed_case
printf 'bob:x:1001:1001:Bob:/home/bob:/bin/bash\n' >>"${HOSTFS}/etc/passwd"
printf 'bob:!:19600:0:99999:7:::\n' >>"${HOSTFS}/etc/shadow"
mkdir -p "${HOSTFS}/home/bob"
printf 'bob\n' >"${ROOT}/meta/owners/%home%bob"
GOVERNED_SUDO_USER=bob
run_governed_installer --pollinator-user alice
if assert_governed_success_core "explicit --pollinator-user overrides SUDO_USER"; then
  if [ -f "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ] && [ ! -e "${HOSTFS}/home/bob/.local/bin/tendril-mcp" ]; then
    pass "explicit --pollinator-user overrides SUDO_USER"
  else
    fail "explicit --pollinator-user overrides SUDO_USER"
  fi
fi

new_governed_case
preseed_tendril_user with-subid
run_governed_installer --pollinator-user alice
if assert_governed_success_core "existing compatible tendril user is accepted"; then
  if events_match '^CMD adduser '; then
    fail "existing compatible tendril user is accepted: adduser ran"
  else
    pass "existing compatible tendril user is accepted"
  fi
fi

new_governed_case
preseed_tendril_user
run_governed_installer --pollinator-user alice
if assert_governed_success_core "existing tendril without subids gets usermod"; then
  if events_match '^CMD usermod '; then
    pass "existing tendril without subids gets usermod"
  else
    fail "existing tendril without subids gets usermod" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
fi

new_governed_case
mark_docker_engine_packages
run_governed_installer --pollinator-user alice
if assert_governed_success_core "installer-compatible docker-ce packages are not rejected"; then
  pass "installer-compatible docker-ce packages are not rejected"
  if grep -Eq '^CMD systemctl (enable |start )docker\.service' "${events_file}"; then
    fail "compatible package run enabled rootful Docker"
  else
    pass "compatible package run does not enable rootful Docker"
  fi
fi

new_governed_case
mkdir -p "${HOSTFS}/home/alice/.local/bin"
printf 'leftover-tendril\n' >"${HOSTFS}/home/alice/.local/bin/tendril"
run_governed_installer --pollinator-user alice
if assert_governed_failure "pre-existing Pollinator tendril is diagnosed not deleted"; then
  if [ "$(cat "${HOSTFS}/home/alice/.local/bin/tendril")" = "leftover-tendril" ]; then
    pass "pre-existing Pollinator tendril is diagnosed not deleted"
  else
    fail "pre-existing Pollinator tendril is diagnosed not deleted: file changed"
  fi
fi

new_governed_case
cat >"${ROOT}/state/sudo-l" <<'EOF'
User alice may run the following commands on this host:
    (ALL) NOPASSWD: ALL
EOF
touch "${ROOT}/state/passwordless-tendril"
run_governed_installer --pollinator-user alice
if assert_governed_failure "passwordless Pollinator escalation fails governance"; then
  if grep -qi 'P2\|passwordless\|non-interactively' "${stderr_file}"; then
    pass "passwordless Pollinator escalation fails governance"
  else
    fail "passwordless Pollinator escalation fails governance: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
  if grep -q 'Posture:     GOVERNED' "${stdout_file}"; then
    fail "passwordless Pollinator escalation reported GOVERNED success"
  fi
fi

new_governed_case
cat >"${ROOT}/state/sudo-l" <<'EOF'
User alice may run the following commands on this host:
    (root) NOPASSWD: /bin/sh
EOF
run_governed_installer --pollinator-user alice
if assert_governed_failure "indirect root NOPASSWD fails P2"; then
  if grep -qi 'P2\|passwordless\|NOPASSWD\|root' "${stderr_file}"; then
    if grep -q 'Posture:     GOVERNED' "${stdout_file}"; then
      fail "indirect root NOPASSWD fails P2: reported GOVERNED success"
    else
      pass "indirect root NOPASSWD fails P2"
    fi
  else
    fail "indirect root NOPASSWD fails P2: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
touch "${ROOT}/state/sudo-l-fail"
run_governed_installer --pollinator-user alice
if assert_governed_failure "unreadable sudo policy fails P2 closed"; then
  if grep -qi 'sudo policy\|refusing to classify' "${stderr_file}"; then
    if grep -q 'Posture:     GOVERNED' "${stdout_file}"; then
      fail "unreadable sudo policy fails P2 closed: reported GOVERNED success"
    else
      pass "unreadable sudo policy fails P2 closed"
    fi
  else
    fail "unreadable sudo policy fails P2 closed: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
setup_governed_host
setup_governed_shims
write_exec "${SHIM_DIR}/docker" <<EOF
#!/bin/sh
. "${SHIM_DIR}/hostpath.lib"
logcmd docker "\$*"
case "\${DOCKER_HOST:-}" in
  unix:///run/user/*)
    printf '[name=seccomp]\\n'
    exit 0
    ;;
esac
exit 1
EOF
set +e
(
  cd "${ROOT}" || exit 1
  env -i HOME="${HOSTFS}/root" PATH="${SHIM_DIR}" TMPDIR="${TMP_DIR}" LC_ALL=C \
    UNAME_S=Linux UNAME_M=x86_64 \
    /bin/sh "${installer}" --governed --pollinator-user alice
) >"${stdout_file}" 2>"${stderr_file}"
status=$?
set -e
if assert_governed_failure "missing rootless result fails governance"; then
  if grep -qi 'not rootless' "${stderr_file}"; then
    pass "missing rootless result fails governance"
  else
    fail "missing rootless result fails governance: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

# --- rerun / idempotence ----------------------------------------------------

new_governed_case
run_governed_installer --pollinator-user alice
if [ "${status}" -ne 0 ]; then
  fail "rerun fixture first install" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
else
  printf 'SENTINEL-ENV\n' >"${HOSTFS}/home/tendril/.env"
  printf 'SENTINEL-DOT\n' >"${HOSTFS}/home/tendril/.tendril/marker"
  printf 'SENTINEL-KEY\n' >"${HOSTFS}/home/tendril/.tendril/app.pem"
  printf 'SENTINEL-GRANT\n' >"${HOSTFS}/home/tendril/.tendril/grants.yaml"
  printf 'SENTINEL-CRED\n' >"${HOSTFS}/home/tendril/.tendril/credentials"
  printf 'SENTINEL-SUB\n' >"${HOSTFS}/home/tendril/substrates.yaml"
  mkdir -p "${HOSTFS}/home/tendril/.tendril/substrates/myrepo"
  printf 'SENTINEL-REPO\n' >"${HOSTFS}/home/tendril/.tendril/substrates/myrepo/HEAD"
  mkdir -p "${HOSTFS}/home/tendril/.tendril/workspaces"
  printf 'SENTINEL-WS\n' >"${HOSTFS}/home/tendril/.tendril/workspaces/ws1"
  mkdir -p "${HOSTFS}/home/tendril/.tendril/run-workspaces"
  printf 'SENTINEL-RW\n' >"${HOSTFS}/home/tendril/.tendril/run-workspaces/rw1"
  run_governed_installer --pollinator-user alice
  if assert_governed_success_core "rerun completes without destructive recreation"; then
    pass "rerun completes without destructive recreation"
    if [ "$(cat "${HOSTFS}/home/tendril/.env")" = "SENTINEL-ENV" ]; then
      pass "existing .env sentinel preserved"
    else
      fail "existing .env sentinel preserved" "content=$(cat "${HOSTFS}/home/tendril/.env")"
    fi
    if [ "$(cat "${HOSTFS}/home/tendril/.tendril/marker")" = "SENTINEL-DOT" ]; then
      pass "existing .tendril sentinel preserved"
    else
      fail "existing .tendril sentinel preserved"
    fi
    if [ "$(cat "${HOSTFS}/home/tendril/.tendril/app.pem")" = "SENTINEL-KEY" ] \
      && [ "$(cat "${HOSTFS}/home/tendril/.tendril/grants.yaml")" = "SENTINEL-GRANT" ] \
      && [ "$(cat "${HOSTFS}/home/tendril/.tendril/credentials")" = "SENTINEL-CRED" ] \
      && [ "$(cat "${HOSTFS}/home/tendril/substrates.yaml")" = "SENTINEL-SUB" ] \
      && [ "$(cat "${HOSTFS}/home/tendril/.tendril/substrates/myrepo/HEAD")" = "SENTINEL-REPO" ] \
      && [ "$(cat "${HOSTFS}/home/tendril/.tendril/workspaces/ws1")" = "SENTINEL-WS" ] \
      && [ "$(cat "${HOSTFS}/home/tendril/.tendril/run-workspaces/rw1")" = "SENTINEL-RW" ]; then
      pass "credential/grant/Substrate/workspace fixtures preserved"
    else
      fail "credential/grant/Substrate/workspace fixtures preserved"
    fi
    if grep -Eq '^CMD systemctl (enable |start )docker\.service' "${events_file}"; then
      fail "rerun does not enable rootful Docker" "events=$(tr '\n' ' ' <"${events_file}")"
    else
      pass "rerun does not enable rootful Docker"
    fi
    if events_match '^CMD adduser '; then
      fail "rerun recreated the tendril account"
    fi
  fi
fi

# --- fresh install collision and effective validation -----------------------

new_governed_case
mkdir -p "${HOSTFS}/etc/systemd/system"
printf 'pre-existing-full-unit\n' >"${HOSTFS}/etc/systemd/system/tendril.service"
run_governed_installer --pollinator-user alice
if assert_governed_failure "fresh install rejects a pre-existing /etc full-unit collision"; then
  if grep -q -- '--governed-upgrade' "${stderr_file}" \
    && grep -q '/etc/systemd/system/tendril.service already exists' "${stderr_file}"; then
    if [ "$(cat "${HOSTFS}/etc/systemd/system/tendril.service")" = "pre-existing-full-unit" ]; then
      if assert_no_host_mutation "fresh install rejects a pre-existing /etc full-unit collision"; then
        pass "fresh install rejects a pre-existing /etc full-unit collision"
      fi
    else
      fail "fresh install rejects a pre-existing /etc full-unit collision: /etc unit was rewritten"
    fi
  else
    fail "fresh install rejects a pre-existing /etc full-unit collision: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
mkdir -p "${HOSTFS}/etc/systemd/system/tendril.service.d"
cat >"${HOSTFS}/etc/systemd/system/tendril.service.d/weaken.conf" <<'EOF'
[Service]
User=root
EOF
run_governed_installer --pollinator-user alice
if assert_governed_failure "fresh install effective-validation rejects a weakening drop-in"; then
  if grep -q 'effective service validation failed' "${stderr_file}"; then
    if [ -f "${HOSTFS}/etc/systemd/system/tendril.service.d/weaken.conf" ]; then
      pass "fresh install effective-validation rejects a weakening drop-in"
    else
      fail "fresh install effective-validation rejects a weakening drop-in: drop-in was removed"
    fi
  else
    fail "fresh install effective-validation rejects a weakening drop-in: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

# --- governed upgrade -------------------------------------------------------

new_governed_case
printf '1000\n' >"${ROOT}/euid"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'requires root' "${stderr_file}"; then
  pass "governed upgrade requires root"
else
  fail "governed upgrade requires root" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
run_governed_installer --governed-upgrade --pollinator-user alice
if assert_governed_failure "mutual exclusion is enforced in governed mode"; then
  if grep -q 'mutually exclusive' "${stderr_file}"; then
    pass "mutual-exclusion failure"
  else
    fail "mutual-exclusion failure: message" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
  fi
fi

new_governed_case
prepare_upgrade_host
run_governed_upgrade_installer
if [ "${status}" -ne 0 ] && grep -q -- '--pollinator-user' "${stderr_file}"; then
  pass "governed upgrade requires named --pollinator-user"
else
  fail "governed upgrade requires named --pollinator-user" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
GOVERNED_SUDO_USER=alice
run_governed_upgrade_installer
if [ "${status}" -ne 0 ] && grep -q -- '--pollinator-user' "${stderr_file}"; then
  pass "governed upgrade does not guess a Pollinator from SUDO_USER"
else
  fail "governed upgrade does not guess a Pollinator from SUDO_USER" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ]; then
  pass "named Pollinator selection"
  if events_match 'CMD curl https://github.com/opentendril/opentendril/releases/download/v0.3.0/opentendril-linux-amd64.tar.gz'; then
    pass "governed upgrade pins the requested release"
  else
    fail "governed upgrade pins the requested release" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if events_match '^CMD install -o tendril -g tendril -m 0750 .* /home/tendril/.local/bin/tendril$'; then
    pass "Stem binary remains owner tendril:tendril mode 0750"
  else
    fail "Stem binary remains owner tendril:tendril mode 0750" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
  if grep -q 'tendril-payload' "${HOSTFS}/home/tendril/.local/bin/tendril" \
    && ! grep -q 'tendril old' "${HOSTFS}/home/tendril/.local/bin/tendril"; then
    pass "existing release baseline upgrades in place"
  else
    fail "existing release baseline upgrades in place"
  fi
  if [ -e "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ]; then
    fail "MCP absence remains absence: MCP was created"
  else
    pass "MCP absent remains absent"
  fi
  if grep -q 'tendril-mcp: absent' "${stdout_file}"; then
    pass "MCP absence is reported"
  else
    fail "MCP absence is reported" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
  fi
  if assert_no_upgrade_bootstrap "no governed host-bootstrap operations during upgrade"; then
    pass "no governed host-bootstrap operations during upgrade"
  fi
  if assert_durable_untouched "durable .env, .tendril, credentials, grants and Substrates untouched"; then
    :
  fi
  if grep -Eq '^CMD systemctl (enable |disable).*tendril' "${events_file}"; then
    fail "enablement state is unchanged: enable/disable was invoked" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "enablement state is unchanged"
  fi
  if grep -q 'does not qualify this Ubuntu release for fresh governed installation' "${stdout_file}" \
    && grep -q 'Fresh governed installation remains Ubuntu 24.04 LTS' "${stdout_file}"; then
    pass "Ubuntu 24.04 governed upgrade does not expand the fresh-install matrix"
  else
    fail "Ubuntu 24.04 governed upgrade does not expand the fresh-install matrix" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
  fi
  if assert_no_sudo_timestamp_mutation "successful governed upgrade does not invalidate sudo timestamps"; then
    pass "successful governed upgrade does not invalidate sudo timestamps"
  fi
else
  fail "named Pollinator selection" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
place_old_mcp alice
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && grep -q 'mcp-payload' "${HOSTFS}/home/alice/.local/bin/tendril-mcp" \
  && ! grep -q 'tendril-mcp old' "${HOSTFS}/home/alice/.local/bin/tendril-mcp"; then
  pass "MCP present is upgraded at the named Pollinator path"
  if events_match '^CMD install -o alice -g alice -m 0755 .* /home/alice/.local/bin/tendril-mcp$'; then
    pass "MCP replacement uses the named Pollinator owner"
  else
    fail "MCP replacement uses the named Pollinator owner" "events=$(tr '\n' ' ' <"${events_file}")"
  fi
else
  fail "MCP present is upgraded at the named Pollinator path" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf 'bob:x:1001:1001:Bob:/home/bob:/bin/bash\n' >>"${HOSTFS}/etc/passwd"
printf 'bob:!:19600:0:99999:7:::\n' >>"${HOSTFS}/etc/shadow"
printf 'bob:x:1001:\n' >>"${HOSTFS}/etc/group"
mkdir -p "${HOSTFS}/home/bob"
printf 'bob\n' >"${ROOT}/meta/owners/%home%bob"
place_old_mcp bob
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ]; then
  if [ -e "${HOSTFS}/home/alice/.local/bin/tendril-mcp" ]; then
    fail "named Pollinator without MCP does not create MCP"
  elif ! grep -q 'tendril-mcp old' "${HOSTFS}/home/bob/.local/bin/tendril-mcp"; then
    fail "MCP at an unnamed account was mutated"
  else
    pass "MCP is upgraded only at the named Pollinator path"
  fi
else
  fail "MCP is upgraded only at the named Pollinator path" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
rm -f "${HOSTFS}/home/tendril/.local/bin/tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'protected Stem binary' "${stderr_file}"; then
  if events_match '^CMD tar '; then
    fail "missing protected Stem binary: mutated after failure" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "missing protected Stem binary"
  fi
else
  fail "missing protected Stem binary" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
preseed_tendril_user with-subid
place_old_stem_binary
seed_durable_stem_state
write_floor_systemctl_show 2001
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'neither /usr/local/lib/systemd/system/tendril.service nor /etc/systemd/system/tendril.service exists' "${stderr_file}"; then
  pass "governed upgrade fails if base unit is missing"
else
  fail "governed upgrade fails if base unit is missing" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
preseed_tendril_user with-subid
place_old_stem_binary
seed_durable_stem_state
write_tendril_unit_file "${HOSTFS}/etc/systemd/system/tendril.service" 2001
write_floor_systemctl_show 2001
prepare_upgrade_rootless_runtime
legacy_hash="$(file_hash "${HOSTFS}/etc/systemd/system/tendril.service")"
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && [ -f "${HOSTFS}/usr/local/lib/systemd/system/tendril.service" ] \
  && [ ! -e "${HOSTFS}/etc/systemd/system/tendril.service" ]; then
  pass "exact known legacy unit migrates"
  if grep -q 'Known legacy unit migrated' "${stdout_file}"; then
    pass "exact legacy migration is reported"
  else
    fail "exact legacy migration is reported" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
  fi
  if grep -q 'tendril-payload' "${HOSTFS}/home/tendril/.local/bin/tendril" \
    && ! grep -q 'tendril old' "${HOSTFS}/home/tendril/.local/bin/tendril"; then
    pass "legacy migration replaces the protected Stem binary"
  else
    fail "legacy migration replaces the protected Stem binary"
  fi
else
  fail "exact known legacy unit migrates" "status=${status} stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
preseed_tendril_user with-subid
place_old_stem_binary
seed_durable_stem_state
write_tendril_unit_file "${HOSTFS}/etc/systemd/system/tendril.service" 2001
printf 'x' >>"${HOSTFS}/etc/systemd/system/tendril.service"
write_floor_systemctl_show 2001
before_legacy="$(cat "${HOSTFS}/etc/systemd/system/tendril.service")"
before_stem="$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'fails closed before mutation' "${stderr_file}"; then
  if [ -e "${HOSTFS}/usr/local/lib/systemd/system/tendril.service" ]; then
    fail "one-byte legacy rejection: vendor unit was written"
  elif [ "$(cat "${HOSTFS}/etc/systemd/system/tendril.service")" != "${before_legacy}" ]; then
    fail "one-byte legacy rejection: /etc unit was mutated"
  elif [ "$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")" != "${before_stem}" ]; then
    fail "one-byte legacy rejection: Stem binary was mutated"
  elif events_match '^CMD (install |systemctl stop |systemctl start )'; then
    fail "one-byte legacy rejection: host mutation occurred" "events=$(tr '\n' ' ' <"${events_file}")"
  else
    pass "one-byte legacy rejection"
    pass "ambiguous /etc base unit fails closed"
  fi
else
  fail "one-byte legacy rejection" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
mkdir -p "${HOSTFS}/etc/systemd/system/tendril.service.d"
printf '[Service]\nEnvironment=CUSTOM_ADMIN=1\n' >"${HOSTFS}/etc/systemd/system/tendril.service.d/10-admin.conf"
dropin_hash="$(file_hash "${HOSTFS}/etc/systemd/system/tendril.service.d/10-admin.conf")"
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && [ "$(file_hash "${HOSTFS}/etc/systemd/system/tendril.service.d/10-admin.conf")" = "${dropin_hash}" ]; then
  pass "administrator drop-in preservation"
  if grep -q 'Administrator drop-ins preserved' "${stdout_file}"; then
    pass "administrator drop-ins are reported"
  else
    fail "administrator drop-ins are reported" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
  fi
else
  fail "administrator drop-in preservation" "status=${status} stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf 'SENTINEL-FULL-UNIT\n' >"${HOSTFS}/etc/systemd/system/tendril.service"
full_hash="$(file_hash "${HOSTFS}/etc/systemd/system/tendril.service")"
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && [ "$(file_hash "${HOSTFS}/etc/systemd/system/tendril.service")" = "${full_hash}" ] \
  && [ "$(cat "${HOSTFS}/etc/systemd/system/tendril.service")" = "SENTINEL-FULL-UNIT" ]; then
  pass "administrator full-unit preservation"
  if grep -q 'Administrator full-unit override preserved' "${stdout_file}"; then
    pass "administrator full-unit override is reported"
  else
    fail "administrator full-unit override is reported" "stdout=$(tr '\n' ' ' <"${stdout_file}")"
  fi
else
  fail "administrator full-unit preservation" "status=${status} stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
sed -i 's/User=tendril/User=root/' "${ROOT}/state/systemctl-show-tendril.service"
old_unit="$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")"
old_stem="$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'effective service validation failed' "${stderr_file}"; then
  pass "effective-property weakening"
  if [ "$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")" = "${old_unit}" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")" = "${old_stem}" ]; then
    if assert_upgrade_preflight_unmutated "weakened pre-upgrade effective service fails before mutation"; then
      pass "weakened pre-upgrade effective service fails before mutation"
    fi
  else
    fail "weakened pre-upgrade effective service fails before mutation: host files changed"
  fi
else
  fail "effective-property weakening" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
cat >"${ROOT}/state/systemctl-show-tendril.service" <<'EOF'
User=tendril
Group=tendril
WorkingDirectory=/home/tendril
ExecStart={ path=/home/tendril/.local/bin/tendril ; argv[]=/home/tendril/.local/bin/tendril serve ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }
Environment=DOCKER_HOST=unix:///run/user/2001/docker.sock XDG_RUNTIME_DIR=/run/user/2001 TENDRIL_LOCAL_SOCKET=/var/lib/opentendril-transport/stem.sock
StateDirectory=opentendril-transport
StateDirectoryMode=0755
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
ReadWritePaths=/home/tendril /run/user/2001 /tmp
EOF
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'ReadWritePaths' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "ReadWritePaths widening rejection"; then
    pass "ReadWritePaths widening rejection"
  fi
else
  fail "ReadWritePaths widening rejection" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
touch "${ROOT}/state/active/tendril.service"
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && events_match '^CMD systemctl stop tendril.service' \
  && events_match '^CMD systemctl start tendril.service' \
  && [ -f "${ROOT}/state/active/tendril.service" ]; then
  pass "active Stem is restarted and verified active"
else
  fail "active Stem is restarted and verified active" "status=${status} events=$(tr '\n' ' ' <"${events_file}") stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && ! events_match '^CMD systemctl start tendril.service' \
  && [ ! -f "${ROOT}/state/active/tendril.service" ]; then
  pass "inactive Stem remains inactive"
else
  fail "inactive Stem remains inactive" "status=${status} events=$(tr '\n' ' ' <"${events_file}")"
fi

new_governed_case
prepare_upgrade_host
touch "${ROOT}/state/enabled/tendril.service"
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && [ -f "${ROOT}/state/enabled/tendril.service" ] \
  && ! grep -Eq '^CMD systemctl (enable |disable).*tendril' "${events_file}"; then
  pass "enablement preservation"
else
  fail "enablement preservation" "status=${status} events=$(tr '\n' ' ' <"${events_file}")"
fi

new_governed_case
prepare_upgrade_host
old_unit="$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")"
old_stem="$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")"
touch "${ROOT}/state/fail-daemon-reload-once"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'daemon-reload failed' "${stderr_file}"; then
  if [ "$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")" = "${old_unit}" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")" = "${old_stem}" ]; then
    pass "service-definition failure rollback"
  else
    fail "service-definition failure rollback: prior state was not restored"
  fi
else
  fail "service-definition failure rollback" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
old_unit="$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")"
old_stem="$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")"
touch "${ROOT}/state/fail-install-tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'failed to replace the protected Stem binary' "${stderr_file}"; then
  if [ "$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")" = "${old_unit}" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")" = "${old_stem}" ]; then
    pass "Stem binary replacement failure rollback"
  else
    fail "Stem binary replacement failure rollback: prior state was not restored"
  fi
else
  fail "Stem binary replacement failure rollback" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
place_old_mcp alice
old_unit="$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")"
old_stem="$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")"
old_mcp="$(cat "${HOSTFS}/home/alice/.local/bin/tendril-mcp")"
touch "${ROOT}/state/fail-install-mcp"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'failed to replace tendril-mcp' "${stderr_file}"; then
  if [ "$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")" = "${old_unit}" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")" = "${old_stem}" ] \
    && [ "$(cat "${HOSTFS}/home/alice/.local/bin/tendril-mcp")" = "${old_mcp}" ]; then
    pass "MCP replacement failure rollback"
  else
    fail "MCP replacement failure rollback: prior state was not restored"
  fi
else
  fail "MCP replacement failure rollback" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
place_old_mcp alice
old_unit="$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")"
old_stem="$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")"
old_mcp="$(cat "${HOSTFS}/home/alice/.local/bin/tendril-mcp")"
touch "${ROOT}/state/active/tendril.service"
touch "${ROOT}/state/fail-start-tendril-once"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'failed to restart tendril.service' "${stderr_file}"; then
  if [ "$(cat "${HOSTFS}/usr/local/lib/systemd/system/tendril.service")" = "${old_unit}" ] \
    && [ "$(cat "${HOSTFS}/home/tendril/.local/bin/tendril")" = "${old_stem}" ] \
    && [ "$(cat "${HOSTFS}/home/alice/.local/bin/tendril-mcp")" = "${old_mcp}" ] \
    && [ -f "${ROOT}/state/active/tendril.service" ]; then
    pass "restart failure rollback"
  else
    fail "restart failure rollback: prior state was not restored"
  fi
else
  fail "restart failure rollback" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
touch "${ROOT}/state/fail-install-tendril"
touch "${ROOT}/state/fail-rollback-cp"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] \
  && grep -q 'failed to replace the protected Stem binary' "${stderr_file}" \
  && grep -q 'rollback failed' "${stderr_file}" \
  && ! grep -q 'governed upgrade completed' "${stdout_file}"; then
  pass "rollback failure reporting"
else
  fail "rollback failure reporting" "status=${status} stderr=$(tr '\n' ' ' <"${stderr_file}") stdout=$(tr '\n' ' ' <"${stdout_file}")"
fi

new_governed_case
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'tendril account does not exist' "${stderr_file}"; then
  pass "governed upgrade fails if tendril principal is missing"
else
  fail "governed upgrade fails if tendril principal is missing" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
write_os_release ubuntu 26.04
run_governed_upgrade_installer --pollinator-user alice --version v0.3.0
if [ "${status}" -eq 0 ] \
  && [ -f "${HOSTFS}/usr/local/lib/systemd/system/tendril.service" ] \
  && grep -q 'tendril-payload' "${HOSTFS}/home/tendril/.local/bin/tendril" \
  && grep -q 'does not qualify this Ubuntu release for fresh governed installation' "${stdout_file}" \
  && grep -q 'Fresh governed installation remains Ubuntu 24.04 LTS' "${stdout_file}"; then
  pass "valid Ubuntu 26.04 existing governed upgrade"
  if assert_no_upgrade_bootstrap "valid Ubuntu 26.04 existing governed upgrade"; then
    pass "Ubuntu 26.04 upgrade does not bootstrap Docker or the Stem principal"
  fi
  if assert_durable_untouched "Ubuntu 26.04 upgrade leaves durable Stem state untouched"; then
    :
  fi
else
  fail "valid Ubuntu 26.04 existing governed upgrade" "status=${status} stderr=$(tr '\n' ' ' <"${stderr_file}") stdout=$(tr '\n' ' ' <"${stdout_file}")"
fi

new_governed_case
write_os_release debian 12
prepare_upgrade_host
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'requires Ubuntu' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "non-Ubuntu governed upgrade rejected"; then
    pass "non-Ubuntu governed upgrade rejected"
  fi
else
  fail "non-Ubuntu governed upgrade rejected" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
WSL_DISTRO=Ubuntu
prepare_upgrade_host
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'does not support WSL' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "WSL governed upgrade rejected"; then
    pass "WSL governed upgrade rejected"
  fi
else
  fail "WSL governed upgrade rejected" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
rm -rf "${HOSTFS}/run/user/2001"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q '/run/user/2001 is missing' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "missing tendril runtime directory fails before mutation"; then
    pass "missing tendril runtime directory fails before mutation"
  fi
else
  fail "missing tendril runtime directory fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf 'root\n' >"${ROOT}/meta/owners/%run%user%2001"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q '/run/user/2001 is owned by root' "${stderr_file}"; then
  if [ "$(cat "${ROOT}/meta/owners/%run%user%2001")" = root ]; then
    if assert_upgrade_preflight_unmutated "wrong-owner tendril runtime directory fails before mutation"; then
      pass "wrong-owner tendril runtime directory fails before mutation"
    fi
  else
    fail "wrong-owner tendril runtime directory fails before mutation: ownership was repaired"
  fi
else
  fail "wrong-owner tendril runtime directory fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
rm -f "${ROOT}/state/docker-rootless-ready"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'failed to query the tendril Docker daemon\|will not install, start, or repair Docker' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "unreachable tendril Docker fails before mutation"; then
    pass "unreachable tendril Docker fails before mutation"
  fi
else
  fail "unreachable tendril Docker fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf '[name=seccomp]\n' >"${ROOT}/state/docker-tendril-info"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'not rootless' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "non-rootless tendril Docker fails before mutation"; then
    pass "non-rootless tendril Docker fails before mutation"
  fi
else
  fail "non-rootless tendril Docker fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
touch "${ROOT}/state/active/docker.service"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'docker.service is active' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "upgrade rejects active rootful docker.service"; then
    pass "upgrade rejects active rootful docker.service"
  fi
else
  fail "upgrade rejects active rootful docker.service" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
touch "${ROOT}/state/enabled/docker.socket"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'enabled' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "upgrade rejects enabled rootful docker.socket"; then
    pass "upgrade rejects enabled rootful docker.socket"
  fi
else
  fail "upgrade rejects enabled rootful docker.socket" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf '[name=seccomp]\n' >"${ROOT}/state/docker-info"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'rootful' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "upgrade rejects usable ambient rootful Docker"; then
    pass "upgrade rejects usable ambient rootful Docker"
  fi
else
  fail "upgrade rejects usable ambient rootful Docker" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf 'root\n' >"${ROOT}/meta/owners/%home%tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q '/home/tendril is owned by root' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "invalid Stem home ownership fails before mutation"; then
    pass "invalid Stem home ownership fails before mutation"
  fi
else
  fail "invalid Stem home ownership fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf 'tendril:$6$notlocked:19600:0:99999:7:::\n' >"${HOSTFS}/etc/shadow.tmp"
grep -v '^tendril:' "${HOSTFS}/etc/shadow" >>"${HOSTFS}/etc/shadow.tmp"
mv "${HOSTFS}/etc/shadow.tmp" "${HOSTFS}/etc/shadow"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'interactive password' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "interactive Stem principal fails before mutation"; then
    pass "interactive Stem principal fails before mutation"
  fi
else
  fail "interactive Stem principal fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
preseed_tendril_user with-subid
rm -rf "${HOSTFS}/home/tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q '/home/tendril is missing' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "missing Stem home fails before mutation"; then
    pass "missing Stem home fails before mutation"
  fi
else
  fail "missing Stem home fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
rm -f "${HOSTFS}/home/tendril/.local/bin/tendril"
mkdir -p "${HOSTFS}/home/tendril/.local/bin/tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'not a regular file' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "protected Stem directory fails before mutation"; then
    pass "protected Stem directory fails before mutation"
  fi
else
  fail "protected Stem directory fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf 'root\n' >"${ROOT}/meta/owners/%home%tendril%.local%bin%tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'owned by root' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "invalid protected Stem owner fails before mutation"; then
    pass "invalid protected Stem owner fails before mutation"
  fi
else
  fail "invalid protected Stem owner fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
mkdir -p "${ROOT}/meta/groups"
printf 'alice\n' >"${ROOT}/meta/groups/%home%tendril%.local%bin%tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'group is alice' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "invalid protected Stem group fails before mutation"; then
    pass "invalid protected Stem group fails before mutation"
  fi
else
  fail "invalid protected Stem group fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
chmod 0755 "${HOSTFS}/home/tendril/.local/bin/tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'mode is 755' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "invalid protected Stem mode fails before mutation"; then
    pass "invalid protected Stem mode fails before mutation"
  fi
else
  fail "invalid protected Stem mode fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
run_governed_upgrade_installer --pollinator-user tendril
if [ "${status}" -ne 0 ] && grep -q 'cannot be tendril' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "Pollinator/Stem overlap fails before mutation"; then
    pass "Pollinator/Stem overlap fails before mutation"
  fi
else
  fail "Pollinator/Stem overlap fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
printf 'alice tendril\n' >"${ROOT}/state/groups-alice"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -q 'is in group tendril' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "Pollinator in Stem group fails before mutation"; then
    pass "Pollinator in Stem group fails before mutation"
  fi
else
  fail "Pollinator in Stem group fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
cat >"${ROOT}/state/sudo-l" <<'EOF'
User alice may run the following commands on this host:
    (ALL) NOPASSWD: ALL
EOF
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'passwordless sudo' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "Pollinator NOPASSWD ALL fails before mutation"; then
    pass "Pollinator NOPASSWD ALL fails before mutation"
  fi
else
  fail "Pollinator NOPASSWD ALL fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
cat >"${ROOT}/state/sudo-l" <<'EOF'
User alice may run the following commands on this host:
    (root) NOPASSWD: /bin/sh
EOF
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'passwordless sudo' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "Pollinator root NOPASSWD fails before mutation"; then
    pass "Pollinator root NOPASSWD fails before mutation"
  fi
else
  fail "Pollinator root NOPASSWD fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
fi

new_governed_case
prepare_upgrade_host
touch "${ROOT}/state/passwordless-tendril"
run_governed_upgrade_installer --pollinator-user alice
if [ "${status}" -ne 0 ] && grep -qi 'non-interactively' "${stderr_file}"; then
  if assert_upgrade_preflight_unmutated "Pollinator sudo -n to Stem fails before mutation"; then
    pass "Pollinator sudo -n to Stem fails before mutation"
    if events_match '^CMD sudo -l -U alice' \
      && events_match '^CMD sudo -u alice sudo -n -u tendril true' \
      && assert_no_sudo_timestamp_mutation "failed upgrade preflight performs no sudo -K"; then
      pass "failed upgrade preflight performs no sudo -K"
    else
      fail "failed upgrade preflight performs no sudo -K" "events=$(tr '\n' ' ' <"${events_file}")"
    fi
  fi
else
  fail "Pollinator sudo -n to Stem fails before mutation" "stderr=$(tr '\n' ' ' <"${stderr_file}")"
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

host_unit_after="absent"
host_sudoers_after="absent"
if [ -e "${host_unit_path}" ]; then
  host_unit_after="$(stat -c '%s %Y' "${host_unit_path}" 2>/dev/null || echo present)"
fi
if [ -e "${host_sudoers_path}" ]; then
  host_sudoers_after="$(stat -c '%s %Y' "${host_sudoers_path}" 2>/dev/null || echo present)"
fi
if [ "${host_unit_after}" = "${host_unit_before}" ]; then
  pass "host systemd unit was not mutated"
else
  fail "host systemd unit was not mutated" "before=${host_unit_before} after=${host_unit_after}"
fi
if [ "${host_sudoers_after}" = "${host_sudoers_before}" ]; then
  pass "host sudoers snippet was not mutated"
else
  fail "host sudoers snippet was not mutated" "before=${host_sudoers_before} after=${host_sudoers_after}"
fi
host_tendril_home_after="$(ls -la /home/tendril 2>/dev/null || echo '__missing__')"
if [ "${host_tendril_home_after}" = "${host_tendril_home_before}" ]; then
  pass "host /home/tendril was not mutated"
else
  fail "host /home/tendril was not mutated"
fi

if [ "${failures}" -gt 0 ]; then
  echo
  echo "${failures} installer test(s) failed; ${passes} passed."
  exit 1
fi

echo
echo "All ${passes} installer tests passed."
