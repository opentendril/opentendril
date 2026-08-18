#!/bin/sh
# Select the Greenhouse → Stem transport and write nginx upstreams.
# Default is the local Unix socket. TCP is explicit only; a missing
# socket never falls through to a TCP host.
set -eu

TRANSPORT=$(printf '%s' "${STEM_TRANSPORT:-unix}" | tr '[:upper:]' '[:lower:]')
SOCKET="${STEM_SOCKET:-/var/lib/opentendril-transport/stem.sock}"
OUT="${STEM_UPSTREAMS_CONF:-/tmp/stem-upstreams.conf}"

write_upstreams() {
    umask 022
    cat > "$OUT"
}

fail() {
    echo "ERROR: Greenhouse cannot reach the configured Stem transport. $*" >&2
    exit 1
}

case "$TRANSPORT" in
unix)
    case "$SOCKET" in
    /*) ;;
    *) fail "STEM_SOCKET must be an absolute path (got: $SOCKET)." ;;
    esac
    if [ ! -S "$SOCKET" ]; then
        fail "$SOCKET is missing or not a socket. Start the governed Stem first. STEM_TRANSPORT=tcp is only for a Stem that is intentionally reachable by TCP and is not selected when the socket is missing."
    fi
    write_upstreams <<EOF
upstream stem_api {
    server unix:${SOCKET};
}
upstream stem_ws {
    server unix:${SOCKET};
}
EOF
    echo "Greenhouse Stem transport: unix ${SOCKET}"
    ;;
tcp)
    if [ -z "${STEM_HOST:-}" ]; then
        fail "STEM_TRANSPORT=tcp requires STEM_HOST."
    fi
    PORT="${STEM_PORT:-8080}"
    GATEWAY="${STEM_GATEWAY_PORT:-9090}"
    write_upstreams <<EOF
upstream stem_api {
    server ${STEM_HOST}:${PORT};
}
upstream stem_ws {
    server ${STEM_HOST}:${GATEWAY};
    server ${STEM_HOST}:${PORT} backup;
}
EOF
    echo "Greenhouse Stem transport: tcp ${STEM_HOST}:${PORT} (ws ${STEM_HOST}:${GATEWAY})"
    ;;
*)
    fail "STEM_TRANSPORT must be unix or tcp (got: ${STEM_TRANSPORT:-})."
    ;;
esac
