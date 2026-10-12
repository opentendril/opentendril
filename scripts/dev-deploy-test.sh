#!/usr/bin/env bash
# Hermetic tests for scripts/dev-deploy.sh.
#
# Bubblewrap hides the host tendril home, the governed wrapper, the Stem
# socket, and the absolute programs the deployment invokes. Nothing here
# starts the live Greenhouse or writes the checkout.
#
# Usage:
#   bash scripts/dev-deploy-test.sh

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd -P)"
deploy="${repo_root}/scripts/dev-deploy.sh"
lifecycle="${repo_root}/scripts/greenhouse-lifecycle.sh"
installer="${repo_root}/install.sh"

if [ ! -x /usr/bin/bwrap ]; then
  echo "dev-deploy-test: host is missing /usr/bin/bwrap" >&2
  exit 1
fi

version="$(tr -d '[:space:]' <"${repo_root}/VERSION")"
release_tag="opentendril-greenhouse:${version}"
dev_version="${version}+dev"
release_id="sha256:$(printf 'b%.0s' {1..64})"
dev_id="sha256:$(printf 'c%.0s' {1..64})"
source_sha="$(printf 'a%.0s' {1..40})"
substrate_sha="$(printf 'e%.0s' {1..40})"
old_sha="$(printf 'd%.0s' {1..40})"
canary="CANARY-VALUE-do-not-leak-9f3a"
secret="botanist-secret-value"
release_container_id="relctr123"
dev_container_id="devctr456"

works=()
WORK=""

cleanup() {
  local dir
  for dir in "${works[@]+"${works[@]}"}"; do
    rm -rf "$dir"
  done
}
trap cleanup EXIT

fail() {
  printf 'dev-deploy-test: %s\n' "$*" >&2
  if [ -n "${WORK}" ]; then
    printf 'dev-deploy-test: fixture %s\n' "$WORK" >&2
    if [ -f "${WORK}/events" ]; then
      printf 'dev-deploy-test: events:\n' >&2
      cat "${WORK}/events" >&2
    fi
    if [ -f "${WORK}/err" ]; then
      printf 'dev-deploy-test: stderr:\n' >&2
      cat "${WORK}/err" >&2
    fi
    if [ -f "${WORK}/out" ]; then
      printf 'dev-deploy-test: stdout:\n' >&2
      cat "${WORK}/out" >&2
    fi
  fi
  exit 1
}

assert_absent() {
  local file=$1 needle=$2
  if [ -f "$file" ] && grep -F -- "$needle" "$file" >/dev/null; then
    fail "found unexpected text ${needle} in ${file}"
  fi
}

write_binary() {
  local path=$1 label=$2 rev=$3
  mkdir -p "$(dirname "$path")"
  cat >"$path" <<EOF
#!/bin/sh
# vcs.revision ${rev}
# vcs.modified false
case "\$1" in
  --version) printf '%s %s\n' "${label}" "${dev_version}" ;;
esac
EOF
  chmod 0755 "$path"
}

write_shims() {
  local work=$1
  mkdir -p "${work}/shims" "${work}/bin" "${work}/real"
  cp -L /usr/bin/stat "${work}/real/stat"
  chmod 0755 "${work}/real/stat"

  cat >"${work}/bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF

  cat >"${work}/shims/id" <<'EOF'
#!/bin/sh
case "$*" in
  "-u") printf '0\n' ;;
  "-gn alice") printf 'alice\n' ;;
  "-Gn alice") printf 'alice\n' ;;
  *)
    printf 'unexpected id %s\n' "$*" >>/tmp/work/events
    exit 1
    ;;
esac
EOF

  cat >"${work}/shims/getent" <<'EOF'
#!/bin/sh
case "$1:$2" in
  passwd:alice) printf 'alice:x:1500:1500:Alice:/tmp/work/alice:/bin/bash\n' ;;
  passwd:tendril) printf 'tendril:x:2001:2001:Tendril:/home/tendril:/usr/sbin/nologin\n' ;;
  shadow:tendril) printf 'tendril:!:19000:0:99999:7:::\n' ;;
  *)
    printf 'unexpected getent %s\n' "$*" >>/tmp/work/events
    exit 2
    ;;
esac
EOF

  cat >"${work}/shims/stat" <<'EOF'
#!/bin/bash
set -euo pipefail
if [ "${1:-}" != -c ] || [ "$#" -ne 3 ]; then
  printf 'unexpected stat %s\n' "$*" >>/tmp/work/events
  exit 1
