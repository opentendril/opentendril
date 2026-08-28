#!/usr/bin/env bash
set -euo pipefail

COMMAND="${1:-help}"
shift || true

TEST_NAME="${TEST_NAME:-opentendril-test-r13}"
RUN_DIR="${RUN_DIR:-$HOME/tmp/$TEST_NAME}"
CACHE_DIR="${CACHE_DIR:-$HOME/.cache/opentendril-test}"
SSH_PORT="${SSH_PORT:-22231}"
MEMORY_MB="${MEMORY_MB:-4096}"
HOST_MEMORY_RESERVE_MB="${HOST_MEMORY_RESERVE_MB:-2048}"
PROCESSORS="${PROCESSORS:-2}"
DISK_SIZE="${DISK_SIZE:-40G}"
MODE="${MODE:-strict}" # strict | fast

BASE_IMAGE="${BASE_IMAGE:-}"
BASE_IMAGE_SHA256="${BASE_IMAGE_SHA256:-}"
REFRESH_BASE_IMAGE="${REFRESH_BASE_IMAGE:-0}"
MODEL_SOURCE="${MODEL_SOURCE:-}"
MODEL_MOUNT="${MODEL_MOUNT:-/mnt/opentendril-model-cache}"

UBUNTU_IMAGE_URL="${UBUNTU_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
UBUNTU_SUMS_URL="${UBUNTU_SUMS_URL:-https://cloud-images.ubuntu.com/noble/current/SHA256SUMS}"
OVMF_CODE="${OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.fd}"
OVMF_VARS="${OVMF_VARS:-/usr/share/OVMF/OVMF_VARS_4M.fd}"

LOG="$RUN_DIR/qualification.log"
PID_FILE="$RUN_DIR/qemu.pid"
QMP_SOCKET="$RUN_DIR/qmp.sock"
SSH_KEY="$RUN_DIR/botanist-ed25519"
PASS_FILE="$RUN_DIR/.botanist-pass"
KNOWN_HOSTS="$RUN_DIR/known-hosts"

usage() {
    cat <<'USAGE'
Usage:
  scripts/qualification-machine.sh create [--reuse|--replace]
  scripts/qualification-machine.sh status
  scripts/qualification-machine.sh stop
  scripts/qualification-machine.sh cleanup
  scripts/qualification-machine.sh ssh [command ...]

Key environment variables:
  TEST_NAME              opentendril-test-r13
  RUN_DIR                $HOME/tmp/$TEST_NAME
  CACHE_DIR              $HOME/.cache/opentendril-test
  SSH_PORT               22231
  MEMORY_MB              4096
  HOST_MEMORY_RESERVE_MB 2048
  PROCESSORS              2
  DISK_SIZE              40G
  MODE                    strict | fast
  BASE_IMAGE              existing qcow2 base image; skips Ubuntu download
  BASE_IMAGE_SHA256       optional checksum for BASE_IMAGE
  REFRESH_BASE_IMAGE      1 to replace the cached Ubuntu image
  MODEL_SOURCE            fast mode only; host model directory shared read-only
  MODEL_MOUNT             /mnt/opentendril-model-cache
USAGE
}

ts() { date -u +'%Y-%m-%dT%H:%M:%SZ'; }
log() { printf '[%s] %s\n' "$(ts)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }

require() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

validate() {
    [[ "$TEST_NAME" == opentendril-test-* ]] || die "TEST_NAME must start with opentendril-test-"
    case "$MODE" in strict|fast) ;; *) die "MODE must be strict or fast" ;; esac
    for value in "$SSH_PORT" "$MEMORY_MB" "$HOST_MEMORY_RESERVE_MB" "$PROCESSORS"; do
        [[ "$value" =~ ^[1-9][0-9]*$ ]] || die "numeric settings must be positive integers"
    done
    [ "$SSH_PORT" -le 65535 ] || die "SSH_PORT must be <= 65535"
}

pid_is_qemu() {
    local pid="$1"
    kill -0 "$pid" 2>/dev/null || return 1
    ps -p "$pid" -o comm= 2>/dev/null | grep -qx 'qemu-system-x86_64'
}

recorded_pid() {
    [ -s "$PID_FILE" ] || return 1
    tr -d '[:space:]' < "$PID_FILE"
}

named_pid() {
    ps -eo pid=,args= | awk -v name="$TEST_NAME" '
        /[q]emu-system-x86_64/ && index($0, "-name " name) {print $1; exit}
    '
}

