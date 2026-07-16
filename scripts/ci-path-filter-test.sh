#!/usr/bin/env bash
# Table-driven test for scripts/ci-path-filter.sh.
#
# The path filter decides whether the required merge gate runs any checks at
# all, so a silent mistake here is a silent false green. CI runs this before
# using the filter: if the routing is wrong, the gate fails instead of passing
# something it never verified.
#
# Usage: scripts/ci-path-filter-test.sh
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
filter="${script_dir}/ci-path-filter.sh"

failures=0

# check <description> <expected-sprout-python> <expected-stem-go> <path>...
check() {
    local description="$1" want_python="$2" want_go="$3"
    shift 3

    local actual
    actual="$(printf '%s\n' "$@" | bash "$filter")"

    local want
    want="$(printf 'sprout-python=%s\nstem-go=%s' "$want_python" "$want_go")"

    if [ "$actual" != "$want" ]; then
        echo "FAIL: ${description}"
        echo "  paths:    $*"
        echo "  expected: $(echo "$want" | tr '\n' ' ')"
        echo "  actual:   $(echo "$actual" | tr '\n' ' ')"
        failures=$((failures + 1))
    else
        echo "ok: ${description}"
    fi
}

#                                                      python  go
check "Go kernel change runs the Go job"                false  true   cmd/stem/internal/conductor/docker.go
check "nested Go path is covered by cmd/*"              false  true   cmd/stem/internal/terrarium/firecracker.go
check "guest agent runs the Go job"                     false  true   cmd/sprout-agent/main.go
check "roots change runs the Go job"                    false  true   roots/example/root.go
check "go.mod runs the Go job"                          false  true   go.mod
check "Python sprout source runs the audit"             true   false  sprouts/python/src/main.py
check "Python lock file runs the audit"                 true   false  sprouts/python/requirements.lock
check "Python sprout image runs the audit"              true   false  sprouts/python/Dockerfile
check "the workflow itself runs everything"             true   true   .github/workflows/ci.yml
check "the filter itself runs everything"               true   true   scripts/ci-path-filter.sh
check "Makefile runs everything"                        true   true   Makefile
check "documentation runs nothing"                      false  false  docs/DESIGN-STATELESS-SPROUT.md
check "top-level markdown runs nothing"                 false  false  README.md
check "nested markdown runs nothing"                    false  false  sprouts/python/README.md
check "no changes at all runs nothing"                  false  false  ""

# The regression this filter exists to prevent: each of these previously
# matched no rule, selected no jobs, and let the required gate report green.
check "verifier image is not silently inert"            true   true   sprouts/go-verifier/Dockerfile
check "sprout image is not silently inert"              true   true   sprouts/go/Dockerfile
check "sequence definitions are not silently inert"     true   true   .tendril/sequences/codex-delegate.yaml
check "unknown top-level file is not silently inert"    true   true   some-new-thing.yaml
check "scripts are not silently inert"                  true   true   scripts/pr-check.sh

# Mixed sets take the union, and an inert path never masks a live one.
check "mixed Go and Python change runs both"            true   true   cmd/stem/main.go sprouts/python/src/main.py
check "documentation alongside code still runs the job" false  true   docs/README.md cmd/stem/main.go
check "documentation alongside an unknown path"         true   true   docs/README.md sprouts/node/Dockerfile

if [ "$failures" -gt 0 ]; then
    echo
    echo "${failures} path-filter test(s) failed."
    exit 1
fi

echo
echo "All path-filter tests passed."