fi
format=$2
path=$3
testmode=$(cat /tmp/work/mode)
repo=$(cat /tmp/work/repo-path)
owner=""
group=""
mode_bits=""
case "$path" in
  "$repo")
    if [ "$testmode" = bad-repo-owner ]; then
      owner=root
    else
      owner=alice
    fi
    group=alice
    mode_bits=755
    ;;
  /usr/local/sbin/opentendril-greenhouse)
    owner=root
    group=root
    mode_bits=755
    ;;
  /home/tendril)
    if [ "$testmode" = bad-home-owner ]; then
      owner=alice
    else
      owner=tendril
    fi
    group=tendril
    mode_bits=$(/tmp/work/real/stat -c '%a' "$path")
    ;;
  /home/tendril/*)
    owner=tendril
    group=tendril
    mode_bits=$(/tmp/work/real/stat -c '%a' "$path")
    ;;
  /tmp/work/alice|/tmp/work/alice/*)
    owner=alice
    group=alice
    mode_bits=$(/tmp/work/real/stat -c '%a' "$path")
    ;;
  /run/user/2001|/run/user/2001/*)
    owner=tendril
    group=tendril
    mode_bits=$(/tmp/work/real/stat -c '%a' "$path")
    ;;
  *)
    printf 'unexpected stat path %s\n' "$path" >>/tmp/work/events
    exit 1
    ;;
esac
case "$format" in
  '%U') printf '%s\n' "$owner" ;;
  '%G') printf '%s\n' "$group" ;;
  '%a') printf '%s\n' "$mode_bits" ;;
  *)
    printf 'unexpected stat format %s\n' "$format" >>/tmp/work/events
    exit 1
    ;;
esac
EOF

  cat >"${work}/shims/readlink" <<'EOF'
#!/bin/sh
if [ "$1" = /proc/4242/exe ]; then
  printf '/home/tendril/.local/bin/tendril\n'
  exit 0
fi
printf 'unexpected readlink %s\n' "$*" >>/tmp/work/events
exit 1
EOF

  cat >"${work}/shims/systemctl" <<'EOF'
#!/bin/sh
printf 'systemctl %s\n' "$*" >>/tmp/work/events
case "$*" in
  "show -p MainPID --value tendril.service")
    printf '4242\n'
    ;;
  "restart tendril.service")
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
EOF

  cat >"${work}/shims/curl" <<'EOF'
#!/bin/bash
set -euo pipefail
printf 'curl %s\n' "$*" >>/tmp/work/events
url=""
prev=""
for arg in "$@"; do
  case "$arg" in
    *Authorization*|*api-key*|*BOTANIST*|*botanist-secret-value*)
      printf 'curl carried a credential\n' >>/tmp/work/events
      exit 1
      ;;
  esac
  case "$prev" in
    -H|--header)
      printf 'curl sent a header\n' >>/tmp/work/events
      exit 1
      ;;
  esac
  url=$arg
  prev=$arg
done
case "$url" in
  http://127.0.0.1:8080/health|http://127.0.0.1:4173/health)
    if [ -f /tmp/work/fail-health ]; then
      printf '500'
    else
      printf '200'
    fi
    ;;
  http://127.0.0.1:4173/v1/sessions|http://127.0.0.1:8080/v1/sessions)
    printf '401'
    ;;
  *)
    printf 'unexpected curl url %s\n' "$url" >>/tmp/work/events
    printf '000'
    exit 1
    ;;
esac
EOF

  cat >"${work}/shims/git" <<'EOF'
#!/bin/bash
set -euo pipefail
if [ -n "${BOTANIST_KEY:-}" ] || [ -n "${DOCKER_HOST:-}" ] || [ -n "${DOCKER_CONTEXT:-}" ]; then
  printf 'git inherited caller environment\n' >>/tmp/work/events
  exit 1
fi
if [ "${GIT_OPTIONAL_LOCKS:-}" != 0 ]; then
  printf 'git was not denied optional locks\n' >>/tmp/work/events
  exit 1
fi
printf 'git %s\n' "$*" >>/tmp/work/events
[ "${1:-}" = --no-optional-locks ] || exit 1
shift
[ "${1:-}" = -C ] || exit 1
dir=$2
shift 2
repo=$(cat /tmp/work/repo-path)
case "$*" in
  "rev-parse HEAD")
    if [ "$dir" = "$repo" ]; then
      cat /tmp/work/expected-sha
    else
      cat /tmp/work/substrate-sha
    fi
    ;;
  "status --porcelain")
    ;;
  "rev-parse --show-toplevel")
    printf '%s\n' "$dir"
    ;;
  for-each-ref*)
    printf 'ref %s refs/heads/main\n' "$(tr -d '[:space:]' </tmp/work/substrate-sha)"
    ;;
  *)
    printf 'unexpected git %s\n' "$*" >>/tmp/work/events
    exit 1
    ;;
esac
EOF

  cat >"${work}/shims/go" <<'EOF'
#!/bin/bash
set -euo pipefail
printf 'go %s\n' "$*" >>/tmp/work/events
if [ "${1:-}" = version ] && [ "${2:-}" = -m ]; then
  bin=$3
  rev=$(sed -n 's/^# vcs.revision //p' "$bin" | head -n 1)
  mod=$(sed -n 's/^# vcs.modified //p' "$bin" | head -n 1)
  [ -n "$rev" ] || exit 1
  [ -n "$mod" ] || exit 1
  printf '%s\n' "$bin"
  printf 'build\tvcs.revision=%s\n' "$rev"
  printf 'build\tvcs.modified=%s\n' "$mod"
  exit 0
fi
if [ "${1:-}" != build ]; then
  printf 'unexpected go %s\n' "$*" >>/tmp/work/events
  exit 1
fi
if [ -n "${BOTANIST_KEY:-}" ] || [ -n "${DOCKER_CONTEXT:-}" ]; then
  printf 'go build inherited caller environment\n' >>/tmp/work/events
  exit 1
fi
shift
out=""
ldflags=""
package=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -C)
      shift 2
      ;;
    -ldflags)
      ldflags=$2
      shift 2
      ;;
    -ldflags=*)
      ldflags=${1#-ldflags=}
      shift
      ;;
    -o)
      out=$2
      shift 2
      ;;
    -o=*)
      out=${1#-o=}
      shift
      ;;
    *)
      package=$1
      shift
      ;;
  esac
done
case "$out" in
  /tmp/work/alice/.cache/opentendril-dev-build/tendril|/tmp/work/alice/.cache/opentendril-dev-build/tendril-mcp)
    ;;
  *)
    printf 'go build wrote outside the Pollinator cache: %s\n' "$out" >>/tmp/work/events
    exit 1
    ;;
esac
dev_version=$(tr -d '[:space:]' </tmp/work/dev-version)
case "$ldflags" in
  *"-X github.com/opentendril/opentendril/internal/buildinfo.Version=${dev_version}"*) ;;
  *)
    printf 'go build ldflags missing the checkout version: %s\n' "$ldflags" >>/tmp/work/events
    exit 1
    ;;
esac
label=$(basename "$out")
case "$package" in
  ./cmd/stem) [ "$label" = tendril ] || exit 1 ;;
  ./cmd/tendril-mcp) [ "$label" = tendril-mcp ] || exit 1 ;;
  *)
    printf 'unexpected go package %s\n' "$package" >>/tmp/work/events
    exit 1
    ;;
esac
rev=$(tr -d '[:space:]' </tmp/work/expected-sha)
cat >"$out" <<END_BINARY
#!/bin/sh
# vcs.revision ${rev}
# vcs.modified false
case "\$1" in
  --version) printf '%s %s\n' "${label}" "${dev_version}" ;;
esac
END_BINARY
chmod 0755 "$out"
EOF

  cat >"${work}/shims/install" <<'EOF'
#!/bin/bash
set -euo pipefail
printf 'install %s\n' "$*" >>/tmp/work/events
dir=0
mode=""
positionals=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    -d) dir=1; shift ;;
    -o|-g) shift 2 ;;
    -m) mode=$2; shift 2 ;;
    --) shift; break ;;
    -*)
      printf 'unexpected install flag %s\n' "$1" >>/tmp/work/events
      exit 1
      ;;
    *)
      positionals+=("$1")
      shift
      ;;
  esac
done
if [ "$dir" -eq 1 ]; then
  mkdir -p "${positionals[0]}"
  [ -n "$mode" ] && chmod "$mode" "${positionals[0]}"
else
  cp -a "${positionals[0]}" "${positionals[1]}"
  [ -n "$mode" ] && chmod "$mode" "${positionals[1]}"
fi
EOF

  cat >"${work}/shims/chown" <<'EOF'
#!/bin/sh
printf 'chown %s\n' "$*" >>/tmp/work/events
exit 0
EOF

  cat >"${work}/shims/runuser" <<'EOF'
#!/bin/sh
if [ "${1:-}" != -u ] || [ "${3:-}" != -- ]; then
  printf 'unexpected runuser %s\n' "$*" >>/tmp/work/events
  exit 1
fi
printf 'runuser %s\n' "$2" >>/tmp/work/events
shift 3
exec "$@"
EOF

  cat >"${work}/shims/docker" <<'EOF'
#!/bin/bash
set -euo pipefail
ROOT=/tmp/work/docker
log() { printf '%s\n' "$*" >>/tmp/work/events; }
die() { log "DOCKER-FAIL $*"; printf 'docker-shim: %s\n' "$*" >&2; exit 1; }

if [ "${HOME:-}" != /home/tendril ] || [ "${USER:-}" != tendril ] || [ "${LOGNAME:-}" != tendril ]; then
  die "bad identity HOME=${HOME:-} USER=${USER:-}"
fi
if [ -n "${BOTANIST_KEY:-}" ] || [ -n "${DOCKER_CONTEXT:-}" ] || [ -n "${STEM_TRANSPORT:-}" ]; then
  die "caller environment reached the Docker client"
fi
if [ "${DOCKER_HOST:-}" != unix:///run/user/2001/docker.sock ]; then
  die "bad DOCKER_HOST ${DOCKER_HOST:-}"
fi
if [ "${XDG_RUNTIME_DIR:-}" != /run/user/2001 ]; then
  die "bad runtime dir ${XDG_RUNTIME_DIR:-}"
fi
if [ "${1:-}" != --host=unix:///run/user/2001/docker.sock ]; then
  die "bad --host ${1:-}"
fi
shift
log "call $(printf '%q ' "$@")"

find_container() {
  local key=$1 dir base
  local found=""
  shopt -s nullglob
  for dir in "$ROOT/containers"/*; do
    base=$(basename "$dir")
    if [ "$base" = "$key" ]; then
      found=$dir
      break
    fi
    if [ -f "$dir/id" ] && [ "$(tr -d '[:space:]' <"$dir/id")" = "$key" ]; then
      found=$dir
      break
    fi
  done
  [ -n "$found" ] || return 1
  printf '%s\n' "$found"
}

sub=${1:-}
shift || die "missing docker command"
case "$sub" in
  info)
    [ "${1:-}" = --format ] || die "unexpected info $*"
    case "$2" in
      '{{.SecurityOptions}}') printf 'name=rootless\n' ;;
      '{{.DockerRootDir}}') printf '/home/tendril/.local/share/docker\n' ;;
      *) die "unexpected info format $2" ;;
    esac
    ;;
  image)
    action=$1
    shift
    case "$action" in
      tag) die "refusing to retag an image" ;;
      inspect)
        [ "${1:-}" = --format ] || die "unexpected image inspect $*"
        format=$2
        tag=$3
        release_tag=$(tr -d '[:space:]' </tmp/work/release-tag)
        dev_tag=opentendril-greenhouse:dev
        case "$tag" in
          "$release_tag")
            case "$format" in
              '{{.Id}}') cat /tmp/work/release-id ;;
              *) die "unexpected release inspect format" ;;
            esac
            ;;
          "$dev_tag")
            [ -f "$ROOT/built" ] || die "development image was not built"
            case "$format" in
              '{{.Id}}') cat /tmp/work/dev-id ;;
              '{{.Os}}/{{.Architecture}}|{{index .Config.Labels "org.opencontainers.image.version"}}|{{index .Config.Labels "org.opencontainers.image.revision"}}')
                printf 'linux/amd64|%s|%s\n' "$(tr -d '[:space:]' <"$ROOT/built-version")" "$(tr -d '[:space:]' <"$ROOT/built-revision")"
                ;;
              *) die "unexpected dev inspect format $format" ;;
            esac
            ;;
          *) die "unexpected image tag $tag" ;;
        esac
        ;;
      *) die "unexpected image action $action" ;;
    esac
    ;;
  build)
    platform=""
    tag=""
    revision=""
    built_version=""
    file=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --platform) platform=$2; shift 2 ;;
        --tag) tag=$2; shift 2 ;;
        --file) file=$2; shift 2 ;;
        --build-arg)
          case "$2" in
            OT_REVISION=*) revision=${2#OT_REVISION=} ;;
            OT_VERSION=*) built_version=${2#OT_VERSION=} ;;
            *) die "unexpected build arg $2" ;;
          esac
          shift 2
          ;;
        *) shift ;;
      esac
    done
    [ "$platform" = linux/amd64 ] || die "platform $platform"
    [ "$tag" = opentendril-greenhouse:dev ] || die "refusing tag $tag"
    [ "$revision" = "$(tr -d '[:space:]' </tmp/work/expected-sha)" ] || die "build revision $revision"
    [ "$built_version" = "$(tr -d '[:space:]' </tmp/work/dev-version)" ] || die "build version $built_version"
    [ -f "$file" ] || die "missing Dockerfile $file"
    mkdir -p "$ROOT"
    printf '%s\n' "$built_version" >"$ROOT/built-version"
    if [ "$(tr -d '[:space:]' </tmp/work/mode)" = bad-revision ]; then
      printf '%s\n' "$(tr -d '[:space:]' </tmp/work/old-sha)" >"$ROOT/built-revision"
    else
      printf '%s\n' "$revision" >"$ROOT/built-revision"
    fi
    : >"$ROOT/built"
    ;;
  run)
    printf '%s\n' "$@" >/tmp/work/run-args
    name=""
    prev=""
    image=""
    for arg in "$@"; do
      case "$arg" in
        *0.0.0.0*|*docker.sock*|*privileged*|*BOTANIST*|*api-key*|*botanist-secret-value*|*/home/tendril*)
          die "run argument is outside the fixed contract: $arg"
          ;;
      esac
      if [ "$prev" = --name ]; then
        name=$arg
      fi
      prev=$arg
      image=$arg
    done
    [ "$name" = opentendril-greenhouse ] || die "run name $name"
    [ "$image" = "$(tr -d '[:space:]' </tmp/work/dev-id)" ] || die "run image $image"
    [ "$image" != "$(tr -d '[:space:]' </tmp/work/release-id)" ] || die "run used the release image"
    dir="$ROOT/containers/$name"
    [ ! -e "$dir" ] || die "run target already exists"
    mkdir -p "$dir"
    printf '%s\n' "$(tr -d '[:space:]' </tmp/work/dev-container-id)" >"$dir/id"
    printf '%s\n' "$image" >"$dir/image"
    printf 'running\n' >"$dir/status"
    printf 'healthy\n' >"$dir/health"
    printf '127.0.0.1:4173->8080/tcp\n' >"$dir/ports"
    ;;
  update)
    case "${1:-}" in
      --restart=no|--restart=unless-stopped) ;;
      *) die "unexpected update $1" ;;
    esac
    find_container "$2" >/dev/null || die "update missing $2"
    ;;
  rename)
    dir=$(find_container "$1") || die "rename missing $1"
    [ ! -e "$ROOT/containers/$2" ] || die "rename target exists $2"
    mv "$dir" "$ROOT/containers/$2"
    ;;
  ps)
    [ "${1:-}" = --filter ] || die "unexpected ps $*"
    [ "$2" = status=running ] || die "unexpected ps filter $2"
    shopt -s nullglob
    for dir in "$ROOT/containers"/*; do
      if [ "$(tr -d '[:space:]' <"$dir/status")" = running ]; then
        printf '%s %s\n' "$(basename "$dir")" "$(tr -d '[:space:]' <"$dir/ports")"
      fi
    done
    ;;
  logs)
    printf 'shim-log\n' >&2
    ;;
  tag)
    die "refusing to retag an image"
    ;;
  container)
    action=$1
    shift
    case "$action" in
      inspect)
        format=""
        if [ "${1:-}" = --format ]; then
          format=$2
          shift 2
        fi
        if ! dir=$(find_container "$1"); then
          log "miss container $1"
          exit 1
        fi
        case "$format" in
          "") ;;
          '{{.Image}}') tr -d '[:space:]' <"$dir/image"; printf '\n' ;;
          '{{.Id}}') tr -d '[:space:]' <"$dir/id"; printf '\n' ;;
          '{{.Name}}') printf '/%s\n' "$(basename "$dir")" ;;
          '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}|{{.Image}}')
            printf '%s|%s|%s\n' "$(tr -d '[:space:]' <"$dir/status")" "$(tr -d '[:space:]' <"$dir/health")" "$(tr -d '[:space:]' <"$dir/image")"
            ;;
          *) die "unexpected container format $format" ;;
        esac
        ;;
      stop)
        dir=$(find_container "$1") || die "stop missing $1"
        printf 'exited\n' >"$dir/status"
        ;;
      start)
        dir=$(find_container "$1") || die "start missing $1"
        printf 'running\n' >"$dir/status"
        printf 'healthy\n' >"$dir/health"
        ;;
      rm)
        if [ "${1:-}" = --force ]; then
          shift
        fi
        dir=$(find_container "$1") || die "rm missing $1"
        image=$(tr -d '[:space:]' <"$dir/image")
        if [ "$image" = "$(tr -d '[:space:]' </tmp/work/release-id)" ]; then
          die "refusing to remove release container $1"
        fi
        rm -rf "$dir"
        ;;
      ls|restart)
        die "release wrapper verb reached the development Docker shim: $action"
        ;;
      *) die "unexpected container action $action" ;;
    esac
    ;;
  *)
    die "unexpected docker command $sub"
    ;;
esac
EOF

  chmod 0755 "${work}/bin/sleep" "${work}/shims/"*
}

setup_fixture() {
  local mode=$1
  local stem_rev=$2
  WORK="$(mktemp -d /tmp/opentendril-dev-deploy-test.XXXXXX)"
  works+=("$WORK")
  mkdir -p \
    "${WORK}/alice/.local/bin" \
    "${WORK}/alice/.local/share" \
    "${WORK}/alice/.cache" \
    "${WORK}/tendril-home/.local/bin" \
    "${WORK}/tendril-home/.local/share" \
    "${WORK}/tendril-home/.tendril/substrates/sample/.git" \
    "${WORK}/run-user/2001" \
    "${WORK}/transport" \
    "${WORK}/sbin" \
    "${WORK}/docker/containers/opentendril-greenhouse"
  chmod 0755 "${WORK}/alice" "${WORK}/alice/.local" "${WORK}/alice/.local/bin"
  chmod 0750 "${WORK}/alice/.local/share" "${WORK}/alice/.cache"
  chmod 0755 "${WORK}/tendril-home" "${WORK}/tendril-home/.local" "${WORK}/tendril-home/.tendril"
  chmod 0750 "${WORK}/tendril-home/.local/bin" "${WORK}/tendril-home/.local/share"
  chmod 0700 "${WORK}/run-user/2001"
  printf '%s\n' "$canary" >"${WORK}/tendril-home/.tendril/canary"
  printf 'index-sentinel\n' >"${WORK}/tendril-home/.tendril/substrates/sample/.git/index"
  printf 'substrate-sentinel\n' >"${WORK}/tendril-home/.tendril/substrates/sample/SENTINEL"
  write_binary "${WORK}/tendril-home/.local/bin/tendril" tendril "$stem_rev"
  chmod 0750 "${WORK}/tendril-home/.local/bin/tendril"
  write_binary "${WORK}/alice/.local/bin/tendril-mcp" tendril-mcp "$old_sha"
  printf 'governed-wrapper-fixture\n' >"${WORK}/sbin/opentendril-greenhouse"
  chmod 0755 "${WORK}/sbin/opentendril-greenhouse"
  python3 - <<PY
import os, socket
path = "${WORK}/transport/stem.sock"
try:
    os.remove(path)
except FileNotFoundError:
    pass
sock = socket.socket(socket.AF_UNIX)
sock.bind(path)
PY
  printf '%s\n' "$mode" >"${WORK}/mode"
  printf '%s\n' "$repo_root" >"${WORK}/repo-path"
  printf '%s\n' "$source_sha" >"${WORK}/expected-sha"
  printf '%s\n' "$substrate_sha" >"${WORK}/substrate-sha"
  printf '%s\n' "$old_sha" >"${WORK}/old-sha"
  printf '%s\n' "$dev_version" >"${WORK}/dev-version"
  printf '%s\n' "$release_tag" >"${WORK}/release-tag"
  printf '%s\n' "$release_id" >"${WORK}/release-id"
  printf '%s\n' "$dev_id" >"${WORK}/dev-id"
  printf '%s\n' "$dev_container_id" >"${WORK}/dev-container-id"
  printf '%s\n' "$release_id" >"${WORK}/docker/containers/opentendril-greenhouse/image"
  printf '%s\n' "$release_container_id" >"${WORK}/docker/containers/opentendril-greenhouse/id"
  printf 'running\n' >"${WORK}/docker/containers/opentendril-greenhouse/status"
  printf 'healthy\n' >"${WORK}/docker/containers/opentendril-greenhouse/health"
  printf '127.0.0.1:4173->8080/tcp\n' >"${WORK}/docker/containers/opentendril-greenhouse/ports"
  : >"${WORK}/events"
  write_shims "$WORK"
  if [ "$mode" = stem-unhealthy ]; then
    : >"${WORK}/fail-health"
  fi
}

run_deploy() {
  local cmd=$1
  local status=0
  set +e
  bwrap \
    --ro-bind / / \
    --dev /dev \
    --proc /proc \
    --tmpfs /tmp \
    --bind "$WORK" /tmp/work \
    --bind "${WORK}/tendril-home" /home/tendril \
    --bind "${WORK}/sbin" /usr/local/sbin \
    --bind "${WORK}/run-user" /run/user \
    --bind "${WORK}/transport" /var/lib/opentendril-transport \
    --ro-bind "$repo_root" "$repo_root" \
    --bind "${WORK}/shims/id" /usr/bin/id \
    --bind "${WORK}/shims/stat" /usr/bin/stat \
    --bind "${WORK}/shims/getent" /usr/bin/getent \
    --bind "${WORK}/shims/readlink" /usr/bin/readlink \
    --bind "${WORK}/shims/systemctl" /usr/bin/systemctl \
    --bind "${WORK}/shims/curl" /usr/bin/curl \
    --bind "${WORK}/shims/git" /usr/bin/git \
    --bind "${WORK}/shims/go" /usr/bin/go \
    --bind "${WORK}/shims/install" /usr/bin/install \
    --bind "${WORK}/shims/chown" /usr/bin/chown \
    --bind "${WORK}/shims/runuser" /usr/sbin/runuser \
    --bind "${WORK}/shims/docker" /usr/bin/docker \
    -- \
    env \
      SUDO_USER=alice \
      DOCKER_HOST=tcp://203.0.113.9:2375 \
      DOCKER_CONTEXT=evil-context \
      STEM_TRANSPORT=tcp \
      IMAGE=opentendril-greenhouse:pwned \
      BOTANIST_KEY="$secret" \
      PATH="/tmp/work/bin:${PATH}" \
      bash "$deploy" "$cmd" \
      >"${WORK}/out" 2>"${WORK}/err"
  status=$?
  set -e
  printf '%s\n' "$status"
}

require_run_contract() {
  local arg
  [ -f "${WORK}/run-args" ] || fail "development container was not created"
  while IFS= read -r arg; do
    case "$arg" in
      *0.0.0.0*|*docker.sock*|*privileged*|*BOTANIST*|*api-key*|*botanist-secret-value*|*/home/tendril*)
        fail "run argument left the fixed contract: ${arg}"
        ;;
    esac
  done <"${WORK}/run-args"
  for arg in \
    --pull=never \
    --detach \
    --name \
    opentendril-greenhouse \
    --restart \
    unless-stopped \
    --publish \
    127.0.0.1:4173:8080 \
    --read-only \
    --tmpfs \
    /tmp \
    /etc/nginx/conf.d:uid=101,gid=101,mode=0755 \
    --cap-drop \
    ALL \
    --security-opt \
    no-new-privileges:true \
    "--health-cmd" \
    "wget -q -O /dev/null http://127.0.0.1:8080/" \
    --mount \
    "type=bind,source=/var/lib/opentendril-transport,target=/var/lib/opentendril-transport,readonly" \
    --env \
    STEM_TRANSPORT=unix \
    "STEM_SOCKET=/var/lib/opentendril-transport/stem.sock" \
    "$dev_id"
  do
    grep -Fx -- "$arg" "${WORK}/run-args" >/dev/null || fail "run is missing ${arg}"
  done
}

