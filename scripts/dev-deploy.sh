#!/bin/bash
# Deploy the Stem, the MCP client, and Greenhouse from one clean checkout.
# Container start, stop, health, and the fixed runtime contract come from
# scripts/greenhouse-lifecycle.sh, the same text the governed installer
# embeds in the Greenhouse wrapper. This command does not retag a verified
# release image and does not modify the governed wrapper.
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO=$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd -P)
LIFECYCLE="${SCRIPT_DIR}/greenhouse-lifecycle.sh"

STEM_USER=tendril
EXPECTED_STEM_HOME=/home/tendril
GREENHOUSE_WRAPPER=/usr/local/sbin/opentendril-greenhouse
TRANSPORT_DIR=/var/lib/opentendril-transport
STEM_SOCKET=/var/lib/opentendril-transport/stem.sock
SOCKET_CHECK_PATH=$STEM_SOCKET
EXPECTED_ACTIVE=opentendril-greenhouse
EXPECTED_RELEASE_HOLD=opentendril-greenhouse-release
EXPECTED_DEV_HOLD=opentendril-greenhouse-dev
EXPECTED_DEV_TAG=opentendril-greenhouse:dev
ACTIVE=$EXPECTED_ACTIVE
RELEASE_HOLD=$EXPECTED_RELEASE_HOLD
DEV_HOLD=$EXPECTED_DEV_HOLD
DEV_TAG=$EXPECTED_DEV_TAG
CONTAINER=$ACTIVE

ID_BIN=/usr/bin/id
STAT_BIN=/usr/bin/stat
GETENT_BIN=/usr/bin/getent
READLINK_BIN=/usr/bin/readlink
GIT_BIN=/usr/bin/git
GO_BIN=/usr/bin/go
CURL_BIN=/usr/bin/curl
SYSTEMCTL_BIN=/usr/bin/systemctl
INSTALL_BIN=/usr/bin/install
SHA256_BIN=/usr/bin/sha256sum
FIND_BIN=/usr/bin/find
CP_BIN=/usr/bin/cp
CHMOD_BIN=/usr/bin/chmod
CHOWN_BIN=/usr/bin/chown
RM_BIN=/usr/bin/rm
MKTEMP_BIN=/usr/bin/mktemp
CMP_BIN=/usr/bin/cmp
AWK_BIN=/usr/bin/awk
SORT_BIN=/usr/bin/sort
TR_BIN=/usr/bin/tr
GREP_BIN=/usr/bin/grep
RUNUSER_BIN=/usr/sbin/runuser
ENV_BIN=/usr/bin/env
DOCKER_BIN=/usr/bin/docker

log() { printf 'opentendril-dev-deploy: %s\n' "$*"; }
fail() {
  printf 'opentendril-dev-deploy: FAILED: %s\n' "$*" >&2
  exit 1
}

run_restore_attempt() {
  (
    trap - EXIT
    set -euo pipefail
    RESTORING=1
    "$@"
  )
}

usage() {
  printf 'Usage: sudo make dev-deploy\n       sudo make dev-rollback\n' >&2
  exit 2
}

require_tool() {
  local path=$1
  [ -x "$path" ] || fail "required program is missing or not executable: ${path}"
  [ -f "$path" ] || fail "required program is not a file: ${path}"
}

require_ordinary_name() {
  local name=$1
  local what=${2:-account}
  case "$name" in
    ''|*[!a-zA-Z0-9._-]*)
      fail "invalid ${what} name"
      ;;
  esac
}

