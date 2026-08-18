#!/usr/bin/env bash
# Prove the containerized Greenhouse's Compose deployment model:
# unix (--profile ui) mounts only /var/lib/opentendril-transport read-only;
# explicit TCP (--profile ui-tcp) has no Unix transport bind; a missing
# unix path/socket never falls back to TCP. Host
# /var/lib/opentendril-transport may be present or absent.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
transport_sh="${root}/ui/nginx/15-stem-transport.sh"
nginx_template="${root}/ui/nginx/default.conf.template"
compose_file="${root}/docker-compose.yml"
failed=0
tcp_project="ot-gh-tcp-check"
unix_missing_project="ot-gh-unix-missing"
workdir="$(mktemp -d)"

pass() { echo "  PASS  $*"; }
fail() { echo "  FAIL  $*"; failed=1; }

assert_file_contains() {
  local file="$1" needle="$2" label="$3"
  if grep -Fq -- "$needle" "$file"; then
    pass "$label"
  else
    fail "$label (missing ${needle} in ${file})"
  fi
}

assert_file_lacks() {
  local file="$1" needle="$2" label="$3"
  if grep -Fq -- "$needle" "$file"; then
    fail "$label (found ${needle} in ${file})"
  else
    pass "$label"
  fi
}

compose_cmd() {
  env -u STEM_TRANSPORT -u STEM_SOCKET -u STEM_HOST -u STEM_PORT \
    -u STEM_GATEWAY_PORT -u UI_BIND -u UI_PORT \
    docker compose -f "$compose_file" "$@"
}

cleanup() {
  docker compose -f "$compose_file" -p "$tcp_project" --profile ui-tcp down --remove-orphans >/dev/null 2>&1 || true
  if [ -n "${unix_missing_file:-}" ]; then
    docker compose -f "$unix_missing_file" -p "$unix_missing_project" --profile ui down --remove-orphans >/dev/null 2>&1 || true
  fi
  if [ -n "${sock_holder:-}" ]; then
    kill "$sock_holder" 2>/dev/null || true
  fi
  rm -rf "$workdir"
}
trap cleanup EXIT

echo "== Compose Unix default (--profile ui) =="

unix_json="$(compose_cmd --profile ui config --format json)"
# STEM_TRANSPORT=tcp must not retarget the unix profile.
unix_forced_tcp_json="$(STEM_TRANSPORT=tcp STEM_HOST=host.docker.internal \
  env -u STEM_SOCKET -u STEM_PORT -u STEM_GATEWAY_PORT -u UI_BIND -u UI_PORT \
  docker compose -f "$compose_file" --profile ui config --format json)"

python3 - "$unix_json" "$unix_forced_tcp_json" <<'PY' || failed=1
import json, sys

cfg = json.loads(sys.argv[1])
forced = json.loads(sys.argv[2])
ui = cfg["services"]["ui"]
errors = []

def ok(msg):
    print("  PASS  " + msg)

def bad(msg):
    print("  FAIL  " + msg)
    errors.append(msg)

if "ui-tcp" in cfg.get("services", {}):
    bad("unix profile does not include the TCP service")
else:
    ok("unix profile does not include the TCP service")

net = ui.get("network_mode") or ui.get("networkMode")
if net == "host":
    bad("host network mode is absent")
else:
    ok("host network mode is absent")

extra = ui.get("extra_hosts") or ui.get("extraHosts") or {}
if extra:
    bad("default extra_hosts is empty (got %s)" % extra)
else:
    ok("default extra_hosts is empty")

if ui.get("read_only") is True or ui.get("readOnly") is True:
    ok("read-only root filesystem")
else:
    bad("read-only root filesystem")

cap_drop = ui.get("cap_drop") or ui.get("capDrop") or []
if "ALL" in cap_drop:
    ok("all capabilities dropped")
else:
    bad("all capabilities dropped (got %s)" % cap_drop)

sec = ui.get("security_opt") or ui.get("securityOpt") or []
if any("no-new-privileges" in str(item) for item in sec):
    ok("no-new-privileges")
else:
    bad("no-new-privileges (got %s)" % sec)

ports = ui.get("ports") or []
published = json.dumps(ports)
if "127.0.0.1" in published and "4173" in published:
    ok("loopback UI publication")
else:
    bad("loopback UI publication (got %s)" % ports)

env = ui.get("environment") or {}
if isinstance(env, list):
    env = dict(item.split("=", 1) for item in env)