refuse_mutation_events() {
  local label=$1
  if grep -F 'DOCKER-FAIL' "${WORK}/events" >/dev/null; then
    fail "${label} hit a Docker contract failure"
  fi
  assert_absent "${WORK}/events" "refusing to retag"
  assert_absent "${WORK}/events" "pwned"
  assert_absent "${WORK}/events" "203.0.113.9"
  assert_absent "${WORK}/events" "evil-context"
  assert_absent "${WORK}/events" "$secret"
  assert_absent "${WORK}/out" "$secret"
  assert_absent "${WORK}/err" "$secret"
  assert_absent "${WORK}/events" "$canary"
}

# --- static contracts, no mounts ---

python3 - "$installer" "$lifecycle" <<'PY'
import pathlib, sys
installer = pathlib.Path(sys.argv[1]).read_text()
lifecycle = pathlib.Path(sys.argv[2]).read_text()
marker = "<<'END_GREENHOUSE_LIFECYCLE'\n"
start = installer.index(marker) + len(marker)
end = installer.index("\nEND_GREENHOUSE_LIFECYCLE\n", start)
embed = installer[start:end] + "\n"
if embed != lifecycle:
    raise SystemExit("embedded Greenhouse lifecycle does not match scripts/greenhouse-lifecycle.sh")
