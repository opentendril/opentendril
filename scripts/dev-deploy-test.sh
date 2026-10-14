#!/usr/bin/env bash
# Hermetic tests for scripts/dev-deploy.sh.
#
# Bubblewrap hides the host tendril home, the governed wrapper, the Stem
# socket, and the absolute programs the deployment invokes. Nothing here
# starts the live Greenhouse or writes the checkout.
#
# GitHub-hosted Ubuntu 24.04 keeps
# kernel.apparmor_restrict_unprivileged_userns=1 and does not attach an
# AppArmor profile to /usr/bin/bwrap. Unprivileged bubblewrap 0.9 then
# fails with "setting up uid map: Permission denied". Root can create the
# mount namespace without a user namespace, so this harness retries through
# passwordless sudo only after that unprivileged probe fails. The root
# namespace keeps / read-only, and the runner has no /home/tendril, so the
# mounts that must be created sit on fresh tmpfs parents. Covering a program
# follows a symlink. A missing target cannot be created on that read-only
# root, so /usr/bin and /usr/sbin are mounted from a directory where each
# covered program is an ordinary file and every other name points back at
# the host /usr tree. That keeps relative links, such as ../lib/..., intact.
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

bwrap_bin=/usr/bin/bwrap
bwrap_as_root=0
bwrap_prefix=("$bwrap_bin")

select_bwrap() {
  local err denied
  err="$(mktemp)"
  if "$bwrap_bin" --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp -- /bin/true >/dev/null 2>"$err"; then
    rm -f "$err"
    return 0
  fi
  denied="$(tr '\n' ' ' <"$err")"
  if sudo -n -- "$bwrap_bin" --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp -- /bin/true >/dev/null 2>"$err"; then
    bwrap_as_root=1
    bwrap_prefix=(sudo -n -- "$bwrap_bin")
    printf 'dev-deploy-test: unprivileged bubblewrap was denied (%s); using a root mount namespace\n' \
      "$denied" >&2
    rm -f "$err"
    return 0
  fi
  printf 'dev-deploy-test: bubblewrap cannot create a mount namespace\n' >&2
  printf 'dev-deploy-test: unprivileged probe: %s\n' "$denied" >&2
  printf 'dev-deploy-test: root probe:\n' >&2
  cat "$err" >&2
  rm -f "$err"
  exit 1
}

select_bwrap

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
command_mirror_root=""

# Bubblewrap follows a symlink when it covers a program. A missing target
# then fails with "No such file or directory" because the read-only root
# cannot grow that path. Publish ordinary files for the programs this
# harness covers, and point every other name at the host /usr tree so a
# relative link still resolves.
prepare_command_mirrors() {
  command_mirror_root="$(mktemp -d /tmp/opentendril-dev-deploy-commands.XXXXXX)"
  works+=("$command_mirror_root")
  python3 - "$command_mirror_root" <<'PY'
import os
import sys

root = sys.argv[1]
parents = {
    "usr-bin": (
        "/usr/bin",
        "/tmp/host-usr/bin",
        {
            "id",
            "stat",
            "getent",
            "readlink",
            "systemctl",
            "curl",
            "git",
            "go",
            "install",
            "chown",
            "docker",
        },
    ),
    "usr-sbin": (
        "/usr/sbin",
        "/tmp/host-usr/sbin",
        {"runuser"},
    ),
}
for key, (parent, alias, covered) in parents.items():
    mirror = os.path.join(root, key)
    os.makedirs(mirror, exist_ok=True)
    present = set(os.listdir(parent)) if os.path.isdir(parent) else set()
    for name in present:
        dest = os.path.join(mirror, name)
        if name in covered:
            open(dest, "wb").close()
        else:
            os.symlink("%s/%s" % (alias, name), dest)
    for name in covered - present:
        open(os.path.join(mirror, name), "wb").close()
PY
}

