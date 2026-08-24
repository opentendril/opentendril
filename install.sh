#!/bin/sh
# OpenTendril installer.
#
# Default: LOCAL / SINGLE-PRINCIPAL bootstrap. Downloads an official GitHub
# Release archive, verifies SHA-256, and places only `tendril` at
# ~/.local/bin/tendril. Does not establish the governed P1-P5 boundary. Does
# not run tendril.
#
# Governed: Ubuntu 24.04 LTS linux/amd64 with systemd and rootless Docker.
# Requires root. Places protected tendril under the tendril principal and
# tendril-mcp under the Pollinator-hosting account. Does not start an
# unconfigured Stem.
#
# This script must work when supplied on stdin:
#   curl -fsSL <release>/install.sh | sh
#   curl -fsSL <release>/install.sh | sudo sh -s -- --governed --pollinator-user <user>
# Do not consult $0 as a filesystem path.

set -eu

RELEASE_ROOT="https://github.com/opentendril/opentendril/releases"
DOCKER_GPG_URL="https://download.docker.com/linux/ubuntu/gpg"
ARCHIVE_PREFIX="opentendril"
DEST_SUBDIR=".local/bin"
DEST_NAME="tendril"
STEM_USER="tendril"
STEM_HOME="/home/tendril"
STEM_BIN="${STEM_HOME}/.local/bin/tendril"
UNIT_PATH="/etc/systemd/system/tendril.service"
SUDOERS_SNIPPET="/etc/sudoers.d/opentendril-p2"

version="${OPENTENDRIL_VERSION:-}"
want_help=0
governed=0
pollinator_user=""
workdir=""
docker_units_guarded=0
governed_finished=0

die() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "${workdir:-}" ] && [ -d "$workdir" ]; then
    rm -rf "$workdir"
  fi
}

governed_cleanup() {
  if [ "${docker_units_guarded:-0}" -eq 1 ] && [ "${governed_finished:-0}" -ne 1 ]; then
    systemctl disable --now docker.service docker.socket </dev/null 2>/dev/null || true
  fi
  cleanup
}

usage() {
  cat <<'EOF'
OpenTendril installer

Usage:
  sh install.sh [--version <tag>]
  OPENTENDRIL_VERSION=<tag> sh install.sh
  curl -fsSL <url>/install.sh | sh
  curl -fsSL <url>/install.sh | sh -s -- --version <tag>

  sudo sh install.sh --governed --pollinator-user <user> [--version <tag>]
  curl -fsSL <url>/install.sh | sudo sh -s -- --governed --pollinator-user <user>

Options:
  --version <tag>              Pin to one GitHub Release (v0.3.0 or 0.3.0).
                               Also accepted as OPENTENDRIL_VERSION.
  --governed                   Establish the Ubuntu 24.04 LTS governed host.
                               Requires root. Does not install locally.
  --pollinator-user <account>  Ordinary Pollinator-hosting account. Required
                               for --governed unless sudo already set SUDO_USER
                               to a non-root ordinary account.
  -h, --help                   Show this help.

Local platforms (default; no --governed):
  linux/amd64, linux/arm64 (including WSL2)
  darwin/amd64, darwin/arm64
  Installs only tendril to ~/.local/bin/tendril. Does not install tendril-mcp
  or establish the governed P1-P5 boundary.

Governed platform (--governed):
  Ubuntu 24.04 LTS, linux/amd64, systemd, rootless Docker only.
  WSL, macOS, other Linux distributions, other Ubuntu releases, and arm64
  are not qualified. Installs protected tendril for the tendril principal
  and tendril-mcp for the Pollinator account. Does not start the Stem.
EOF
}

is_hex64() {
  [ "${#1}" -eq 64 ] || return 1
  case "$1" in
    *[!0-9a-fA-F]*) return 1 ;;
  esac
  return 0
}

to_lower_hex() {
  printf '%s' "$1" | tr 'A-F' 'a-f'
}

normalize_tag() {
  _tag=$1
  [ -n "$_tag" ] || die "--version / OPENTENDRIL_VERSION must not be empty"
  case "$_tag" in
    v*) ;;
    *) _tag="v${_tag}" ;;
  esac
  case "$_tag" in
    *[!A-Za-z0-9._-]*) die "invalid release tag: ${_tag}" ;;
  esac
  printf '%s' "$_tag"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --)
        shift
        break
        ;;
      -h|--help)
        want_help=1
        shift
        ;;
      --governed)
        governed=1
        shift
        ;;
      --pollinator-user)
        [ $# -ge 2 ] || die "--pollinator-user requires an account name"
        pollinator_user=$2
        shift 2
        ;;
      --pollinator-user=*)
        pollinator_user=${1#--pollinator-user=}
        shift
        ;;
      --version)
        [ $# -ge 2 ] || die "--version requires a release tag"
        version=$2
        shift 2
        ;;
      --version=*)
        version=${1#--version=}
        shift
        ;;
      -*)
        die "unknown option: $1 (try --help)"
        ;;
      *)
        die "unexpected argument: $1 (try --help)"
        ;;
    esac
  done
  [ $# -eq 0 ] || die "unexpected argument: $1 (try --help)"
}