read_passwd() {
  local user=$1
  local line
  line=$("$GETENT_BIN" passwd "$user" 2>/dev/null) || fail "account ${user} does not exist"
  IFS=: read -r pw_name pw_pass pw_uid pw_gid pw_gecos pw_home pw_shell <<<"$line"
  [ "$pw_name" = "$user" ] || fail "account lookup for ${user} returned ${pw_name}"
  case "$pw_uid" in
    ''|*[!0-9]*) fail "account ${user} has an unusable uid" ;;
  esac
  [ -n "$pw_home" ] || fail "account ${user} has no home directory"
  case "$pw_home" in
    /*) ;;
    *) fail "account ${user} home is not absolute" ;;
  esac
}

refuse_symlink() {
  if [ -L "$1" ]; then
    fail "${1} is a symlink"
  fi
}

owner_of() { "$STAT_BIN" -c '%U' "$1"; }
group_of() { "$STAT_BIN" -c '%G' "$1"; }
mode_of() { "$STAT_BIN" -c '%a' "$1"; }

refuse_other_writable() {
  local mode other
  mode=$(mode_of "$1")
  other=${mode: -1}
  case "$other" in
    2|3|6|7) fail "${1} is writable by users other than its owner" ;;
  esac
}

require_owned_dir() {
  local path=$1 user=$2
  [ -d "$path" ] || fail "${path} is missing or is not a directory"
  refuse_symlink "$path"
  [ "$(owner_of "$path")" = "$user" ] || fail "${path} is owned by $(owner_of "$path"), expected ${user}"
  refuse_other_writable "$path"
}

ensure_owned_dir() {
  local user=$1 group=$2 mode=$3 path=$4 parent
  if [ -e "$path" ]; then
    [ -d "$path" ] || fail "${path} exists and is not a directory"
    refuse_symlink "$path"
    [ "$(owner_of "$path")" = "$user" ] || fail "${path} is owned by $(owner_of "$path"), expected ${user}; refusing to take ownership"
    [ "$(group_of "$path")" = "$group" ] || fail "${path} group is $(group_of "$path"), expected ${group}; refusing to change it"
    refuse_other_writable "$path"
    return 0
  fi
  parent=$(dirname -- "$path")
  [ -d "$parent" ] || fail "${parent} is missing; refusing to create ${path}"
  refuse_symlink "$parent"
  [ "$(owner_of "$parent")" = "$user" ] || fail "${parent} is owned by $(owner_of "$parent"), expected ${user}; refusing to create ${path}"
  "$INSTALL_BIN" -d -o "$user" -g "$group" -m "$mode" "$path" </dev/null
  [ "$(owner_of "$path")" = "$user" ] || fail "created ${path} is not owned by ${user}"
  [ "$(group_of "$path")" = "$group" ] || fail "created ${path} group is not ${group}"
}

run_as() {
  local user=$1 home=$2
  shift 2
  "$RUNUSER_BIN" -u "$user" -- "$ENV_BIN" -i \
    PATH=/usr/bin:/bin \
    HOME="$home" USER="$user" LOGNAME="$user" \
    "$@"
}

git_as() {
  local user=$1 home=$2
  shift 2
  run_as "$user" "$home" GIT_OPTIONAL_LOCKS=0 "$GIT_BIN" --no-optional-locks "$@"
}

canonical_version() {
  local version
  [ -f "${REPO}/VERSION" ] || fail "VERSION is missing"
  version=$("$TR_BIN" -d '[:space:]' <"${REPO}/VERSION")
  printf '%s\n' "$version" | "$GREP_BIN" -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
    fail "VERSION is not MAJOR.MINOR.PATCH"
  printf '%s\n' "$version"
}

release_tag() {
  printf 'opentendril-greenhouse:%s\n' "$(canonical_version)"
}

dev_version() {
  printf '%s+dev\n' "$(canonical_version)"
}

source_sha() {
  local sha dirty top physical
  sha=$(git_as "$pollinator_user" "$pollinator_home" -C "$REPO" rev-parse HEAD)
  printf '%s\n' "$sha" | "$GREP_BIN" -Eq '^[0-9a-f]{40}$' || fail "checkout revision is not a full commit SHA"
  dirty=$(git_as "$pollinator_user" "$pollinator_home" -C "$REPO" status --porcelain)
  [ -z "$dirty" ] || fail "checkout is dirty; refusing to deploy a mixed tree"
  top=$(git_as "$pollinator_user" "$pollinator_home" -C "$REPO" rev-parse --show-toplevel)
  physical=$(CDPATH= cd -- "$top" && pwd -P)
  [ "$physical" = "$REPO" ] || fail "script location is not the checkout toplevel"
  printf '%s\n' "$sha"
}

vcs_field() {
  local bin=$1 key=$2
  "$GO_BIN" version -m "$bin" | "$AWK_BIN" -v key="$key" '$1=="build" && index($2, key "=")==1 { print substr($2, length(key)+2); found=1; exit } END { if (!found) exit 1 }'
}

assert_binary_revision() {
  local bin=$1 label=$2 sha=$3 version=$4
  local got_rev got_mod got_ver
  got_rev=$(vcs_field "$bin" vcs.revision) || fail "${label} has no vcs.revision"
  got_mod=$(vcs_field "$bin" vcs.modified) || fail "${label} has no vcs.modified"
  [ "$got_rev" = "$sha" ] || fail "${label} revision ${got_rev} does not match checkout ${sha}"
  [ "$got_mod" = "false" ] || fail "${label} was built from a modified tree"
  got_ver=$("$bin" --version)
  case "$label" in
    tendril) [ "$got_ver" = "tendril ${version}" ] || fail "tendril --version is '${got_ver}', expected 'tendril ${version}'" ;;
    tendril-mcp) [ "$got_ver" = "tendril-mcp ${version}" ] || fail "tendril-mcp --version is '${got_ver}', expected 'tendril-mcp ${version}'" ;;
  esac
}

http_code() {
  local code
  code=$("$CURL_BIN" -sS --max-time 5 -o /dev/null -w '%{http_code}' -- "$1" || true)
  [ -n "$code" ] || code=000
  printf '%s' "$code"
}

wait_http() {
  local url=$1 want=$2 i code
  i=0
  while [ "$i" -lt 60 ]; do
    code=$(http_code "$url")
    [ "$code" = "$want" ] && return 0
    i=$((i + 1))
    sleep 1
  done
  printf 'opentendril-dev-deploy: %s returned %s, expected %s\n' "$url" "$code" "$want" >&2
  return 1
}

assert_release_image_untouched() {
  local tag id
  tag=$(release_tag)
  [ "$tag" != "$DEV_TAG" ] || fail "development tag collides with the release tag"
  id=$(rootless_docker image inspect --format '{{.Id}}' "$tag") || fail "release image tag ${tag} is missing"
  [ "$id" = "$RELEASE_IMAGE_ID" ] || fail "release image ${tag} changed from ${RELEASE_IMAGE_ID} to ${id}"
}

assert_wrapper_untouched() {
  local now
  now=$("$SHA256_BIN" "$GREENHOUSE_WRAPPER" | "$AWK_BIN" '{print $1}')
  [ "$now" = "$WRAPPER_SHA" ] || fail "governed Greenhouse wrapper changed"
}

assert_docker_root() {
  local root
  root=$(rootless_docker info --format '{{.DockerRootDir}}') || fail "cannot read Docker root"
  [ "$root" = "${STEM_HOME}/.local/share/docker" ] ||
    fail "refusing deployment: Docker root is ${root}, expected ${STEM_HOME}/.local/share/docker"
}

image_of() {
  rootless_docker container inspect --format '{{.Image}}' "$1"
}

container_exists() {
  rootless_docker container inspect "$1" >/dev/null 2>&1
}

assert_not_release_image() {
  local name=$1 image
  image=$(image_of "$name")
  [ "$image" != "$RELEASE_IMAGE_ID" ] || fail "refusing to remove ${name}; it is the verified release image"
}

stop_and_unrestart() {
  local name=$1
  if container_exists "$name"; then
    rootless_docker container stop "$name" >/dev/null
    rootless_docker update --restart=no "$name" >/dev/null
  fi
}

start_named() {
  local name=$1
  rootless_docker update --restart=unless-stopped "$name" >/dev/null
  rootless_docker container start "$name" >/dev/null
}

park_release_container() {
  local current=""
  if container_exists "$ACTIVE"; then
    current=$(image_of "$ACTIVE")
  fi
  if [ "$current" = "$RELEASE_IMAGE_ID" ]; then
    if container_exists "$RELEASE_HOLD"; then
      fail "release container is still named ${ACTIVE} while ${RELEASE_HOLD} also exists"
    fi
    log "parking the running release container as ${RELEASE_HOLD}"
    stop_and_unrestart "$ACTIVE"
    rootless_docker rename "$ACTIVE" "$RELEASE_HOLD" >/dev/null
    RELEASE_CONTAINER_ID=$(rootless_docker container inspect --format '{{.Id}}' "$RELEASE_HOLD")
    NEED_RESTORE=1
  elif container_exists "$RELEASE_HOLD"; then
    RELEASE_CONTAINER_ID=$(rootless_docker container inspect --format '{{.Id}}' "$RELEASE_HOLD")
    [ "$(image_of "$RELEASE_HOLD")" = "$RELEASE_IMAGE_ID" ] || fail "parked container ${RELEASE_HOLD} is not the release image"
    NEED_RESTORE=1
  else
    fail "no release Greenhouse container is available to preserve for rollback"
  fi
}

remove_dev_active_if_present() {
  if ! container_exists "$ACTIVE"; then
    return 0
  fi
  assert_not_release_image "$ACTIVE"
  log "replacing development container ${ACTIVE}"
  stop_and_unrestart "$ACTIVE"
  rootless_docker container rm "$ACTIVE" >/dev/null
}

wait_container_healthy() {
  local i row state rest health
  i=0
  while [ "$i" -lt 45 ]; do
    row=$(get_container_row) || fail "could not read Greenhouse health"
    state=${row%%|*}
    rest=${row#*|}
    health=${rest%%|*}
    if [ "$state" = running ] && [ "$health" = healthy ]; then
      return 0
    fi
    if [ "$health" = unhealthy ]; then
      rootless_docker logs --tail 80 "$CONTAINER" >&2 || true
      fail "container ${CONTAINER} is unhealthy"
    fi
    i=$((i + 1))
    sleep 2
  done
  rootless_docker logs --tail 80 "$CONTAINER" >&2 || true
  fail "container ${CONTAINER} did not become healthy (last status: ${state:-unknown}/${health:-unknown})"
}

assert_single_publisher() {
  local running count
  running=$(rootless_docker ps --filter status=running --format '{{.Names}} {{.Ports}}' | "$GREP_BIN" -E '127\.0\.0\.1:4173->8080' || true)
  count=$(printf '%s\n' "$running" | "$AWK_BIN" 'NF { n++ } END { print n+0 }')
  [ "$count" -eq 1 ] || fail "expected one Greenhouse publisher on 127.0.0.1:4173, found: ${running:-none}"
  case "$running" in
    "${ACTIVE} "*) ;;
    *) fail "port 4173 is published by an unexpected container: ${running}" ;;
  esac
  case "$running" in
    *0.0.0.0:*) fail "Greenhouse is published beyond loopback: ${running}" ;;
  esac
}

verify_runtime() {
  local expected_image=$1
  wait_http http://127.0.0.1:8080/health 200
  wait_http http://127.0.0.1:4173/health 200
  [ "$(http_code http://127.0.0.1:4173/v1/sessions)" = "401" ] || fail "Greenhouse proxy did not reject an unauthenticated session list"
  [ "$(http_code http://127.0.0.1:8080/v1/sessions)" = "401" ] || fail "Stem did not reject an unauthenticated session list"
  wait_container_healthy
  assert_single_publisher
  [ "$(image_of "$ACTIVE")" = "$expected_image" ] || fail "running Greenhouse image is not ${expected_image}"
}

swap_to_release() {
  [ -n "${RELEASE_CONTAINER_ID:-}" ] || fail "release container id is unknown"
  rootless_docker container inspect "$RELEASE_CONTAINER_ID" >/dev/null 2>&1 || fail "release container ${RELEASE_CONTAINER_ID} is gone"
  if container_exists "$ACTIVE"; then
    local image
    image=$(image_of "$ACTIVE")
    if [ "$image" = "$RELEASE_IMAGE_ID" ]; then
      start_named "$ACTIVE"
      return 0
    fi
    stop_and_unrestart "$ACTIVE"
    if container_exists "$DEV_HOLD"; then
      assert_not_release_image "$DEV_HOLD"
      rootless_docker container rm "$DEV_HOLD" >/dev/null || fail "could not clear ${DEV_HOLD}"
    fi
    rootless_docker rename "$ACTIVE" "$DEV_HOLD" >/dev/null
  fi
  local release_name
  release_name=$(rootless_docker container inspect --format '{{.Name}}' "$RELEASE_CONTAINER_ID")
  release_name=${release_name#/}
  if [ "$release_name" != "$ACTIVE" ]; then
    rootless_docker rename "$release_name" "$ACTIVE" >/dev/null
  fi
  start_named "$ACTIVE"
}

swap_to_dev_hold() {
  if ! container_exists "$DEV_HOLD"; then
    fail "development container ${DEV_HOLD} is missing; cannot return from rollback"
  fi
  assert_not_release_image "$DEV_HOLD"
  if container_exists "$ACTIVE"; then
    stop_and_unrestart "$ACTIVE"
    if [ "$(image_of "$ACTIVE")" = "$RELEASE_IMAGE_ID" ]; then
      if container_exists "$RELEASE_HOLD"; then
        fail "cannot park release container; ${RELEASE_HOLD} already exists"
      fi
      rootless_docker rename "$ACTIVE" "$RELEASE_HOLD" >/dev/null
      RELEASE_CONTAINER_ID=$(rootless_docker container inspect --format '{{.Id}}' "$RELEASE_HOLD")
    else
      assert_not_release_image "$ACTIVE"
      rootless_docker container rm "$ACTIVE" >/dev/null
    fi
  fi
  rootless_docker rename "$DEV_HOLD" "$ACTIVE" >/dev/null
  start_named "$ACTIVE"
}

snapshot_git() {
  local dest=$1 dir count substrates
  substrates="${STEM_HOME}/.tendril/substrates"
  {
    printf 'dev %s\n' "$(git_as "$pollinator_user" "$pollinator_home" -C "$REPO" rev-parse HEAD)"
    git_as "$pollinator_user" "$pollinator_home" -C "$REPO" status --porcelain
    if [ -d "$substrates" ]; then
      while IFS= read -r dir; do
        [ -n "$dir" ] || continue
        count=$(git_as "$STEM_USER" "$STEM_HOME" -C "$dir" status --porcelain | "$AWK_BIN" 'END { print NR+0 }')
        printf 'repo %s %s %s\n' "$dir" "$(git_as "$STEM_USER" "$STEM_HOME" -C "$dir" rev-parse HEAD)" "$count"
        git_as "$STEM_USER" "$STEM_HOME" -C "$dir" for-each-ref --format='ref %(objectname) %(refname)'
      done < <("$FIND_BIN" "$substrates" -mindepth 2 -maxdepth 3 -type d -name .git -printf '%h\n' | "$SORT_BIN")
    fi
  } >"$dest"
}

is_sha256() {
  local hex
  case "$1" in
    sha256:*) hex=${1#sha256:} ;;
    *) return 1 ;;
  esac
  [ "${#hex}" -eq 64 ] || return 1
  case "$hex" in
    *[!0-9a-f]*) return 1 ;;
  esac
  return 0
}

build_binaries() {
  local version sha
  version=$(dev_version)
  sha=$(source_sha)
  ensure_owned_dir "$pollinator_user" "$pollinator_group" 0750 "${pollinator_home}/.cache"
  ensure_owned_dir "$pollinator_user" "$pollinator_group" 0750 "$BUILD_DIR"
  log "building tendril and tendril-mcp ${version} at ${sha}"
  run_as "$pollinator_user" "$pollinator_home" \
    "$GO_BIN" build -C "$REPO" \
    -ldflags="-s -w -X github.com/opentendril/opentendril/internal/buildinfo.Version=${version}" \
    -o "${BUILD_DIR}/tendril" ./cmd/stem
  run_as "$pollinator_user" "$pollinator_home" \
    "$GO_BIN" build -C "$REPO" \
    -ldflags="-s -w -X github.com/opentendril/opentendril/internal/buildinfo.Version=${version}" \
    -o "${BUILD_DIR}/tendril-mcp" ./cmd/tendril-mcp
  [ "$(owner_of "${BUILD_DIR}/tendril")" = "$pollinator_user" ] || fail "built Stem is not owned by the Pollinator-hosting account"
  [ "$(owner_of "${BUILD_DIR}/tendril-mcp")" = "$pollinator_user" ] || fail "built MCP client is not owned by the Pollinator-hosting account"
  "$CHMOD_BIN" 0755 "${BUILD_DIR}/tendril" "${BUILD_DIR}/tendril-mcp"
  assert_binary_revision "${BUILD_DIR}/tendril" tendril "$sha" "$version"
  assert_binary_revision "${BUILD_DIR}/tendril-mcp" tendril-mcp "$sha" "$version"
  local dirty
  dirty=$(git_as "$pollinator_user" "$pollinator_home" -C "$REPO" status --porcelain)
  [ -z "$dirty" ] || fail "build dirtied the checkout"
}

install_mcp() {
  local version sha
  version=$(dev_version)
  sha=$(source_sha)
  ensure_owned_dir "$pollinator_user" "$pollinator_group" 0755 "${pollinator_home}/.local"
  ensure_owned_dir "$pollinator_user" "$pollinator_group" 0755 "${pollinator_home}/.local/bin"
  ensure_owned_dir "$pollinator_user" "$pollinator_group" 0750 "${pollinator_home}/.local/share"
  ensure_owned_dir "$pollinator_user" "$pollinator_group" 0750 "$MCP_ROLLBACK_DIR"
  if [ -f "$MCP_BIN" ]; then
    [ "$(owner_of "$MCP_BIN")" = "$pollinator_user" ] || fail "${MCP_BIN} is owned by $(owner_of "$MCP_BIN"), expected ${pollinator_user}"
    if [ ! -f "${MCP_ROLLBACK_DIR}/tendril-mcp" ]; then
      "$CP_BIN" -a "$MCP_BIN" "${MCP_ROLLBACK_DIR}/tendril-mcp"
    fi
  fi
  "$INSTALL_BIN" -o "$pollinator_user" -g "$pollinator_group" -m 0755 "${BUILD_DIR}/tendril-mcp" "$MCP_BIN" </dev/null
  assert_binary_revision "$MCP_BIN" tendril-mcp "$sha" "$version"
  log "MCP client installed at ${MCP_BIN} (${version}, ${sha})"
}

restore_previous_stem() {
  [ "${STEM_NEEDS_RESTORE:-0}" = 1 ] || return 0
  [ -f "${ROLLBACK_DIR}/tendril" ] || return 1
  log "restoring the previous Stem binary"
  "$CP_BIN" -a "${ROLLBACK_DIR}/tendril" "$STEM_BIN" || return 1
  "$CHOWN_BIN" "${STEM_USER}:${STEM_USER}" "$STEM_BIN" || return 1
  "$CHMOD_BIN" 0750 "$STEM_BIN" || return 1
  "$SYSTEMCTL_BIN" restart tendril.service || true
  STEM_NEEDS_RESTORE=0
}

install_stem_if_needed() {
  local version sha installed_rev running_pid running_exe version_line
  version=$(dev_version)
  sha=$(source_sha)
  [ -f "$STEM_BIN" ] || fail "protected Stem binary is missing: ${STEM_BIN}"
  installed_rev=$(vcs_field "$STEM_BIN" vcs.revision 2>/dev/null || true)
  running_pid=$("$SYSTEMCTL_BIN" show -p MainPID --value tendril.service)
  running_exe=""
  if [ -n "$running_pid" ] && [ "$running_pid" != "0" ]; then
    running_exe=$("$READLINK_BIN" "/proc/${running_pid}/exe" || true)
  fi
  version_line=$("$STEM_BIN" --version 2>/dev/null || true)
  if [ "$installed_rev" = "$sha" ] && [ "$version_line" = "tendril ${version}" ] && [ "$running_exe" = "$STEM_BIN" ]; then
    assert_binary_revision "$STEM_BIN" tendril "$sha" "$version"
    log "Stem already running at ${sha}; leaving the process in place"
    return 0
  fi
  ensure_owned_dir "$STEM_USER" "$STEM_USER" 0750 "${STEM_HOME}/.local"
  ensure_owned_dir "$STEM_USER" "$STEM_USER" 0750 "${STEM_HOME}/.local/share"
  ensure_owned_dir "$STEM_USER" "$STEM_USER" 0750 "$ROLLBACK_DIR"
  if [ ! -f "${ROLLBACK_DIR}/tendril" ]; then
    "$CP_BIN" -a "$STEM_BIN" "${ROLLBACK_DIR}/tendril"
    "$CHOWN_BIN" "${STEM_USER}:${STEM_USER}" "${ROLLBACK_DIR}/tendril"
    "$CHMOD_BIN" 0750 "${ROLLBACK_DIR}/tendril"
  fi
  log "installing Stem ${version} at ${sha} and restarting tendril.service"
  STEM_NEEDS_RESTORE=1
  "$INSTALL_BIN" -o "$STEM_USER" -g "$STEM_USER" -m 0750 "${BUILD_DIR}/tendril" "$STEM_BIN" </dev/null
  "$SYSTEMCTL_BIN" restart tendril.service
  if ! wait_http http://127.0.0.1:8080/health 200 || [ ! -S "$STEM_SOCKET" ]; then
    restore_previous_stem || true
    wait_http http://127.0.0.1:8080/health 200 || true
    fail "new Stem did not become healthy; previous binary restored"
  fi
  assert_binary_revision "$STEM_BIN" tendril "$sha" "$version"
  running_pid=$("$SYSTEMCTL_BIN" show -p MainPID --value tendril.service)
  running_exe=$("$READLINK_BIN" "/proc/${running_pid}/exe" || true)
  if [ "$running_exe" != "$STEM_BIN" ]; then
    restore_previous_stem || true
    fail "new Stem did not become healthy; previous binary restored"
  fi
  STEM_NEEDS_RESTORE=0
  log "Stem restarted at ${sha}"
}

stage_greenhouse_context() {
  local stage=$1
  "$INSTALL_BIN" -d -o "$STEM_USER" -g "$STEM_USER" -m 0755 "$stage" </dev/null
  "$CP_BIN" -a "${REPO}/ui/Dockerfile" "${REPO}/ui/package.json" "${REPO}/ui/package-lock.json" \
    "${REPO}/ui/tsconfig.json" "${REPO}/ui/vite.config.ts" "${REPO}/ui/index.html" "$stage/"
  "$CP_BIN" -a "${REPO}/ui/src" "${REPO}/ui/nginx" "$stage/"
  "$CHOWN_BIN" -R "${STEM_USER}:${STEM_USER}" "$stage"
  "$CHMOD_BIN" -R u+rX "$stage"
  "$CMP_BIN" -s "${REPO}/ui/Dockerfile" "${stage}/Dockerfile" || fail "staged Dockerfile does not match ui/Dockerfile"
}

cleanup_stage() {
  case "${stage:-}" in
    /tmp/opentendril-greenhouse-src.*)
      "$RM_BIN" -rf "$stage"
      stage=""
      ;;
  esac
}

build_greenhouse_image() {
  local sha version tag metadata expected
  sha=$(source_sha)
  version=$(dev_version)
  tag=$(release_tag)
  [ "$DEV_TAG" != "$tag" ] || fail "development tag collides with the release tag"
  stage=$("$MKTEMP_BIN" -d /tmp/opentendril-greenhouse-src.XXXXXX)
  stage_greenhouse_context "$stage"
  log "building ${DEV_TAG} from ui/Dockerfile at ${sha}"
  rootless_docker build \
    --platform linux/amd64 \
    --tag "$DEV_TAG" \
    --build-arg "OT_VERSION=${version}" \
    --build-arg "OT_REVISION=${sha}" \
    --file "${stage}/Dockerfile" \
    "$stage"
  cleanup_stage
  metadata=$(rootless_docker image inspect --format '{{.Os}}/{{.Architecture}}|{{index .Config.Labels "org.opencontainers.image.version"}}|{{index .Config.Labels "org.opencontainers.image.revision"}}' "$DEV_TAG")
  expected="linux/amd64|${version}|${sha}"
  [ "$metadata" = "$expected" ] || fail "Greenhouse image metadata is ${metadata}, expected ${expected}"
  DEV_IMAGE_ID=$(rootless_docker image inspect --format '{{.Id}}' "$DEV_TAG")
  is_sha256 "$DEV_IMAGE_ID" || fail "development image has no immutable sha256 id: ${DEV_IMAGE_ID}"
  [ "$DEV_IMAGE_ID" != "$RELEASE_IMAGE_ID" ] || fail "development image id collided with the release image"
  local tagged
  tagged=$(rootless_docker image inspect --format '{{.Id}}' "$tag")
  [ "$tagged" = "$RELEASE_IMAGE_ID" ] || fail "build moved the release tag ${tag}"
  log "Greenhouse image ${DEV_TAG} is ${DEV_IMAGE_ID} revision ${sha}"
}

deploy_greenhouse() {
  park_release_container
  remove_dev_active_if_present
  log "starting development Greenhouse"
  require_stem_socket
  verify_rootless_daemon
  IMAGE_ID=$DEV_IMAGE_ID
  run_fixed_container
  verify_runtime "$DEV_IMAGE_ID"
  log "testing rollback to the parked release container"
  swap_to_release
  verify_runtime "$RELEASE_IMAGE_ID"
  log "rollback container is healthy; returning to the development image"
  swap_to_dev_hold
  verify_runtime "$DEV_IMAGE_ID"
  DEV_READY=1
  NEED_RESTORE=0
  assert_release_image_untouched
  assert_wrapper_untouched
}

restore_greenhouse() {
  [ "${NEED_RESTORE:-0}" = 1 ] || return 0
  [ "${DEV_READY:-0}" = 1 ] && return 0
  [ "${RESTORING:-0}" = 1 ] && return 0
  log "deployment failed; restoring a Greenhouse container on port 4173"
  if container_exists "$DEV_HOLD" && run_restore_attempt swap_to_dev_hold; then
    return 0
  fi
  if [ -n "${RELEASE_CONTAINER_ID:-}" ] && rootless_docker container inspect "$RELEASE_CONTAINER_ID" >/dev/null 2>&1 \
    && run_restore_attempt swap_to_release; then
    return 0
  fi
  if container_exists "$ACTIVE"; then
    run_restore_attempt start_named "$ACTIVE" || true
  fi
}

write_state() {
  ensure_owned_dir "$pollinator_user" "$pollinator_group" 0750 "$STATE_DIR"
  local state=${STATE_DIR}/state
  "$RM_BIN" -f "$state"
  cat >"$state" <<EOF
SOURCE_SHA=${SOURCE_SHA}
DEV_VERSION=${DEV_VERSION}
RELEASE_IMAGE_ID=${RELEASE_IMAGE_ID}
RELEASE_CONTAINER_ID=${RELEASE_CONTAINER_ID}
DEV_IMAGE_ID=${DEV_IMAGE_ID}
DEV_TAG=${DEV_TAG}
WRAPPER_SHA=${WRAPPER_SHA}
ACTIVE=${ACTIVE}
RELEASE_HOLD=${RELEASE_HOLD}
DEV_HOLD=${DEV_HOLD}
EOF
  "$CHOWN_BIN" "${pollinator_user}:${pollinator_group}" "$state"
  "$CHMOD_BIN" 0640 "$state"
}

assign_state_field() {
  local key=$1 value=$2
  case "$key" in
    SOURCE_SHA|DEV_VERSION|RELEASE_IMAGE_ID|RELEASE_CONTAINER_ID|DEV_IMAGE_ID|DEV_TAG|WRAPPER_SHA|ACTIVE|RELEASE_HOLD|DEV_HOLD)
      ;;
    *) fail "state file has an unexpected field" ;;
  esac
  case "$value" in
    ''|*[!a-zA-Z0-9:._+-]*) fail "state file field ${key} has an unexpected value" ;;
  esac
  printf -v "$key" '%s' "$value"
}

load_state() {
  local state=${STATE_DIR}/state line key value
  [ -f "$state" ] || fail "no development deployment state at ${state}"
  refuse_symlink "$state"
  [ "$(owner_of "$state")" = "$pollinator_user" ] || fail "state file is not owned by the Pollinator-hosting account"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ''|\#*) continue ;;
    esac
    key=${line%%=*}
    value=${line#*=}
    [ "$key" != "$line" ] || fail "state file has a line without a value"
    assign_state_field "$key" "$value"
  done <"$state"
  [ -n "${RELEASE_IMAGE_ID:-}" ] || fail "state is missing RELEASE_IMAGE_ID"
  [ -n "${RELEASE_CONTAINER_ID:-}" ] || fail "state is missing RELEASE_CONTAINER_ID"
  [ -n "${WRAPPER_SHA:-}" ] || fail "state is missing WRAPPER_SHA"
  [ -n "${DEV_IMAGE_ID:-}" ] || fail "state is missing DEV_IMAGE_ID"
  [ "${DEV_TAG:-}" = "$EXPECTED_DEV_TAG" ] || fail "state development tag is not ${EXPECTED_DEV_TAG}"
  [ "${ACTIVE:-}" = "$EXPECTED_ACTIVE" ] || fail "state active container name is unexpected"
  [ "${RELEASE_HOLD:-}" = "$EXPECTED_RELEASE_HOLD" ] || fail "state release container name is unexpected"
  [ "${DEV_HOLD:-}" = "$EXPECTED_DEV_HOLD" ] || fail "state development container name is unexpected"
  is_sha256 "$RELEASE_IMAGE_ID" || fail "state release image id is not an immutable sha256 id"
  is_sha256 "$DEV_IMAGE_ID" || fail "state development image id is not an immutable sha256 id"
  [ "$DEV_IMAGE_ID" != "$RELEASE_IMAGE_ID" ] || fail "state records the same id for the development and release images"
}

prepare_host() {
  local tool groups shadow hash runtime
  [ "$("$ID_BIN" -u)" -eq 0 ] || fail "this development deployment needs administrator authority. Run: sudo make dev-deploy"
  for tool in "$ID_BIN" "$STAT_BIN" "$GETENT_BIN" "$READLINK_BIN" "$GIT_BIN" "$GO_BIN" "$CURL_BIN" \
    "$SYSTEMCTL_BIN" "$INSTALL_BIN" "$SHA256_BIN" "$FIND_BIN" "$CP_BIN" "$CHMOD_BIN" "$CHOWN_BIN" \
    "$RM_BIN" "$MKTEMP_BIN" "$CMP_BIN" "$AWK_BIN" "$SORT_BIN" "$TR_BIN" "$GREP_BIN" \
    "$RUNUSER_BIN" "$ENV_BIN" "$DOCKER_BIN"; do
    require_tool "$tool"
  done
  pollinator_user=${SUDO_USER:-}
  if [ -z "$pollinator_user" ] || [ "$pollinator_user" = root ]; then
    fail "cannot identify the Pollinator-hosting account. Run sudo make dev-deploy from that account."
  fi
  case "${SUDO_UID:-}" in
    ''|*[!0-9]*)
      fail "cannot identify the invoking account uid. Run sudo make dev-deploy from that account."
      ;;
  esac
  require_ordinary_name "$pollinator_user"
  [ "$pollinator_user" != "$STEM_USER" ] || fail "Pollinator-hosting account cannot be ${STEM_USER}"
  read_passwd "$pollinator_user"
  pollinator_uid=$pw_uid
  pollinator_home=$pw_home
  [ "$pollinator_uid" = "$SUDO_UID" ] || fail "SUDO_USER ${pollinator_user} does not match the invoking uid ${SUDO_UID}"
  [ "$pollinator_uid" -ge 1000 ] || fail "Pollinator-hosting account ${pollinator_user} is a system account"
  require_owned_dir "$pollinator_home" "$pollinator_user"
  [ "$(owner_of "$pollinator_home")" = "$pollinator_user" ] || fail "Pollinator home owner does not match the account"
  pollinator_group=$("$ID_BIN" -gn "$pollinator_user") || fail "cannot read the Pollinator-hosting group"
  groups=$("$ID_BIN" -Gn "$pollinator_user") || fail "cannot read groups for ${pollinator_user}"
  case " ${groups} " in
    *" ${STEM_USER} "*) fail "Pollinator-hosting account ${pollinator_user} is in group ${STEM_USER}" ;;
  esac

  read_passwd "$STEM_USER"
  tendril_uid=$pw_uid
  STEM_HOME=$pw_home
  [ "$tendril_uid" -ge 1000 ] || fail "${STEM_USER} uid ${tendril_uid} is a system account"
  [ "$STEM_HOME" = "$EXPECTED_STEM_HOME" ] || fail "${STEM_USER} home is ${STEM_HOME}, expected ${EXPECTED_STEM_HOME}"
  [ "$pollinator_home" != "$STEM_HOME" ] || fail "Pollinator home and Stem home are the same directory"
  [ "$pollinator_uid" != "$tendril_uid" ] || fail "Pollinator uid and Stem uid are the same"
  require_owned_dir "$STEM_HOME" "$STEM_USER"
  shadow=$("$GETENT_BIN" shadow "$STEM_USER" 2>/dev/null) || fail "cannot read the ${STEM_USER} shadow entry"
  shadow_hash=${shadow#*:}
  shadow_hash=${shadow_hash%%:*}
  case "$shadow_hash" in
    '!'*|'*'*) ;;
    *) fail "${STEM_USER} has an interactive password" ;;
  esac

  refuse_symlink "$REPO"
  [ -d "$REPO" ] || fail "checkout is missing"
  [ "$(owner_of "$REPO")" = "$pollinator_user" ] || fail "checkout is owned by $(owner_of "$REPO"), expected ${pollinator_user}"
  refuse_other_writable "$REPO"
  refuse_symlink "$LIFECYCLE"
  [ -f "$LIFECYCLE" ] || fail "shared Greenhouse lifecycle is missing"

  STEM_BIN=${STEM_HOME}/.local/bin/tendril
  [ -f "$STEM_BIN" ] || fail "protected Stem binary is missing: ${STEM_BIN}"
  refuse_symlink "$STEM_BIN"
  [ "$(owner_of "$STEM_BIN")" = "$STEM_USER" ] || fail "${STEM_BIN} is owned by $(owner_of "$STEM_BIN"), expected ${STEM_USER}"
  [ "$(group_of "$STEM_BIN")" = "$STEM_USER" ] || fail "${STEM_BIN} group is $(group_of "$STEM_BIN"), expected ${STEM_USER}"
  [ "$(mode_of "$STEM_BIN")" = 750 ] || fail "${STEM_BIN} mode is not 0750"
  runtime=/run/user/${tendril_uid}
  require_owned_dir "$runtime" "$STEM_USER"
  [ -d "$TRANSPORT_DIR" ] || fail "${TRANSPORT_DIR} is missing"
  refuse_symlink "$TRANSPORT_DIR"
  [ -f "$GREENHOUSE_WRAPPER" ] || fail "governed Greenhouse wrapper is missing: ${GREENHOUSE_WRAPPER}"
  refuse_symlink "$GREENHOUSE_WRAPPER"
  [ "$(owner_of "$GREENHOUSE_WRAPPER")" = root ] || fail "${GREENHOUSE_WRAPPER} is not root-owned"
  [ "$(group_of "$GREENHOUSE_WRAPPER")" = root ] || fail "${GREENHOUSE_WRAPPER} is not root-group-owned"
  [ "$(mode_of "$GREENHOUSE_WRAPPER")" = 755 ] || fail "${GREENHOUSE_WRAPPER} mode is not 0755"

  STEM_UID=$tendril_uid
  DOCKER_HOST=unix:///run/user/${STEM_UID}/docker.sock
  # shellcheck disable=SC1090
  . "$LIFECYCLE"
  verify_rootless_daemon
  assert_docker_root

  MCP_BIN=${pollinator_home}/.local/bin/tendril-mcp
  STATE_DIR=${pollinator_home}/.local/share/opentendril-dev-deploy
  BUILD_DIR=${pollinator_home}/.cache/opentendril-dev-build
  MCP_ROLLBACK_DIR=${pollinator_home}/.local/share/opentendril-dev-rollback
  ROLLBACK_DIR=${STEM_HOME}/.local/share/opentendril-dev-rollback
  IMAGE=$(release_tag)
  RELEASE_IMAGE_ID=$(rootless_docker image inspect --format '{{.Id}}' "$IMAGE") || fail "release Greenhouse tag is not present"
  is_sha256 "$RELEASE_IMAGE_ID" || fail "release image has no immutable sha256 id: ${RELEASE_IMAGE_ID}"
  IMAGE_ID=$RELEASE_IMAGE_ID
  WRAPPER_SHA=$("$SHA256_BIN" "$GREENHOUSE_WRAPPER" | "$AWK_BIN" '{print $1}')
}

cmd_deploy() {
  local before after end_sha
  SOURCE_SHA=$(source_sha)
  DEV_VERSION=$(dev_version)
  log "checkout ${SOURCE_SHA}; release image ${RELEASE_IMAGE_ID}; pollinator ${pollinator_user}"
  before=$("$MKTEMP_BIN")
  snapshot_git "$before"
  build_binaries
  install_mcp
  install_stem_if_needed
  [ -S "$STEM_SOCKET" ] || fail "Stem Unix socket is missing: ${STEM_SOCKET}"
  build_greenhouse_image
  deploy_greenhouse
  after=$("$MKTEMP_BIN")
  snapshot_git "$after"
  if ! "$CMP_BIN" -s "$before" "$after"; then
    diff -u "$before" "$after" >&2 || true
    fail "workspace, Fruit, or Git state changed during deployment"
  fi
  "$RM_BIN" -f "$before" "$after"
  end_sha=$(source_sha)
  [ "$end_sha" = "$SOURCE_SHA" ] || fail "checkout changed from ${SOURCE_SHA} to ${end_sha}"
  assert_binary_revision "$STEM_BIN" tendril "$SOURCE_SHA" "$DEV_VERSION"
  assert_binary_revision "$MCP_BIN" tendril-mcp "$SOURCE_SHA" "$DEV_VERSION"
  write_state
  log "DONE checkout ${SOURCE_SHA}"
  log "Stem: ${STEM_BIN} (${DEV_VERSION})"
  log "MCP client: ${MCP_BIN} (${DEV_VERSION})"
  log "Greenhouse: http://127.0.0.1:4173 image ${DEV_IMAGE_ID}"
  log "Rollback: sudo make dev-rollback"
}

cmd_rollback() {
  load_state
  assert_release_image_untouched
  assert_wrapper_untouched
  [ -S "$STEM_SOCKET" ] || fail "Stem Unix socket is missing: ${STEM_SOCKET}"
  log "restoring release Greenhouse container ${RELEASE_CONTAINER_ID}"
  NEED_RESTORE=1
  DEV_READY=0
  swap_to_release
  verify_runtime "$RELEASE_IMAGE_ID"
  NEED_RESTORE=0
  assert_release_image_untouched
  assert_wrapper_untouched
  log "DONE rollback. Greenhouse is the preserved release container at http://127.0.0.1:4173"
  log "Development image remains available as ${DEV_TAG}. The governed installer was not changed."
}

on_exit() {
  local status=$?
  trap - EXIT
  cleanup_stage
  if [ "$status" -ne 0 ]; then
    restore_previous_stem || true
    restore_greenhouse || true
  fi
  exit "$status"
}

main() {
  local cmd=${1:-deploy}
  [ "$#" -le 1 ] || usage
  trap on_exit EXIT
  prepare_host
  case "$cmd" in
    deploy) cmd_deploy ;;
    rollback) cmd_rollback ;;
    *) usage ;;
  esac
}

main "$@"