cleanup() {
  local dir
  for dir in "${works[@]+"${works[@]}"}"; do
    if [ "${bwrap_as_root:-0}" -eq 1 ]; then
      sudo -n -- rm -rf "$dir"
    else
      rm -rf "$dir"
    fi
  done
}
trap cleanup EXIT
prepare_command_mirrors

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
  /tmp/work/alice/.local/share/opentendril-dev-deploy|/tmp/work/alice/.local/share/opentendril-dev-deploy/*)
    if [ "$testmode" = root-owned-state ]; then
      owner=root
      group=root
    else
      owner=alice
      group=alice
    fi
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
    if [ -f /tmp/work/mode ] && [ "$(cat /tmp/work/mode)" = stem-no-socket ] && [ ! -f /tmp/work/socket-removed ]; then
      rm -f /var/lib/opentendril-transport/stem.sock
      : >/tmp/work/socket-removed
    fi
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
    if [ "$dir" != "$repo" ] && [ "$(tr -d '[:space:]' </tmp/work/mode)" = protected-index ]; then
      n=0
      if [ -f /tmp/work/substrate-status-count ]; then
        n=$(tr -d '[:space:]' </tmp/work/substrate-status-count)
      fi
      n=$((n + 1))
      printf '%s\n' "$n" >/tmp/work/substrate-status-count
      if [ "$n" -ge 2 ]; then
        printf ' M SENTINEL\n'
      fi
    fi
    ;;
  "rev-parse --show-toplevel")
    printf '%s\n' "$dir"
    ;;
  for-each-ref*)
    sha=$(tr -d '[:space:]' </tmp/work/substrate-sha)
    n=0
    if [ -f /tmp/work/for-each-ref-count ]; then
      n=$(tr -d '[:space:]' </tmp/work/for-each-ref-count)
    fi
    n=$((n + 1))
    printf '%s\n' "$n" >/tmp/work/for-each-ref-count
    mode=$(tr -d '[:space:]' </tmp/work/mode)
    printf '%s refs/heads/main -\n' "$sha"
    head_oid=$sha
    head_sym=refs/remotes/origin/main
    if [ "$n" -ge 2 ] && [ "$mode" = protected-remote-head ]; then
      head_sym=refs/remotes/origin/other
    fi
    if [ "$n" -ge 2 ] && [ "$mode" = remote-head-peeled ]; then
      head_oid=$(tr -d '[:space:]' </tmp/work/old-sha)
    fi
    printf '%s refs/remotes/origin/HEAD %s\n' "$head_oid" "$head_sym"
    if [ "$n" -ge 2 ]; then
      case "$mode" in
        external-remote-ref|mixed-protected)
          printf '%s refs/remotes/origin/staging/ai-greenhouse-dev-lifecycle -\n' "$sha"
          ;;
      esac
      case "$mode" in
        mixed-protected)
          printf '%s refs/heads/sprout/task-fruit -\n' "$(tr -d '[:space:]' </tmp/work/old-sha)"
          ;;
        protected-owned)
          printf '%s refs/opentendril/owned/qualification -\n' "$(tr -d '[:space:]' </tmp/work/old-sha)"
          ;;
        protected-tag)
          printf '%s refs/tags/v9 -\n' "$(tr -d '[:space:]' </tmp/work/old-sha)"
          ;;
      esac
    fi
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
dest=${positionals[${#positionals[@]}-1]}
if [ -f /tmp/work/mode ] && [ "$(tr -d '[:space:]' </tmp/work/mode)" = state-write-fail ]; then
  case "$dest" in
    */opentendril-dev-deploy/state|*/opentendril-dev-deploy/state.incoming)
      printf 'install refused state file\n' >>/tmp/work/events
      exit 1
      ;;
  esac