detect_platform() {
  raw_os=$(uname -s)
  raw_arch=$(uname -m)
  wsl=0

  case "$raw_os" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *)
      die "unsupported operating system: ${raw_os} (supported: linux, darwin)"
      ;;
  esac

  case "$raw_arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *)
      die "unsupported architecture: ${raw_arch} (supported: amd64, arm64)"
      ;;
  esac

  if [ "$os" = linux ]; then
    if [ -n "${WSL_DISTRO_NAME:-}" ]; then
      wsl=1
    else
      osrelease=$(cat /proc/sys/kernel/osrelease 2>/dev/null) || osrelease=
      case "$osrelease" in
        *[Mm]icrosoft*) wsl=1 ;;
      esac
    fi
  fi
}

release_base() {
  if [ -n "$version" ]; then
    tag=$(normalize_tag "$version")
    printf '%s/download/%s' "$RELEASE_ROOT" "$tag"
  else
    printf '%s/latest/download' "$RELEASE_ROOT"
  fi
}

download() {
  _url=$1
  _dest=$2
  case "$_url" in
    https://github.com/opentendril/opentendril/releases/*) ;;
    *) die "refusing download from ${_url}" ;;
  esac
  command -v curl >/dev/null 2>&1 || die "curl is required"
  curl -fsSL --proto '=https' -o "$_dest" "$_url" </dev/null || die "failed to download ${_url}"
  [ -f "$_dest" ] || die "download produced no file: ${_dest}"
}

download_docker_gpg() {
  _dest=$1
  case "$DOCKER_GPG_URL" in
    https://download.docker.com/linux/ubuntu/gpg) ;;
    *) die "refusing Docker bootstrap URL ${DOCKER_GPG_URL}" ;;
  esac
  command -v curl >/dev/null 2>&1 || die "curl is required"
  curl -fsSL --proto '=https' -o "$_dest" "$DOCKER_GPG_URL" </dev/null || die "failed to download Docker repository key"
  [ -f "$_dest" ] || die "Docker repository key download produced no file"
}

expected_hash=""
match_count=0

ingest_checksum_line() {
  _line=$1
  case "$_line" in
    ''|'#'*) return 0 ;;
  esac
  _hash=${_line%% *}
  _rest=${_line#"$_hash"}
  while :; do
    case "$_rest" in
      ' '*) _rest=${_rest# } ;;
      *) break ;;
    esac
  done
  _name=$_rest
  case "$_name" in
    '*'*) _name=${_name#\*} ;;
  esac
  [ "$_name" = "$archive" ] || return 0
  match_count=$((match_count + 1))
  expected_hash=$_hash
}

verify_archive() {
  match_count=0
  expected_hash=""
  while IFS= read -r line || [ -n "$line" ]; do
    ingest_checksum_line "$line"
  done <"$checksums_path"

  if [ "$match_count" -eq 0 ]; then
    die "checksums.txt has no entry for ${archive}"
  fi
  if [ "$match_count" -gt 1 ]; then
    die "checksums.txt has ${match_count} entries for ${archive} (exactly one required)"
  fi
  is_hex64 "$expected_hash" || die "checksums.txt entry for ${archive} is not a SHA-256 hex digest"

  if command -v sha256sum >/dev/null 2>&1; then
    hash_out=$(sha256sum "$archive_path") || die "failed to hash ${archive}"
  elif command -v shasum >/dev/null 2>&1; then
    hash_out=$(shasum -a 256 "$archive_path") || die "failed to hash ${archive}"
  else
    die "checksum verification requires sha256sum or shasum -a 256"
  fi
  actual_hash=$(to_lower_hex "${hash_out%% *}")
  want_hash=$(to_lower_hex "$expected_hash")
  is_hex64 "$actual_hash" || die "computed digest for ${archive} is not a SHA-256 hex digest"
  if [ "$actual_hash" != "$want_hash" ]; then
    die "SHA-256 mismatch for ${archive} (refusing to extract or install)"
  fi
}

extract_member() {
  _member=$1
  command -v tar >/dev/null 2>&1 || die "tar is required"
  tar -C "$workdir" -xzf "$archive_path" "$_member" </dev/null || die "failed to extract ${_member} from ${archive}"
  if [ -L "${workdir}/${_member}" ] || [ ! -f "${workdir}/${_member}" ]; then
    die "archive member ${_member} is not a regular file"
  fi
  [ -s "${workdir}/${_member}" ] || die "extracted ${_member} is empty"
}

