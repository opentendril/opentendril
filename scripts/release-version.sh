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
#   scripts/release-version.sh intended-tag
#   scripts/release-version.sh publication-state <source-sha>
#
# publication-state classifies first-publication vs safe-retry against
# authoritative remote main and remote tags. It does not create, move,
# delete, or push tags, and it does not write VERSION.
set -euo pipefail

usage() {
  cat <<'EOF' >&2
Usage:
  scripts/release-version.sh current
  scripts/release-version.sh next <patch|minor|major>
  scripts/release-version.sh bump <patch|minor|major>
  scripts/release-version.sh check
  scripts/release-version.sh intended-tag
  scripts/release-version.sh publication-state <source-sha>
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

is_full_sha() {
  [[ "${1}" =~ ^[0-9a-fA-F]{40}$ ]]
}

normalize_sha() {
  printf '%s' "${1}" | tr 'A-F' 'a-f'
}

# Run `git ls-remote` with the remaining arguments. The listing file receives
# stdout only after a successful exit. A failed query is never treated as an
# empty listing: that would confuse "could not read history" with "absent."
#
# `purpose` is interpolated into the fail-closed diagnostic. `remote` is used
# only to include the remote URL in that diagnostic.
run_git_ls_remote() {
  local listing="$1" remote="$2" purpose="$3"
  shift 3
  local err url detail

  err="$(mktemp)"
  if ! git ls-remote "$@" >"${listing}" 2>"${err}"; then
    url="$(git remote get-url "${remote}" 2>/dev/null || printf '%s' "${remote}")"
    detail="$(tr '\n' ' ' <"${err}" | sed 's/[[:space:]]\{1,\}/ /g; s/^ //; s/ $//')"
    rm -f "${listing}" "${err}"
    if [ -n "${detail}" ]; then
      die "failed to query ${purpose} on ${remote} (${url}): ${detail}"
    fi
    die "failed to query ${purpose} on ${remote} (${url})"
  fi
  rm -f "${err}"
}

# Highest stable vMAJOR.MINOR.PATCH tag on the remote. Empty if none exist.
# Local tags are ignored: only git ls-remote output is used.
published_baseline() {
  local remote="$1"
  local listing
  local sha ref tag latest=""

  listing="$(mktemp)"
  run_git_ls_remote "${listing}" "${remote}" "published tags" --tags "${remote}"

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
  done <"${listing}"
  rm -f "${listing}"

  printf '%s\n' "${latest}"
}

# Commit currently named by remote refs/heads/main. Fail closed if the query
# fails, the ref is missing, or more than one SHA is reported.
remote_main_sha() {
  local remote="$1"
  local listing sha="" ref_col sha_col

  listing="$(mktemp)"
  run_git_ls_remote "${listing}" "${remote}" "current main" "${remote}" "refs/heads/main"

  while IFS=$'\t' read -r sha_col ref_col; do
    [ "${ref_col}" = "refs/heads/main" ] || continue
    if [ -n "${sha}" ]; then
      rm -f "${listing}"
      die "remote ${remote} reported multiple SHAs for refs/heads/main"
    fi
    sha="${sha_col}"
  done <"${listing}"
  rm -f "${listing}"

  [ -n "${sha}" ] || die "failed to resolve current main on ${remote}"
  is_full_sha "${sha}" || die "remote main SHA is not a full commit: ${sha}"
  printf '%s\n' "$(normalize_sha "${sha}")"
}

# Commit the remote intended tag points at. Empty if the tag is absent.
# Annotated tags use the peeled commit (^{}); lightweight tags use the direct
# ref. Local tags are never consulted.
remote_tag_commit() {
  local remote="$1" tag="$2"
  local listing sha ref peeled="" direct=""

  listing="$(mktemp)"
  run_git_ls_remote \
    "${listing}" \
    "${remote}" \
    "published tags" \
    --tags \
    "${remote}" \
    "refs/tags/${tag}" \
    "refs/tags/${tag}^{}"

  while IFS=$'\t' read -r sha ref; do
    case "${ref}" in
      "refs/tags/${tag}^{}") peeled="${sha}" ;;
      "refs/tags/${tag}") direct="${sha}" ;;
    esac
  done <"${listing}"
  rm -f "${listing}"

  if [ -n "${peeled}" ]; then
    is_full_sha "${peeled}" || die "remote tag ${tag} peeled SHA is not a full commit: ${peeled}"
    printf '%s\n' "$(normalize_sha "${peeled}")"
  elif [ -n "${direct}" ]; then
    is_full_sha "${direct}" || die "remote tag ${tag} SHA is not a full commit: ${direct}"
    printf '%s\n' "$(normalize_sha "${direct}")"
  fi
}

assert_first_publication_version() {
  local current="$1" published="$2" tag="$3" remote="$4"
  local next_patch next_minor next_major

  if [ -z "${published}" ]; then
    return 0
  fi
  if [ "${current}" = "${published}" ]; then
    die "VERSION ${current} matches published baseline but tag ${tag} is absent on ${remote}"
  fi

  next_patch="$(increment_version "${published}" patch)"
  next_minor="$(increment_version "${published}" minor)"
  next_major="$(increment_version "${published}" major)"
  if [ "${current}" != "${next_patch}" ] &&
    [ "${current}" != "${next_minor}" ] &&
    [ "${current}" != "${next_major}" ]; then
    die "VERSION ${current} is not a single patch, minor, or major increment from published baseline ${published}"
  fi
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

cmd_intended_tag() {
  local version
  version="$(read_version "$(version_path)")"
  printf 'v%s\n' "${version}"
}

# Classify publication against remote main and the intended remote tag.
# stdout is GitHub Actions output lines. Local tags are not authority.
cmd_publication_state() {
  local source current remote main_sha tag tag_commit published state

  [ "$#" -eq 1 ] || die "publication-state requires exactly one source commit SHA"
  is_full_sha "${1}" || die "source commit must be a full 40-character SHA-1 (got: ${1:-<missing>})"
  source="$(normalize_sha "${1}")"

  current="$(read_version "$(version_path)")"
  tag="v${current}"
  remote="$(require_remote)"
  main_sha="$(remote_main_sha "${remote}")"

  if [ "${main_sha}" != "${source}" ]; then
    die "publication source ${source} is not current remote main ${main_sha}; refusing to publish a stale commit"
  fi

  tag_commit="$(remote_tag_commit "${remote}" "${tag}")"

  if [ -z "${tag_commit}" ]; then
    published="$(published_baseline "${remote}")"
    assert_first_publication_version "${current}" "${published}" "${tag}" "${remote}"
    state="first-publication"
  elif [ "${tag_commit}" = "${source}" ]; then
    state="safe-retry"
  else
    die "intended tag ${tag} exists at ${tag_commit}, not publication source ${source}; refusing to move or overwrite it"
  fi

  printf 'version=%s\n' "${current}"
  printf 'intended-tag=%s\n' "${tag}"
  printf 'state=%s\n' "${state}"
  printf 'source-sha=%s\n' "${source}"
  printf 'main-sha=%s\n' "${main_sha}"
  printf 'tag-commit=%s\n' "${tag_commit}"
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
    intended-tag)
      [ "$#" -eq 0 ] || die "intended-tag takes no arguments"
      cmd_intended_tag
      ;;
    publication-state)
      [ "$#" -eq 1 ] || die "publication-state requires exactly one source commit SHA"
      cmd_publication_state "$1"
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