if ". scripts/greenhouse-lifecycle.sh" in installer or "source scripts/greenhouse-lifecycle.sh" in installer:
    raise SystemExit("install.sh reads the checkout lifecycle at install time")
PY

for token in \
  "no-new-privileges" \
  "uid=101,gid=101,mode=0755" \
  "127.0.0.1" \
  "4173" \
  "STEM_TRANSPORT" \
  "unix"
do
  grep -F -- "$token" "$lifecycle" >/dev/null || fail "lifecycle missing ${token}"
  grep -F -- "$token" "${repo_root}/docker-compose.yml" >/dev/null || fail "compose reference missing ${token}"
done
grep -F -- 'source=${TRANSPORT_DIR},target=${TRANSPORT_DIR},readonly' "$lifecycle" >/dev/null || fail "lifecycle missing the transport mount"
grep -F -- "/var/lib/opentendril-transport" "$installer" >/dev/null || fail "installer missing the transport directory"
grep -F -- "/var/lib/opentendril-transport" "${repo_root}/docker-compose.yml" >/dev/null || fail "compose reference missing the transport directory"
grep -F -- "--cap-drop ALL" "$lifecycle" >/dev/null || fail "lifecycle missing capability drop"
grep -F -- "cap_drop: [ALL]" "${repo_root}/docker-compose.yml" >/dev/null || fail "compose reference missing capability drop"
grep -F -- "--pull=never" "$lifecycle" >/dev/null || fail "lifecycle missing pull never"
grep -F -- '"$RUNUSER_BIN" -u tendril --' "$lifecycle" >/dev/null || fail "lifecycle missing the tendril runuser contract"
grep -F -- 'HOME=/home/tendril' "$lifecycle" >/dev/null || fail "lifecycle missing the tendril home pin"