extract_tendril() {
  extract_member tendril
  if [ -e "${workdir}/tendril-mcp" ]; then
    die "tendril-mcp was extracted; local mode installs only tendril"
  fi
  extracted="${workdir}/tendril"
}

install_tendril() {
  mkdir -p "$dest_dir" || die "cannot create ${dest_dir}"
  if command -v install >/dev/null 2>&1; then
    install -m 0755 "$extracted" "$dest_path" </dev/null || die "failed to install ${dest_path}"
  else
    cp "$extracted" "$dest_path" </dev/null || die "failed to copy ${dest_path}"
    chmod 0755 "$dest_path" || die "failed to chmod ${dest_path}"
  fi
  [ -f "$dest_path" ] || die "install produced no file: ${dest_path}"
}

path_has_dest() {
  case ":${PATH}:" in
    *":${dest_dir}:"*) return 0 ;;
  esac
  return 1
}

print_success() {
  platform="${os}/${arch}"
  if [ "$wsl" -eq 1 ]; then
    platform="${platform} (WSL follows the Linux LOCAL path)"
  fi
  printf '\n'
  printf 'Installed: %s\n' "$dest_path"
  printf 'Platform:  %s\n' "$platform"
  printf 'Posture:   LOCAL / SINGLE-PRINCIPAL\n'
  printf '\n'
  printf 'This installation does NOT establish the governed P1-P5 boundary.\n'
  printf 'The Stem will run as this account. That is a legitimate local posture;\n'
  printf 'it is not a governed installation.\n'
  printf '\n'
  printf 'Next:\n'
  printf '  tendril hardiness\n'
  printf '  tendril init\n'
  printf '\n'
  printf 'First-use documentation:\n'
  printf '  https://github.com/opentendril/opentendril/blob/main/docs/GUIDE-INSTALL.md\n'
  printf '  https://github.com/opentendril/opentendril/blob/main/docs/GUIDE-QUICKSTART.md\n'
  if ! path_has_dest; then
    printf '\n'
    printf '%s is not on PATH. Add it for this shell:\n' "$dest_dir"
    printf '  export PATH="%s:$PATH"\n' "$dest_dir"
  fi
}

prepare_workdir() {
  trap "$1" EXIT
  trap "$1; exit 1" INT HUP TERM
  workdir=$(mktemp -d "${TMPDIR:-/tmp}/opentendril-install.XXXXXX") || die "cannot create a temporary directory"
  checksums_path="${workdir}/checksums.txt"
  archive_path="${workdir}/${archive}"
}

obtain_verified_archive() {
  base=$(release_base)
  download "${base}/checksums.txt" "$checksums_path"
  download "${base}/${archive}" "$archive_path"
  verify_archive
}

# --- governed helpers -------------------------------------------------------

fs_exists() {
  stat "$1" >/dev/null 2>&1
}

fs_is_dir() {
  _kind=$(stat -c '%F' "$1" 2>/dev/null) || return 1
  [ "$_kind" = directory ]
}

fs_owner() {
  stat -c '%U' "$1" 2>/dev/null || return 1
}

parse_passwd_line() {
  _pwline=$1
  IFS=: read -r pw_name pw_x pw_uid pw_gid pw_gecos pw_home pw_shell <<EOF
${_pwline}
EOF
}

passwd_entry() {
  getent passwd "$1" 2>/dev/null
}

shadow_hash() {
  _shline=$(getent shadow "$1" 2>/dev/null) || return 1
  IFS=: read -r _sh_name _sh_hash _rest <<EOF
${_shline}
EOF
  printf '%s' "$_sh_hash"
}

password_is_locked() {
  case "$1" in
    '!'*|'*') return 0 ;;
  esac
  return 1
}