fi
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
        if [ -f /tmp/work/fail-start-remaining ]; then
          left=$(tr -d '[:space:]' </tmp/work/fail-start-remaining)
          case "$left" in
            ''|*[!0-9]*) die "bad fail-start-remaining ${left}" ;;
          esac
          if [ "$left" -gt 0 ]; then
            printf '%s\n' "$((left - 1))" >/tmp/work/fail-start-remaining
            die "injected start failure"
          fi
        fi
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
  local sudo_uid=${2-1500}
  local status=0
  set +e
  "${bwrap_prefix[@]}" \
    --ro-bind / / \
    --dev /dev \
    --proc /proc \
    --tmpfs /tmp \
    --tmpfs /home \
    --tmpfs /var/lib \
    --tmpfs /usr/local \
    --ro-bind /usr /tmp/host-usr \
    --ro-bind "${command_mirror_root}/usr-bin" /usr/bin \
    --ro-bind "${command_mirror_root}/usr-sbin" /usr/sbin \
    --bind "$WORK" /tmp/work \
    --ro-bind "$repo_root" "$repo_root" \
    --bind "${WORK}/tendril-home" /home/tendril \
    --bind "${WORK}/sbin" /usr/local/sbin \
    --bind "${WORK}/run-user" /run/user \
    --bind "${WORK}/transport" /var/lib/opentendril-transport \
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
      SUDO_UID="$sudo_uid" \
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
  # Root-created files must be readable by the invoking user. Adding
  # execute for everyone would turn a 0750 Stem binary into 0755, and
  # the next command would reject that mode.
  if [ "$bwrap_as_root" -eq 1 ]; then
    sudo -n -- chown -R "$(id -u):$(id -g)" "$WORK"
  fi
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

assert_release_preserved() {
  local name=$1
  [ -f "${WORK}/docker/containers/${name}/id" ] || fail "release container ${name} is missing"
  [ "$(tr -d '[:space:]' <"${WORK}/docker/containers/${name}/id")" = "$release_container_id" ] || fail "release container id changed"
  [ "$(tr -d '[:space:]' <"${WORK}/docker/containers/${name}/image")" = "$release_id" ] || fail "release container image changed"
  assert_absent "${WORK}/events" "refusing to remove release"
  assert_absent "${WORK}/events" "refusing to retag"
  if grep -E 'call (tag|image tag) ' "${WORK}/events" >/dev/null; then
    fail "a container identity was retagged"
  fi
}

assert_running_publisher() {
  local image=$1
  [ -f "${WORK}/docker/containers/opentendril-greenhouse/status" ] || fail "active Greenhouse container is missing"
  [ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/status")" = running ] || fail "publisher is not running"
  [ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/image")" = "$image" ] || fail "publisher image drifted"
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
[ "$(stat -c '%a' "$(dirname "$state_file")")" = 750 ] || fail "state directory mode is not 0750"
[ "$(stat -c '%a' "$state_file")" = 640 ] || fail "state file mode is not 0640"
[ ! -e "$(dirname "$state_file")/.state-write-probe" ] || fail "state directory probe was left behind"
[ ! -e "$(dirname "$state_file")/state.incoming" ] || fail "incoming state file was left behind"
awk '
  /install -d -o alice -g alice -m 0750 \/tmp\/work\/alice\/.local\/share\/opentendril-dev-deploy$/ { if (!dir) dir = NR }
  /install -o alice -g alice -m 0640 / && /opentendril-dev-deploy\/.state-write-probe$/ { if (!probe) probe = NR }
  /^call / { if (!call) call = NR }
  END {
    if (!dir || !probe) exit 1
    if (call && (dir > call || probe > call)) exit 2
  }
' "${WORK}/events" || fail "state directory was not created for alice before Docker"
if grep -F 'chown ' "${WORK}/events" | grep -F 'opentendril-dev-deploy' >/dev/null; then
  fail "state path was handed to chown"
fi
[ ! -e "${WORK}/docker/containers/opentendril-greenhouse-prior" ] || fail "first deployment held a previous container"

# --- repeat deployment records state again without taking ownership ---

first_state=$(cat "$state_file")
status="$(run_deploy deploy)"
[ "$status" -eq 0 ] || fail "repeat deployment failed"
grep -F -- "DONE checkout ${source_sha}" "${WORK}/out" >/dev/null || fail "repeat deployment did not report the checkout"
grep -F -- "SOURCE_SHA=${source_sha}" "$state_file" >/dev/null || fail "repeat deployment lost the recorded revision"
grep -F -- "RELEASE_CONTAINER_ID=${release_container_id}" "$state_file" >/dev/null || fail "repeat deployment lost the release container id"
if grep -F -- "refusing to take ownership" "${WORK}/err" >/dev/null; then
  fail "repeat deployment tried to take ownership"
fi
[ "$(stat -c '%a' "$state_file")" = 640 ] || fail "repeat deployment state file mode drifted"
[ ! -e "${WORK}/docker/containers/opentendril-greenhouse-prior" ] || fail "previous development container was left behind"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse-release/id")" = "$release_container_id" ] || fail "repeat deployment changed the release container id"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/image")" = "$dev_id" ] || fail "repeat deployment did not leave the development image running"
if grep -F 'chown ' "${WORK}/events" | grep -F 'opentendril-dev-deploy' >/dev/null; then
  fail "repeat deployment handed the state path to chown"