if grep -nE 'dr3w|/home/dr3w|BOTANIST_KEY|api-key|Authorization|1001' "$deploy" "$lifecycle"; then
  fail "deployment source contains a personal path or a credential"
fi
if grep -nE '/home/[A-Za-z0-9._-]+' "$deploy" | grep -v '/home/tendril'; then
  fail "deployment source contains a home other than /home/tendril"
fi
grep -F -- 'opentendril-greenhouse:dev' "$deploy" >/dev/null || fail "development tag is not fixed"
grep -F -- 'sudo make dev-deploy' "${repo_root}/Makefile" >/dev/null || true
grep -E '^dev-deploy:' "${repo_root}/Makefile" >/dev/null || fail "make dev-deploy is missing"
grep -E '^dev-rollback:' "${repo_root}/Makefile" >/dev/null || fail "make dev-rollback is missing"

host_git_before="$(mktemp)"
host_git_after="$(mktemp)"
{
  GIT_OPTIONAL_LOCKS=0 git -C "$repo_root" --no-optional-locks status --porcelain=v1 --untracked-files=all
  GIT_OPTIONAL_LOCKS=0 git -C "$repo_root" --no-optional-locks diff
  GIT_OPTIONAL_LOCKS=0 git -C "$repo_root" --no-optional-locks diff --cached
} >"$host_git_before"
host_wrapper_before=""
if [ -f /usr/local/sbin/opentendril-greenhouse ]; then
  host_wrapper_before="$(sha256sum /usr/local/sbin/opentendril-greenhouse)"