parse_os_release() {
  os_id=
  os_version_id=
  os_release_text=$(cat /etc/os-release) || die "cannot read /etc/os-release"
  [ -n "$os_release_text" ] || die "/etc/os-release is empty"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ID=*|VERSION_ID=*)
        _key=${line%%=*}
        _val=${line#*=}
        _val=${_val#\"}
        _val=${_val%\"}
        _val=${_val#\'}
        _val=${_val%\'}
        if [ "$_key" = ID ]; then
          os_id=$_val
        else
          os_version_id=$_val
        fi
        ;;
    esac
  done <<EOF
${os_release_text}
EOF
  [ -n "$os_id" ] || die "/etc/os-release does not contain ID"
  [ -n "$os_version_id" ] || die "/etc/os-release does not contain VERSION_ID"
}

pkg_installed() {
  _st=$(dpkg-query -W -f='${Status}' "$1" 2>/dev/null) || return 1
  case "$_st" in
    *"install ok installed"*) return 0 ;;
  esac
  return 1
}

unit_is_active() {
  systemctl is-active "$1" </dev/null >/dev/null 2>&1
}

unit_is_enabled() {
  _en=$(systemctl is-enabled "$1" </dev/null 2>/dev/null) || return 1
  case "$_en" in
    enabled|enabled-runtime) return 0 ;;
  esac
  return 1
}

rootful_cli_usable() {
  command -v docker >/dev/null 2>&1 || return 1
  _opts=$(DOCKER_HOST= docker info --format '{{.SecurityOptions}}' </dev/null 2>/dev/null) || return 1
  case "$_opts" in
    *rootless*) return 1 ;;
  esac
  return 0
}

docker_engine_stack_installed() {
  pkg_installed docker-ce || return 1
  pkg_installed docker-ce-cli || return 1
  pkg_installed containerd.io || return 1
  pkg_installed docker-ce-rootless-extras || return 1
}

classify_docker() {
  docker_class=absent
  if pkg_installed docker.io; then
    docker_class=unsafe
    docker_reason="package docker.io is installed. Ubuntu docker.io starts a rootful daemon and does not ship dockerd-rootless-setuptool.sh. Governed installation will not remove or replace it. Use a clean Ubuntu 24.04 LTS host."
    return 0
  fi
  if unit_is_active docker.service; then
    docker_class=unsafe
    docker_reason="system docker.service is active (rootful). Governed installation will not disable, remove, or repurpose a foreign rootful Docker daemon."
    return 0
  fi
  if unit_is_active docker.socket; then
    docker_class=unsafe
    docker_reason="system docker.socket is active (rootful). Governed installation will not disable, remove, or repurpose a foreign rootful Docker socket."
    return 0
  fi
  if unit_is_enabled docker.service || unit_is_enabled docker.socket; then
    docker_class=unsafe
    docker_reason="system docker.service or docker.socket is enabled. That is a rootful boot path. Governed installation will not disable or repurpose it."
    return 0
  fi
  if rootful_cli_usable; then
    docker_class=unsafe
    docker_reason="a usable rootful Docker daemon answered docker info. Governed installation will not take over a foreign rootful Docker posture."
    return 0
  fi
  if docker_engine_stack_installed; then
    docker_class=compatible
    docker_reason="Docker Engine packages are present and the system rootful daemon is not active; this is installer-compatible package state."
    return 0
  fi
  if pkg_installed docker-ce || pkg_installed docker-ce-cli || pkg_installed containerd.io || pkg_installed docker-ce-rootless-extras; then
    docker_class=unsafe
    docker_reason="a partial Docker Engine package set is present without the required rootless extras stack, and the state cannot be classified as a clean host. Refusing to guess."
    return 0
  fi
  docker_class=absent
  docker_reason="no Docker Engine or docker.io installation detected"
}

require_ordinary_name() {
  _n=$1
  _what=$2
  case "$_n" in
    ''|*[!a-zA-Z0-9._-]*)
      die "invalid ${_what} name: ${_n}"
      ;;
  esac
}

resolve_pollinator() {
  if [ -z "$pollinator_user" ]; then
    if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
      pollinator_user=$SUDO_USER
    else
      die "governed installation requires --pollinator-user <account>. Direct root execution cannot identify the Pollinator-hosting account. SUDO_USER is accepted only when it names a non-root ordinary account."
    fi
  fi
  require_ordinary_name "$pollinator_user" "Pollinator-hosting account"
  if [ "$pollinator_user" = root ]; then
    die "Pollinator-hosting account cannot be root"
  fi
  if [ "$pollinator_user" = "$STEM_USER" ]; then
    die "Pollinator-hosting account cannot be ${STEM_USER} (the Stem principal)"
  fi
  _pw=$(passwd_entry "$pollinator_user") || die "Pollinator-hosting account '${pollinator_user}' does not exist"
  parse_passwd_line "$_pw"
  pollinator_uid=$pw_uid
  pollinator_gid=$pw_gid
  pollinator_home=$pw_home
  case "$pollinator_uid" in
    ''|*[!0-9]*) die "Pollinator-hosting account '${pollinator_user}' has an unusable uid" ;;
  esac
  if [ "$pollinator_uid" -eq 0 ]; then
    die "Pollinator-hosting account cannot be uid 0"
  fi
  if [ "$pollinator_uid" -lt 1000 ]; then
    die "Pollinator-hosting account '${pollinator_user}' is a system account (uid ${pollinator_uid}); an ordinary login account is required"
  fi
  [ -n "$pollinator_home" ] || die "Pollinator-hosting account '${pollinator_user}' has no home directory"
  fs_is_dir "$pollinator_home" || die "Pollinator home ${pollinator_home} is missing"
}

subid_has_user() {
  _file=$1
  _user=$2
  _found=0
  while IFS=: read -r _n _rest || [ -n "${_n:-}" ]; do
    if [ "$_n" = "$_user" ]; then
      _found=1
    fi
    _n=
  done <<EOF
$(cat "$_file")
EOF
  [ "$_found" -eq 1 ]
}