fi
grep -F -- "RELEASE_CONTAINER_ID=${release_container_id}" <<<"$first_state" >/dev/null || fail "first deployment did not record the release container"

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

# --- a Stem that never presents the governed socket is restored before Greenhouse starts ---

setup_fixture stem-no-socket "$old_sha"
stem_before="$(sha256sum "${WORK}/tendril-home/.local/bin/tendril")"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "Stem without a socket was accepted"
grep -F -- "previous binary restored" "${WORK}/err" >/dev/null || fail "socketless Stem was not restored"
[ -f "${WORK}/socket-removed" ] || fail "the governed socket was not removed"
[ ! -S "${WORK}/transport/stem.sock" ] || fail "the governed socket was recreated by the test shim"
[ "$(sha256sum "${WORK}/tendril-home/.local/bin/tendril")" = "$stem_before" ] || fail "socketless Stem bytes were not restored"
restarts=$(grep -c 'systemctl restart tendril.service' "${WORK}/events" || true)
[ "$restarts" -eq 2 ] || fail "expected one restart of the new Stem and one of the restored Stem, saw ${restarts}"
if grep -E 'call (build|run|rename|tag|container rm) ' "${WORK}/events" >/dev/null; then
  fail "socketless Stem continued into Greenhouse replacement"
fi
assert_release_preserved opentendril-greenhouse
refuse_mutation_events "stem-no-socket"

# --- the invoking uid must match the named account before any Docker call ---

setup_fixture happy "$source_sha"
status="$(run_deploy deploy 1501)"
[ "$status" -ne 0 ] || fail "a mismatched invoking uid was accepted"
grep -F -- "does not match the invoking uid" "${WORK}/err" >/dev/null || fail "mismatched uid produced the wrong failure"
if grep -F -- 'call ' "${WORK}/events" >/dev/null; then
  fail "mismatched uid reached Docker"
fi

setup_fixture happy "$source_sha"
status="$(run_deploy deploy "")"
[ "$status" -ne 0 ] || fail "a missing invoking uid was accepted"
grep -F -- "cannot identify the invoking account uid" "${WORK}/err" >/dev/null || fail "missing uid produced the wrong failure"
if grep -F -- 'call ' "${WORK}/events" >/dev/null; then
  fail "missing uid reached Docker"
fi

# --- a failed container start restores the last development publisher ---

setup_fixture happy "$source_sha"
printf '1\n' >"${WORK}/fail-start-remaining"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "an interrupted deployment was accepted"
grep -F -- "deployment failed; restoring a Greenhouse container" "${WORK}/out" >/dev/null || fail "interrupted deployment did not restore a publisher"
grep -F -- "injected start failure" "${WORK}/err" >/dev/null || fail "interrupted deployment did not fail the injected start"
assert_running_publisher "$dev_id"
assert_release_preserved opentendril-greenhouse-release
if grep -E 'call container rm ' "${WORK}/events" >/dev/null; then
  fail "interrupted deployment removed a container"
fi

# --- when that development publisher cannot start, the preserved release container returns ---

setup_fixture happy "$source_sha"
printf '2\n' >"${WORK}/fail-start-remaining"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "a deployment with two failed starts was accepted"
grep -F -- "deployment failed; restoring a Greenhouse container" "${WORK}/out" >/dev/null || fail "second start failure did not restore a publisher"
assert_running_publisher "$release_id"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/id")" = "$release_container_id" ] || fail "restored publisher is not the release container"
assert_release_preserved opentendril-greenhouse
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse-dev/image")" = "$dev_id" ] || fail "development container was deleted while returning to the release"
if grep -E 'call container rm ' "${WORK}/events" >/dev/null; then
  fail "release fallback removed a container"
fi

# --- an interrupted rollback keeps the release container and returns the development publisher ---