if env.get("STEM_TRANSPORT") == "unix":
    ok("unix profile pins STEM_TRANSPORT=unix")
else:
    bad("unix profile pins STEM_TRANSPORT=unix (got %r)" % env.get("STEM_TRANSPORT"))
if env.get("STEM_SOCKET") == "/var/lib/opentendril-transport/stem.sock":
    ok("STEM_SOCKET defaults to /var/lib/opentendril-transport/stem.sock")
else:
    bad("STEM_SOCKET default (got %r)" % env.get("STEM_SOCKET"))
if env.get("STEM_HOST") in ("", None):
    ok("unix profile does not set STEM_HOST")
else:
    bad("unix profile does not set STEM_HOST (got %r)" % env.get("STEM_HOST"))
if "host.docker.internal" in json.dumps(env):
    bad("default local Greenhouse does not depend on host.docker.internal")
else:
    ok("default local Greenhouse does not depend on host.docker.internal")

secret_keys = [k for k in env if any(part in k.upper() for part in
               ("BOTANIST", "API_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "GRANT"))]
if secret_keys:
    bad("no credentials configured in the container (got %s)" % secret_keys)
else:
    ok("no credentials configured in the container")

binds = []
for vol in ui.get("volumes") or []:
    if isinstance(vol, str):
        if vol.startswith("/tmp") or ":/tmp" in vol:
            continue
        binds.append(vol)
        continue
    vtype = vol.get("type")
    target = vol.get("target") or ""
    if vtype == "tmpfs" or target in ("/tmp", "/etc/nginx/conf.d"):
        continue
    if vtype == "bind":
        binds.append(vol)

def bind_source(vol):
    if isinstance(vol, str):
        return vol.split(":", 1)[0]
    return vol.get("source")

if any(bind_source(vol) in ("/var/lib", "/var/lib/") for vol in binds):
    bad("whole /var/lib is not mounted")
else:
    ok("whole /var/lib is not mounted")

if len(binds) != 1:
    bad("only /var/lib/opentendril-transport is bind-mounted (got %s)" % binds)
    vol = None
else:
    vol = binds[0]
    if isinstance(vol, str):
        ok_mount = vol.startswith("/var/lib/opentendril-transport:/var/lib/opentendril-transport") and ":ro" in vol
    else:
        source = vol.get("source")
        target = vol.get("target")
        read_only = bool(vol.get("read_only") or vol.get("readOnly"))
        ok_mount = source == "/var/lib/opentendril-transport" and target == "/var/lib/opentendril-transport" and read_only
    if ok_mount:
        ok("mounts only /var/lib/opentendril-transport read-only")
    else:
        bad("mounts only /var/lib/opentendril-transport read-only (got %s)" % vol)
    if isinstance(vol, dict):
        bind = vol.get("bind") or {}
        if bind.get("create_host_path") is False or bind.get("createHostPath") is False:
            ok("create_host_path remains false")
        else:
            bad("create_host_path remains false (got %s)" % bind)

forbidden_sources = ("/home/tendril", "/run/user", "docker.sock", ".tendril", "/var/run/docker")
blob = json.dumps(ui.get("volumes") or [])
for needle in forbidden_sources:
    if needle in blob:
        bad("no control-plane mount %s" % needle)
    else:
        ok("no control-plane mount %s" % needle)

if "host.docker.internal" in json.dumps(cfg):
    bad("rendered unix compose has no host.docker.internal")
else:
    ok("rendered unix compose has no host.docker.internal")

unix_blob = json.dumps(cfg)
if "/run/opentendril" in unix_blob:
    bad("/run/opentendril is absent from unix compose")
else:
    ok("/run/opentendril is absent from unix compose")

forced_ui = forced["services"]["ui"]
forced_env = forced_ui.get("environment") or {}
if isinstance(forced_env, list):
    forced_env = dict(item.split("=", 1) for item in forced_env)
if forced_env.get("STEM_TRANSPORT") == "unix":
    ok("STEM_TRANSPORT=tcp does not retarget --profile ui")
else:
    bad("STEM_TRANSPORT=tcp does not retarget --profile ui (got %r)" % forced_env.get("STEM_TRANSPORT"))
if "/var/lib/opentendril-transport" in json.dumps(forced_ui.get("volumes") or []):
    ok("forcing STEM_TRANSPORT=tcp keeps the unix transport mount")
else:
    bad("forcing STEM_TRANSPORT=tcp keeps the unix transport mount")

sys.exit(1 if errors else 0)
PY

echo "== Compose explicit TCP (--profile ui-tcp) =="

tcp_json="$(env -u STEM_TRANSPORT -u STEM_SOCKET -u STEM_PORT -u STEM_GATEWAY_PORT \
  -u UI_BIND -u UI_PORT STEM_HOST=10.255.255.254 \
  docker compose -f "$compose_file" --profile ui-tcp config --format json)"
tcp_nohost_json="$(compose_cmd --profile ui-tcp config --format json)"

python3 - "$tcp_json" "$tcp_nohost_json" <<'PY' || failed=1
import json, sys

cfg = json.loads(sys.argv[1])
empty = json.loads(sys.argv[2])
svc = cfg["services"]["ui-tcp"]
errors = []

def ok(msg):
    print("  PASS  " + msg)

def bad(msg):
    print("  FAIL  " + msg)
    errors.append(msg)

if "ui" in cfg.get("services", {}):
    bad("TCP profile does not include the unix service")
else:
    ok("TCP profile does not include the unix service")

if (svc.get("network_mode") or svc.get("networkMode")) == "host":
    bad("TCP profile has no host network")
else:
    ok("TCP profile has no host network")

if svc.get("read_only") is True or svc.get("readOnly") is True:
    ok("TCP profile keeps a read-only root filesystem")
else:
    bad("TCP profile keeps a read-only root filesystem")

cap_drop = svc.get("cap_drop") or svc.get("capDrop") or []
if "ALL" in cap_drop:
    ok("TCP profile drops all capabilities")
else:
    bad("TCP profile drops all capabilities")

env = svc.get("environment") or {}
if isinstance(env, list):
    env = dict(item.split("=", 1) for item in env)
if env.get("STEM_TRANSPORT") == "tcp":
    ok("TCP profile pins STEM_TRANSPORT=tcp")
else:
    bad("TCP profile pins STEM_TRANSPORT=tcp (got %r)" % env.get("STEM_TRANSPORT"))
if env.get("STEM_HOST") == "10.255.255.254":
    ok("TCP profile interpolates STEM_HOST")
else:
    bad("TCP profile interpolates STEM_HOST (got %r)" % env.get("STEM_HOST"))

blob = json.dumps(cfg)
if "/run/opentendril" in blob:
    bad("TCP compose does not require /run/opentendril")
else:
    ok("TCP compose does not require /run/opentendril")
if "/var/lib/opentendril-transport" in blob:
    bad("TCP compose does not require /var/lib/opentendril-transport")
else:
    ok("TCP compose does not require /var/lib/opentendril-transport")

binds = []
for vol in svc.get("volumes") or []:
    if isinstance(vol, str):
        binds.append(vol)
        continue
    if vol.get("type") == "bind":
        binds.append(vol)
if binds:
    bad("TCP profile has no Unix transport bind (got %s)" % binds)
else:
    ok("TCP profile has no Unix transport bind")

if "host.docker.internal" in blob:
    bad("TCP compose has no host.docker.internal")
else:
    ok("TCP compose has no host.docker.internal")

empty_env = empty["services"]["ui-tcp"].get("environment") or {}
if isinstance(empty_env, list):
    empty_env = dict(item.split("=", 1) for item in empty_env)
if empty_env.get("STEM_HOST") in ("", None):
    ok("TCP compose config allows empty STEM_HOST (enforced at start)")
else:
    bad("TCP compose config empty STEM_HOST (got %r)" % empty_env.get("STEM_HOST"))

sys.exit(1 if errors else 0)
PY

echo "== Compose TCP starts (host transport dir may exist) =="

if [ -e /var/lib/opentendril-transport ]; then
  pass "host /var/lib/opentendril-transport exists; TCP start must still ignore it"
else
  pass "host /var/lib/opentendril-transport is absent; TCP start must still succeed"
fi

tcp_up_log="${workdir}/tcp-up.log"
if env -u STEM_TRANSPORT -u STEM_SOCKET -u STEM_GATEWAY_PORT -u UI_BIND \
    STEM_HOST=10.255.255.254 STEM_PORT=9 UI_PORT=4179 \
    docker compose -f "$compose_file" -p "$tcp_project" --profile ui-tcp \
    up -d --no-build >"$tcp_up_log" 2>&1; then
  pass "TCP compose up succeeds regardless of host transport dir"
else
  fail "TCP compose up succeeds regardless of host transport dir"
  cat "$tcp_up_log"
fi

tcp_cid="$(docker compose -f "$compose_file" -p "$tcp_project" --profile ui-tcp ps -q ui-tcp 2>/dev/null || true)"
if [ -n "$tcp_cid" ] && [ "$(docker inspect -f '{{.State.Running}}' "$tcp_cid" 2>/dev/null || echo false)" = "true" ]; then
  pass "TCP container is running"
  mounts="$(docker inspect -f '{{json .Mounts}}' "$tcp_cid")"
  if printf '%s' "$mounts" | grep -Fq /run/opentendril; then
    fail "running TCP container has no /run/opentendril bind"
  else
    pass "running TCP container has no /run/opentendril bind"
  fi
  if printf '%s' "$mounts" | grep -Fq /var/lib/opentendril-transport; then
    fail "running TCP container has no /var/lib/opentendril-transport bind"
  else
    pass "running TCP container has no /var/lib/opentendril-transport bind"
  fi
  net="$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$tcp_cid")"
  if [ "$net" = "host" ]; then
    fail "running TCP container is not host-networked"
  else
    pass "running TCP container is not host-networked"
  fi
  upstreams="$(docker exec "$tcp_cid" cat /tmp/stem-upstreams.conf)"
  if printf '%s' "$upstreams" | grep -Fq "server 10.255.255.254:9;"; then
    pass "generated nginx upstreams use TCP only"
  else
    fail "generated nginx upstreams use TCP only"
    printf '%s\n' "$upstreams"
  fi
  if printf '%s' "$upstreams" | grep -Fq "unix:"; then
    fail "generated TCP upstreams contain no unix: server"
  else
    pass "generated TCP upstreams contain no unix: server"
  fi
else
  fail "TCP container is running"
fi

docker compose -f "$compose_file" -p "$tcp_project" --profile ui-tcp down --remove-orphans >/dev/null 2>&1 || true

echo "== Compose TCP requires STEM_HOST at start =="

nohost_log="$(mktemp)"
if env -u STEM_HOST -u STEM_TRANSPORT -u STEM_SOCKET -u STEM_PORT \
    -u STEM_GATEWAY_PORT -u UI_BIND -u UI_PORT \
    docker compose -f "$compose_file" -p "$tcp_project" --profile ui-tcp \
    run --rm --no-deps -T ui-tcp >"$nohost_log" 2>&1; then
  fail "TCP start without STEM_HOST fails"
else
  pass "TCP start without STEM_HOST fails"
fi
if grep -Fq "STEM_TRANSPORT=tcp requires STEM_HOST" "$nohost_log"; then
  pass "TCP start names the missing STEM_HOST"
else
  fail "TCP start names the missing STEM_HOST"
  cat "$nohost_log"
fi
if grep -Fq "unix:" "$nohost_log"; then
  fail "TCP start without STEM_HOST does not fall back to unix"
else
  pass "TCP start without STEM_HOST does not fall back to unix"
fi

echo "== Compose Unix fails closed on a guaranteed-missing transport path =="

# Isolated fixture: do not use the production
# /var/lib/opentendril-transport source, which may exist on a governed
# host. Prove create_host_path: false against a path that this script
# never creates.
missing_src="${workdir}/absent-transport"
unix_missing_file="${workdir}/missing-transport.yml"
unix_up_log="${workdir}/unix-missing-up.log"
cat > "$unix_missing_file" <<EOF
name: ${unix_missing_project}
services:
  ui:
    image: opentendril-ui
    profiles: ["ui"]
    volumes:
      - type: bind
        source: ${missing_src}
        target: /var/lib/opentendril-transport
        read_only: true
        bind:
          create_host_path: false
EOF
if [ -e "$missing_src" ]; then
  fail "fixture transport path stays absent (${missing_src})"
else
  pass "fixture transport path stays absent"
fi
if docker compose -f "$unix_missing_file" -p "$unix_missing_project" --profile ui \
    up -d --no-build >"$unix_up_log" 2>&1; then
  fail "unix compose up fails when the transport bind source is missing"
  docker compose -f "$unix_missing_file" -p "$unix_missing_project" --profile ui down --remove-orphans >/dev/null 2>&1 || true
else
  pass "unix compose up fails when the transport bind source is missing"
fi
if grep -Eiq 'absent-transport|bind|mount|no such file|does not exist' "$unix_up_log"; then
  pass "unix missing-dir error names the transport path or bind"
else
  fail "unix missing-dir error names the transport path or bind"
  cat "$unix_up_log"
fi
if grep -Fq "host.docker.internal" "$unix_up_log"; then
  fail "unix missing-dir error does not mention host.docker.internal"
else
  pass "unix missing-dir error does not mention host.docker.internal"
fi
if grep -Fq "0.0.0.0" "$unix_up_log"; then
  fail "unix missing-dir error does not recommend TERROIR_HOST=0.0.0.0"
else
  pass "unix missing-dir error does not recommend TERROIR_HOST=0.0.0.0"
fi
unix_cid="$(docker compose -f "$unix_missing_file" -p "$unix_missing_project" --profile ui ps -q ui 2>/dev/null || true)"
if [ -n "$unix_cid" ]; then
  fail "unix missing-dir up leaves no container"
else
  pass "unix missing-dir up leaves no container"
fi

echo "== nginx Unix-socket local mode =="

sock="${workdir}/stem.sock"
python3 - "$sock" <<'PY' &
import os, socket, sys, time
path = sys.argv[1]
if os.path.exists(path):
    os.unlink(path)
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(path)
s.listen(1)
while True:
    time.sleep(60)
PY
sock_holder=$!
for _ in $(seq 1 50); do
  [ -S "$sock" ] && break
  sleep 0.05
done
if [ ! -S "$sock" ]; then
  fail "could not create a test Unix socket at ${sock}"
  exit 1
fi

unix_out="${workdir}/unix-upstreams.conf"
if STEM_TRANSPORT=unix STEM_SOCKET="$sock" STEM_HOST=host.docker.internal \
    STEM_UPSTREAMS_CONF="$unix_out" sh "$transport_sh" >/dev/null; then
  pass "unix mode succeeds when the socket exists"
else
  fail "unix mode succeeds when the socket exists"
fi

assert_file_contains "$unix_out" "unix:${sock}" "unix upstream names the socket"
assert_file_lacks "$unix_out" "host.docker.internal" "unix mode ignores STEM_HOST"
assert_file_lacks "$unix_out" ":8080" "unix mode does not emit a TCP port"

rendered_unix="${workdir}/unix-nginx.conf"
cat "$unix_out" "$nginx_template" > "$rendered_unix"

assert_file_contains "$rendered_unix" "proxy_pass http://stem_api;" " /health and /v1 use stem_api"
assert_file_contains "$rendered_unix" "location = /health" "nginx defines /health"
assert_file_contains "$rendered_unix" "location /v1" "nginx defines /v1"
assert_file_contains "$rendered_unix" "location = /ws" "nginx defines /ws"
assert_file_contains "$rendered_unix" "proxy_pass http://stem_ws;" " /ws uses stem_ws"
assert_file_contains "$rendered_unix" "proxy_set_header Upgrade" " /ws upgrade is configured"
assert_file_contains "$rendered_unix" "proxy_set_header Connection" " /ws connection upgrade is configured"
assert_file_lacks "$rendered_unix" "proxy_set_header Authorization" "Authorization is passthrough (not set)"
assert_file_lacks "$rendered_unix" "BOTANIST" "nginx holds no Botanist key"
assert_file_lacks "$rendered_unix" "host.docker.internal" "unix nginx config has no host.docker.internal"
assert_file_contains "$rendered_unix" \
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; style-src-elem 'self'; style-src-attr 'unsafe-inline'" \
  "CSP is unchanged"

echo "== missing socket does not fall back to TCP =="

missing_out="${workdir}/missing-upstreams.conf"
missing_log="${workdir}/missing.log"
if STEM_TRANSPORT=unix STEM_SOCKET="${workdir}/absent.sock" \
    STEM_HOST=host.docker.internal STEM_PORT=8080 \
    STEM_UPSTREAMS_CONF="$missing_out" \
    sh "$transport_sh" >"$missing_log" 2>&1; then
  fail "missing socket fails closed"
else
  pass "missing socket fails closed"
fi
if [ -f "$missing_out" ]; then
  fail "missing socket does not write an upstream file"
else
  pass "missing socket does not write an upstream file"
fi
if grep -Fq "host.docker.internal" "$missing_log"; then
  fail "missing-socket error does not recommend host.docker.internal"
else
  pass "missing-socket error does not recommend host.docker.internal"
fi
if grep -Fq "0.0.0.0" "$missing_log"; then
  fail "missing-socket error does not recommend TERROIR_HOST=0.0.0.0"
else
  pass "missing-socket error does not recommend TERROIR_HOST=0.0.0.0"
fi
if grep -Fq "Greenhouse cannot reach the configured Stem transport" "$missing_log"; then
  pass "missing socket reports a transport failure"
else
  fail "missing socket reports a transport failure"
fi

echo "== explicit TCP selector script =="

tcp_out="${workdir}/tcp-upstreams.conf"
if STEM_TRANSPORT=tcp STEM_HOST=stem.example STEM_PORT=8080 STEM_GATEWAY_PORT=9090 \
    STEM_UPSTREAMS_CONF="$tcp_out" sh "$transport_sh" >/dev/null; then
  pass "explicit TCP mode renders"
else
  fail "explicit TCP mode renders"
fi
assert_file_contains "$tcp_out" "server stem.example:8080;" "TCP API upstream"
assert_file_contains "$tcp_out" "server stem.example:9090;" "TCP WS upstream"
assert_file_contains "$tcp_out" "server stem.example:8080 backup;" "TCP WS backup"
assert_file_lacks "$tcp_out" "unix:" "explicit TCP does not use a Unix socket"

tcp_nohost_log="${workdir}/tcp-nohost.log"
if STEM_TRANSPORT=tcp STEM_HOST= STEM_UPSTREAMS_CONF="${workdir}/tcp-nohost.conf" \
    sh "$transport_sh" >"$tcp_nohost_log" 2>&1; then
  fail "TCP mode without STEM_HOST fails"
else
  pass "TCP mode without STEM_HOST fails"
fi

echo "== governed systemd state-directory contract =="

guide="${root}/docs/GUIDE-INSTALL.md"
assert_file_contains "$guide" "StateDirectory=opentendril-transport" \
  "governed unit sets StateDirectory=opentendril-transport"
assert_file_contains "$guide" "StateDirectoryMode=0755" \
  "governed unit sets StateDirectoryMode=0755"
assert_file_contains "$guide" "Environment=TENDRIL_LOCAL_SOCKET=/var/lib/opentendril-transport/stem.sock" \
  "governed unit sets TENDRIL_LOCAL_SOCKET"
assert_file_lacks "$guide" "RuntimeDirectory=opentendril" \
  "obsolete RuntimeDirectory=opentendril transport contract is absent"
assert_file_lacks "$guide" "RuntimeDirectoryPreserve" \
  "RuntimeDirectoryPreserve is not required for this transport"
assert_file_lacks "$guide" "/run/opentendril" \
  "governed install no longer documents /run/opentendril"

echo "== Slice 2 defaults name the amended socket path =="

assert_file_contains "${root}/ui/Dockerfile" \
  "STEM_SOCKET=/var/lib/opentendril-transport/stem.sock" \
  "Docker image default STEM_SOCKET"
assert_file_lacks "${root}/ui/Dockerfile" "/run/opentendril" \
  "Docker image has no /run/opentendril fallback"
assert_file_contains "$transport_sh" \
  'SOCKET="${STEM_SOCKET:-/var/lib/opentendril-transport/stem.sock}"' \
  "nginx transport-selection default"
assert_file_lacks "$transport_sh" "/run/opentendril" \
  "nginx transport-selection has no /run/opentendril fallback"
assert_file_lacks "$compose_file" "/run/opentendril" \
  "Compose has no /run/opentendril mount or default"
assert_file_lacks "${root}/docs/GREENHOUSE.md" "/run/opentendril" \
  "GREENHOUSE.md has no /run/opentendril path"
assert_file_lacks "${root}/ui/README.md" "/run/opentendril" \
  "ui/README.md has no /run/opentendril path"

echo "== invalid transport is rejected =="
if STEM_TRANSPORT=magic STEM_UPSTREAMS_CONF="${workdir}/magic.conf" \
    sh "$transport_sh" >/dev/null 2>&1; then
  fail "invalid STEM_TRANSPORT is rejected"
else
  pass "invalid STEM_TRANSPORT is rejected"
fi

if [ "$failed" -ne 0 ]; then
  echo
  echo "Greenhouse transport checks failed."
  exit 1
fi
echo
echo "✅ Greenhouse local-socket transport checks passed."