running_test_machines() {
    ps -eo pid=,rss=,etime=,args= | awk '/[q]emu-system-x86_64/ && /-name opentendril-test-/ {print}'
}

live_run_pid() {
    local pid
    pid="$(recorded_pid 2>/dev/null || true)"
    if [ -n "$pid" ] && pid_is_qemu "$pid"; then printf '%s\n' "$pid"; return; fi
    pid="$(named_pid || true)"
    if [ -n "$pid" ] && pid_is_qemu "$pid"; then printf '%s\n' "$pid"; fi
}

available_memory_mb() {
    awk '/^MemAvailable:/ {printf "%d\n", $2 / 1024}' /proc/meminfo
}

check_memory() {
    local available required
    available="$(available_memory_mb)"
    required=$((MEMORY_MB + HOST_MEMORY_RESERVE_MB))
    if [ "$available" -lt "$required" ]; then
        printf 'Insufficient available memory.\n\n' >&2
        printf 'Requested VM memory: %s MB\nHost reserve:        %s MB\nAvailable:           %s MB\n\n' \
            "$MEMORY_MB" "$HOST_MEMORY_RESERVE_MB" "$available" >&2
        printf 'Running OpenTendril test VMs:\n' >&2
        running_test_machines >&2 || true
        die "stop another VM, lower MEMORY_MB, or abort"
    fi
    log "memory available=${available}MB required=${required}MB"
}

qmp_powerdown() {
    [ -S "$QMP_SOCKET" ] || return 1
    python3 - "$QMP_SOCKET" <<'PY'
import json, socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(3)
s.connect(sys.argv[1])
f = s.makefile("rwb", buffering=0)
def receive():
    while True:
        obj = json.loads(f.readline())
        if "event" not in obj:
            return obj
if "QMP" not in receive():
    raise SystemExit("invalid QMP greeting")
for command in ("qmp_capabilities", "system_powerdown"):
    f.write(json.dumps({"execute": command}).encode() + b"\r\n")
    reply = receive()
    if "error" in reply:
        raise SystemExit(str(reply["error"]))
PY
}

wait_exit() {
    local pid="$1" seconds="$2"
    for ((i=0; i<seconds; i++)); do
        kill -0 "$pid" 2>/dev/null || return 0
        sleep 1
    done
    return 1
}

stop_pid() {
    local pid="$1" socket="${2:-}"
    pid_is_qemu "$pid" || return 0
    if [ -n "$socket" ] && [ -S "$socket" ]; then
        log "requesting ACPI shutdown through QMP pid=$pid"
        QMP_SOCKET="$socket" qmp_powerdown || true
        if wait_exit "$pid" 60; then log "QEMU exited cleanly pid=$pid"; return; fi
    fi
    log "sending SIGTERM pid=$pid"
    kill -TERM "$pid" 2>/dev/null || true
    if wait_exit "$pid" 15; then return; fi
    log "sending SIGKILL pid=$pid"
    kill -KILL "$pid" 2>/dev/null || true
    wait_exit "$pid" 5 || die "QEMU pid=$pid did not stop"
}

stop_run() {
    local pid
    pid="$(live_run_pid || true)"
    if [ -z "$pid" ]; then
        log "no running VM for $TEST_NAME"
    else
        stop_pid "$pid" "$QMP_SOCKET"
    fi
    rm -f "$PID_FILE" "$QMP_SOCKET"
}

ssh_base() {
    ssh -i "$SSH_KEY" -p "$SSH_PORT" \
        -o StrictHostKeyChecking=accept-new \
        -o UserKnownHostsFile="$KNOWN_HOSTS" \
        -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes \
        botanist@127.0.0.1 "$@"
}

guest_sudo() {
    local command="$1"
    [ -r "$PASS_FILE" ] || die "missing $PASS_FILE"
    cat "$PASS_FILE" | ssh -i "$SSH_KEY" -p "$SSH_PORT" \
        -o StrictHostKeyChecking=accept-new \
        -o UserKnownHostsFile="$KNOWN_HOSTS" \
        -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes \
        botanist@127.0.0.1 "sudo -S -p '' sh -c $(printf '%q' "$command")"
}