ensure_tendril_principal() {
  _pw=$(passwd_entry "$STEM_USER" || true)
  if [ -n "$_pw" ]; then
    parse_passwd_line "$_pw"
    if [ "$pw_home" != "$STEM_HOME" ]; then
      die "existing ${STEM_USER} account has home ${pw_home}, expected ${STEM_HOME}; refusing to rewrite it"
    fi
    case "$pw_uid" in
      ''|*[!0-9]*) die "existing ${STEM_USER} account has an unusable uid" ;;
    esac
    if [ "$pw_uid" -lt 1000 ]; then
      die "existing ${STEM_USER} account has uid ${pw_uid} (system account); refusing to rewrite it"
    fi
    _hash=$(shadow_hash "$STEM_USER") || die "cannot read the ${STEM_USER} shadow entry"
    if ! password_is_locked "$_hash"; then
      die "existing ${STEM_USER} account has an interactive password; refusing to rewrite it"
    fi
    fs_is_dir "$STEM_HOME" || die "existing ${STEM_USER} account is missing home ${STEM_HOME}; refusing to create it by rewriting the account"
    _owner=$(fs_owner "$STEM_HOME") || die "cannot determine owner of ${STEM_HOME}"
    if [ "$_owner" != "$STEM_USER" ]; then
      die "existing ${STEM_HOME} is owned by ${_owner}, expected ${STEM_USER}; refusing to take ownership"
    fi
    tendril_uid=$pw_uid
  else
    require_cmd adduser
    adduser --disabled-password --gecos "OpenTendril Stem" "$STEM_USER" </dev/null || die "failed to create the ${STEM_USER} account"
    _pw=$(passwd_entry "$STEM_USER") || die "adduser did not create ${STEM_USER}"
    parse_passwd_line "$_pw"
    if [ "$pw_home" != "$STEM_HOME" ]; then
      die "created ${STEM_USER} account has home ${pw_home}, expected ${STEM_HOME}"
    fi
    if [ "$pw_uid" -lt 1000 ]; then
      die "created ${STEM_USER} account has uid ${pw_uid}; an ordinary login uid (>= 1000) is required"
    fi
    tendril_uid=$pw_uid
    fs_is_dir "$STEM_HOME" || die "created ${STEM_USER} account is missing home ${STEM_HOME}"
  fi
  chmod 0750 "$STEM_HOME" || die "failed to chmod ${STEM_HOME}"
  _groups=$(id -Gn "$pollinator_user") || die "cannot read groups for ${pollinator_user}"
  for _g in $_groups; do
    if [ "$_g" = "$STEM_USER" ]; then
      die "Pollinator-hosting account ${pollinator_user} is in group ${STEM_USER}, so it can write the Stem resolution path. Remove that group membership."
    fi
  done
  require_cmd usermod
  if ! subid_has_user /etc/subuid "$STEM_USER" || ! subid_has_user /etc/subgid "$STEM_USER"; then
    usermod --add-subuids 165536-231071 --add-subgids 165536-231071 "$STEM_USER" </dev/null || die "failed to add subordinate uid/gid ranges for ${STEM_USER}"
  fi
  if ! subid_has_user /etc/subuid "$STEM_USER" || ! subid_has_user /etc/subgid "$STEM_USER"; then
    die "${STEM_USER} still has no subordinate uid/gid ranges after usermod"
  fi
  require_cmd loginctl
  loginctl enable-linger "$STEM_USER" </dev/null || die "failed to enable linger for ${STEM_USER}"
}

apt_install() {
  DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" </dev/null || die "apt-get install failed: $*"
}

install_prereq_packages() {
  require_cmd apt-get
  DEBIAN_FRONTEND=noninteractive apt-get update </dev/null || die "apt-get update failed"
  apt_install ca-certificates curl git uidmap slirp4netns dbus-user-session
  require_cmd curl
}

install_docker_engine() {
  require_cmd apt-get
  require_cmd systemctl
  install -m 0755 -d /etc/apt/keyrings </dev/null || die "failed to create /etc/apt/keyrings"
  download_docker_gpg "${workdir}/docker.asc"
  install -m 0644 "${workdir}/docker.asc" /etc/apt/keyrings/docker.asc </dev/null || die "failed to install the Docker repository key"
  chmod a+r /etc/apt/keyrings/docker.asc || die "failed to chmod /etc/apt/keyrings/docker.asc"
  _arch=$(dpkg --print-architecture) || die "dpkg --print-architecture failed"
  cat >"${workdir}/docker.sources" <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: ${_arch}
Signed-By: /etc/apt/keyrings/docker.asc
EOF
  install -m 0644 "${workdir}/docker.sources" /etc/apt/sources.list.d/docker.sources </dev/null || die "failed to install the Docker apt source"
  docker_units_guarded=1
  systemctl mask docker.service docker.socket </dev/null || true
  DEBIAN_FRONTEND=noninteractive apt-get update </dev/null || die "apt-get update failed after adding the Docker repository"
  apt_install docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras
  systemctl disable --now docker.service docker.socket </dev/null || true
  if unit_is_active docker.service || unit_is_active docker.socket; then
    systemctl disable --now docker.service docker.socket </dev/null || true
    die "Docker Engine installation left a rootful docker.service or docker.socket active"
  fi
}

