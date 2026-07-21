#!/usr/bin/env bash
# Decides which native CI jobs a set of changed paths requires.
#
# Reads changed paths on stdin (one per line) and writes the job flags to
# stdout as `name=true|false` lines, ready to append to $GITHUB_OUTPUT.
#
# The routing is FAIL CLOSED: a path that matches no rule below requires
# *every* job. This mirrors the change-scoped local verifier, which widens to
# the whole module whenever it cannot derive a change's scope — uncertainty
# resolves toward more verification, never less. An earlier version of this
# filter had no default case, so an unrecognized path selected no jobs at all
# and the gate reported green having run nothing; the paths it silently
# ignored included every sprout Dockerfile and the whole Stoma.
#
# Documentation is the one allowlisted exception, because it carries no
# executable surface. It is an explicit allowlist rather than a fallthrough
# precisely so that forgetting to classify something is safe.
#
# Usage: scripts/ci-path-filter.sh < changed-paths
set -euo pipefail

sprout_python=false
stem_go=false

while IFS= read -r path; do
    [ -n "$path" ] || continue

    # Note: in a bash `case`, `*` spans `/` — `cmd/*` covers cmd/**.
    case "$path" in
        # Known inert: no executable surface.
        docs/*|*.md|LICENSE)
            ;;

        # The gate's own definition: re-run everything it can gate on.
        .github/workflows/ci.yml|scripts/ci-path-filter.sh|scripts/ci-path-filter-test.sh|Makefile)
            sprout_python=true
            stem_go=true
            ;;

        # The Python sprout, including its image and packaging: the audit job
        # installs this lock file and scans it.
        sprouts/python/*)
            sprout_python=true
            ;;

        # The Go kernel. `cmd/*` (not `cmd/stem/*`) so cmd/stoma — the
        # microVM Stoma — is covered rather than silently unbuilt.
        cmd/*|roots/*|go.mod|go.sum)
            stem_go=true
            ;;

        # Fail closed: an unclassified path has an unknown blast radius.
        *)
            sprout_python=true
            stem_go=true
            ;;
    esac
done

echo "sprout-python=$sprout_python"
echo "stem-go=$stem_go"