fi

# --- happy path: replace, prove rollback, return to development ---

setup_fixture happy "$source_sha"
wrapper_before="$(sha256sum "${WORK}/sbin/opentendril-greenhouse")"
sentinel_before="$(sha256sum "${WORK}/tendril-home/.tendril/substrates/sample/SENTINEL" "${WORK}/tendril-home/.tendril/substrates/sample/.git/index" "${WORK}/tendril-home/.tendril/canary")"
status="$(run_deploy deploy)"
[ "$status" -eq 0 ] || fail "development deployment failed"
grep -F -- "DONE checkout ${source_sha}" "${WORK}/out" >/dev/null || fail "deploy did not report the checkout"
grep -F -- "Rollback: sudo make dev-rollback" "${WORK}/out" >/dev/null || fail "deploy did not report rollback"
require_run_contract
refuse_mutation_events deploy
if grep -E 'call (tag|image tag|container rm) ' "${WORK}/events" >/dev/null; then
  fail "deploy removed or retagged a container"
fi
grep -F -- 'call build ' "${WORK}/events" >/dev/null || fail "deploy did not build the development image"
grep -F -- 'runuser alice' "${WORK}/events" >/dev/null || fail "Pollinator commands did not use the discovered account"
grep -F -- 'runuser tendril' "${WORK}/events" >/dev/null || fail "Docker commands did not use tendril"
if grep -F -- 'systemctl restart' "${WORK}/events" >/dev/null; then
  fail "matching Stem was restarted"
