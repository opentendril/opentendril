#!/usr/bin/env bash
# ============================================================================
# Tendril Node Identity Genesis Script
# ============================================================================
#
# Generates a cryptographic identity (GPG key) for this Tendril node so that
# every autonomous commit is verifiable. This is Phase 1 of the Federated Hive.
#
# Usage:
#   chmod +x scripts/generate-node-identity.sh
#   ./scripts/generate-node-identity.sh
#
# What it does:
#   1. Generates an Ed25519 GPG key pair (no passphrase, suitable for automation)
#   2. Configures git in this repository to sign all commits with the new key
#   3. Exports the public key so you can upload it to GitHub
#
# After running:
#   • Copy the exported public key block
#   • Go to https://github.com/settings/keys → "New GPG key"
#   • Paste the key and save
#   • All future commits from this node will show as "Verified" on GitHub
#
# ============================================================================

set -euo pipefail

# --- Configuration ---
NODE_NAME="${TENDRIL_NODE_NAME:-Tendril Node}"
NODE_EMAIL="${TENDRIL_NODE_EMAIL:-tendril@jurnx.com}"
KEY_COMMENT="Tendril Autonomous Agent — $(hostname)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

echo -e "${CYAN}${BOLD}"
echo "╔══════════════════════════════════════════════════════╗"
echo "║       Tendril Node Identity Genesis v1.0            ║"
echo "║       Cryptographic Commit Signing Setup            ║"
echo "╚══════════════════════════════════════════════════════╝"
echo -e "${NC}"

# --- Pre-flight checks ---
if ! command -v gpg &> /dev/null; then
    echo -e "${RED}✗ GPG is not installed. Install it first:${NC}"
    echo "  Ubuntu/Debian: sudo apt install gnupg"
    echo "  macOS:         brew install gnupg"
    exit 1
fi

echo -e "${YELLOW}Node Name:${NC}  $NODE_NAME"
echo -e "${YELLOW}Node Email:${NC} $NODE_EMAIL"
echo -e "${YELLOW}Hostname:${NC}   $(hostname)"
echo ""

# --- Check for existing Tendril keys ---
EXISTING_KEY=$(gpg --list-secret-keys --keyid-format long "$NODE_EMAIL" 2>/dev/null | grep -oP '(?<=sec\s{3}ed25519/)[A-F0-9]+' || true)

if [ -n "$EXISTING_KEY" ]; then
    echo -e "${YELLOW}⚠  An existing GPG key was found for ${NODE_EMAIL}: ${EXISTING_KEY}${NC}"
    echo ""
    read -p "Use existing key? [Y/n] " -n 1 -r
    echo ""
    if [[ $REPLY =~ ^[Nn]$ ]]; then
        echo -e "${CYAN}Generating a new key...${NC}"
    else
        GPG_KEY_ID="$EXISTING_KEY"
        echo -e "${GREEN}✓ Using existing key: ${GPG_KEY_ID}${NC}"
    fi
fi

# --- Generate new key if needed ---
if [ -z "${GPG_KEY_ID:-}" ]; then
    echo -e "${CYAN}Generating Ed25519 GPG key pair (no passphrase for automation)...${NC}"
    echo ""

    # Batch generation — no TTY interaction required
    gpg --batch --gen-key <<EOF
%no-protection
Key-Type: eddsa
Key-Curve: ed25519
Subkey-Type: eddsa
Subkey-Curve: ed25519
Name-Real: ${NODE_NAME}
Name-Email: ${NODE_EMAIL}
Name-Comment: ${KEY_COMMENT}
Expire-Date: 2y
%commit
EOF

    # Extract the key ID
    GPG_KEY_ID=$(gpg --list-secret-keys --keyid-format long "$NODE_EMAIL" 2>/dev/null \
        | grep -oP '(?<=sec\s{3}ed25519/)[A-F0-9]+' \
        | tail -1)

    if [ -z "$GPG_KEY_ID" ]; then
        echo -e "${RED}✗ Failed to generate GPG key. Check gpg output above.${NC}"
        exit 1
    fi

    echo -e "${GREEN}✓ Generated GPG key: ${GPG_KEY_ID}${NC}"
fi

# --- Configure Git ---
echo ""
echo -e "${CYAN}Configuring git for signed commits...${NC}"

# Set for this repository specifically (not global, so other repos are unaffected)
git config user.signingkey "$GPG_KEY_ID"
git config commit.gpgsign true
git config tag.gpgsign true
git config gpg.program gpg

# Set the commit identity to match the GPG key
git config user.name "$NODE_NAME"
git config user.email "$NODE_EMAIL"

echo -e "${GREEN}✓ Git configured:${NC}"
echo "    user.signingkey = $GPG_KEY_ID"
echo "    commit.gpgsign  = true"
echo "    tag.gpgsign     = true"
echo "    user.name       = $NODE_NAME"
echo "    user.email      = $NODE_EMAIL"

# --- Export Public Key ---
echo ""
echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════${NC}"
echo -e "${CYAN}${BOLD}  YOUR PUBLIC KEY (copy this to GitHub)${NC}"
echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════${NC}"
echo ""

PUBLIC_KEY=$(gpg --armor --export "$GPG_KEY_ID")
echo "$PUBLIC_KEY"

echo ""
echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════${NC}"

# Also save to a file for convenience
KEY_FILE="$(git rev-parse --show-toplevel)/tendril-node-$(hostname).pub"
echo "$PUBLIC_KEY" > "$KEY_FILE"
echo ""
echo -e "${GREEN}✓ Public key also saved to: ${KEY_FILE}${NC}"

# --- Print next steps ---
echo ""
echo -e "${BOLD}${GREEN}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${GREEN}║                    NEXT STEPS                        ║${NC}"
echo -e "${BOLD}${GREEN}╚══════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "  ${BOLD}1.${NC} Copy the public key block above"
echo -e "  ${BOLD}2.${NC} Go to ${CYAN}https://github.com/settings/keys${NC}"
echo -e "  ${BOLD}3.${NC} Click ${BOLD}\"New GPG key\"${NC}"
echo -e "  ${BOLD}4.${NC} Paste the key and save"
echo -e "  ${BOLD}5.${NC} (Optional) Enable ${BOLD}Vigilant Mode${NC} in GitHub settings"
echo -e "     to flag unsigned commits as \"Unverified\""
echo ""
echo -e "  ${YELLOW}For Docker deployments:${NC}"
echo -e "  Mount a GPG volume or inject the private key via secrets."
echo -e "  Set TENDRIL_NODE_NAME and TENDRIL_NODE_EMAIL as env vars."
echo ""
echo -e "${GREEN}${BOLD}Node identity genesis complete. All future commits will be signed.${NC}"