setup_fixture happy "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -eq 0 ] || fail "deployment before the interrupted rollback failed"
printf '1\n' >"${WORK}/fail-start-remaining"
: >"${WORK}/events"
wrapper_before="$(sha256sum "${WORK}/sbin/opentendril-greenhouse")"
status="$(run_deploy rollback)"
[ "$status" -ne 0 ] || fail "an interrupted rollback was accepted"
grep -F -- "deployment failed; restoring a Greenhouse container" "${WORK}/out" >/dev/null || fail "interrupted rollback did not restore a publisher"
assert_running_publisher "$dev_id"
assert_release_preserved opentendril-greenhouse-release
[ "$(sha256sum "${WORK}/sbin/opentendril-greenhouse")" = "$wrapper_before" ] || fail "interrupted rollback edited the governed wrapper"
if grep -E 'call (build|run|tag|container rm) ' "${WORK}/events" >/dev/null; then
  fail "interrupted rollback built, ran, retagged, or removed a container"
fi

# Stem startup refreshes a managed Substrate with git fetch. That fetch can
# add one remote-tracking ref while HEAD, the index, local branches, and
# Fruit branches stay put. The deployment must report that refresh and
# finish. A protected change in the same window must still fail by name.

setup_fixture external-remote-ref "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -eq 0 ] || fail "an external remote-tracking ref failed deployment"
grep -F -- "external remote-tracking refresh, not a deployment mutation: refs/remotes/origin/staging/ai-greenhouse-dev-lifecycle created" "${WORK}/out" >/dev/null || fail "remote-tracking refresh was not classified"
if grep -F -- "workspace, Fruit, or Git state changed during deployment" "${WORK}/err" >/dev/null; then
  fail "remote-tracking refresh was classified as a protected mutation"
fi
if grep -F -- "FAILED:" "${WORK}/err" >/dev/null; then
  fail "remote-tracking refresh produced a deployment failure"
fi
[ -f "${WORK}/alice/.local/share/opentendril-dev-deploy/state" ] || fail "remote-tracking refresh stopped before deployment state was recorded"

setup_fixture mixed-protected "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "a new local branch was ignored because a remote-tracking ref also appeared"
grep -F -- "local branch changed during deployment:" "${WORK}/err" >/dev/null || fail "local branch change was not named"
grep -F -- "refs/heads/sprout/task-fruit" "${WORK}/err" >/dev/null || fail "Fruit branch change was not named"
grep -F -- "external remote-tracking refresh, not a deployment mutation: refs/remotes/origin/staging/ai-greenhouse-dev-lifecycle created" "${WORK}/out" >/dev/null || fail "mixed change hid the remote-tracking refresh"
if grep -F -- "refs/remotes/origin/staging/ai-greenhouse-dev-lifecycle" "${WORK}/err" >/dev/null; then
  fail "remote-tracking refresh was reported as the protected failure"
fi
if grep -F -- "workspace, Fruit, or Git state changed during deployment" "${WORK}/err" >/dev/null; then
  fail "mixed change used the combined Git state failure"
fi

setup_fixture protected-owned "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "a Tendril-owned ref change was ignored"
grep -F -- "Tendril-owned ref changed during deployment:" "${WORK}/err" >/dev/null || fail "Tendril-owned ref change was not named"
grep -F -- "refs/opentendril/owned/qualification" "${WORK}/err" >/dev/null || fail "Tendril-owned ref was not identified"

setup_fixture protected-index "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "an index or worktree change was ignored"
grep -F -- "index or worktree changed during deployment:" "${WORK}/err" >/dev/null || fail "index or worktree change was not named"
grep -F -- "SENTINEL" "${WORK}/err" >/dev/null || fail "index or worktree change did not name the status"

setup_fixture protected-tag "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "an unexpected tag ref was ignored"
grep -F -- "unexpected Git ref changed during deployment:" "${WORK}/err" >/dev/null || fail "unexpected tag was not named"
grep -F -- "refs/tags/v9" "${WORK}/err" >/dev/null || fail "unexpected tag ref was not identified"

