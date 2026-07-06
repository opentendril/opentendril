#!/usr/bin/env bash
set -euo pipefail

# OpenTendril systemd installer — idempotent production service setup.

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "Error: This script must be run with sudo."
  echo "Usage: sudo ./install.sh"
  exit 1
fi

if [[ -z "${SUDO_USER:-}" || "${SUDO_USER}" == "root" ]]; then
  echo "Error: Could not determine the non-root invoking user."
  echo "Run this script from your user account: sudo ./install.sh"
  exit 1
fi

SERVICE_USER="${SUDO_USER}"
SERVICE_GROUP="$(id -gn "${SERVICE_USER}")"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY_DEST="/usr/local/bin/tendril"
SERVICE_NAME="opentendril"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
BUILD_OUTPUT="${REPO_ROOT}/tendril"

echo "==> OpenTendril installer"
echo "    User: ${SERVICE_USER}"
echo "    Repo: ${REPO_ROOT}"

echo "==> Building tendril binary as ${SERVICE_USER}..."
sudo -Hiu "${SERVICE_USER}" bash -c "cd '${REPO_ROOT}' && go build -o tendril ./cmd/stem"

if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
  echo "==> Stopping existing ${SERVICE_NAME} service..."
  systemctl stop "${SERVICE_NAME}"
fi

echo "==> Installing binary to ${BINARY_DEST}..."
install -m 755 "${BUILD_OUTPUT}" "${BINARY_DEST}"
rm -f "${BUILD_OUTPUT}"

echo "==> Writing systemd unit file..."
cat > "${SERVICE_FILE}" <<EOF
[Unit]
Description=OpenTendril Go Orchestrator

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_GROUP}
WorkingDirectory=${REPO_ROOT}
ExecStart=${BINARY_DEST} serve
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

echo "==> Reloading systemd and starting ${SERVICE_NAME}..."
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"
systemctl start "${SERVICE_NAME}"

echo ""
echo "OpenTendril installed and running as systemd service '${SERVICE_NAME}'."
echo ""
echo "View logs:"
echo "  journalctl -u ${SERVICE_NAME} -f"
echo ""
echo "Check status:"
echo "  systemctl status ${SERVICE_NAME}"