fi
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/image")" = "$dev_id" ] || fail "active container is not the development image"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse-release/image")" = "$release_id" ] || fail "parked container is not the release image"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse-release/id")" = "$release_container_id" ] || fail "release container id changed"
[ ! -e "${WORK}/docker/containers/opentendril-greenhouse-dev" ] || fail "development hold was left behind"
[ "$(sha256sum "${WORK}/sbin/opentendril-greenhouse")" = "$wrapper_before" ] || fail "governed wrapper bytes changed"
[ "$(sha256sum "${WORK}/tendril-home/.tendril/substrates/sample/SENTINEL" "${WORK}/tendril-home/.tendril/substrates/sample/.git/index" "${WORK}/tendril-home/.tendril/canary")" = "$sentinel_before" ] || fail "Substrate or canary bytes changed"
state_file="${WORK}/alice/.local/share/opentendril-dev-deploy/state"
[ -f "$state_file" ] || fail "state file was not written"
grep -F -- "DEV_TAG=opentendril-greenhouse:dev" "$state_file" >/dev/null || fail "state tag drifted"
grep -F -- "DEV_IMAGE_ID=${dev_id}" "$state_file" >/dev/null || fail "state development id drifted"
grep -F -- "RELEASE_IMAGE_ID=${release_id}" "$state_file" >/dev/null || fail "state release id drifted"
grep -F -- "SOURCE_SHA=${source_sha}" "$state_file" >/dev/null || fail "state revision drifted"
assert_absent "$state_file" "$secret"
assert_absent "$state_file" "dr3w"