setup_fixture protected-remote-head "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "a remote HEAD change was treated as a branch refresh"
grep -F -- "HEAD changed during deployment:" "${WORK}/err" >/dev/null || fail "remote HEAD change was not named"
grep -F -- "refs/remotes/origin/HEAD" "${WORK}/err" >/dev/null || fail "remote HEAD ref was not identified"
if grep -F -- "external remote-tracking refresh" "${WORK}/out" >/dev/null; then
  fail "remote HEAD change was classified as a remote-tracking refresh"
fi

setup_fixture remote-head-peeled "$source_sha"
status="$(run_deploy deploy)"
[ "$status" -eq 0 ] || fail "a peeled remote HEAD oid failed deployment without a symref change"
if grep -F -- "HEAD changed during deployment:" "${WORK}/err" >/dev/null; then
  fail "peeled remote HEAD oid was classified as a HEAD change"
fi
if grep -F -- "FAILED:" "${WORK}/err" >/dev/null; then
  fail "peeled remote HEAD oid produced a deployment failure"
fi

# A root-owned recovery file is the record left by an earlier deployment
# that created the directory as root. Deploy must stop before replacing
# binaries or containers. Rollback may read it and must not take ownership.

setup_fixture root-owned-state "$source_sha"
state_dir="${WORK}/alice/.local/share/opentendril-dev-deploy"
mkdir -p "$state_dir"
wrapper_sha="$(sha256sum "${WORK}/sbin/opentendril-greenhouse" | awk '{print $1}')"
cat >"${state_dir}/state" <<EOF
SOURCE_SHA=${old_sha}
DEV_VERSION=${dev_version}
RELEASE_IMAGE_ID=${release_id}
RELEASE_CONTAINER_ID=${release_container_id}
DEV_IMAGE_ID=${dev_id}
DEV_TAG=opentendril-greenhouse:dev
WRAPPER_SHA=${wrapper_sha}
ACTIVE=opentendril-greenhouse
RELEASE_HOLD=opentendril-greenhouse-release
DEV_HOLD=opentendril-greenhouse-dev
EOF
chmod 0755 "$state_dir"
chmod 0644 "${state_dir}/state"
root_state_before="$(sha256sum "${state_dir}/state")"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "root-owned deployment state was accepted"
grep -F -- "owned by root, expected alice; refusing to take ownership" "${WORK}/err" >/dev/null || fail "root-owned state produced the wrong failure"
grep -F -- "SOURCE_SHA=${old_sha}" "${WORK}/err" >/dev/null || fail "root-owned failure did not name the preserved revision"
grep -F -- "RELEASE_CONTAINER_ID=${release_container_id}" "${WORK}/err" >/dev/null || fail "root-owned failure did not name the preserved release container"
grep -F -- "left unchanged" "${WORK}/err" >/dev/null || fail "root-owned failure did not say the recovery state was left unchanged"
[ "$(sha256sum "${state_dir}/state")" = "$root_state_before" ] || fail "root-owned recovery state was modified"
[ "$(stat -c '%a' "$state_dir")" = 755 ] || fail "root-owned state directory mode changed"
[ "$(stat -c '%a' "${state_dir}/state")" = 644 ] || fail "root-owned state file mode changed"
if grep -E '^(call|install|chown) ' "${WORK}/events" >/dev/null; then
  fail "root-owned state reached Docker, install, or chown"
fi
if grep -F -- "DONE checkout" "${WORK}/out" >/dev/null; then
  fail "root-owned state reported a recorded checkout"
fi

: >"${WORK}/events"
status="$(run_deploy rollback)"
[ "$status" -eq 0 ] || fail "rollback could not read root-owned recovery state"
grep -F -- "using root-owned deployment recovery state without changing its ownership" "${WORK}/out" >/dev/null || fail "rollback did not say it left root ownership unchanged"
[ "$(sha256sum "${state_dir}/state")" = "$root_state_before" ] || fail "rollback modified root-owned recovery state"
[ "$(stat -c '%a' "${state_dir}/state")" = 644 ] || fail "rollback changed the root-owned state file mode"
if grep -F 'chown ' "${WORK}/events" | grep -F 'opentendril-dev-deploy' >/dev/null; then
  fail "rollback took ownership of the recovery state"
fi
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/image")" = "$release_id" ] || fail "root-owned rollback did not leave the release image"
[ "$(tr -d '[:space:]' <"${WORK}/docker/containers/opentendril-greenhouse/id")" = "$release_container_id" ] || fail "root-owned rollback did not keep the release container"