ensure_user_runtime() {
  if ! fs_exists "/run/user/${tendril_uid}"; then
    systemctl start "user@${tendril_uid}.service" </dev/null || true
  fi
  if ! fs_exists "/run/user/${tendril_uid}"; then
    install -d -o "$STEM_USER" -g "$STEM_USER" -m 0700 "/run/user/${tendril_uid}" </dev/null || die "failed to create /run/user/${tendril_uid} for the ${STEM_USER} rootless daemon"
  fi
}

tendril_docker_opts() {
  sudo -u "$STEM_USER" -H \
    XDG_RUNTIME_DIR="/run/user/${tendril_uid}" \
    DOCKER_HOST="unix:///run/user/${tendril_uid}/docker.sock" \
    docker info --format '{{.SecurityOptions}}' </dev/null
}

establish_rootless_docker() {
  command -v dockerd-rootless-setuptool.sh >/dev/null 2>&1 || die "dockerd-rootless-setuptool.sh is missing; Docker Engine docker-ce-rootless-extras is required"
  ensure_user_runtime
  if ! sudo -u "$STEM_USER" -H \
    XDG_RUNTIME_DIR="/run/user/${tendril_uid}" \
    dockerd-rootless-setuptool.sh install </dev/null; then
    systemctl disable --now docker.service docker.socket </dev/null || true
    die "dockerd-rootless-setuptool.sh install failed"
  fi
  if ! sudo -u "$STEM_USER" -H \
    XDG_RUNTIME_DIR="/run/user/${tendril_uid}" \
    systemctl --user enable --now docker </dev/null; then
    systemctl disable --now docker.service docker.socket </dev/null || true
    die "failed to enable the ${STEM_USER} rootless Docker user unit"
  fi
  if unit_is_active docker.service || unit_is_active docker.socket; then
    systemctl disable --now docker.service docker.socket </dev/null || true
    die "rootful Docker became active while establishing the rootless daemon"
  fi
  _opts=$(tendril_docker_opts) || {
    systemctl disable --now docker.service docker.socket </dev/null || true
    die "failed to query the ${STEM_USER} Docker daemon after rootless setup"
  }
  case "$_opts" in
    *rootless*) ;;
    *)
      systemctl disable --now docker.service docker.socket </dev/null || true
      die "tendril Docker daemon is not rootless (SecurityOptions: ${_opts})"
      ;;
  esac
}

install_governed_binaries() {
  extract_member tendril
  extract_member tendril-mcp
  _pollinator_tendril="${pollinator_home}/.local/bin/tendril"
  if fs_exists "$_pollinator_tendril"; then
    die "refusing to proceed: ${_pollinator_tendril} already exists. Governed mode will not place the full tendril executable on the Pollinator account, and will not delete the existing file."
  fi
  install -d -o "$STEM_USER" -g "$STEM_USER" -m 0750 "${STEM_HOME}/.local" </dev/null || die "failed to create ${STEM_HOME}/.local"
  install -d -o "$STEM_USER" -g "$STEM_USER" -m 0750 "${STEM_HOME}/.local/bin" </dev/null || die "failed to create ${STEM_HOME}/.local/bin"
  install -o "$STEM_USER" -g "$STEM_USER" -m 0750 "${workdir}/tendril" "$STEM_BIN" </dev/null || die "failed to install ${STEM_BIN}"
  _pgrp=$(id -gn "$pollinator_user") || _pgrp=$pollinator_user
  install -d -o "$pollinator_user" -g "$_pgrp" -m 0755 "${pollinator_home}/.local" </dev/null || die "failed to create ${pollinator_home}/.local"
  install -d -o "$pollinator_user" -g "$_pgrp" -m 0755 "${pollinator_home}/.local/bin" </dev/null || die "failed to create ${pollinator_home}/.local/bin"
  install -o "$pollinator_user" -g "$_pgrp" -m 0755 "${workdir}/tendril-mcp" "${pollinator_home}/.local/bin/tendril-mcp" </dev/null || die "failed to install tendril-mcp"
  if fs_exists "$_pollinator_tendril"; then
    die "governed installation placed tendril on the Pollinator account; refusing to continue"
  fi
}

