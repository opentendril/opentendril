#!/usr/bin/env bash
# Canonical release-version contract.
#
# VERSION is the only repository source of release identity. This script is the
# deterministic way to read it, calculate the next patch/minor/major, apply a
# requested bump, or check that the file is a stable semantic version aligned
# with published remote tag history.
#
# Remote tags are queried in place (git ls-remote). Local tags are never
# consulted, so a stale or conflicting local tag cannot become authority.
#
# Preparation writes VERSION and does nothing else: no tag, no push, no
# pull request, no publication, and no update of Git refs.
#
# Usage:
#   scripts/release-version.sh current
#   scripts/release-version.sh next <patch|minor|major>
#   scripts/release-version.sh bump <patch|minor|major>
#   scripts/release-version.sh check
set -euo pipefail

usage() {
  cat <<'EOF' >&2
Usage:
  scripts/release-version.sh current
  scripts/release-version.sh next <patch|minor|major>
  scripts/release-version.sh bump <patch|minor|major>
  scripts/release-version.sh check
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

repo_root() {
  git rev-parse --show-toplevel 2>/dev/null || die "must be run from a repository root"
}

version_path() {
  printf '%s/VERSION' "$(repo_root)"
}

is_stable_semver() {
  [[ "${1}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

# Reads VERSION as exactly MAJOR.MINOR.PATCH, optionally followed by one newline.
read_version() {
  local path="$1"
  local bytes line

  [ -f "${path}" ] || die "VERSION file is missing: ${path}"
  [ -s "${path}" ] || die "VERSION file is empty: ${path}"

  bytes="$(wc -c < "${path}" | tr -d '[:space:]')"
  IFS= read -r line < "${path}" || true
  [ -n "${line}" ] || die "VERSION file is empty: ${path}"

  if [ "${bytes}" -ne "${#line}" ] && [ "${bytes}" -ne $((${#line} + 1)) ]; then
    die "VERSION must contain only MAJOR.MINOR.PATCH"
  fi

  is_stable_semver "${line}" || die "VERSION is not a stable MAJOR.MINOR.PATCH semantic version: ${line}"
  printf '%s\n' "${line}"
}

parse_kind() {
  case "${1}" in
    patch | minor | major) printf '%s\n' "${1}" ;;
    *) die "kind must be patch, minor, or major (got: ${1:-<missing>})" ;;
  esac
}

increment_version() {
  local version="$1" kind="$2"
  local major minor patch

  is_stable_semver "${version}" || die "cannot increment invalid version: ${version}"
  IFS=. read -r major minor patch <<<"${version}"

  case "${kind}" in
    patch) printf '%s.%s.%s\n' "${major}" "${minor}" "$((patch + 1))" ;;
    minor) printf '%s.%s.0\n' "${major}" "$((minor + 1))" ;;
    major) printf '%s.0.0\n' "$((major + 1))" ;;
    *) die "kind must be patch, minor, or major (got: ${kind})" ;;
  esac
}

version_greater() {
  local a_major a_minor a_patch b_major b_minor b_patch
  IFS=. read -r a_major a_minor a_patch <<<"$1"
  IFS=. read -r b_major b_minor b_patch <<<"$2"

  if [ "${a_major}" -gt "${b_major}" ]; then return 0; fi
  if [ "${a_major}" -lt "${b_major}" ]; then return 1; fi
  if [ "${a_minor}" -gt "${b_minor}" ]; then return 0; fi
  if [ "${a_minor}" -lt "${b_minor}" ]; then return 1; fi
  [ "${a_patch}" -gt "${b_patch}" ]
}

remote_name() {
  printf '%s\n' "${RELEASE_VERSION_REMOTE:-origin}"
}

require_remote() {
  local remote
  remote="$(remote_name)"
  git remote get-url "${remote}" >/dev/null 2>&1 || die "remote '${remote}' is required to consult published tag history"
  printf '%s\n' "${remote}"
}

# Highest stable vMAJOR.MINOR.PATCH tag on the remote. Empty if none exist.
# Local tags are ignored: only git ls-remote output is used.
published_baseline() {
  local remote="$1"
  local sha ref tag latest=""

  while IFS=$'\t' read -r sha ref; do
    [ -n "${ref}" ] || continue
    case "${ref}" in
      refs/tags/*^{}) continue ;;
      refs/tags/v*)
        tag="${ref#refs/tags/v}"
        if is_stable_semver "${tag}"; then
          if [ -z "${latest}" ] || version_greater "${tag}" "${latest}"; then
            latest="${tag}"
          fi
        fi
        ;;
    esac
  done < <(git ls-remote --tags "${remote}")

  printf '%s\n' "${latest}"
}

write_version() {
  local path="$1" version="$2"
  local tmp

  is_stable_semver "${version}" || die "refusing to write invalid version: ${version}"
  tmp="$(mktemp "${path}.tmp.XXXXXX")"
  printf '%s\n' "${version}" > "${tmp}"
  mv -f "${tmp}" "${path}"
}

cmd_current() {
  read_version "$(version_path)"
}

cmd_next() {
  local kind version
  kind="$(parse_kind "${1:-}")"
  version="$(read_version "$(version_path)")"
  increment_version "${version}" "${kind}"
}

cmd_bump() {
  local kind path current published next remote
  kind="$(parse_kind "${1:-}")"
  path="$(version_path)"
  current="$(read_version "${path}")"
  remote="$(require_remote)"
  published="$(published_baseline "${remote}")"

  if [ -z "${published}" ]; then
    die "no published release tags on ${remote}; refusing to bump an unpublished VERSION (${current})"
  fi
  if [ "${current}" != "${published}" ]; then
    die "refusing to bump: VERSION ${current} is not the published baseline ${published}"
  fi

  next="$(increment_version "${current}" "${kind}")"
  write_version "${path}" "${next}"
  printf '%s\n' "${next}"
}

cmd_check() {
  local path current remote published next_patch next_minor next_major
  path="$(version_path)"
  current="$(read_version "${path}")"
  remote="$(require_remote)"
  published="$(published_baseline "${remote}")"

  if [ -n "${published}" ]; then
    next_patch="$(increment_version "${published}" patch)"
    next_minor="$(increment_version "${published}" minor)"
    next_major="$(increment_version "${published}" major)"
    if [ "${current}" != "${published}" ] &&
      [ "${current}" != "${next_patch}" ] &&
      [ "${current}" != "${next_minor}" ] &&
      [ "${current}" != "${next_major}" ]; then
      die "VERSION ${current} is not the published baseline ${published} and not a single patch, minor, or major increment from it"
    fi
  fi

  printf '%s\n' "${current}"
}

main() {
  local cmd="${1:-}"
  shift || true

  case "${cmd}" in
    current)
      [ "$#" -eq 0 ] || die "current takes no arguments"
      cmd_current
      ;;
    next)
      [ "$#" -eq 1 ] || die "next requires exactly one kind: patch, minor, or major"
      cmd_next "$1"
      ;;
    bump)
      [ "$#" -eq 1 ] || die "bump requires exactly one kind: patch, minor, or major"
      cmd_bump "$1"
      ;;
    check)
      [ "$#" -eq 0 ] || die "check takes no arguments"
      cmd_check
      ;;
    -h | --help | help | "")
      usage
      [ "${cmd}" = "-h" ] || [ "${cmd}" = "--help" ] || [ "${cmd}" = "help" ] || exit 1
      ;;
    *)
      usage
      die "unknown command: ${cmd}"
      ;;
  esac
}

main "$@"