resolve_base_image() {
    local image_dir image sums expected actual
    if [ -n "$BASE_IMAGE" ]; then
        [ -r "$BASE_IMAGE" ] || die "BASE_IMAGE is not readable: $BASE_IMAGE"
        qemu-img info "$BASE_IMAGE" >/dev/null
        actual="$(sha256sum "$BASE_IMAGE" | awk '{print $1}')"
        [ -z "$BASE_IMAGE_SHA256" ] || [ "$actual" = "$BASE_IMAGE_SHA256" ] || die "BASE_IMAGE checksum mismatch"
        log "using explicit base image: $BASE_IMAGE sha256=$actual"
        RESOLVED_BASE_IMAGE="$BASE_IMAGE"
        return
    fi

    image_dir="$CACHE_DIR/ubuntu-noble"
    image="$image_dir/noble-server-cloudimg-amd64.img"
    sums="$image_dir/SHA256SUMS"
    mkdir -p "$image_dir"
    if [ "$REFRESH_BASE_IMAGE" = 1 ]; then rm -f "$image" "$image.sha256" "$sums"; fi

    if [ ! -f "$image" ]; then
        log "Ubuntu image cache miss"
        curl -fsSL --retry 5 --retry-all-errors -o "$sums.tmp" "$UBUNTU_SUMS_URL"
        expected="$(awk '$2 == "*noble-server-cloudimg-amd64.img" || $2 == "noble-server-cloudimg-amd64.img" {print $1; exit}' "$sums.tmp")"
        [ -n "$expected" ] || die "Ubuntu checksum not found"
        curl -fL --retry 5 --retry-all-errors -o "$image.tmp" "$UBUNTU_IMAGE_URL"
        actual="$(sha256sum "$image.tmp" | awk '{print $1}')"
        [ "$actual" = "$expected" ] || die "Ubuntu image checksum mismatch"
        mv "$sums.tmp" "$sums"; mv "$image.tmp" "$image"; printf '%s\n' "$expected" > "$image.sha256"
    else
        log "Ubuntu image cache hit: $image"
        actual="$(sha256sum "$image" | awk '{print $1}')"
        if [ -r "$image.sha256" ]; then
            expected="$(tr -d '[:space:]' < "$image.sha256")"
            [ "$actual" = "$expected" ] || die "cached Ubuntu image checksum mismatch"
        else
            printf '%s\n' "$actual" > "$image.sha256"
        fi
    fi
    log "base image sha256=$(cat "$image.sha256")"
    RESOLVED_BASE_IMAGE="$image"
}

find_model_source() {
    if [ -n "$MODEL_SOURCE" ]; then printf '%s\n' "$MODEL_SOURCE"; return; fi
    [ -d /usr/share/ollama/.ollama/models ] && { printf '%s\n' /usr/share/ollama/.ollama/models; return; }
    [ -d "$HOME/.ollama/models" ] && printf '%s\n' "$HOME/.ollama/models"
}

prepare_run_dir() {
    local replace="$1" answer
    mkdir -p "$RUN_DIR"
    if [ -e "$RUN_DIR/disk.qcow2" ] || [ -e "$SSH_KEY" ]; then
        if [ "$replace" != 1 ]; then
            if [ ! -t 0 ]; then die "inactive run data exists; use create --replace"; fi
            read -r -p "Inactive run data exists at $RUN_DIR. Replace it? [y/N] " answer
            [[ "${answer:-N}" =~ ^[Yy]$ ]] || die "aborted"
        fi
        rm -f "$RUN_DIR/disk.qcow2" "$RUN_DIR/seed.iso" "$RUN_DIR/vars.fd" \
            "$RUN_DIR/user-data" "$RUN_DIR/meta-data" "$PID_FILE" "$QMP_SOCKET" \
            "$SSH_KEY" "$SSH_KEY.pub" "$PASS_FILE" "$KNOWN_HOSTS" "$RUN_DIR/guest-ssh.sh"
    else
        rm -f "$PID_FILE" "$QMP_SOCKET"
    fi
}

write_guest_files() {
    local password public_key
    password="$(cat "$PASS_FILE")"; public_key="$(cat "$SSH_KEY.pub")"
    cat > "$RUN_DIR/user-data" <<EOF_USER
#cloud-config
hostname: $TEST_NAME
fqdn: $TEST_NAME.local
manage_etc_hosts: true
users:
  - name: botanist
    gecos: Botanist
    groups: [sudo]
    shell: /bin/bash
    lock_passwd: false
    sudo: ALL=(ALL) PASSWD:ALL
    ssh_authorized_keys:
      - $public_key
chpasswd:
  expire: false
  list: |
    botanist:$password
ssh_pwauth: false
timezone: UTC
package_update: false
package_upgrade: false
EOF_USER
    cat > "$RUN_DIR/meta-data" <<EOF_META
instance-id: $TEST_NAME-$(date -u +%Y%m%dT%H%M%SZ)
local-hostname: $TEST_NAME
EOF_META
    cat > "$RUN_DIR/guest-ssh.sh" <<EOF_SSH
#!/usr/bin/env bash
set -euo pipefail
exec ssh -i "$SSH_KEY" -p "$SSH_PORT" \
  -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$KNOWN_HOSTS" \
  -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes \
  botanist@127.0.0.1 "\$@"
EOF_SSH
    chmod 700 "$RUN_DIR/guest-ssh.sh"
}