ensure_control_plane() {
  if fs_exists "${STEM_HOME}/.tendril"; then
    fs_is_dir "${STEM_HOME}/.tendril" || die "${STEM_HOME}/.tendril exists and is not a directory; refusing to replace it"
    _owner=$(fs_owner "${STEM_HOME}/.tendril") || die "cannot determine owner of ${STEM_HOME}/.tendril"
    [ "$_owner" = "$STEM_USER" ] || die "${STEM_HOME}/.tendril is owned by ${_owner}, expected ${STEM_USER}; refusing to take ownership"
  else
    install -d -o "$STEM_USER" -g "$STEM_USER" -m 0700 "${STEM_HOME}/.tendril" </dev/null || die "failed to create ${STEM_HOME}/.tendril"
  fi
  for _sub in substrates workspaces run-workspaces; do
    _d="${STEM_HOME}/.tendril/${_sub}"
    if fs_exists "$_d"; then
      fs_is_dir "$_d" || die "${_d} exists and is not a directory; refusing to replace it"
    else
      install -d -o "$STEM_USER" -g "$STEM_USER" -m 0700 "$_d" </dev/null || die "failed to create ${_d}"
    fi
  done
  if fs_exists "${STEM_HOME}/.env"; then
    _owner=$(fs_owner "${STEM_HOME}/.env") || die "cannot determine owner of ${STEM_HOME}/.env"
    [ "$_owner" = "$STEM_USER" ] || die "${STEM_HOME}/.env is owned by ${_owner}, expected ${STEM_USER}; refusing to replace it"
  else
    : >"${workdir}/empty.env"
    install -o "$STEM_USER" -g "$STEM_USER" -m 0600 "${workdir}/empty.env" "${STEM_HOME}/.env" </dev/null || die "failed to create ${STEM_HOME}/.env"
  fi
}

install_tendril_unit() {
  cat >"${workdir}/tendril.service" <<EOF
[Unit]
Description=OpenTendril Stem
After=network-online.target

[Service]
User=tendril
Group=tendril
WorkingDirectory=/home/tendril
Environment=DOCKER_HOST=unix:///run/user/${tendril_uid}/docker.sock
Environment=XDG_RUNTIME_DIR=/run/user/${tendril_uid}
StateDirectory=opentendril-transport
StateDirectoryMode=0755
Environment=TENDRIL_LOCAL_SOCKET=/var/lib/opentendril-transport/stem.sock
ExecStart=/home/tendril/.local/bin/tendril serve
Restart=on-failure

NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/home/tendril /run/user/${tendril_uid}
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes

[Install]
WantedBy=multi-user.target
EOF
  install -m 0644 "${workdir}/tendril.service" "$UNIT_PATH" </dev/null || die "failed to install ${UNIT_PATH}"
  systemctl daemon-reload </dev/null || die "systemctl daemon-reload failed"
}

listing_has_passwordless_stem() {
  _listing=$1
  case "$_listing" in
    *NOPASSWD*)
      case "$_listing" in
        *'(ALL)'*|*'(ALL : ALL)'*|*'(tendril)'*|*'NOPASSWD: ALL'*|*'NOPASSWD:ALL'*)
          return 0
          ;;
      esac
      ;;
  esac
  return 1
}

enforce_p2() {
  require_cmd sudo
  require_cmd visudo
  sudo -u "$pollinator_user" sudo -K </dev/null 2>/dev/null || true
  _listing=$(sudo -l -U "$pollinator_user" </dev/null 2>/dev/null) || _listing=
  if listing_has_passwordless_stem "$_listing"; then
    die "Pollinator-hosting account ${pollinator_user} has passwordless sudo that can become ${STEM_USER}. That violates P2 (unattended escalation). Remove NOPASSWD rules that allow this account to run commands as ${STEM_USER}. Governed installation will not loosen sudo policy."
  fi
  case "$_listing" in
    *'not allowed to run sudo'*|'')
      ;;
    *)
      printf 'Defaults:%s timestamp_timeout=0\n' "$pollinator_user" >"${workdir}/opentendril-p2"
      chmod 0440 "${workdir}/opentendril-p2" || die "failed to chmod the sudoers snippet"
      visudo -c -f "${workdir}/opentendril-p2" </dev/null || die "sudoers snippet failed visudo validation"
      install -m 0440 "${workdir}/opentendril-p2" "$SUDOERS_SNIPPET" </dev/null || die "failed to install ${SUDOERS_SNIPPET}"
      if ! visudo -c </dev/null; then
        rm -f "$SUDOERS_SNIPPET"
        die "activating ${SUDOERS_SNIPPET} failed visudo; the snippet was removed"
      fi
      ;;
  esac
  sudo -u "$pollinator_user" sudo -K </dev/null 2>/dev/null || true
  if sudo -u "$pollinator_user" sudo -n -u "$STEM_USER" true </dev/null 2>/dev/null; then
    die "Pollinator-hosting account ${pollinator_user} can become ${STEM_USER} non-interactively (sudo -n -u ${STEM_USER}). That violates P2. Cached or passwordless escalation is not an accepted governed posture."
  fi
}

