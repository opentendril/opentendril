#!/usr/bin/env bash
# Prove the containerized Greenhouse's local Stem transport:
# default Compose mounts only /run/opentendril read-only, nginx unix mode
# routes /health /v1 /ws through the socket, TCP is explicit, and a missing
# socket does not fall back to TCP.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
transport_sh="${root}/ui/nginx/15-stem-transport.sh"
nginx_template="${root}/ui/nginx/default.conf.template"
compose_file="${root}/docker-compose.yml"
failed=0

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

echo "== Compose default local Greenhouse =="

# Isolate from a developer's shell STEM_* / UI_* values.
compose_env=(
  env -u STEM_TRANSPORT -u STEM_SOCKET -u STEM_HOST -u STEM_PORT
  -u STEM_GATEWAY_PORT -u UI_BIND -u UI_PORT
)
compose_json="$("${compose_env[@]}" docker compose --profile ui -f "$compose_file" config --format json)"

python3 - "$compose_json" <<'PY' || failed=1
import json, sys

cfg = json.loads(sys.argv[1])
ui = cfg["services"]["ui"]
errors = []

def ok(msg):
    print("  PASS  " + msg)

def bad(msg):
    print("  FAIL  " + msg)
    errors.append(msg)

# Host network is forbidden.
net = ui.get("network_mode") or ui.get("networkMode")
if net == "host":
    bad("host network mode is absent")
else:
    ok("host network mode is absent")

# extra_hosts must not inject host.docker.internal.
extra = ui.get("extra_hosts") or ui.get("extraHosts") or {}
if extra:
    bad("default extra_hosts is empty (got %s)" % extra)
else:
    ok("default extra_hosts is empty")

# Hardening stays in place.
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

# Loopback publication.
ports = ui.get("ports") or []
published = json.dumps(ports)
if "127.0.0.1" in published and "4173" in published:
    ok("loopback UI publication")
else:
    bad("loopback UI publication (got %s)" % ports)

# Environment: unix default, no host.docker.internal, no credentials.
env = ui.get("environment") or {}
if isinstance(env, list):
    env = dict(item.split("=", 1) for item in env)
transport = env.get("STEM_TRANSPORT", "")
socket = env.get("STEM_SOCKET", "")
host = env.get("STEM_HOST", "")
if transport == "unix":
    ok("STEM_TRANSPORT defaults to unix")
else:
    bad("STEM_TRANSPORT defaults to unix (got %r)" % transport)
if socket == "/run/opentendril/stem.sock":
    ok("STEM_SOCKET defaults to /run/opentendril/stem.sock")
else:
    bad("STEM_SOCKET default (got %r)" % socket)
if host in ("", None):
    ok("STEM_HOST is unset by default")
else:
    bad("STEM_HOST is unset by default (got %r)" % host)
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

# Bind mounts: only the dedicated runtime directory, read-only.
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

if len(binds) != 1:
    bad("only /run/opentendril is bind-mounted (got %s)" % binds)
else:
    vol = binds[0]
    if isinstance(vol, str):
        ok_mount = vol.startswith("/run/opentendril:/run/opentendril") and ":ro" in vol
        source, target, read_only = "/run/opentendril", "/run/opentendril", ":ro" in vol
    else:
        source = vol.get("source")
        target = vol.get("target")
        read_only = bool(vol.get("read_only") or vol.get("readOnly"))
        ok_mount = source == "/run/opentendril" and target == "/run/opentendril" and read_only
    if ok_mount:
        ok("mounts only /run/opentendril read-only")
    else:
        bad("mounts only /run/opentendril read-only (got %s)" % vol)
    if isinstance(vol, dict):
        bind = vol.get("bind") or {}
        if bind.get("create_host_path") is False or bind.get("createHostPath") is False:
            ok("missing host runtime path does not create /run/opentendril")
        else:
            bad("missing host runtime path does not create /run/opentendril (got %s)" % bind)

forbidden_sources = ("/home/tendril", "/run/user", "docker.sock", ".tendril", "/var/run/docker")
blob = json.dumps(ui.get("volumes") or [])
for needle in forbidden_sources:
    if needle in blob:
        bad("no control-plane mount %s" % needle)
    else:
        ok("no control-plane mount %s" % needle)

if "host.docker.internal" in json.dumps(cfg):
    bad("rendered compose has no host.docker.internal")
else:
    ok("rendered compose has no host.docker.internal")

sys.exit(1 if errors else 0)
PY

echo "== nginx Unix-socket local mode =="

workdir="$(mktemp -d)"
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
cleanup() {
  kill "$sock_holder" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT
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

# Combined rendered view: generated upstreams + the committed template.
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

echo "== explicit TCP mode =="

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