create_machine() {
    local reuse=0 replace=0 pid answer model_source="" ssh_ready=0 format
    while [ "$#" -gt 0 ]; do
        case "$1" in --reuse) reuse=1 ;; --replace) replace=1 ;; *) die "unknown create option: $1" ;; esac
        shift
    done
    [ "$reuse" -eq 0 ] || [ "$replace" -eq 0 ] || die "choose --reuse or --replace, not both"

    validate
    for command in ssh ssh-keygen python3 curl sha256sum qemu-img qemu-system-x86_64 xorriso ss; do require "$command"; done
    [ -r "$OVMF_CODE" ] && [ -r "$OVMF_VARS" ] || die "OVMF firmware files are unavailable"
    [ -e /dev/kvm ] || die "/dev/kvm is unavailable"

    mkdir -p "$RUN_DIR" "$CACHE_DIR"; touch "$LOG"; chmod 600 "$LOG"
    exec > >(tee -a "$LOG") 2>&1
    log "create test_name=$TEST_NAME mode=$MODE run_dir=$RUN_DIR"

    pid="$(live_run_pid || true)"
    if [ -n "$pid" ]; then
        if [ "$reuse" -eq 1 ]; then status_machine; return; fi
        if [ "$replace" -eq 1 ]; then stop_run
        elif [ -t 0 ]; then
            printf 'An OpenTendril test VM is already running: name=%s pid=%s port=%s dir=%s\n' "$TEST_NAME" "$pid" "$SSH_PORT" "$RUN_DIR"
            read -r -p '[R]euse, [S]top and replace, [A]bort [A]: ' answer
            case "${answer:-A}" in R|r) status_machine; return ;; S|s) stop_run ;; *) die "aborted" ;; esac
        else
            die "test VM already running; use --reuse or --replace"
        fi
    fi

    prepare_run_dir "$replace"
    check_memory
    ss -ltnH | awk '{print $4}' | grep -Eq "(^|:)${SSH_PORT}$" && die "TCP port $SSH_PORT is already in use"
    resolve_base_image
    format="$(qemu-img info "$RESOLVED_BASE_IMAGE" | awk -F': ' '/file format:/ {print $2; exit}')"
    [ -n "$format" ] || die "could not determine base-image format"

    ssh-keygen -t ed25519 -N '' -f "$SSH_KEY" -C "$TEST_NAME-botanist" >/dev/null
    python3 - <<'PY_PASS' > "$PASS_FILE"
