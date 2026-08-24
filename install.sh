#!/bin/sh
# OpenTendril local installer.
#
# LOCAL / SINGLE-PRINCIPAL bootstrap. Downloads an official GitHub Release
# archive, verifies SHA-256, and places only `tendril` at ~/.local/bin/tendril.
# Does not establish the governed P1-P5 boundary. Does not run tendril.
#
# This script must work when supplied on stdin:
#   curl -fsSL <release>/install.sh | sh
# Do not consult $0 as a filesystem path.

set -eu

RELEASE_ROOT="https://github.com/opentendril/opentendril/releases"
ARCHIVE_PREFIX="opentendril"
DEST_SUBDIR=".local/bin"
DEST_NAME="tendril"

version="${OPENTENDRIL_VERSION:-}"
want_help=0
workdir=""

die() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "${workdir:-}" ] && [ -d "$workdir" ]; then
    rm -rf "$workdir"
  fi
}

usage() {
  cat <<'EOF'
OpenTendril local installer (LOCAL / SINGLE-PRINCIPAL)

Usage:
  sh install.sh [--version <tag>]
  OPENTENDRIL_VERSION=<tag> sh install.sh
  curl -fsSL <url>/install.sh | sh
  curl -fsSL <url>/install.sh | sh -s -- --version <tag>

Options:
  --version <tag>   Pin to one GitHub Release (v0.3.0 or 0.3.0).
                    Also accepted as OPENTENDRIL_VERSION.
  --governed        Reserved. Not implemented. Exits without installing.
  -h, --help        Show this help.

Supported local platforms:
  linux/amd64, linux/arm64 (including WSL2)
  darwin/amd64, darwin/arm64

Installs only tendril to ~/.local/bin/tendril. Does not install tendril-mcp,
modify shell profiles, or establish the governed P1-P5 boundary.
EOF
}

refuse_governed() {
  cat <<'EOF' >&2
install.sh: --governed is reserved and is not implemented.

This installer provides LOCAL / SINGLE-PRINCIPAL installation only.
It does not create a tendril operating-system principal, install tendril-mcp,
configure a system service, or establish the governed P1-P5 boundary.
EOF
  exit 1
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
        refuse_governed
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
    elif [ -r /proc/sys/kernel/osrelease ]; then
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

extract_tendril() {
  command -v tar >/dev/null 2>&1 || die "tar is required"
  tar -C "$workdir" -xzf "$archive_path" tendril </dev/null || die "failed to extract tendril from ${archive}"
  extracted="${workdir}/tendril"
  if [ -L "$extracted" ] || [ ! -f "$extracted" ]; then
    die "archive member tendril is not a regular file"
  fi
  [ -s "$extracted" ] || die "extracted tendril is empty"
  if [ -e "${workdir}/tendril-mcp" ]; then
    die "tendril-mcp was extracted; local mode installs only tendril"
  fi
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

main() {
  parse_args "$@"
  if [ "$want_help" -eq 1 ]; then
    usage
    exit 0
  fi
  [ -n "${HOME:-}" ] || die "HOME is not set"
  detect_platform
  archive="${ARCHIVE_PREFIX}-${os}-${arch}.tar.gz"
  dest_dir="${HOME}/${DEST_SUBDIR}"
  dest_path="${dest_dir}/${DEST_NAME}"
  base=$(release_base)

  trap cleanup EXIT
  trap 'cleanup; exit 1' INT HUP TERM
  workdir=$(mktemp -d "${TMPDIR:-/tmp}/opentendril-install.XXXXXX") || die "cannot create a temporary directory"
  checksums_path="${workdir}/checksums.txt"
  archive_path="${workdir}/${archive}"

  download "${base}/checksums.txt" "$checksums_path"
  download "${base}/${archive}" "$archive_path"
  verify_archive
  extract_tendril
  install_tendril
  print_success
}

main "$@"