# --- explicit rollback of that same fixture ---

: >"${WORK}/events"
rm -f "${WORK}/run-args"
status="$(run_deploy rollback)"
[ "$status" -eq 0 ] || fail "rollback failed"
grep -F -- "DONE rollback" "${WORK}/out" >/dev/null || fail "rollback did not report completion"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/image")" = "$release_id" ] || fail "rollback did not restore the release image"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/id")" = "$release_container_id" ] || fail "rollback did not restore the release container"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse-dev/image")" = "$dev_id" ] || fail "development container was deleted during rollback"
if grep -E 'call (build|run|tag|container rm) ' "${WORK}/events" >/dev/null; then
  fail "rollback built, ran, retagged, or removed a container"
fi
[ "$(sha256sum "${WORK}/sbin/opentendril-greenhouse")" = "$wrapper_before" ] || fail "rollback edited the governed wrapper"
assert_absent "${WORK}/events" "$secret"
assert_absent "${WORK}/out" "$secret"

# --- wrong image revision fails before replacement ---

setup_fixture bad-revision "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "wrong image revision was accepted"
grep -F -- "Greenhouse image metadata is" "${WORK}/err" >/dev/null || fail "wrong revision did not fail closed"
grep -F -- 'call build ' "${WORK}/events" >/dev/null || fail "revision check did not inspect a built image"
if grep -E 'call (run|rename|tag|container rm) ' "${WORK}/events" >/dev/null; then
  fail "wrong revision mutated a container"
fi
[ -d "${WORK}/docker/containers/opentendril-greenhouse" ] || fail "release container disappeared"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/image")" = "$release_id" ] || fail "wrong revision changed the release container"
[ ! -e "${WORK}/docker/containers/opentendril-greenhouse-release" ] || fail "wrong revision parked the release container"
refuse_mutation_events "bad-revision"

# --- ownership mismatches fail before Docker ---

setup_fixture bad-repo-owner "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "foreign checkout owner was accepted"
grep -F -- "checkout is owned by root, expected alice" "${WORK}/err" >/dev/null || fail "foreign checkout owner produced the wrong failure"
if grep -F -- 'call ' "${WORK}/events" >/dev/null; then
  fail "foreign checkout owner reached Docker"
fi

setup_fixture bad-home-owner "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "foreign tendril home owner was accepted"
grep -F -- "/home/tendril is owned by alice, expected tendril" "${WORK}/err" >/dev/null || fail "foreign home owner produced the wrong failure"
if grep -F -- 'call ' "${WORK}/events" >/dev/null; then
  fail "foreign home owner reached Docker"
fi

# --- a new Stem that does not become healthy is restored, and Greenhouse is not started ---

setup_fixture stem-unhealthy "$old_sha"
stem_before="$(sha256sum "${WORK}/tendril-home/.local/bin/tendril")"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "unhealthy Stem was accepted"
grep -F -- "previous binary restored" "${WORK}/err" >/dev/null || fail "unhealthy Stem was not restored"
[ "$(sha256sum "${WORK}/tendril-home/.local/bin/tendril")" = "$stem_before" ] || fail "restored Stem bytes differ"
if grep -E 'call (build|run|rename|tag|container rm) ' "${WORK}/events" >/dev/null; then
  fail "unhealthy Stem continued into Greenhouse replacement"
fi
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/image")" = "$release_id" ] || fail "unhealthy Stem changed the release container"
refuse_mutation_events "stem-unhealthy"

{
  GIT_OPTIONAL_LOCKS=0 git -C "$repo_root" --no-optional-locks status --porcelain=v1 --untracked-files=all
  GIT_OPTIONAL_LOCKS=0 git -C "$repo_root" --no-optional-locks diff
  GIT_OPTIONAL_LOCKS=0 git -C "$repo_root" --no-optional-locks diff --cached
} >"$host_git_after"
cmp -s "$host_git_before" "$host_git_after" || fail "host checkout Git state changed"
rm -f "$host_git_before" "$host_git_after"

if [ -n "$host_wrapper_before" ]; then
  [ "$(sha256sum /usr/local/sbin/opentendril-greenhouse)" = "$host_wrapper_before" ] || fail "host Greenhouse wrapper changed"
fi

printf 'dev-deploy-test: ok\n'