# A failed state write after a deployment must not replace the recorded
# revision. The previous binaries and the previous Greenhouse publisher
# come back, and the recovery file stays byte for byte.

setup_fixture state-write-fail "$old_sha"
state_dir="${WORK}/alice/.local/share/opentendril-dev-deploy"
mkdir -p "$state_dir"
wrapper_sha="$(sha256sum "${WORK}/sbin/opentendril-greenhouse" | awk '{print $1}')"
cat >"${state_dir}/state" <<EOF
SOURCE_SHA=${old_sha}
DEV_VERSION=${dev_version}
RELEASE_IMAGE_ID=${release_id}
RELEASE_CONTAINER_ID=${release_container_id}
DEV_IMAGE_ID=${dev_id}
DEV_TAG=opentendril-greenhouse:dev
WRAPPER_SHA=${wrapper_sha}
ACTIVE=opentendril-greenhouse
RELEASE_HOLD=opentendril-greenhouse-release
DEV_HOLD=opentendril-greenhouse-dev
EOF
chmod 0750 "$state_dir"
chmod 0640 "${state_dir}/state"
active="${WORK}/docker/containers/opentendril-greenhouse"
release="${WORK}/docker/containers/opentendril-greenhouse-release"
mkdir -p "$release"
cp "$active/id" "$active/image" "$active/health" "$active/ports" "$release/"
printf 'exited\n' >"$release/status"
printf '%s\n' "$dev_id" >"$active/image"
printf '%s\n' "$dev_container_id" >"$active/id"
printf 'running\n' >"$active/status"
printf 'healthy\n' >"$active/health"
printf '127.0.0.1:4173->8080/tcp\n' >"$active/ports"
printf 'previous\n' >"$active/previous-marker"
recovery_state_before="$(sha256sum "${state_dir}/state")"
stem_before="$(sha256sum "${WORK}/tendril-home/.local/bin/tendril")"
mcp_before="$(sha256sum "${WORK}/alice/.local/bin/tendril-mcp")"
status="$(run_deploy deploy)"
[ "$status" -ne 0 ] || fail "a failed state write was accepted"
grep -F -- "deployment state was not replaced; recorded revision remains ${old_sha}" "${WORK}/err" >/dev/null || fail "failed state write did not keep the recorded revision"
grep -F -- "This checkout was not recorded" "${WORK}/err" >/dev/null || fail "failed state write recorded the checkout"
if grep -F -- "DONE checkout" "${WORK}/out" >/dev/null; then
  fail "failed state write reported a recorded checkout"
fi
[ "$(sha256sum "${state_dir}/state")" = "$recovery_state_before" ] || fail "failed state write replaced the recovery file"
grep -F -- "SOURCE_SHA=${old_sha}" "${state_dir}/state" >/dev/null || fail "recovery file lost its revision"
if grep -F -- "SOURCE_SHA=${source_sha}" "${state_dir}/state" >/dev/null; then
  fail "recovery file reports the unrecorded checkout"
fi
[ "$(sha256sum "${WORK}/tendril-home/.local/bin/tendril")" = "$stem_before" ] || fail "failed state write left the new Stem in place"
[ "$(sha256sum "${WORK}/alice/.local/bin/tendril-mcp")" = "$mcp_before" ] || fail "failed state write left the new MCP client in place"
[ -f "$active/previous-marker" ] || fail "previous Greenhouse publisher was not restored"
[ ! -e "${WORK}/docker/containers/opentendril-greenhouse-prior" ] || fail "previous Greenhouse publisher was left under the hold name"
[ "$(tr -d '[:space:]' <"$active/image")" = "$dev_id" ] || fail "restored Greenhouse publisher is not the previous development image"
[ "$(tr -d '[:space:]' <"$release/id")" = "$release_container_id" ] || fail "failed state write changed the release container"
grep -F -- "install refused state file" "${WORK}/events" >/dev/null || fail "state write did not fail at the state file"
[ ! -e "${state_dir}/state.incoming" ] || fail "incoming state file remained after the failed write"
[ ! -e "${state_dir}/.state-write-probe" ] || fail "state probe remained after the failed write"

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
