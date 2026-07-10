# Macrophage fuzz-verification image (issue #154).
#
# Unlike sprouts/go/Dockerfile — which builds the tendril-go tool harness
# once and then discards the Go toolchain to hit a <20MB runtime budget
# (docs/DESIGN-STATELESS-SPROUT.md) — the Macrophage role needs `go test
# -fuzz` available at container *runtime*, so this image keeps the full Go
# toolchain. It is only ever used for the deterministic post-generation fuzz
# check the Conductor runs for a "macrophage" sequence step; every other Go
# sprout (Worker, Verifier, Debugger) still runs the minimal
# opentendril-go:latest image unchanged.
#
# No ENTRYPOINT/tool protocol here: the Conductor starts this container idle
# (see the CMD below) and execs `go test ...` into it directly via
# terrarium.Terrarium.Run, exactly like every other terrarium's exec calls.
FROM golang:1.24-alpine

RUN apk add --no-cache git ca-certificates

WORKDIR /app

CMD ["tail", "-f", "/dev/null"]