import secrets, string
alphabet = string.ascii_letters + string.digits
print("".join(secrets.choice(alphabet) for _ in range(20)))
PY_PASS
    chmod 600 "$SSH_KEY" "$PASS_FILE"
    write_guest_files

    xorriso -as genisoimage -output "$RUN_DIR/seed.iso" -volid CIDATA -joliet -rock \
        -graft-points "user-data=$RUN_DIR/user-data" "meta-data=$RUN_DIR/meta-data" >/dev/null
    cp "$OVMF_VARS" "$RUN_DIR/vars.fd"
    qemu-img create -f qcow2 -F "$format" -b "$RESOLVED_BASE_IMAGE" "$RUN_DIR/disk.qcow2" "$DISK_SIZE"

    QEMU_MODEL_ARGS=()
    if [ "$MODE" = fast ]; then
        model_source="$(find_model_source || true)"
        if [ -n "$model_source" ]; then
            [ -d "$model_source" ] || die "MODEL_SOURCE must be a directory: $model_source"
            log "fast model source=$model_source"
            QEMU_MODEL_ARGS=(-virtfs "local,path=$model_source,mount_tag=opentendril-model-cache,security_model=none,readonly=on")
        else
            log "fast mode: no local model source found"
        fi
    elif [ -n "$MODEL_SOURCE" ]; then
        die "MODEL_SOURCE is not permitted in strict mode"
    fi

    qemu-system-x86_64 -name "$TEST_NAME" -machine q35,accel=kvm -cpu host \
        -m "$MEMORY_MB" -smp "$PROCESSORS" \
        -drive "if=pflash,format=raw,readonly=on,file=$OVMF_CODE" \
        -drive "if=pflash,format=raw,file=$RUN_DIR/vars.fd" \
        -drive "if=virtio,file=$RUN_DIR/disk.qcow2,format=qcow2,discard=unmap" \
        -drive "if=virtio,file=$RUN_DIR/seed.iso,format=raw,readonly=on" \
        -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" -device virtio-net-pci,netdev=n0 \
        -device virtio-rng-pci "${QEMU_MODEL_ARGS[@]}" -display none -serial none \
        -qmp "unix:$QMP_SOCKET,server=on,wait=off" -pidfile "$PID_FILE" -daemonize

    pid="$(recorded_pid)"; pid_is_qemu "$pid" || die "QEMU exited during launch"
    log "QEMU started pid=$pid"
    for _ in $(seq 1 120); do
        if ssh_base true >/dev/null 2>&1; then ssh_ready=1; break; fi
        pid_is_qemu "$pid" || die "QEMU exited while waiting for SSH"
        sleep 2
    done
    [ "$ssh_ready" -eq 1 ] || die "SSH did not become ready"
    ssh_base 'cloud-init status --wait'
    ssh_base 'grep -E "^(ID|VERSION_ID)=" /etc/os-release; uname -m; id botanist'
    guest_sudo 'true'

    if [ "$MODE" = fast ] && [ -n "$model_source" ]; then
        guest_sudo "mkdir -p '$MODEL_MOUNT' && mount -t 9p -o trans=virtio,version=9p2000.L,ro opentendril-model-cache '$MODEL_MOUNT'"
        log "model cache mounted read-only at $MODEL_MOUNT"
        if [ -d "$model_source/manifests" ] && [ -d "$model_source/blobs" ]; then
            guest_sudo "mkdir -p /etc/systemd/system/ollama.service.d && printf '[Service]\nEnvironment=OLLAMA_MODELS=$MODEL_MOUNT\n' > /etc/systemd/system/ollama.service.d/10-model-cache.conf"
            log "Ollama service drop-in prepared to use the mounted model cache"
        fi
    fi
    log "ready ssh=$RUN_DIR/guest-ssh.sh password=$PASS_FILE"
}

status_machine() {
    local pid
    validate
    pid="$(live_run_pid || true)"
    printf 'test name:        %s\nrun directory:    %s\nSSH port:         %s\navailable memory: %s MB\n' \
        "$TEST_NAME" "$RUN_DIR" "$SSH_PORT" "$(available_memory_mb)"
    if [ -n "$pid" ]; then
        printf 'state:            running\nQEMU pid:         %s\n' "$pid"
        ps -p "$pid" -o pid=,rss=,etime=,args=
    else
        printf 'state:            stopped\n'
    fi
}

cleanup_machines() {
    local rows answer pid socket row
    rows="$(running_test_machines || true)"
    [ -n "$rows" ] || { printf 'No running OpenTendril test VMs found.\n'; return; }
    printf 'Running OpenTendril test VMs:\n%s\n' "$rows"
    [ -t 0 ] || die "cleanup requires an interactive terminal"
    read -r -p 'Gracefully stop all listed VMs? [y/N] ' answer
    [[ "${answer:-N}" =~ ^[Yy]$ ]] || die "cleanup aborted"
    while IFS= read -r row; do
        pid="$(awk '{print $1}' <<<"$row")"
        socket="$(sed -n 's/.*-qmp unix:\([^, ]*\).*/\1/p' <<<"$row")"
        stop_pid "$pid" "$socket"
    done <<< "$rows"
}

case "$COMMAND" in
    create) create_machine "$@" ;;
    status) [ "$#" -eq 0 ] || die "status takes no arguments"; status_machine ;;
    stop)
        [ "$#" -eq 0 ] || die "stop takes no arguments"
        mkdir -p "$RUN_DIR"; touch "$LOG"; chmod 600 "$LOG"; exec > >(tee -a "$LOG") 2>&1
        stop_run
        ;;
    cleanup) [ "$#" -eq 0 ] || die "cleanup takes no arguments"; cleanup_machines ;;
    ssh) validate; ssh_base "$@" ;;
    help|-h|--help) usage ;;
    *) usage >&2; exit 2 ;;
esac
