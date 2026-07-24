#!/bin/sh

# Hormonal Trigger: Network Security
# Runs at the Meristem before a Tendril is allowed to sprout.
# Arguments: $1 = Path to JSON payload containing 'genotype' and 'transcript'

PAYLOAD_FILE=$1

if [ ! -f "$PAYLOAD_FILE" ]; then
    echo "❌ [Meristem Failure]: Hormonal trigger could not locate the sprout payload at $PAYLOAD_FILE" >&2
    exit 1
fi

# Extract task using basic grep/awk to avoid depending on jq
TASK=$(grep -o '"task"\s*:\s*"[^"]*"' "$PAYLOAD_FILE" | sed 's/"task"\s*:\s*"//' | sed 's/"$//')

# Check for forbidden internal IP addresses in the task
if echo "$TASK" | grep -qE "127\.0\.0\.1|localhost|10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}"; then
    echo "❌ [Meristem Sprout Aborted]: The task requested targets an internal or loopback IP address." >&2
    echo "Hormonal Trigger (restrict-internal-network.sh) has blocked the creation of this Tendril to prevent Server-Side Request Forgery (SSRF)." >&2
    exit 1
fi

echo "✅ Hormonal Trigger Passed: Network targets appear safe."
exit 0
