#!/usr/bin/env bash
set -euo pipefail

instructions="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/AGENTS.md"

require_text() {
  local text="$1"
  if ! grep -Fq "$text" "${instructions}"; then
    echo "::error::AGENTS.md is missing required Pollinator Git guidance: ${text}"
    exit 1
  fi
}

require_text "A builder configured as an OpenTendril Pollinator with a governed Git surface MUST use OpenTendril"
require_text "do not use host Git for target-Substrate network or mutation commands"
require_text "do not use direct GitHub credentials"
require_text 'Do not use host `gh` for target-repository mutation or publication.'
require_text "Local read-only source inspection remains allowed."
require_text "If a required governed capability is unavailable, stop and report it rather than bypassing OpenTendril."
require_text "The Botanist retains merge authority."
require_text "### Conventional host-Git preflight (only outside Pollinator posture)"
require_text "This local host setup applies only outside OpenTendril Pollinator posture."
require_text "### gofmt pre-commit hook (conventional host-Git workflows only)"

if grep -Fq "opentendril-opentendril" "${instructions}"; then
  echo "::error::AGENTS.md must not hard-code a local Substrate name."
  exit 1
fi

if grep -Fq "Before starting work on ANY task, the builder MUST run this sequence" "${instructions}"; then
  echo "::error::AGENTS.md applies conventional host-Git preflight unconditionally."
  exit 1
fi

echo "Pollinator Git governance is correctly scoped in AGENTS.md."