print_governed_success() {
  printf '\n'
  printf 'Posture:     GOVERNED\n'
  printf 'Platform:    Ubuntu 24.04 LTS linux/amd64 (systemd, rootless Docker)\n'
  printf 'Stem:        %s\n' "$STEM_USER"
  printf 'Pollinator:  %s\n' "$pollinator_user"
  printf 'Protected:   %s\n' "$STEM_BIN"
  printf 'MCP client:  %s/.local/bin/tendril-mcp\n' "$pollinator_home"
  printf 'Rootless:    verified (docker info SecurityOptions contains rootless)\n'
  printf 'systemd:     %s installed; not enabled; not started\n' "$UNIT_PATH"
  printf '\n'
  printf 'Interactive login as %s is not required.\n' "$STEM_USER"
  printf 'This installer did not run tendril init and did not start the Stem.\n'
  printf 'It did not create provider credentials, Pollinator credentials, grants, or Substrates.\n'
  printf '\n'
  printf 'Next, complete configuration as the Stem from %s:\n' "$STEM_HOME"
  printf '\n'
  printf "  sudo -u %s -H bash -lc 'cd %s && export DOCKER_HOST=unix:///run/user/%s/docker.sock && %s init'\n" \
    "$STEM_USER" "$STEM_HOME" "$tendril_uid" "$STEM_BIN"
  printf '\n'
  printf "  sudo -u %s -H bash -lc 'cd %s && %s hardiness'\n" \
    "$STEM_USER" "$STEM_HOME" "$STEM_BIN"
  printf '\n'
  printf 'Then follow Stage 5 (Git connection) and Stage 6 (Pollinator credentials):\n'
  printf '  https://github.com/opentendril/opentendril/blob/main/docs/GUIDE-INSTALL.md\n'
  printf '\n'
  printf 'When configuration is complete, start the Stem:\n'
  printf '\n'
  printf '  systemctl enable --now tendril\n'
}

require_governed_platform() {
  require_cmd id
  _uid=$(id -u) || die "id -u failed"
  if [ "$_uid" != 0 ]; then
    die "governed installation requires root (effective uid 0). Re-run as root, for example: sudo sh install.sh --governed --pollinator-user <account>"
  fi
  detect_platform
  if [ "$os" != linux ]; then
    die "governed installation requires Linux; ${raw_os} is not a qualified governed platform (Ubuntu 24.04 LTS linux/amd64 only)"
  fi
  if [ "$wsl" -eq 1 ]; then
    die "governed installation does not support WSL. The qualified governed platform is Ubuntu 24.04 LTS linux/amd64 with systemd and rootless Docker."
  fi
  if [ "$arch" != amd64 ]; then
    die "governed installation requires linux/amd64; linux/${arch} is not qualified"
  fi
  parse_os_release
  if [ "$os_id" != ubuntu ]; then
    die "governed installation requires Ubuntu; /etc/os-release ID=${os_id} is not qualified (Ubuntu 24.04 LTS only)"
  fi
  if [ "$os_version_id" != 24.04 ]; then
    die "governed installation requires Ubuntu 24.04 LTS; VERSION_ID=${os_version_id} is not qualified"
  fi
}

install_governed() {
  require_governed_platform
  require_cmd cat
  require_cmd stat
  require_cmd getent
  require_cmd install
  require_cmd systemctl
  require_cmd dpkg
  require_cmd dpkg-query
  resolve_pollinator
  classify_docker
  if [ "$docker_class" = unsafe ]; then
    die "$docker_reason"
  fi
  archive="${ARCHIVE_PREFIX}-${os}-${arch}.tar.gz"
  prepare_workdir governed_cleanup
  install_prereq_packages
  obtain_verified_archive
  ensure_tendril_principal
  install_docker_engine
  establish_rootless_docker
  install_governed_binaries
  ensure_control_plane
  install_tendril_unit
  enforce_p2
  governed_finished=1
  print_governed_success
}

install_local() {
  [ -n "${HOME:-}" ] || die "HOME is not set"
  detect_platform
  archive="${ARCHIVE_PREFIX}-${os}-${arch}.tar.gz"
  dest_dir="${HOME}/${DEST_SUBDIR}"
  dest_path="${dest_dir}/${DEST_NAME}"
  prepare_workdir cleanup
  obtain_verified_archive
  extract_tendril
  install_tendril
  print_success
}

main() {
  parse_args "$@"
  if [ "$want_help" -eq 1 ]; then
    usage
    exit 0
  fi
  if [ "$governed" -eq 1 ]; then
    install_governed
    return
  fi
  if [ -n "$pollinator_user" ]; then
    die "--pollinator-user is only valid with --governed"
  fi
  install_local
}

main "$@"
